"""Long-lived tunnel subprocess supervisor (Sprint E-5, TUN-5..8/10).

The manager owns exactly one child process at a time (autossh in v1),
tails its stdout on a daemon thread, and mirrors the current state to
`state/tunnel/state.json` atomically. All external surface is method-based
on `TunnelManager` — HTTP routes in `server.py` delegate here.

Concurrency: a single `threading.RLock` guards the in-memory state dict.
Reader thread + Popen wait + HTTP threads all touch state through
`_with_state` context. Atomic writes use `tmp + os.replace` (matches
Sprint E-4 `arch.py`).

Signals: children are spawned with `start_new_session=True` so SIGTERM to
autossh does NOT propagate to uvicorn. Manual `stop()` sends SIGTERM via
`os.killpg`; if the child hasn't exited within 5 s we escalate to SIGKILL
(same pattern as Sprint A `_killpg_or_pid`).

Reconciler: on init `sweep_stale_lock()` inspects `state/tunnel/pid` — if
the PID is alive AND its `comm` basename matches the allowlisted binary,
the manager adopts it as running. Otherwise both `pid` and `lock` are
removed and state resets to idle (TUN-8 zombie recovery).

Redaction (TUN-10): before any line lands in the deque or `stdout.log`,
we strip `Bearer <hex>`, `token=<hex>`, and bare 32+ hex tokens.
"""

from __future__ import annotations

import json
import os
import re
import signal
import subprocess
import sys
import threading
import time
from collections import deque
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Callable, Iterator

from orchestrator.dashboard.tunnel.providers import (
    ProviderSpec,
    compile_reconnect_regex,
    compile_url_regex,
    resolve,
)
from orchestrator.procutil import pid_alive


# ---- Constants ------------------------------------------------------------

LOCK_FILENAME = "lock"
PID_FILENAME = "pid"
STATE_FILENAME = "state.json"
STDOUT_LOG_FILENAME = "stdout.log"

DEFAULT_STOP_TIMEOUT_S = 5.0
DEFAULT_LOG_BUFFER_LINES = 500

STATE_IDLE = "idle"
STATE_STARTING = "starting"
STATE_RUNNING = "running"
STATE_STOPPING = "stopping"
STATE_ERROR = "error"

# Redaction patterns (TUN-10). Order matters: header/query forms first so
# their explicit prefixes survive; the loose 32+ hex fallback catches raw
# tokens that appear unattached.
_REDACT_PATTERNS: tuple[re.Pattern[str], ...] = (
    re.compile(r"(?i)(Bearer\s+)[A-Za-z0-9._\-]{8,}"),
    re.compile(r"(?i)(token=)[A-Za-z0-9._\-]{8,}"),
    re.compile(r"(?i)(Authorization:\s*Bearer\s+)[A-Za-z0-9._\-]{8,}"),
    re.compile(r"\b[A-Fa-f0-9]{32,}\b"),
)
_REDACT_PLACEHOLDER = "***REDACTED***"


# ---- Config surface -------------------------------------------------------


@dataclass(frozen=True, slots=True)
class TunnelManagerConfig:
    """Runtime-facing subset of `DashboardConfig.tunnel` the manager needs.

    We take our own dataclass so tests can construct one without dragging
    the whole `DashboardConfig` graph in.
    """

    provider: str = "autossh"
    command: str = "autossh"
    args: tuple[str, ...] = ()
    url_regex: str | None = None
    url_parse_timeout_s: int = 30
    stop_timeout_s: float = DEFAULT_STOP_TIMEOUT_S


# ---- Manager --------------------------------------------------------------


def _utc_iso(dt: datetime | None = None) -> str:
    return (dt or datetime.now(timezone.utc)).strftime("%Y-%m-%dT%H:%M:%SZ")


def _redact(line: str) -> str:
    """Replace known token shapes with a fixed placeholder (TUN-10)."""
    out = line
    for i, pat in enumerate(_REDACT_PATTERNS):
        if i < 3:
            out = pat.sub(lambda m: m.group(1) + _REDACT_PLACEHOLDER, out)
        else:
            out = pat.sub(_REDACT_PLACEHOLDER, out)
    return out


def _atomic_write_json(path: Path, data: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_suffix(path.suffix + ".tmp")
    tmp.write_text(json.dumps(data, indent=2), encoding="utf-8")
    os.replace(tmp, path)


def _pid_comm(pid: int) -> str | None:
    """Portable `ps -p <pid> -o comm=` — returns the binary basename or None.

    macOS + Linux both accept this form (BSD ps supports `-o comm=`). We
    avoid `/proc/<pid>/exe` because macOS has no procfs. Returns None on
    lookup failure so callers treat unknown as "not matching".
    """
    try:
        proc = subprocess.run(  # noqa: S603 — argv locally constructed
            ["ps", "-p", str(pid), "-o", "comm="],
            capture_output=True,
            text=True,
            timeout=2,
            check=False,
        )
    except (subprocess.SubprocessError, FileNotFoundError, OSError):
        return None
    if proc.returncode != 0:
        return None
    out = (proc.stdout or "").strip()
    if not out:
        return None
    return os.path.basename(out.splitlines()[0].strip())


class TunnelManager:
    """Single-child supervisor for the tunnel provider process.

    Public surface: `start(cfg)`, `stop()`, `status()`, `logs_iter()`,
    `sweep_stale_lock(cfg)`. Everything else is private; tests exercise
    the class via `start(cfg)` + a monkeypatched `Popen` factory.
    """

    def __init__(
        self,
        state_dir: Path,
        *,
        popen_factory: Callable[..., subprocess.Popen[bytes]] | None = None,
        log_buffer_lines: int = DEFAULT_LOG_BUFFER_LINES,
    ) -> None:
        self._state_dir = Path(state_dir)
        self._tunnel_dir = self._state_dir / "tunnel"
        self._popen_factory = popen_factory or subprocess.Popen
        self._lock = threading.RLock()
        self._proc: subprocess.Popen[bytes] | None = None
        self._reader_thread: threading.Thread | None = None
        self._logs: deque[str] = deque(maxlen=log_buffer_lines)
        self._logs_lock = threading.Lock()
        self._state: dict[str, Any] = self._blank_state()

    # ---- state helpers ----------------------------------------------------

    @staticmethod
    def _blank_state() -> dict[str, Any]:
        return {
            "state": STATE_IDLE,
            "provider": None,
            "pid": None,
            "url": None,
            "started_at": None,
            "url_at": None,
            "restart_count": 0,
            "autossh_reconnects": 0,
            "last_error": None,
            "last_exit_code": None,
            "phase": STATE_IDLE,
        }

    def _write_state(self) -> None:
        _atomic_write_json(self._tunnel_dir / STATE_FILENAME, self._state)

    def _lock_path(self) -> Path:
        return self._tunnel_dir / LOCK_FILENAME

    def _pid_path(self) -> Path:
        return self._tunnel_dir / PID_FILENAME

    def _stdout_log_path(self) -> Path:
        return self._tunnel_dir / STDOUT_LOG_FILENAME

    # ---- public API -------------------------------------------------------

    def status(self) -> dict[str, Any]:
        with self._lock:
            return dict(self._state)

    def logs_iter(self) -> Iterator[str]:
        """Snapshot the current tail — real live-tail lives in the SSE route."""
        with self._logs_lock:
            return iter(list(self._logs))

    def start(self, cfg: TunnelManagerConfig) -> dict[str, Any]:
        """Spawn the tunnel subprocess. Returns the resulting status snapshot.

        Raises `RuntimeError("already_running")` when state != idle and
        `RuntimeError("locked")` when the on-disk lock is held by a live
        peer. The HTTP layer maps these to 409.
        """
        with self._lock:
            if self._state["state"] != STATE_IDLE:
                raise RuntimeError("already_running")
            self._acquire_lock_or_raise(cfg.provider)
            spec = resolve(cfg.provider)
            argv = [cfg.command] + list(cfg.args or spec.default_args)
            self._state.update(
                state=STATE_STARTING,
                provider=cfg.provider,
                phase=STATE_STARTING,
                started_at=_utc_iso(),
                url=None,
                url_at=None,
                last_error=None,
                last_exit_code=None,
            )
            self._write_state()

            try:
                proc = self._spawn(argv)
            except (FileNotFoundError, OSError) as exc:
                self._state.update(
                    state=STATE_ERROR,
                    phase=STATE_ERROR,
                    last_error=f"spawn_failed:{type(exc).__name__}",
                )
                self._write_state()
                self._release_lock_files()
                raise

            self._proc = proc
            self._state.update(
                state=STATE_RUNNING,
                phase=STATE_RUNNING,
                pid=proc.pid,
            )
            self._write_state()
            self._write_pid_file(proc.pid)
            self._start_reader_thread(spec, cfg)
            return dict(self._state)

    def stop(self) -> dict[str, Any]:
        """SIGTERM the process group; escalate to SIGKILL after 5 s."""
        with self._lock:
            if self._state["state"] == STATE_IDLE:
                raise RuntimeError("not_running")
            proc = self._proc
            self._state.update(state=STATE_STOPPING, phase=STATE_STOPPING)
            self._write_state()

        stop_timeout = DEFAULT_STOP_TIMEOUT_S
        if proc is not None and proc.poll() is None:
            self._signal_pgroup(proc.pid, signal.SIGTERM)
            try:
                proc.wait(timeout=stop_timeout)
            except subprocess.TimeoutExpired:
                self._signal_pgroup(proc.pid, signal.SIGKILL)
                try:
                    proc.wait(timeout=stop_timeout)
                except subprocess.TimeoutExpired:
                    pass

        with self._lock:
            exit_code = proc.returncode if proc is not None else None
            self._state.update(
                state=STATE_IDLE,
                phase=STATE_IDLE,
                pid=None,
                url=None,
                url_at=None,
                started_at=None,
                restart_count=self._state.get("restart_count", 0) + 1,
                last_exit_code=exit_code,
            )
            self._write_state()
            self._proc = None

        self._release_lock_files()
        return self.status()

    def sweep_stale_lock(self, cfg: TunnelManagerConfig | None = None) -> None:
        """Boot-time reconciler (TUN-8).

        If `state/tunnel/pid` names a PID that is still alive AND whose
        `comm` matches the allowlisted binary, we adopt it. Otherwise both
        lock and pid files are removed and state stays idle.
        """
        with self._lock:
            pid_file = self._pid_path()
            lock_file = self._lock_path()
            if not pid_file.exists() and not lock_file.exists():
                return
            adopted = False
            expected_command = cfg.command if cfg is not None else None
            try:
                raw = pid_file.read_text(encoding="utf-8").strip()
                pid = int(raw)
            except (OSError, ValueError):
                pid = 0
            if pid > 0 and pid_alive(pid):
                if expected_command is None:
                    adopted = True
                else:
                    comm = _pid_comm(pid)
                    adopted = comm is not None and comm == os.path.basename(
                        expected_command
                    )
            if adopted and cfg is not None:
                self._state.update(
                    state=STATE_RUNNING,
                    phase=STATE_RUNNING,
                    provider=cfg.provider,
                    pid=pid,
                    started_at=_utc_iso(),
                )
                self._write_state()
                return
            self._release_lock_files()

    # ---- internals --------------------------------------------------------

    def _acquire_lock_or_raise(self, provider: str) -> None:
        lock_path = self._lock_path()
        if lock_path.exists():
            try:
                data = json.loads(lock_path.read_text(encoding="utf-8"))
            except (OSError, json.JSONDecodeError):
                data = {}
            pid = int(data.get("pid") or 0) if isinstance(data, dict) else 0
            if pid > 0 and pid_alive(pid):
                raise RuntimeError("locked")
            # Stale — sweep and continue.
            try:
                lock_path.unlink()
            except OSError:
                pass
        self._tunnel_dir.mkdir(parents=True, exist_ok=True)
        payload = {
            "pid": os.getpid(),
            "started_at": _utc_iso(),
            "provider": provider,
            "phase": STATE_STARTING,
        }
        _atomic_write_json(lock_path, payload)

    def _write_pid_file(self, pid: int) -> None:
        self._tunnel_dir.mkdir(parents=True, exist_ok=True)
        tmp = self._pid_path().with_suffix(".tmp")
        tmp.write_text(str(pid), encoding="utf-8")
        os.replace(tmp, self._pid_path())

    def _release_lock_files(self) -> None:
        for path in (self._lock_path(), self._pid_path()):
            try:
                path.unlink()
            except FileNotFoundError:
                pass
            except OSError:
                pass

    def _spawn(self, argv: list[str]) -> subprocess.Popen[bytes]:
        # start_new_session=True → dedicated process group so SIGTERM to
        # autossh doesn't propagate to the dashboard.
        return self._popen_factory(
            argv,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            start_new_session=True,
        )

    def _signal_pgroup(self, pid: int, sig: int) -> None:
        # Mirror `orch._killpg_or_pid` — pgid signal catches child ssh spawned
        # by autossh; fall back to plain kill if the group is gone.
        try:
            pgid = os.getpgid(pid)
        except (ProcessLookupError, PermissionError, OSError):
            try:
                os.kill(pid, sig)
            except (ProcessLookupError, PermissionError, OSError):
                pass
            return
        try:
            os.killpg(pgid, sig)
        except (ProcessLookupError, PermissionError, OSError):
            try:
                os.kill(pid, sig)
            except (ProcessLookupError, PermissionError, OSError):
                pass

    def _start_reader_thread(
        self, spec: ProviderSpec, cfg: TunnelManagerConfig
    ) -> None:
        url_pattern = cfg.url_regex or spec.url_regex
        url_re = compile_url_regex(url_pattern)
        reconnect_re = compile_reconnect_regex(spec)
        deadline = time.monotonic() + max(1, cfg.url_parse_timeout_s)
        proc = self._proc
        if proc is None or proc.stdout is None:
            return

        def _run() -> None:
            url_seen = False
            timeout_flagged = False
            try:
                for raw_line in iter(proc.stdout.readline, b""):
                    try:
                        line = raw_line.decode("utf-8", errors="replace").rstrip("\n")
                    except Exception:  # noqa: BLE001
                        continue
                    line = _redact(line)
                    with self._logs_lock:
                        self._logs.append(line)
                    self._append_stdout_log(line)
                    if not url_seen:
                        m = url_re.search(line)
                        if m:
                            if spec.url_template:
                                # Named-group interpolation. bore-style:
                                # partial URL in stdout (host:port) → assemble.
                                captured = spec.url_template.format_map(
                                    m.groupdict()
                                )
                            else:
                                # Whole-match. autossh-style: URL emitted verbatim.
                                captured = m.group(0)
                            with self._lock:
                                self._state["url"] = captured
                                self._state["url_at"] = _utc_iso()
                                self._write_state()
                            url_seen = True
                    if reconnect_re.search(line):
                        with self._lock:
                            self._state["autossh_reconnects"] = (
                                int(self._state.get("autossh_reconnects", 0)) + 1
                            )
                            self._write_state()
                    if (
                        not url_seen
                        and not timeout_flagged
                        and time.monotonic() > deadline
                    ):
                        with self._lock:
                            self._state["last_error"] = "url_parse_timeout"
                            self._write_state()
                        # Diagnostic marker only — keep tailing so a late URL
                        # still populates. Flag guards against re-writing.
                        timeout_flagged = True
            except Exception as exc:  # noqa: BLE001
                with self._lock:
                    self._state["last_error"] = f"reader_error:{type(exc).__name__}"
                    self._write_state()
            finally:
                self._on_child_exit()

        thread = threading.Thread(target=_run, daemon=True, name="tunnel-reader")
        self._reader_thread = thread
        thread.start()

    def _append_stdout_log(self, line: str) -> None:
        try:
            self._tunnel_dir.mkdir(parents=True, exist_ok=True)
            with self._stdout_log_path().open("a", encoding="utf-8") as fh:
                fh.write(line + "\n")
        except OSError as exc:
            print(f"[tunnel] stdout.log write failed: {exc}", file=sys.stderr)

    def _on_child_exit(self) -> None:
        proc = self._proc
        exit_code = None
        if proc is not None:
            try:
                exit_code = proc.wait(timeout=1)
            except subprocess.TimeoutExpired:
                exit_code = None
        with self._lock:
            current = self._state.get("state")
            # STOPPING → clean manual stop. IDLE → stop() already finalized;
            # this callback is racing after the fact. In both cases we stay
            # idle. Anything else means the child died on its own → error.
            clean_exit = current in (STATE_STOPPING, STATE_IDLE)
            self._state.update(
                state=STATE_IDLE if clean_exit else STATE_ERROR,
                phase=STATE_IDLE if clean_exit else STATE_ERROR,
                pid=None,
                url=None,
                url_at=None,
                started_at=None,
                last_exit_code=exit_code,
            )
            if not clean_exit and self._state.get("last_error") is None:
                self._state["last_error"] = "child_exited"
            self._write_state()
            self._proc = None
        self._release_lock_files()


# ---- Convenience read function ------------------------------------------


def read_state(state_dir: Path) -> dict[str, Any] | None:
    """Read `state/tunnel/state.json` — returns None when absent/corrupt.

    Public helper for HTTP handlers that just want to snapshot state without
    holding a manager reference (e.g. tests).
    """
    path = Path(state_dir) / "tunnel" / STATE_FILENAME
    if not path.exists():
        return None
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return None
    if not isinstance(data, dict):
        return None
    return data
