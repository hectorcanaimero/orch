"""Unit tests for `orchestrator.state` (R-010 + R-011).

Covers:
    - `EventLog` round-trip and event-type validation (FR-STATE-7).
    - `SpendLog` date rotation across UTC-day boundaries (FR-STATE-6, AS-12).
    - `RunFile` create/save/load round-trip, atomic-write retry-on-failure
      (FR-STATE-3).
    - `acquire_flock` contention returns `FlockContentionError` (FR-STATE-4,
      AS-09).
    - Shell-out wrappers subprocess `scripts/task-*.sh` correctly (C-1..C-3)
      — via `unittest.mock` (we NEVER touch real tasks.json in tests).
    - `reconcile_run` alive-adopts, dead+diff finishes, dead+no-diff blocks
      (FR-STATE-5, AS-07).
"""

from __future__ import annotations

import json
import os
import subprocess
from pathlib import Path
from unittest.mock import patch

import pytest

from orchestrator.models import Dispatch, SpendEntry, Task
from orchestrator.state import (
    EVENT_TYPES,
    EventLog,
    FlockContentionError,
    ReconcileReport,
    RunFile,
    SpendLog,
    _atomic_write,
    _reset_task_in_place,
    acquire_flock,
    call_task_block,
    call_task_finish,
    call_task_reset,
    call_task_start,
    load_tasks,
    reconcile_in_flight,
    reconcile_run,
    write_lock_holder,
)


TINY = Path(__file__).parent / "fixtures" / "tiny_tasks.json"


# ---- helpers ------------------------------------------------------------


def _mk_task(tid: str, files: list[str] | None = None) -> Task:
    return Task(
        id=tid,
        phase=1,
        title=f"Task {tid}",
        description="",
        model="opencode-go/glm-5.1",
        reason="",
        status="in-progress",
        dependencies=[],
        estimate_hours=0.1,
        files=list(files or []),
        spec_ref="",
        comments=[],
    )


def _mk_dispatch(tid: str, pid: int) -> Dispatch:
    return Dispatch(
        task_id=tid,
        backend="opencode",
        pid=pid,
        session_id="s-" + tid,
        started_at="2026-08-19T12:00:00Z",
        prompt_path=f"state/prompts/x/{tid}.txt",
        log_path=f"state/logs/{tid}.log",
        output_path=f"state/logs/{tid}.out",
    )


# ---- load_tasks re-home -------------------------------------------------


def test_load_tasks_from_state_module() -> None:
    """`load_tasks` MUST be importable from state.py — its canonical home."""
    tasks = load_tasks(TINY)
    assert {t.id for t in tasks} == {"T-A", "T-B", "T-C", "T-D", "T-E"}


def test_load_tasks_also_importable_from_task_queue() -> None:
    """Back-compat re-export: existing callers must keep working."""
    from orchestrator.task_queue import load_tasks as legacy_load

    from orchestrator.state import load_tasks as canonical_load

    assert legacy_load is canonical_load


# ---- EventLog -----------------------------------------------------------


def test_event_types_constant_locked() -> None:
    """FR-STATE-7 enumerates the exact set. Regression guard.

    `retry` was added in the follow-up closeout for FR-D-4 (retry-once).
    `escalate` was added in the FR-D-8 amendment (2026-08) for attempt-3
    escalation to `route.escalation_model`.
    `reconciled` was added when the orphan-PID reap loop landed.
    `budget_pause` and `budget_skip` were added in Sprint 7 for the provider
    budget guardrails (all-capped sleep + per-dispatch skip). Any further
    additions require a spec update.
    """
    expected = (
        "dispatch",
        "success",
        "fail",
        "block",
        "timeout",
        "retry",
        "escalate",
        "resume_adopt",
        "resume_revert",
        "id_spoof_detected",
        "flock_contention",
        "reconciled",
        "budget_pause",
        "budget_skip",
    )
    assert EVENT_TYPES == expected


def test_event_log_round_trip(tmp_path: Path) -> None:
    log = EventLog(tmp_path / "events.jsonl")
    log.emit("dispatch", "T-A", backend="opencode", pid=1234)
    log.emit("success", "T-A", backend="opencode", cost_usd=0.01)

    rows = [json.loads(line) for line in (tmp_path / "events.jsonl").read_text().splitlines()]
    assert len(rows) == 2
    assert rows[0]["event_type"] == "dispatch"
    assert rows[0]["task_id"] == "T-A"
    assert rows[0]["backend"] == "opencode"
    assert rows[0]["extra"]["pid"] == 1234
    # Schema keys locked (FR-STATE-7).
    # Fase 2: `project_id` es un campo adicional (default None → serializado
    # como null cuando no viene project_id). Retrocompat: filas viejas sin
    # el campo también se leen sin fallo (leído via json.load, `project_id`
    # ausente == None).
    assert set(rows[0].keys()) == {
        "event_type",
        "task_id",
        "backend",
        "ts",
        "extra",
        "project_id",
    }
    # Sin project_id explícito → None (serializado como null).
    assert rows[0]["project_id"] is None


def test_event_log_populates_project_id_when_configured(tmp_path: Path) -> None:
    """Fase 2: EventLog construido con project_id lo estampa en cada línea."""
    log = EventLog(tmp_path / "events.jsonl", project_id="my-proj")
    entry = log.emit("dispatch", "T-A", backend="opencode", pid=1)
    assert entry.project_id == "my-proj"
    row = json.loads((tmp_path / "events.jsonl").read_text().strip())
    assert row["project_id"] == "my-proj"


def test_event_entry_tolerates_legacy_row_without_project_id() -> None:
    """Un JSONL viejo (pre-Fase 2) sin `project_id` debe seguir leyéndose.

    El dashboard u otro consumidor que rehidrate `EventEntry` desde una fila
    vieja no tiene que romperse. `project_id` cae a None por default.
    """
    from orchestrator.models import EventEntry

    legacy_row = {
        "event_type": "dispatch",
        "task_id": "T-A",
        "backend": "opencode",
        "ts": "2026-08-19T12:00:00Z",
        "extra": {"pid": 1234},
    }
    entry = EventEntry(**legacy_row)  # sin project_id → default None
    assert entry.project_id is None
    assert entry.event_type == "dispatch"


def test_event_log_rejects_unknown_type(tmp_path: Path) -> None:
    log = EventLog(tmp_path / "events.jsonl")
    with pytest.raises(ValueError, match="unknown event_type"):
        log.emit("mystery", "T-A", backend="claude")
    # Nothing was written.
    assert not (tmp_path / "events.jsonl").exists() or (
        (tmp_path / "events.jsonl").read_text() == ""
    )


# ---- SpendLog -----------------------------------------------------------


def test_spend_log_records_row(tmp_path: Path) -> None:
    spend = SpendLog(tmp_path)
    entry = SpendEntry(
        ts="2026-08-19T12:00:00Z",
        task_id="T-A",
        backend="claude",
        model="opus",
        tokens_in=100,
        tokens_out=50,
        cost_usd=0.03,
        duration_s=1.2,
    )
    path = spend.record(entry)
    assert path.name == "spend-2026-08-19.jsonl"
    row = json.loads(path.read_text().strip())
    assert row["task_id"] == "T-A"
    assert row["cost_usd"] == 0.03


def test_spend_log_rotates_on_utc_date(tmp_path: Path) -> None:
    """Two entries with different UTC dates → two separate files (§10)."""
    spend = SpendLog(tmp_path)
    a = SpendEntry(
        ts="2026-08-19T23:59:59Z",
        task_id="T-A",
        backend="claude",
        model="opus",
        tokens_in=1,
        tokens_out=1,
        cost_usd=0.01,
        duration_s=1.0,
    )
    b = SpendEntry(
        ts="2026-08-20T00:00:01Z",
        task_id="T-B",
        backend="claude",
        model="opus",
        tokens_in=1,
        tokens_out=1,
        cost_usd=0.02,
        duration_s=1.0,
    )
    pa = spend.record(a)
    pb = spend.record(b)
    assert pa != pb
    assert pa.name == "spend-2026-08-19.jsonl"
    assert pb.name == "spend-2026-08-20.jsonl"


def test_spend_log_enriches_entry_with_project_id(tmp_path: Path) -> None:
    """Fase 2: SpendLog(state_dir, project_id=...) estampa el pid en cada fila.

    El SpendEntry se puede construir sin `project_id` (retrocompat con call
    sites); el log lo enriquece antes de serializar.
    """
    spend = SpendLog(tmp_path, project_id="tenant-a")
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
    assert entry.project_id is None  # el caller no lo puso
    path = spend.record(entry)
    row = json.loads(path.read_text().strip())
    assert row["project_id"] == "tenant-a"


def test_spend_log_respects_explicit_project_id_on_entry(tmp_path: Path) -> None:
    """Si el caller pasó project_id explícito en el SpendEntry, no lo pisamos."""
    spend = SpendLog(tmp_path, project_id="from-log")
    entry = SpendEntry(
        ts="2026-08-19T12:00:00Z",
        task_id="T-A",
        backend="claude",
        model="opus",
        tokens_in=1,
        tokens_out=1,
        cost_usd=0.01,
        duration_s=1.0,
        project_id="from-caller",
    )
    path = spend.record(entry)
    row = json.loads(path.read_text().strip())
    assert row["project_id"] == "from-caller"


# ---- RunFile ------------------------------------------------------------


def test_run_file_create_save_load_round_trip(tmp_path: Path) -> None:
    state_dir = tmp_path / "state"
    rf = RunFile.create(state_dir, run_id="test-run-1", mode="auto")
    rf.add_dispatch(_mk_dispatch("T-A", pid=1234))
    rf.mark_done("T-A")

    reloaded = RunFile.load(state_dir / "run-test-run-1.json")
    assert reloaded.state.run_id == "test-run-1"
    assert reloaded.state.mode == "auto"
    assert reloaded.state.in_flight == {}
    assert reloaded.state.completed == ["T-A"]


def test_run_file_atomic_write_retries_once_on_rename_failure(tmp_path: Path) -> None:
    """A transient `os.replace` failure MUST retry silently once (§7).

    We patch `os.replace` to fail only when writing the run file itself, then
    count how many times it was called for that path. `save()` also invokes
    `rebuild_index()` (writes `index.json`) — we intentionally exclude those
    from the flaky path so the test is robust to unrelated side-writes.
    """
    state_dir = tmp_path / "state"
    rf = RunFile.create(state_dir, run_id="retry-run", mode="auto")

    # Sanity: the create() succeeded, so os.replace works normally now.
    assert rf.path.exists()

    run_file_calls = {"n": 0}
    real_replace = os.replace

    def flaky(src, dst):
        # Only inject failure on writes targeting this run file, not on
        # index.json rewrites done by `rebuild_index()`.
        target = str(dst)
        if target == str(rf.path):
            run_file_calls["n"] += 1
            if run_file_calls["n"] == 1:
                raise OSError("simulated transient failure")
        return real_replace(src, dst)

    with patch("orchestrator.state.os.replace", side_effect=flaky):
        rf.mark_done("T-Z")  # forces a save → two _atomic_write attempts

    assert run_file_calls["n"] == 2  # first failed, retry succeeded
    reloaded = RunFile.load(rf.path)
    assert reloaded.state.completed == ["T-Z"]


def test_run_file_in_flight_serializes_dispatch(tmp_path: Path) -> None:
    """`Dispatch` inside `in_flight` must survive save/load intact."""
    state_dir = tmp_path / "state"
    rf = RunFile.create(state_dir, run_id="r", mode="auto")
    d = _mk_dispatch("T-A", pid=999)
    rf.add_dispatch(d)

    reloaded = RunFile.load(rf.path)
    got = reloaded.state.in_flight["T-A"]
    assert got.pid == 999
    assert got.backend == "opencode"
    assert got.session_id == "s-T-A"


# ---- flock --------------------------------------------------------------


def test_acquire_flock_first_call_succeeds(tmp_path: Path) -> None:
    lock_path = tmp_path / "state" / ".lock"
    fd = acquire_flock(lock_path)
    try:
        assert lock_path.exists()
        # Writing holder metadata should not raise.
        write_lock_holder(fd, run_id="alpha", pid=os.getpid())
    finally:
        fd.close()


def test_acquire_flock_second_call_raises_contention(tmp_path: Path) -> None:
    """AS-09: second orchestrator must fail fast with FlockContentionError."""
    lock_path = tmp_path / "state" / ".lock"
    fd1 = acquire_flock(lock_path)
    try:
        write_lock_holder(fd1, run_id="first-run", pid=os.getpid())
        with pytest.raises(FlockContentionError) as exc:
            acquire_flock(lock_path)
        # Holder run-id surfaces to the caller for the error message.
        assert exc.value.holder_run_id is not None
        assert "first-run" in exc.value.holder_run_id
    finally:
        fd1.close()


def test_acquire_flock_releases_on_close(tmp_path: Path) -> None:
    lock_path = tmp_path / "state" / ".lock"
    fd1 = acquire_flock(lock_path)
    fd1.close()
    # After close the lock must be re-acquirable — no zombie holder.
    fd2 = acquire_flock(lock_path)
    fd2.close()


# ---- Shell-out wrappers -------------------------------------------------


def test_call_task_start_invokes_script_with_correct_args() -> None:
    """No real subprocess — we mock the call and just verify the argv."""
    fake_result = subprocess.CompletedProcess(
        args=["scripts/task-start.sh", "T-A", "orchestrator"],
        returncode=0,
        stdout="OK",
        stderr="",
    )
    with patch("orchestrator.state._ensure_v2_cwd"), patch(
        "orchestrator.state.subprocess.run", return_value=fake_result
    ) as m:
        call_task_start("T-A")
    call = m.call_args
    assert call.args[0] == ["scripts/task-start.sh", "T-A", "orchestrator"]
    assert call.kwargs["check"] is False
    assert call.kwargs["capture_output"] is True
    assert call.kwargs["text"] is True


def test_call_task_finish_and_block_forward_args() -> None:
    fake = subprocess.CompletedProcess(args=[], returncode=0, stdout="", stderr="")
    with patch("orchestrator.state._ensure_v2_cwd"), patch(
        "orchestrator.state.subprocess.run", return_value=fake
    ) as m:
        call_task_finish("T-A", "done things", "claude/opus")
        call_task_block("T-B", "explained why", "codex/gpt-5.4")
    assert m.call_args_list[0].args[0] == [
        "scripts/task-finish.sh",
        "T-A",
        "done things",
        "claude/opus",
    ]
    assert m.call_args_list[1].args[0] == [
        "scripts/task-block.sh",
        "T-B",
        "explained why",
        "codex/gpt-5.4",
    ]


# ---- reconcile_run ------------------------------------------------------


def test_reconcile_alive_pid_is_adopted(tmp_path: Path) -> None:
    """PID alive → kept in in_flight, `resume_adopt` emitted."""
    state_dir = tmp_path / "state"
    rf = RunFile.create(state_dir, run_id="resume-1", mode="auto")
    rf.add_dispatch(_mk_dispatch("T-A", pid=os.getpid()))  # self PID = definitely alive
    ev = EventLog(state_dir / "events.jsonl")

    tasks_by_id = {"T-A": _mk_task("T-A")}
    report = reconcile_run(rf, ev, tasks_by_id)

    assert report.adopted == ["T-A"]
    assert report.reverted == []
    # Still in_flight because we adopted the live child.
    assert "T-A" in rf.state.in_flight
    lines = (state_dir / "events.jsonl").read_text().splitlines()
    assert any('"resume_adopt"' in line for line in lines)


def test_reconcile_dead_pid_no_files_reverts(tmp_path: Path) -> None:
    """PID dead, task has no files → block + revert (AS-07 safe default)."""
    state_dir = tmp_path / "state"
    rf = RunFile.create(state_dir, run_id="resume-2", mode="auto")
    rf.add_dispatch(_mk_dispatch("T-X", pid=999999))  # unlikely alive
    ev = EventLog(state_dir / "events.jsonl")

    tasks_by_id = {"T-X": _mk_task("T-X", files=[])}

    with patch("orchestrator.state.file_backend.pid_alive", return_value=False), patch(
        "orchestrator.state.call_task_block"
    ) as block_mock:
        report = reconcile_run(rf, ev, tasks_by_id)

    assert report.reverted == ["T-X"]
    assert report.adopted == []
    assert "T-X" not in rf.state.in_flight
    assert "T-X" in rf.state.blocked
    block_mock.assert_called_once()
    args = block_mock.call_args.args
    assert args[0] == "T-X"
    assert "no work detected" in args[1]


def test_reconcile_dead_pid_with_dirty_files_adopts_as_done(tmp_path: Path) -> None:
    """PID dead + git diff on declared files → finish + adopt."""
    state_dir = tmp_path / "state"
    rf = RunFile.create(state_dir, run_id="resume-3", mode="auto")
    rf.add_dispatch(_mk_dispatch("T-Y", pid=999999))
    ev = EventLog(state_dir / "events.jsonl")

    tasks_by_id = {"T-Y": _mk_task("T-Y", files=["src/foo.ts"])}

    with patch("orchestrator.state.file_backend.pid_alive", return_value=False), patch(
        "orchestrator.state._git_diff_touches", return_value=True
    ), patch("orchestrator.state.call_task_finish") as finish_mock:
        report = reconcile_run(rf, ev, tasks_by_id)

    assert report.adopted == ["T-Y"]
    assert report.reverted == []
    assert "T-Y" not in rf.state.in_flight
    assert "T-Y" in rf.state.completed
    finish_mock.assert_called_once()


def test_reconcile_report_has_expected_shape() -> None:
    r = ReconcileReport()
    assert r.adopted == []
    assert r.reverted == []
    assert r.errors == []


# ---- _atomic_write direct test -----------------------------------------


def test_atomic_write_creates_parent_dirs(tmp_path: Path) -> None:
    target = tmp_path / "deep" / "nested" / "file.json"
    _atomic_write(target, b'{"ok":true}')
    assert target.read_bytes() == b'{"ok":true}'


# ---- Sprint A / Issue #7: stuck in-progress recovery -------------------


def _write_tasks_json(root: Path, rows: list[dict]) -> Path:
    """Write a `tasks.json` with the given rows and return its path."""
    root.mkdir(parents=True, exist_ok=True)
    path = root / "tasks.json"
    path.write_text(
        json.dumps({"meta": {}, "phases": [], "tasks": rows}, indent=2),
        encoding="utf-8",
    )
    return path


def test_reset_task_in_place_reverts_in_progress_to_todo(tmp_path: Path) -> None:
    """`_reset_task_in_place` (Python fallback) reverts one task."""
    path = _write_tasks_json(tmp_path, [
        {"id": "F0.5.T1", "phase": 0, "title": "t", "model": "m", "status": "in-progress", "comments": []},
        {"id": "F0.5.T2", "phase": 0, "title": "t", "model": "m", "status": "todo", "comments": []},
    ])
    assert _reset_task_in_place(path, "F0.5.T1") is True
    reloaded = json.loads(path.read_text())
    row = next(r for r in reloaded["tasks"] if r["id"] == "F0.5.T1")
    assert row["status"] == "todo"
    # Audit comment appended.
    assert any("reset from in-progress" in c["body"] for c in row["comments"])
    # Other task untouched.
    other = next(r for r in reloaded["tasks"] if r["id"] == "F0.5.T2")
    assert other["status"] == "todo"


def test_reset_task_in_place_noop_when_not_in_progress(tmp_path: Path) -> None:
    """Resetting a `todo`/`done` task is a no-op returning False."""
    path = _write_tasks_json(tmp_path, [
        {"id": "F0.5.T1", "phase": 0, "title": "t", "model": "m", "status": "done", "comments": []},
    ])
    assert _reset_task_in_place(path, "F0.5.T1") is False
    row = json.loads(path.read_text())["tasks"][0]
    assert row["status"] == "done"  # untouched


def test_call_task_reset_prefers_shell_script_when_present(tmp_path: Path) -> None:
    """When `scripts/task-reset.sh` exists, it is invoked instead of the fallback."""
    _write_tasks_json(tmp_path, [
        {"id": "F0.5.T1", "phase": 0, "title": "t", "model": "m", "status": "in-progress", "comments": []},
    ])
    (tmp_path / "scripts").mkdir()
    for name in ("task-start.sh", "task-finish.sh", "task-block.sh", "task-reset.sh"):
        script = tmp_path / "scripts" / name
        script.write_text("#!/bin/sh\nexit 0\n")
        script.chmod(0o755)

    fake = subprocess.CompletedProcess(args=[], returncode=0, stdout="", stderr="")
    with patch("orchestrator.state.subprocess.run", return_value=fake) as m:
        result = call_task_reset("F0.5.T1", project_root=tmp_path)
    assert result is True
    # First positional arg is the argv list.
    argv = m.call_args.args[0]
    assert argv[:2] == ["scripts/task-reset.sh", "F0.5.T1"]


def test_call_task_reset_falls_back_when_script_missing(tmp_path: Path) -> None:
    """Legacy projects without task-reset.sh must still recover via the
    Python fallback (in-place atomic rewrite)."""
    path = _write_tasks_json(tmp_path, [
        {"id": "F0.5.T1", "phase": 0, "title": "t", "model": "m", "status": "in-progress", "comments": []},
    ])
    (tmp_path / "scripts").mkdir()
    for name in ("task-start.sh", "task-finish.sh", "task-block.sh"):
        script = tmp_path / "scripts" / name
        script.write_text("#!/bin/sh\nexit 0\n")
        script.chmod(0o755)
    # NO task-reset.sh present.

    assert call_task_reset("F0.5.T1", project_root=tmp_path) is True
    reloaded = json.loads(path.read_text())
    assert reloaded["tasks"][0]["status"] == "todo"


def _make_run_file_with_stale_in_flight(
    state_dir: Path, task_id: str, dead_pid: int
) -> Path:
    """Build a run-*.json where `task_id` has PID `dead_pid` in in_flight."""
    state_dir.mkdir(parents=True, exist_ok=True)
    run_id = "test-run-orphan"
    payload = {
        "run_id": run_id,
        "started_at": "2026-08-21T10:00:00Z",
        "mode": "auto",
        "in_flight": {
            task_id: {
                "task_id": task_id,
                "backend": "opencode",
                "pid": dead_pid,
                "session_id": "s-1",
                "started_at": "2026-08-21T10:00:00Z",
                "prompt_path": "",
                "log_path": "",
                "output_path": "",
                "attempt": 1,
            }
        },
        "completed": [],
        "blocked": [],
        "deferred": [],
    }
    path = state_dir / f"run-{run_id}.json"
    path.write_text(json.dumps(payload, indent=2))
    return path


def test_reconcile_reverts_in_progress_with_dead_pid(tmp_path: Path) -> None:
    """Extended reconcile_in_flight (Issue #7) must ALSO revert tasks.json
    when the PID is dead — not just the run-file's in_flight map."""
    state_dir = tmp_path / ".orchestrator" / "state"
    _make_run_file_with_stale_in_flight(state_dir, "F0.5.T1", dead_pid=999999)
    _write_tasks_json(tmp_path, [
        {"id": "F0.5.T1", "phase": 0, "title": "t", "model": "m", "status": "in-progress", "comments": []},
    ])
    (tmp_path / "scripts").mkdir()
    for name in ("task-start.sh", "task-finish.sh", "task-block.sh"):
        script = tmp_path / "scripts" / name
        script.write_text("#!/bin/sh\nexit 0\n")
        script.chmod(0o755)

    # Force PID to be reported dead deterministically.
    with patch("orchestrator.state.os.kill", side_effect=ProcessLookupError):
        report = reconcile_in_flight(state_dir, project_root=tmp_path)

    assert len(report["reconciled"]) == 1
    assert report["reconciled"][0]["task_id"] == "F0.5.T1"
    assert report["reset"] == ["F0.5.T1"]
    # tasks.json now says todo.
    reloaded = json.loads((tmp_path / "tasks.json").read_text())
    assert reloaded["tasks"][0]["status"] == "todo"


def test_reconcile_keeps_in_progress_with_alive_pid(tmp_path: Path) -> None:
    """Live PID → no reconcile, tasks.json untouched."""
    state_dir = tmp_path / ".orchestrator" / "state"
    _make_run_file_with_stale_in_flight(state_dir, "F0.5.T1", dead_pid=os.getpid())
    _write_tasks_json(tmp_path, [
        {"id": "F0.5.T1", "phase": 0, "title": "t", "model": "m", "status": "in-progress", "comments": []},
    ])

    # No monkeypatching of os.kill — self PID IS alive.
    report = reconcile_in_flight(state_dir, project_root=tmp_path)

    assert report["reconciled"] == []
    assert report["reset"] == []
    reloaded = json.loads((tmp_path / "tasks.json").read_text())
    assert reloaded["tasks"][0]["status"] == "in-progress"


def test_reconcile_reverts_with_project_root_none_skips_tasks_json(
    tmp_path: Path,
) -> None:
    """Backwards-compat: legacy call sites that don't pass project_root
    still get the run-file reconcile but skip tasks.json (silently). This
    preserves pre-Sprint-A behavior for tests / callers that don't know
    the project root."""
    state_dir = tmp_path / ".orchestrator" / "state"
    _make_run_file_with_stale_in_flight(state_dir, "F0.5.T1", dead_pid=999999)
    _write_tasks_json(tmp_path, [
        {"id": "F0.5.T1", "phase": 0, "title": "t", "model": "m", "status": "in-progress", "comments": []},
    ])

    with patch("orchestrator.state.os.kill", side_effect=ProcessLookupError):
        report = reconcile_in_flight(state_dir)  # project_root omitted

    # Run-file still reconciled (backwards-compat).
    assert len(report["reconciled"]) == 1
    # But tasks.json was NOT touched because project_root wasn't provided.
    assert report["reset"] == []
    reloaded = json.loads((tmp_path / "tasks.json").read_text())
    assert reloaded["tasks"][0]["status"] == "in-progress"
