import pytest
from pathlib import Path
from orchestrator.state.sqlite_backend import SqliteBackend


@pytest.fixture
def backend(tmp_path):
    db_path = tmp_path / "orch.db"
    b = SqliteBackend(project_id="p1", db_path=db_path, project_root=tmp_path)
    b.bootstrap([])
    # Seed a minimal tasks_runtime row directly so we don't need full task wiring.
    import sqlite3
    conn = sqlite3.connect(str(db_path))
    conn.execute("PRAGMA foreign_keys = ON")
    conn.execute(
        "INSERT OR IGNORE INTO tasks_runtime "
        "(project_id, task_id, status, updated_at) VALUES (?, ?, 'done', '2026-01-01T00:00:00Z')",
        ("p1", "task-001"),
    )
    conn.commit()
    conn.close()
    return b


def test_set_task_pr_stores_url_and_sets_pending(backend):
    backend.set_task_pr("task-001", "https://github.com/org/repo/pull/42")
    rows = backend.get_tasks_with_pending_ci()
    assert len(rows) == 1
    assert rows[0]["task_id"] == "task-001"
    assert rows[0]["pr_url"] == "https://github.com/org/repo/pull/42"
    assert rows[0]["ci_status"] == "pending"
    assert rows[0]["ci_attempts"] == 0


def test_get_tasks_with_pending_ci_only_returns_pending(backend):
    backend.set_task_pr("task-001", "https://github.com/org/repo/pull/42")
    backend.set_task_ci_status("task-001", "success")
    assert backend.get_tasks_with_pending_ci() == []


def test_set_task_ci_status_updates_status(backend):
    backend.set_task_pr("task-001", "https://github.com/org/repo/pull/42")
    backend.set_task_ci_status("task-001", "failure")
    rows = backend.get_tasks_with_pending_ci()  # no longer pending
    assert rows == []


def test_increment_ci_attempts(backend):
    backend.set_task_pr("task-001", "https://github.com/org/repo/pull/42")
    backend.increment_ci_attempts("task-001")
    backend.increment_ci_attempts("task-001")
    rows = backend.get_tasks_with_pending_ci()
    assert rows[0]["ci_attempts"] == 2


def test_get_tasks_with_pending_ci_null_pr_url_excluded(backend):
    # task without pr_url should never appear
    rows = backend.get_tasks_with_pending_ci()
    assert rows == []


def test_set_task_pr_raises_on_unknown_task(backend):
    with pytest.raises(KeyError):
        backend.set_task_pr("nonexistent", "https://github.com/x/y/pull/1")


def test_set_task_ci_status_raises_on_invalid_status(backend):
    backend.set_task_pr("task-001", "https://github.com/org/repo/pull/42")
    with pytest.raises(ValueError):
        backend.set_task_ci_status("task-001", "broken")


def test_increment_ci_attempts_raises_on_unknown_task(backend):
    with pytest.raises(KeyError):
        backend.increment_ci_attempts("nonexistent")


def test_set_task_ci_status_raises_on_unknown_task(backend):
    with pytest.raises(KeyError):
        backend.set_task_ci_status("nonexistent", "success")
