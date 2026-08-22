"""Parity tests: run identical scenarios against BOTH state backends.

This file exercises every method of the `StateBackend` Protocol using the
`backend` fixture from `conftest.py`, which is parametrized over
`["file", "sqlite"]`. If a test passes here, the two backends agree on the
observable contract for that operation.

For the file backend, `set_task_status` shells out — we monkeypatch the
shell wrappers to isolate the test from disk. Sqlite writes go directly to
the DB and are observable via `get_task_status`.
"""

from __future__ import annotations

from pathlib import Path

import pytest

from orchestrator.models import Dispatch, EventEntry, SpendEntry, Task
from orchestrator.state import FileBackend
from orchestrator.state.interface import StateBackend


def _task(tid: str, status: str = "todo", comments: list[dict] | None = None) -> Task:
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
        comments=comments or [],
    )


def _dispatch(tid: str, pid: int = 111) -> Dispatch:
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


def _seed_project(backend: StateBackend, tmp_path: Path) -> None:
    """Bootstrap the backend with a tiny task set.

    For FileBackend we also need a valid `tasks.json` on disk because
    `get_task_status` reads it directly.
    """
    tasks = [_task("T-A"), _task("T-B"), _task("T-C")]
    if isinstance(backend, FileBackend):
        import json as _json

        (tmp_path / "tasks.json").write_text(_json.dumps({
            "tasks": [
                {
                    "id": t.id,
                    "phase": t.phase,
                    "title": t.title,
                    "description": t.description,
                    "model": t.model,
                    "reason": t.reason,
                    "status": t.status,
                    "dependencies": t.dependencies,
                    "estimateHours": t.estimate_hours,
                    "files": t.files,
                    "specRef": t.spec_ref,
                    "comments": t.comments,
                }
                for t in tasks
            ],
        }))
    backend.bootstrap(tasks)


# ---- task status --------------------------------------------------------


def test_parity_get_task_status_snapshot(backend: StateBackend, tmp_path: Path) -> None:
    _seed_project(backend, tmp_path)
    statuses = backend.get_all_task_status()
    assert {"T-A", "T-B", "T-C"} <= set(statuses.keys())
    assert all(statuses[t] == "todo" for t in ("T-A", "T-B", "T-C"))


def test_parity_get_unknown_task_returns_none(backend: StateBackend, tmp_path: Path) -> None:
    _seed_project(backend, tmp_path)
    assert backend.get_task_status("T-NOPE") is None


# ---- runs + dispatches --------------------------------------------------


def test_parity_run_lifecycle(backend: StateBackend, tmp_path: Path) -> None:
    _seed_project(backend, tmp_path)
    state = backend.create_run(run_id="parity-run-1", mode="auto")
    assert state.run_id == "parity-run-1"
    backend.add_dispatch("parity-run-1", _dispatch("T-A", pid=42))
    in_flight = list(backend.iter_in_flight("parity-run-1"))
    assert [d.task_id for d in in_flight] == ["T-A"]
    backend.remove_dispatch("parity-run-1", "T-A")
    assert list(backend.iter_in_flight("parity-run-1")) == []


def test_parity_save_and_load_run(backend: StateBackend, tmp_path: Path) -> None:
    _seed_project(backend, tmp_path)
    state = backend.create_run(run_id="parity-run-2", mode="auto")
    state.completed.append("T-A")
    state.blocked.append("T-B")
    state.in_flight["T-C"] = _dispatch("T-C", pid=77)
    backend.save_run(state)
    reloaded = backend.load_run("parity-run-2")
    assert reloaded.completed == ["T-A"]
    assert reloaded.blocked == ["T-B"]
    assert "T-C" in reloaded.in_flight
    assert reloaded.in_flight["T-C"].pid == 77


def test_parity_list_runs(backend: StateBackend, tmp_path: Path) -> None:
    _seed_project(backend, tmp_path)
    backend.create_run(run_id="r1", mode="auto")
    backend.create_run(run_id="r2", mode="semi")
    listing = backend.list_runs()
    ids = {row["run_id"] for row in listing}
    assert {"r1", "r2"} <= ids


def test_parity_clear_in_flight_for_run(backend: StateBackend, tmp_path: Path) -> None:
    _seed_project(backend, tmp_path)
    backend.create_run(run_id="r-clear", mode="auto")
    backend.add_dispatch("r-clear", _dispatch("T-A", pid=1))
    backend.add_dispatch("r-clear", _dispatch("T-B", pid=2))
    cleared = backend.clear_in_flight_for_run("r-clear")
    assert set(cleared) == {"T-A", "T-B"}
    assert list(backend.iter_in_flight("r-clear")) == []


# ---- events -------------------------------------------------------------


def test_parity_event_append_and_iter(backend: StateBackend, tmp_path: Path) -> None:
    _seed_project(backend, tmp_path)
    backend.create_run(run_id="r-ev", mode="auto")
    for i in range(3):
        backend.append_event("r-ev", EventEntry(
            event_type="dispatch",
            task_id=f"T-{i}",
            backend="opencode",
            ts=f"2026-08-19T12:00:0{i}Z",
            extra={"pid": 100 + i},
        ))
    rows = list(backend.iter_events(run_id="r-ev"))
    task_ids = [r["task_id"] for r in rows]
    # Both backends preserve insertion order for the same run's events.
    assert task_ids == ["T-0", "T-1", "T-2"]


# ---- Sprint C: extended iter_events + get_task_last_events --------------


def test_parity_iter_events_task_id_filter(backend: StateBackend, tmp_path: Path) -> None:
    _seed_project(backend, tmp_path)
    backend.create_run(run_id="r-tf", mode="auto")
    for i in range(4):
        backend.append_event("r-tf", EventEntry(
            event_type="dispatch",
            task_id="T-A" if i % 2 == 0 else "T-B",
            backend="opencode",
            ts=f"2026-08-19T12:00:0{i}Z",
        ))
    rows = list(backend.iter_events(task_id="T-A"))
    assert rows and all(r["task_id"] == "T-A" for r in rows)
    assert len(rows) == 2


def test_parity_iter_events_limit_returns_tail(backend: StateBackend, tmp_path: Path) -> None:
    _seed_project(backend, tmp_path)
    backend.create_run(run_id="r-lim", mode="auto")
    for i in range(5):
        backend.append_event("r-lim", EventEntry(
            event_type="dispatch",
            task_id="T-A",
            backend="opencode",
            ts=f"2026-08-19T12:00:0{i}Z",
        ))
    rows = list(backend.iter_events(task_id="T-A", limit=2))
    # Tail semantics: last 2 in chronological order.
    assert len(rows) == 2
    assert rows[0]["ts"] == "2026-08-19T12:00:03Z"
    assert rows[1]["ts"] == "2026-08-19T12:00:04Z"


def test_parity_get_task_last_events_full(backend: StateBackend, tmp_path: Path) -> None:
    _seed_project(backend, tmp_path)
    backend.create_run(run_id="r-le", mode="auto")
    # Two events for T-A (dispatch then success), one for T-B.
    backend.append_event("r-le", EventEntry(
        event_type="dispatch", task_id="T-A", backend="opencode",
        ts="2026-08-19T12:00:00Z",
    ))
    backend.append_event("r-le", EventEntry(
        event_type="success", task_id="T-A", backend="opencode",
        ts="2026-08-19T12:00:05Z",
    ))
    backend.append_event("r-le", EventEntry(
        event_type="dispatch", task_id="T-B", backend="opencode",
        ts="2026-08-19T12:00:03Z",
    ))
    last = backend.get_task_last_events()
    assert set(last.keys()) >= {"T-A", "T-B"}
    assert last["T-A"]["event_type"] == "success"
    assert last["T-A"]["ts"] == "2026-08-19T12:00:05Z"
    assert last["T-B"]["event_type"] == "dispatch"


def test_parity_get_task_last_events_whitelist(backend: StateBackend, tmp_path: Path) -> None:
    _seed_project(backend, tmp_path)
    backend.create_run(run_id="r-wl", mode="auto")
    for tid, ts in [("T-A", "01"), ("T-B", "02"), ("T-C", "03")]:
        backend.append_event("r-wl", EventEntry(
            event_type="dispatch", task_id=tid, backend="opencode",
            ts=f"2026-08-19T12:00:{ts}Z",
        ))
    last = backend.get_task_last_events(task_ids=["T-A", "T-C"])
    assert set(last.keys()) == {"T-A", "T-C"}
    # Empty whitelist returns an empty mapping without touching storage.
    assert backend.get_task_last_events(task_ids=[]) == {}


def test_parity_get_task_last_events_no_events(backend: StateBackend, tmp_path: Path) -> None:
    _seed_project(backend, tmp_path)
    assert backend.get_task_last_events() == {}


# ---- spend --------------------------------------------------------------


def test_parity_spend_append_and_iter(backend: StateBackend, tmp_path: Path) -> None:
    _seed_project(backend, tmp_path)
    for i in range(3):
        backend.append_spend(SpendEntry(
            ts=f"2026-08-{19+i:02d}T12:00:00Z",
            task_id=f"T-{i}",
            backend="claude",
            model="opus",
            tokens_in=10,
            tokens_out=5,
            cost_usd=0.01 * (i + 1),
            duration_s=1.0,
        ))
    rows = list(backend.iter_all_spend())
    assert len(rows) >= 3
    assert {r["task_id"] for r in rows} >= {"T-0", "T-1", "T-2"}


def test_parity_spend_window_filter(backend: StateBackend, tmp_path: Path) -> None:
    _seed_project(backend, tmp_path)
    for i in range(3):
        backend.append_spend(SpendEntry(
            ts=f"2026-08-{19+i:02d}T12:00:00Z",
            task_id=f"T-{i}",
            backend="claude",
            model="opus",
            tokens_in=1,
            tokens_out=1,
            cost_usd=0.01,
            duration_s=1.0,
        ))
    rows = list(backend.iter_spend(
        since="2026-08-20T00:00:00Z",
        until="2026-08-20T23:59:59Z",
    ))
    ids = {r["task_id"] for r in rows}
    assert ids == {"T-1"}


# ---- reconciliation -----------------------------------------------------


def test_parity_reconcile_in_flight_smoke(backend: StateBackend, tmp_path: Path) -> None:
    """`reconcile_in_flight` should return a well-shaped dict for both backends."""
    _seed_project(backend, tmp_path)
    backend.create_run(run_id="r-rec", mode="auto")
    report = backend.reconcile_in_flight()
    assert isinstance(report, dict)
    assert "reconciled" in report
    assert "checked" in report


# ---- bootstrap idempotency ---------------------------------------------


def test_parity_bootstrap_is_idempotent(backend: StateBackend, tmp_path: Path) -> None:
    tasks = [_task("T-Boot")]
    if isinstance(backend, FileBackend):
        import json as _json

        (tmp_path / "tasks.json").write_text(_json.dumps({"tasks": [{
            "id": "T-Boot", "phase": 1, "title": "", "model": "opencode/glm-5.1",
            "reason": "", "status": "todo", "dependencies": [],
            "estimateHours": 0.1, "files": [], "specRef": "", "comments": [],
        }]}))
    backend.bootstrap(tasks)
    backend.bootstrap(tasks)  # must not raise
