"""Advisory file-locking helpers (extracted verbatim from `state.py`).

Two lock scopes exist:
    - Global orchestrator flock (`state/.lock`) — one live orchestrator per
      project (FR-STATE-4).
    - Per-task locks (`state/task-locks/<task_id>.lock`) — opt-in with
      `--task-locks` to allow multiple orchestrators as long as they target
      disjoint task sets.

Both use POSIX `fcntl.LOCK_EX | LOCK_NB` so a second acquirer fails fast
instead of blocking.
"""

from __future__ import annotations

import fcntl
import logging
import os
from pathlib import Path
from typing import IO

log = logging.getLogger(__name__)


class FlockContentionError(Exception):
    """Another orchestrator holds `state/.lock` (FR-STATE-4 / AS-09).

    Callers should map to exit-code 3. `holder_run_id` may be `None` if the
    lock exists but the holder file couldn't be identified.
    """

    def __init__(self, path: Path, holder_run_id: str | None = None):
        self.path = path
        self.holder_run_id = holder_run_id
        msg = (
            f"another orchestrator holds the flock at {path}"
            + (f" (run-id={holder_run_id})" if holder_run_id else "")
            + ". wait or --resume <run-id>."
        )
        super().__init__(msg)


def acquire_flock(path: str | Path) -> IO[bytes]:
    """Acquire an advisory exclusive flock on `path`.

    Uses `LOCK_EX | LOCK_NB` — non-blocking, exclusive. On contention the
    caller gets a `FlockContentionError` immediately (AS-09 says "second
    exits 3 within 1 s").

    The returned file handle MUST be kept alive for the lifetime of the run;
    closing it releases the flock. Callers typically stash it on `RunFile` or
    a top-level variable.
    """
    lock_path = Path(path)
    lock_path.parent.mkdir(parents=True, exist_ok=True)
    # Open in append+binary so we can write the holder run-id later without
    # truncating any pre-existing content another orchestrator might have
    # left. `os.O_CLOEXEC` prevents accidental leak into spawned CLIs.
    fd = open(lock_path, "ab+")
    try:
        fcntl.flock(fd.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        # Best-effort: try to read the run-id the holder wrote.
        holder: str | None = None
        try:
            fd.seek(0)
            holder = fd.read().decode("utf-8", errors="replace").strip() or None
        except Exception:  # noqa: BLE001 — never let diagnostics mask the real error
            holder = None
        fd.close()
        raise FlockContentionError(lock_path, holder_run_id=holder) from None
    return fd


def try_acquire_task_lock(task_id: str, state_dir: str | Path) -> IO[bytes] | None:
    """Non-blocking exclusive lock scoped to a single task.

    Allows multiple concurrent orch instances as long as each targets a
    distinct set of task ids (opt-in via `--task-locks`). Returns the open
    fd on success (caller MUST keep it alive and close on release), or None
    if another orch already holds this task's lock.

    Lock files live under `<state_dir>/task-locks/<task_id>.lock` and are
    never garbage-collected (advisory flock is released on process exit).
    """
    locks_dir = Path(state_dir) / "task-locks"
    locks_dir.mkdir(parents=True, exist_ok=True)
    lock_path = locks_dir / f"{task_id}.lock"
    fd = open(lock_path, "ab+")
    try:
        fcntl.flock(fd.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        fd.close()
        return None
    try:
        fd.seek(0)
        fd.truncate(0)
        fd.write(f"pid={os.getpid()}\n".encode())
        fd.flush()
    except Exception:  # noqa: BLE001
        pass
    return fd


def release_task_lock(fd: IO[bytes] | None) -> None:
    """Release a per-task lock acquired via `try_acquire_task_lock`."""
    if fd is None:
        return
    try:
        fd.close()
    except Exception:  # noqa: BLE001
        pass


def write_lock_holder(fd: IO[bytes], run_id: str, pid: int) -> None:
    """Write `run-id=<uuid>, pid=<pid>` inside the lock file for diagnostics.

    The bytes are meaningful only for the error message another orchestrator
    would print on contention; they never gate any behavior.
    """
    try:
        fd.seek(0)
        fd.truncate(0)
        fd.write(f"run-id={run_id}, pid={pid}\n".encode("utf-8"))
        fd.flush()
        os.fsync(fd.fileno())
    except Exception as exc:  # noqa: BLE001
        log.warning("could not write lock holder metadata: %s", exc)
