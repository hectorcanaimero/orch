"""Tests for milestone methods in SqliteBackend (Sprint F-3)."""
from __future__ import annotations

from pathlib import Path

import pytest

from orchestrator.models import Task
from orchestrator.state import _reset_backend_cache
from orchestrator.state.sqlite_backend import SqliteBackend, _reset_schema_cache_for_tests


# ---- helpers ----------------------------------------------------------------


def _task(tid: str, status: str = "todo") -> Task:
    return Task(
        id=tid,
        phase=1,
        title=f"Task {tid}",
        description="",
        model="claude",
        reason="",
        status=status,  # type: ignore[arg-type]
        dependencies=[],
        estimate_hours=0.1,
        files=[],
        spec_ref="",
        comments=[],
    )


def _backend(tmp_path: Path) -> SqliteBackend:
    db = tmp_path / "orch.db"
    b = SqliteBackend(db_path=db, project_id="test", project_root=tmp_path)
    b.bootstrap([])
    return b


@pytest.fixture(autouse=True)
def _reset_cache():
    _reset_backend_cache()
    _reset_schema_cache_for_tests()
    yield
    _reset_backend_cache()
    _reset_schema_cache_for_tests()


# ---- upsert_milestone -------------------------------------------------------


def test_upsert_milestone_creates_record(tmp_path: Path):
    b = _backend(tmp_path)
    b.upsert_milestone("M1", title="Login Feature")
    milestones = b.get_milestones()
    assert len(milestones) == 1
    assert milestones[0]["id"] == "M1"
    assert milestones[0]["title"] == "Login Feature"


def test_upsert_milestone_updates_existing(tmp_path: Path):
    b = _backend(tmp_path)
    b.upsert_milestone("M1", title="Old Title")
    b.upsert_milestone("M1", title="New Title")
    milestones = b.get_milestones()
    assert len(milestones) == 1
    assert milestones[0]["title"] == "New Title"


def test_upsert_milestone_with_optional_fields(tmp_path: Path):
    b = _backend(tmp_path)
    b.upsert_milestone(
        "M2",
        title="With Extras",
        description="Some description",
        target_date="2026-12-31",
    )
    milestones = b.get_milestones()
    assert milestones[0]["description"] == "Some description"
    assert milestones[0]["target_date"] == "2026-12-31"


# ---- get_milestones ---------------------------------------------------------


def test_get_milestones_empty_returns_empty_list(tmp_path: Path):
    b = _backend(tmp_path)
    assert b.get_milestones() == []


def test_get_milestones_returns_progress(tmp_path: Path):
    b = _backend(tmp_path)
    # bootstrap seeds both tasks_runtime AND tasks_definition
    b.bootstrap([
        _task("T1", status="done"),
        _task("T2", status="backlog"),
    ])
    # Manually set T1 status to done via tasks_runtime (bootstrap uses INSERT OR IGNORE;
    # 'done' is not a valid initial status transition via set_task_status, but
    # bootstrap does honour the status on first insert).
    b.upsert_milestone("M1", title="Feature A")
    b.set_task_milestone("T1", "M1")
    b.set_task_milestone("T2", "M1")
    milestones = b.get_milestones()
    assert len(milestones) == 1
    m = milestones[0]
    assert m["progress"]["total"] == 2
    assert m["progress"]["done"] == 1
    assert m["progress"]["pct"] == 50


def test_get_milestones_no_tasks_shows_zero_progress(tmp_path: Path):
    b = _backend(tmp_path)
    b.upsert_milestone("M1", title="Empty Milestone")
    milestones = b.get_milestones()
    assert milestones[0]["progress"]["total"] == 0
    assert milestones[0]["progress"]["done"] == 0
    assert milestones[0]["progress"]["pct"] == 0


def test_get_milestones_default_status_is_open(tmp_path: Path):
    b = _backend(tmp_path)
    b.upsert_milestone("M1", title="New")
    milestones = b.get_milestones()
    assert milestones[0]["status"] == "open"


# ---- set_task_milestone -----------------------------------------------------


def test_set_task_milestone_raises_on_unknown_task(tmp_path: Path):
    b = _backend(tmp_path)
    b.upsert_milestone("M1", title="M")
    with pytest.raises(KeyError):
        b.set_task_milestone("NONEXISTENT", "M1")


def test_set_task_milestone_raises_on_unknown_milestone(tmp_path: Path):
    b = _backend(tmp_path)
    b.bootstrap([_task("T1")])
    with pytest.raises(KeyError):
        b.set_task_milestone("T1", "NONEXISTENT_MILESTONE")


def test_set_task_milestone_assigns_correctly(tmp_path: Path):
    b = _backend(tmp_path)
    b.bootstrap([_task("T1")])
    b.upsert_milestone("M1", title="M")
    b.set_task_milestone("T1", "M1")
    milestones = b.get_milestones()
    assert milestones[0]["progress"]["total"] == 1


# ---- complete_milestone ------------------------------------------------------


def test_complete_milestone_changes_status(tmp_path: Path):
    b = _backend(tmp_path)
    b.upsert_milestone("M1", title="Done Feature")
    b.complete_milestone("M1")
    milestones = b.get_milestones()
    assert milestones[0]["status"] == "completed"


def test_complete_milestone_raises_on_unknown_id(tmp_path: Path):
    """complete_milestone on a nonexistent id should raise KeyError."""
    b = _backend(tmp_path)
    with pytest.raises(KeyError):
        b.complete_milestone("NONEXISTENT")
