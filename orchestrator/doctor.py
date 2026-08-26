"""Shared aggregator for `orch doctor` output.

Sprint E-3 dashboard SPA — extracted from ``orchestrator.orch._run_doctor_subcommand``
so the dashboard's ``GET /api/doctor`` endpoint can produce the same JSON payload
without spawning a subprocess per request.

Pure data assembly on top of :mod:`orchestrator.preflight` — no argparse, no
stdout, no logging. The CLI wrapper in ``orch.py`` calls this and either prints
the JSON verbatim or hands the payload to ``_render_doctor_report``.

The returned dict has the shape documented by the SPA type contract:

    {
        "project": {"id": str, "root": str},
        "backend": "file" | "sqlite",
        "checks": [{"name": str, "status": str, "detail": str, "remediation": str | None}, ...],
        "summary": {"ok": int, "warn": int, "error": int, "skip": int},
        "exit_code": int,
    }
"""

from __future__ import annotations

from pathlib import Path
from typing import Any, Callable

from . import preflight
from .paths import ProjectPaths


ConfigLoader = Callable[[Path], dict[str, Any]]


def _resolve_budgets_path(paths: ProjectPaths, cfg: dict[str, Any]) -> Path | None:
    """Mirror the resolution rules from the CLI for `budgets.yaml`.

    Order of precedence:
        1. Absolute path in config → use as-is.
        2. Relative → try project_root first, then orchestrator/ subdir.

    Kept in sync with ``orchestrator.orch._resolve_budgets_path``.
    """
    raw = cfg.get("budgets_config") or "budgets.yaml"
    candidate = Path(raw)
    if candidate.is_absolute():
        return candidate
    for root in (paths.project_root, paths.project_root / "orchestrator"):
        p = (root / candidate).resolve()
        if p.exists():
            return p
    return (paths.project_root / candidate).resolve()


def _resolve_sqlite_path(paths: ProjectPaths, cfg: dict[str, Any]) -> Path | None:
    """Resolve ``state.sqlite_path`` (relative to state_dir, or absolute).

    Kept in sync with ``orchestrator.orch._resolve_sqlite_path``.
    """
    raw = ((cfg.get("state") or {}) or {}).get("sqlite_path")
    if not raw:
        return paths.state_dir / "orch.db"
    candidate = Path(raw)
    if candidate.is_absolute():
        return candidate
    return (paths.state_dir / candidate).resolve()


def build_doctor_report(
    paths: ProjectPaths,
    *,
    config_loader: ConfigLoader,
    only: str | None = None,
) -> dict[str, Any]:
    """Run every doctor check and return the JSON payload.

    ``config_loader`` is the callable that parses ``config.yaml`` — the CLI
    passes its ``_load_config`` (which fills defaults + raises on missing file);
    tests / callers that want a leaner path can pass a slimmer loader. When it
    raises, the doctor emits a ``config.parse`` error check and proceeds with
    an empty config so the rest of the environment still gets probed.

    ``only`` is the same substring filter as the CLI's ``--only`` flag.
    """
    checks: list[preflight.CheckResult] = []

    try:
        cfg = config_loader(paths.config_yaml)
    except Exception:  # noqa: BLE001 — every load path counts as parse error
        cfg = {}

    backend_kind = str(((cfg.get("state") or {}) or {}).get("backend", "file"))
    budgets_preset = cfg.get("budgets_preset")
    budgets_path = _resolve_budgets_path(paths, cfg)

    # Always run check_config_files so individual file checks (config.parse,
    # router.parse, budgets.parse, tasks.parse) are always reported, even when
    # the config_loader raised (e.g. a malformed override file — the error
    # should surface as router.parse, not as a generic config.parse).
    checks.extend(
        preflight.check_config_files(
            config_yaml=paths.config_yaml,
            budgets_yaml=budgets_path,
            router_yaml=paths.router_yaml,
            tasks_json=paths.tasks_json,
            budgets_preset=budgets_preset,
        )
    )

    # Scripts + jq.
    checks.extend(preflight.check_scripts(paths.scripts_dir))

    # Backends — best-effort load tasks + router (missing/broken files are
    # already flagged by ``check_config_files``; probing here still surfaces
    # the environment picture).
    tasks_list: list = []
    router_map: dict = {}
    try:
        from orchestrator.state import load_tasks

        tasks_list = load_tasks(paths.tasks_json)
    except Exception:  # noqa: BLE001
        tasks_list = []
    try:
        from orchestrator.router import load_router

        router_map = load_router(paths.router_yaml)
    except Exception:  # noqa: BLE001
        router_map = {}
    checks.extend(preflight.check_backends(tasks_list, router_map))

    # models.resolve — every task.model must resolve to a route entry.
    unresolved = [t for t in tasks_list if router_map and t.model not in router_map]
    if not tasks_list:
        checks.append(
            preflight.CheckResult(
                name="models.resolve",
                status="skip",
                detail="no tasks loaded — cannot check resolution",
            )
        )
    elif not router_map:
        checks.append(
            preflight.CheckResult(
                name="models.resolve",
                status="skip",
                detail="router did not load — cannot check resolution",
            )
        )
    elif unresolved:
        checks.append(
            preflight.CheckResult(
                name="models.resolve",
                status="error",
                detail=(
                    f"{len(unresolved)} task(s) reference unroutable models "
                    f"(first: {unresolved[0].id!r} -> {unresolved[0].model!r})"
                ),
                remediation="Add the missing entries to model_router.yaml.",
            )
        )
    else:
        checks.append(
            preflight.CheckResult(
                name="models.resolve",
                status="ok",
                detail=f"{len(tasks_list)} task(s) resolve",
            )
        )

    # State backend probe. Bump this when a new sqlite_migrations/NNN_*.sql
    # ships — it must equal the highest migration number in that directory.
    checks.extend(
        preflight.check_state_backend(
            state_dir=paths.state_dir,
            backend=backend_kind,
            sqlite_path=_resolve_sqlite_path(paths, cfg),
            expected_schema_version=2,
        )
    )

    # Sprint E-5 (TUN-11): tunnel config + provider binary checks. Read
    # dashboard.yaml from the project root — matches DashboardConfig.load()
    # which layers the packaged default under any project override.
    checks.extend(preflight.check_tunnel(paths.project_root / "dashboard.yaml"))

    if only:
        checks = [c for c in checks if only in c.name]

    summary = preflight.summarize_checks(checks)
    exit_code = preflight.exit_code_for_checks(checks)

    return {
        "project": {
            "id": paths.project_id,
            "root": str(paths.project_root),
        },
        "backend": backend_kind,
        "checks": [c.as_json() for c in checks],
        "summary": summary,
        "exit_code": exit_code,
    }
