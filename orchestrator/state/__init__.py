"""State / I/O layer for the orchestrator.

Sprint B: this used to be a single `state.py` module. It's now a package that
re-exports the same public surface so no callsite outside `orchestrator/state/`
had to change. The internal split:

    orchestrator/state/
        interface.py     — StateBackend Protocol + factory error type
        file_backend.py  — original JSONL/JSON code + FileBackend façade
        sqlite_backend.py (commit 2) — new SQLite implementation
        flock.py         — advisory file locking (verbatim from state.py)
        shell.py         — task-*.sh wrappers (verbatim from state.py)
        sqlite_migrations/ — versioned schema files

`get_backend(paths, cfg)` selects `FileBackend` (default) or `SqliteBackend`
(when `cfg["state"]["backend"] == "sqlite"`). Cached per-process by
`(state_dir, project_id, backend_kind)` so re-entrant callers share one.
"""

from __future__ import annotations

# Kept as public re-exports so historical tests / callers can still do
# `patch("orchestrator.state.os.replace", ...)` and
# `patch("orchestrator.state.subprocess.run", ...)`. Both patch the module
# object itself (not our namespace), which is shared with `file_backend` and
# `shell` — so the mocks propagate everywhere in the process.
import os  # noqa: F401 — back-compat surface
import subprocess  # noqa: F401 — back-compat surface
from pathlib import Path
from typing import Any

# Re-export the entire public surface so `from orchestrator.state import X`
# keeps working for every caller written before the Sprint B refactor.
from .file_backend import (  # noqa: F401
    EVENT_TYPES,
    EventLog,
    FileBackend,
    ReconcileReport,
    RunFile,
    SpendLog,
    _atomic_write,
    _git_diff_touches,
    _run_state_to_dict,
    _utc_now_iso,
    load_tasks,
    rebuild_index,
    reconcile_in_flight,
    reconcile_run,
)
from .flock import (  # noqa: F401
    FlockContentionError,
    acquire_flock,
    release_task_lock,
    try_acquire_task_lock,
    write_lock_holder,
)
from .interface import BackendFactoryError, StateBackend  # noqa: F401


# Adapters are imported lazily below (they pull in sqlite_backend, which
# should stay optional at package import time).
from .shell import (  # noqa: F401
    CwdViolationError,
    _ensure_v2_cwd,
    _reset_task_in_place,
    _run_script,
    call_task_block,
    call_task_finish,
    call_task_reset,
    call_task_start,
    ensure_project_root,
)


__all__ = [
    "BackendFactoryError",
    "CwdViolationError",
    "EVENT_TYPES",
    "EventLog",
    "FileBackend",
    "FlockContentionError",
    "ReconcileReport",
    "RunFile",
    "SpendLog",
    "StateBackend",
    "_atomic_write",
    "_ensure_v2_cwd",
    "_git_diff_touches",
    "_reset_task_in_place",
    "_run_script",
    "_run_state_to_dict",
    "_utc_now_iso",
    "acquire_flock",
    "call_task_block",
    "call_task_finish",
    "call_task_reset",
    "call_task_start",
    "ensure_project_root",
    "get_backend",
    "load_tasks",
    "make_event_log",
    "make_runfile",
    "make_spend_log",
    "rebuild_index",
    "reconcile_in_flight",
    "reconcile_run",
    "release_task_lock",
    "try_acquire_task_lock",
    "write_lock_holder",
]


# ---- backend factory ----------------------------------------------------


# Cached per-process. Keyed by (state_dir, project_id, kind) so the file and
# sqlite backends can coexist without stepping on each other during tests.
_BACKEND_CACHE: dict[tuple[str, str | None, str], StateBackend] = {}


def get_backend(paths: Any, cfg: dict[str, Any] | None = None) -> StateBackend:
    """Construct (or fetch cached) `StateBackend` for the given `paths`/config.

    `paths` is expected to be a `ProjectPaths` instance (see
    `orchestrator.paths`) but this function only reads `.state_dir`,
    `.project_id`, and `.project_root` — a duck-typed test double works too.

    `cfg` is the parsed `config.yaml` dict. `cfg["state"]["backend"]` selects
    the implementation. Missing key → file backend (backwards-compat).

    Raises `BackendFactoryError` for unknown backend names.
    """
    state_dir = Path(getattr(paths, "state_dir"))
    project_id = getattr(paths, "project_id", None)
    project_root = getattr(paths, "project_root", None)

    kind = "file"
    if cfg is not None:
        state_cfg = cfg.get("state") if isinstance(cfg, dict) else None
        if isinstance(state_cfg, dict):
            kind = str(state_cfg.get("backend", "file") or "file").lower()

    key = (str(state_dir), project_id, kind)
    cached = _BACKEND_CACHE.get(key)
    if cached is not None:
        return cached

    if kind == "file":
        backend: StateBackend = FileBackend(
            state_dir=state_dir,
            project_id=project_id,
            project_root=Path(project_root) if project_root else None,
        )
    elif kind == "sqlite":
        from .sqlite_backend import SqliteBackend  # imported lazily (commit 2)

        sqlite_path = None
        if cfg is not None and isinstance(cfg.get("state"), dict):
            raw = cfg["state"].get("sqlite_path")
            if raw:
                sqlite_path = Path(raw)
                if not sqlite_path.is_absolute():
                    sqlite_path = state_dir / raw
        if sqlite_path is None:
            sqlite_path = state_dir / "orch.db"
        backend = SqliteBackend(
            db_path=sqlite_path,
            project_id=project_id or "default",
            project_root=Path(project_root) if project_root else None,
        )
    else:
        raise BackendFactoryError(
            f"unknown state backend {kind!r}; expected 'file' or 'sqlite'"
        )

    _BACKEND_CACHE[key] = backend
    return backend


def _reset_backend_cache() -> None:
    """Test helper — drops the process-wide backend cache."""
    _BACKEND_CACHE.clear()


def __getattr__(name: str) -> Any:
    """Lazy re-export of the adapter factories.

    Keeps `orchestrator.state` import-cheap for callers that don't need
    sqlite; only pays the sqlite import cost on first access.
    """
    if name in {"make_runfile", "make_event_log", "make_spend_log"}:
        from . import adapters

        return getattr(adapters, name)
    raise AttributeError(f"module 'orchestrator.state' has no attribute {name!r}")
