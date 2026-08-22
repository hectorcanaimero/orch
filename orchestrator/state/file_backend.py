"""File-based `StateBackend` implementation — wraps today's JSON/JSONL logic.

All the code in this module was moved verbatim from the original `state.py`
during the Sprint B refactor. Behavior is byte-identical; only the module
boundary changed. `FileBackend` at the bottom is a thin façade that maps the
`StateBackend` Protocol onto the free functions and classes above.
"""

from __future__ import annotations

import json
import logging
import os
from collections.abc import Iterator
from dataclasses import asdict, dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Iterable

from ..models import Dispatch, EventEntry, RunState, SpendEntry, Status, Task
from .shell import (
    _ensure_v2_cwd,
    call_task_block as _call_task_block_direct,
    call_task_finish as _call_task_finish_direct,
    call_task_start as _call_task_start_direct,
)


def _lookup_shell(name: str, default):
    """Late-bind a callable through `orchestrator.state`.

    Tests do `patch("orchestrator.state.call_task_finish", ...)`; the patch
    only touches the `state` package namespace. This helper resolves the
    symbol on the package at call-time so those patches actually reach
    `reconcile_run` inside this file.
    """
    import sys

    pkg = sys.modules.get("orchestrator.state")
    if pkg is None:
        return default
    return getattr(pkg, name, default)

log = logging.getLogger(__name__)


# ---- Constants ----------------------------------------------------------


# Locked per FR-STATE-7 / NFR-OBS-1. Extending this requires a spec update.
# The list is a tuple so it's immutable at import time.
EVENT_TYPES: tuple[str, ...] = (
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


# ---- Utilities ----------------------------------------------------------


def _utc_now_iso() -> str:
    """ISO-8601 UTC timestamp with 'Z' suffix (matches shell scripts)."""
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def load_tasks(path: str | Path) -> list[Task]:
    """Read `tasks.json` (read-only) and return a list of `Task` instances.

    Handles both shapes of `tasks.json`:
        {"tasks": [...], "meta": {...}}   (real project shape)
        [...]                              (bare array — legacy / fixture)

    This is the canonical loader (per design §5 which puts `tasks.json` reading
    in `state.py`). `task_queue.load_tasks` is a re-export for compatibility.
    """
    with open(path, encoding="utf-8") as fh:
        raw = json.load(fh)

    if isinstance(raw, dict):
        rows = raw.get("tasks", [])
    elif isinstance(raw, list):
        rows = raw
    else:
        raise ValueError(
            f"{path}: expected top-level dict or list, got {type(raw).__name__}"
        )

    return [Task.from_json(row) for row in rows]


# ---- Atomic write helper ------------------------------------------------


def _atomic_write(path: Path, payload: bytes) -> None:
    """Write bytes to `path` atomically via tmp + rename.

    Guarantees: readers never see a half-written file. Uses `os.replace` so a
    crash in the middle leaves either the old file or the new one, never a
    truncated hybrid (FR-STATE-3).
    """
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_suffix(path.suffix + ".tmp")
    with open(tmp, "wb") as fh:
        fh.write(payload)
        fh.flush()
        os.fsync(fh.fileno())
    os.replace(tmp, path)


# ---- Runs index ---------------------------------------------------------


def rebuild_index(state_dir: Path) -> None:
    """Rewrite `<state_dir>/index.json` from all `run-*.json` in `state_dir`.

    Called after every mutation of a run file or event log so the static
    dashboard SPA can enumerate runs without directory listing. Malformed or
    empty run files are skipped silently — index building must never crash
    the orchestrator.

    Written atomically via tmp + `os.replace` so partial reads are impossible.
    """
    try:
        state_dir = Path(state_dir)
        if not state_dir.exists():
            return
        runs: list[dict[str, Any]] = []
        for run_path in state_dir.glob("run-*.json"):
            # Skip our own tmp files from _atomic_write.
            if run_path.suffix != ".json":
                continue
            try:
                with open(run_path, encoding="utf-8") as fh:
                    raw = json.load(fh)
                run_id = raw["run_id"]
                started_at = raw["started_at"]
            except (OSError, json.JSONDecodeError, KeyError, TypeError):
                # Malformed / empty / mid-write — skip silently.
                continue

            try:
                mtime = run_path.stat().st_mtime
                updated_at = datetime.fromtimestamp(mtime, timezone.utc).strftime(
                    "%Y-%m-%dT%H:%M:%SZ"
                )
            except OSError:
                updated_at = started_at

            in_flight = raw.get("in_flight", {}) or {}
            completed = raw.get("completed", []) or []
            blocked = raw.get("blocked", []) or []
            deferred = raw.get("deferred", []) or []
            events_file = f"events-{run_id}.jsonl"

            runs.append({
                "run_id": run_id,
                "started_at": started_at,
                "updated_at": updated_at,
                "mode": raw.get("mode", "auto"),
                "status": "live" if in_flight else "done",
                "in_flight_count": len(in_flight),
                "completed_count": len(completed),
                "blocked_count": len(blocked),
                "deferred_count": len(deferred),
                "run_file": run_path.name,
                "events_file": events_file,
            })

        # Newest first by started_at.
        runs.sort(key=lambda r: r["started_at"], reverse=True)
        payload = json.dumps(
            {"generated_at": _utc_now_iso(), "runs": runs},
            indent=2,
        ).encode("utf-8")
        _atomic_write(state_dir / "index.json", payload)
    except Exception as exc:  # noqa: BLE001 — never crash the orchestrator on index build
        log.warning("rebuild_index failed: %s", exc)


# ---- Orphan reconciliation (dead in-flight PIDs) ------------------------


def reconcile_in_flight(state_dir: Path, project_id: str | None = None) -> dict:
    """Scan every `run-*.json` in `state_dir` for orphaned in-flight dispatches.

    A dispatch is "orphaned" when its recorded PID is no longer a live process
    (crashed CLI, killed shell, closed terminal session, etc.). We probe with
    `os.kill(pid, 0)` — the classic POSIX aliveness check:

        - success               → alive
        - ProcessLookupError    → dead → reconcile
        - PermissionError       → alive (belongs to another uid, but exists)

    For each dead entry we:
        1. Remove it from `in_flight`.
        2. Append its `task_id` to `blocked` (idempotent).
        3. Append a synthetic `reconciled` line to the run's `events-*.jsonl`
           so the dashboard and audit trail see the transition.
        4. Persist the mutated run file via `_atomic_write`.

    After all mutations we call `rebuild_index(state_dir)` once so the
    dashboard's `index.json` reflects the new `in_flight_count` / status.

    Returns a small dict for observability:
        {"reconciled": [{"run_id", "task_id", "pid"}, ...], "checked": N}

    Never raises past its own boundary — a broken reconciler must not crash
    the orchestrator loop it runs inside.

    Fase 2: `project_id` opcional — se propaga al `EventLog` sintético para
    que las líneas `reconciled` traigan el tenant correcto. None es válido
    (retrocompat con call sites que aún no lo pasan).
    """
    result: dict[str, Any] = {"reconciled": [], "checked": 0}
    try:
        state_dir = Path(state_dir)
        if not state_dir.exists():
            return result

        mutated = False
        for run_path in state_dir.glob("run-*.json"):
            if run_path.suffix != ".json":
                # Skip `.tmp` files left behind by _atomic_write mid-write.
                continue
            try:
                with open(run_path, encoding="utf-8") as fh:
                    raw = json.load(fh)
            except (OSError, json.JSONDecodeError):
                # Malformed / empty / mid-write — skip silently.
                continue

            run_id = raw.get("run_id")
            if not run_id:
                continue

            in_flight: dict[str, dict[str, Any]] = raw.get("in_flight", {}) or {}
            if not in_flight:
                continue

            blocked: list[str] = list(raw.get("blocked", []) or [])
            run_mutated = False

            for task_id, dispatch in list(in_flight.items()):
                result["checked"] += 1
                # Defensive: entries without a valid PID field can't be probed.
                # Skip silently — never crash.
                pid = dispatch.get("pid") if isinstance(dispatch, dict) else None
                if not isinstance(pid, int) or pid <= 0:
                    continue

                try:
                    os.kill(pid, 0)
                    alive = True
                except ProcessLookupError:
                    alive = False
                except PermissionError:
                    # PID exists but is owned by another user — treat as alive.
                    alive = True
                except OSError as exc:
                    # Any other kernel-level error: log and skip (don't reap).
                    log.warning(
                        "reconcile_in_flight: os.kill(%d, 0) errored: %s",
                        pid,
                        exc,
                    )
                    continue

                if alive:
                    continue

                # ---- orphan: reconcile --------------------------------------
                backend = (
                    dispatch.get("backend", "") if isinstance(dispatch, dict) else ""
                )
                del in_flight[task_id]
                if task_id not in blocked:
                    blocked.append(task_id)

                events_path = state_dir / f"events-{run_id}.jsonl"
                try:
                    EventLog(events_path, project_id=project_id).emit(
                        event_type="reconciled",
                        task_id=task_id,
                        backend=backend,
                        reason="orphaned_process",
                        original_pid=pid,
                    )
                except OSError as exc:
                    # Event append failing must not block the state mutation —
                    # the dashboard will still see the run file reconciled.
                    log.warning(
                        "reconcile_in_flight: could not append event to %s: %s",
                        events_path,
                        exc,
                    )

                run_mutated = True
                mutated = True
                result["reconciled"].append(
                    {"run_id": run_id, "task_id": task_id, "pid": pid}
                )
                log.info(
                    "reconcile_in_flight: reaped orphan task=%s pid=%d run=%s",
                    task_id,
                    pid,
                    run_id,
                )

            if run_mutated:
                raw["in_flight"] = in_flight
                raw["blocked"] = blocked
                try:
                    payload = json.dumps(raw, indent=2).encode("utf-8")
                    _atomic_write(run_path, payload)
                except OSError as exc:
                    log.warning(
                        "reconcile_in_flight: failed to persist %s: %s",
                        run_path,
                        exc,
                    )

        if mutated:
            rebuild_index(state_dir)
        return result
    except Exception as exc:  # noqa: BLE001 — must never crash the orchestrator
        log.warning("reconcile_in_flight failed: %s", exc)
        return result


# ---- RunFile ------------------------------------------------------------


@dataclass
class ReconcileReport:
    """Summary returned by `reconcile_run` for the CLI to log/print.

    Adopted PIDs stay in `RunState.in_flight`; reverted tasks were shell-called
    back to `todo`/`blocked` via `scripts/task-*.sh` and are dropped from
    `in_flight`. Errors are best-effort strings for the human — the reconciler
    never raises past its own boundary.
    """

    adopted: list[str] = field(default_factory=list)
    reverted: list[str] = field(default_factory=list)
    errors: list[str] = field(default_factory=list)


class RunFile:
    """Persistent per-run state at `state/run-<uuid>.json`.

    The RunFile object holds an in-memory `RunState` and mirrors every
    transition to disk via `save()` (atomic tmp+rename). `add_dispatch` /
    `remove_dispatch` / `mark_done` / `mark_blocked` are the mutation
    surface; they call `save()` themselves so callers never forget.
    """

    def __init__(self, path: Path, state: RunState):
        self.path = path
        self.state = state

    # ---- factory / persistence ------------------------------------------

    @classmethod
    def create(cls, state_dir: Path, run_id: str, mode: str) -> "RunFile":
        """Build a fresh RunFile — used by the CLI when starting a new run."""
        state_dir.mkdir(parents=True, exist_ok=True)
        state = RunState(
            run_id=run_id,
            started_at=_utc_now_iso(),
            mode=mode,  # type: ignore[arg-type]
        )
        path = state_dir / f"run-{run_id}.json"
        rf = cls(path, state)
        rf.save()
        return rf

    @classmethod
    def load(cls, path: Path) -> "RunFile":
        """Read an existing run file (used by `--resume`)."""
        with open(path, encoding="utf-8") as fh:
            raw = json.load(fh)
        # Rehydrate Dispatch objects — asdict() would have flattened them.
        in_flight_raw: dict[str, dict[str, Any]] = raw.get("in_flight", {}) or {}
        in_flight = {tid: Dispatch(**row) for tid, row in in_flight_raw.items()}
        state = RunState(
            run_id=raw["run_id"],
            started_at=raw["started_at"],
            mode=raw.get("mode", "auto"),
            in_flight=in_flight,
            completed=list(raw.get("completed", [])),
            blocked=list(raw.get("blocked", [])),
            deferred=list(raw.get("deferred", [])),
        )
        return cls(path, state)

    def save(self) -> None:
        """Atomic rewrite. Retries once if the rename step raises.

        The single retry covers transient FS races (e.g. antivirus holding
        the tmp file open). If the retry also fails we let the exception
        propagate — the operator needs to know.
        """
        payload = json.dumps(_run_state_to_dict(self.state), indent=2).encode("utf-8")
        try:
            _atomic_write(self.path, payload)
        except OSError as exc:
            log.warning("run-file atomic write failed once (%s); retrying", exc)
            _atomic_write(self.path, payload)
        # Keep state/index.json in sync so the dashboard SPA can enumerate runs.
        rebuild_index(self.path.parent)

    # ---- mutation surface -----------------------------------------------

    def add_dispatch(self, dispatch: Dispatch) -> None:
        self.state.in_flight[dispatch.task_id] = dispatch
        self.save()

    def remove_dispatch(self, task_id: str) -> None:
        self.state.in_flight.pop(task_id, None)
        self.save()

    def mark_done(self, task_id: str) -> None:
        self.state.in_flight.pop(task_id, None)
        if task_id not in self.state.completed:
            self.state.completed.append(task_id)
        self.save()

    def mark_blocked(self, task_id: str) -> None:
        self.state.in_flight.pop(task_id, None)
        if task_id not in self.state.blocked:
            self.state.blocked.append(task_id)
        self.save()


def _run_state_to_dict(state: RunState) -> dict[str, Any]:
    """Serialize `RunState` including nested `Dispatch` values."""
    return {
        "run_id": state.run_id,
        "started_at": state.started_at,
        "mode": state.mode,
        "in_flight": {tid: asdict(d) for tid, d in state.in_flight.items()},
        "completed": list(state.completed),
        "blocked": list(state.blocked),
        "deferred": list(state.deferred),
    }


# ---- Event log ----------------------------------------------------------


class EventLog:
    """Append-only JSONL writer at `state/events-<run-id>.jsonl`.

    Each line is exactly one `EventEntry`. Writes are flushed after every
    line so a crash still leaves a complete tail — the dashboard tails this
    file live. Event types are validated against `EVENT_TYPES` (FR-STATE-7).

    Fase 2: `project_id` opcional en el constructor. Cuando lo pasás, todos
    los `EventEntry` emitidos lo llevan; cuando es None (retrocompat con
    tests viejos y con invocaciones rupies previas al refactor), queda None.
    """

    def __init__(self, path: Path, project_id: str | None = None):
        self.path = path
        self.project_id = project_id
        self.path.parent.mkdir(parents=True, exist_ok=True)

    def emit(
        self,
        event_type: str,
        task_id: str,
        backend: str | None = None,
        **extra: Any,
    ) -> EventEntry:
        """Append one event line. Returns the entry (useful for tests)."""
        if event_type not in EVENT_TYPES:
            # Locked schema — extending needs a spec update, not a silent write.
            raise ValueError(
                f"unknown event_type {event_type!r}; allowed: {EVENT_TYPES}"
            )
        entry = EventEntry(
            event_type=event_type,
            task_id=task_id,
            backend=backend or "",
            ts=_utc_now_iso(),
            extra=dict(extra),
            project_id=self.project_id,
        )
        line = json.dumps(asdict(entry), separators=(",", ":"))
        with open(self.path, "a", encoding="utf-8") as fh:
            fh.write(line + "\n")
            fh.flush()
        # Refresh index so the dashboard sees updated_at on live runs.
        rebuild_index(self.path.parent)
        return entry


# ---- Spend log ----------------------------------------------------------


class SpendLog:
    """Append-only JSONL writer at `state/spend-<YYYY-MM-DD>.jsonl` (UTC).

    Date rotation is per-entry: the file path is derived from `entry.ts`, so
    a run spanning midnight UTC produces two files without any explicit
    rollover step. That matches the dashboard's contract of polling
    `spend-<today>.jsonl` (NFR-OBS-2, C-5).

    Fase 2: `project_id` opcional. Si el entry viene sin `project_id` (None),
    y el log tiene uno seteado, lo populamos al vuelo antes de serializar.
    Esto ahorra sitios río arriba: pasás `SpendLog(state_dir, project_id=...)`
    y te olvidás. Si el caller igual pasa `project_id` explícito en el entry,
    respetamos ese (no lo pisamos).
    """

    def __init__(self, state_dir: Path, project_id: str | None = None):
        self.state_dir = state_dir
        self.project_id = project_id
        self.state_dir.mkdir(parents=True, exist_ok=True)

    def _path_for(self, ts: str) -> Path:
        # `ts` looks like "2026-08-19T12:00:00Z" — take the date prefix.
        # If parsing fails, fall back to "today" so a bad ts never silently
        # drops the row.
        try:
            date = datetime.strptime(ts, "%Y-%m-%dT%H:%M:%SZ").date()
        except ValueError:
            date = datetime.now(timezone.utc).date()
        return self.state_dir / f"spend-{date.isoformat()}.jsonl"

    def record(self, entry: SpendEntry) -> Path:
        """Append one row; returns the file path written to (for tests).

        Fase 2: si el `SpendLog` tiene `project_id` y el entry no lo trae
        (None), lo enriquecemos antes de serializar. `SpendEntry` es frozen
        → hacemos `dataclasses.replace` en vez de mutar in-place.
        """
        if self.project_id is not None and entry.project_id is None:
            from dataclasses import replace as _dc_replace
            entry = _dc_replace(entry, project_id=self.project_id)
        path = self._path_for(entry.ts)
        line = json.dumps(asdict(entry), separators=(",", ":"))
        with open(path, "a", encoding="utf-8") as fh:
            fh.write(line + "\n")
            fh.flush()
        return path


# ---- Resume reconciliation ---------------------------------------------


def _pid_alive(pid: int) -> bool:
    """Return True if `pid` refers to a live process. `os.kill(pid, 0)` is
    the classic POSIX aliveness probe — signal 0 is a no-op that only does
    the permission/existence check.
    """
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        # PID exists but we can't signal it — treat as alive to be safe.
        return True
    return True


def _git_diff_touches(files: list[str], project_root: Path | None = None) -> bool:
    """True if any of the given paths shows up in `git status --porcelain`.

    Used by the resume heuristic to decide whether a dead PID actually did
    any work (finish) or crashed empty-handed (revert). Never raises: if git
    isn't installed or the repo is missing, returns False.

    Fase 1: acepta `project_root` opcional; None → `Path.cwd()`.
    """
    import subprocess

    if not files:
        return False
    probe_cwd = Path(project_root) if project_root is not None else Path.cwd()
    try:
        result = subprocess.run(  # noqa: S603 — trusted local git
            ["git", "status", "--porcelain", "--", *files],
            check=False,
            capture_output=True,
            text=True,
            cwd=str(probe_cwd),
        )
    except (FileNotFoundError, OSError) as exc:
        log.warning("git status probe failed: %s", exc)
        return False
    return bool(result.stdout.strip())


def reconcile_run(
    run_file: RunFile,
    event_log: EventLog,
    tasks_by_id: dict[str, Task],
    project_root: Path | None = None,
) -> ReconcileReport:
    """Reconcile a resumed run's `in_flight` map against reality (FR-STATE-5).

    For each in-flight dispatch:
      - PID alive           → adopt (keep in `in_flight`, emit `resume_adopt`).
      - PID dead + git diff → agent finished work but didn't report → invoke
                              `task-finish.sh`, emit `resume_adopt`, drop from
                              `in_flight`.
      - PID dead + no diff  → invoke `task-block.sh` ("orchestrator crash, no
                              work detected"), emit `resume_revert`, drop from
                              `in_flight`.

    AS-07 says no task appears in `in_flight` across two run files
    simultaneously — this method guarantees that by mutating `run_file` and
    persisting via `mark_done` / `mark_blocked`.
    """
    report = ReconcileReport()
    # Snapshot the dict — we mutate `in_flight` inside the loop.
    for task_id, dispatch in list(run_file.state.in_flight.items()):
        try:
            if _lookup_shell("_pid_alive", _pid_alive)(dispatch.pid):
                event_log.emit(
                    "resume_adopt",
                    task_id,
                    backend=dispatch.backend,
                    pid=dispatch.pid,
                    reason="pid_alive",
                )
                report.adopted.append(task_id)
                continue

            # PID is dead — decide finish-or-revert based on git evidence.
            task = tasks_by_id.get(task_id)
            files = list(task.files) if task else []
            model = f"{dispatch.backend}/unknown"  # best-effort tag for scripts

            if files and _lookup_shell("_git_diff_touches", _git_diff_touches)(
                files, project_root=project_root
            ):
                _lookup_shell("call_task_finish", _call_task_finish_direct)(
                    task_id,
                    "resumed: git diff detected on declared files",
                    model,
                    project_root=project_root,
                )
                event_log.emit(
                    "resume_adopt",
                    task_id,
                    backend=dispatch.backend,
                    pid=dispatch.pid,
                    reason="dead_but_files_dirty",
                )
                run_file.mark_done(task_id)
                report.adopted.append(task_id)
            else:
                _lookup_shell("call_task_block", _call_task_block_direct)(
                    task_id,
                    "orchestrator crash, no work detected",
                    model,
                    project_root=project_root,
                )
                event_log.emit(
                    "resume_revert",
                    task_id,
                    backend=dispatch.backend,
                    pid=dispatch.pid,
                    reason="dead_no_diff",
                )
                run_file.mark_blocked(task_id)
                report.reverted.append(task_id)
        except Exception as exc:  # noqa: BLE001 — never let reconciler explode
            log.exception("reconcile failed for %s: %s", task_id, exc)
            report.errors.append(f"{task_id}: {exc}")
    return report


# ------------------------------------------------------------------------
# StateBackend façade — commit 1 wrapper. No behavior change vs pre-refactor.
# ------------------------------------------------------------------------


class FileBackend:
    """`StateBackend` implementation that delegates to the free functions above.

    The wrapper is intentionally thin: every method routes to the same
    JSON/JSONL code path today's orchestrator uses. The abstraction only
    exists so commit 2 can drop a `SqliteBackend` alongside without touching
    callsites (see `orchestrator/state/interface.py`).

    Constructed once per process by `get_backend(paths, cfg)`.
    """

    def __init__(
        self,
        state_dir: Path,
        project_id: str | None = None,
        project_root: Path | None = None,
    ) -> None:
        self.state_dir = Path(state_dir)
        self.project_id = project_id
        self.project_root = project_root
        # In-memory cache of RunFile handles keyed by run_id so `add_dispatch`
        # and friends don't re-read + re-parse on every mutation.
        self._runs: dict[str, RunFile] = {}

    # ---- lifecycle ------------------------------------------------------

    def bootstrap(self, tasks: Iterable[Task]) -> None:
        """No-op for the file backend — state files are lazy-created on write."""
        # Reserved for parity with sqlite. Kept as an explicit no-op so a
        # reader sees the intent instead of an accidental omission.
        _ = list(tasks)

    # ---- task status ----------------------------------------------------

    def get_task_status(self, task_id: str) -> Status | None:
        """Read status straight from `tasks.json` — the source of truth for
        the file backend. Returns None if the id is unknown.
        """
        if self.project_root is None:
            return None
        tasks_json = self.project_root / "tasks.json"
        if not tasks_json.exists():
            return None
        try:
            tasks = load_tasks(tasks_json)
        except Exception:  # noqa: BLE001
            return None
        for t in tasks:
            if t.id == task_id:
                return t.status
        return None

    def get_all_task_status(self) -> dict[str, Status]:
        """Snapshot of every task_id → status from `tasks.json`."""
        if self.project_root is None:
            return {}
        tasks_json = self.project_root / "tasks.json"
        if not tasks_json.exists():
            return {}
        try:
            tasks = load_tasks(tasks_json)
        except Exception:  # noqa: BLE001
            return {}
        return {t.id: t.status for t in tasks}

    def set_task_status(
        self,
        task_id: str,
        status: Status,
        author: str,
        note: str,
        ts: str,  # noqa: ARG002 — file backend derives ts inside the shell script
    ) -> None:
        """Route to the appropriate `scripts/task-*.sh` for file backend.

        The shell scripts remain the sole writers of tasks.json (single-writer
        contract). The file backend just picks which script based on status.
        `reset` uses task-start-style transition to `todo` — for the file
        backend today, we shell out to a `task-reset.sh` if present; otherwise
        we skip (sqlite backend implements it natively).
        """
        # Validate the id up front so unknown tasks fail with KeyError.
        if self.get_task_status(task_id) is None and self.project_root is not None:
            raise KeyError(task_id)
        if status == "in-progress":
            call_task_start(task_id, author=author, project_root=self.project_root)
        elif status == "done":
            call_task_finish(task_id, note or "done", author, project_root=self.project_root)
        elif status == "blocked":
            call_task_block(task_id, note or "blocked", author, project_root=self.project_root)
        elif status == "todo":
            # No dedicated shell script for reset in the current templates.
            # Best effort: shell out if the operator added one, else no-op.
            reset = None
            if self.project_root is not None:
                candidate = self.project_root / "scripts" / "task-reset.sh"
                if candidate.exists():
                    reset = candidate
            if reset is not None:
                import subprocess

                subprocess.run(  # noqa: S603
                    [str(reset), task_id, author, note or "reset"],
                    check=False,
                    capture_output=True,
                    text=True,
                    cwd=str(self.project_root),
                )
        else:
            raise ValueError(f"unsupported status transition: {status!r}")

    # ---- runs -----------------------------------------------------------

    def create_run(self, run_id: str, mode: str) -> RunState:
        rf = RunFile.create(self.state_dir, run_id=run_id, mode=mode)
        self._runs[run_id] = rf
        return rf.state

    def load_run(self, run_id: str) -> RunState:
        path = self.state_dir / f"run-{run_id}.json"
        rf = RunFile.load(path)
        self._runs[run_id] = rf
        return rf.state

    def save_run(self, state: RunState) -> None:
        rf = self._runs.get(state.run_id)
        if rf is None:
            rf = RunFile(self.state_dir / f"run-{state.run_id}.json", state)
            self._runs[state.run_id] = rf
        rf.state = state
        rf.save()

    def list_runs(self) -> list[dict[str, Any]]:
        rebuild_index(self.state_dir)
        index_path = self.state_dir / "index.json"
        if not index_path.exists():
            return []
        try:
            with open(index_path, encoding="utf-8") as fh:
                raw = json.load(fh)
            return list(raw.get("runs", []) or [])
        except (OSError, json.JSONDecodeError):
            return []

    # ---- dispatches -----------------------------------------------------

    def add_dispatch(self, run_id: str, dispatch: Dispatch) -> None:
        rf = self._require_run(run_id)
        rf.add_dispatch(dispatch)

    def remove_dispatch(self, run_id: str, task_id: str) -> None:
        rf = self._require_run(run_id)
        rf.remove_dispatch(task_id)

    def iter_in_flight(self, run_id: str) -> Iterator[Dispatch]:
        rf = self._require_run(run_id)
        for d in rf.state.in_flight.values():
            yield d

    def _require_run(self, run_id: str) -> RunFile:
        rf = self._runs.get(run_id)
        if rf is None:
            path = self.state_dir / f"run-{run_id}.json"
            if not path.exists():
                raise KeyError(run_id)
            rf = RunFile.load(path)
            self._runs[run_id] = rf
        return rf

    # ---- events + spend -------------------------------------------------

    def append_event(self, run_id: str, entry: EventEntry) -> None:
        events_path = self.state_dir / f"events-{run_id}.jsonl"
        # Bypass EventLog's ts stamping — the entry already has one.
        events_path.parent.mkdir(parents=True, exist_ok=True)
        line = json.dumps(asdict(entry), separators=(",", ":"))
        with open(events_path, "a", encoding="utf-8") as fh:
            fh.write(line + "\n")
            fh.flush()
        rebuild_index(self.state_dir)

    def iter_events(
        self,
        run_id: str | None = None,
        since_id: int | None = None,  # noqa: ARG002 — file backend has no monotonic id
    ) -> Iterator[dict[str, Any]]:
        if run_id is not None:
            paths = [self.state_dir / f"events-{run_id}.jsonl"]
        else:
            paths = sorted(self.state_dir.glob("events-*.jsonl"))
        for p in paths:
            if not p.exists():
                continue
            with p.open("r", encoding="utf-8") as fh:
                for line in fh:
                    line = line.strip()
                    if not line:
                        continue
                    try:
                        obj = json.loads(line)
                    except (json.JSONDecodeError, ValueError):
                        continue
                    if isinstance(obj, dict):
                        yield obj

    def append_spend(self, entry: SpendEntry) -> None:
        SpendLog(self.state_dir, project_id=self.project_id).record(entry)

    def iter_spend(
        self,
        since: str | None = None,
        until: str | None = None,
    ) -> Iterator[dict[str, Any]]:
        for row in self.iter_all_spend():
            ts = row.get("ts", "")
            if since is not None and ts < since:
                continue
            if until is not None and ts > until:
                continue
            yield row

    def iter_all_spend(self) -> Iterator[dict[str, Any]]:
        for path in sorted(self.state_dir.glob("spend-*.jsonl")):
            with path.open("r", encoding="utf-8") as fh:
                for line in fh:
                    line = line.strip()
                    if not line:
                        continue
                    try:
                        obj = json.loads(line)
                    except (json.JSONDecodeError, ValueError):
                        continue
                    if isinstance(obj, dict):
                        yield obj

    # ---- reconciliation -------------------------------------------------

    def reconcile_in_flight(self) -> dict[str, Any]:
        return reconcile_in_flight(self.state_dir, project_id=self.project_id)

    def reset_task(self, task_id: str, author: str, note: str, ts: str) -> None:
        self.set_task_status(task_id, "todo", author=author, note=note, ts=ts)

    def clear_in_flight_for_run(self, run_id: str) -> list[str]:
        rf = self._require_run(run_id)
        cleared = list(rf.state.in_flight.keys())
        rf.state.in_flight.clear()
        rf.save()
        return cleared
