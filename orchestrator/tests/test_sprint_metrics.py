"""Tests for Sprint F-5 sprint_health metrics."""
from datetime import datetime, timezone
from pathlib import Path

import pytest

from orchestrator.dashboard.metrics import sprint_eta, sprint_health, sprint_velocity
from orchestrator.models import Task


def _task(tid: str, status: str, estimate_hours: float = 2.0, phase: int = 1) -> Task:
    return Task(
        id=tid,
        title=f"Task {tid}",
        description="",
        model="claude",
        reason="",
        status=status,
        dependencies=[],
        phase=phase,
        estimate_hours=estimate_hours,
        files=[],
        spec_ref="",
        comments=[],
    )


# ---------------------------------------------------------------------------
# sprint_velocity
# ---------------------------------------------------------------------------

def test_velocity_normal():
    assert sprint_velocity(14, 7) == pytest.approx(2.0)


def test_velocity_zero_done():
    assert sprint_velocity(0, 7) == 0.0


def test_velocity_zero_window_guard():
    assert sprint_velocity(5, 0) == 0.0


# ---------------------------------------------------------------------------
# sprint_eta
# ---------------------------------------------------------------------------

_NOW = datetime(2026, 9, 1, 12, 0, 0, tzinfo=timezone.utc)


def test_eta_calculates_date():
    result = sprint_eta(velocity_per_day=2.0, remaining_tasks=10, remaining_hours=20.0, now=_NOW)
    assert result["eta_days"] == pytest.approx(5.0)
    assert result["eta_date"] == "2026-09-06"
    assert result["confidence"] == "high"


def test_eta_none_when_no_velocity():
    result = sprint_eta(0.0, 10, 20.0, now=_NOW)
    assert result["eta_days"] is None
    assert result["eta_date"] is None
    assert result["confidence"] == "none"


def test_eta_none_when_no_remaining():
    result = sprint_eta(2.0, 0, 0.0, now=_NOW)
    assert result["eta_days"] is None
    assert result["eta_date"] is None


def test_eta_low_confidence_beyond_30_days():
    result = sprint_eta(velocity_per_day=0.5, remaining_tasks=100, remaining_hours=200.0, now=_NOW)
    assert result["confidence"] == "low"


# ---------------------------------------------------------------------------
# sprint_health
# ---------------------------------------------------------------------------

def _make_tasks():
    return [
        _task("t1", "done", 3.0),
        _task("t2", "done", 2.0),
        _task("t3", "in_progress", 4.0),
        _task("t4", "backlog", 2.0),
        _task("t5", "blocked", 1.0),
    ]


def test_sprint_health_counts():
    tasks = _make_tasks()
    result = sprint_health(tasks, done_7d=2, last_events={}, now=_NOW)
    assert result["done_count"] == 2
    assert result["remaining_tasks"] == 2    # in_progress + backlog (not blocked, not done)
    assert result["blocked_count"] == 1


def test_sprint_health_remaining_hours():
    tasks = _make_tasks()
    result = sprint_health(tasks, done_7d=2, last_events={}, now=_NOW)
    # remaining = t3 (4h) + t4 (2h) = 6h  (t5 blocked excluded from remaining)
    assert result["remaining_hours"] == pytest.approx(6.0)


def test_sprint_health_eta_present():
    tasks = _make_tasks()
    result = sprint_health(tasks, done_7d=14, last_events={}, now=_NOW)
    assert result["eta_date"] is not None
    assert result["velocity_per_day"] == pytest.approx(2.0)


def test_sprint_health_blocker_includes_reason():
    tasks = _make_tasks()
    last_events = {
        "t5": {
            "event_type": "fail",
            "ts": "2026-08-30T10:00:00Z",
            "extra": {"reason": "rate limit exceeded"},
        }
    }
    result = sprint_health(tasks, done_7d=0, last_events=last_events, now=_NOW)
    assert len(result["blockers"]) == 1
    b = result["blockers"][0]
    assert b["task_id"] == "t5"
    assert b["reason"] == "rate limit exceeded"
    assert b["blocked_at"] == "2026-08-30T10:00:00Z"


def test_sprint_health_no_blockers_empty_list():
    tasks = [_task("t1", "done"), _task("t2", "todo")]
    result = sprint_health(tasks, done_7d=1, last_events={}, now=_NOW)
    assert result["blockers"] == []
    assert result["blocked_count"] == 0


def test_sprint_health_all_done_no_eta():
    tasks = [_task("t1", "done"), _task("t2", "done")]
    result = sprint_health(tasks, done_7d=2, last_events={}, now=_NOW)
    assert result["remaining_tasks"] == 0
    assert result["eta_days"] is None


# ---------------------------------------------------------------------------
# count_done_last_n_days (SqliteBackend integration)
# ---------------------------------------------------------------------------

def test_count_done_last_n_days(tmp_path: Path):
    from orchestrator.state.sqlite_backend import SqliteBackend
    import sqlite3

    db_path = tmp_path / "orch.db"
    b = SqliteBackend(project_id="p1", db_path=db_path, project_root=tmp_path)
    b.bootstrap([])

    conn = sqlite3.connect(str(db_path))
    conn.execute("PRAGMA foreign_keys = ON")
    # Insert 2 done tasks updated recently and 1 done task updated long ago
    conn.execute(
        "INSERT OR IGNORE INTO tasks_runtime (project_id, task_id, status, updated_at) "
        "VALUES (?, ?, 'done', datetime('now', '-1 days'))",
        ("p1", "recent-1"),
    )
    conn.execute(
        "INSERT OR IGNORE INTO tasks_runtime (project_id, task_id, status, updated_at) "
        "VALUES (?, ?, 'done', datetime('now', '-3 days'))",
        ("p1", "recent-2"),
    )
    conn.execute(
        "INSERT OR IGNORE INTO tasks_runtime (project_id, task_id, status, updated_at) "
        "VALUES (?, ?, 'done', datetime('now', '-30 days'))",
        ("p1", "old-1"),
    )
    conn.commit()
    conn.close()

    assert b.count_done_last_n_days(7) == 2
    assert b.count_done_last_n_days(1) == 1   # only the -1 day one
    assert b.count_done_last_n_days(60) == 3
