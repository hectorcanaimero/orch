"""Tests for the `orch task-status` subcommand (Sprint B — commit 3).

The subcommand is the single-writer helper that the shell scripts
`task-{start,finish,block,reset}.sh` shell into. We verify:

    - Unknown task id → exit 2.
    - Illegal transition → exit 3.
    - Happy path on sqlite backend → tasks_runtime row updated.
    - Happy path on file backend → shells into scripts/task-start.sh via
      the FileBackend.set_task_status path.

We do NOT invoke the shell scripts themselves (they exec `orch`, which
would create a fork loop in tests). The templates/scripts/*.sh are covered
by an integration-style test that just asserts they exec `orch task-status`.
"""

from __future__ import annotations

import json
import shutil
from pathlib import Path

import pytest

from orchestrator.orch import _run_task_status_subcommand
from orchestrator.state import _reset_backend_cache
from orchestrator.state.sqlite_backend import _reset_schema_cache_for_tests

FIXTURE_TASKS = Path(__file__).parent / "fixtures" / "tiny_tasks.json"


@pytest.fixture(autouse=True)
def _clear_caches():
    _reset_backend_cache()
    _reset_schema_cache_for_tests()
    yield
    _reset_backend_cache()
    _reset_schema_cache_for_tests()


@pytest.fixture
def project(tmp_path: Path) -> Path:
    """Build a minimal valid orch project layout at tmp_path."""
    root = tmp_path / "proj"
    root.mkdir()
    # tasks.json (copy the tiny fixture verbatim)
    (root / "tasks.json").write_bytes(FIXTURE_TASKS.read_bytes())
    # A minimal scripts/task-start.sh so ensure_valid() passes.
    scripts = root / "scripts"
    scripts.mkdir()
    for name in ("task-start.sh", "task-finish.sh", "task-block.sh"):
        (scripts / name).write_text("#!/usr/bin/env bash\nexit 0\n")
        (scripts / name).chmod(0o755)
    # orchestrator/config.yaml — minimal.
    cfg_dir = root / ".orchestrator"
    cfg_dir.mkdir()
    (cfg_dir / "config.yaml").write_text(
        "state:\n  backend: sqlite\n  sqlite_path: null\n"
    )
    # model_router.yaml (required by _load_config path? no — task-status only
    # needs config.yaml + tasks.json + scripts). Skipped.
    return root


@pytest.fixture
def project_file_backend(tmp_path: Path) -> Path:
    """Same as `project` but pinned to the file backend."""
    root = tmp_path / "proj-file"
    root.mkdir()
    (root / "tasks.json").write_bytes(FIXTURE_TASKS.read_bytes())
    scripts = root / "scripts"
    scripts.mkdir()
    for name in ("task-start.sh", "task-finish.sh", "task-block.sh"):
        # Real inert scripts — write nothing to tasks.json.
        (scripts / name).write_text("#!/usr/bin/env bash\nexit 0\n")
        (scripts / name).chmod(0o755)
    cfg_dir = root / ".orchestrator"
    cfg_dir.mkdir()
    (cfg_dir / "config.yaml").write_text("state:\n  backend: file\n")
    return root


# ---- sqlite backend ----------------------------------------------------


def test_sqlite_happy_path_updates_row(project: Path) -> None:
    argv = [
        "T-A",
        "in-progress",
        "--author",
        "test",
        "--note",
        "start",
        "--project-root",
        str(project),
        "--project-id",
        "proj-a",
        "--config",
        ".orchestrator/config.yaml",
    ]
    rc = _run_task_status_subcommand(argv)
    assert rc == 0
    # Verify the row landed.
    import sqlite3

    db = project / ".orchestrator" / "state" / "proj-a" / "orch.db"
    conn = sqlite3.connect(str(db))
    try:
        row = conn.execute(
            "SELECT status FROM tasks_runtime "
            "WHERE project_id='proj-a' AND task_id='T-A'"
        ).fetchone()
    finally:
        conn.close()
    assert row is not None
    assert row[0] == "in-progress"


def test_sqlite_unknown_task_exits_2(project: Path) -> None:
    argv = [
        "T-NOPE",
        "in-progress",
        "--project-root", str(project),
        "--project-id", "proj-a",
        "--config", ".orchestrator/config.yaml",
    ]
    rc = _run_task_status_subcommand(argv)
    assert rc == 2


def test_sqlite_illegal_transition_exits_3(project: Path) -> None:
    # T-A starts as `todo`; jumping straight to `done` is illegal.
    argv = [
        "T-A",
        "done",
        "--project-root", str(project),
        "--project-id", "proj-a",
        "--config", ".orchestrator/config.yaml",
    ]
    rc = _run_task_status_subcommand(argv)
    assert rc == 3


# ---- file backend ------------------------------------------------------


def test_file_backend_invokes_task_start_script(
    project_file_backend: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """FileBackend.set_task_status(in-progress) → call_task_start()."""
    calls: list[tuple] = []

    def fake_call_task_start(task_id, author="orchestrator", project_root=None):
        calls.append(("start", task_id, author, project_root))

    # Patch through the package namespace so the late-binding shim in
    # file_backend.py picks it up.
    import orchestrator.state as state_pkg

    monkeypatch.setattr(state_pkg, "call_task_start", fake_call_task_start)
    # FileBackend uses the direct import too — patch it there.
    from orchestrator.state import file_backend as fb_mod

    monkeypatch.setattr(fb_mod, "_call_task_start_direct", fake_call_task_start)

    argv = [
        "T-A",
        "in-progress",
        "--author", "test",
        "--project-root", str(project_file_backend),
        "--project-id", "proj-file",
        "--config", ".orchestrator/config.yaml",
    ]
    rc = _run_task_status_subcommand(argv)
    assert rc == 0
    assert calls, "expected FileBackend to invoke task-start"
    assert calls[0][1] == "T-A"
    assert calls[0][2] == "test"


def test_file_backend_unknown_task_exits_2(project_file_backend: Path) -> None:
    argv = [
        "T-NOPE",
        "in-progress",
        "--project-root", str(project_file_backend),
        "--project-id", "proj-file",
        "--config", ".orchestrator/config.yaml",
    ]
    rc = _run_task_status_subcommand(argv)
    assert rc == 2


# ---- shell scripts contract -------------------------------------------


def _read(path: Path) -> str:
    return path.read_text(encoding="utf-8")


def test_task_start_script_delegates_to_orch_task_status() -> None:
    script = Path(__file__).parents[1] / "templates" / "scripts" / "task-start.sh"
    body = _read(script)
    assert "orch task-status" in body
    assert "in-progress" in body


def test_task_finish_script_delegates_to_orch_task_status() -> None:
    script = Path(__file__).parents[1] / "templates" / "scripts" / "task-finish.sh"
    body = _read(script)
    assert "orch task-status" in body
    assert "done" in body


def test_task_block_script_delegates_to_orch_task_status() -> None:
    script = Path(__file__).parents[1] / "templates" / "scripts" / "task-block.sh"
    body = _read(script)
    assert "orch task-status" in body
    assert "blocked" in body


def test_task_reset_script_exists_and_delegates() -> None:
    script = Path(__file__).parents[1] / "templates" / "scripts" / "task-reset.sh"
    assert script.exists()
    body = _read(script)
    assert "orch task-status" in body
    assert "todo" in body
