"""Shared pytest fixtures for orchestrator tests.

Sprint B: introduces the `backend` fixture — a parametrized `StateBackend`
producer that runs the same test body against both `FileBackend` and
`SqliteBackend`. New parity tests live in `test_backend_parity.py`.

Backwards-compat gate: set `ORCH_TEST_BACKEND=file` to restrict the fixture
to the file backend only (skips sqlite runs). CI can use this to prove the
refactor didn't drift file-backend behavior.
"""

from __future__ import annotations

import os
from pathlib import Path

import pytest

from orchestrator.state import FileBackend, _reset_backend_cache
from orchestrator.state.interface import StateBackend
from orchestrator.state.sqlite_backend import (
    SqliteBackend,
    _reset_schema_cache_for_tests,
)


def _selected_backends() -> list[str]:
    """Read ORCH_TEST_BACKEND to decide which backends to parametrize.

    Values:
        - unset / "both" / "all" → run both file and sqlite
        - "file"                 → file only (backwards-compat gate)
        - "sqlite"               → sqlite only (useful for local iteration)
    """
    env = (os.environ.get("ORCH_TEST_BACKEND") or "both").lower()
    if env == "file":
        return ["file"]
    if env == "sqlite":
        return ["sqlite"]
    return ["file", "sqlite"]


@pytest.fixture(params=_selected_backends(), ids=lambda p: f"backend={p}")
def backend(request, tmp_path: Path) -> StateBackend:
    """Return a fresh StateBackend of the requested kind under `tmp_path`.

    Every parametrized test gets its own tmp_path (pytest built-in), so runs
    across backends can never bleed into each other.

    File backend: `tmp_path / 'state'`.
    Sqlite backend: `tmp_path / 'state' / 'orch.db'` with a unique
    project_id derived from the tmp_path (guarantees fresh isolation).
    """
    _reset_backend_cache()
    _reset_schema_cache_for_tests()
    state_dir = tmp_path / "state"
    state_dir.mkdir(parents=True, exist_ok=True)
    kind = request.param
    project_id = f"pytest-{tmp_path.name}"
    if kind == "file":
        return FileBackend(
            state_dir=state_dir,
            project_id=project_id,
            project_root=tmp_path,
        )
    if kind == "sqlite":
        return SqliteBackend(
            db_path=state_dir / "orch.db",
            project_id=project_id,
            project_root=tmp_path,
        )
    raise ValueError(f"unknown backend param: {kind!r}")
