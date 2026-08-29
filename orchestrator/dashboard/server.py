"""FastAPI application for the orchestrator dashboard.

Read-only surface. Never mutates `tasks.json` or state files.

Endpoints:
    GET  /                   — React SPA (mounted last as a StaticFiles fallback)
    GET  /logs/stream        — SSE feed of live events (text/event-stream)
    GET  /api/tasks          — JSON dump of all tasks + filters applied
    GET  /api/task/{id}      — JSON detail for a single task
    GET  /api/metrics        — JSON dump of metrics (models + days + total)
    GET  /api/events/stream  — SSE feed (JSON payloads), alias of /logs/stream
    GET  /api/events         — JSON list of recent events
    GET  /api/config         — dashboard config snapshot
    GET  /api/doctor         — doctor probe payload
    GET  /api/architecture/* — architecture snapshot endpoints
    GET  /api/tunnel/*       — tunnel supervisor endpoints
    GET  /snapshot           — full JSON dump of project state
    GET  /stakeholder/summary — curated JSON payload for stakeholder profile

Design:
    - `AppState` holds `ProjectPaths` + a lazily-loaded `PricingTable`. Every
      request re-reads `tasks.json` and the JSONL logs from disk. That's
      acceptable at MVP scale (334 tasks, 20 event files ~10KB each) and
      keeps the code stateless / correct as new events land during a run.
    - The React SPA (frontend/) is the sole UI — mounted at `/` so
      `/`, `/kanban`, `/metrics`, `/logs`, `/stakeholder`, etc. all resolve
      to the SPA's `index.html` (React Router owns the client route).
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
    executive_summary,
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
from orchestrator.state import get_backend as _get_state_backend, load_tasks


# Sprint E-5: server-side idle cutoff for `/api/tunnel/logs` (TUN-10 / D3).
# Module-level so tests can shrink it; the endpoint reads through
# `_get_sse_idle_cutoff_s()` on every stream open.
TUNNEL_SSE_IDLE_CUTOFF_S = 30 * 60


def _get_sse_idle_cutoff_s() -> float:
    return float(TUNNEL_SSE_IDLE_CUTOFF_S)


def _load_tasks_hydrated(paths: ProjectPaths) -> list:
    """Load tasks.json and overlay live statuses from the state backend.

    Sprint F-8 (fix #72): every dashboard endpoint that ships task rows must
    read runtime status from the backend, not from the stale `tasks.json`
    seed. `_load_tasks` inside `_render_context` already did this locally;
    F-8 extracts the same logic as a module-level helper so `/api/milestones`,
    `/api/sprint`, and `/api/summary` share the single source of truth
    instead of leaking `tasks.json` statuses into their payloads.
    """
    try:
        tasks = load_tasks(paths.tasks_json)
    except (OSError, ValueError):
        return []
    try:
        import yaml
        from dataclasses import replace as _replace
        raw_cfg: dict = {}
        try:
            raw_cfg = yaml.safe_load(paths.config_yaml.read_text(encoding="utf-8")) or {}
        except Exception:  # noqa: BLE001
            pass
        if isinstance(raw_cfg, dict):
            runtime = _get_state_backend(paths, raw_cfg).get_all_task_status()
            if runtime:
                tasks = [
                    _replace(t, status=runtime[t.id]) if t.id in runtime else t
                    for t in tasks
                ]
    except Exception:  # noqa: BLE001 — fall back to tasks.json statuses
        pass
    return tasks


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


def _resolve_spa_dist(
    project_root: Path, package_dir: Path | None = None
) -> tuple[Path, str] | None:
    """Return (path, source_label) or None if no SPA build is available.

    Resolution order:
      1. --spa-dist CLI flag (if wired in the future — currently unused)
      2. <project_root>/frontend/dist/  (project-specific build)
      3. <orchestrator package>/spa/    (build shipped in the wheel)

    `package_dir` is the `orchestrator/` package directory. Defaulting to
    `Path(__file__).parent.parent` locates the packaged SPA next to the
    dashboard module in every install layout setuptools produces. It's
    exposed as a parameter purely for tests that need to point the
    "packaged" tier at a tmp fixture without patching `__file__`.
    """
    # 2. Project-specific
    project_dist = project_root / "frontend" / "dist"
    if project_dist.is_dir() and (project_dist / "index.html").is_file():
        return project_dist, "project"

    # 3. Packaged
    if package_dir is None:
        package_dir = Path(__file__).parent.parent
    package_dist = package_dir / "spa"
    if package_dist.is_dir() and (package_dist / "index.html").is_file():
        return package_dist, "packaged"

    return None


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
    # F-8: shared helper — SQLite runtime status overlaid on tasks.json.
    tasks = state.cached("tasks", lambda: _load_tasks_hydrated(paths))
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
            if q_low in t.id.lower()
            or q_low in (t.title or "").lower()
            or q_low in (t.description or "").lower()
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
                # G-5: opt-in flag to show the spend/budget view to stakeholders.
                # Off by default — spend is sensitive.
                "show_spend_to_stakeholder": bool(
                    dashboard_raw.get("show_spend_to_stakeholder", False)
                ),
            },
            "presentation": {
                "status_labels": raw.get("presentation", {}).get("status_labels") or {},
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
    config: str = ".orchestrator/config.yaml",
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
    from fastapi.responses import JSONResponse, StreamingResponse
    from fastapi.staticfiles import StaticFiles

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

    app = FastAPI(
        title="Orch Dashboard",
        description="Read-only view over tasks.json / events / spend.",
        version="0.1.0",
    )
    app.state.app_state = app_state

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

    # Route names are load-bearing: `ProfileGuardMiddleware` consults them
    # against `DashboardConfig.stakeholder_routes` to decide 200 vs 403.
    # Every JSON/SSE route below MUST declare `name=` or it stays
    # operator-only. The React SPA (mounted at `/` at the end of this
    # function) is the only user-facing surface — every HTML/Jinja route
    # from the legacy dashboard has been removed.

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

    @app.get("/api/graph", name="api_graph")
    def api_graph():
        """Return the task dependency graph as nodes + edges.

        Operator-only. The Graph page in the SPA uses this to render a visual
        DAG view of the task dependency tree via Mermaid.js (CDN).

        Response shape:
            {
                "nodes": [{"id": "T-1", "label": "…", "status": "done",
                           "phase": 1, "on_critical_path": true}],
                "edges": [{"source": "T-1", "target": "T-2"}]
            }
        """
        view = _load_project_view(app_state)
        critical_ids = view["critical_path"]
        nodes = [
            {
                "id": t.id,
                "label": t.title or t.id,
                "status": t.status,
                "phase": t.phase,
                "on_critical_path": t.id in critical_ids,
            }
            for t in view["tasks"]
        ]
        edges = [
            {"source": dep, "target": t.id}
            for t in view["tasks"]
            for dep in t.dependencies
        ]
        return JSONResponse({"nodes": nodes, "edges": edges})

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

    @app.get("/api/budget/summary", name="api_budget_summary")
    def api_budget_summary():
        """Budget vs actual (G-5): configured per-provider token budget vs
        rolling-window tokens used + USD spent. Requires budgets.yaml.

        Reuses BudgetGate.snapshot() for the in-window token sums (single
        source of truth with the dispatch gate) and spend_reader for the
        informational USD figure. `pct`/`over_threshold` are token-based —
        the config has no USD limit, so we never invent one.
        """
        import os
        from orchestrator.dashboard.metrics import budget_vs_actual
        from orchestrator.spend_reader import (
            aggregate_by_provider,
            iter_today_entries,
        )

        preset = os.environ.get("ORCH_BUDGETS_PRESET") or "conservative"
        budget_cfg = None
        for candidate in (
            paths.config_yaml.parent / "budgets.yaml",
            paths.project_root / "budgets.yaml",
        ):
            try:
                budget_cfg = load_budget_config(candidate, preset=preset)
            except ValueError:
                budget_cfg = None
            if budget_cfg is not None:
                break
        if budget_cfg is None or not budget_cfg.providers:
            return JSONResponse({"available": False, "rows": []})

        gate = BudgetGate(state_dir=paths.state_dir, config=budget_cfg)
        snap = gate.snapshot()
        used = {p: int(v.get("tokens_used", 0)) for p, v in snap.items()}
        cost = aggregate_by_provider(iter_today_entries(paths.state_dir))
        rows = budget_vs_actual(
            budget_cfg, used_by_provider=used, cost_by_provider=cost
        )
        return JSONResponse({"available": True, "rows": rows})

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

    @app.get("/api/milestones", name="api_milestones")
    def api_milestones():
        """Return all milestones with task progress. Requires SQLite backend."""
        import yaml
        from orchestrator.state.sqlite_backend import SqliteBackend

        cfg_path = app_state.paths.config_yaml
        try:
            raw_cfg = yaml.safe_load(cfg_path.read_text(encoding="utf-8")) or {}
        except (OSError, ValueError, yaml.YAMLError):
            raw_cfg = {}

        backend = _get_state_backend(app_state.paths, raw_cfg)
        if not isinstance(backend, SqliteBackend):
            return JSONResponse({"milestones": [], "backend": "file"})

        # G-3: attach a tasks-based ETA per milestone. Velocity is the same
        # project-wide rolling-7d figure /api/sprint uses; applied to each
        # milestone's remaining task count.
        from datetime import datetime, timezone
        from orchestrator.dashboard.metrics import milestone_eta, sprint_health

        milestones = backend.get_milestones()
        tasks = _load_tasks_hydrated(app_state.paths)
        done_7d = backend.count_done_last_n_days(7)
        velocity = sprint_health(tasks, done_7d, {}).get("velocity_per_day", 0.0)
        today = datetime.now(timezone.utc).date().isoformat()
        for m in milestones:
            remaining = m["progress"]["total"] - m["progress"]["done"]
            m["eta"] = milestone_eta(
                remaining=remaining,
                velocity_per_day=velocity,
                today=today,
                target_date=m.get("target_date"),
            )
        return JSONResponse({"milestones": milestones})

    @app.get("/api/sprint", name="api_sprint")
    def api_sprint():
        """Sprint health: velocity, ETA, blockers. Requires SQLite backend."""
        import yaml
        from orchestrator.state.sqlite_backend import SqliteBackend
        from orchestrator.dashboard.metrics import sprint_health

        cfg_path = app_state.paths.config_yaml
        try:
            raw_cfg = yaml.safe_load(cfg_path.read_text(encoding="utf-8")) or {}
        except (OSError, ValueError, yaml.YAMLError):
            raw_cfg = {}

        backend = _get_state_backend(app_state.paths, raw_cfg)
        if not isinstance(backend, SqliteBackend):
            return JSONResponse({"available": False, "reason": "file_backend"})

        tasks = _load_tasks_hydrated(app_state.paths)
        done_7d = backend.count_done_last_n_days(7)
        blocked_ids = [t.id for t in tasks if t.status == "blocked"]
        last_events = backend.get_task_last_events(blocked_ids) if blocked_ids else {}

        payload = sprint_health(tasks, done_7d, last_events)
        payload["available"] = True
        return JSONResponse(payload)

    @app.get("/api/summary", name="api_summary")
    def api_summary():
        """Deterministic executive summary from sprint health + spend (G-4).

        NO LLM — every sentence is a template filled from real figures, so it
        works headless (no `claude` CLI needed on the host), costs nothing, and
        is fully deterministic. Requires SQLite (sprint_health source).
        """
        import yaml
        from orchestrator.state.sqlite_backend import SqliteBackend
        from orchestrator.dashboard.metrics import sprint_health
        from orchestrator.spend_reader import (
            aggregate_by_provider,
            iter_today_entries,
        )

        cfg_path = app_state.paths.config_yaml
        try:
            raw_cfg = yaml.safe_load(cfg_path.read_text(encoding="utf-8")) or {}
        except (OSError, ValueError, yaml.YAMLError):
            raw_cfg = {}
        language = (raw_cfg.get("dashboard") or {}).get("summary_language") or "es"

        backend = _get_state_backend(app_state.paths, raw_cfg)
        if not isinstance(backend, SqliteBackend):
            return JSONResponse({"available": False, "summary": None})

        tasks = _load_tasks_hydrated(app_state.paths)
        done_7d = backend.count_done_last_n_days(7)
        blocked_ids = [t.id for t in tasks if t.status == "blocked"]
        last_events = (
            backend.get_task_last_events(blocked_ids) if blocked_ids else {}
        )
        health = sprint_health(tasks, done_7d, last_events)

        spend = aggregate_by_provider(iter_today_entries(app_state.paths.state_dir))
        total_spend = round(sum(spend.values()), 2) if spend else None

        done = int(health.get("done_count", 0))
        remaining = int(health.get("remaining_tasks", 0))
        in_progress = sum(1 for t in tasks if t.status in ("in_progress", "in-progress"))
        blocked_reasons = [
            f"• {b.get('title', b.get('task_id'))}: {b.get('reason', '')}".strip()
            for b in health.get("blockers", [])
            if b.get("reason")
        ]
        summary = executive_summary(
            done=done,
            total=done + remaining,
            in_progress=in_progress,
            blocked=int(health.get("blocked_count", 0)),
            blocked_reasons=blocked_reasons,
            eta_date=health.get("eta_date"),
            total_spend_usd=total_spend,
            language=language,
        )
        return JSONResponse({"available": True, "summary": summary})

    @app.get("/api/config", name="api_config")
    def api_config():
        return JSONResponse(_load_project_config(app_state))

    @app.get("/api/config/status", name="api_config_status")
    def api_config_status():
        """Check if the project configuration is complete.

        Returns whether a setup wizard needs to be shown. A project is considered
        "setup" when it has required config fields populated.
        """
        import json
        try:
            config_data = _load_project_config(app_state)
            tasks_data = app_state.paths.tasks_json.read_text(encoding="utf-8")
            tasks_json = json.loads(tasks_data)
            meta = tasks_json.get("meta", {})
            project_id = meta.get("project")

            is_setup = bool(
                project_id
                and config_data.get("spec_root")
                and config_data.get("state", {}).get("backend")
                and config_data.get("budgets", {}).get("preset")
            )
            return JSONResponse({
                "is_setup": is_setup,
                "project_id": project_id,
                "spec_root": config_data.get("spec_root"),
                "backend": config_data.get("state", {}).get("backend"),
                "budget_preset": config_data.get("budgets", {}).get("preset"),
            })
        except Exception as e:
            return JSONResponse({"is_setup": False, "error": str(e)})

    @app.post("/api/config/setup", name="api_config_setup")
    async def api_config_setup(request: Request):
        """Save the initial setup configuration from the wizard."""
        try:
            import json
            import yaml
            from pathlib import Path

            body = await request.json()

            # Validate required fields
            if not body.get("project_id") or not body.get("project_root"):
                raise HTTPException(
                    status_code=400,
                    detail="project_id and project_root are required"
                )

            project_root = Path(body.get("project_root")).expanduser().resolve()
            config_yaml = project_root / "orchestrator" / "config.yaml"
            tasks_json = project_root / "tasks.json"

            # Update tasks.json meta
            if tasks_json.exists():
                tasks_data = json.loads(tasks_json.read_text(encoding="utf-8"))
                meta = tasks_data.setdefault("meta", {})
                meta["project"] = body.get("project_id")
                # Add model tier choices if provided
                for tier in ["premium", "standard", "cheap"]:
                    if body.get("model_choices", {}).get(tier):
                        meta[f"default_{tier}_model"] = body["model_choices"][tier]
                tasks_json.write_text(json.dumps(tasks_data, indent=2) + "\n")

            # Update config.yaml
            if config_yaml.exists():
                config_data = yaml.safe_load(config_yaml.read_text(encoding="utf-8")) or {}

                # Update state backend
                if "state" not in config_data:
                    config_data["state"] = {}
                config_data["state"]["backend"] = body.get("state_backend", "file")

                # Update budget preset
                config_data["budgets_preset"] = body.get("budget_preset", "conservative")

                # Update spec root
                config_data["spec_root"] = body.get("spec_root", "specs")

                config_yaml.write_text(yaml.dump(config_data, default_flow_style=False))

            return JSONResponse({"success": True, "message": "Configuration saved successfully"})
        except HTTPException:
            raise
        except Exception as e:
            raise HTTPException(
                status_code=500,
                detail=f"Setup failed: {str(e)}"
            )

    @app.get("/api/whoami", name="api_whoami")
    def api_whoami():
        """Return the resolved dashboard profile for the current session.

        Sprint E-6: the SPA uses this to hide operator-only nav items
        (Doctor, Tunnel, Metrics, Logs) when running in stakeholder mode.
        Deliberately NOT part of `/api/config` — that endpoint is on the
        operator-only path and its payload would leak orchestrator knobs
        (budgets, findings repo, state backend) to a stakeholder session.
        """
        return JSONResponse({"profile": app_state.config.profile})

    # ------------------------------------------------------------------
    # Documents API — read-only markdown file browser for stakeholders
    # ------------------------------------------------------------------
    _DOC_ROOTS = ("docs", "specs", "openspec")
    _DOC_CATEGORY_LABELS: dict[str, str] = {
        "docs": "Docs",
        "specs": "Specs",
        "openspec": "OpenSpec",
    }
    _MAX_DOC_BYTES = 512 * 1024  # 512 KB hard cap per file

    def _extract_doc_title(path: Path) -> str:
        """Return the first # heading in a markdown file or the stem."""
        try:
            for line in path.open(encoding="utf-8", errors="replace"):
                stripped = line.strip()
                if stripped.startswith("# "):
                    return stripped[2:].strip()
                if stripped:
                    break  # non-blank, non-heading → fall through
        except OSError:
            pass
        return path.stem.replace("-", " ").replace("_", " ").title()

    def _is_safe_doc_path(rel: str, root: Path) -> bool:
        """True iff rel resolves inside root and ends with .md."""
        if not rel.endswith(".md"):
            return False
        try:
            resolved = (root / rel).resolve()
            return resolved.is_relative_to(root.resolve())
        except (ValueError, OSError):
            return False

    @app.get("/api/docs", name="api_docs_list")
    def api_docs_list():
        """List all markdown documents grouped by top-level directory.

        Scans docs/, specs/, and openspec/ under the project root.
        Returns a JSON array of document descriptors. This endpoint is on
        the stakeholder allow-list so operators and stakeholders alike can
        browse project artefacts.
        """
        import os as _os

        root = app_state.paths.project_root
        items: list[dict[str, Any]] = []
        for doc_root in _DOC_ROOTS:
            base = root / doc_root
            if not base.is_dir():
                continue
            category_label = _DOC_CATEGORY_LABELS.get(doc_root, doc_root.title())
            for dirpath, _dirs, filenames in _os.walk(base):
                for fname in sorted(filenames):
                    if not fname.endswith(".md"):
                        continue
                    full = Path(dirpath) / fname
                    try:
                        stat = full.stat()
                    except OSError:
                        continue
                    rel = full.relative_to(root).as_posix()
                    # sub-category: second path component (e.g. docs/prd → "prd")
                    parts = Path(rel).parts
                    sub = parts[1].upper() if len(parts) > 2 else category_label
                    items.append({
                        "path": rel,
                        "title": _extract_doc_title(full),
                        "category": category_label,
                        "sub_category": sub,
                        "size_bytes": stat.st_size,
                        "modified_iso": __import__("datetime").datetime.fromtimestamp(
                            stat.st_mtime,
                            tz=__import__("datetime").timezone.utc,
                        ).isoformat(),
                    })
        return JSONResponse({"docs": items})

    @app.get("/api/docs/content", name="api_docs_content")
    def api_docs_content(path: str):
        """Return the raw markdown content of a project document.

        `path` must be relative to the project root and resolve within it
        (path-traversal guard). Only .md files are served. Content is
        returned as plain text so the SPA can render it with any MD library.
        """
        from fastapi.responses import PlainTextResponse

        root = app_state.paths.project_root
        if not _is_safe_doc_path(path, root):
            from fastapi import HTTPException
            raise HTTPException(status_code=400, detail="invalid document path")
        full = root / path
        try:
            data = full.read_bytes()
        except OSError:
            from fastapi import HTTPException
            raise HTTPException(status_code=404, detail="document not found")
        if len(data) > _MAX_DOC_BYTES:
            from fastapi import HTTPException
            raise HTTPException(status_code=413, detail="document too large")
        return PlainTextResponse(data.decode("utf-8", errors="replace"))

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

    _ARCH_ASSET_EXTS = {".html", ".css", ".js", ".svg", ".png", ".jpg", ".json"}

    @app.get("/api/architecture/assets/{path:path}", name="api_arch_assets")
    def api_arch_assets(path: str):
        if path.startswith("."):
            raise HTTPException(status_code=404, detail="invalid asset path")
        suffix = Path(path).suffix.lower()
        if suffix not in _ARCH_ASSET_EXTS:
            raise HTTPException(status_code=404, detail="file type not allowed")
        arch = _arch_dir()
        if not arch.exists():
            raise HTTPException(status_code=404, detail="no architecture directory")
        # resolve() + relative_to() guards against symlink-based traversal.
        # URL-encoded `..` segments are normalized by Starlette before routing.
        try:
            target = (arch / path).resolve()
            target.relative_to(arch.resolve())
        except ValueError:
            raise HTTPException(status_code=404, detail="invalid asset path")
        if not target.exists():
            raise HTTPException(status_code=404, detail="asset not found")
        return FileResponse(str(target))

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

    # ---- Stakeholder curated view -----------------------------------------
    def _stakeholder_payload() -> dict[str, Any]:
        """Build the sanitized payload consumed by /stakeholder/summary.

        Deliberately narrow: only phase progress, task counts, milestones,
        total spend (rounded up), ETA, spend-by-day sparkline, phase timeline,
        and a computed executive summary. No per-model breakdown, no raw log
        content, no per-task exit codes.
        """
        import datetime as _dt

        view = _load_project_view(app_state)
        tasks = list(view["tasks"])
        spends = read_all_spends(paths.state_dir)
        total = total_cost(spends, pricing)
        eta_h = eta_hours_remaining(tasks, view["human_hours"])

        # ---- phase timeline --------------------------------------------------
        # For each phase: task counts by status + estimate hours.
        # The SPA renders this as proportional horizontal bars (Gantt-like).
        # We use the task JSON phases list to get phase names when available.
        raw_tasks_json: dict[str, Any] = {}
        try:
            import json as _json
            raw_tasks_json = _json.loads(paths.project_root.joinpath("tasks.json").read_text(encoding="utf-8"))
        except (OSError, ValueError):
            pass
        phase_name_map: dict[int, str] = {
            int(p.get("id", 0)): str(p.get("name", f"Phase {p.get('id', 0)}"))
            for p in raw_tasks_json.get("phases", [])
            if isinstance(p, dict)
        }

        per_phase: dict[int, dict[str, Any]] = {}
        for t in tasks:
            p = t.phase
            if p not in per_phase:
                per_phase[p] = {
                    "phase": p,
                    "name": phase_name_map.get(p, f"Phase {p}"),
                    "total": 0, "done": 0, "in_progress": 0,
                    "blocked": 0, "backlog": 0,
                    "estimate_hours": 0.0,
                }
            row = per_phase[p]
            row["total"] += 1
            row["estimate_hours"] = round(row["estimate_hours"] + (t.estimate_hours or 0.0), 2)
            s = t.status
            if s == "done":
                row["done"] += 1
            elif s == "in-progress":
                row["in_progress"] += 1
            elif s == "blocked":
                row["blocked"] += 1
            else:
                row["backlog"] += 1

        phases_timeline = [
            {**row, "pct_done": round(row["done"] / row["total"] * 100) if row["total"] else 0}
            for row in (per_phase[k] for k in sorted(per_phase))
        ]

        # ---- spend by day (last 14 days) ------------------------------------
        # Grouped daily totals — safe to show because we never break down by model.
        cutoff = (_dt.datetime.now(_dt.timezone.utc) - _dt.timedelta(days=14)).date()
        daily: dict[str, float] = {}
        for s in spends:
            ts_str = str(s.get("ts", ""))[:10]
            try:
                day = _dt.date.fromisoformat(ts_str)
            except ValueError:
                continue
            if day < cutoff:
                continue
            cost = float(s.get("cost_usd", 0) or 0)
            daily[ts_str] = daily.get(ts_str, 0.0) + cost
        spend_by_day = [
            {"date": d, "cost": round(v, 4)}
            for d, v in sorted(daily.items())
        ]

        # ---- executive summary (computed, no LLM) ---------------------------
        # G-4: single source of truth — metrics.executive_summary(). Language
        # from dashboard.summary_language (default es). The inline template that
        # lived here (Sprint E-7) was folded into that tested helper.
        summ = view["summary"]
        blocked_reasons: list[str] = []
        for t in tasks:
            if t.status == "blocked" and t.comments:
                body = t.comments[0].get("body", "").strip()
                if body:
                    blocked_reasons.append(f"• {t.title}: {body[:120]}")
        import yaml
        try:
            _raw_cfg = yaml.safe_load(
                paths.config_yaml.read_text(encoding="utf-8")
            ) or {}
        except (OSError, ValueError, yaml.YAMLError):
            _raw_cfg = {}
        _summary_lang = (_raw_cfg.get("dashboard") or {}).get("summary_language") or "es"
        exec_summary = executive_summary(
            done=getattr(summ, "done", 0),
            total=getattr(summ, "total", 0),
            in_progress=getattr(summ, "in_progress", 0),
            blocked=getattr(summ, "blocked", 0),
            blocked_reasons=blocked_reasons,
            eta_hours=eta_h,
            total_spend_usd=round_up_to_step(total, 0.50),
            language=_summary_lang,
        )["text"]

        return {
            "project_id": view["project_id"],
            "summary": summ.as_dict(),
            "milestones": milestones_from_phases(tasks),
            "spend_rounded_usd": round_up_to_step(total, 0.50),
            "eta_hours": eta_h,
            "refresh_interval_s": app_state.config.kanban.refresh_interval_s or 30,
            # New fields (Sprint E-7):
            "phases_timeline": phases_timeline,
            "spend_by_day": spend_by_day,
            "exec_summary": exec_summary,
        }

    @app.get("/stakeholder/summary", name="stakeholder_summary_json")
    def stakeholder_summary_json():
        """JSON version of the curated view — consumed by the SPA."""
        return JSONResponse(_stakeholder_payload())

    # ---- SPA mount (root) --------------------------------------------------
    # Serve the compiled Vite React SPA at `/`. Since removing the legacy
    # Jinja dashboard, the SPA is the ONLY user-facing surface — every
    # non-API URL (`/`, `/kanban`, `/metrics`, `/logs`, `/stakeholder`, …)
    # is a client-side route owned by React Router.
    #
    # `html=True` on the base StaticFiles is not enough for a React Router
    # BrowserRouter: it only returns `index.html` when the URL resolves to
    # a directory (e.g. `/`), NOT for arbitrary nested client routes
    # (e.g. `/kanban`). We subclass to make ANY 404 under `/` fall back
    # to `index.html` so hard-reloads on a client route work. Real 404s
    # for missing bundled assets under `/assets/*` stay as 404 — that
    # matches the standard SPA-hosting contract (nginx `try_files $uri
    # /index.html;`).
    #
    # The mount is OPTIONAL. If `frontend/dist/` doesn't exist we just log
    # a friendly note to stderr and keep booting — the JSON APIs stay
    # available so integrations and tests still work in a bare-source
    # checkout without a Node toolchain.
    #
    # NOTE: this MUST live at the end of `create_app()`, AFTER every
    # `@app.get(...)`. FastAPI resolves routes in registration order, so
    # placing it last keeps the specific JSON/SSE routes winning over the
    # catch-all StaticFiles mount that answers everything else.
    resolved = _resolve_spa_dist(paths.project_root)
    if resolved is not None:
        spa_dist, source = resolved
        from starlette.exceptions import HTTPException as StarletteHTTPException
        from starlette.responses import FileResponse
        from starlette.types import Scope

        # Paths that should NEVER fall back to the SPA index.html —
        # if the request landed on the SPA mount it means every earlier
        # explicit route missed, and for these prefixes a real 404 is
        # the correct answer (not a masking HTML shell). Includes:
        #   - `assets/*` : bundled JS/CSS — a missing chunk is a build bug.
        #   - `api/*`    : operator JSON APIs — a 404 here means bad path
        #                  or path-parameter validation failure; masking
        #                  it with HTML would hide bugs from callers.
        #   - `snapshot`, `stakeholder/summary`, `logs/stream`,
        #     `docs`, `openapi.json`, `redoc` — same reasoning.
        _NO_FALLBACK_PREFIXES = (
            "assets/", "assets",
            "api/", "api",
            "snapshot", "logs/stream",
            "stakeholder/summary",
            "docs", "openapi.json", "redoc",
        )

        class SPAStaticFiles(StaticFiles):
            """StaticFiles subclass with true SPA fallback semantics.

            When a GET under the mount 404s, return `index.html` (200) so
            React Router can pick up the client-side route. Any other
            error propagates unchanged. Bundled asset and JSON-API paths
            are excluded from the fallback so a real 404 stays a 404
            (masking bugs behind an HTML shell would break integrations
            and hide build breakage).
            """

            async def get_response(self, path: str, scope: Scope):
                try:
                    return await super().get_response(path, scope)
                except StarletteHTTPException as exc:
                    if exc.status_code != 404:
                        raise
                    # See `_NO_FALLBACK_PREFIXES` above.
                    for prefix in _NO_FALLBACK_PREFIXES:
                        if path == prefix or path.startswith(prefix + "/") or path == prefix.rstrip("/"):
                            raise
                    index = Path(self.directory) / "index.html"
                    if not index.is_file():
                        raise
                    return FileResponse(str(index))

        app.mount(
            "/",
            SPAStaticFiles(directory=str(spa_dist), html=True),
            name="spa",
        )
        print(f"SPA mounted at / → {spa_dist} ({source})", file=sys.stderr)
    else:
        print(
            "SPA not mounted — run `pnpm build` in frontend/ (project-specific) "
            "or reinstall via `pipx install --force ...` (packaged). "
            "JSON APIs remain available.",
            file=sys.stderr,
        )

    return app


# ---- CLI runner ------------------------------------------------------------


def run(
    *,
    port: int = 7420,
    host: str = "127.0.0.1",
    project_root: str | None = None,
    project_id: str | None = None,
    config: str = ".orchestrator/config.yaml",
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
    ]
    if host in ("0.0.0.0", "::"):
        import socket as _socket
        try:
            lan_ip = _socket.gethostbyname(_socket.gethostname())
        except OSError:
            lan_ip = None
        if lan_ip and lan_ip != "127.0.0.1":
            banner_lines.append(f"  LAN/VPN: http://{lan_ip}:{port}")
        banner_lines.append(f"  localhost: http://127.0.0.1:{port}")
    banner_lines += [
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
