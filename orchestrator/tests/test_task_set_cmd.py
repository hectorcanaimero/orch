"""Tests for the `orch task set` subcommand (Sprint F-1 — Task 8)."""

from __future__ import annotations

from pathlib import Path

import pytest

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


def _make_sqlite_project(root: Path) -> None:
    """Scaffold a minimal sqlite-backed project under `root`."""
    root.mkdir(parents=True, exist_ok=True)
    (root / "tasks.json").write_bytes(FIXTURE_TASKS.read_bytes())
    scripts = root / "scripts"
    scripts.mkdir()
    for name in ("task-start.sh", "task-finish.sh", "task-block.sh"):
        (scripts / name).write_text("#!/usr/bin/env bash\nexit 0\n")
        (scripts / name).chmod(0o755)
    orch_dir = root / ".orchestrator"
    orch_dir.mkdir()
    (orch_dir / "config.yaml").write_text(
        "state:\n  backend: sqlite\n  sqlite_path: null\n"
    )
    (orch_dir / "model_router.yaml").write_text(
        "opencode-go/glm-5.1:\n"
        "  backend: opencode\n"
        "  cli_model: glm-5.1\n"
        "  tier: cheap\n"
        "opencode/claude-sonnet-4-6:\n"
        "  backend: claude\n"
        "  cli_model: claude-sonnet-4-6\n"
        "  tier: standard\n"
    )


def _common_args(root: Path) -> list[str]:
    return [
        "--project-root", str(root),
        "--project-id", "proj-task-set",
        "--config", ".orchestrator/config.yaml",
    ]


def _bootstrap_project(root: Path) -> None:
    """Bootstrap the SQLite backend by loading tasks once."""
    from orchestrator.state import get_backend
    from orchestrator.orch import _load_config, _resolve_paths_from_argv
    import argparse

    # Build a minimal namespace to resolve paths
    ns = argparse.Namespace(
        project_root=str(root),
        project_id="proj-task-set",
        config=".orchestrator/config.yaml",
    )
    paths = _resolve_paths_from_argv(ns)
    cfg = _load_config(paths.config_yaml)
    backend = get_backend(paths, cfg)

    import json
    raw = json.loads(FIXTURE_TASKS.read_bytes())
    from orchestrator.models import Task
    tasks = [Task(**{
        "id": t["id"],
        "phase": t["phase"],
        "title": t["title"],
        "description": t.get("description", ""),
        "model": t["model"],
        "reason": t.get("reason", ""),
        "status": t.get("status", "todo"),
        "dependencies": t.get("dependencies", []),
        "estimate_hours": t.get("estimateHours", 0.0),
        "files": t.get("files", []),
        "spec_ref": t.get("specRef", ""),
        "comments": t.get("comments", []),
    }) for t in raw["tasks"]]
    backend.bootstrap(tasks)

    # Also seed tasks_definition via set_task_model (bootstrap doesn't seed it)
    from orchestrator.state.sqlite_backend import SqliteBackend
    assert isinstance(backend, SqliteBackend)
    # Upsert definition rows so set_task_model/set_task_backend can find them
    import sqlite3
    db = paths.state_dir / "orch.db"
    conn = sqlite3.connect(str(db))
    now = "2026-08-25T00:00:00Z"
    for t in tasks:
        conn.execute(
            "INSERT OR IGNORE INTO tasks_definition "
            "(project_id, task_id, model, backend, updated_at) "
            "VALUES (?, ?, ?, ?, ?)",
            ("proj-task-set", t.id, t.model, "opencode", now),
        )
    conn.commit()
    conn.close()


# ---- tests ---------------------------------------------------------------


def test_task_set_missing_fields_returns_error(tmp_path: Path, capsys) -> None:
    """Calling task set without any mutation flag must exit 1."""
    from orchestrator.orch import _run_task_set_subcommand

    root = tmp_path / "proj"
    _make_sqlite_project(root)
    rc = _run_task_set_subcommand([
        "--id", "T-A",
        *_common_args(root),
    ])
    assert rc == 1
    err = capsys.readouterr().err
    assert "at least one" in err


def test_task_set_model_updates_tasks_definition(tmp_path: Path, capsys) -> None:
    """--model writes to tasks_definition."""
    import sqlite3
    from orchestrator.orch import _run_task_set_subcommand
    from orchestrator.orch import _load_config, _resolve_paths_from_argv
    import argparse

    root = tmp_path / "proj"
    _make_sqlite_project(root)
    _bootstrap_project(root)

    rc = _run_task_set_subcommand([
        "--id", "T-A",
        "--model", "opencode/claude-sonnet-4-6",
        *_common_args(root),
    ])
    assert rc == 0
    out = capsys.readouterr().out
    assert "model" in out
    assert "opencode/claude-sonnet-4-6" in out

    ns = argparse.Namespace(
        project_root=str(root),
        project_id="proj-task-set",
        config=".orchestrator/config.yaml",
    )
    paths = _resolve_paths_from_argv(ns)
    db = paths.state_dir / "orch.db"
    conn = sqlite3.connect(str(db))
    row = conn.execute(
        "SELECT model FROM tasks_definition "
        "WHERE project_id='proj-task-set' AND task_id='T-A'"
    ).fetchone()
    conn.close()
    assert row is not None
    assert row[0] == "opencode/claude-sonnet-4-6"


def test_task_set_status_todo_to_done(tmp_path: Path, capsys) -> None:
    """--status done on a todo task succeeds (manual completion path)."""
    import sqlite3
    from orchestrator.orch import _run_task_set_subcommand
    from orchestrator.orch import _resolve_paths_from_argv
    import argparse

    root = tmp_path / "proj"
    _make_sqlite_project(root)
    _bootstrap_project(root)

    rc = _run_task_set_subcommand([
        "--id", "T-A",
        "--status", "done",
        *_common_args(root),
    ])
    assert rc == 0

    ns = argparse.Namespace(
        project_root=str(root),
        project_id="proj-task-set",
        config=".orchestrator/config.yaml",
    )
    paths = _resolve_paths_from_argv(ns)
    db = paths.state_dir / "orch.db"
    conn = sqlite3.connect(str(db))
    status = conn.execute(
        "SELECT status FROM tasks_runtime "
        "WHERE project_id='proj-task-set' AND task_id='T-A'"
    ).fetchone()[0]
    conn.close()
    assert status == "done"


def test_task_set_file_backend_returns_error(tmp_path: Path, capsys) -> None:
    """task set must reject file-backend projects."""
    from orchestrator.orch import _run_task_set_subcommand

    root = tmp_path / "proj"
    root.mkdir(parents=True, exist_ok=True)
    (root / "tasks.json").write_bytes(FIXTURE_TASKS.read_bytes())
    scripts = root / "scripts"
    scripts.mkdir()
    for name in ("task-start.sh", "task-finish.sh", "task-block.sh"):
        (scripts / name).write_text("#!/usr/bin/env bash\nexit 0\n")
        (scripts / name).chmod(0o755)
    orch_dir = root / ".orchestrator"
    orch_dir.mkdir()
    (orch_dir / "config.yaml").write_text(
        "state:\n  backend: file\n"
    )
    (orch_dir / "model_router.yaml").write_text(
        "opencode-go/glm-5.1:\n"
        "  backend: opencode\n"
        "  cli_model: glm-5.1\n"
        "  tier: cheap\n"
    )

    rc = _run_task_set_subcommand([
        "--id", "T-A",
        "--model", "opencode/claude-sonnet-4-6",
        "--project-root", str(root),
        "--project-id", "proj-file",
        "--config", ".orchestrator/config.yaml",
    ])
    assert rc == 1
    err = capsys.readouterr().err
    assert "sqlite" in err.lower()


def test_task_dispatch_routes_to_task_set(tmp_path: Path, capsys) -> None:
    """Top-level `orch task set` dispatch resolves to _run_task_set_subcommand."""
    from orchestrator.orch import _run_task_subcommand

    # Calling task set with no args shows usage, returns 1.
    rc = _run_task_subcommand([])
    assert rc == 1
    out = capsys.readouterr().out
    assert "subcommand" in out.lower() or "usage" in out.lower()


def test_task_set_backend_flag_updates_tasks_definition(tmp_path: Path, capsys) -> None:
    """--backend writes tasks_definition.backend."""
    import sqlite3
    from orchestrator.orch import _run_task_set_subcommand
    from orchestrator.orch import _resolve_paths_from_argv
    import argparse

    root = tmp_path / "proj"
    _make_sqlite_project(root)
    _bootstrap_project(root)

    rc = _run_task_set_subcommand([
        "--id", "T-B",
        "--backend", "claude",
        *_common_args(root),
    ])
    assert rc == 0

    ns = argparse.Namespace(
        project_root=str(root),
        project_id="proj-task-set",
        config=".orchestrator/config.yaml",
    )
    paths = _resolve_paths_from_argv(ns)
    db = paths.state_dir / "orch.db"
    conn = sqlite3.connect(str(db))
    backend = conn.execute(
        "SELECT backend FROM tasks_definition "
        "WHERE project_id='proj-task-set' AND task_id='T-B'"
    ).fetchone()[0]
    conn.close()
    assert backend == "claude"
