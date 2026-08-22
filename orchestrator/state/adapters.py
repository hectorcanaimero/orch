"""Adapter shims that present the historic `RunFile` / `EventLog` / `SpendLog`
surface but persist through a `StateBackend`.

These exist so the main orchestrator loop (`orch.py`) does NOT need to know
which backend is active — it constructs whatever object the shim provides
and keeps calling the same methods it used before Sprint B.

The file backend does not need any adapter: `FileBackend` uses the concrete
`RunFile` / `EventLog` / `SpendLog` classes directly, so callers on the file
path get those instances back verbatim.

The sqlite backend gets thin wrappers here whose `.add_dispatch`, `.emit`,
`.record` methods call the sqlite backend under the hood.
"""

from __future__ import annotations

from pathlib import Path
from typing import Any

from ..models import Dispatch, EventEntry, RunState, SpendEntry
from .file_backend import EVENT_TYPES, EventLog, RunFile, SpendLog, _utc_now_iso
from .interface import StateBackend
from .sqlite_backend import SqliteBackend


class SqliteRunFile:
    """RunFile-shaped wrapper backed by a `SqliteBackend`.

    Preserves the callsite contract of `orchestrator/state.RunFile`:
        - `.path` — synthetic path (`<state_dir>/run-<id>.json`) for logging
        - `.state` — the in-memory `RunState`
        - `.add_dispatch(dispatch)` / `.remove_dispatch(tid)`
        - `.mark_done(tid)` / `.mark_blocked(tid)`
        - `.save()`

    Every mutation is mirrored to the sqlite backend inside the same call so
    the orchestrator doesn't need a batch/flush step.
    """

    def __init__(
        self,
        backend: SqliteBackend,
        state: RunState,
        state_dir: Path,
    ) -> None:
        self.backend = backend
        self.state = state
        self.path = state_dir / f"run-{state.run_id}.json"

    @classmethod
    def create(
        cls,
        backend: SqliteBackend,
        run_id: str,
        mode: str,
        state_dir: Path,
        parent_pid: int = 0,
    ) -> "SqliteRunFile":
        state = backend.create_run(run_id=run_id, mode=mode, parent_pid=parent_pid)
        return cls(backend, state, state_dir)

    @classmethod
    def load(cls, backend: SqliteBackend, run_id: str, state_dir: Path) -> "SqliteRunFile":
        state = backend.load_run(run_id)
        return cls(backend, state, state_dir)

    def save(self) -> None:
        self.backend.save_run(self.state)

    def add_dispatch(self, dispatch: Dispatch) -> None:
        self.state.in_flight[dispatch.task_id] = dispatch
        self.backend.add_dispatch(self.state.run_id, dispatch)
        self.backend.save_run(self.state)

    def remove_dispatch(self, task_id: str) -> None:
        self.state.in_flight.pop(task_id, None)
        self.backend.remove_dispatch(self.state.run_id, task_id)
        self.backend.save_run(self.state)

    def mark_done(self, task_id: str) -> None:
        self.state.in_flight.pop(task_id, None)
        if task_id not in self.state.completed:
            self.state.completed.append(task_id)
        self.backend.remove_dispatch(self.state.run_id, task_id)
        self.backend.save_run(self.state)

    def mark_blocked(self, task_id: str) -> None:
        self.state.in_flight.pop(task_id, None)
        if task_id not in self.state.blocked:
            self.state.blocked.append(task_id)
        self.backend.remove_dispatch(self.state.run_id, task_id)
        self.backend.save_run(self.state)


class SqliteEventLog:
    """EventLog-shaped wrapper. `.emit(event_type, task_id, backend, **extra)`.

    `run_id` is captured at construction so the main loop's existing
    `event_log.emit(...)` call sites remain unchanged.
    """

    def __init__(
        self,
        backend: SqliteBackend,
        run_id: str,
        state_dir: Path,
        project_id: str | None = None,
    ) -> None:
        self.backend = backend
        self.run_id = run_id
        self.path = state_dir / f"events-{run_id}.jsonl"
        self.project_id = project_id or backend.project_id

    def emit(
        self,
        event_type: str,
        task_id: str,
        backend: str | None = None,
        **extra: Any,
    ) -> EventEntry:
        if event_type not in EVENT_TYPES:
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
        self.backend.append_event(self.run_id, entry)
        return entry


class SqliteSpendLog:
    """SpendLog-shaped wrapper. `.record(entry)` returns a synthetic path."""

    def __init__(
        self,
        backend: SqliteBackend,
        state_dir: Path,
        project_id: str | None = None,
    ) -> None:
        self.backend = backend
        self.state_dir = state_dir
        self.project_id = project_id or backend.project_id

    def record(self, entry: SpendEntry) -> Path:
        # Enrich project_id if the entry didn't declare one (parity with
        # file-backed SpendLog).
        if self.project_id is not None and entry.project_id is None:
            from dataclasses import replace as _dc_replace
            entry = _dc_replace(entry, project_id=self.project_id)
        self.backend.append_spend(entry)
        # Synthetic path — not written to, but callers may log it.
        try:
            from datetime import datetime as _dt

            date_str = _dt.strptime(entry.ts, "%Y-%m-%dT%H:%M:%SZ").date().isoformat()
        except ValueError:
            date_str = "unknown"
        return self.state_dir / f"spend-{date_str}.jsonl"


def make_runfile(
    backend: StateBackend,
    state_dir: Path,
    run_id: str,
    mode: str,
    *,
    create: bool = True,
    parent_pid: int = 0,
) -> Any:
    """Backend-aware factory. Returns either `RunFile` or `SqliteRunFile`.

    Sprint A / Issue #12: `parent_pid` is forwarded to the concrete backend
    on create so `orch stop` can locate the running orch later.
    """
    if isinstance(backend, SqliteBackend):
        if create:
            return SqliteRunFile.create(
                backend, run_id, mode, state_dir, parent_pid=parent_pid
            )
        return SqliteRunFile.load(backend, run_id, state_dir)
    if create:
        return RunFile.create(
            state_dir, run_id=run_id, mode=mode, parent_pid=parent_pid
        )
    return RunFile.load(state_dir / f"run-{run_id}.json")


def make_event_log(
    backend: StateBackend,
    state_dir: Path,
    run_id: str,
    project_id: str | None = None,
) -> Any:
    """Backend-aware factory. Returns `EventLog` or `SqliteEventLog`."""
    if isinstance(backend, SqliteBackend):
        return SqliteEventLog(backend, run_id=run_id, state_dir=state_dir, project_id=project_id)
    return EventLog(state_dir / f"events-{run_id}.jsonl", project_id=project_id)


def make_spend_log(
    backend: StateBackend,
    state_dir: Path,
    project_id: str | None = None,
) -> Any:
    """Backend-aware factory. Returns `SpendLog` or `SqliteSpendLog`."""
    if isinstance(backend, SqliteBackend):
        return SqliteSpendLog(backend, state_dir=state_dir, project_id=project_id)
    return SpendLog(state_dir, project_id=project_id)
