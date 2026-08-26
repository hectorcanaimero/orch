"""Integration tests for WorktreeManager wiring in _reap_once and _install_sigint.

Verifies that Sprint F-2 worktree lifecycle hooks are called at the correct
times:
    - wm.push(task_id) is called on success (and only on success)
    - wm.remove(task_id) is always called after reap
    - wm.remove_all() is called in main() after _drain_wait (not in the SIGINT handler)

All WorktreeManager calls are mocked — no real git commands are run.
"""
from __future__ import annotations

import os
import signal
import time
from dataclasses import dataclass
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from orchestrator.dispatcher import DispatchResult
from orchestrator.models import Dispatch, RouteEntry, Task
from orchestrator.orch import (
    InFlight,
    TaskQueue,
    _DrainFlag,
    _Sem,
    _install_sigint,
    _reap_once,
)
from orchestrator.worktree import WorktreeError, WorktreeManager


# ---- Fixtures ---------------------------------------------------------------


@pytest.fixture(autouse=True)
def _restore_sigint():
    """Restore original SIGINT/SIGTERM handlers after each test.

    _install_sigint installs global signal handlers without cleanup.
    This fixture ensures no state leaks between tests.
    """
    import signal as _signal
    old_sigint = _signal.getsignal(_signal.SIGINT)
    old_sigterm = _signal.getsignal(_signal.SIGTERM)
    yield
    _signal.signal(_signal.SIGINT, old_sigint)
    _signal.signal(_signal.SIGTERM, old_sigterm)


# ---- Helpers ----------------------------------------------------------------


def _make_task(task_id: str = "T-WT1") -> Task:
    return Task(
        id=task_id,
        phase=1,
        title="worktree test",
        description="",
        model="opencode/test-model",
        reason="",
        status="todo",
        dependencies=[],
        estimate_hours=0.1,
        files=[],
        spec_ref="",
        comments=[],
    )


def _make_route(backend: str = "opencode") -> RouteEntry:
    return RouteEntry(
        backend=backend,
        cli_model="test/model",
        tier="cheap",
        is_premium=False,
        fallback_cli_model=None,
        escalation_model=None,
    )


def _make_dispatch(pid: int, log_path: str = "/dev/null") -> Dispatch:
    return Dispatch(
        task_id="T-WT1",
        backend="opencode",
        pid=pid,
        session_id="s-test",
        started_at="2026-08-26T00:00:00Z",
        prompt_path="/dev/null",
        log_path=log_path,
        output_path="",
        attempt=1,
    )


def _make_backend(success: bool = True) -> MagicMock:
    backend = MagicMock()
    backend.parse_result.return_value = DispatchResult(
        exit_code=0 if success else 1,
        success=success,
        cost_usd=0.0,
        tokens_in=0,
        tokens_out=0,
        stdout="",
        stderr="",
        error_message=None if success else "simulated failure",
    )
    return backend


def _make_in_flight(
    pid: int,
    *,
    task_id: str = "T-WT1",
    worktree_path: "Path | None" = None,
    success: bool = True,
) -> dict[int, InFlight]:
    """Return a minimal in_flight dict with one entry."""
    task = _make_task(task_id)
    route = _make_route()
    backend = _make_backend(success)
    dispatch = _make_dispatch(pid)
    entry = InFlight(
        task=task,
        route=route,
        backend=backend,
        dispatch=dispatch,
        started_at_mono=time.monotonic(),
        timeout_s=60.0,
        timed_out=False,
        task_lock_fd=None,
        worktree_path=worktree_path,
    )
    return {pid: entry}


def _make_mocks(tmp_path: Path, *, backend_name: str = "opencode"):
    """Build the minimal mocks _reap_once needs beyond in_flight and wm."""
    queue = MagicMock()
    queue.mark_done = MagicMock()
    queue.mark_blocked = MagicMock()

    run_file = MagicMock()
    run_file.mark_done = MagicMock()
    run_file.mark_blocked = MagicMock()

    event_log = MagicMock()
    event_log.emit = MagicMock()

    spend_log = MagicMock()
    spend_log.record = MagicMock()

    cfg: dict = {
        "concurrency": {"global_max": 4, "per_provider": {}},
        "strict_files_phases": [],
        "budget": {},
    }

    gsem = _Sem(4)
    gsem._count = 1  # one slot acquired (the in-flight task)
    # psem must have a slot for the backend so release() works.
    psem: dict = {backend_name: _Sem(4)}
    psem[backend_name]._count = 1

    return queue, run_file, event_log, spend_log, cfg, gsem, psem


# ---- Test 1: _install_sigint - second signal does not call remove_all --------


def test_install_sigint_does_not_call_remove_all_on_second_signal() -> None:
    """On the second SIGINT (hard kill), wm.remove_all() is NOT called in the handler.

    Sprint F-2: The handler sets drain.hard_kill_next and kills children, but
    the actual worktree cleanup (wm.remove_all) happens later in main() after
    _drain_wait completes, avoiding cleanup-during-drain race conditions.
    """
    drain = _DrainFlag()
    drain.set = True  # simulate first signal already received
    in_flight: dict = {}
    wm = MagicMock(spec=WorktreeManager)

    _install_sigint(drain, in_flight, wm=wm)

    # Trigger the handler as if a second SIGINT arrived.
    handler = signal.getsignal(signal.SIGINT)
    handler(signal.SIGINT, None)

    # The handler must NOT call remove_all (that happens in main after _drain_wait).
    wm.remove_all.assert_not_called()
    # But it should set the hard_kill flag.
    assert drain.hard_kill_next is True


# ---- Test 2: _install_sigint - no wm on second signal does not raise --------


def test_install_sigint_no_wm_does_not_raise_on_second_signal() -> None:
    """Without a WorktreeManager, the second signal handler works without error."""
    drain = _DrainFlag()
    drain.set = True  # first signal already received
    in_flight: dict = {}

    # wm=None (default)
    _install_sigint(drain, in_flight)

    handler = signal.getsignal(signal.SIGINT)
    # Must not raise even though wm is None.
    handler(signal.SIGINT, None)

    # drain flag should indicate hard kill was requested.
    assert drain.hard_kill_next is True


# ---- Test 3: _reap_once calls push then remove on success -------------------


def test_reap_calls_push_and_remove_on_success(tmp_path: Path) -> None:
    """When a task succeeds and worktree_path is set, wm.push then wm.remove are called."""
    worktree_path = tmp_path / ".worktrees" / "T-WT1"
    pid = os.getpid()  # use our own pid so waitpid can reap it via mock

    in_flight = _make_in_flight(pid, worktree_path=worktree_path, success=True)
    queue, run_file, event_log, spend_log, cfg, gsem, psem = _make_mocks(tmp_path)

    wm = MagicMock(spec=WorktreeManager)

    with (
        patch("orchestrator.orch.os.waitpid", side_effect=[(pid, 0), (0, 0)]),
        patch("orchestrator.orch._read_log_safely", return_value=""),
        patch("orchestrator.orch._post_run_checks", return_value=(
            DispatchResult(exit_code=0, success=True), None
        )),
        patch("orchestrator.orch._record_spend"),
        patch("orchestrator.orch.call_task_finish"),
    ):
        _reap_once(
            in_flight, queue, run_file, event_log, spend_log,
            cfg, tmp_path, gsem, psem,
            wm=wm,
        )

    wm.push.assert_called_once_with("T-WT1")
    wm.remove.assert_called_once_with("T-WT1")
    # push before remove (index-based to survive interleaved calls).
    call_names = [c[0] for c in wm.method_calls]
    assert "push" in call_names
    assert "remove" in call_names
    assert call_names.index("push") < call_names.index("remove"), "push must happen before remove"


# ---- Test 4: _reap_once skips push on failure but still removes -------------


def test_reap_skips_push_on_failure(tmp_path: Path) -> None:
    """When a task fails, wm.push must NOT be called. wm.remove must still run."""
    worktree_path = tmp_path / ".worktrees" / "T-WT1"
    pid = os.getpid()

    in_flight = _make_in_flight(pid, worktree_path=worktree_path, success=False)
    queue, run_file, event_log, spend_log, cfg, gsem, psem = _make_mocks(tmp_path)

    wm = MagicMock(spec=WorktreeManager)

    with (
        patch("orchestrator.orch.os.waitpid", side_effect=[(pid, 0), (0, 0)]),
        patch("orchestrator.orch._read_log_safely", return_value=""),
        patch("orchestrator.orch._post_run_checks", return_value=(
            DispatchResult(exit_code=1, success=False, error_message="fail"), None
        )),
        patch("orchestrator.orch._record_spend"),
        patch("orchestrator.orch.call_task_block"),
    ):
        _reap_once(
            in_flight, queue, run_file, event_log, spend_log,
            cfg, tmp_path, gsem, psem,
            retry_queue=None,  # disable retry so task goes straight to blocked
            wm=wm,
        )

    wm.push.assert_not_called()
    wm.remove.assert_called_once_with("T-WT1")


# ---- Test 5: _reap_once logs warning when push fails but does not downgrade -


def test_reap_logs_warning_when_push_fails(tmp_path: Path) -> None:
    """When wm.push raises WorktreeError, the task is NOT downgraded to failed.

    wm.remove() must still be called after a push failure.
    """
    worktree_path = tmp_path / ".worktrees" / "T-WT1"
    pid = os.getpid()

    in_flight = _make_in_flight(pid, worktree_path=worktree_path, success=True)
    queue, run_file, event_log, spend_log, cfg, gsem, psem = _make_mocks(tmp_path)

    wm = MagicMock(spec=WorktreeManager)
    wm.push.side_effect = WorktreeError("T-WT1", ["git", "push"], "remote: error")

    with (
        patch("orchestrator.orch.os.waitpid", side_effect=[(pid, 0), (0, 0)]),
        patch("orchestrator.orch._read_log_safely", return_value=""),
        patch("orchestrator.orch._post_run_checks", return_value=(
            DispatchResult(exit_code=0, success=True), None
        )),
        patch("orchestrator.orch._record_spend"),
        patch("orchestrator.orch.call_task_finish"),
    ):
        # Must not raise — push failures are best-effort.
        _reap_once(
            in_flight, queue, run_file, event_log, spend_log,
            cfg, tmp_path, gsem, psem,
            wm=wm,
        )

    # push was attempted.
    wm.push.assert_called_once_with("T-WT1")
    # remove must still run despite the push error.
    wm.remove.assert_called_once_with("T-WT1")
    # Task was treated as success (mark_done was called).
    queue.mark_done.assert_called_once_with("T-WT1")
