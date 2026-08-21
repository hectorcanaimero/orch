"""DAG resolver over `tasks.json`.

Design (per `design.md §4`):
    - Kahn-variant scheduler with dynamic re-evaluation on `mark_done`.
    - Cycle detection at load time (safety net — current data is a forest).
    - Never mutates `tasks.json` on disk. All status transitions live in an
      in-memory `dict[id, Status]` (`_status`); the real writer is
      `scripts/task-*.sh`, invoked by `state.py` (Batch C).

Split of concerns:
    - `load_tasks` lives in `orchestrator.state` (I/O layer, R-010). It is
      re-exported here for backwards compatibility with earlier Batch B
      callers/tests that imported it from this module.
    - `TaskQueue` owns the mutable status map.
    - `ready()` is pure w.r.t. `_status` + `in_flight_ids` (FR-Q-5).
"""

from __future__ import annotations

import fnmatch
from typing import Iterable

from .models import Status, Task
from .state import load_tasks  # re-export — canonical home is `state.py`

__all__ = [
    "TaskQueue",
    "TaskCycleError",
    "MissingDependencyError",
    "load_tasks",
]


class TaskCycleError(Exception):
    """`tasks.json` contains a dependency cycle (FR-Q-3).

    The message lists the offending IDs so the operator can trace the loop.
    """

    def __init__(self, cycle: list[str]):
        self.cycle = cycle
        chain = " -> ".join(cycle) if cycle else "(unknown)"
        super().__init__(
            f"dependency cycle detected in tasks.json: {chain}. "
            "Break the cycle before running the orchestrator."
        )


class MissingDependencyError(Exception):
    """A task lists a `deps[]` id that isn't in `tasks.json` (FR-Q-4)."""

    def __init__(self, offenders: list[tuple[str, str]]):
        self.offenders = offenders
        lines = [f"  - {tid} depends on unknown {dep!r}" for tid, dep in offenders]
        super().__init__(
            f"tasks.json has {len(offenders)} unresolved dependency reference(s):\n"
            + "\n".join(lines)
        )


class TaskQueue:
    """Dynamic Kahn-variant resolver over an in-memory status map.

    `tasks.json` is read-only from the orchestrator's perspective (FR-STATE-2);
    this class holds the ephemeral view that reacts to `mark_done`,
    `mark_blocked`, and `mark_in_flight` between reap ticks.
    """

    def __init__(self, tasks: Iterable[Task]):
        self._by_id: dict[str, Task] = {}
        for t in tasks:
            if t.id in self._by_id:
                raise ValueError(f"duplicate task id in tasks.json: {t.id!r}")
            self._by_id[t.id] = t

        self._validate_deps()  # missing-dep guard (FR-Q-4)
        self._detect_cycles()  # cycle guard (FR-Q-3)

        # Status map is what mutates. `Task` itself is frozen.
        self._status: dict[str, Status] = {
            tid: t.status for tid, t in self._by_id.items()
        }

    # ---- validation ------------------------------------------------------

    def _validate_deps(self) -> None:
        offenders: list[tuple[str, str]] = []
        for t in self._by_id.values():
            for dep in t.dependencies:
                if dep not in self._by_id:
                    offenders.append((t.id, dep))
        if offenders:
            offenders.sort()
            raise MissingDependencyError(offenders)

    def _detect_cycles(self) -> None:
        """DFS with three-color marking (WHITE/GRAY/BLACK) → cycle list on hit."""
        WHITE, GRAY, BLACK = 0, 1, 2
        color: dict[str, int] = {tid: WHITE for tid in self._by_id}
        stack: list[str] = []

        def visit(node: str) -> None:
            color[node] = GRAY
            stack.append(node)
            for dep in self._by_id[node].dependencies:
                if color[dep] == GRAY:
                    # Cycle: extract the ring from the DFS stack.
                    i = stack.index(dep)
                    raise TaskCycleError(stack[i:] + [dep])
                if color[dep] == WHITE:
                    visit(dep)
            color[node] = BLACK
            stack.pop()

        for tid in self._by_id:
            if color[tid] == WHITE:
                visit(tid)

    # ---- read side -------------------------------------------------------

    def status(self, task_id: str) -> Status:
        return self._status[task_id]

    def all_tasks(self) -> list[Task]:
        """Deterministic order: stable sort by (phase, id) — matches FR-Q-2."""
        return sorted(self._by_id.values(), key=lambda t: (t.phase, t.id))

    def ready(
        self,
        in_flight_ids: Iterable[str] | None = None,
        only: str | None = None,
    ) -> list[Task]:
        """Return tasks whose deps are all `done` and self is `todo`.

        Pure w.r.t. its inputs (FR-Q-5): same `_status` + `in_flight_ids` →
        same output. `in_flight_ids` excludes anything the caller has already
        picked but not yet marked `in-progress`.

        `only` is a dispatcher-scope fnmatch glob on `task.id`; when set,
        only tasks matching the glob are returned. The FULL DAG is still used
        for dep resolution — a task whose deps sit OUTSIDE the glob becomes
        ready as soon as those deps reach `done` (typical for tasks pre-marked
        `done` in `tasks.json`). This is the correct home for `--only`
        filtering: graph-scope validation stays whole, only the candidate set
        for dispatch is narrowed.
        """
        in_flight = set(in_flight_ids or ())
        out: list[Task] = []
        for tid in sorted(self._by_id, key=lambda i: (self._by_id[i].phase, i)):
            if self._status[tid] != "todo":
                continue
            if tid in in_flight:
                continue
            if only is not None and not fnmatch.fnmatchcase(tid, only):
                continue
            task = self._by_id[tid]
            if all(self._status.get(d) == "done" for d in task.dependencies):
                out.append(task)
        return out

    def pending(self) -> bool:
        """True while any task is still `todo` or `in-progress`."""
        return any(s in ("todo", "in-progress") for s in self._status.values())

    # ---- write side (in-memory only; disk is `scripts/task-*.sh`) --------

    def mark_in_flight(self, task_id: str) -> None:
        self._require_known(task_id)
        self._status[task_id] = "in-progress"

    def mark_done(self, task_id: str) -> None:
        """Mark a task done — dependents become ready on the next `ready()`."""
        self._require_known(task_id)
        self._status[task_id] = "done"

    def mark_blocked(self, task_id: str) -> None:
        """Mark blocked. Dependents do NOT become ready (see AS-02)."""
        self._require_known(task_id)
        self._status[task_id] = "blocked"

    def _require_known(self, task_id: str) -> None:
        if task_id not in self._by_id:
            raise KeyError(f"unknown task id: {task_id!r}")
