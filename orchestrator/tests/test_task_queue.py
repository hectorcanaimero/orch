"""Unit tests for `orchestrator.task_queue`.

Covers R-008 acceptance:
    - cycle detection at load (FR-Q-3)
    - missing-dep detection at load (FR-Q-4)
    - `ready()` returns unblocked roots initially (FR-Q-2, FR-Q-5)
    - `mark_done` unblocks dependents; `mark_blocked` does NOT
    - `mark_in_flight` removes from ready-set on next call
"""

from __future__ import annotations

from pathlib import Path

import pytest

from orchestrator.models import Task
from orchestrator.task_queue import (
    MissingDependencyError,
    TaskCycleError,
    TaskQueue,
    load_tasks,
)


FIXTURE = Path(__file__).parent / "fixtures" / "tiny_tasks.json"


# ---- helpers ------------------------------------------------------------


def _mk(tid: str, deps: list[str] | None = None, status: str = "todo") -> Task:
    """Build a `Task` with sane defaults for terse test setup."""
    return Task(
        id=tid,
        phase=1,
        title=f"Task {tid}",
        description="",
        model="opencode-go/glm-5.1",
        reason="",
        status=status,
        dependencies=list(deps or []),
        estimate_hours=0.1,
        files=[],
        spec_ref="",
        comments=[],
    )


# ---- load_tasks ---------------------------------------------------------


def test_load_tasks_reads_fixture() -> None:
    tasks = load_tasks(FIXTURE)
    assert len(tasks) == 5
    ids = {t.id for t in tasks}
    assert ids == {"T-A", "T-B", "T-C", "T-D", "T-E"}
    # from_json handled camelCase → snake_case.
    c = next(t for t in tasks if t.id == "T-C")
    assert c.estimate_hours == 1
    assert c.spec_ref == "docs/spec.md"
    assert c.dependencies == ["T-A", "T-B"]


def test_load_tasks_missing_optional_fields_defaults_to_empty_lists(
    tmp_path: Path,
) -> None:
    """`from_json` must default `files`/`dependencies`/`comments` to `[]`."""
    p = tmp_path / "sparse.json"
    p.write_text(
        '{"tasks":[{"id":"X","phase":1,"title":"x","model":"m","status":"todo"}]}'
    )
    tasks = load_tasks(p)
    assert tasks[0].files == []
    assert tasks[0].dependencies == []
    assert tasks[0].comments == []


# ---- cycle detection ----------------------------------------------------


def test_cycle_detection_raises_with_ids() -> None:
    """A → B → C → A must raise TaskCycleError listing the ring."""
    tasks = [_mk("A", ["C"]), _mk("B", ["A"]), _mk("C", ["B"])]
    with pytest.raises(TaskCycleError) as exc:
        TaskQueue(tasks)
    # All three ids appear in the cycle string.
    for tid in ("A", "B", "C"):
        assert tid in str(exc.value)


def test_self_loop_detected() -> None:
    tasks = [_mk("A", ["A"])]
    with pytest.raises(TaskCycleError):
        TaskQueue(tasks)


def test_forest_loads_without_cycle_error() -> None:
    """Real project shape (334 tasks) is a forest — must not false-positive."""
    tasks = load_tasks(FIXTURE)
    q = TaskQueue(tasks)  # would raise if false positive
    assert q.pending()


# ---- missing dependency -------------------------------------------------


def test_missing_dependency_raises() -> None:
    tasks = [_mk("A"), _mk("B", ["ghost"])]
    with pytest.raises(MissingDependencyError) as exc:
        TaskQueue(tasks)
    assert "ghost" in str(exc.value)
    assert "B" in str(exc.value)


# ---- ready() ------------------------------------------------------------


def test_ready_returns_only_unblocked_roots_initially() -> None:
    """Fixture: T-A, T-B are ready roots. T-C waits, T-D waits, T-E blocked."""
    q = TaskQueue(load_tasks(FIXTURE))
    ready_ids = [t.id for t in q.ready()]
    assert ready_ids == ["T-A", "T-B"]  # sorted by (phase, id)


def test_ready_excludes_in_flight() -> None:
    q = TaskQueue(load_tasks(FIXTURE))
    ready_ids = [t.id for t in q.ready(in_flight_ids={"T-A"})]
    assert ready_ids == ["T-B"]


def test_ready_is_pure() -> None:
    """Calling ready() twice without state change yields identical output."""
    q = TaskQueue(load_tasks(FIXTURE))
    assert [t.id for t in q.ready()] == [t.id for t in q.ready()]


# ---- mark_done / mark_blocked -------------------------------------------


def test_mark_done_unblocks_dependents() -> None:
    q = TaskQueue(load_tasks(FIXTURE))
    q.mark_done("T-A")
    q.mark_done("T-B")
    # T-C's deps (T-A, T-B) are done → C should now be ready.
    ready_ids = [t.id for t in q.ready()]
    assert "T-C" in ready_ids
    # T-D still waits on T-C.
    assert "T-D" not in ready_ids


def test_mark_done_chain_propagates() -> None:
    q = TaskQueue(load_tasks(FIXTURE))
    for tid in ("T-A", "T-B", "T-C"):
        q.mark_done(tid)
    ready_ids = [t.id for t in q.ready()]
    assert ready_ids == ["T-D"]


def test_mark_blocked_does_not_unblock_dependents() -> None:
    """AS-02: blocking X must NOT free Y where Y.deps=[X]."""
    q = TaskQueue(load_tasks(FIXTURE))
    q.mark_done("T-A")
    q.mark_blocked("T-B")
    ready_ids = [t.id for t in q.ready()]
    # T-C requires BOTH T-A and T-B done; T-B is blocked, so T-C stays out.
    assert "T-C" not in ready_ids
    assert ready_ids == []  # only T-A/T-B were ever ready roots


def test_mark_in_flight_removes_from_ready() -> None:
    q = TaskQueue(load_tasks(FIXTURE))
    q.mark_in_flight("T-A")
    ready_ids = [t.id for t in q.ready()]
    assert "T-A" not in ready_ids
    assert "T-B" in ready_ids


def test_mark_unknown_id_raises() -> None:
    q = TaskQueue(load_tasks(FIXTURE))
    with pytest.raises(KeyError):
        q.mark_done("nope")


# ---- pending() ----------------------------------------------------------


def test_pending_false_when_all_terminal() -> None:
    q = TaskQueue(load_tasks(FIXTURE))
    for tid in ("T-A", "T-B", "T-C", "T-D"):
        q.mark_done(tid)
    # T-E was seeded as blocked → terminal → pending is False.
    assert q.pending() is False


# ---- F-8 (fix #72): backend hydration + persistence ------------------------


class _FakeBackend:
    """Minimal stand-in for SqliteBackend that exercises the F-8 contract."""

    def __init__(self, initial: dict[str, str] | None = None) -> None:
        self._status: dict[str, str] = dict(initial or {})
        self.calls: list[tuple[str, str]] = []
        self.raise_on_hydrate: Exception | None = None
        self.raise_on_persist: Exception | None = None

    def get_all_task_status(self) -> dict[str, str]:
        if self.raise_on_hydrate:
            raise self.raise_on_hydrate
        return dict(self._status)

    def set_task_status(
        self,
        task_id: str,
        status: str,
        *,
        author: str,
        note: str,
        ts: str,
    ) -> None:
        if self.raise_on_persist:
            raise self.raise_on_persist
        self.calls.append((task_id, status))
        self._status[task_id] = status


def test_backend_hydration_overrides_tasksjson_seed() -> None:
    """Runtime status in the backend wins over the tasks.json seed —
    that's the whole point of F-8. Without this, a task marked `done` in
    the DB would still show as `todo` here because tasks.json is stale."""
    tasks = [_mk("T-A", status="todo"), _mk("T-B", deps=["T-A"], status="todo")]
    backend = _FakeBackend({"T-A": "done"})
    q = TaskQueue(tasks, backend=backend)

    assert q.status("T-A") == "done"
    # And because T-A is `done`, T-B is now ready without any local mutation.
    assert [t.id for t in q.ready()] == ["T-B"]


def test_hydration_ignores_ids_not_in_tasksjson() -> None:
    tasks = [_mk("T-A", status="todo")]
    backend = _FakeBackend({"T-A": "done", "T-GHOST": "done"})
    q = TaskQueue(tasks, backend=backend)

    assert q.status("T-A") == "done"
    with pytest.raises(KeyError):
        q.status("T-GHOST")  # ghost id must not leak into the queue


def test_hydration_swallowed_when_backend_raises() -> None:
    """A hiccup at init must not stop the dispatch loop — we fall back to
    the tasks.json seed and log a warning."""
    tasks = [_mk("T-A", status="todo")]
    backend = _FakeBackend()
    backend.raise_on_hydrate = RuntimeError("db is locked")
    q = TaskQueue(tasks, backend=backend)  # must not raise
    assert q.status("T-A") == "todo"


def test_mark_done_persists_to_backend() -> None:
    tasks = [_mk("T-A", status="todo")]
    backend = _FakeBackend({"T-A": "todo"})
    q = TaskQueue(tasks, backend=backend)
    q.mark_done("T-A")
    assert backend.calls == [("T-A", "done")]
    assert backend._status["T-A"] == "done"


def test_mark_blocked_and_in_flight_persist_to_backend() -> None:
    tasks = [_mk("T-A", status="todo")]
    backend = _FakeBackend({"T-A": "todo"})
    q = TaskQueue(tasks, backend=backend)
    q.mark_in_flight("T-A")
    q.mark_blocked("T-A")
    assert backend.calls == [
        ("T-A", "in-progress"),
        ("T-A", "blocked"),
    ]


def test_mark_persistence_failure_does_not_abort_the_mutation() -> None:
    """The in-memory value must still update even when the backend rejects
    the write — otherwise a single DB hiccup would stall the whole loop."""
    tasks = [_mk("T-A", status="todo")]
    backend = _FakeBackend({"T-A": "todo"})
    backend.raise_on_persist = RuntimeError("illegal transition")
    q = TaskQueue(tasks, backend=backend)
    q.mark_done("T-A")  # must not raise
    assert q.status("T-A") == "done"


def test_no_backend_keeps_pre_F8_behavior() -> None:
    """Regression guard: constructing without `backend=` matches every call
    site that hasn't been ported (file backend, all existing unit tests)."""
    tasks = [_mk("T-A", status="todo")]
    q = TaskQueue(tasks)  # no backend kwarg
    q.mark_done("T-A")
    assert q.status("T-A") == "done"
