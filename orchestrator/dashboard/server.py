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

import sys
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
    eta_hours_remaining,
    events_for_task,
    human_hours_by_task,
    last_updated_by_task,
    metrics_by_day,
    metrics_by_model,
    milestones_from_phases,
    orphan_dependencies,
    parallelizable_tasks,
    phase_counts,
    project_summary,
    read_all_events,
    read_all_spends,
    round_up_to_step,
    total_cost,
)
from orchestrator.budget import BudgetGate, load_budget_config
from orchestrator.dashboard.dashboard_config import (
    PROFILE_OPERATOR,
    DashboardConfig,
)
from orchestrator.dashboard.middleware import (
    ProfileGuardMiddleware,
    TokenAuthMiddleware,
)
from orchestrator.dashboard.pricing import PricingTable
from orchestrator.paths import ProjectPaths, resolve_project_paths
from orchestrator.state import load_tasks


# Sprint E-5: server-side idle cutoff for `/api/tunnel/logs` (TUN-10 / D3).
# Module-level so tests can shrink it; the endpoint reads through
# `_get_sse_idle_cutoff_s()` on every stream open.
TUNNEL_SSE_IDLE_CUTOFF_S = 30 * 60


def _get_sse_idle_cutoff_s() -> float:
    return float(TUNNEL_SSE_IDLE_CUTOFF_S)


# Sprint E-5 (TUN-9): auto_start knobs. Overridable in tests so we don't
# actually sleep 0.5s × 3 while probing. Both are module-level so tests can
# shrink them via monkeypatch without importing the handler closure.
TUNNEL_AUTO_START_PROBE_RETRIES = 3
TUNNEL_AUTO_START_PROBE_GAP_S = 0.5


def _tunnel_self_probe(port: int, timeout_s: float) -> bool:
    """Blocking `GET http://127.0.0.1:<port>/` — stdlib only (NFR-2).

    Retries up to `TUNNEL_AUTO_START_PROBE_RETRIES` times with a fixed gap
    to tolerate slow bind. Any 2xx counts as success. Returns False if
    every attempt raises or returns non-2xx.
    """
    import time
    from urllib.error import URLError
    from urllib.request import urlopen

    url = f"http://127.0.0.1:{port}/"
    attempts = max(1, int(TUNNEL_AUTO_START_PROBE_RETRIES))
    for i in range(attempts):
        try:
            with urlopen(url, timeout=timeout_s) as resp:  # noqa: S310 — loopback
                code = getattr(resp, "status", None) or resp.getcode()
                if 200 <= int(code) < 300:
                    return True
        except (URLError, OSError, ValueError):
            pass
        if i < attempts - 1:
            time.sleep(TUNNEL_AUTO_START_PROBE_GAP_S)
    return False


async def _run_auto_start(
    app_state: "AppState", mgr: Any, port: int, timeout_s: float
) -> None:
    """Probe the running dashboard from the event loop and spawn on success.

    Blocking bits (urlopen, Popen inside `manager.start`) are pushed off
    the loop via `asyncio.to_thread` so uvicorn keeps serving during the
    probe window.
    """
    import asyncio

    try:
        ok = await asyncio.to_thread(_tunnel_self_probe, port, timeout_s)
    except Exception as exc:  # noqa: BLE001 — self-probe MUST NOT break startup
        print(
            f"[tunnel] auto_start_skipped: self_probe_failed ({exc})",
            file=sys.stderr,
        )
        return
    if not ok:
        print(
            "[tunnel] auto_start_skipped: self_probe_failed",
            file=sys.stderr,
        )
        return

    tcfg = app_state.config.tunnel
    # Rebuild the same TunnelManagerConfig shape the route uses so the
    # spawn path is identical.
    from orchestrator.dashboard.tunnel import TunnelManagerConfig

    mcfg = TunnelManagerConfig(
        provider=tcfg.provider,
        command=tcfg.command,
        args=tuple(tcfg.args or ()),
        url_regex=tcfg.url_regex,
        url_parse_timeout_s=int(tcfg.url_parse_timeout_s),
    )
    try:
        await asyncio.to_thread(mgr.start, mcfg)
        print("[tunnel] auto_start: spawned", file=sys.stderr)
    except RuntimeError as exc:
        # `already_running` / `locked` land here — logged, not raised, so
        # a race with a manual /start doesn't crash startup.
        print(f"[tunnel] auto_start_skipped: {exc}", file=sys.stderr)
    except Exception as exc:  # noqa: BLE001 — mirror the log-and-continue rule
        print(
            f"[tunnel] auto_start_skipped: spawn_error ({type(exc).__name__})",
            file=sys.stderr,
        )


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
    # Sprint E-5: uvicorn port for the tunnel auto_start self-probe (TUN-9).
    # `None` when unknown (tests / non-uvicorn boots) → auto_start skips.
    probe_port: int | None = None

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


def _load_project_config(state: AppState) -> dict[str, Any]:
    """Return a whitelisted view of the project's `config.yaml`.

    Explicit picking (never `dict(**raw)`) so a future config key with a
    secret cannot accidentally leak through the `/api/config` endpoint.
    """
    def _loader() -> dict[str, Any]:
        import yaml
        cfg_path = state.paths.config_yaml
        try:
            raw = yaml.safe_load(cfg_path.read_text(encoding="utf-8")) or {}
        except (OSError, ValueError, yaml.YAMLError):
            return {}
        if not isinstance(raw, dict):
            return {}

        concurrency_raw = raw.get("concurrency") or {}
        budget_raw = raw.get("budget") or {}
        state_raw = raw.get("state") or {}
        retry_raw = raw.get("retry") or {}
        findings_raw = raw.get("findings") or {}
        dashboard_raw = raw.get("dashboard") or {}

        return {
            "concurrency": {
                "global_max": concurrency_raw.get("global_max"),
                "per_provider": concurrency_raw.get("per_provider") or {},
                "per_file": concurrency_raw.get("per_file"),
            },
            "budget": {
                "per_dispatch_usd": budget_raw.get("per_dispatch_usd"),
            },
            "state": {
                "backend": state_raw.get("backend"),
                "sqlite_path": state_raw.get("sqlite_path"),
                # `tasks_json_precedence` is a top-level key in config.yaml
                # but belongs to state semantics — group it here for API sanity.
                "tasks_json_precedence": raw.get("tasks_json_precedence"),
            },
            "retry": {
                "backoff_seconds": retry_raw.get("backoff_seconds"),
                "rate_limit_backoff_seconds": retry_raw.get("rate_limit_backoff_seconds"),
            },
            "spec_root": raw.get("spec_root"),
            "budgets": {
                "config_path": raw.get("budgets_config"),
                "preset": raw.get("budgets_preset"),
                "typical_dispatch_tokens": raw.get("typical_dispatch_tokens"),
            },
            "findings": {
                "publish_repo": findings_raw.get("publish_repo"),
                "publish_rate_limit_per_hour": findings_raw.get("publish_rate_limit_per_hour"),
                "label": findings_raw.get("label"),
                "min_publish_confidence": findings_raw.get("min_publish_confidence"),
            },
            # Sprint E-3 — per-project SPA client config. Only surface public,
            # non-secret keys here (profile/token stay OUT: the middleware
            # already gates auth, and echoing the token would defeat it).
            "dashboard": {
                "board_url": dashboard_raw.get("board_url"),
            },
            "strict_files_phases": raw.get("strict_files_phases") or [],
            "default_timeout_multiplier": raw.get("default_timeout_multiplier"),
        }

    return state.cached("config", _loader)


# ---- App factory -----------------------------------------------------------


def create_app(
    paths: ProjectPaths | None = None,
    *,
    project_root: str | None = None,
    project_id: str | None = None,
    config: str = "orchestrator/config.yaml",
    profile_override: str | None = None,
    token_override: str | None = None,
    probe_port: int | None = None,
) -> Any:
    """Build and return the FastAPI application.

    Either pass `paths` directly (tests do this) or pass the CLI flags
    (`project_root` / `project_id`) and let this function resolve them.

    `profile_override` / `token_override` map 1:1 to the `--profile` /
    `--token` CLI flags. They win over env + config.yaml but only when
    passed (typically wired by `run()` below).
    """
    # Lazy imports — the web stack only loads when we actually create the app.
    # `Request` is imported at module scope (see top) to keep annotations
    # resolvable under `from __future__ import annotations`.
    from fastapi import Depends, FastAPI, HTTPException, Query
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
    # Load DashboardConfig against paths.config_yaml so `dashboard:` block
    # is honored. CLI overrides take precedence over env + config.
    dash_cfg = DashboardConfig.load(
        paths.project_root,
        config_yaml=paths.config_yaml,
        profile_override=profile_override,
        token_override=token_override,
    )
    app_state = AppState(
        paths=paths, pricing=pricing, config=dash_cfg, probe_port=probe_port
    )
    # Sprint E-5: attach the tunnel supervisor singleton when enabled.
    # Kept absent (None) when disabled so nothing spawns/sweeps — TUN-NFR-3
    # (rollback via config) plus TUN-8 (reconciler runs at boot only when
    # the manager exists).
    app_state.tunnel_manager = None
    if dash_cfg.tunnel.enabled:
        from orchestrator.dashboard.tunnel import TunnelManager, TunnelManagerConfig
        _tm = TunnelManager(state_dir=paths.state_dir)
        _tm.sweep_stale_lock(
            TunnelManagerConfig(
                provider=dash_cfg.tunnel.provider,
                command=dash_cfg.tunnel.command,
            )
        )
        app_state.tunnel_manager = _tm

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

    # ---- Sprint E-2 middleware: profile guard + token auth ---------------
    # FastAPI executes the LAST-added middleware FIRST on the wire, so add
    # ProfileGuard first and TokenAuth second → on-wire order is
    # `auth → guard → route`. Auth failing yields 401 before the guard
    # even runs (no route-existence leak).
    if dash_cfg.profile != PROFILE_OPERATOR:
        app.add_middleware(ProfileGuardMiddleware, config=dash_cfg)
        app.add_middleware(TokenAuthMiddleware, config=dash_cfg)

    # ---- Sprint E-3 middleware: DEV-ONLY CORS ----------------------------
    # Enabled only when `ORCH_DASHBOARD_DEV_CORS` is truthy so a local
    # Vite SPA on :5173 can talk to the FastAPI backend on :7420. Added
    # AFTER auth in code so it runs BEFORE auth at request time — that
    # lets the CORS preflight (OPTIONS) succeed without a token, which
    # is what browsers require before dispatching the real request.
    #
    # NEVER enable in production. The allow-list is hard-coded to
    # localhost origins and a warning is printed to stderr on boot.
    import os
    import sys
    _cors_flag = (os.environ.get("ORCH_DASHBOARD_DEV_CORS") or "").strip().lower()
    if _cors_flag in ("1", "true", "yes"):
        from starlette.middleware.cors import CORSMiddleware

        app.add_middleware(
            CORSMiddleware,
            allow_origins=["http://localhost:5173", "http://127.0.0.1:5173"],
            allow_credentials=True,
            allow_methods=["GET", "POST", "OPTIONS"],
            allow_headers=["Authorization", "Content-Type"],
        )
        print(
            "[dashboard] DEV CORS enabled for localhost:5173 — "
            "do NOT enable in production",
            file=sys.stderr,
        )

    # ---- Template context helper -----------------------------------------
    # `profile` is injected into every template render so partials can
    # feature-flag sensitive rows without each route re-passing it.
    # `TemplateResponse` ignores unknown keys, so this is safe.
    def _tpl_ctx(**kwargs: Any) -> dict[str, Any]:
        base = {"profile": app_state.config.profile}
        base.update(kwargs)
        return base

    # ---- Root: task table --------------------------------------------------
    # Route names are load-bearing: `ProfileGuardMiddleware` consults them
    # against `DashboardConfig.stakeholder_routes` to decide 200 vs 403.
    # Every route below MUST declare `name=` or it stays operator-only.
    @app.get("/", response_class=HTMLResponse, name="index")
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
        ctx = _tpl_ctx(
            request=request,
            summary=view["summary"].as_dict(),
            phases=view["phases"],
            models=view["models"],
            rows=rows,
            grouped_rows=grouped_rows,
            row_count=len(rows),
            total_count=len(tasks_all),
            parallelizable_ids=list(view["parallelizable_ids"]),
            project_id=view["project_id"],
            project_root=view["project_root"],
            state_layout=view["state_layout"],
            filters={
                "phase": phase or [], "status": status, "model": model,
                "q": q, "group": group or "",
                "parallelizable": bool(parallelizable),
                "has_comments": bool(has_comments),
                "has_spec": bool(has_spec),
                "blocked_since": blocked_since,
            },
            orphans=view["orphans"],
        )
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

    @app.get("/kanban", response_class=HTMLResponse, name="kanban_page")
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
        ctx = _tpl_ctx(
            request=request,
            summary=view["summary"].as_dict(),
            phases=view["phases"],
            models=view["models"],
            columns=columns,
            row_count=len(rows),
            total_count=len(view["tasks"]),
            project_id=view["project_id"],
            project_root=view["project_root"],
            state_layout=view["state_layout"],
            filters={
                "phase": phase, "model": model, "q": q,
                "parallelizable": bool(parallelizable),
                "wip": wip, "group": group, "sort": sort,
                "has_comments": bool(has_comments),
                "has_spec": bool(has_spec),
                "blocked_since": blocked_since,
            },
            refresh_interval_s=app_state.config.kanban.refresh_interval_s,
        )
        # HTMX polling wants only the board section — same context, smaller
        # template. Full page render is the default for browser navigation.
        template_name = "partials/kanban_board.html" if partial == "board" else "kanban.html"
        return templates.TemplateResponse(template_name, ctx)

    # ---- Metrics page ------------------------------------------------------
    @app.get("/metrics", response_class=HTMLResponse, name="metrics_page")
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
        ctx = _tpl_ctx(
            request=request,
            summary=view["summary"].as_dict(),
            project_id=view["project_id"],
            project_root=view["project_root"],
            by_model=by_model,
            by_day=by_day,
            burndown=burndown,
            total_cost=round(total, 4),
            done_hours=round(done_hours, 1),
            estimate_hours_total=view["summary"].estimate_hours_total,
        )
        return templates.TemplateResponse("metrics.html", ctx)

    # ---- Logs page ---------------------------------------------------------
    @app.get("/logs", response_class=HTMLResponse, name="logs_page")
    def logs_page(
        request: Request,
        task_id: str | None = Query(None),
        limit: int = Query(100, ge=1, le=1000),
    ):
        view = _load_project_view(app_state)
        events = load_recent_events(paths.state_dir, limit=limit)
        if task_id:
            events = [e for e in events if e["task_id"] == task_id]
        ctx = _tpl_ctx(
            request=request,
            project_id=view["project_id"],
            project_root=view["project_root"],
            events=events,
            task_id_filter=task_id or "",
            limit=limit,
        )
        return templates.TemplateResponse("logs.html", ctx)

    # ---- SSE stream --------------------------------------------------------
    def _stream(task_id: str | None):
        """Generator yielding SSE-formatted frames."""
        for ev in tail_events(paths.state_dir, task_id_filter=task_id):
            yield sse_frame(ev, event="event")

    @app.get("/logs/stream", name="logs_stream")
    def logs_stream(task_id: str | None = Query(None)):
        return StreamingResponse(
            _stream(task_id),
            media_type="text/event-stream",
            headers={"Cache-Control": "no-cache", "X-Accel-Buffering": "no"},
        )

    @app.get("/api/events/stream", name="api_events_stream")
    def api_events_stream(task_id: str | None = Query(None)):
        # Alias — same payload, meant for programmatic consumers.
        return StreamingResponse(
            _stream(task_id),
            media_type="text/event-stream",
            headers={"Cache-Control": "no-cache", "X-Accel-Buffering": "no"},
        )

    @app.get("/api/events", name="api_events")
    def api_events(
        task_id: str | None = Query(None),
        limit: int = Query(200, ge=1, le=1000),
    ):
        """Return the last `limit` events (formatted). Used to seed the Logs page.

        Sibling to `/api/events/stream` — this is the one-shot history the SPA
        fetches on mount, then the SSE endpoint appends live events on top.
        Filtering by `task_id` happens AFTER slicing so callers get up to
        `limit` matching rows regardless of how noisy the run was overall.
        """
        events = load_recent_events(paths.state_dir, limit=limit)
        if task_id:
            events = [e for e in events if e["task_id"] == task_id]
        return JSONResponse({"events": events, "count": len(events)})

    # ---- JSON APIs ---------------------------------------------------------
    @app.get("/api/tasks", name="api_tasks")
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

    @app.get("/api/task/{task_id}", name="api_task_detail")
    def api_task_detail(task_id: str):
        view = _load_project_view(app_state)
        for t in view["tasks"]:
            if t.id == task_id:
                d = _task_to_dict(t, view["human_hours"], view["last_updated"], view["impact"], view["critical_path"])
                d["parallelizable"] = t.id in view["parallelizable_ids"]
                return JSONResponse(d)
        raise HTTPException(status_code=404, detail=f"task not found: {task_id}")

    @app.get("/api/budgets", name="api_budgets")
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

    @app.get("/api/metrics", name="api_metrics")
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

    @app.get("/api/config", name="api_config")
    def api_config():
        return JSONResponse(_load_project_config(app_state))

    @app.get("/api/doctor", name="api_doctor")
    def api_doctor():
        # Reuse the same code path as `orch doctor --json` — never
        # subprocess out to the CLI (a shell per request would be brutal
        # on a page that offers a Re-run button). See ``orchestrator.doctor``.
        #
        # We deliberately DO NOT memoize into ``app_state._cache``: the
        # operator expects a live-ish reading when they click "Re-run",
        # and the probes already cap themselves at ~5s each via
        # `preflight._PROBE_TIMEOUT_S`.
        from orchestrator.doctor import build_doctor_report

        def _loader(path: Path) -> dict[str, Any]:
            # Slim config loader — parse-only, no default injection. The
            # doctor's ``config.parse`` check is what surfaces a truly
            # unreadable YAML; defaults would mask real issues here.
            import yaml
            with open(path, encoding="utf-8") as fh:
                return yaml.safe_load(fh) or {}

        payload = build_doctor_report(app_state.paths, config_loader=_loader)
        return JSONResponse(payload)

    # ---- Architecture diagram (archify skill) ------------------------------
    # Sprint E-4: read-only accessors over docs/architecture/ + a POST
    # regenerate that fires `orch arch generate` as a fire-and-forget
    # subprocess. The CLI owns lock/dispatch/archive semantics — these
    # endpoints just surface state.
    import subprocess
    from orchestrator import arch as arch_mod
    from fastapi.responses import FileResponse

    def _arch_dir() -> Path:
        return app_state.paths.project_root / arch_mod.ARCH_DIR

    def _current_html_path() -> Path:
        return _arch_dir() / arch_mod.CURRENT_FILENAME

    def _load_arch_events():
        return arch_mod.read_events(app_state.paths.state_dir)

    def _current_source_hash() -> str | None:
        """Recompute the hash against the live artifact tree.

        Deliberately not cached: the dashboard needs to reflect edits to
        docs/prd/ or specs/ the moment they land so operators can see
        when the on-disk diagram has drifted from its sources.
        """
        sources = arch_mod.discover_sources(
            app_state.paths.project_root, app_state.paths.tasks_json
        )
        return arch_mod.compute_source_hash(sources)

    @app.get("/api/architecture/status", name="api_arch_status")
    def api_arch_status():
        current = _current_html_path()
        events = _load_arch_events()
        last = events[-1] if events else None
        exists = current.exists()
        lock = arch_mod.read_lock(app_state.paths.state_dir)
        return JSONResponse({
            "exists": exists,
            "generated_at": (last or {}).get("timestamp") if exists else None,
            "source_hash": _current_source_hash() if exists else None,
            "count": len(events),
            "last_cost_usd": (last or {}).get("cost_usd") if last else None,
            "regenerate_in_progress": lock is not None,
            # Progress fields: null when idle, populated when a run is live.
            # The frontend derives `elapsed_s` client-side from `started_at`
            # so the counter ticks smoothly between polls without a wall
            # of stale numbers.
            "phase": (lock or {}).get("phase") if lock else None,
            "phase_at": (lock or {}).get("phase_at") if lock else None,
            "started_at": (lock or {}).get("started_at") if lock else None,
        })

    @app.get("/api/architecture/current", name="api_arch_current")
    def api_arch_current():
        current = _current_html_path()
        if not current.exists():
            raise HTTPException(status_code=404, detail="no current architecture diagram")
        return FileResponse(str(current), media_type="text/html")

    @app.get("/api/architecture/history", name="api_arch_history")
    def api_arch_history():
        # 30 s TTL: history mutates on regeneration events only, not per
        # request. AppState.cached() defaults to 2 s so we manage the slot
        # directly to widen the window without disturbing other consumers.
        import time
        hit = app_state._cache.get("arch_history_30s")
        if hit and (time.monotonic() - hit[0]) < 30.0:
            return JSONResponse(hit[1])
        events = _load_arch_events()
        snapshots = [
            {
                "timestamp": ev.get("timestamp", ""),
                "source_hash": ev.get("source_hash", ""),
                "cost_usd": ev.get("cost_usd", 0.0),
                "model": ev.get("model", ""),
                "source_artifacts": ev.get("source_artifacts", {}),
            }
            for ev in events
        ]
        snapshots.sort(key=lambda r: r["timestamp"], reverse=True)
        payload = {"snapshots": snapshots}
        app_state._cache["arch_history_30s"] = (time.monotonic(), payload)
        return JSONResponse(payload)

    @app.get("/api/architecture/snapshot/{iso_ts}", name="api_arch_snapshot")
    def api_arch_snapshot(iso_ts: str):
        # `iso_ts` is the filename-safe token (colons replaced with dashes)
        # emitted by `arch.archive_current`. We look up any archive file
        # whose name starts with the token — the trailing `-<hash7>.html`
        # varies per source_hash.
        archive_dir = _arch_dir() / arch_mod.ARCHIVE_SUBDIR
        if not archive_dir.is_dir():
            raise HTTPException(status_code=404, detail="no snapshot archive")
        # Reject any traversal attempts before touching the FS.
        if "/" in iso_ts or ".." in iso_ts or iso_ts.startswith("."):
            raise HTTPException(status_code=404, detail="invalid snapshot id")
        matches = sorted(archive_dir.glob(f"{iso_ts}-*.html"))
        if not matches:
            raise HTTPException(status_code=404, detail=f"snapshot not found: {iso_ts}")
        return FileResponse(str(matches[0]), media_type="text/html")

    @app.post("/api/architecture/regenerate", name="api_arch_regenerate", status_code=202)
    def api_arch_regenerate():
        if arch_mod.read_lock(app_state.paths.state_dir) is not None:
            raise HTTPException(status_code=409, detail="regeneration already in progress")
        from datetime import datetime, timezone
        import shutil as _shutil
        import uuid as _uuid
        # Prefer the venv console-script — it uses the correct Python and
        # avoids the PEP 420 namespace-package trap where cwd=project_root
        # shadows the real `orchestrator` package with a scaffold dir that
        # only holds config.yaml/state/. Fall back to `python -m` for envs
        # where `orch` isn't on PATH.
        orch_bin = _shutil.which("orch")
        if orch_bin:
            cmd = [orch_bin, "arch", "generate",
                   "--project-root", str(app_state.paths.project_root)]
        else:
            cmd = [sys.executable, "-m", "orchestrator", "arch", "generate",
                   "--project-root", str(app_state.paths.project_root)]
        # Persist stdout+stderr to a log file so silent subprocess crashes are
        # debuggable. Previously DEVNULL swallowed ImportErrors — cost us
        # ~30 min hunting a namespace-package trap.
        log_path = app_state.paths.state_dir / "arch-generate.log"
        log_path.parent.mkdir(parents=True, exist_ok=True)
        log_handle = open(log_path, "ab")  # noqa: SIM115 — child owns fd
        try:
            subprocess.Popen(  # noqa: S603 — argv locally constructed
                cmd,
                stdout=log_handle,
                stderr=log_handle,
                start_new_session=True,
            )
        finally:
            log_handle.close()
        return JSONResponse(
            {
                "started_at": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
                "run_id": _uuid.uuid4().hex[:8],
            },
            status_code=202,
        )

    # ---- Tunnel supervisor (Sprint E-5) -----------------------------------
    # Three gates run in fixed order per TUN-3: config (404) → operator
    # profile (403) → loopback host (403). `/capabilities` is intentionally
    # auth-free so the SPA can decide whether to render the panel BEFORE
    # requesting a token (TUN-4).
    from orchestrator.dashboard.tunnel import (
        TunnelManagerConfig,
        evaluate_capabilities,
        require_loopback_host,
        require_operator_profile,
        require_tunnel_enabled,
        REASON_CONFIG_DISABLED,
        REASON_HOST_GATE,
        REASON_PROFILE_GATE,
    )

    # Short-vocab mapping for the `reasons` list per the design doc
    # (`disabled`/`not_operator`/`not_loopback`/`autossh_missing`). The
    # single `reason` field keeps the TUN-4 spec vocab.
    _CAPABILITY_REASON_SHORT = {
        REASON_CONFIG_DISABLED: "disabled",
        REASON_PROFILE_GATE: "not_operator",
        REASON_HOST_GATE: "not_loopback",
    }

    def _tunnel_manager_or_500():
        """Return the singleton manager or 500 — depends on `require_tunnel_enabled`
        having passed, so absence here is a wiring bug, not a config gate.
        """
        mgr = getattr(app_state, "tunnel_manager", None)
        if mgr is None:
            raise HTTPException(500, "tunnel manager not initialized")
        return mgr

    def _tunnel_manager_config() -> "TunnelManagerConfig":
        tcfg = app_state.config.tunnel
        return TunnelManagerConfig(
            provider=tcfg.provider,
            command=tcfg.command,
            args=tuple(tcfg.args or ()),
            url_regex=tcfg.url_regex,
            url_parse_timeout_s=int(tcfg.url_parse_timeout_s),
        )

    @app.get("/api/tunnel/capabilities", name="api_tunnel_capabilities")
    def api_tunnel_capabilities(request: Request):
        # Intentionally auth-free: always 200. When `tunnel.enabled: false`
        # the manager is absent and `evaluate_capabilities` short-circuits
        # on the config gate — no manager reference needed here.
        tcfg = getattr(app_state.config, "tunnel", None)
        enabled = bool(tcfg and tcfg.enabled)
        provider = (tcfg.provider if tcfg else None) if enabled else None
        can_control, reason = evaluate_capabilities(request, app_state.config)
        reasons: list[str] = []
        if not can_control:
            short = _CAPABILITY_REASON_SHORT.get(reason)
            if short:
                reasons.append(short)
        # Only consult PATH once every prior gate passes — matches the
        # gate-order guarantee in TUN-4.
        if can_control:
            import shutil as _sh
            binary = tcfg.command if tcfg else None
            if binary and _sh.which(binary) is None:
                can_control = False
                reason = "autossh_missing"
                reasons.append("autossh_missing")
        return JSONResponse({
            "enabled": enabled,
            "provider": provider,
            "can_control": can_control,
            "reason": reason,
            "reasons": reasons,
        })

    @app.get(
        "/api/tunnel/status",
        name="api_tunnel_status",
        dependencies=[
            Depends(require_tunnel_enabled),
            Depends(require_operator_profile),
            Depends(require_loopback_host),
        ],
    )
    def api_tunnel_status():
        mgr = _tunnel_manager_or_500()
        return JSONResponse(mgr.status())

    @app.post(
        "/api/tunnel/start",
        name="api_tunnel_start",
        status_code=202,
        dependencies=[
            Depends(require_tunnel_enabled),
            Depends(require_operator_profile),
            Depends(require_loopback_host),
        ],
    )
    def api_tunnel_start():
        mgr = _tunnel_manager_or_500()
        try:
            snap = mgr.start(_tunnel_manager_config())
        except RuntimeError as exc:
            msg = str(exc)
            if msg == "already_running":
                return JSONResponse(
                    {"error": "already_running", "state": mgr.status().get("state")},
                    status_code=409,
                )
            if msg == "locked":
                return JSONResponse({"error": "locked"}, status_code=409)
            raise HTTPException(500, f"start_failed:{msg}")
        return JSONResponse(snap, status_code=202)

    @app.post(
        "/api/tunnel/stop",
        name="api_tunnel_stop",
        status_code=202,
        dependencies=[
            Depends(require_tunnel_enabled),
            Depends(require_operator_profile),
            Depends(require_loopback_host),
        ],
    )
    def api_tunnel_stop():
        mgr = _tunnel_manager_or_500()
        try:
            snap = mgr.stop()
        except RuntimeError as exc:
            if str(exc) == "not_running":
                return JSONResponse({"error": "not_running"}, status_code=409)
            raise HTTPException(500, f"stop_failed:{exc}")
        return JSONResponse(snap, status_code=202)

    @app.get(
        "/api/tunnel/logs",
        name="api_tunnel_logs",
        dependencies=[
            Depends(require_tunnel_enabled),
            Depends(require_operator_profile),
            Depends(require_loopback_host),
        ],
    )
    async def api_tunnel_logs():
        import anyio
        mgr = _tunnel_manager_or_500()

        async def _emit():
            # Replay the current tail once; the manager owns redaction so
            # nothing here needs to touch token patterns (TUN-10).
            for line in mgr.logs_iter():
                yield f"data: {line}\n\n"
            cutoff = _get_sse_idle_cutoff_s()
            # Bounded live-tail: each cycle either yields new lines (resets
            # the deadline) or waits `poll_interval` before checking again;
            # after `cutoff` seconds of no activity we emit `retry:` and
            # close so an EventSource client cleanly reconnects (D3).
            deadline_task = anyio.current_time() + cutoff
            poll_interval = 0.5
            last_seen = list(mgr.logs_iter())
            last_len = len(last_seen)
            while True:
                snap = list(mgr.logs_iter())
                if len(snap) > last_len:
                    for line in snap[last_len:]:
                        yield f"data: {line}\n\n"
                    last_len = len(snap)
                    deadline_task = anyio.current_time() + cutoff
                if anyio.current_time() >= deadline_task:
                    yield "retry: 5000\n\n"
                    return
                # Also close cleanly when the child is gone AND the state
                # is idle — otherwise the loop would spin forever on a
                # long-dead tunnel with an empty buffer.
                st = mgr.status().get("state")
                if st == "idle":
                    yield "event: idle\ndata: {}\n\n"
                    return
                with anyio.move_on_after(poll_interval):
                    await anyio.sleep(poll_interval)

        return StreamingResponse(
            _emit(),
            media_type="text/event-stream",
            headers={"Cache-Control": "no-cache", "X-Accel-Buffering": "no"},
        )

    # ---- Tunnel auto_start startup handler (Sprint E-5, TUN-9) -----------
    # Sequence: bind → serve → self-probe GET / → spawn.
    # The startup event fires AFTER uvicorn binds but BEFORE serve begins
    # accepting; scheduling `asyncio.create_task` lets the event handler
    # return, uvicorn drops into its serve loop, and the task then probes
    # the socket that's now accepting. Blocking pieces (urlopen, subprocess
    # Popen) run through `asyncio.to_thread` so the event loop stays free.
    @app.on_event("startup")
    async def _tunnel_auto_start() -> None:  # noqa: D401
        import asyncio

        tcfg = getattr(app_state.config, "tunnel", None)
        if not tcfg or not tcfg.enabled or not tcfg.auto_start:
            return
        mgr = getattr(app_state, "tunnel_manager", None)
        if mgr is None:
            print(
                "[tunnel] auto_start_skipped: manager not initialized",
                file=sys.stderr,
            )
            return
        port = app_state.probe_port
        if port is None:
            print(
                "[tunnel] auto_start_skipped: probe port unknown",
                file=sys.stderr,
            )
            return
        timeout_s = float(tcfg.startup_probe_timeout_s or 3)
        asyncio.create_task(
            _run_auto_start(app_state, mgr, port, timeout_s)
        )

    # ---- Snapshot export ---------------------------------------------------
    @app.get("/snapshot", name="snapshot")
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
    @app.get("/partials/task-modal/{task_id}", response_class=HTMLResponse,
             name="partial_task_modal")
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
                    _tpl_ctx(request=request, task=row),
                )
        raise HTTPException(status_code=404, detail=f"task not found: {task_id}")

    @app.get("/partials/task-row/{task_id}", response_class=HTMLResponse,
             name="partial_task_row")
    def partial_task_row(request: Request, task_id: str):
        view = _load_project_view(app_state)
        for t in view["tasks"]:
            if t.id == task_id:
                row = _task_to_dict(t, view["human_hours"], view["last_updated"], view["impact"], view["critical_path"])
                row["parallelizable"] = t.id in view["parallelizable_ids"]
                return templates.TemplateResponse(
                    "partials/task_row.html",
                    _tpl_ctx(request=request, row=row),
                )
        raise HTTPException(status_code=404, detail=f"task not found: {task_id}")

    # ---- Stakeholder curated view -----------------------------------------
    def _stakeholder_payload() -> dict[str, Any]:
        """Build the sanitized payload shared by /stakeholder + /stakeholder/summary.

        Deliberately narrow: only phase progress, task counts, milestones,
        total spend (rounded up), and ETA. No per-model breakdown, no
        raw log content, no per-task exit codes. Verified by the security
        test suite in `test_dashboard_stakeholder_view.py`.
        """
        view = _load_project_view(app_state)
        spends = read_all_spends(paths.state_dir)
        total = total_cost(spends, pricing)
        return {
            "project_id": view["project_id"],
            "summary": view["summary"].as_dict(),
            "milestones": milestones_from_phases(view["tasks"]),
            "spend_rounded_usd": round_up_to_step(total, 0.50),
            "eta_hours": eta_hours_remaining(view["tasks"], view["human_hours"]),
            "refresh_interval_s": app_state.config.kanban.refresh_interval_s or 30,
        }

    @app.get("/stakeholder", response_class=HTMLResponse, name="stakeholder_index")
    def stakeholder_index(request: Request):
        payload = _stakeholder_payload()
        ctx = _tpl_ctx(
            request=request,
            project_id=payload["project_id"],
            project_root=paths.project_root,
            state_layout=paths.state_layout,
            summary=payload["summary"],
            milestones=payload["milestones"],
            spend_rounded_usd=payload["spend_rounded_usd"],
            eta_hours=payload["eta_hours"],
            refresh_interval_s=payload["refresh_interval_s"],
        )
        return templates.TemplateResponse("stakeholder.html", ctx)

    @app.get("/stakeholder/summary", name="stakeholder_summary_json")
    def stakeholder_summary_json():
        """JSON version of the curated view — same fields, no HTML."""
        return JSONResponse(_stakeholder_payload())

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
    profile: str | None = None,
    token: str | None = None,
) -> int:
    """Launch uvicorn in the foreground. Returns the process exit code.

    `profile` / `token` map 1:1 to the `--profile` / `--token` CLI flags.
    Both are optional — when omitted, DashboardConfig falls back to env +
    config.yaml + defaults.
    """
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

    app = create_app(
        paths=paths,
        profile_override=profile,
        token_override=token,
        probe_port=port,
    )
    resolved = app.state.app_state.config

    banner_lines = [
        f"Orch dashboard running on http://{host}:{port}",
        f"Project: {paths.project_id} ({paths.project_root})",
        f"State dir: {paths.state_dir}",
        f"Profile: {resolved.profile}",
    ]
    if resolved.profile != PROFILE_OPERATOR:
        banner_lines.append(
            "Token auth: ENABLED"
            if resolved.token
            else "Token auth: MISCONFIGURED (no token set — every request 401s)"
        )
    banner_lines.append("Ctrl+C to stop")
    print("\n".join(banner_lines))

    uvicorn.run(app, host=host, port=port, reload=reload, log_level="info")
    return 0
