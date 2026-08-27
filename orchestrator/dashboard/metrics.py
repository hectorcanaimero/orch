"""Metrics aggregation for the dashboard.

Everything in this module is READ-ONLY and pure Python. No FastAPI, no
network, no filesystem writes. Consumes `Task` objects (from `state.load_tasks`),
event JSONL rows (from `events-*.jsonl`), and spend JSONL rows (from
`spend-*.jsonl`).

The main aggregations:
    - `human_hours_by_task(events)` — per-task wall time from dispatch→success/fail
    - `metrics_by_model(spends, tasks, pricing)` — token + cost + task-count table
    - `metrics_by_day(spends, days, pricing)` — bucketed by UTC date
    - `parallelizable_tasks(tasks)` — task ids whose deps are all done AND
      themselves are still `backlog`/`todo`
    - `project_summary(tasks)` — one-shot header stats (done/in-progress/…)
    - `read_all_events(state_dir)` / `read_all_spends(state_dir)` — file
      scanners, tolerant of malformed lines
"""

from __future__ import annotations

import json
from collections import defaultdict
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import TYPE_CHECKING, Any, Iterable

from orchestrator.models import Task
from orchestrator.dashboard.pricing import PricingTable

if TYPE_CHECKING:
    from orchestrator.budget import BudgetConfig


# ---- File scanners ---------------------------------------------------------


def read_all_events(state_dir: Path) -> list[dict]:
    """Slurp every `events-*.jsonl` file under `state_dir` into a list.

    Sort order: by `ts` ascending (chronological). Malformed lines and
    non-dict payloads are dropped without raising. Missing dir → empty list.

    Sprint B: if `orch.db` is present alongside the JSONL files, its rows
    are merged in too (so a project mid-migration surfaces both sources).
    """
    events: list[dict] = []
    if state_dir.exists():
        for path in sorted(state_dir.glob("events-*.jsonl")):
            events.extend(_read_jsonl_dicts(path))
    events.extend(_read_events_from_sqlite(state_dir))
    events.sort(key=lambda e: str(e.get("ts", "")))
    return events


def read_all_spends(state_dir: Path) -> list[dict]:
    """Slurp every `spend-*.jsonl` file under `state_dir`. Chronological.

    Sprint B: if `orch.db` is present it's read alongside the JSONL files.
    """
    spends: list[dict] = []
    if state_dir.exists():
        for path in sorted(state_dir.glob("spend-*.jsonl")):
            spends.extend(_read_jsonl_dicts(path))
    spends.extend(_read_spends_from_sqlite(state_dir))
    spends.sort(key=lambda s: str(s.get("ts", "")))
    return spends


def _read_events_from_sqlite(state_dir: Path) -> list[dict]:
    """Yield event rows from `<state_dir>/orch.db`, if the DB exists.

    Missing DB → empty list. Never raises past its own boundary — the
    dashboard tolerates a broken DB by silently falling back to JSONL.
    """
    db_path = state_dir / "orch.db"
    if not db_path.exists():
        return []
    try:
        import sqlite3

        conn = sqlite3.connect(str(db_path))
        conn.row_factory = sqlite3.Row
        try:
            cur = conn.execute(
                "SELECT id, project_id, run_id, event_type, task_id, "
                "backend, ts, extra_json FROM events ORDER BY ts ASC"
            )
            out: list[dict] = []
            for row in cur.fetchall():
                try:
                    extra = json.loads(row["extra_json"] or "{}")
                except (json.JSONDecodeError, ValueError):
                    extra = {}
                out.append({
                    "id": row["id"],
                    "event_type": row["event_type"],
                    "task_id": row["task_id"],
                    "backend": row["backend"] or "",
                    "ts": row["ts"],
                    "extra": extra,
                    "project_id": row["project_id"],
                    "run_id": row["run_id"],
                })
            return out
        finally:
            conn.close()
    except Exception:  # noqa: BLE001 — dashboard read must not crash
        return []


def _read_spends_from_sqlite(state_dir: Path) -> list[dict]:
    db_path = state_dir / "orch.db"
    if not db_path.exists():
        return []
    try:
        import sqlite3

        conn = sqlite3.connect(str(db_path))
        conn.row_factory = sqlite3.Row
        try:
            cur = conn.execute(
                "SELECT ts, task_id, backend, model, tokens_in, tokens_out, "
                "cost_usd, duration_s, project_id FROM spend ORDER BY ts ASC"
            )
            return [dict(row) for row in cur.fetchall()]
        finally:
            conn.close()
    except Exception:  # noqa: BLE001
        return []


def _read_jsonl_dicts(path: Path) -> list[dict]:
    """Read a JSONL file, silently dropping malformed lines. Empty on error."""
    out: list[dict] = []
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
                    out.append(obj)
    except OSError:
        return []
    return out


# ---- Human hours -----------------------------------------------------------


def _parse_ts(ts: str) -> datetime | None:
    """Parse `2026-08-21T12:34:56Z` → aware UTC datetime. None on failure."""
    if not ts:
        return None
    try:
        # `.strptime` doesn't grok `Z` in Python < 3.11 without fromisoformat.
        # We use fromisoformat via a small swap for robustness.
        return datetime.fromisoformat(ts.replace("Z", "+00:00"))
    except (ValueError, TypeError):
        return None


def human_hours_by_task(events: Iterable[dict]) -> dict[str, float]:
    """Sum wall-clock hours spent per task from `dispatch → success/fail` pairs.

    Strategy: iterate events chronologically per task; on `dispatch`, stash the
    ts; on the next terminal event (`success`, `fail`, `timeout`), close the
    interval. Multiple retries stack additively.

    When `extra.duration_s` is present on the terminal event (the orchestrator
    records it), we PREFER that over the wall-clock delta: it excludes queue
    time and is more accurate. Falls back to `terminal_ts - dispatch_ts` when
    duration is missing/invalid.

    Returns a dict `{task_id: hours}` with 1 decimal of implicit precision
    (callers should round for display).
    """
    per_task: dict[str, list[dict]] = defaultdict(list)
    for ev in events:
        tid = ev.get("task_id")
        if tid and tid != "-":
            per_task[str(tid)].append(ev)

    hours: dict[str, float] = {}
    terminal = {"success", "fail", "timeout"}

    for tid, evs in per_task.items():
        evs.sort(key=lambda e: str(e.get("ts", "")))
        total_seconds = 0.0
        pending_dispatch_ts: datetime | None = None
        for ev in evs:
            etype = ev.get("event_type")
            if etype == "dispatch":
                pending_dispatch_ts = _parse_ts(str(ev.get("ts", "")))
            elif etype in terminal:
                # Prefer recorded duration if present and > 0.
                extra = ev.get("extra") or {}
                dur = extra.get("duration_s")
                try:
                    dur_f = float(dur) if dur is not None else 0.0
                except (TypeError, ValueError):
                    dur_f = 0.0
                if dur_f > 0:
                    total_seconds += dur_f
                    pending_dispatch_ts = None
                    continue
                if pending_dispatch_ts is not None:
                    term_ts = _parse_ts(str(ev.get("ts", "")))
                    if term_ts is not None:
                        delta = (term_ts - pending_dispatch_ts).total_seconds()
                        if delta > 0:
                            total_seconds += delta
                    pending_dispatch_ts = None
        if total_seconds > 0:
            hours[tid] = round(total_seconds / 3600.0, 3)
    return hours


def last_updated_by_task(events: Iterable[dict]) -> dict[str, str]:
    """Latest event `ts` per task_id. Used for the "última actualización" col."""
    last: dict[str, str] = {}
    for ev in events:
        tid = ev.get("task_id")
        ts = str(ev.get("ts", ""))
        if not tid or tid == "-" or not ts:
            continue
        prev = last.get(str(tid), "")
        if ts > prev:
            last[str(tid)] = ts
    return last


# ---- Per-model / per-day aggregations --------------------------------------


@dataclass(frozen=True, slots=True)
class ModelStats:
    model: str
    tasks_total: int
    tokens_in: int
    tokens_out: int
    cost_usd: float

    def as_dict(self) -> dict[str, Any]:
        return {
            "model": self.model,
            "tasks_total": self.tasks_total,
            "tokens_in": self.tokens_in,
            "tokens_out": self.tokens_out,
            "cost_usd": round(self.cost_usd, 4),
        }


@dataclass(frozen=True, slots=True)
class DayStats:
    date: str  # YYYY-MM-DD
    tokens_in: int
    tokens_out: int
    cost_usd: float
    tasks_total: int

    def as_dict(self) -> dict[str, Any]:
        return {
            "date": self.date,
            "tokens_in": self.tokens_in,
            "tokens_out": self.tokens_out,
            "cost_usd": round(self.cost_usd, 4),
            "tasks_total": self.tasks_total,
        }


def metrics_by_model(
    spends: Iterable[dict],
    pricing: PricingTable,
) -> list[ModelStats]:
    """Aggregate spend rows by `model`. Uses pricing fallback when cost==0.

    "tasks_total" counts DISTINCT `task_id`s that touched the model (a task
    with 2 retries still counts once). Sorted by cost descending — most
    expensive first.
    """
    rows: dict[str, dict[str, Any]] = defaultdict(lambda: {
        "tokens_in": 0,
        "tokens_out": 0,
        "cost_usd": 0.0,
        "task_ids": set(),
    })
    for s in spends:
        model = str(s.get("model") or "unknown")
        ti = int(s.get("tokens_in", 0) or 0)
        to = int(s.get("tokens_out", 0) or 0)
        cost = pricing.resolve_cost(
            recorded_cost=s.get("cost_usd"),
            model=model,
            tokens_in=ti,
            tokens_out=to,
        )
        r = rows[model]
        r["tokens_in"] += ti
        r["tokens_out"] += to
        r["cost_usd"] += cost
        tid = s.get("task_id")
        if tid:
            r["task_ids"].add(tid)

    stats = [
        ModelStats(
            model=name,
            tasks_total=len(r["task_ids"]),
            tokens_in=r["tokens_in"],
            tokens_out=r["tokens_out"],
            cost_usd=r["cost_usd"],
        )
        for name, r in rows.items()
    ]
    stats.sort(key=lambda m: (-m.cost_usd, m.model))
    return stats


def metrics_by_day(
    spends: Iterable[dict],
    pricing: PricingTable,
    days: int = 14,
) -> list[DayStats]:
    """Bucket spend rows by UTC date. Newest first, up to `days` rows.

    A day appears in the result ONLY if there was at least one spend row on
    that date. We don't backfill empty days — the table looks cleaner and the
    Alpine.js chart hooks can add zero-fill if they need it later.
    """
    per_day: dict[str, dict[str, Any]] = defaultdict(lambda: {
        "tokens_in": 0,
        "tokens_out": 0,
        "cost_usd": 0.0,
        "task_ids": set(),
    })
    for s in spends:
        ts = str(s.get("ts", ""))
        # ts looks like 2026-08-21T12:34:56Z — the first 10 chars are the date.
        if len(ts) < 10:
            continue
        day = ts[:10]
        model = str(s.get("model") or "unknown")
        ti = int(s.get("tokens_in", 0) or 0)
        to = int(s.get("tokens_out", 0) or 0)
        cost = pricing.resolve_cost(
            recorded_cost=s.get("cost_usd"),
            model=model,
            tokens_in=ti,
            tokens_out=to,
        )
        r = per_day[day]
        r["tokens_in"] += ti
        r["tokens_out"] += to
        r["cost_usd"] += cost
        tid = s.get("task_id")
        if tid:
            r["task_ids"].add(tid)

    stats = [
        DayStats(
            date=day,
            tokens_in=r["tokens_in"],
            tokens_out=r["tokens_out"],
            cost_usd=r["cost_usd"],
            tasks_total=len(r["task_ids"]),
        )
        for day, r in per_day.items()
    ]
    stats.sort(key=lambda d: d.date, reverse=True)
    return stats[:days]


def total_cost(spends: Iterable[dict], pricing: PricingTable) -> float:
    """Sum of resolved cost across every spend row (fallback aware)."""
    total = 0.0
    for s in spends:
        total += pricing.resolve_cost(
            recorded_cost=s.get("cost_usd"),
            model=str(s.get("model") or "default"),
            tokens_in=int(s.get("tokens_in", 0) or 0),
            tokens_out=int(s.get("tokens_out", 0) or 0),
        )
    return round(total, 4)


# ---- Task-graph helpers ----------------------------------------------------


def parallelizable_tasks(tasks: Iterable[Task]) -> list[Task]:
    """Return tasks whose deps are all `done` AND themselves are backlog/todo.

    This answers the operator question "what can I launch RIGHT NOW without
    waiting on anything?". Blocked / in-progress / done tasks are excluded.
    """
    by_id: dict[str, Task] = {t.id: t for t in tasks}
    ready: list[Task] = []
    for t in by_id.values():
        if t.status not in {"backlog", "todo"}:
            continue
        deps_ok = True
        for dep_id in t.dependencies:
            dep = by_id.get(dep_id)
            if dep is None or dep.status != "done":
                deps_ok = False
                break
        if deps_ok:
            ready.append(t)
    ready.sort(key=lambda t: (t.phase, t.id))
    return ready


@dataclass(frozen=True, slots=True)
class ProjectSummary:
    total: int
    done: int
    in_progress: int
    blocked: int
    backlog: int  # backlog + todo — the operator doesn't care about the split
    percent_done: float
    estimate_hours_total: float

    def as_dict(self) -> dict[str, Any]:
        return {
            "total": self.total,
            "done": self.done,
            "in_progress": self.in_progress,
            "blocked": self.blocked,
            "backlog": self.backlog,
            "percent_done": round(self.percent_done, 1),
            "estimate_hours_total": round(self.estimate_hours_total, 1),
        }


def project_summary(tasks: Iterable[Task]) -> ProjectSummary:
    """One-shot counts for the header bar."""
    total = 0
    done = 0
    in_progress = 0
    blocked = 0
    backlog = 0
    est = 0.0
    for t in tasks:
        total += 1
        est += float(t.estimate_hours or 0)
        if t.status == "done":
            done += 1
        elif t.status == "in-progress":
            in_progress += 1
        elif t.status == "blocked":
            blocked += 1
        else:
            backlog += 1
    pct = (done / total * 100.0) if total else 0.0
    return ProjectSummary(
        total=total,
        done=done,
        in_progress=in_progress,
        blocked=blocked,
        backlog=backlog,
        percent_done=pct,
        estimate_hours_total=est,
    )


def downstream_impact(tasks: Iterable[Task]) -> dict[str, int]:
    """Return `{task_id: N}` — count of tasks (transitively) waiting on it.

    Only counts NOT-YET-DONE downstream tasks — a completed task no longer
    "waits" on anything, so it doesn't inflate the score of its upstream
    once the chain has drained. This makes the ranking useful for the
    operator: "unblock this and N tasks become reachable".

    O(V+E) via a single reverse-adjacency build + BFS per task.
    """
    tasks_list = list(tasks)
    by_id: dict[str, Task] = {t.id: t for t in tasks_list}
    # Reverse graph: dep_id -> {tasks that depend on dep_id}
    reverse: dict[str, set[str]] = defaultdict(set)
    for t in tasks_list:
        for dep in t.dependencies:
            reverse[dep].add(t.id)

    out: dict[str, int] = {}
    for t in tasks_list:
        # Seed with `t.id` so a cyclic graph (A → B → A) can't re-count
        # the starting task as its own descendant. Tolerated as a defensive
        # guard even though tasks.json is DAG-shaped by contract.
        seen: set[str] = {t.id}
        stack = list(reverse.get(t.id, ()))
        while stack:
            nxt = stack.pop()
            if nxt in seen:
                continue
            seen.add(nxt)
            # Always traverse further — even through done tasks — so we can
            # reach pending descendants below completed intermediate nodes.
            stack.extend(reverse.get(nxt, ()))
        # Count only pending downstream. Done tasks are transit-only:
        # they've already been unblocked so they don't "wait" on us.
        seen.discard(t.id)  # don't count the task itself
        out[t.id] = sum(1 for tid in seen
                        if by_id.get(tid) and by_id[tid].status != "done")
    return out


def events_for_task(events: Iterable[dict], task_id: str,
                    limit: int = 200) -> list[dict]:
    """Filter events to a single task_id, keep last `limit`, chronological.

    Returned rows are the raw event dicts (already sorted by `ts` in
    `read_all_events`). The template does the presentation.
    """
    matches = [e for e in events if e.get("task_id") == task_id]
    if len(matches) > limit:
        matches = matches[-limit:]
    return matches


def critical_path(tasks: Iterable[Task]) -> set[str]:
    """Return the set of task ids that lie on the longest weighted path.

    Weight per node = `estimate_hours` (fallback to 1 when 0 so the graph
    doesn't collapse for un-estimated projects). Direction follows
    `dependencies` — a task's estimated wall time accumulates AFTER its
    upstream deps finish.

    Uses Kahn-style topological order + DP. O(V+E). Silent on cycles
    (returns whatever it computed before the cycle stalled progress).
    """
    tasks_list = list(tasks)
    if not tasks_list:
        return set()
    by_id: dict[str, Task] = {t.id: t for t in tasks_list}

    def _w(t: Task) -> float:
        return float(t.estimate_hours) if t.estimate_hours else 1.0

    indeg: dict[str, int] = {t.id: len(t.dependencies) for t in tasks_list}
    forward: dict[str, list[str]] = defaultdict(list)
    for t in tasks_list:
        for dep in t.dependencies:
            forward[dep].append(t.id)

    dist: dict[str, float] = {}
    parent: dict[str, str | None] = {}
    ready = [tid for tid, d in indeg.items() if d == 0]
    while ready:
        tid = ready.pop()
        t = by_id[tid]
        best = _w(t)
        best_parent: str | None = None
        for dep in t.dependencies:
            if dep in dist:
                cand = dist[dep] + _w(t)
                if cand > best:
                    best = cand
                    best_parent = dep
        dist[tid] = best
        parent[tid] = best_parent
        for nxt in forward.get(tid, ()):
            indeg[nxt] -= 1
            if indeg[nxt] == 0:
                ready.append(nxt)

    if not dist:
        return set()
    end = max(dist, key=lambda k: dist[k])
    path: set[str] = set()
    cur: str | None = end
    while cur is not None:
        path.add(cur)
        cur = parent.get(cur)
    return path


def orphan_dependencies(tasks: Iterable[Task]) -> list[dict[str, str]]:
    """Return `[{task_id, missing_dep_id}, ...]` for deps pointing at unknown ids.

    A healthy `tasks.json` has none. Anything here signals a typo, a
    deleted task, or an incomplete import.
    """
    tasks_list = list(tasks)
    known = {t.id for t in tasks_list}
    out: list[dict[str, str]] = []
    for t in tasks_list:
        for dep in t.dependencies:
            if dep not in known:
                out.append({"task_id": t.id, "missing_dep_id": dep})
    return out


def burndown_by_day(events: Iterable[dict], days: int = 14) -> list[dict[str, Any]]:
    """Return `[{date, done_count}, ...]` for the last `days` UTC days.

    A task counts as "done" the day its `exit_ok` event landed. Multiple
    exit_ok events for the same task (retries after a fail then success)
    count once — the earliest wins.
    """
    from datetime import datetime, timezone, timedelta

    earliest_ok: dict[str, str] = {}
    for e in events:
        if e.get("event_type") != "exit_ok":
            continue
        tid = e.get("task_id")
        ts = str(e.get("ts", ""))
        if not tid or not ts:
            continue
        prev = earliest_ok.get(tid)
        if prev is None or ts < prev:
            earliest_ok[tid] = ts

    # Bucket by UTC date.
    per_day: dict[str, int] = defaultdict(int)
    for ts in earliest_ok.values():
        per_day[ts[:10]] += 1

    today = datetime.now(timezone.utc).date()
    out: list[dict[str, Any]] = []
    for i in range(days - 1, -1, -1):
        d = today - timedelta(days=i)
        key = d.isoformat()
        out.append({"date": key, "done_count": per_day.get(key, 0)})
    return out


def round_up_to_step(value: float, step: float = 0.5) -> float:
    """Round `value` UP to the nearest multiple of `step` (default $0.50).

    Sprint E-2 uses this on the stakeholder view so displayed spend can't
    accidentally under-report to the person we're briefing. Cheap + no
    edge-case surprises because we snap non-negative floats.
    """
    if step <= 0:
        return value
    import math
    if value <= 0:
        return 0.0
    return math.ceil(value / step) * step


def eta_hours_remaining(
    tasks: Iterable[Task],
    human_hours_by_id: dict[str, float] | None = None,
) -> float | None:
    """Estimate remaining wall-clock hours from tasks + past pace.

    Returns:
        `None` when there's no signal (no done tasks or zero recorded
        human hours) — the template renders `—` instead of misleading 0.

    Rationale:
        - Sum `estimate_hours` for non-done tasks → the plan's guess of
          what remains.
        - Compute the ratio of actual human hours spent on DONE tasks
          against the plan's estimate for those same tasks (`efficiency`).
          When people took 1.5x the estimate on average, remaining
          estimates should also be scaled by 1.5.
        - Fall back to raw remaining estimate when we can't compute a
          ratio (no completions yet or zero human hours recorded).
    """
    tasks_list = list(tasks)
    remaining_est = sum(
        float(t.estimate_hours or 0.0)
        for t in tasks_list
        if t.status != "done"
    )
    if remaining_est <= 0:
        return None
    hours = human_hours_by_id or {}
    done_est = 0.0
    done_actual = 0.0
    for t in tasks_list:
        if t.status != "done":
            continue
        done_est += float(t.estimate_hours or 0.0)
        done_actual += float(hours.get(t.id, 0.0))
    if done_est <= 0 or done_actual <= 0:
        return remaining_est
    ratio = done_actual / done_est
    return remaining_est * ratio


def milestone_eta(
    remaining: int,
    velocity_per_day: float,
    today: str,
    target_date: str | None = None,
) -> dict | None:
    """Project a completion date for one milestone from task throughput.

    tasks-based: `eta_days = ceil(remaining / velocity_per_day)`. `today` is an
    ISO date string (injected for testability — no clock read inside). Returns
    None when there's no signal (nothing remaining, or zero velocity) — the UI
    renders '—' instead of a misleading date.

    confidence:
      - "high" → eta lands on/before target_date (when set) AND within 30 days
      - "low"  → eta misses target_date, or is > 30 days out with no target
    """
    import math  # noqa: PLC0415 — local import matches this module's style
    from datetime import date, timedelta  # noqa: PLC0415

    if remaining <= 0 or velocity_per_day <= 0:
        return None
    eta_days = math.ceil(remaining / velocity_per_day)
    eta_d = date.fromisoformat(today) + timedelta(days=eta_days)

    if target_date:
        try:
            confidence = "high" if eta_d <= date.fromisoformat(target_date) else "low"
        except ValueError:
            confidence = "high" if eta_days <= 30 else "low"
    else:
        confidence = "high" if eta_days <= 30 else "low"

    return {"eta_date": eta_d.isoformat(), "eta_days": eta_days, "confidence": confidence}


def budget_vs_actual(
    cfg: "BudgetConfig",
    used_by_provider: dict[str, int],
    cost_by_provider: dict[str, float],
) -> list[dict]:
    """Pair each configured provider's token budget with tokens actually used
    (rolling window) + USD spent. Pure — callers supply the aggregates.

    `pct` and `over_threshold` are computed in TOKENS (the unit the guardrail
    enforces). `cost_usd` rides along as an informational figure only — the
    budget config has no USD limit, so we never invent one.
    """
    rows = []
    for provider in sorted(cfg.providers):
        pb = cfg.providers[provider]
        used = int(used_by_provider.get(provider, 0))
        budget = int(pb.token_budget)
        pct = int(used / budget * 100) if budget > 0 else 0
        rows.append({
            "provider": provider,
            "token_budget": budget,
            "tokens_used": used,
            "pct": pct,
            "threshold_pct": pb.threshold_pct,
            "over_threshold": budget > 0 and pct >= pb.threshold_pct,
            "cost_usd": round(float(cost_by_provider.get(provider, 0.0)), 4),
        })
    return rows


def executive_summary(
    *,
    done: int,
    total: int,
    in_progress: int = 0,
    blocked: int = 0,
    blocked_reasons: list[str] | None = None,
    eta_date: str | None = None,
    eta_hours: float | None = None,
    total_spend_usd: float | None = None,
    language: str = "es",
) -> dict:
    """Render a deterministic business-language progress summary. No LLM —
    every sentence is a template filled from real figures (progress / in-progress
    / blocked + reasons / ETA / spend). Deterministic ⇒ testable, free, no
    `claude` CLI dependency on the dashboard host.

    Single source of truth for BOTH `/api/summary` and the stakeholder payload
    (Sprint E-7's inline summary was folded in here — G-4). Callers map their
    own data onto these primitives. Sentences are appended only when the data
    is present, so a partial input still reads cleanly.

    `generated_from` echoes the inputs for provenance. Unknown language → `es`.
    """
    lang = language if language in ("es", "en") else "es"
    pct = round(done / total * 100) if total > 0 else 0
    reasons = blocked_reasons or []
    head: list[str] = []
    tail: list[str] = []  # multi-line detail (blocked reasons)

    if lang == "es":
        head.append(f"Proyecto {pct}% completo — {done} de {total} tareas entregadas.")
        if in_progress:
            head.append(f"{in_progress} en progreso.")
        if blocked:
            head.append(f"{blocked} bloqueada(s) — requieren atención.")
        if reasons:
            tail.append("Bloqueos:\n" + "\n".join(reasons[:3]))
        if eta_date:
            head.append(f"ETA estimado: {eta_date}.")
        elif eta_hours is not None:
            head.append(f"Restan ~{eta_hours}h al ritmo actual.")
        if total_spend_usd is not None:
            head.append(f"Gastado en AI: ${total_spend_usd:.2f}.")
    else:  # en
        head.append(f"Project {pct}% complete — {done} of {total} tasks delivered.")
        if in_progress:
            head.append(f"{in_progress} in progress.")
        if blocked:
            head.append(f"{blocked} blocked — need attention.")
        if reasons:
            tail.append("Blocked:\n" + "\n".join(reasons[:3]))
        if eta_date:
            head.append(f"Estimated ETA: {eta_date}.")
        elif eta_hours is not None:
            head.append(f"~{eta_hours}h remaining at current pace.")
        if total_spend_usd is not None:
            head.append(f"AI spend: ${total_spend_usd:.2f}.")

    text = " ".join(head) + (("\n\n" + "\n".join(tail)) if tail else "")
    return {
        "text": text,
        "language": lang,
        "generated_from": {
            "done": done,
            "total": total,
            "in_progress": in_progress,
            "blocked": blocked,
            "eta_date": eta_date,
            "eta_hours": eta_hours,
            "total_spend_usd": total_spend_usd,
        },
    }


def milestones_from_phases(
    tasks: Iterable[Task],
) -> list[dict[str, Any]]:
    """Produce a stakeholder-friendly milestone list.

    Each entry:
        `{phase, total_count, done_count, done: bool}`

    A phase is `done=True` when every task in that phase has status
    "done". The list is sorted by phase — the template renders it as a
    checklist so the stakeholder can see progress at a glance.
    """
    per_phase: dict[int, dict[str, int]] = defaultdict(
        lambda: {"total_count": 0, "done_count": 0}
    )
    for t in tasks:
        row = per_phase[t.phase]
        row["total_count"] += 1
        if t.status == "done":
            row["done_count"] += 1
    out: list[dict[str, Any]] = []
    for phase in sorted(per_phase.keys()):
        row = per_phase[phase]
        out.append({
            "phase": phase,
            "total_count": row["total_count"],
            "done_count": row["done_count"],
            "done": row["total_count"] > 0 and row["done_count"] == row["total_count"],
        })
    return out


def phase_counts(tasks: Iterable[Task]) -> list[dict[str, Any]]:
    """`[{phase, name, total, done}, ...]` for the sidebar. Sorted by phase."""
    per_phase: dict[int, dict[str, int]] = defaultdict(
        lambda: {"total": 0, "done": 0, "in_progress": 0, "blocked": 0}
    )
    for t in tasks:
        row = per_phase[t.phase]
        row["total"] += 1
        if t.status == "done":
            row["done"] += 1
        elif t.status == "in-progress":
            row["in_progress"] += 1
        elif t.status == "blocked":
            row["blocked"] += 1
    out = [
        {
            "phase": p,
            "total": r["total"],
            "done": r["done"],
            "in_progress": r["in_progress"],
            "blocked": r["blocked"],
        }
        for p, r in sorted(per_phase.items())
    ]
    return out


# ---- Sprint health (F-5) ---------------------------------------------------


def sprint_velocity(done_7d: int, window_days: int = 7) -> float:
    """Tasks completed per day over the last *window_days* days."""
    return done_7d / window_days if window_days > 0 else 0.0


def sprint_eta(
    velocity_per_day: float,
    remaining_tasks: int,
    remaining_hours: float,
    now: datetime | None = None,
) -> dict[str, Any]:
    """Compute ETA fields from velocity and remaining work.

    Returns a dict with:
        eta_days        float | None  — projected days to completion
        eta_date        str | None    — ISO date (YYYY-MM-DD)
        confidence      str           — 'high' | 'low' | 'none'
    """
    _now = now or datetime.now(timezone.utc)
    if velocity_per_day <= 0 or remaining_tasks <= 0:
        return {"eta_days": None, "eta_date": None, "confidence": "none"}

    eta_days = remaining_tasks / velocity_per_day
    from datetime import timedelta
    eta_dt = _now + timedelta(days=eta_days)
    confidence = "high" if eta_days <= 30 else "low"
    return {
        "eta_days": round(eta_days, 1),
        "eta_date": eta_dt.date().isoformat(),
        "confidence": confidence,
    }


def sprint_health(
    tasks: Iterable[Task],
    done_7d: int,
    last_events: dict[str, dict[str, Any]],
    now: datetime | None = None,
) -> dict[str, Any]:
    """Aggregate sprint health payload for `GET /api/sprint`.

    Args:
        tasks:       All project tasks (Task objects).
        done_7d:     Count of tasks with status='done' updated in last 7 days.
                     Comes from the SQLite backend query.
        last_events: Latest event per task_id (from SqliteBackend.get_task_last_events).
        now:         Override for 'current time' (testing).

    Returns a dict ready for JSON serialisation.
    """
    tasks_list = list(tasks)
    _TERMINAL = {"done", "skipped"}
    active = [t for t in tasks_list if t.status not in _TERMINAL]
    blocked_tasks = [t for t in tasks_list if t.status == "blocked"]
    remaining = [t for t in active if t.status != "blocked"]

    done_count = sum(1 for t in tasks_list if t.status == "done")
    remaining_tasks = len(remaining)
    remaining_hours = sum(float(t.estimate_hours or 0.0) for t in remaining)

    velocity = sprint_velocity(done_7d)
    eta = sprint_eta(velocity, remaining_tasks, remaining_hours, now=now)

    blockers: list[dict[str, Any]] = []
    for t in sorted(blocked_tasks, key=lambda x: x.phase):
        ev = last_events.get(t.id, {})
        extra = ev.get("extra", {})
        reason: str = extra.get("reason") or ev.get("event_type") or "unknown"
        blocked_at: str | None = ev.get("ts")
        blockers.append({
            "task_id": t.id,
            "title": t.title or t.id,
            "phase": t.phase,
            "reason": reason[:300],
            "blocked_at": blocked_at,
            "estimate_hours": float(t.estimate_hours or 0.0),
        })

    return {
        "velocity_per_day": round(velocity, 2),
        "done_count": done_count,
        "remaining_tasks": remaining_tasks,
        "remaining_hours": round(remaining_hours, 1),
        "blocked_count": len(blocked_tasks),
        **eta,
        "blockers": blockers,
    }
