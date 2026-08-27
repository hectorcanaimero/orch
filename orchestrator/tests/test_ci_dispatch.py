import time
from pathlib import Path
from unittest.mock import MagicMock

import pytest

from orchestrator.orch import _check_ci_once
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


# ---------------------------------------------------------------------------
# _check_ci_once dispatch loop integration (mocked VCS + state backend)
# ---------------------------------------------------------------------------

def _make_mock_cfg(ci_max_retries: int = 1, ci_poll_interval_s: float = 0.0) -> dict:
    return {
        "vcs": {
            "auto_pr": True,
            "ci_max_retries": ci_max_retries,
            "ci_poll_interval_s": ci_poll_interval_s,
        }
    }


def _mock_state_backend(pending_tasks: list[dict]) -> MagicMock:
    b = MagicMock()
    b.get_tasks_with_pending_ci.return_value = pending_tasks
    return b


def _mock_vcs(ci_status: str, ci_logs: str = "logs") -> MagicMock:
    v = MagicMock()
    v.get_ci_status.return_value = ci_status
    v.get_ci_logs.return_value = ci_logs
    return v


def _mock_queue(task_id: str) -> MagicMock:
    q = MagicMock()
    q._tasks = []  # noqa: SLF001
    q._status = {task_id: "in_progress"}  # noqa: SLF001
    return q


def _ci_kwargs(cfg: dict, sb: MagicMock, vcs: MagicMock, q: MagicMock, wm: MagicMock) -> dict:
    return dict(
        cfg=cfg,
        state_backend=sb,
        vcs_provider=vcs,
        queue=q,
        wm=wm,
        in_flight={},
        run_file=MagicMock(),
        event_log=MagicMock(),
        spend_log=MagicMock(),
        gsem=MagicMock(),
        psem={"claude": MagicMock()},
        retry_queue=[],
        router={},
        task_costs={},
        state_dir=Path("/tmp"),
        cwd=Path("/tmp"),
        last_check_ts=0.0,
    )


def test_ci_check_throttled_when_called_just_now():
    sb = _mock_state_backend([])
    vcs = _mock_vcs("success")
    q = _mock_queue("t1")
    wm = MagicMock()
    kwargs = _ci_kwargs(_make_mock_cfg(ci_poll_interval_s=9999.0), sb, vcs, q, wm)
    kwargs["last_check_ts"] = time.monotonic()
    _check_ci_once(**kwargs)
    sb.get_tasks_with_pending_ci.assert_not_called()


def test_ci_success_marks_task_done():
    task_id = "t1"
    pending = [{"task_id": task_id, "pr_url": "https://gh/pull/1", "ci_attempts": 0}]
    sb = _mock_state_backend(pending)
    vcs = _mock_vcs("success")
    q = _mock_queue(task_id)
    wm = MagicMock()
    _check_ci_once(**_ci_kwargs(_make_mock_cfg(), sb, vcs, q, wm))
    sb.set_task_ci_status.assert_called_once_with(task_id, "success")
    q.mark_done.assert_called_once_with(task_id)


def test_ci_failure_under_cap_increments_attempts():
    task_id = "t1"
    pending = [{"task_id": task_id, "pr_url": "https://gh/pull/1", "ci_attempts": 0}]
    sb = _mock_state_backend(pending)
    vcs = _mock_vcs("failure")
    q = _mock_queue(task_id)
    wm = MagicMock()
    wm.recreate.return_value = MagicMock()
    kwargs = _ci_kwargs(_make_mock_cfg(ci_max_retries=2), sb, vcs, q, wm)
    _check_ci_once(**kwargs)
    sb.increment_ci_attempts.assert_called_once_with(task_id)
    sb.set_task_ci_status.assert_called_with(task_id, "pending")
    q.mark_blocked.assert_not_called()


def test_ci_failure_at_cap_blocks_task():
    task_id = "t1"
    pending = [{"task_id": task_id, "pr_url": "https://gh/pull/1", "ci_attempts": 1}]
    sb = _mock_state_backend(pending)
    vcs = _mock_vcs("failure")
    q = _mock_queue(task_id)
    wm = MagicMock()
    _check_ci_once(**_ci_kwargs(_make_mock_cfg(ci_max_retries=1), sb, vcs, q, wm))
    sb.set_task_ci_status.assert_called_once_with(task_id, "failure")
    q.mark_blocked.assert_called_once_with(task_id)


def test_ci_pending_does_nothing():
    task_id = "t1"
    pending = [{"task_id": task_id, "pr_url": "https://gh/pull/1", "ci_attempts": 0}]
    sb = _mock_state_backend(pending)
    vcs = _mock_vcs("pending")
    q = _mock_queue(task_id)
    wm = MagicMock()
    _check_ci_once(**_ci_kwargs(_make_mock_cfg(), sb, vcs, q, wm))
    sb.set_task_ci_status.assert_not_called()
    q.mark_done.assert_not_called()
    q.mark_blocked.assert_not_called()


def test_ci_no_pending_tasks_skips_vcs_calls():
    sb = _mock_state_backend([])
    vcs = _mock_vcs("success")
    q = MagicMock()
    wm = MagicMock()
    _check_ci_once(**_ci_kwargs(_make_mock_cfg(), sb, vcs, q, wm))
    vcs.get_ci_status.assert_not_called()
