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


def _check_orch_skill_installed() -> preflight.CheckResult:
    """H-6: ok when `~/.claude/skills/orch/SKILL.md` exists.

    - ``skip`` when `~/.claude/` doesn't exist (Claude Code not set up).
    - ``ok`` when the skill file is present.
    - ``warn`` when Claude Code is set up but the skill is missing — hint
      the operator to run ``orch install-skills``.
    """
    claude_home = Path.home() / ".claude"
    skill_path = claude_home / "skills" / "orch" / "SKILL.md"
    if not claude_home.exists():
        return preflight.CheckResult(
            name="skill.orch",
            status="skip",
            detail="~/.claude/ not present (Claude Code not installed)",
        )
    if skill_path.exists():
        return preflight.CheckResult(
            name="skill.orch",
            status="ok",
            detail=f"loaded from {skill_path}",
        )
    return preflight.CheckResult(
        name="skill.orch",
        status="warn",
        detail="the /orch skill is not installed for Claude Code",
        remediation="orch install-skills",
    )


def _check_sqlite_orphan_rows(
    paths: ProjectPaths,
    cfg: dict[str, Any],
    backend_kind: str,
) -> preflight.CheckResult:
    """F-9 (fix #73): report tasks_runtime / tasks_definition rows whose
    project_id has no matching row in `projects`. Pure read — never
    deletes; the remediation hint prints the SQL for the operator.
    """
    if backend_kind != "sqlite":
        return preflight.CheckResult(
            name="sqlite.orphan_rows",
            status="skip",
            detail="not applicable to the file backend",
        )
    try:
        from .state.sqlite_backend import SqliteBackend
        backend = SqliteBackend(
            project_id=paths.project_id,
            db_path=_resolve_sqlite_path(paths, cfg),
            project_root=paths.project_root,
        )
        orphans = backend.detect_orphan_rows()
    except Exception as exc:  # noqa: BLE001
        return preflight.CheckResult(
            name="sqlite.orphan_rows",
            status="warn",
            detail=f"could not read DB: {exc}",
        )
    if not orphans:
        return preflight.CheckResult(
            name="sqlite.orphan_rows",
            status="ok",
            detail="no orphan rows",
        )
    total = sum(sum(v.values()) for v in orphans.values())
    pids = sorted({pid for m in orphans.values() for pid in m})
    return preflight.CheckResult(
        name="sqlite.orphan_rows",
        status="warn",
        detail=(
            f"{total} orphan row(s) across tables={sorted(orphans.keys())} "
            f"project_ids={pids}"
        ),
        remediation=(
            "sqlite3 <db> \"DELETE FROM tasks_runtime WHERE project_id NOT IN "
            "(SELECT project_id FROM projects); "
            "DELETE FROM tasks_definition WHERE project_id NOT IN "
            "(SELECT project_id FROM projects);\""
        ),
    )


def _check_state_db_divergence(paths: ProjectPaths) -> preflight.CheckResult:
    """Issue #84: report when a project has BOTH a legacy `state/orch.db`
    AND a namespaced `state/<project_id>/orch.db` — historically the CLI
    wrote to one and the dashboard read the other, so operators saw
    silently divergent state.

    ``resolve_project_paths`` now prefers namespaced when both exist and
    warns to stderr; this check surfaces the same signal in the standard
    doctor report with a concrete remediation.
    """
    legacy = paths.project_root / ".orchestrator" / "state" / "orch.db"
    namespaced = paths.project_root / ".orchestrator" / "state" / paths.project_id / "orch.db"
    if not (legacy.exists() and namespaced.exists()):
        return preflight.CheckResult(
            name="state.no_divergent_dbs",
            status="ok",
            detail="single SQLite state DB (or none) — no divergence risk",
        )
    try:
        legacy_size = legacy.stat().st_size
        namespaced_size = namespaced.stat().st_size
    except OSError:
        legacy_size = namespaced_size = -1
    return preflight.CheckResult(
        name="state.no_divergent_dbs",
        status="warn",
        detail=(
            f"two SQLite DBs coexist under {paths.project_root}/.orchestrator/state/ "
            f"(legacy={legacy_size}B, namespaced={namespaced_size}B). "
            f"orch is using {namespaced} and IGNORING {legacy}."
        ),
        remediation=(
            f"Inspect both, then either merge rows into {namespaced} or "
            f"delete the stale legacy copy: `rm {legacy}`. "
            "Backup first if unsure."
        ),
    )


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

    # F-9 (fix #73): surface orphan runtime rows so the operator can spot
    # a silent DAG failure before it burns tokens. Only meaningful for the
    # sqlite backend; skipped cleanly for file.
    checks.append(_check_sqlite_orphan_rows(paths, cfg, backend_kind))

    # Issue #84: warn when both legacy and namespaced SQLite DBs coexist.
    checks.append(_check_state_db_divergence(paths))

    # H-6: check whether the packaged /orch Claude Code skill is installed
    # under ~/.claude/skills/orch/. Skipped when Claude Code isn't set up.
    checks.append(_check_orch_skill_installed())

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
