"""Tests for F-9 (fix #73): orphan-row detection in SqliteBackend + doctor.

An orphan row = a `tasks_runtime` or `tasks_definition` row whose
`project_id` has no matching row in `projects`. FK enforcement blocks
NEW orphans (see PRAGMA in `_connect`), but historical DBs (from
before F-9 or after a direct sqlite3-CLI edit) can still hold them.
Silent DAG failures follow — the fix surfaces them loudly.
"""
from __future__ import annotations

import logging
import sqlite3
from pathlib import Path

import pytest


def _reset_caches() -> None:
    from orchestrator.state import _reset_backend_cache
    _reset_backend_cache()
    try:
        from orchestrator.state.sqlite_backend import _reset_schema_cache_for_tests
        _reset_schema_cache_for_tests()
    except ImportError:
        pass


def _new_backend(tmp_path: Path, *, project_id: str = "proj"):
    from orchestrator.state.sqlite_backend import SqliteBackend
    from orchestrator.models import Task

    _reset_caches()
    db_path = tmp_path / "orch.db"
    backend = SqliteBackend(
        project_id=project_id, db_path=db_path, project_root=tmp_path
    )
    backend.bootstrap([])  # creates the schema + projects row
    return backend, db_path


def _seed_orphan(db_path: Path, table: str, ghost_project_id: str, task_id: str) -> None:
    """Insert a row bypassing the FK by disabling `foreign_keys` on this
    single connection — mirrors what a direct sqlite3-CLI edit does."""
    conn = sqlite3.connect(str(db_path))
    conn.row_factory = sqlite3.Row
    conn.execute("PRAGMA foreign_keys = OFF")
    if table == "tasks_runtime":
        conn.execute(
            "INSERT INTO tasks_runtime "
            "(project_id, task_id, status, comments_json, updated_at) "
            "VALUES (?, ?, 'todo', '[]', '2026-01-01T00:00:00Z')",
            (ghost_project_id, task_id),
        )
    elif table == "tasks_definition":
        conn.execute(
            "INSERT INTO tasks_definition "
            "(project_id, task_id, updated_at) "
            "VALUES (?, ?, '2026-01-01T00:00:00Z')",
            (ghost_project_id, task_id),
        )
    conn.commit()
    conn.close()


# ---- detect_orphan_rows ----------------------------------------------------


def test_detect_orphan_rows_returns_empty_when_clean(tmp_path: Path) -> None:
    backend, _ = _new_backend(tmp_path)
    assert backend.detect_orphan_rows() == {}


def test_detect_orphan_rows_finds_ghost_in_tasks_runtime(tmp_path: Path) -> None:
    backend, db_path = _new_backend(tmp_path)
    _seed_orphan(db_path, "tasks_runtime", "ghost-project", "T-A")
    _seed_orphan(db_path, "tasks_runtime", "ghost-project", "T-B")

    orphans = backend.detect_orphan_rows()
    assert orphans == {"tasks_runtime": {"ghost-project": 2}}


def test_detect_orphan_rows_groups_by_table_and_project(tmp_path: Path) -> None:
    backend, db_path = _new_backend(tmp_path)
    _seed_orphan(db_path, "tasks_runtime", "ghost-A", "T-1")
    _seed_orphan(db_path, "tasks_runtime", "ghost-B", "T-2")
    _seed_orphan(db_path, "tasks_definition", "ghost-A", "T-1")

    orphans = backend.detect_orphan_rows()
    assert orphans == {
        "tasks_runtime": {"ghost-A": 1, "ghost-B": 1},
        "tasks_definition": {"ghost-A": 1},
    }


# ---- bootstrap logs a warning ---------------------------------------------


def test_bootstrap_logs_warning_when_orphans_present(
    tmp_path: Path, caplog: pytest.LogCaptureFixture,
) -> None:
    backend, db_path = _new_backend(tmp_path)
    _seed_orphan(db_path, "tasks_runtime", "ghost", "T-A")

    with caplog.at_level(logging.WARNING, logger="orchestrator.state.sqlite_backend"):
        backend.bootstrap([])  # re-bootstrap runs the check

    assert any(
        "orphaned rows" in rec.message.lower() and "ghost" in rec.message
        for rec in caplog.records
    )
    # Repair hint present (SQL for cleanup).
    assert any("DELETE FROM tasks_runtime" in rec.message for rec in caplog.records)


def test_bootstrap_silent_when_no_orphans(
    tmp_path: Path, caplog: pytest.LogCaptureFixture,
) -> None:
    backend, _ = _new_backend(tmp_path)
    with caplog.at_level(logging.WARNING, logger="orchestrator.state.sqlite_backend"):
        backend.bootstrap([])
    assert not any("orphan" in rec.message.lower() for rec in caplog.records)


# ---- doctor surfaces the same info ----------------------------------------


def _make_paths(tmp_path: Path):
    from orchestrator.paths import ProjectPaths
    return ProjectPaths(
        project_root=tmp_path, project_id="proj",
        config_yaml=tmp_path / ".orchestrator" / "config.yaml",
        explicit_root=True, state_layout="legacy",
    )


def test_doctor_reports_ok_when_no_orphans(tmp_path: Path) -> None:
    """The sqlite.orphan_rows check must land in the doctor payload as ok
    when the DB is clean."""
    from orchestrator.doctor import _check_sqlite_orphan_rows

    _, db_path = _new_backend(tmp_path)
    # Point the doctor at the same DB _new_backend created.
    cfg = {"state": {"sqlite_path": str(db_path)}}
    result = _check_sqlite_orphan_rows(_make_paths(tmp_path), cfg, "sqlite")
    assert result.name == "sqlite.orphan_rows"
    assert result.status == "ok"


def test_doctor_reports_warn_with_repair_hint_when_orphans_present(
    tmp_path: Path,
) -> None:
    from orchestrator.doctor import _check_sqlite_orphan_rows

    _, db_path = _new_backend(tmp_path)
    _seed_orphan(db_path, "tasks_runtime", "ghost", "T-A")
    cfg = {"state": {"sqlite_path": str(db_path)}}

    result = _check_sqlite_orphan_rows(_make_paths(tmp_path), cfg, "sqlite")
    assert result.status == "warn"
    assert "ghost" in result.detail
    assert result.remediation is not None
    assert "DELETE FROM tasks_runtime" in result.remediation


def test_doctor_skips_check_for_file_backend(tmp_path: Path) -> None:
    """File-backend projects should skip cleanly, not error."""
    from orchestrator.doctor import _check_sqlite_orphan_rows
    result = _check_sqlite_orphan_rows(_make_paths(tmp_path), {}, "file")
    assert result.status == "skip"
