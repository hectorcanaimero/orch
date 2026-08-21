"""Live event tailer for the SSE endpoint.

Design notes:
    - `tail_events(state_dir, ...)` is a plain generator. FastAPI wraps it in
      an SSE response but the generator itself is testable in isolation.
    - We tail the NEWEST events-*.jsonl file (by mtime). When a fresher one
      appears (a new run starts), we switch to that file automatically.
    - Every yielded item is a formatted dict `{ts, task_id, event_type,
      backend, human, severity, raw}` so the template partial (`log_line.html`)
      is dumb and doesn't need to know the event schema.
    - No busy-loop: `poll_interval_s` sleep between reads. Default 0.5s.

Severity buckets drive the CSS class in the template:
    success  → "ok"      (green)
    fail     → "err"     (red)
    timeout  → "err"     (red)
    retry    → "warn"    (yellow)
    block    → "block"   (orange)
    dispatch → "info"    (blue)
    *        → "muted"   (gray)
"""

from __future__ import annotations

import json
import time
from collections.abc import Iterable, Iterator
from pathlib import Path
from typing import Any


_SEVERITY_MAP: dict[str, str] = {
    "success": "ok",
    "fail": "err",
    "timeout": "err",
    "retry": "warn",
    "block": "block",
    "dispatch": "info",
    "escalate": "warn",
    "resume_adopt": "info",
    "resume_revert": "warn",
    "id_spoof_detected": "err",
    "flock_contention": "err",
    "reconciled": "info",
}

_ICON_MAP: dict[str, str] = {
    "success": "OK",
    "fail": "FAIL",
    "timeout": "TIMEOUT",
    "retry": "RETRY",
    "block": "BLOCK",
    "dispatch": "->",
    "escalate": "ESC",
    "resume_adopt": "ADOPT",
    "resume_revert": "REVERT",
    "id_spoof_detected": "SPOOF",
    "flock_contention": "LOCK",
    "reconciled": "RECON",
}


def format_event(ev: dict) -> dict[str, Any]:
    """Turn a raw event dict into the shape the template renders.

    Never raises — a totally malformed event still produces something
    displayable (with severity `"muted"`).
    """
    etype = str(ev.get("event_type", "?"))
    task_id = str(ev.get("task_id", "?"))
    backend = str(ev.get("backend", "") or "")
    ts = str(ev.get("ts", ""))
    hhmmss = ts[11:19] if len(ts) >= 19 else ts
    extra = ev.get("extra") or {}
    severity = _SEVERITY_MAP.get(etype, "muted")
    icon = _ICON_MAP.get(etype, "*")

    # Short human-readable tail:
    #   dispatch → " → claude-sonnet-4-6"
    #   success  → " (12m 34s, $0.21)"
    #   fail     → " (retry 2/3)" or " (fatal)"
    #   block    → " (dep X-42 not done)"
    tail = ""
    if etype == "dispatch":
        model = extra.get("cli_model") or ""
        attempt = extra.get("attempt")
        tail = f" -> {model}" if model else ""
        if attempt and int(attempt) > 1:
            tail += f" (attempt {attempt})"
    elif etype == "success":
        dur = _fmt_duration(extra.get("duration_s"))
        cost = extra.get("cost_usd")
        cost_s = f", ${float(cost):.3f}" if _is_num(cost) else ""
        tail = f" ({dur}{cost_s})" if dur or cost_s else ""
    elif etype in {"fail", "timeout"}:
        attempt = extra.get("attempt")
        max_a = extra.get("max_attempts")
        if attempt and max_a:
            tail = f" (retry {attempt}/{max_a})"
        elif attempt:
            tail = f" (attempt {attempt})"
    elif etype == "block":
        reason = extra.get("reason") or extra.get("blocked_dep") or ""
        tail = f" ({reason})" if reason else ""
    elif etype == "retry":
        attempt = extra.get("attempt")
        tail = f" (attempt {attempt})" if attempt else ""

    human = f"[{hhmmss}] {icon} {task_id} {etype}{tail}"
    return {
        "ts": ts,
        "task_id": task_id,
        "event_type": etype,
        "backend": backend,
        "human": human,
        "severity": severity,
        "raw": ev,
    }


def _fmt_duration(dur: Any) -> str:
    """`612.4` → `10m 12s`, `45.2` → `45s`, missing → `""`."""
    if not _is_num(dur):
        return ""
    total = int(float(dur))
    if total < 60:
        return f"{total}s"
    m, s = divmod(total, 60)
    if m < 60:
        return f"{m}m {s:02d}s"
    h, m = divmod(m, 60)
    return f"{h}h {m:02d}m"


def _is_num(x: Any) -> bool:
    try:
        float(x)
        return True
    except (TypeError, ValueError):
        return False


def load_recent_events(state_dir: Path, limit: int = 100) -> list[dict[str, Any]]:
    """Return the last `limit` formatted events across all events-*.jsonl.

    Used by the initial page load of `/logs` — the SSE stream then appends
    live events as they arrive.
    """
    if not state_dir.exists():
        return []
    all_events: list[dict] = []
    for path in state_dir.glob("events-*.jsonl"):
        try:
            with path.open("r", encoding="utf-8") as fh:
                for line in fh:
                    line = line.strip()
                    if not line:
                        continue
                    try:
                        obj = json.loads(line)
                    except (json.JSONDecodeError, ValueError):
                        continue
                    if isinstance(obj, dict):
                        all_events.append(obj)
        except OSError:
            continue
    all_events.sort(key=lambda e: str(e.get("ts", "")))
    tail = all_events[-limit:]
    return [format_event(e) for e in tail]


def newest_events_file(state_dir: Path) -> Path | None:
    """Return the most recent `events-*.jsonl` file by mtime, or None."""
    if not state_dir.exists():
        return None
    candidates = list(state_dir.glob("events-*.jsonl"))
    if not candidates:
        return None
    return max(candidates, key=lambda p: p.stat().st_mtime)


def tail_events(
    state_dir: Path,
    poll_interval_s: float = 0.5,
    task_id_filter: str | None = None,
    max_iterations: int | None = None,
) -> Iterator[dict[str, Any]]:
    """Yield newly-appended events forever (until the client disconnects).

    Parameters:
        state_dir       — where events-*.jsonl files live
        poll_interval_s — sleep between polls
        task_id_filter  — if set, only yield events with matching `task_id`
        max_iterations  — TEST HOOK: cap total loop iterations before exit.
                          `None` means run forever (the real server behavior).
    """
    current_path: Path | None = None
    current_pos: int = 0
    iterations = 0

    while True:
        if max_iterations is not None:
            iterations += 1
            if iterations > max_iterations:
                return

        newest = newest_events_file(state_dir)
        if newest is None:
            time.sleep(poll_interval_s)
            continue

        # Switch to a newer file if a fresh run started.
        if current_path != newest:
            current_path = newest
            current_pos = 0

        try:
            with current_path.open("r", encoding="utf-8") as fh:
                fh.seek(current_pos)
                for line in fh:
                    line = line.rstrip("\n")
                    if not line.strip():
                        continue
                    try:
                        obj = json.loads(line)
                    except (json.JSONDecodeError, ValueError):
                        continue
                    if not isinstance(obj, dict):
                        continue
                    if task_id_filter and str(obj.get("task_id")) != task_id_filter:
                        continue
                    yield format_event(obj)
                current_pos = fh.tell()
        except OSError:
            time.sleep(poll_interval_s)
            continue

        time.sleep(poll_interval_s)


def sse_frame(payload: dict[str, Any], event: str = "message") -> str:
    """Format a single Server-Sent Events frame from a JSON-serializable dict."""
    data = json.dumps(payload, separators=(",", ":"))
    return f"event: {event}\ndata: {data}\n\n"
