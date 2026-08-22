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
from typing import Any, Iterable

from orchestrator.models import Task
from orchestrator.dashboard.pricing import PricingTable


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
