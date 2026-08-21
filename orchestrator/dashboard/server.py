"""FastAPI application for the orchestrator dashboard.

Read-only surface. Never mutates `tasks.json` or state files.

Endpoints:
    GET  /                   — Jira-style task table (HTML)
    GET  /metrics            — metrics tables (HTML)
    GET  /logs               — logs page (HTML)
    GET  /logs/stream        — SSE feed of live events (text/event-stream)
    GET  /api/tasks          — JSON dump of all tasks + filters applied
    GET  /api/task/{id}      — JSON detail for a single task
    GET  /api/metrics        — JSON dump of metrics (models + days + total)
    GET  /api/events/stream  — SSE feed (JSON payloads), alias of /logs/stream
    GET  /partials/task-row/{id} — HTMX partial (single row refresh — MVP hook)
    GET  /partials/task-modal/{id} — HTMX partial (modal detail)

Design:
    - `AppState` holds `ProjectPaths` + a lazily-loaded `PricingTable`. Every
      request re-reads `tasks.json` and the JSONL logs from disk. That's
      acceptable at MVP scale (334 tasks, 20 event files ~10KB each) and
      keeps the code stateless / correct as new events land during a run.
    - Jinja2 is imported lazily inside `create_app()` so unit tests that
      exercise pure Python helpers don't require jinja2 to be installed.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

# Must live at module scope so `from __future__ import annotations` +
# FastAPI's `get_type_hints()` can resolve `Request` in route signatures.
# Keeping it lazy inside create_app() causes FastAPI to treat `request` as
# a query param → 422 on every HTML route.
from fastapi import Request

from orchestrator.dashboard.log_stream import (
    format_event,
    load_recent_events,
    sse_frame,
    tail_events,
)
from orchestrator.dashboard.metrics import (
    burndown_by_day,
    critical_path,
    downstream_impact,
    events_for_task,
    human_hours_by_task,
    last_updated_by_task,
    metrics_by_day,
    metrics_by_model,
    orphan_dependencies,
    parallelizable_tasks,
    phase_counts,
    project_summary,
    read_all_events,
    read_all_spends,
    total_cost,
)
from orchestrator.budget import BudgetGate, load_budget_config
from orchestrator.dashboard.dashboard_config import DashboardConfig
from orchestrator.dashboard.pricing import PricingTable
from orchestrator.paths import ProjectPaths, resolve_project_paths
from orchestrator.state import load_tasks


# ---- App state -------------------------------------------------------------


@dataclass
class AppState:
    """Runtime state bound to a FastAPI app instance.

    We stash the resolved `ProjectPaths` + the pricing table on the app
    itself (via `app.state`) so route handlers can look them up without any
    global. Pricing is loaded ONCE at startup — if the operator edits
    `pricing.yaml` mid-run, they need to restart the dashboard (acceptable
    trade-off for the MVP).

    `_cache` holds short-lived (2s TTL) memoization of disk reads so a
    single logical page load (which triggers 3+ helper calls) hits the
    filesystem exactly once. Polling every 10s + user paging = fine grained.
    """

    paths: ProjectPaths
    pricing: PricingTable
    config: DashboardConfig = field(default_factory=lambda: DashboardConfig.load())
    _cache: dict[str, tuple[float, Any]] = field(default_factory=dict)
    _cache_ttl_s: float = 2.0

    def cached(self, key: str, loader):
        """Return `loader()` result cached for `_cache_ttl_s` seconds.

        Cheap enough that we don't bother with per-file mtime tracking;
        the TTL naturally aligns with a single request cluster.
        """
        import time
        now = time.monotonic()
        hit = self._cache.get(key)
        if hit and (now - hit[0]) < self._cache_ttl_s:
            return hit[1]
        value = loader()
        self._cache[key] = (now, value)
        return value


# ---- Data loaders ----------------------------------------------------------


def _load_project_view(state: AppState) -> dict[str, Any]:
    """Read tasks + events + spends and shape the payload every view needs.

    Returned dict keys:
        tasks          — list[Task]
        summary        — ProjectSummary
        phases         — list[{phase, total, done, in_progress, blocked}]
        models         — sorted list of distinct model strings across tasks
        human_hours    — dict[task_id → hours]
        last_updated   — dict[task_id → ts]
        parallelizable_ids — set[str] of tasks ready to launch right now
        project_id     — str
        state_layout   — "legacy" | "namespaced"
    """
    paths = state.paths
    def _load_tasks():
        try:
            return load_tasks(paths.tasks_json)
        except (OSError, ValueError):
            return []

    tasks = state.cached("tasks", _load_tasks)
    events = state.cached("events", lambda: read_all_events(paths.state_dir))
    hours = state.cached("human_hours", lambda: human_hours_by_task(events))
    last = state.cached("last_updated", lambda: last_updated_by_task(events))

    para = {t.id for t in parallelizable_tasks(tasks)}
    models = sorted({t.model for t in tasks if t.model})
    impact = state.cached("impact", lambda: downstream_impact(tasks))
    cpath = state.cached("critical_path", lambda: critical_path(tasks))
    orphans = state.cached("orphans", lambda: orphan_dependencies(tasks))

    return {
        "tasks": tasks,
        "summary": project_summary(tasks),
        "phases": phase_counts(tasks),
        "models": models,
        "human_hours": hours,
        "last_updated": last,
        "parallelizable_ids": para,
        "impact": impact,
        "critical_path": cpath,
        "orphans": orphans,
        "project_id": paths.project_id,
        "project_root": str(paths.project_root),
        "state_layout": paths.state_layout,
    }


def _apply_filters(
    tasks: list,
    *,
    phase: int | list[int] | None,
    status: str | None,
    model: str | None,
    q: str | None,
    only_parallelizable: bool,
    parallelizable_ids: set,
    has_comments: bool = False,
    has_spec: bool = False,
    blocked_since_days: int | None = None,
    last_updated: dict[str, str] | None = None,
) -> list:
    """Apply the query-param filters on the task list in memory.

    Sprint 5 adds three quality-of-life filters (all optional):
      - has_comments — tasks with at least 1 comment.
      - has_spec     — tasks with a non-empty spec_ref.
      - blocked_since_days — blocked tasks stuck N+ days (via last_updated).
    """
    out = tasks
    if phase is not None:
        if isinstance(phase, list):
            if phase:  # non-empty list → filter; empty list treated as "all"
                phase_set = set(phase)
                out = [t for t in out if t.phase in phase_set]
        else:
            out = [t for t in out if t.phase == phase]
    if status:
        out = [t for t in out if t.status == status]
    if model:
        out = [t for t in out if t.model == model]
    if q:
        q_low = q.lower()
        out = [
            t for t in out
            if q_low in t.id.lower() or q_low in (t.title or "").lower()
        ]
    if only_parallelizable:
        out = [t for t in out if t.id in parallelizable_ids]
    if has_comments:
        out = [t for t in out if t.comments]
    if has_spec:
        out = [t for t in out if (t.spec_ref or "").strip()]
    if blocked_since_days is not None and last_updated is not None:
        from datetime import datetime, timezone, timedelta
        cutoff = datetime.now(timezone.utc) - timedelta(days=blocked_since_days)
        def _stale(t):
            if t.status != "blocked":
                return False
            ts = last_updated.get(t.id) or ""
            if not ts:
                # No event → we can't prove staleness; keep it (better UX
                # than silently dropping evidence).
                return True
            try:
                # Tolerate both trailing Z and offset-suffixed timestamps.
                ts_norm = ts.replace("Z", "+00:00")
                dt = datetime.fromisoformat(ts_norm)
                if dt.tzinfo is None:
                    dt = dt.replace(tzinfo=timezone.utc)
                return dt <= cutoff
            except ValueError:
                return True
        out = [t for t in out if _stale(t)]
    return out


def _task_to_dict(t, hours: dict[str, float], last: dict[str, str],
                  impact: dict[str, int] | None = None,
                  critical_ids: set[str] | None = None) -> dict[str, Any]:
    """Serialize a Task for JSON responses + template access."""
    return {
        "id": t.id,
        "phase": t.phase,
        "title": t.title,
        "description": t.description,
        "model": t.model,
        "reason": t.reason,
        "status": t.status,
        "dependencies": list(t.dependencies),
        "dep_count": len(t.dependencies),
        "estimate_hours": t.estimate_hours,
        "files": list(t.files),
        "spec_ref": t.spec_ref,
        "comments": list(t.comments),
        "human_hours": hours.get(t.id, 0.0),
        "last_updated": last.get(t.id, ""),
        "downstream_impact": (impact or {}).get(t.id, 0),
        "on_critical_path": t.id in (critical_ids or set()),
    }


# ---- App factory -----------------------------------------------------------


def create_app(
    paths: ProjectPaths | None = None,
    *,
    project_root: str | None = None,
    project_id: str | None = None,
    config: str = "orchestrator/config.yaml",
) -> Any:
    """Build and return the FastAPI application.

    Either pass `paths` directly (tests do this) or pass the CLI flags
    (`project_root` / `project_id`) and let this function resolve them.
    """
    # Lazy imports — the web stack only loads when we actually create the app.
    # `Request` is imported at module scope (see top) to keep annotations
    # resolvable under `from __future__ import annotations`.
    from fastapi import FastAPI, HTTPException, Query
    from fastapi.responses import HTMLResponse, JSONResponse, StreamingResponse
    from fastapi.staticfiles import StaticFiles
    from fastapi.templating import Jinja2Templates

    if paths is None:
        paths = resolve_project_paths(
            project_root_arg=project_root,
            project_id_arg=project_id,
            config_arg=config,
        )

    pricing = PricingTable.load(paths.project_root)
    dash_cfg = DashboardConfig.load(paths.project_root)
    app_state = AppState(paths=paths, pricing=pricing, config=dash_cfg)

    tpl_dir = Path(__file__).parent / "templates"
    static_dir = Path(__file__).parent / "static"

    app = FastAPI(
        title="Orch Dashboard",
        description="Read-only view over tasks.json / events / spend.",
        version="0.1.0",
    )
    app.state.app_state = app_state
    templates = Jinja2Templates(directory=str(tpl_dir))
    if static_dir.exists():
        app.mount("/static", StaticFiles(directory=str(static_dir)), name="static")

    # ---- Root: task table --------------------------------------------------
    @app.get("/", response_class=HTMLResponse)
    def index(
        request: Request,
        phase: list[int] | None = Query(None),
        status: str | None = Query(None),
        model: str | None = Query(None),
        q: str | None = Query(None),
        group: str | None = Query(None,
                                  description="'phase' groups the table rows by phase with collapse."),
        parallelizable: int = Query(0),
        has_comments: int = Query(0),
        has_spec: int = Query(0),
        blocked_since: int | None = Query(None, ge=0, le=365,
                                          description="Blocked tasks stuck N+ days"),
    ):
        view = _load_project_view(app_state)
        tasks_all = view["tasks"]
        filtered = _apply_filters(
            tasks_all,
            phase=phase,
            status=status,
            model=model,
            q=q,
            only_parallelizable=bool(parallelizable),
            parallelizable_ids=view["parallelizable_ids"],
            has_comments=bool(has_comments),
            has_spec=bool(has_spec),
            blocked_since_days=blocked_since,
            last_updated=view["last_updated"],
        )
        # Serialize once so both the template and downstream partials get the
        # same shape (dicts, not Task instances).
        rows = [_task_to_dict(t, view["human_hours"], view["last_updated"], view["impact"], view["critical_path"]) for t in filtered]
        # Optional: group by phase for the collapsible-section render.
        grouped_rows: list[dict] | None = None
        if group == "phase":
            buckets: dict[int, list] = {}
            for r in rows:
                buckets.setdefault(r["phase"], []).append(r)
            grouped_rows = [
                {"phase": p, "count": len(buckets[p]), "rows": buckets[p]}
                for p in sorted(buckets)
            ]
        ctx = {
            "request": request,
            "summary": view["summary"].as_dict(),
            "phases": view["phases"],
            "models": view["models"],
            "rows": rows,
            "grouped_rows": grouped_rows,
            "row_count": len(rows),
            "total_count": len(tasks_all),
            "parallelizable_ids": list(view["parallelizable_ids"]),
            "project_id": view["project_id"],
            "project_root": view["project_root"],
            "state_layout": view["state_layout"],
            "filters": {
                "phase": phase or [], "status": status, "model": model,
                "q": q, "group": group or "",
                "parallelizable": bool(parallelizable),
                "has_comments": bool(has_comments),
                "has_spec": bool(has_spec),
                "blocked_since": blocked_since,
            },
            "orphans": view["orphans"],
        }
        return templates.TemplateResponse("index.html", ctx)

    # ---- Kanban page -------------------------------------------------------
    # Per-column hint shown in empty state. Kept as a constant so the
    # template can stay dumb (no branching on column key).
    _EMPTY_HINTS = {
        "backlog": "Nothing in backlog. All planned work has moved forward.",
        "todo": "No queued tasks. Add rows to tasks.json to see them here.",
        "blocked": "Nothing blocked — every task has its deps clear.",
        "in-progress": "No task currently in flight.",
        "done": "No completions yet.",
    }

    @app.get("/kanban", response_class=HTMLResponse)
    def kanban_page(
        request: Request,
        phase: int | None = Query(None),
        model: str | None = Query(None),
        q: str | None = Query(None),
        parallelizable: int = Query(0),
        # Sprint 2: visualization knobs. All optional, backwards-compatible.
        wip: int | None = Query(None, ge=1, le=99,
                                description="Per-column WIP limit. Warn when column count exceeds it."),
        group: str | None = Query(None, pattern="^(phase)?$",
                                  description="Group cards inside a column. Only 'phase' supported."),
        # Sprint 3: sort strategy inside each column.
        sort: str | None = Query(None, pattern="^(impact|phase)?$",
                                 description="Sort within a column: 'impact' (bottlenecks first) | 'phase' (default)."),
        # Sprint 3: partial=board returns just the <section> for HTMX polling.
        partial: str | None = Query(None, pattern="^(board)?$",
                                    description="'board' returns only the board section for HTMX polling."),
        # Sprint 5: quality-of-life filters shared with `/`.
        has_comments: int = Query(0),
        has_spec: int = Query(0),
        blocked_since: int | None = Query(None, ge=0, le=365),
    ):
        # Sprint 5: apply operator defaults from dashboard.yaml when the
        # query didn't specify the knob. URL bookmarks always win.
        kdefaults = app_state.config.kanban
        if wip is None:
            wip = kdefaults.wip_default
        if sort is None:
            sort = kdefaults.sort_default
        if group is None:
            group = kdefaults.group_default
        view = _load_project_view(app_state)
        filtered = _apply_filters(
            view["tasks"],
            phase=phase,
            status=None,  # kanban shows all statuses as columns
            model=model,
            q=q,
            only_parallelizable=bool(parallelizable),
            parallelizable_ids=view["parallelizable_ids"],
            has_comments=bool(has_comments),
            has_spec=bool(has_spec),
            blocked_since_days=blocked_since,
            last_updated=view["last_updated"],
        )
        # Stable sort: phase asc, then id asc — makes bucket order
        # predictable across page loads regardless of tasks.json ordering.
        # When sort=impact, order by downstream_impact desc, then phase asc,
        # then id asc as tiebreakers so the ordering is still deterministic.
        impact_map = view["impact"]
        if sort == "impact":
            filtered = sorted(
                filtered,
                key=lambda t: (-impact_map.get(t.id, 0), t.phase, t.id),
            )
        else:
            filtered = sorted(filtered, key=lambda t: (t.phase, t.id))

        rows = [_task_to_dict(t, view["human_hours"], view["last_updated"], view["impact"], view["critical_path"]) for t in filtered]
        # Buckets in a fixed visual order — todo/backlog on the left,
        # done on the right. Tasks with unknown statuses fall into "todo"
        # so the operator still sees them.
        columns_order = ["backlog", "todo", "blocked", "in-progress", "done"]
        buckets: dict[str, list[dict[str, Any]]] = {c: [] for c in columns_order}
        for r in rows:
            key = r["status"] if r["status"] in buckets else "todo"
            r["parallelizable"] = r["id"] in view["parallelizable_ids"]
            buckets[key].append(r)

        # Optional phase grouping inside each column. When group=phase,
        # `groups` is a list of {phase, cards} preserving the sort order
        # above; otherwise it's a single synthetic group so the template
        # can iterate uniformly.
        def _phase_groups(cards: list[dict[str, Any]]) -> list[dict[str, Any]]:
            if group != "phase":
                return [{"phase": None, "cards": cards}]
            out: dict[int, list[dict[str, Any]]] = {}
            for c in cards:
                out.setdefault(c["phase"], []).append(c)
            return [{"phase": p, "cards": out[p]} for p in sorted(out.keys())]

        columns = [
            {
                "key": k,
                "label": k.replace("-", " "),
                "cards": buckets[k],
                "count": len(buckets[k]),
                "over_wip": bool(wip) and len(buckets[k]) > wip,
                "groups": _phase_groups(buckets[k]),
                "empty_hint": _EMPTY_HINTS.get(k, ""),
            }
            for k in columns_order
        ]
        ctx = {
            "request": request,
            "summary": view["summary"].as_dict(),
            "phases": view["phases"],
            "models": view["models"],
            "columns": columns,
            "row_count": len(rows),
            "total_count": len(view["tasks"]),
            "project_id": view["project_id"],
            "project_root": view["project_root"],
            "state_layout": view["state_layout"],
            "filters": {
                "phase": phase, "model": model, "q": q,
                "parallelizable": bool(parallelizable),
                "wip": wip, "group": group, "sort": sort,
                "has_comments": bool(has_comments),
                "has_spec": bool(has_spec),
                "blocked_since": blocked_since,
            },
            "refresh_interval_s": app_state.config.kanban.refresh_interval_s,
        }
        # HTMX polling wants only the board section — same context, smaller
        # template. Full page render is the default for browser navigation.
        template_name = "partials/kanban_board.html" if partial == "board" else "kanban.html"
        return templates.TemplateResponse(template_name, ctx)

    # ---- Metrics page ------------------------------------------------------
    @app.get("/metrics", response_class=HTMLResponse)
    def metrics_page(request: Request):
        view = _load_project_view(app_state)
        spends = read_all_spends(paths.state_dir)
        events = app_state.cached("events", lambda: read_all_events(paths.state_dir))
        by_model = [m.as_dict() for m in metrics_by_model(spends, pricing)]
        by_day = [d.as_dict() for d in metrics_by_day(spends, pricing, days=14)]
        burndown = burndown_by_day(events, days=14)
        total = total_cost(spends, pricing)
        # Estimation vs actual: sum estimateHours across ALL tasks, sum
        # human_hours across DONE tasks only (partial for in-flight would
        # skew the projection).
        done_hours = sum(
            v for tid, v in view["human_hours"].items()
            if any(t.id == tid and t.status == "done" for t in view["tasks"])
        )
        ctx = {
            "request": request,
            "summary": view["summary"].as_dict(),
            "project_id": view["project_id"],
            "project_root": view["project_root"],
            "by_model": by_model,
            "by_day": by_day,
            "burndown": burndown,
            "total_cost": round(total, 4),
            "done_hours": round(done_hours, 1),
            "estimate_hours_total": view["summary"].estimate_hours_total,
        }
        return templates.TemplateResponse("metrics.html", ctx)

    # ---- Logs page ---------------------------------------------------------
    @app.get("/logs", response_class=HTMLResponse)
    def logs_page(
        request: Request,
        task_id: str | None = Query(None),
        limit: int = Query(100, ge=1, le=1000),
    ):
        view = _load_project_view(app_state)
        events = load_recent_events(paths.state_dir, limit=limit)
        if task_id:
            events = [e for e in events if e["task_id"] == task_id]
        ctx = {
            "request": request,
            "project_id": view["project_id"],
            "project_root": view["project_root"],
            "events": events,
            "task_id_filter": task_id or "",
            "limit": limit,
        }
        return templates.TemplateResponse("logs.html", ctx)

    # ---- SSE stream --------------------------------------------------------
    def _stream(task_id: str | None):
        """Generator yielding SSE-formatted frames."""
        for ev in tail_events(paths.state_dir, task_id_filter=task_id):
            yield sse_frame(ev, event="event")

    @app.get("/logs/stream")
    def logs_stream(task_id: str | None = Query(None)):
        return StreamingResponse(
            _stream(task_id),
            media_type="text/event-stream",
            headers={"Cache-Control": "no-cache", "X-Accel-Buffering": "no"},
        )

    @app.get("/api/events/stream")
    def api_events_stream(task_id: str | None = Query(None)):
        # Alias — same payload, meant for programmatic consumers.
        return StreamingResponse(
            _stream(task_id),
            media_type="text/event-stream",
            headers={"Cache-Control": "no-cache", "X-Accel-Buffering": "no"},
        )

    # ---- JSON APIs ---------------------------------------------------------
    @app.get("/api/tasks")
    def api_tasks(
        phase: int | None = Query(None),
        status: str | None = Query(None),
        model: str | None = Query(None),
        q: str | None = Query(None),
        parallelizable: int = Query(0),
    ):
        view = _load_project_view(app_state)
        filtered = _apply_filters(
            view["tasks"],
            phase=phase, status=status, model=model, q=q,
            only_parallelizable=bool(parallelizable),
            parallelizable_ids=view["parallelizable_ids"],
        )
        rows = [_task_to_dict(t, view["human_hours"], view["last_updated"], view["impact"], view["critical_path"]) for t in filtered]
        return JSONResponse({
            "project_id": view["project_id"],
            "project_root": view["project_root"],
            "summary": view["summary"].as_dict(),
            "tasks": rows,
            "count": len(rows),
            "total": len(view["tasks"]),
        })

    @app.get("/api/task/{task_id}")
    def api_task_detail(task_id: str):
        view = _load_project_view(app_state)
        for t in view["tasks"]:
            if t.id == task_id:
                d = _task_to_dict(t, view["human_hours"], view["last_updated"], view["impact"], view["critical_path"])
                d["parallelizable"] = t.id in view["parallelizable_ids"]
                return JSONResponse(d)
        raise HTTPException(status_code=404, detail=f"task not found: {task_id}")

    @app.get("/api/budgets")
    def api_budgets():
        """Sprint 7 — per-provider budget snapshot for the dashboard.

        Returns `{disabled: bool, preset: str, providers: {...}}`. When
        `budgets.yaml` isn't present the gate stays disabled and the UI
        should hide the section. Preset resolution mirrors the main loop:
        env `ORCH_BUDGETS_PRESET` → `config.yaml` → default `conservative`.
        """
        import os
        # Try config-adjacent first (packaged default), then project_root.
        preset = os.environ.get("ORCH_BUDGETS_PRESET") or "conservative"
        candidates = [
            paths.config_yaml.parent / "budgets.yaml",
            paths.project_root / "budgets.yaml",
        ]
        budget_cfg = None
        for candidate in candidates:
            try:
                budget_cfg = load_budget_config(candidate, preset=preset)
            except ValueError:
                # Bad preset — bubble up as 400 so the operator sees the typo.
                from fastapi import HTTPException
                raise HTTPException(
                    status_code=400,
                    detail=f"unknown budgets preset {preset!r}",
                )
            if budget_cfg is not None:
                break
        gate = BudgetGate(state_dir=paths.state_dir, config=budget_cfg)
        return JSONResponse({
            "disabled": gate.disabled,
            "preset": preset,
            "providers": gate.snapshot(),
        })

    @app.get("/api/metrics")
    def api_metrics():
        view = _load_project_view(app_state)
        spends = read_all_spends(paths.state_dir)
        return JSONResponse({
            "project_id": view["project_id"],
            "total_cost_usd": total_cost(spends, pricing),
            "by_model": [m.as_dict() for m in metrics_by_model(spends, pricing)],
            "by_day": [d.as_dict() for d in metrics_by_day(spends, pricing, days=14)],
            "estimate_hours_total": view["summary"].estimate_hours_total,
        })

    # ---- Snapshot export ---------------------------------------------------
    @app.get("/snapshot")
    def snapshot(request: Request):
        """One JSON dump of the project's current state — safe to archive in git.

        Includes: summary, all tasks (serialized), parallelizable ids, orphan
        deps, critical path, downstream impact. Excludes streaming logs.
        Response `Content-Disposition: attachment` so browsers download it.
        """
        from datetime import datetime, timezone
        view = _load_project_view(app_state)
        spends = read_all_spends(paths.state_dir)
        rows = [
            _task_to_dict(t, view["human_hours"], view["last_updated"],
                          view["impact"], view["critical_path"])
            for t in view["tasks"]
        ]
        for r in rows:
            r["parallelizable"] = r["id"] in view["parallelizable_ids"]

        payload = {
            "schema_version": 1,
            "captured_at": datetime.now(timezone.utc).isoformat(),
            "project_id": view["project_id"],
            "project_root": view["project_root"],
            "summary": view["summary"].as_dict(),
            "phases": view["phases"],
            "tasks": rows,
            "parallelizable_ids": sorted(view["parallelizable_ids"]),
            "critical_path": sorted(view["critical_path"]),
            "orphans": view["orphans"],
            "spend": {
                "total_usd": round(total_cost(spends, pricing), 4),
                "by_model": [m.as_dict() for m in metrics_by_model(spends, pricing)],
            },
        }
        filename = f"orch-snapshot-{view['project_id']}-{datetime.now(timezone.utc).strftime('%Y%m%d-%H%M%S')}.json"
        return JSONResponse(
            payload,
            headers={"Content-Disposition": f'attachment; filename="{filename}"'},
        )

    # ---- HTMX partials -----------------------------------------------------
    @app.get("/partials/task-modal/{task_id}", response_class=HTMLResponse)
    def partial_task_modal(request: Request, task_id: str):
        view = _load_project_view(app_state)
        for t in view["tasks"]:
            if t.id == task_id:
                row = _task_to_dict(t, view["human_hours"], view["last_updated"], view["impact"], view["critical_path"])
                # Enrich with dep status for the modal.
                by_id = {tt.id: tt for tt in view["tasks"]}
                row["dep_details"] = [
                    {
                        "id": did,
                        "title": (by_id.get(did).title if did in by_id else "?"),
                        "status": (by_id.get(did).status if did in by_id else "missing"),
                    }
                    for did in t.dependencies
                ]
                row["parallelizable"] = t.id in view["parallelizable_ids"]
                # Sprint 3: read events lazily — only the modal needs them,
                # not every /kanban render. Scoping to this task_id keeps
                # the payload tiny even on projects with 20+ event files.
                all_events = read_all_events(paths.state_dir)
                row["timeline"] = events_for_task(all_events, task_id)
                return templates.TemplateResponse(
                    "partials/task_modal.html",
                    {"request": request, "task": row},
                )
        raise HTTPException(status_code=404, detail=f"task not found: {task_id}")

    @app.get("/partials/task-row/{task_id}", response_class=HTMLResponse)
    def partial_task_row(request: Request, task_id: str):
        view = _load_project_view(app_state)
        for t in view["tasks"]:
            if t.id == task_id:
                row = _task_to_dict(t, view["human_hours"], view["last_updated"], view["impact"], view["critical_path"])
                row["parallelizable"] = t.id in view["parallelizable_ids"]
                return templates.TemplateResponse(
                    "partials/task_row.html",
                    {"request": request, "row": row},
                )
        raise HTTPException(status_code=404, detail=f"task not found: {task_id}")

    return app


# ---- CLI runner ------------------------------------------------------------


def run(
    *,
    port: int = 7420,
    host: str = "127.0.0.1",
    project_root: str | None = None,
    project_id: str | None = None,
    config: str = "orchestrator/config.yaml",
    reload: bool = False,
) -> int:
    """Launch uvicorn in the foreground. Returns the process exit code."""
    try:
        import uvicorn
    except ImportError:
        print("uvicorn is not installed; add fastapi + uvicorn[standard] to your env.")
        return 1

    paths = resolve_project_paths(
        project_root_arg=project_root,
        project_id_arg=project_id,
        config_arg=config,
    )
    # Best-effort validation. The dashboard still boots even if the layout
    # is imperfect (tests use fixtures without scripts/task-start.sh) but
    # we log a clear warning so the operator sees what's wrong.
    if not paths.tasks_json.exists():
        print(f"[warn] {paths.tasks_json} does not exist — dashboard will show 0 tasks.")

    app = create_app(paths=paths)

    banner = (
        f"Orch dashboard running on http://{host}:{port}\n"
        f"Project: {paths.project_id} ({paths.project_root})\n"
        f"State dir: {paths.state_dir}\n"
        f"Ctrl+C to stop"
    )
    print(banner)

    uvicorn.run(app, host=host, port=port, reload=reload, log_level="info")
    return 0
