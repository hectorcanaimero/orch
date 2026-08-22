"""Shared aggregation module for Sprint C observability commands.

`build_status_snapshot(paths, cfg)` is the ONE place that reduces backend
+ tasks.json + router.yaml into a JSON-shaped dict. Both `orch status` and
`orch graph` call it in-process (do NOT shell out to `orch status --json`
from graph). Refactor target: any other observability surface that needs
"the current view" reaches for this function.

Design guardrails:
    - We only touch the `StateBackend` Protocol methods. No reaching into
      private fields on FileBackend or SqliteBackend.
    - `defer_reasons` is not persisted (Sprint C decision #6). From-disk
      readers see `defer_reason: null` for every task. Only the running
      orch process, which holds the in-memory dict, can populate them —
      that path is not exercised here.
    - `SpendEntry` has no `run_id` column (Sprint C decision #5). We
      compute the per-task cost aggregate across ALL spend rows for the
      project — good enough for "what has this task cost so far?".
"""

from __future__ import annotations

import fnmatch
from pathlib import Path
from typing import Any

from .models import RouteEntry, Task
from .paths import ProjectPaths
from .router import load_router
from .state import get_backend, load_tasks
from .state.interface import StateBackend


def _human_last_event(row: dict[str, Any] | None) -> str | None:
    """Turn a last-event row into a short human string; None → None."""
    if not row:
        return None
    et = row.get("event_type") or "?"
    ts = row.get("ts") or ""
    return f"{et} @ {ts}" if ts else et


def build_status_snapshot(
    paths: ProjectPaths,
    cfg: dict[str, Any],
    *,
    only: str | None = None,
    status_filter: set[str] | None = None,
    defer_reasons: dict[str, str] | None = None,
) -> dict[str, Any]:
    """Aggregate the current project state into a single JSON-serializable dict.

    Callers:
        - `orch status` (human table + JSON output)
        - `orch tasks` (thin listing; reuses this then trims columns)
        - `orch graph` (mid-run HTML snapshot)

    `only` is a fnmatch glob applied to task ids AFTER aggregation, so the
    project-wide totals stay accurate (matches the mental model users have
    for `--only`).

    `status_filter` is a set of `Status` strings applied per-task AFTER
    aggregation.

    `defer_reasons` — passed by the running orch (in-memory dict). Reads
    from disk pass None here → every entry gets `defer_reason: null`.
    """
    backend: StateBackend = get_backend(paths, cfg)

    # Tasks + router are read-only, source-of-truth for the DAG structure.
    try:
        tasks: list[Task] = load_tasks(paths.tasks_json)
    except Exception:  # noqa: BLE001 — malformed tasks.json is not our problem
        tasks = []

    router_map: dict[str, RouteEntry] = {}
    try:
        router_map = load_router(paths.router_yaml)
    except Exception:  # noqa: BLE001 — router load may fail; report tasks anyway
        router_map = {}

    # Ensure bootstrap so sqlite has tasks_runtime rows (idempotent).
    try:
        backend.bootstrap(tasks)
    except Exception:  # noqa: BLE001 — best-effort
        pass

    status_by_id = backend.get_all_task_status()

    # Per-task cumulative cost from the spend log (all-time for the project).
    cost_by_task: dict[str, float] = {}
    try:
        for row in backend.iter_all_spend():
            tid = row.get("task_id")
            if not isinstance(tid, str):
                continue
            cost = float(row.get("cost_usd") or 0.0)
            cost_by_task[tid] = cost_by_task.get(tid, 0.0) + cost
    except Exception:  # noqa: BLE001
        pass

    # Last-event lookup, one call for the whole project.
    last_events: dict[str, dict[str, Any]] = {}
    try:
        last_events = backend.get_task_last_events()
    except Exception:  # noqa: BLE001
        last_events = {}

    # Latest run info (the "current" one). We do NOT fold historical runs.
    latest_run: dict[str, Any] | None = None
    try:
        runs = backend.list_runs()
        if runs:
            latest_run = runs[0]  # list_runs is newest-first for both backends
    except Exception:  # noqa: BLE001
        latest_run = None

    task_rows: list[dict[str, Any]] = []
    for t in tasks:
        route = router_map.get(t.model)
        backend_name = route.backend if route else "?"
        cli_model = route.cli_model if route else t.model
        row_status = status_by_id.get(t.id, t.status)
        last_ev = last_events.get(t.id)
        row = {
            "id": t.id,
            "phase": t.phase,
            "title": t.title,
            "status": row_status,
            "backend": backend_name,
            "cli_model": cli_model,
            "model": t.model,
            "tier": route.tier if route else None,
            "dependencies": list(t.dependencies),
            "cost_usd": round(cost_by_task.get(t.id, 0.0), 4),
            "last_event": last_ev,
            "last_event_human": _human_last_event(last_ev),
            "defer_reason": (defer_reasons or {}).get(t.id),
        }
        task_rows.append(row)

    # Filter after aggregation so totals still reflect the whole project.
    filtered = task_rows
    if only:
        filtered = [r for r in filtered if fnmatch.fnmatchcase(r["id"], only)]
    if status_filter:
        filtered = [r for r in filtered if r["status"] in status_filter]

    # Totals — always project-wide, not filtered (so `--only` doesn't lie).
    totals: dict[str, int] = {}
    for r in task_rows:
        totals[r["status"]] = totals.get(r["status"], 0) + 1
    totals["_total"] = len(task_rows)

    # Cost totals (project + filtered view).
    project_cost = round(sum(cost_by_task.values()), 4)
    filtered_cost = round(sum(r["cost_usd"] for r in filtered), 4)

    backend_kind = "sqlite" if backend.__class__.__name__ == "SqliteBackend" else "file"

    return {
        "project": {
            "project_id": paths.project_id,
            "project_root": str(paths.project_root),
            "backend": backend_kind,
            "state_dir": str(paths.state_dir),
        },
        "totals": totals,
        "cost": {
            "project_total_usd": project_cost,
            "filtered_total_usd": filtered_cost,
        },
        "latest_run": latest_run,
        "tasks": filtered,
        "filters": {
            "only": only,
            "status": sorted(status_filter) if status_filter else None,
        },
    }
