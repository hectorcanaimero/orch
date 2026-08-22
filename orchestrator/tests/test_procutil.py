"""Tests for `orchestrator.procutil.pid_alive` (Batch 1 of Sprint E-5, D1)."""

from __future__ import annotations

import os
import signal
import subprocess

import pytest

from orchestrator import procutil
from orchestrator.procutil import pid_alive


def test_pid_alive_true_for_current_process() -> None:
    assert pid_alive(os.getpid()) is True


def test_pid_alive_false_for_dead_pid() -> None:
    # Spawn a short-lived child, kill it, reap it, then probe the PID.
    proc = subprocess.Popen(["sleep", "30"])
    try:
        os.kill(proc.pid, signal.SIGKILL)
    finally:
        proc.wait(timeout=5)
    assert pid_alive(proc.pid) is False


@pytest.mark.parametrize("bad_pid", [0, -1, -12345])
def test_pid_alive_false_for_non_positive_pid(bad_pid: int) -> None:
    assert pid_alive(bad_pid) is False


def test_pid_alive_true_on_permission_error(monkeypatch: pytest.MonkeyPatch) -> None:
    def _fake_kill(_pid: int, _sig: int) -> None:
        raise PermissionError("not owner")

    monkeypatch.setattr(procutil.os, "kill", _fake_kill)
    # A PID the OS knows about but we can't signal → treated as alive.
    assert pid_alive(42) is True


def test_pid_alive_false_on_generic_os_error(monkeypatch: pytest.MonkeyPatch) -> None:
    def _fake_kill(_pid: int, _sig: int) -> None:
        raise OSError("unexpected")

    monkeypatch.setattr(procutil.os, "kill", _fake_kill)
    assert pid_alive(42) is False
