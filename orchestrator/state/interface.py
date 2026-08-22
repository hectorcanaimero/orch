"""`StateBackend` Protocol — the abstraction commit 2 flips over to sqlite.

Every backend method here is called from at least one site in `orch.py`,
`dispatcher.py`, or the dashboard. The Protocol is intentionally narrow: it
carves out the mutable slice of state so the `tasks.json` DAG + spec fields
can stay file-owned regardless of backend.

Design notes:
    - Methods are named after the domain concept, not the storage. The file
      backend translates them into JSONL/JSON writes; the sqlite backend
      translates them into SQL.
    - `bootstrap(tasks)` is a no-op for the file backend (state files are
      created on first write). The sqlite backend uses it to seed
      `projects` + `tasks_runtime` rows.
    - `set_task_status()` is the ONLY method that mutates task status. It
      MUST be called exclusively from the `orch task-status` subcommand
      (single-writer contract — see design §4).
    - Iterators return dicts (not dataclasses) so the dashboard can serialize
      them as JSON without a round-trip.
"""

from __future__ import annotations

from pathlib import Path
from typing import Any, Iterable, Iterator, Protocol, runtime_checkable

from ..models import Dispatch, EventEntry, RunState, SpendEntry, Status, Task


@runtime_checkable
class StateBackend(Protocol):
    """Storage-agnostic view over the mutable orchestrator state."""

    # ---- lifecycle ------------------------------------------------------

    def bootstrap(self, tasks: Iterable[Task]) -> None:
        """Idempotent seeding of project + per-task runtime rows.

        File backend: no-op (state files are created lazily on first write).
        Sqlite backend: `INSERT OR IGNORE` into `projects` + `tasks_runtime`.
        """
        ...

    # ---- task status ----------------------------------------------------

    def get_task_status(self, task_id: str) -> Status | None:
        """Current status or None if the task is unknown to the backend."""
        ...

    def get_all_task_status(self) -> dict[str, Status]:
        """Snapshot of every known task_id → status."""
        ...

    def set_task_status(
        self,
        task_id: str,
        status: Status,
        author: str,
        note: str,
        ts: str,
    ) -> None:
        """MUTATE task status. ONLY callable from `orch task-status`.

        Raises `KeyError` if `task_id` is unknown to the backend.
        """
        ...

    # ---- runs -----------------------------------------------------------

    def create_run(self, run_id: str, mode: str) -> RunState:
        """Persist a fresh run and return the initial in-memory state."""
        ...

    def load_run(self, run_id: str) -> RunState:
        """Rehydrate an existing run. Raises `FileNotFoundError`/`KeyError`."""
        ...

    def save_run(self, state: RunState) -> None:
        """Mirror in-memory `RunState` to storage after any mutation."""
        ...

    def list_runs(self) -> list[dict[str, Any]]:
        """Directory-listing analogue used by the dashboard index."""
        ...

    # ---- dispatches -----------------------------------------------------

    def add_dispatch(self, run_id: str, dispatch: Dispatch) -> None:
        """Record a new in-flight subprocess."""
        ...

    def remove_dispatch(self, run_id: str, task_id: str) -> None:
        """Mark a dispatch finished (removed from in-flight)."""
        ...

    def iter_in_flight(self, run_id: str) -> Iterator[Dispatch]:
        """Yield every currently in-flight dispatch for a run."""
        ...

    # ---- events + spend -------------------------------------------------

    def append_event(self, run_id: str, entry: EventEntry) -> None:
        """Append one event row (file: JSONL, sqlite: `events` INSERT)."""
        ...

    def iter_events(
        self,
        run_id: str | None = None,
        since_id: int | None = None,
    ) -> Iterator[dict[str, Any]]:
        """Yield event rows. `since_id` is for the sqlite dashboard live tail."""
        ...

    def append_spend(self, entry: SpendEntry) -> None:
        """Append one spend row (file: JSONL rotated by date, sqlite: `spend`)."""
        ...

    def iter_spend(
        self,
        since: str | None = None,
        until: str | None = None,
    ) -> Iterator[dict[str, Any]]:
        """Yield spend rows in the [since, until] ISO ts window (inclusive)."""
        ...

    def iter_all_spend(self) -> Iterator[dict[str, Any]]:
        """Yield every spend row for this project across all dates."""
        ...

    # ---- reconciliation -------------------------------------------------

    def reconcile_in_flight(self) -> dict[str, Any]:
        """Scan every run for orphan PIDs, reap dead ones. Returns a report."""
        ...

    def reset_task(self, task_id: str, author: str, note: str, ts: str) -> None:
        """Force `task_id` back to `todo` — used by the `reset` script."""
        ...

    def clear_in_flight_for_run(self, run_id: str) -> list[str]:
        """Remove every in-flight dispatch for the run. Returns the task_ids."""
        ...


class BackendFactoryError(Exception):
    """Raised by `get_backend()` when the config asks for a backend that isn't
    wired up (typo, disabled feature, etc.). Callers should map to exit-code 1.
    """
