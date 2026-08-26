"""Unit tests for `orchestrator.state.sqlite_backend` (Sprint B — commit 2).

Covers:
    - Schema bootstrap + `PRAGMA user_version` matches the shipped migration.
    - Round-trip: `create_run` / `save_run` / `load_run` preserves in_flight,
      completed, blocked, deferred.
    - `bootstrap()` seeds `tasks_runtime` idempotently.
    - `set_task_status` enforces legal transitions and refuses unknown ids.
    - Multi-project isolation: two backends over one DB never see each other.
    - Events + spend: append, iter, dedup by hash.
    - Concurrent writers (2 subprocesses) both land rows without corruption.
    - Comments JSON round-trips through `tasks_runtime.comments_json`.
"""

from __future__ import annotations

import json
import multiprocessing as mp
import sqlite3
from pathlib import Path

import pytest

from orchestrator.models import Dispatch, EventEntry, SpendEntry, Task
from orchestrator.state import _reset_backend_cache
from orchestrator.state.sqlite_backend import (
    SqliteBackend,
    _read_migrations,
    _reset_schema_cache_for_tests,
)


# ---- helpers ------------------------------------------------------------


def _task(tid: str, status: str = "todo") -> Task:
    return Task(
        id=tid,
        phase=1,
        title=f"Task {tid}",
        description="",
        model="opencode/glm-5.1",
        reason="",
        status=status,  # type: ignore[arg-type]
        dependencies=[],
        estimate_hours=0.1,
        files=[],
        spec_ref="",
        comments=[],
    )


def _dispatch(tid: str, pid: int = 1234) -> Dispatch:
    return Dispatch(
        task_id=tid,
        backend="opencode",
        pid=pid,
        session_id=f"s-{tid}",
        started_at="2026-08-19T12:00:00Z",
        prompt_path=f"prompts/{tid}.txt",
        log_path=f"logs/{tid}.log",
        output_path=f"logs/{tid}.out",
    )


@pytest.fixture(autouse=True)
def _reset_cache():
    _reset_backend_cache()
    _reset_schema_cache_for_tests()
    yield
    _reset_backend_cache()
    _reset_schema_cache_for_tests()


# ---- schema -------------------------------------------------------------


def test_schema_bootstrap_sets_user_version(tmp_path: Path) -> None:
    db = tmp_path / "orch.db"
    be = SqliteBackend(db_path=db, project_id="p1")
    assert be.schema_version() >= 1
    # Verify all expected tables exist.
    conn = sqlite3.connect(str(db))
    try:
        rows = conn.execute(
            "SELECT name FROM sqlite_master WHERE type='table' ORDER BY name"
        ).fetchall()
        names = {r[0] for r in rows}
    finally:
        conn.close()
    assert {"projects", "tasks_runtime", "runs", "dispatches", "events", "spend",
            "tasks_definition"} <= names


def test_migration_files_discovered() -> None:
    migrations = _read_migrations()
    assert migrations, "expected at least 001_init.sql"
    assert migrations[0][0] == 1
    assert "001" in migrations[0][1]


def test_bootstrap_is_idempotent(tmp_path: Path) -> None:
    db = tmp_path / "orch.db"
    be = SqliteBackend(db_path=db, project_id="p1", project_root=tmp_path)
    tasks = [_task("T-A"), _task("T-B", status="in-progress")]
    be.bootstrap(tasks)
    be.bootstrap(tasks)  # second call must not raise or duplicate rows
    conn = sqlite3.connect(str(db))
    try:
        n = conn.execute(
            "SELECT COUNT(*) FROM tasks_runtime WHERE project_id = 'p1'"
        ).fetchone()[0]
    finally:
        conn.close()
    assert n == 2


# ---- task status --------------------------------------------------------


def test_task_status_round_trip(tmp_path: Path) -> None:
    be = SqliteBackend(db_path=tmp_path / "orch.db", project_id="p1")
    be.bootstrap([_task("T-A")])
    assert be.get_task_status("T-A") == "todo"
    be.set_task_status("T-A", "in-progress", author="orch", note="starting", ts="2026-08-19T12:00:00Z")
    assert be.get_task_status("T-A") == "in-progress"
    be.set_task_status("T-A", "done", author="orch", note="ok", ts="2026-08-19T12:00:01Z")
    assert be.get_task_status("T-A") == "done"


def test_task_status_unknown_raises_key_error(tmp_path: Path) -> None:
    be = SqliteBackend(db_path=tmp_path / "orch.db", project_id="p1")
    be.bootstrap([_task("T-A")])
    with pytest.raises(KeyError):
        be.set_task_status("T-NOPE", "done", author="x", note="", ts="2026-08-19T12:00:00Z")


def test_task_status_illegal_transition_raises_value_error(tmp_path: Path) -> None:
    be = SqliteBackend(db_path=tmp_path / "orch.db", project_id="p1")
    be.bootstrap([_task("T-A")])
    # todo → done directly is illegal (must go through in-progress).
    with pytest.raises(ValueError, match="illegal transition"):
        be.set_task_status("T-A", "done", author="x", note="", ts="2026-08-19T12:00:00Z")


def test_task_status_comments_round_trip(tmp_path: Path) -> None:
    be = SqliteBackend(db_path=tmp_path / "orch.db", project_id="p1")
    be.bootstrap([_task("T-A")])
    be.set_task_status("T-A", "in-progress", author="orch", note="starting", ts="2026-08-19T12:00:00Z")
    be.set_task_status("T-A", "done", author="orch", note="all good", ts="2026-08-19T12:00:01Z")
    conn = sqlite3.connect(str(tmp_path / "orch.db"))
    try:
        row = conn.execute(
            "SELECT comments_json FROM tasks_runtime "
            "WHERE project_id='p1' AND task_id='T-A'"
        ).fetchone()
    finally:
        conn.close()
    comments = json.loads(row[0])
    assert len(comments) == 2
    assert comments[0]["body"] == "starting"
    assert comments[1]["body"] == "all good"


def test_get_all_task_status_snapshot(tmp_path: Path) -> None:
    be = SqliteBackend(db_path=tmp_path / "orch.db", project_id="p1")
    be.bootstrap([_task("T-A"), _task("T-B", status="in-progress")])
    snap = be.get_all_task_status()
    assert snap == {"T-A": "todo", "T-B": "in-progress"}


# ---- multi-project isolation -------------------------------------------


def test_multi_project_isolation(tmp_path: Path) -> None:
    db = tmp_path / "orch.db"
    be_a = SqliteBackend(db_path=db, project_id="proj-a")
    be_b = SqliteBackend(db_path=db, project_id="proj-b")
    be_a.bootstrap([_task("T-A")])
    be_b.bootstrap([_task("T-A"), _task("T-B")])
    be_a.set_task_status("T-A", "in-progress", "orch", "start", "2026-08-19T12:00:00Z")
    assert be_a.get_task_status("T-A") == "in-progress"
    assert be_b.get_task_status("T-A") == "todo"
    assert be_a.get_task_status("T-B") is None
    assert be_b.get_task_status("T-B") == "todo"


# ---- runs / dispatches -------------------------------------------------


def test_run_create_load_save_round_trip(tmp_path: Path) -> None:
    be = SqliteBackend(db_path=tmp_path / "orch.db", project_id="p1")
    state = be.create_run(run_id="r1", mode="auto")
    assert state.run_id == "r1"
    assert state.mode == "auto"
    state.in_flight["T-A"] = _dispatch("T-A", pid=42)
    state.completed.append("T-Z")
    be.save_run(state)
    reloaded = be.load_run("r1")
    assert reloaded.completed == ["T-Z"]
    assert "T-A" in reloaded.in_flight
    assert reloaded.in_flight["T-A"].pid == 42


def test_add_and_remove_dispatch(tmp_path: Path) -> None:
    be = SqliteBackend(db_path=tmp_path / "orch.db", project_id="p1")
    be.create_run(run_id="r1", mode="auto")
    be.add_dispatch("r1", _dispatch("T-A"))
    seen = list(be.iter_in_flight("r1"))
    assert [d.task_id for d in seen] == ["T-A"]
    be.remove_dispatch("r1", "T-A")
    assert list(be.iter_in_flight("r1")) == []


def test_clear_in_flight_for_run(tmp_path: Path) -> None:
    be = SqliteBackend(db_path=tmp_path / "orch.db", project_id="p1")
    be.create_run(run_id="r1", mode="auto")
    be.add_dispatch("r1", _dispatch("T-A"))
    be.add_dispatch("r1", _dispatch("T-B", pid=99))
    cleared = be.clear_in_flight_for_run("r1")
    assert set(cleared) == {"T-A", "T-B"}
    assert list(be.iter_in_flight("r1")) == []


def test_list_runs_returns_newest_first(tmp_path: Path) -> None:
    be = SqliteBackend(db_path=tmp_path / "orch.db", project_id="p1")
    be.create_run(run_id="r1", mode="auto")
    be.create_run(run_id="r2", mode="semi")
    listing = be.list_runs()
    assert {row["run_id"] for row in listing} == {"r1", "r2"}


# ---- events + spend ---------------------------------------------------


def test_events_append_and_iter(tmp_path: Path) -> None:
    be = SqliteBackend(db_path=tmp_path / "orch.db", project_id="p1")
    be.create_run(run_id="r1", mode="auto")
    for i in range(3):
        entry = EventEntry(
            event_type="dispatch",
            task_id=f"T-{i}",
            backend="opencode",
            ts=f"2026-08-19T12:00:0{i}Z",
            extra={"pid": 100 + i},
        )
        be.append_event("r1", entry)
    rows = list(be.iter_events(run_id="r1"))
    assert [r["task_id"] for r in rows] == ["T-0", "T-1", "T-2"]
    assert rows[0]["extra"]["pid"] == 100


def test_events_dedup_hash_prevents_duplicates(tmp_path: Path) -> None:
    """Re-appending the same (ts, task_id, event_type, run_id, pid) is a no-op."""
    be = SqliteBackend(db_path=tmp_path / "orch.db", project_id="p1")
    be.create_run(run_id="r1", mode="auto")
    entry = EventEntry(
        event_type="dispatch",
        task_id="T-A",
        backend="opencode",
        ts="2026-08-19T12:00:00Z",
        extra={"pid": 42},
    )
    be.append_event("r1", entry)
    be.append_event("r1", entry)  # identical → dedup_hash blocks it
    assert len(list(be.iter_events(run_id="r1"))) == 1


def test_events_iter_since_id(tmp_path: Path) -> None:
    """Dashboard live tail uses `since_id` to poll new rows only."""
    be = SqliteBackend(db_path=tmp_path / "orch.db", project_id="p1")
    be.create_run(run_id="r1", mode="auto")
    for i in range(5):
        be.append_event("r1", EventEntry(
            event_type="dispatch",
            task_id=f"T-{i}",
            backend="opencode",
            ts=f"2026-08-19T12:00:0{i}Z",
            extra={"pid": i},
        ))
    all_rows = list(be.iter_events(run_id="r1"))
    assert len(all_rows) == 5
    since = all_rows[2]["id"]
    tail = list(be.iter_events(run_id="r1", since_id=since))
    assert len(tail) == 2
    assert tail[0]["task_id"] == "T-3"


def test_spend_append_and_iter(tmp_path: Path) -> None:
    be = SqliteBackend(db_path=tmp_path / "orch.db", project_id="p1")
    for i in range(3):
        be.append_spend(SpendEntry(
            ts=f"2026-08-{19+i:02d}T12:00:00Z",
            task_id=f"T-{i}",
            backend="claude",
            model="opus",
            tokens_in=10,
            tokens_out=5,
            cost_usd=0.01 * (i + 1),
            duration_s=1.0,
        ))
    rows = list(be.iter_spend())
    assert [r["task_id"] for r in rows] == ["T-0", "T-1", "T-2"]
    assert rows[0]["cost_usd"] == 0.01


def test_spend_window_filter(tmp_path: Path) -> None:
    be = SqliteBackend(db_path=tmp_path / "orch.db", project_id="p1")
    for i in range(3):
        be.append_spend(SpendEntry(
            ts=f"2026-08-{19+i}T12:00:00Z",
            task_id=f"T-{i}",
            backend="claude",
            model="opus",
            tokens_in=1,
            tokens_out=1,
            cost_usd=0.01,
            duration_s=1.0,
        ))
    windowed = list(be.iter_spend(
        since="2026-08-20T00:00:00Z",
        until="2026-08-20T23:59:59Z",
    ))
    assert [r["task_id"] for r in windowed] == ["T-1"]


def test_spend_dedup(tmp_path: Path) -> None:
    be = SqliteBackend(db_path=tmp_path / "orch.db", project_id="p1")
    entry = SpendEntry(
        ts="2026-08-19T12:00:00Z",
        task_id="T-A",
        backend="claude",
        model="opus",
        tokens_in=1,
        tokens_out=1,
        cost_usd=0.01,
        duration_s=1.0,
    )
    be.append_spend(entry)
    be.append_spend(entry)
    assert len(list(be.iter_spend())) == 1


# ---- WAL concurrent writers -------------------------------------------


def _child_writer(db_path_str: str, project_id: str, prefix: str, count: int) -> None:
    """Runs in a subprocess. Opens its own backend and writes `count` events."""
    from orchestrator.models import EventEntry
    from orchestrator.state.sqlite_backend import SqliteBackend, _reset_schema_cache_for_tests

    _reset_schema_cache_for_tests()
    be = SqliteBackend(db_path=Path(db_path_str), project_id=project_id)
    be.create_run(run_id=f"r-{prefix}", mode="auto")
    for i in range(count):
        be.append_event(f"r-{prefix}", EventEntry(
            event_type="dispatch",
            task_id=f"{prefix}-{i}",
            backend="opencode",
            ts=f"2026-08-19T12:{i:02d}:00Z",
            extra={"pid": 1000 + i},
        ))


def test_wal_concurrent_writers_from_two_processes(tmp_path: Path) -> None:
    """Two subprocesses writing concurrently must both land all their rows."""
    db = tmp_path / "orch.db"
    # Pre-init schema so both children start against a ready DB.
    SqliteBackend(db_path=db, project_id="p1")
    ctx = mp.get_context("spawn")
    p1 = ctx.Process(target=_child_writer, args=(str(db), "p1", "A", 10))
    p2 = ctx.Process(target=_child_writer, args=(str(db), "p1", "B", 10))
    p1.start()
    p2.start()
    p1.join(timeout=30)
    p2.join(timeout=30)
    assert p1.exitcode == 0, "writer A failed"
    assert p2.exitcode == 0, "writer B failed"
    be = SqliteBackend(db_path=db, project_id="p1")
    rows = list(be.iter_events())
    task_ids = {r["task_id"] for r in rows}
    a_ids = {f"A-{i}" for i in range(10)}
    b_ids = {f"B-{i}" for i in range(10)}
    assert a_ids <= task_ids, f"missing A rows: {a_ids - task_ids}"
    assert b_ids <= task_ids, f"missing B rows: {b_ids - task_ids}"


# ---- transactions ------------------------------------------------------


def test_write_rollback_on_error(tmp_path: Path) -> None:
    """An exception mid-transaction must NOT leak partial state to disk."""
    be = SqliteBackend(db_path=tmp_path / "orch.db", project_id="p1")
    be.create_run(run_id="r1", mode="auto")
    original_count = len(list(be.iter_events(run_id="r1")))

    class Boom(Exception):
        pass

    # Use the _write context manager directly so we can raise inside.
    try:
        with be._write() as conn:
            conn.execute(
                "INSERT INTO events "
                "(project_id, run_id, event_type, task_id, backend, ts, extra_json) "
                "VALUES ('p1', 'r1', 'dispatch', 'X', '', '2026-08-19T12:00:00Z', '{}')"
            )
            raise Boom("simulated failure mid-transaction")
    except Boom:
        pass

    after_count = len(list(be.iter_events(run_id="r1")))
    assert after_count == original_count, "rollback did not undo the insert"


# ---- factory selection -------------------------------------------------


def test_get_backend_selects_sqlite_from_config(tmp_path: Path) -> None:
    """Factory returns SqliteBackend when cfg says so."""
    from orchestrator.state import get_backend

    class FakePaths:
        state_dir = tmp_path / "state"
        project_id = "p1"
        project_root = tmp_path

    cfg = {"state": {"backend": "sqlite", "sqlite_path": "orch.db"}}
    be = get_backend(FakePaths, cfg)
    assert isinstance(be, SqliteBackend)


def test_get_backend_defaults_to_file(tmp_path: Path) -> None:
    """No cfg → file backend (backwards compat)."""
    from orchestrator.state import FileBackend, get_backend

    class FakePaths:
        state_dir = tmp_path / "state"
        project_id = "p1"
        project_root = tmp_path

    be = get_backend(FakePaths, None)
    assert isinstance(be, FileBackend)


def test_get_backend_unknown_raises(tmp_path: Path) -> None:
    from orchestrator.state import BackendFactoryError, get_backend

    class FakePaths:
        state_dir = tmp_path / "state"
        project_id = "p1"
        project_root = tmp_path

    with pytest.raises(BackendFactoryError, match="unknown state backend"):
        get_backend(FakePaths, {"state": {"backend": "mystery"}})


# ---- tasks_definition tests (Sprint F-1 Task 6) -------------------------


def test_bootstrap_seeds_tasks_definition(tmp_path: Path) -> None:
    """bootstrap() must INSERT tasks_definition rows alongside tasks_runtime."""
    db = tmp_path / "orch.db"
    sb = SqliteBackend(db, "proj")
    tasks = [
        Task(
            id="T1",
            title="First",
            model="claude",
            status="todo",
            dependencies=["T0"],
            files=["a.py"],
            spec_ref="specs/f.md",
            phase=1,
            estimate_hours=2.0,
            reason="fast",
            description="desc",
            comments=[],
        ),
    ]
    sb.bootstrap(tasks)

    conn = sqlite3.connect(db)
    row = conn.execute(
        "SELECT title, model, deps_json, files_json, spec_ref, phase, estimate_h "
        "FROM tasks_definition WHERE project_id='proj' AND task_id='T1'"
    ).fetchone()
    conn.close()

    assert row is not None, "tasks_definition row must be created by bootstrap()"
    assert row[0] == "First"
    assert row[1] == "claude"
    assert json.loads(row[2]) == ["T0"]
    assert json.loads(row[3]) == ["a.py"]
    assert row[4] == "specs/f.md"
    assert row[5] == 1
    assert row[6] == 2.0


def test_bootstrap_definition_is_ignore_on_rerun(tmp_path: Path) -> None:
    """Re-running bootstrap() must not overwrite tasks_definition (INSERT OR IGNORE)."""
    db = tmp_path / "orch.db"
    sb = SqliteBackend(db, "proj")
    task = Task(
        id="T1",
        title="Original",
        model="claude",
        status="todo",
        dependencies=[],
        files=[],
        spec_ref=None,  # type: ignore[arg-type]
        phase=1,
        estimate_hours=1.0,
        reason="",
        description="",
        comments=[],
    )
    sb.bootstrap([task])

    # Re-bootstrap with different title — should NOT overwrite
    task2 = Task(
        id="T1",
        title="Changed",
        model="gemini",
        status="todo",
        dependencies=[],
        files=[],
        spec_ref=None,  # type: ignore[arg-type]
        phase=1,
        estimate_hours=1.0,
        reason="",
        description="",
        comments=[],
    )
    sb.bootstrap([task2])

    conn = sqlite3.connect(db)
    title = conn.execute(
        "SELECT title FROM tasks_definition WHERE task_id='T1'"
    ).fetchone()[0]
    conn.close()
    assert title == "Original", "bootstrap() must not overwrite existing definition rows"


def test_upsert_task_definition_inserts_and_updates(tmp_path: Path) -> None:
    """upsert_task_definition() must INSERT OR REPLACE — update on re-atomize."""
    db = tmp_path / "orch.db"
    sb = SqliteBackend(db, "proj")
    task = Task(
        id="T1",
        title="Original",
        model="claude",
        status="todo",
        dependencies=[],
        files=[],
        spec_ref=None,  # type: ignore[arg-type]
        phase=1,
        estimate_hours=1.0,
        reason="",
        description="",
        comments=[],
    )
    sb.bootstrap([task])
    sb.set_task_status(
        "T1", "in-progress", author="orch", note="starting", ts="2026-08-25T10:00:00Z"
    )

    # Re-atomize changes the model
    sb.upsert_task_definition(
        task_id="T1",
        title="Original",
        model="gemini",
        backend=None,
        deps=[],
        spec_ref=None,
        phase=1,
        estimate_h=1.0,
        reason="",
        files=[],
    )

    conn = sqlite3.connect(db)
    model = conn.execute(
        "SELECT model FROM tasks_definition WHERE task_id='T1'"
    ).fetchone()[0]
    status = conn.execute(
        "SELECT status FROM tasks_runtime WHERE task_id='T1'"
    ).fetchone()[0]
    conn.close()
    assert model == "gemini", "upsert must update model in tasks_definition"
    assert status == "in-progress", "upsert must NOT touch tasks_runtime status"


def test_set_task_model_updates_definition(tmp_path: Path) -> None:
    """set_task_model() must update tasks_definition.model only."""
    db = tmp_path / "orch.db"
    sb = SqliteBackend(db, "proj")
    task = Task(
        id="T2",
        title="T",
        model="claude",
        status="todo",
        dependencies=[],
        files=[],
        spec_ref=None,  # type: ignore[arg-type]
        phase=1,
        estimate_hours=1.0,
        reason="",
        description="",
        comments=[],
    )
    sb.bootstrap([task])

    sb.set_task_model("T2", "codex")

    conn = sqlite3.connect(db)
    model = conn.execute(
        "SELECT model FROM tasks_definition WHERE task_id='T2'"
    ).fetchone()[0]
    conn.close()
    assert model == "codex"


def test_set_task_model_raises_on_missing_task(tmp_path: Path) -> None:
    """set_task_model() must raise KeyError when task not in tasks_definition."""
    db = tmp_path / "orch.db"
    sb = SqliteBackend(db, "proj")
    sb.bootstrap([])  # empty project

    with pytest.raises(KeyError, match="MISSING"):
        sb.set_task_model("MISSING", "claude")


def test_set_task_backend_raises_on_missing_task(tmp_path: Path) -> None:
    """set_task_backend() must raise KeyError when task not in tasks_definition."""
    db = tmp_path / "orch.db"
    sb = SqliteBackend(db, "proj")
    sb.bootstrap([])

    with pytest.raises(KeyError, match="MISSING"):
        sb.set_task_backend("MISSING", "opencode")


def test_set_task_backend_updates_definition(tmp_path: Path) -> None:
    """set_task_backend() must update tasks_definition.backend only."""
    db = tmp_path / "orch.db"
    sb = SqliteBackend(db, "proj")
    task = Task(
        id="T3",
        title="T",
        model="claude",
        status="todo",
        dependencies=[],
        files=[],
        spec_ref=None,  # type: ignore[arg-type]
        phase=1,
        estimate_hours=1.0,
        reason="",
        description="",
        comments=[],
    )
    sb.bootstrap([task])

    sb.set_task_backend("T3", "opencode")

    conn = sqlite3.connect(db)
    backend = conn.execute(
        "SELECT backend FROM tasks_definition WHERE task_id='T3'"
    ).fetchone()[0]
    conn.close()
    assert backend == "opencode"
