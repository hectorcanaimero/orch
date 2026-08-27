"""`StateBackend` implementation backed by a single SQLite database.

Design notes (see docs/design/sprint-b-sqlite-backend.md for the full spec):

- One DB file per orch install (default `~/.orch/orch.db` or `state/orch.db`).
- Multitenant by `project_id` on every table. `projects.project_id` is the
  FK anchor. Cross-project reporting is a simple `GROUP BY project_id`.
- WAL mode + `busy_timeout=5000` + `BEGIN IMMEDIATE` for writes so two
  orchestrators pointing at the same DB serialize cleanly.
- Connection-per-operation. WAL means readers never block writers; sharing a
  single long-lived connection across threads breaks that guarantee. Each
  call opens a fresh connection with `check_same_thread=False`, uses it, and
  closes it. Fast enough for the orch's tick rate.
- Schema versioning via `PRAGMA user_version`. Migrations live in
  `sqlite_migrations/NNN_*.sql` and are applied in order at first-open.
- Non-local filesystem: startup warning (not hard fail) — WAL on NFS/SMB can
  corrupt. Detected via `subprocess.check_output(['df', '-T', path])`.
- Compatible with SQLite 3.32+ (macOS baseline). NO `RETURNING`, STRICT
  tables, or GENERATED columns.
"""

from __future__ import annotations

import hashlib
import json
import logging
import os
import sqlite3
import subprocess
import sys
from collections.abc import Iterator
from contextlib import contextmanager
from dataclasses import asdict
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Iterable

from ..models import Dispatch, EventEntry, Finding, RunState, SpendEntry, Status, Task

log = logging.getLogger(__name__)


# ---- Constants ----------------------------------------------------------


_ALLOWED_STATUSES: frozenset[str] = frozenset(
    {"backlog", "todo", "in-progress", "done", "blocked"}
)

_ALLOWED_CI_STATUSES: frozenset[str] = frozenset({"pending", "success", "failure", "skipped"})

# Legal transitions enforced by `set_task_status`. Same-status writes are
# always allowed (idempotent). Unknown transitions raise ValueError so the
# CLI can map to exit-code 3.
_STATUS_TRANSITIONS: dict[str, frozenset[str]] = {
    "backlog": frozenset({"todo", "blocked", "backlog"}),
    "todo": frozenset({"in-progress", "blocked", "todo", "done"}),  # done = manual completion
    "in-progress": frozenset({"done", "blocked", "todo", "in-progress"}),
    "blocked": frozenset({"todo", "in-progress", "blocked"}),
    "done": frozenset({"todo", "done"}),  # allow re-open for reset
}


def _utc_now_iso() -> str:
    """ISO-8601 UTC timestamp with 'Z' suffix (matches shell scripts)."""
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def _read_migrations() -> list[tuple[int, str, str]]:
    """Discover `NNN_*.sql` files in `sqlite_migrations/`.

    Returns [(version, filename, sql), ...] sorted by version.
    """
    here = Path(__file__).parent / "sqlite_migrations"
    out: list[tuple[int, str, str]] = []
    if not here.exists():
        return out
    for path in sorted(here.glob("*.sql")):
        stem = path.stem
        # "001_init" → version 1
        try:
            version = int(stem.split("_", 1)[0])
        except (ValueError, IndexError):
            continue
        out.append((version, path.name, path.read_text(encoding="utf-8")))
    out.sort(key=lambda row: row[0])
    return out


def _warn_if_non_local(db_path: Path) -> None:
    """Emit a WARN log if `db_path` sits on a non-apfs/hfs filesystem.

    WAL mode on network filesystems (NFS, SMB) can corrupt data — the WAL
    file needs true POSIX byte-range locks that many network mounts fake.
    On macOS we use `df -Y` to get a "Type" column ("apfs", "hfs", "nfs"…).
    Silently returns on non-macOS or any probe failure — never gate startup.
    """
    if sys.platform != "darwin":
        return
    try:
        # macOS BSD flavor: `df -Y` adds a "Type" column right after
        # Filesystem. `df -T` on macOS is a filter, not a printer.
        result = subprocess.run(
            ["df", "-Y", str(db_path.parent)],
            check=False,
            capture_output=True,
            text=True,
            timeout=2,
        )
    except (FileNotFoundError, OSError, subprocess.SubprocessError):
        return
    if result.returncode != 0:
        return
    lines = [ln for ln in result.stdout.splitlines() if ln.strip()]
    if len(lines) < 2:
        return
    header = lines[0].split()
    parts = lines[1].split()
    fstype = ""
    if "Type" in header:
        idx = header.index("Type")
        if idx < len(parts):
            fstype = parts[idx].lower()
    if fstype and fstype not in ("apfs", "hfs", "hfs+"):
        log.warning(
            "sqlite DB on non-local filesystem (%s) — WAL mode may corrupt "
            "(path=%s)",
            fstype,
            db_path,
        )


def _connect(db_path: Path) -> sqlite3.Connection:
    """Open a fresh SQLite connection with the pragmas orch expects.

    - `foreign_keys=ON` so ON DELETE CASCADE on `projects` actually fires.
    - `journal_mode=WAL` so readers don't block writers.
    - `busy_timeout=5000` so concurrent orchestrators wait 5s instead of
      raising SQLITE_BUSY immediately.
    - Row factory returns dict-like rows via sqlite3.Row.
    """
    db_path.parent.mkdir(parents=True, exist_ok=True)
    conn = sqlite3.connect(
        str(db_path),
        timeout=5.0,
        isolation_level=None,  # manual BEGIN — we want IMMEDIATE for writes
        check_same_thread=False,
    )
    conn.row_factory = sqlite3.Row
    conn.execute("PRAGMA foreign_keys = ON")
    conn.execute("PRAGMA journal_mode = WAL")
    conn.execute("PRAGMA busy_timeout = 5000")
    return conn


# ---- Schema bootstrap ---------------------------------------------------


_SCHEMA_INITIALIZED: set[str] = set()


def _init_schema(db_path: Path) -> None:
    """Apply every pending migration in `sqlite_migrations/`.

    Idempotent — `PRAGMA user_version` gates which migrations run. Called
    lazily on the first `_connect_and_init` per DB path.
    """
    key = str(db_path.resolve())
    if key in _SCHEMA_INITIALIZED:
        return
    _warn_if_non_local(db_path)
    conn = _connect(db_path)
    try:
        cur = conn.execute("PRAGMA user_version")
        current_version = cur.fetchone()[0]
        migrations = _read_migrations()
        for version, name, sql in migrations:
            if version <= current_version:
                continue
            log.info("sqlite: applying migration %s (v%d)", name, version)
            # `executescript` handles multiple statements + the PRAGMA
            # user_version inside the file bumps the value.
            conn.executescript(sql)
        # Ensure user_version is at least the max we shipped, defensively.
        if migrations:
            max_v = max(m[0] for m in migrations)
            conn.execute(f"PRAGMA user_version = {max_v}")
        _SCHEMA_INITIALIZED.add(key)
    finally:
        conn.close()


def _reset_schema_cache_for_tests() -> None:
    """Test helper — makes `_init_schema` re-run for the same DB path."""
    _SCHEMA_INITIALIZED.clear()


# ---- SqliteBackend ------------------------------------------------------


class SqliteBackend:
    """Multitenant SQLite implementation of `StateBackend`.

    One instance per `(db_path, project_id)`. The `project_id` is injected
    into every write. `bootstrap()` seeds the `projects` row and every
    `tasks_runtime` row (idempotent).
    """

    def __init__(
        self,
        db_path: Path,
        project_id: str,
        project_root: Path | None = None,
    ) -> None:
        self.db_path = Path(db_path)
        self.project_id = project_id
        self.project_root = project_root
        _init_schema(self.db_path)

    # ---- connection helpers --------------------------------------------

    def _conn(self) -> sqlite3.Connection:
        return _connect(self.db_path)

    @contextmanager
    def _write(self) -> Iterator[sqlite3.Connection]:
        """Context manager that opens a fresh connection, runs the block
        inside `BEGIN IMMEDIATE`, and commits/rolls back accordingly."""
        conn = self._conn()
        try:
            conn.execute("BEGIN IMMEDIATE")
            yield conn
            conn.execute("COMMIT")
        except Exception:
            try:
                conn.execute("ROLLBACK")
            except sqlite3.Error:
                pass
            raise
        finally:
            conn.close()

    # ---- lifecycle -----------------------------------------------------

    def bootstrap(self, tasks: Iterable[Task]) -> None:
        """Seed `projects` + `tasks_runtime` idempotently for this project.

        Uses `INSERT OR IGNORE` so re-running bootstrap over an existing
        DB is a no-op. `tasks.json` status is IGNORED — the DB is the
        source of truth for runtime status once initialized.
        """
        now = _utc_now_iso()
        root = str(self.project_root) if self.project_root is not None else ""
        with self._write() as conn:
            conn.execute(
                "INSERT OR IGNORE INTO projects "
                "(project_id, project_root, created_at, schema_version) "
                "VALUES (?, ?, ?, 1)",
                (self.project_id, root, now),
            )
            for t in tasks:
                # Only insert if missing — never overwrite runtime status.
                # `tasks.json` status is used ONLY on the very first insert
                # (fresh project). Later status changes must go through
                # `set_task_status`.
                conn.execute(
                    "INSERT OR IGNORE INTO tasks_runtime "
                    "(project_id, task_id, status, comments_json, updated_at) "
                    "VALUES (?, ?, ?, ?, ?)",
                    (
                        self.project_id,
                        t.id,
                        t.status if t.status in _ALLOWED_STATUSES else "todo",
                        json.dumps(list(t.comments or [])),
                        now,
                    ),
                )
                conn.execute(
                    "INSERT OR IGNORE INTO tasks_definition "
                    "(project_id, task_id, title, model, backend, deps_json, "
                    " spec_ref, phase, estimate_h, reason, files_json, updated_at) "
                    "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
                    (
                        self.project_id,
                        t.id,
                        t.title or "",
                        t.model,
                        None,
                        json.dumps(list(t.dependencies or [])),
                        t.spec_ref,
                        t.phase,
                        t.estimate_hours,
                        t.reason,
                        json.dumps(list(t.files or [])),
                        now,
                    ),
                )

    def upsert_task_definition(
        self,
        task_id: str,
        *,
        title: str,
        model: str | None,
        backend: str | None,
        deps: list[str],
        spec_ref: str | None,
        phase: int | None,
        estimate_h: float | None,
        reason: str | None,
        files: list[str],
    ) -> None:
        """INSERT OR REPLACE into tasks_definition — called by atomize --apply."""
        now = _utc_now_iso()
        with self._write() as conn:
            conn.execute(
                "INSERT OR REPLACE INTO tasks_definition "
                "(project_id, task_id, title, model, backend, deps_json, "
                " spec_ref, phase, estimate_h, reason, files_json, updated_at) "
                "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
                (
                    self.project_id,
                    task_id,
                    title or "",
                    model,
                    backend,
                    json.dumps(list(deps or [])),
                    spec_ref,
                    phase,
                    estimate_h,
                    reason,
                    json.dumps(list(files or [])),
                    now,
                ),
            )

    def set_task_model(self, task_id: str, model: str) -> None:
        """Update tasks_definition.model — used by orch task set --model."""
        now = _utc_now_iso()
        with self._write() as conn:
            rowcount = conn.execute(
                "UPDATE tasks_definition SET model = ?, updated_at = ? "
                "WHERE project_id = ? AND task_id = ?",
                (model, now, self.project_id, task_id),
            ).rowcount
        if rowcount == 0:
            raise KeyError(
                f"task '{task_id}' not found in tasks_definition for project "
                f"'{self.project_id}'. Run 'orch atomize --apply' first."
            )

    def set_task_backend(self, task_id: str, backend: str) -> None:
        """Update tasks_definition.backend — used by orch task set --backend."""
        now = _utc_now_iso()
        with self._write() as conn:
            rowcount = conn.execute(
                "UPDATE tasks_definition SET backend = ?, updated_at = ? "
                "WHERE project_id = ? AND task_id = ?",
                (backend, now, self.project_id, task_id),
            ).rowcount
        if rowcount == 0:
            raise KeyError(
                f"task '{task_id}' not found in tasks_definition for project "
                f"'{self.project_id}'. Run 'orch atomize --apply' first."
            )

    # ---- milestones (Sprint F-3) ----------------------------------------

    def upsert_milestone(
        self,
        milestone_id: str,
        title: str,
        description: str | None = None,
        target_date: str | None = None,
    ) -> None:
        """Create or update a milestone for this project."""
        now = _utc_now_iso()
        with self._write() as conn:
            conn.execute(
                "INSERT INTO milestones "
                "(project_id, id, title, description, target_date, created_at) "
                "VALUES (?, ?, ?, ?, ?, ?) "
                "ON CONFLICT(project_id, id) DO UPDATE SET "
                "title=excluded.title, description=excluded.description, "
                "target_date=excluded.target_date",
                (self.project_id, milestone_id, title, description, target_date, now),
            )

    def get_milestones(self) -> list[dict]:
        """Return all milestones for this project with computed task progress."""
        conn = self._conn()
        try:
            cur = conn.execute(
                """
                SELECT
                    m.id,
                    m.title,
                    m.description,
                    m.target_date,
                    m.status,
                    m.created_at,
                    COUNT(td.task_id) AS total,
                    SUM(CASE WHEN tr.status = 'done' THEN 1 ELSE 0 END) AS done
                FROM milestones m
                LEFT JOIN tasks_definition td
                    ON td.project_id = m.project_id AND td.milestone_id = m.id
                LEFT JOIN tasks_runtime tr
                    ON tr.project_id = td.project_id AND tr.task_id = td.task_id
                WHERE m.project_id = ?
                GROUP BY m.id
                ORDER BY m.created_at
                """,
                (self.project_id,),
            )
            rows = cur.fetchall()
        finally:
            conn.close()

        result = []
        for row in rows:
            total = row["total"] or 0
            done = row["done"] or 0
            pct = int(done / total * 100) if total > 0 else 0
            result.append({
                "id": row["id"],
                "title": row["title"],
                "description": row["description"],
                "target_date": row["target_date"],
                "status": row["status"],
                "created_at": row["created_at"],
                "progress": {"total": total, "done": done, "pct": pct},
            })
        return result

    def set_task_milestone(self, task_id: str, milestone_id: str) -> None:
        """Assign a task to a milestone. Raises KeyError if either doesn't exist."""
        with self._write() as conn:
            cur = conn.execute(
                "SELECT id FROM milestones WHERE project_id = ? AND id = ?",
                (self.project_id, milestone_id),
            )
            if cur.fetchone() is None:
                raise KeyError(
                    f"milestone '{milestone_id}' not found for project '{self.project_id}'"
                )
            now = _utc_now_iso()
            rowcount = conn.execute(
                "UPDATE tasks_definition SET milestone_id = ?, updated_at = ? "
                "WHERE project_id = ? AND task_id = ?",
                (milestone_id, now, self.project_id, task_id),
            ).rowcount
        if rowcount == 0:
            raise KeyError(
                f"task '{task_id}' not found in tasks_definition for project '{self.project_id}'"
            )

    def complete_milestone(self, milestone_id: str) -> None:
        """Mark a milestone as completed. Raises KeyError if milestone not found."""
        with self._write() as conn:
            rowcount = conn.execute(
                "UPDATE milestones SET status = 'completed' "
                "WHERE project_id = ? AND id = ?",
                (self.project_id, milestone_id),
            ).rowcount
        if rowcount == 0:
            raise KeyError(
                f"milestone '{milestone_id}' not found for project '{self.project_id}'"
            )

    # ---- task status ---------------------------------------------------

    def get_task_status(self, task_id: str) -> Status | None:
        conn = self._conn()
        try:
            cur = conn.execute(
                "SELECT status FROM tasks_runtime "
                "WHERE project_id = ? AND task_id = ?",
                (self.project_id, task_id),
            )
            row = cur.fetchone()
            return row["status"] if row else None
        finally:
            conn.close()

    def get_all_task_status(self) -> dict[str, Status]:
        conn = self._conn()
        try:
            cur = conn.execute(
                "SELECT task_id, status FROM tasks_runtime WHERE project_id = ?",
                (self.project_id,),
            )
            return {row["task_id"]: row["status"] for row in cur.fetchall()}
        finally:
            conn.close()

    def set_task_status(
        self,
        task_id: str,
        status: Status,
        author: str,
        note: str,
        ts: str,
    ) -> None:
        if status not in _ALLOWED_STATUSES:
            raise ValueError(f"illegal status {status!r}")
        # Fetch current + validate transition.
        with self._write() as conn:
            cur = conn.execute(
                "SELECT status, comments_json FROM tasks_runtime "
                "WHERE project_id = ? AND task_id = ?",
                (self.project_id, task_id),
            )
            row = cur.fetchone()
            if row is None:
                raise KeyError(task_id)
            current = row["status"]
            allowed = _STATUS_TRANSITIONS.get(current, frozenset())
            if status not in allowed:
                raise ValueError(
                    f"illegal transition {current!r} -> {status!r} for {task_id!r}"
                )
            comments = json.loads(row["comments_json"] or "[]")
            comments.append({"author": author, "body": note or status, "at": ts})
            started = None
            finished = None
            if status == "in-progress":
                started = ts
            if status == "done":
                finished = ts
            conn.execute(
                "UPDATE tasks_runtime SET status = ?, "
                "comments_json = ?, updated_at = ?, "
                "started_at = COALESCE(?, started_at), "
                "finished_at = COALESCE(?, finished_at) "
                "WHERE project_id = ? AND task_id = ?",
                (
                    status,
                    json.dumps(comments),
                    ts,
                    started,
                    finished,
                    self.project_id,
                    task_id,
                ),
            )

    # ---- runs ----------------------------------------------------------

    def create_run(self, run_id: str, mode: str, parent_pid: int = 0) -> RunState:
        now = _utc_now_iso()
        with self._write() as conn:
            # Ensure the project row exists (safe fallback if bootstrap
            # wasn't called upstream).
            conn.execute(
                "INSERT OR IGNORE INTO projects "
                "(project_id, project_root, created_at, schema_version) "
                "VALUES (?, ?, ?, 1)",
                (
                    self.project_id,
                    str(self.project_root) if self.project_root else "",
                    now,
                ),
            )
            conn.execute(
                "INSERT INTO runs "
                "(run_id, project_id, started_at, updated_at, mode, parent_pid, "
                " status, completed_json, blocked_json, deferred_json) "
                "VALUES (?, ?, ?, ?, ?, ?, 'live', '[]', '[]', '[]')",
                (run_id, self.project_id, now, now, mode, int(parent_pid or 0)),
            )
        return RunState(  # type: ignore[arg-type]
            run_id=run_id,
            started_at=now,
            mode=mode,
            parent_pid=int(parent_pid or 0),
        )

    def load_run(self, run_id: str) -> RunState:
        conn = self._conn()
        try:
            cur = conn.execute(
                "SELECT * FROM runs WHERE run_id = ? AND project_id = ?",
                (run_id, self.project_id),
            )
            row = cur.fetchone()
            if row is None:
                raise KeyError(run_id)
            disp_cur = conn.execute(
                "SELECT * FROM dispatches WHERE run_id = ? AND status = 'in_flight'",
                (run_id,),
            )
            in_flight = {}
            for d in disp_cur.fetchall():
                in_flight[d["task_id"]] = Dispatch(
                    task_id=d["task_id"],
                    backend=d["backend"],  # type: ignore[arg-type]
                    pid=d["pid"],
                    session_id=d["session_id"] or "",
                    started_at=d["started_at"],
                    prompt_path=d["prompt_path"] or "",
                    log_path=d["log_path"] or "",
                    output_path=d["output_path"] or "",
                    attempt=d["attempt"] or 1,
                )
            # Sprint A / Issue #12: parent_pid may be NULL for legacy rows
            # written before the column was populated. Default to 0.
            try:
                pp = row["parent_pid"]
            except (IndexError, KeyError):
                pp = 0
            return RunState(
                run_id=row["run_id"],
                started_at=row["started_at"],
                mode=row["mode"],  # type: ignore[arg-type]
                in_flight=in_flight,
                completed=list(json.loads(row["completed_json"] or "[]")),
                blocked=list(json.loads(row["blocked_json"] or "[]")),
                deferred=list(json.loads(row["deferred_json"] or "[]")),
                parent_pid=int(pp or 0),
            )
        finally:
            conn.close()

    def save_run(self, state: RunState) -> None:
        now = _utc_now_iso()
        status = "live" if state.in_flight else "done"
        with self._write() as conn:
            conn.execute(
                "UPDATE runs SET updated_at = ?, mode = ?, status = ?, "
                "parent_pid = ?, completed_json = ?, blocked_json = ?, "
                "deferred_json = ? "
                "WHERE run_id = ? AND project_id = ?",
                (
                    now,
                    state.mode,
                    status,
                    int(getattr(state, "parent_pid", 0) or 0),
                    json.dumps(state.completed),
                    json.dumps(state.blocked),
                    json.dumps(state.deferred),
                    state.run_id,
                    self.project_id,
                ),
            )
            # Sync dispatches: replace the in_flight set atomically.
            conn.execute(
                "DELETE FROM dispatches WHERE run_id = ? AND status = 'in_flight'",
                (state.run_id,),
            )
            for d in state.in_flight.values():
                conn.execute(
                    "INSERT OR REPLACE INTO dispatches "
                    "(run_id, task_id, project_id, backend, pid, session_id, "
                    " started_at, prompt_path, log_path, output_path, attempt, "
                    " status) "
                    "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'in_flight')",
                    (
                        state.run_id,
                        d.task_id,
                        self.project_id,
                        d.backend,
                        d.pid,
                        d.session_id,
                        d.started_at,
                        d.prompt_path,
                        d.log_path,
                        d.output_path,
                        d.attempt,
                    ),
                )

    def list_runs(self) -> list[dict[str, Any]]:
        conn = self._conn()
        try:
            cur = conn.execute(
                "SELECT run_id, started_at, updated_at, mode, status, "
                "parent_pid, completed_json, blocked_json, deferred_json "
                "FROM runs WHERE project_id = ? "
                "ORDER BY started_at DESC",
                (self.project_id,),
            )
            out: list[dict[str, Any]] = []
            for row in cur.fetchall():
                completed = json.loads(row["completed_json"] or "[]")
                blocked = json.loads(row["blocked_json"] or "[]")
                deferred = json.loads(row["deferred_json"] or "[]")
                in_flight_cur = conn.execute(
                    "SELECT COUNT(*) AS n FROM dispatches "
                    "WHERE run_id = ? AND status = 'in_flight'",
                    (row["run_id"],),
                )
                in_flight = in_flight_cur.fetchone()["n"]
                try:
                    pp = row["parent_pid"]
                except (IndexError, KeyError):
                    pp = 0
                out.append({
                    "run_id": row["run_id"],
                    "started_at": row["started_at"],
                    "updated_at": row["updated_at"],
                    "mode": row["mode"],
                    "status": row["status"],
                    "parent_pid": int(pp or 0),
                    "in_flight_count": int(in_flight),
                    "completed_count": len(completed),
                    "blocked_count": len(blocked),
                    "deferred_count": len(deferred),
                    "run_file": f"run-{row['run_id']}.json",
                    "events_file": f"events-{row['run_id']}.jsonl",
                })
            return out
        finally:
            conn.close()

    # ---- dispatches ----------------------------------------------------

    def add_dispatch(self, run_id: str, dispatch: Dispatch) -> None:
        with self._write() as conn:
            conn.execute(
                "INSERT OR REPLACE INTO dispatches "
                "(run_id, task_id, project_id, backend, pid, session_id, "
                " started_at, prompt_path, log_path, output_path, attempt, status) "
                "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'in_flight')",
                (
                    run_id,
                    dispatch.task_id,
                    self.project_id,
                    dispatch.backend,
                    dispatch.pid,
                    dispatch.session_id,
                    dispatch.started_at,
                    dispatch.prompt_path,
                    dispatch.log_path,
                    dispatch.output_path,
                    dispatch.attempt,
                ),
            )

    def remove_dispatch(self, run_id: str, task_id: str) -> None:
        with self._write() as conn:
            conn.execute(
                "DELETE FROM dispatches WHERE run_id = ? AND task_id = ?",
                (run_id, task_id),
            )

    def iter_in_flight(self, run_id: str) -> Iterator[Dispatch]:
        conn = self._conn()
        try:
            cur = conn.execute(
                "SELECT * FROM dispatches WHERE run_id = ? AND status = 'in_flight'",
                (run_id,),
            )
            for row in cur.fetchall():
                yield Dispatch(
                    task_id=row["task_id"],
                    backend=row["backend"],  # type: ignore[arg-type]
                    pid=row["pid"],
                    session_id=row["session_id"] or "",
                    started_at=row["started_at"],
                    prompt_path=row["prompt_path"] or "",
                    log_path=row["log_path"] or "",
                    output_path=row["output_path"] or "",
                    attempt=row["attempt"] or 1,
                )
        finally:
            conn.close()

    # ---- events + spend ------------------------------------------------

    def append_event(self, run_id: str, entry: EventEntry) -> None:
        pid_hint = (entry.extra or {}).get("pid", "")
        dedup = hashlib.sha256(
            f"{self.project_id}|{entry.ts}|{entry.task_id}|{entry.event_type}|"
            f"{run_id}|{pid_hint}".encode("utf-8")
        ).hexdigest()
        with self._write() as conn:
            conn.execute(
                "INSERT OR IGNORE INTO events "
                "(project_id, run_id, event_type, task_id, backend, ts, "
                " extra_json, dedup_hash) "
                "VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
                (
                    self.project_id,
                    run_id,
                    entry.event_type,
                    entry.task_id,
                    entry.backend or "",
                    entry.ts,
                    json.dumps(entry.extra or {}),
                    dedup,
                ),
            )

    def iter_events(
        self,
        run_id: str | None = None,
        since_id: int | None = None,
        task_id: str | None = None,
        limit: int | None = None,
    ) -> Iterator[dict[str, Any]]:
        """Yield event rows from the sqlite `events` table.

        Adds Sprint C's `task_id` filter and `limit` cap. When `limit` is
        set we sort DESC + LIMIT + reverse in Python so callers still see
        chronological order — matches file-backend behavior.
        """
        conn = self._conn()
        try:
            where = ["project_id = ?"]
            params: list[Any] = [self.project_id]
            if run_id is not None:
                where.append("run_id = ?")
                params.append(run_id)
            if since_id is not None:
                where.append("id > ?")
                params.append(since_id)
            if task_id is not None:
                where.append("task_id = ?")
                params.append(task_id)
            if limit is not None and limit > 0:
                sql = (
                    "SELECT id, project_id, run_id, event_type, task_id, "
                    "backend, ts, extra_json FROM events "
                    f"WHERE {' AND '.join(where)} "
                    "ORDER BY id DESC LIMIT ?"
                )
                params.append(int(limit))
                rows = list(conn.execute(sql, tuple(params)).fetchall())
                rows.reverse()  # oldest → newest for the caller
            else:
                sql = (
                    "SELECT id, project_id, run_id, event_type, task_id, "
                    "backend, ts, extra_json FROM events "
                    f"WHERE {' AND '.join(where)} "
                    "ORDER BY id ASC"
                )
                rows = conn.execute(sql, tuple(params)).fetchall()
            for row in rows:
                yield {
                    "id": row["id"],
                    "event_type": row["event_type"],
                    "task_id": row["task_id"],
                    "backend": row["backend"] or "",
                    "ts": row["ts"],
                    "extra": json.loads(row["extra_json"] or "{}"),
                    "project_id": row["project_id"],
                    "run_id": row["run_id"],
                }
        finally:
            conn.close()

    def get_task_last_events(
        self,
        task_ids: Iterable[str] | None = None,
    ) -> dict[str, dict[str, Any]]:
        """Latest event per task_id via one indexed self-join.

        The correlated subquery uses the (project_id, task_id) index we get
        for free through the events primary key + our secondary lookups.
        """
        wl: list[str] | None
        if task_ids is None:
            wl = None
        else:
            wl = [str(t) for t in task_ids]
            if not wl:
                return {}

        params: list[Any] = [self.project_id]
        extra_where = ""
        if wl is not None:
            placeholders = ",".join("?" * len(wl))
            extra_where = f" AND e.task_id IN ({placeholders})"
            params.extend(wl)

        sql = (
            "SELECT e.id, e.project_id, e.run_id, e.event_type, e.task_id, "
            "e.backend, e.ts, e.extra_json FROM events e "
            "JOIN ("
            "  SELECT task_id, MAX(id) AS max_id FROM events "
            "  WHERE project_id = ? "
            f"  {'AND task_id IN (' + ','.join('?' * len(wl)) + ')' if wl else ''} "
            "  GROUP BY task_id"
            ") latest "
            "  ON latest.task_id = e.task_id AND latest.max_id = e.id "
            f"WHERE e.project_id = ?{extra_where}"
        )
        # Params: inner (project_id [+ wl]), outer (project_id [+ wl]).
        inner_params: list[Any] = [self.project_id]
        if wl:
            inner_params.extend(wl)
        outer_params: list[Any] = [self.project_id]
        if wl:
            outer_params.extend(wl)
        all_params = tuple(inner_params + outer_params)

        conn = self._conn()
        try:
            out: dict[str, dict[str, Any]] = {}
            for row in conn.execute(sql, all_params).fetchall():
                tid = row["task_id"]
                out[tid] = {
                    "id": row["id"],
                    "event_type": row["event_type"],
                    "task_id": tid,
                    "backend": row["backend"] or "",
                    "ts": row["ts"],
                    "extra": json.loads(row["extra_json"] or "{}"),
                    "project_id": row["project_id"],
                    "run_id": row["run_id"],
                }
            return out
        finally:
            conn.close()

    def append_spend(self, entry: SpendEntry) -> None:
        pid_for_dedup = entry.project_id or self.project_id
        dedup = hashlib.sha256(
            f"{pid_for_dedup}|{entry.ts}|{entry.task_id}|{entry.backend}|"
            f"{entry.model}|{entry.cost_usd}|{entry.duration_s}".encode("utf-8")
        ).hexdigest()
        with self._write() as conn:
            conn.execute(
                "INSERT OR IGNORE INTO spend "
                "(project_id, ts, task_id, backend, model, tokens_in, "
                " tokens_out, cost_usd, duration_s, estimated, dedup_hash) "
                "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
                (
                    pid_for_dedup,
                    entry.ts,
                    entry.task_id,
                    entry.backend,
                    entry.model,
                    entry.tokens_in,
                    entry.tokens_out,
                    entry.cost_usd,
                    entry.duration_s,
                    0,
                    dedup,
                ),
            )

    def iter_spend(
        self,
        since: str | None = None,
        until: str | None = None,
    ) -> Iterator[dict[str, Any]]:
        conn = self._conn()
        try:
            where = ["project_id = ?"]
            params: list[Any] = [self.project_id]
            if since is not None:
                where.append("ts >= ?")
                params.append(since)
            if until is not None:
                where.append("ts <= ?")
                params.append(until)
            sql = (
                "SELECT ts, task_id, backend, model, tokens_in, tokens_out, "
                "cost_usd, duration_s, project_id "
                "FROM spend "
                f"WHERE {' AND '.join(where)} "
                "ORDER BY ts ASC"
            )
            cur = conn.execute(sql, tuple(params))
            for row in cur.fetchall():
                yield {
                    "ts": row["ts"],
                    "task_id": row["task_id"],
                    "backend": row["backend"],
                    "model": row["model"],
                    "tokens_in": row["tokens_in"],
                    "tokens_out": row["tokens_out"],
                    "cost_usd": row["cost_usd"],
                    "duration_s": row["duration_s"],
                    "project_id": row["project_id"],
                }
        finally:
            conn.close()

    def iter_all_spend(self) -> Iterator[dict[str, Any]]:
        yield from self.iter_spend()

    # ---- reconciliation ------------------------------------------------

    def reconcile_in_flight(self) -> dict[str, Any]:
        """Scan every project run for orphaned dispatches (dead PIDs).

        Same semantics as the file backend: any dispatch whose PID is dead
        gets removed and appended to `blocked_json`. Emits a synthetic
        `reconciled` event per orphan. Sprint A / Issue #7 parity: ALSO
        reverts the corresponding `tasks_runtime` row from `in-progress`
        back to `todo` so `queue.ready()` picks it up on the next tick.
        Never raises past its own boundary.
        """
        result: dict[str, Any] = {"reconciled": [], "checked": 0, "reset": []}
        try:
            conn = self._conn()
            try:
                cur = conn.execute(
                    "SELECT run_id, task_id, pid, backend FROM dispatches "
                    "WHERE project_id = ? AND status = 'in_flight'",
                    (self.project_id,),
                )
                orphans: list[dict[str, Any]] = []
                for row in cur.fetchall():
                    result["checked"] += 1
                    pid = row["pid"]
                    if not isinstance(pid, int) or pid <= 0:
                        continue
                    try:
                        os.kill(pid, 0)
                        alive = True
                    except ProcessLookupError:
                        alive = False
                    except PermissionError:
                        alive = True
                    except OSError:
                        continue
                    if not alive:
                        orphans.append({
                            "run_id": row["run_id"],
                            "task_id": row["task_id"],
                            "pid": pid,
                            "backend": row["backend"] or "",
                        })
            finally:
                conn.close()

            for o in orphans:
                # Remove dispatch + mutate run blocked list + emit event.
                run_id = o["run_id"]
                tid = o["task_id"]
                with self._write() as w:
                    w.execute(
                        "DELETE FROM dispatches WHERE run_id = ? AND task_id = ?",
                        (run_id, tid),
                    )
                    row = w.execute(
                        "SELECT blocked_json FROM runs WHERE run_id = ?",
                        (run_id,),
                    ).fetchone()
                    if row is not None:
                        blocked = json.loads(row["blocked_json"] or "[]")
                        if tid not in blocked:
                            blocked.append(tid)
                        w.execute(
                            "UPDATE runs SET blocked_json = ?, updated_at = ? "
                            "WHERE run_id = ?",
                            (json.dumps(blocked), _utc_now_iso(), run_id),
                        )
                    dedup = hashlib.sha256(
                        f"{self.project_id}|{_utc_now_iso()}|{tid}|"
                        f"reconciled|{run_id}|{o['pid']}".encode("utf-8")
                    ).hexdigest()
                    w.execute(
                        "INSERT OR IGNORE INTO events "
                        "(project_id, run_id, event_type, task_id, backend, "
                        " ts, extra_json, dedup_hash) "
                        "VALUES (?, ?, 'reconciled', ?, ?, ?, ?, ?)",
                        (
                            self.project_id,
                            run_id,
                            tid,
                            o["backend"],
                            _utc_now_iso(),
                            json.dumps({
                                "reason": "orphaned_process",
                                "original_pid": o["pid"],
                            }),
                            dedup,
                        ),
                    )
                result["reconciled"].append({
                    "run_id": run_id,
                    "task_id": tid,
                    "pid": o["pid"],
                })
                # Sprint A / Issue #7: revert stuck tasks_runtime row too.
                try:
                    ts = _utc_now_iso()
                    with self._write() as w:
                        cur = w.execute(
                            "SELECT status, comments_json FROM tasks_runtime "
                            "WHERE project_id = ? AND task_id = ?",
                            (self.project_id, tid),
                        )
                        trow = cur.fetchone()
                        if trow is not None and trow["status"] == "in-progress":
                            comments = json.loads(trow["comments_json"] or "[]")
                            comments.append({
                                "author": "orch-reconcile",
                                "body": "reset from in-progress (orphaned)",
                                "at": ts,
                            })
                            w.execute(
                                "UPDATE tasks_runtime SET status = 'todo', "
                                "comments_json = ?, updated_at = ?, "
                                "started_at = NULL, finished_at = NULL, "
                                "in_flight_pid = NULL, in_flight_run_id = NULL "
                                "WHERE project_id = ? AND task_id = ?",
                                (json.dumps(comments), ts, self.project_id, tid),
                            )
                            result["reset"].append(tid)
                except Exception as exc:  # noqa: BLE001
                    log.warning(
                        "sqlite reconcile_in_flight: reset failed for %s: %s",
                        tid,
                        exc,
                    )
            return result
        except Exception as exc:  # noqa: BLE001
            log.warning("sqlite reconcile_in_flight failed: %s", exc)
            return result

    def reset_task(self, task_id: str, author: str, note: str, ts: str) -> None:
        """Force a task back to `todo`. Used by the reset script.

        Bypasses the standard transition matrix (any current status → todo).
        """
        with self._write() as conn:
            cur = conn.execute(
                "SELECT comments_json FROM tasks_runtime "
                "WHERE project_id = ? AND task_id = ?",
                (self.project_id, task_id),
            )
            row = cur.fetchone()
            if row is None:
                raise KeyError(task_id)
            comments = json.loads(row["comments_json"] or "[]")
            comments.append({"author": author, "body": note or "reset", "at": ts})
            conn.execute(
                "UPDATE tasks_runtime SET status = 'todo', "
                "comments_json = ?, updated_at = ?, "
                "started_at = NULL, finished_at = NULL, "
                "in_flight_pid = NULL, in_flight_run_id = NULL "
                "WHERE project_id = ? AND task_id = ?",
                (json.dumps(comments), ts, self.project_id, task_id),
            )

    def clear_in_flight_for_run(self, run_id: str) -> list[str]:
        with self._write() as conn:
            cur = conn.execute(
                "SELECT task_id FROM dispatches "
                "WHERE run_id = ? AND status = 'in_flight'",
                (run_id,),
            )
            cleared = [r["task_id"] for r in cur.fetchall()]
            conn.execute(
                "DELETE FROM dispatches WHERE run_id = ?",
                (run_id,),
            )
        return cleared

    # ---- findings (Sprint E-1 / #17) -----------------------------------

    def _row_to_finding(self, row: sqlite3.Row) -> Finding:
        return Finding(
            id=row["id"],
            created_at=row["created_at"],
            type=row["type"],  # type: ignore[arg-type]
            about=row["about"],  # type: ignore[arg-type]
            summary=row["summary"],
            evidence=row["evidence"] or "",
            confidence=row["confidence"],  # type: ignore[arg-type]
            status=row["status"],  # type: ignore[arg-type]
            published_url=row["published_url"],
            duplicate_of=row["duplicate_of"],
            dedup_hash=row["dedup_hash"] or "",
            project_id=row["project_id"] or "",
            author=row["author"] or "agent",
            dismissed_reason=row["dismissed_reason"],
        )

    def append_finding(self, finding: Finding) -> None:
        with self._write() as conn:
            try:
                conn.execute(
                    "INSERT INTO findings "
                    "(id, project_id, created_at, type, about, summary, "
                    " evidence, confidence, status, published_url, "
                    " duplicate_of, dedup_hash, author, dismissed_reason) "
                    "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
                    (
                        finding.id,
                        finding.project_id or self.project_id,
                        finding.created_at,
                        finding.type,
                        finding.about,
                        finding.summary,
                        finding.evidence or "",
                        finding.confidence,
                        finding.status,
                        finding.published_url,
                        finding.duplicate_of,
                        finding.dedup_hash,
                        finding.author or "agent",
                        finding.dismissed_reason,
                    ),
                )
            except sqlite3.IntegrityError as exc:
                # UNIQUE(dedup_hash) or PK collision — raise as ValueError so
                # callers can present a clean "already captured" message.
                raise ValueError(f"finding integrity error: {exc}") from exc

    def iter_findings(
        self,
        status: str | None = None,
        about: str | None = None,
    ) -> Iterator[Finding]:
        conn = self._conn()
        try:
            where = ["project_id = ?"]
            params: list[Any] = [self.project_id]
            if status is not None:
                where.append("status = ?")
                params.append(status)
            if about is not None:
                where.append("about = ?")
                params.append(about)
            sql = (
                "SELECT * FROM findings "
                f"WHERE {' AND '.join(where)} "
                "ORDER BY created_at ASC"
            )
            for row in conn.execute(sql, tuple(params)).fetchall():
                yield self._row_to_finding(row)
        finally:
            conn.close()

    def get_finding(self, finding_id: str) -> Finding | None:
        conn = self._conn()
        try:
            cur = conn.execute(
                "SELECT * FROM findings WHERE project_id = ? AND id = ?",
                (self.project_id, finding_id),
            )
            row = cur.fetchone()
            return self._row_to_finding(row) if row else None
        finally:
            conn.close()

    def update_finding(self, finding_id: str, **updates: Any) -> None:
        allowed = {
            "status",
            "published_url",
            "duplicate_of",
            "dismissed_reason",
            "summary",
            "evidence",
            "confidence",
            "author",
        }
        clean = {k: v for k, v in updates.items() if k in allowed}
        if not clean:
            # Still validate the row exists so callers get consistent KeyError.
            if self.get_finding(finding_id) is None:
                raise KeyError(finding_id)
            return
        assignments = ", ".join(f"{k} = ?" for k in clean)
        params: list[Any] = list(clean.values())
        params.extend([self.project_id, finding_id])
        with self._write() as conn:
            cur = conn.execute(
                f"UPDATE findings SET {assignments} "
                "WHERE project_id = ? AND id = ?",
                tuple(params),
            )
            if cur.rowcount == 0:
                raise KeyError(finding_id)

    def find_finding_by_dedup_hash(self, dedup_hash: str) -> Finding | None:
        conn = self._conn()
        try:
            cur = conn.execute(
                "SELECT * FROM findings WHERE project_id = ? AND dedup_hash = ?",
                (self.project_id, dedup_hash),
            )
            row = cur.fetchone()
            return self._row_to_finding(row) if row else None
        finally:
            conn.close()

    # ---- VCS / CI tracking (Sprint F-4) ------------------------------------

    def set_task_pr(self, task_id: str, pr_url: str) -> None:
        """Store PR URL and initialise ci_status to 'pending'."""
        with self._write() as conn:
            rowcount = conn.execute(
                "UPDATE tasks_runtime SET pr_url = ?, ci_status = 'pending', "
                "updated_at = ? WHERE project_id = ? AND task_id = ?",
                (pr_url, _utc_now_iso(), self.project_id, task_id),
            ).rowcount
        if rowcount == 0:
            raise KeyError(task_id)

    def get_tasks_with_pending_ci(self) -> list[dict]:
        """Return tasks_runtime rows where pr_url IS NOT NULL AND ci_status = 'pending'."""
        conn = self._conn()
        try:
            cur = conn.execute(
                "SELECT task_id, pr_url, ci_status, ci_attempts "
                "FROM tasks_runtime "
                "WHERE project_id = ? AND pr_url IS NOT NULL AND ci_status = 'pending'",
                (self.project_id,),
            )
            return [dict(row) for row in cur.fetchall()]
        finally:
            conn.close()

    def set_task_ci_status(self, task_id: str, status: str) -> None:
        """Update ci_status. Valid values: pending | success | failure | skipped."""
        if status not in _ALLOWED_CI_STATUSES:
            raise ValueError(f"illegal ci_status {status!r}")
        with self._write() as conn:
            rowcount = conn.execute(
                "UPDATE tasks_runtime SET ci_status = ?, updated_at = ? "
                "WHERE project_id = ? AND task_id = ?",
                (status, _utc_now_iso(), self.project_id, task_id),
            ).rowcount
        if rowcount == 0:
            raise KeyError(task_id)

    def increment_ci_attempts(self, task_id: str) -> None:
        """Increment ci_attempts by 1."""
        with self._write() as conn:
            rowcount = conn.execute(
                "UPDATE tasks_runtime SET ci_attempts = ci_attempts + 1, "
                "updated_at = ? WHERE project_id = ? AND task_id = ?",
                (_utc_now_iso(), self.project_id, task_id),
            ).rowcount
        if rowcount == 0:
            raise KeyError(task_id)

    # ---- introspection (used by migrate + tests) -----------------------

    def schema_version(self) -> int:
        conn = self._conn()
        try:
            cur = conn.execute("PRAGMA user_version")
            row = cur.fetchone()
            return int(row[0]) if row else 0
        finally:
            conn.close()
