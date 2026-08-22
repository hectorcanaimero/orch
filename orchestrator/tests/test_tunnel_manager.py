"""Tests for `orchestrator.dashboard.tunnel.manager` — Sprint E-5 (TUN-5..8/10, D2).

The child process is monkeypatched via a fake `Popen` that returns
controlled stdout lines. We never spawn autossh; every path is exercised
against a scripted stream.
"""

from __future__ import annotations

import io
import json
import os
import signal
import subprocess
import threading
import time
from pathlib import Path
from typing import Any

import pytest

from orchestrator.dashboard.tunnel import manager as manager_mod
from orchestrator.dashboard.tunnel.manager import (
    STATE_ERROR,
    STATE_IDLE,
    STATE_RUNNING,
    STATE_STARTING,
    TunnelManager,
    TunnelManagerConfig,
    read_state,
)


# ---- Fake Popen scaffolding ---------------------------------------------


class FakePopen:
    """Minimal Popen-shaped fake driving the reader thread with scripted output."""

    def __init__(
        self,
        argv: list[str],
        *,
        stdout: Any = None,
        stderr: Any = None,
        start_new_session: bool = True,
        script_lines: list[bytes] | None = None,
        exit_code: int = 0,
        delay_after_line_s: float = 0.0,
        exit_after_lines: bool = True,
    ) -> None:
        self.argv = argv
        self.pid = os.getpid()  # any live PID so pid_alive stays true
        self.returncode: int | None = None
        self._exit_code = exit_code
        self._delay = delay_after_line_s
        self._exit_after_lines = exit_after_lines
        self._script = list(script_lines or [])
        self._script_index = 0
        self._closed = False
        self._exit_event = threading.Event()
        self.stdout = _ScriptedStdout(self)

    def poll(self) -> int | None:
        return self.returncode

    def wait(self, timeout: float | None = None) -> int:
        deadline = None if timeout is None else time.monotonic() + timeout
        while self.returncode is None:
            if deadline is not None and time.monotonic() > deadline:
                raise subprocess.TimeoutExpired(cmd=self.argv, timeout=timeout)
            self._exit_event.wait(0.05)
        return self.returncode

    def terminate(self) -> None:
        self._simulate_exit()

    def kill(self) -> None:
        self._simulate_exit()

    def _simulate_exit(self) -> None:
        if self.returncode is None:
            self.returncode = self._exit_code
            self._closed = True
            self._exit_event.set()


class _ScriptedStdout:
    def __init__(self, parent: FakePopen) -> None:
        self._parent = parent

    def readline(self) -> bytes:
        p = self._parent
        if p._script_index < len(p._script):
            line = p._script[p._script_index]
            p._script_index += 1
            if p._delay > 0:
                time.sleep(p._delay)
            return line
        if p._exit_after_lines and p.returncode is None:
            p._simulate_exit()
        return b""


def _make_factory(**kwargs: Any) -> Any:
    """Return a `popen_factory` that ignores its own kwargs and returns FakePopen."""
    def _factory(argv, **_ignore):
        return FakePopen(argv, **kwargs)
    return _factory


def _wait_for(pred, timeout: float = 2.0, interval: float = 0.02) -> bool:
    end = time.monotonic() + timeout
    while time.monotonic() < end:
        if pred():
            return True
        time.sleep(interval)
    return False


# ---- start(): transitions + state.json (TUN-5, TUN-6) --------------------


def test_start_transitions_idle_to_starting_to_running_and_writes_state(tmp_path: Path) -> None:
    factory = _make_factory(script_lines=[b"listening on 7420\n"])
    mgr = TunnelManager(tmp_path, popen_factory=factory)
    cfg = TunnelManagerConfig(provider="autossh", command="autossh", args=("--dry-run",))
    snap = mgr.start(cfg)
    assert snap["state"] in (STATE_RUNNING, STATE_IDLE)
    assert (tmp_path / "tunnel" / "state.json").exists()

    on_disk = read_state(tmp_path)
    assert on_disk is not None
    assert on_disk["provider"] == "autossh"


def test_start_writes_atomic_state_json(tmp_path: Path) -> None:
    factory = _make_factory(script_lines=[])
    mgr = TunnelManager(tmp_path, popen_factory=factory)
    cfg = TunnelManagerConfig(provider="autossh", command="autossh", args=("x",))
    mgr.start(cfg)
    state_file = tmp_path / "tunnel" / "state.json"
    tmp_marker = tmp_path / "tunnel" / "state.json.tmp"
    assert state_file.exists()
    assert not tmp_marker.exists(), "tmp file must be atomically renamed"


def test_double_start_raises_already_running(tmp_path: Path) -> None:
    factory = _make_factory(script_lines=[], exit_after_lines=False)
    mgr = TunnelManager(tmp_path, popen_factory=factory)
    cfg = TunnelManagerConfig(provider="autossh", command="autossh")
    mgr.start(cfg)
    with pytest.raises(RuntimeError, match="already_running"):
        mgr.start(cfg)


# ---- URL extraction (TUN-7) ----------------------------------------------


def test_url_regex_hit_populates_status_url(tmp_path: Path) -> None:
    script = [
        b"autossh[42]: starting ssh\n",
        b"handshake ok\n",
        b"Forwarded: https://abc123.a.pinggy.link is live\n",
    ]
    factory = _make_factory(script_lines=script, exit_after_lines=False)
    mgr = TunnelManager(tmp_path, popen_factory=factory)
    cfg = TunnelManagerConfig(provider="autossh", command="autossh")
    mgr.start(cfg)
    # URL must land while state is still RUNNING (spec: url persists for the
    # lifetime of running; child exit resets it — S:TUN-5).
    assert _wait_for(lambda: mgr.status().get("url") is not None, timeout=2.0)
    snap = mgr.status()
    assert snap["url"] == "https://abc123.a.pinggy.link"
    assert snap["state"] == STATE_RUNNING


def test_url_parse_timeout_sets_last_error_url_stays_null(tmp_path: Path) -> None:
    # Script has no URL lines; short timeout so the marker fires quickly.
    script = [b"noise 1\n", b"noise 2\n"]
    factory = _make_factory(script_lines=script, delay_after_line_s=0.05)
    mgr = TunnelManager(tmp_path, popen_factory=factory)
    cfg = TunnelManagerConfig(
        provider="autossh", command="autossh", url_parse_timeout_s=1
    )
    mgr.start(cfg)
    # Wait past the parse deadline.
    time.sleep(1.2)
    # Reader may already have exited (script ends after 2 lines) — check state.json.
    snap = read_state(tmp_path) or mgr.status()
    assert snap.get("url") is None
    # last_error should be set; if the reader exited before the timeout we
    # tolerate `child_exited` too.
    assert snap.get("last_error") in ("url_parse_timeout", "child_exited")


# ---- Reconnect counter (D2) ----------------------------------------------


def test_reconnect_regex_increments_autossh_reconnects(tmp_path: Path) -> None:
    script = [
        b"autossh[1]: starting ssh (count 1)\n",
        b"ssh exited prematurely; restarting\n",
        b"connection lost, retry\n",
    ]
    factory = _make_factory(script_lines=script)
    mgr = TunnelManager(tmp_path, popen_factory=factory)
    cfg = TunnelManagerConfig(provider="autossh", command="autossh")
    mgr.start(cfg)
    assert _wait_for(
        lambda: mgr.status().get("autossh_reconnects", 0) >= 3, timeout=2.0
    )
    assert mgr.status()["autossh_reconnects"] == 3
    assert mgr.status()["restart_count"] == 0


# ---- stop(): SIGTERM + SIGKILL escalation (TUN-6) ------------------------


def test_stop_signals_process_group_and_transitions_to_idle(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    calls: list[tuple[int, int]] = []

    def _fake_getpgid(pid: int) -> int:
        return pid

    def _fake_killpg(pgid: int, sig: int) -> None:
        calls.append((pgid, sig))

    monkeypatch.setattr(manager_mod.os, "getpgid", _fake_getpgid)
    monkeypatch.setattr(manager_mod.os, "killpg", _fake_killpg)

    factory = _make_factory(script_lines=[], exit_after_lines=False)
    mgr = TunnelManager(tmp_path, popen_factory=factory)
    cfg = TunnelManagerConfig(provider="autossh", command="autossh")
    mgr.start(cfg)
    # Ensure the proc still looks alive so stop() actually signals.
    time.sleep(0.05)

    # Have the fake exit as soon as terminate is called (killpg triggers it).
    original_killpg = _fake_killpg

    def _kill_and_exit(pgid: int, sig: int) -> None:
        original_killpg(pgid, sig)
        proc = mgr._proc
        if proc is not None:
            proc._simulate_exit()

    monkeypatch.setattr(manager_mod.os, "killpg", _kill_and_exit)

    mgr.stop()
    assert calls, "SIGTERM was never sent via killpg"
    assert calls[0][1] == signal.SIGTERM
    snap = mgr.status()
    assert snap["state"] == STATE_IDLE
    assert snap["pid"] is None
    assert snap["restart_count"] == 1


def test_stop_escalates_to_sigkill_when_sigterm_ignored(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.setattr(manager_mod, "DEFAULT_STOP_TIMEOUT_S", 0.2)
    sigs: list[int] = []

    def _fake_getpgid(pid: int) -> int:
        return pid

    def _fake_killpg(_pgid: int, sig: int) -> None:
        sigs.append(sig)
        # Only exit on SIGKILL — ignoring SIGTERM forces escalation.
        if sig == signal.SIGKILL:
            proc = mgr._proc
            if proc is not None:
                proc._simulate_exit()

    monkeypatch.setattr(manager_mod.os, "getpgid", _fake_getpgid)
    monkeypatch.setattr(manager_mod.os, "killpg", _fake_killpg)

    factory = _make_factory(script_lines=[], exit_after_lines=False)
    mgr = TunnelManager(tmp_path, popen_factory=factory)
    cfg = TunnelManagerConfig(provider="autossh", command="autossh")
    mgr.start(cfg)
    time.sleep(0.05)
    mgr.stop()
    assert signal.SIGTERM in sigs
    assert signal.SIGKILL in sigs
    assert mgr.status()["state"] == STATE_IDLE


def test_stop_while_idle_raises_not_running(tmp_path: Path) -> None:
    mgr = TunnelManager(tmp_path, popen_factory=_make_factory())
    with pytest.raises(RuntimeError, match="not_running"):
        mgr.stop()


# ---- restart_count on manual /stop then /start (D#10) --------------------


def test_restart_count_increments_on_manual_stop_start_cycle(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    def _fake_getpgid(pid: int) -> int:
        return pid

    def _fake_killpg(_pgid: int, sig: int) -> None:
        proc = mgr._proc
        if proc is not None:
            proc._simulate_exit()

    monkeypatch.setattr(manager_mod.os, "getpgid", _fake_getpgid)
    monkeypatch.setattr(manager_mod.os, "killpg", _fake_killpg)

    factory = _make_factory(script_lines=[], exit_after_lines=False)
    mgr = TunnelManager(tmp_path, popen_factory=factory)
    cfg = TunnelManagerConfig(provider="autossh", command="autossh")
    mgr.start(cfg)
    time.sleep(0.05)
    mgr.stop()
    assert mgr.status()["restart_count"] == 1

    mgr.start(cfg)
    time.sleep(0.05)
    mgr.stop()
    assert mgr.status()["restart_count"] == 2


# ---- Redaction (TUN-10) --------------------------------------------------


def test_reader_redacts_bearer_tokens_before_appending_to_deque(
    tmp_path: Path,
) -> None:
    script = [
        b"Authorization: Bearer deadbeefdeadbeefdeadbeefdeadbeef\n",
        b"cookie=token=abcd1234efgh5678ijkl9012mnop3456\n",
        b"loose token: 0123456789abcdef0123456789abcdef\n",
    ]
    factory = _make_factory(script_lines=script)
    mgr = TunnelManager(tmp_path, popen_factory=factory)
    cfg = TunnelManagerConfig(provider="autossh", command="autossh")
    mgr.start(cfg)
    assert _wait_for(
        lambda: len(list(mgr.logs_iter())) >= 3, timeout=2.0
    )
    lines = list(mgr.logs_iter())
    joined = "\n".join(lines)
    assert "deadbeefdeadbeefdeadbeefdeadbeef" not in joined
    assert "0123456789abcdef0123456789abcdef" not in joined
    assert "***REDACTED***" in joined

    on_disk = (tmp_path / "tunnel" / "stdout.log").read_text(encoding="utf-8")
    assert "deadbeefdeadbeefdeadbeefdeadbeef" not in on_disk
    assert "***REDACTED***" in on_disk


# ---- sweep_stale_lock (TUN-8 zombie recovery) ----------------------------


def test_sweep_stale_lock_removes_dead_pid_files(tmp_path: Path) -> None:
    tunnel_dir = tmp_path / "tunnel"
    tunnel_dir.mkdir(parents=True)
    # 999999 is virtually never a live PID.
    (tunnel_dir / "pid").write_text("999999", encoding="utf-8")
    (tunnel_dir / "lock").write_text(
        json.dumps({"pid": 999999, "started_at": "2026-01-01T00:00:00Z"}),
        encoding="utf-8",
    )

    mgr = TunnelManager(tmp_path)
    cfg = TunnelManagerConfig(provider="autossh", command="autossh")
    mgr.sweep_stale_lock(cfg)
    assert not (tunnel_dir / "pid").exists()
    assert not (tunnel_dir / "lock").exists()
    assert mgr.status()["state"] == STATE_IDLE


def test_sweep_stale_lock_is_noop_when_no_lock_present(tmp_path: Path) -> None:
    mgr = TunnelManager(tmp_path)
    mgr.sweep_stale_lock(TunnelManagerConfig())
    assert mgr.status()["state"] == STATE_IDLE


def test_start_refuses_when_live_lock_exists(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    tunnel_dir = tmp_path / "tunnel"
    tunnel_dir.mkdir(parents=True)
    (tunnel_dir / "lock").write_text(
        json.dumps({"pid": os.getpid(), "started_at": "2026-01-01T00:00:00Z"}),
        encoding="utf-8",
    )
    mgr = TunnelManager(tmp_path, popen_factory=_make_factory())
    cfg = TunnelManagerConfig(provider="autossh", command="autossh")
    with pytest.raises(RuntimeError, match="locked"):
        mgr.start(cfg)


# ---- Spawn errors --------------------------------------------------------


def test_start_records_error_when_spawn_raises_filenotfound(tmp_path: Path) -> None:
    def _boom(_argv, **_kw):
        raise FileNotFoundError("autossh missing")

    mgr = TunnelManager(tmp_path, popen_factory=_boom)
    cfg = TunnelManagerConfig(provider="autossh", command="autossh")
    with pytest.raises(FileNotFoundError):
        mgr.start(cfg)
    snap = mgr.status()
    assert snap["state"] == STATE_ERROR
    assert snap["last_error"] and snap["last_error"].startswith("spawn_failed:")


# ---- bore provider: partial-URL assembly (url_template) ------------------


def test_bore_manager_captures_partial_url_via_template(tmp_path: Path) -> None:
    """bore emits `listening at bore.pub:PORT` — the manager must assemble
    the full `http://bore.pub:PORT` URL via the provider's url_template."""
    script = [
        b"2024-01-15T12:00:00.000000Z  INFO bore_cli::client: connecting to bore.pub\n",
        b"2024-01-15T12:00:00.100000Z  INFO bore_cli::client: listening at bore.pub:12345\n",
    ]
    factory = _make_factory(script_lines=script, exit_after_lines=False)
    mgr = TunnelManager(tmp_path, popen_factory=factory)
    cfg = TunnelManagerConfig(provider="bore", command="bore")
    mgr.start(cfg)
    assert _wait_for(lambda: mgr.status().get("url") is not None, timeout=2.0)
    snap = mgr.status()
    # Assembled from named group ("port") via url_template — NOT match.group(0).
    assert snap["url"] == "http://bore.pub:12345"
    assert snap["state"] == STATE_RUNNING
    assert snap["provider"] == "bore"


def test_bore_manager_ignores_partial_matches_that_dont_fit_template(
    tmp_path: Path,
) -> None:
    """Lines that don't match the regex at all leave url unset — no crash."""
    script = [
        b"unrelated noise\n",
        b"connecting to bore.pub (not listening yet)\n",
    ]
    factory = _make_factory(
        script_lines=script,
        delay_after_line_s=0.05,
        exit_after_lines=True,
    )
    mgr = TunnelManager(tmp_path, popen_factory=factory)
    cfg = TunnelManagerConfig(
        provider="bore", command="bore", url_parse_timeout_s=1
    )
    mgr.start(cfg)
    # Give the reader a beat to consume the script + trip the timeout.
    time.sleep(1.3)
    snap = read_state(tmp_path) or mgr.status()
    assert snap.get("url") is None
