"""`orch migrate` — one-shot file → sqlite migrator.

Design (see docs/design/sprint-b-sqlite-backend.md §5):
    - backup: copy state/ into `<backup-dir>/<ts>/` before writing anything.
    - create tables: `SqliteBackend` ctor runs the schema migration.
    - import projects: single row (project_id + project_root + created_at).
    - import tasks_runtime: seeded from `tasks.json` on first run only;
      subsequent runs skip via INSERT OR IGNORE.
    - import runs + dispatches: from every `state/run-*.json`.
    - import events: batched from every `state/events-*.jsonl`.
    - import spend: batched from every `state/spend-*.jsonl`.
    - set `projects.migrated_at`.
    - Entire import runs in a single transaction; dedup_hash makes re-runs
      idempotent (UNIQUE constraint blocks duplicate rows).

Flags:
    --project-root PATH  target project layout
    --project-id ID      backend tenant id (default: paths.project_id)
    --backup-dir DIR     default `<state_dir>/backups/`
    --dry-run            log what WOULD be imported; touch no rows
    --force              skip the "migrated_at already set" guard
    --rollback           restore the backup + drop the tenant's rows
    --from BACKUP        which backup dir to restore (default: newest)
"""

from __future__ import annotations

import argparse
import hashlib
import json
import logging
import shutil
import sqlite3
import sys
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Iterable

from .paths import resolve_project_paths
from .state import load_tasks
from .state.sqlite_backend import SqliteBackend

log = logging.getLogger(__name__)


_EVENTS_BATCH = 500
_SPEND_BATCH = 500


def _utc_now_iso() -> str:
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def _event_hash(project_id: str, ts: str, task_id: str, event_type: str, run_id: str) -> str:
    return hashlib.sha256(
        f"{project_id}|{ts}|{task_id}|{event_type}|{run_id}".encode("utf-8")
    ).hexdigest()


def _spend_hash(
    project_id: str,
    ts: str,
    task_id: str,
    backend: str,
    model: str,
    cost_usd: float,
    duration_s: float,
) -> str:
    return hashlib.sha256(
        f"{project_id}|{ts}|{task_id}|{backend}|{model}|{cost_usd}|{duration_s}".encode(
            "utf-8"
        )
    ).hexdigest()


# ---- Backup / restore ---------------------------------------------------


def _make_backup(state_dir: Path, backup_root: Path) -> Path:
    """Copy `state_dir` into `<backup_root>/<ts>-<original_name>/`.

    Returns the created backup path. Raises FileNotFoundError if state_dir
    doesn't exist.
    """
    if not state_dir.exists():
        raise FileNotFoundError(f"state_dir does not exist: {state_dir}")
    ts = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    backup_root.mkdir(parents=True, exist_ok=True)
    base = f"{ts}-{state_dir.name}"
    dest = backup_root / base
    # Same-second retries land side-by-side (dest-1, dest-2…).
    seq = 1
    while dest.exists():
        dest = backup_root / f"{base}-{seq}"
        seq += 1
    # `copytree` preserves symlinks + mtimes. We exclude orch.db itself
    # (a partial DB is unhelpful to restore over a good one) and any
    # directory named `backups` (defensive against recursive-copy loops
    # if the backup dir happens to sit inside state_dir).
    def _ignore(dir_str: str, entries: list[str]) -> list[str]:
        skip = set()
        for e in entries:
            full = Path(dir_str) / e
            if e == "orch.db" or e.endswith(".db-wal") or e.endswith(".db-shm"):
                skip.add(e)
            elif e in ("backups",) or (full.is_dir() and e.endswith("-backups")):
                skip.add(e)
            elif full.resolve() == backup_root.resolve():
                skip.add(e)
        return list(skip)

    shutil.copytree(state_dir, dest, ignore=_ignore, symlinks=True)
    return dest


def _restore_backup(backup_dir: Path, state_dir: Path) -> None:
    """Overwrite `state_dir` with the contents of `backup_dir`.

    The sqlite DB in `state_dir/orch.db` is NOT touched — callers must
    prune the tenant's rows separately if they want a clean rollback.
    """
    if not backup_dir.exists() or not backup_dir.is_dir():
        raise FileNotFoundError(f"backup dir not found: {backup_dir}")
    for item in backup_dir.iterdir():
        target = state_dir / item.name
        if target.exists():
            if target.is_dir() and not target.is_symlink():
                shutil.rmtree(target)
            else:
                target.unlink()
        if item.is_dir():
            shutil.copytree(item, target, symlinks=True)
        else:
            shutil.copy2(item, target)


def _newest_backup(backup_root: Path) -> Path | None:
    if not backup_root.exists():
        return None
    dirs = [p for p in backup_root.iterdir() if p.is_dir()]
    if not dirs:
        return None
    return sorted(dirs, key=lambda p: p.name, reverse=True)[0]


# ---- Import helpers -----------------------------------------------------


def _iter_jsonl_dicts(path: Path) -> Iterable[dict[str, Any]]:
    """Yield dict rows from a JSONL file, silently skipping bad lines.

    Emits a WARN log for the first 3 bad lines per file so operators can
    spot data-quality issues without drowning in noise.
    """
    warned = 0
    try:
        with path.open("r", encoding="utf-8") as fh:
            for lineno, line in enumerate(fh, start=1):
                line = line.strip()
                if not line:
                    continue
                try:
                    obj = json.loads(line)
                except (json.JSONDecodeError, ValueError):
                    if warned < 3:
                        log.warning(
                            "migrate: skipping malformed line %s:%d",
                            path.name,
                            lineno,
                        )
                    warned += 1
                    continue
                if isinstance(obj, dict):
                    yield obj
    except OSError as exc:
        log.warning("migrate: could not read %s: %s", path, exc)


def _import_runs(conn: sqlite3.Connection, state_dir: Path, project_id: str) -> tuple[int, int]:
    """Return (runs_imported, dispatches_imported)."""
    runs = 0
    dispatches = 0
    for path in sorted(state_dir.glob("run-*.json")):
        try:
            with path.open("r", encoding="utf-8") as fh:
                raw = json.load(fh)
        except (OSError, json.JSONDecodeError):
            log.warning("migrate: skipping malformed run file %s", path.name)
            continue
        run_id = raw.get("run_id")
        if not run_id:
            continue
        completed = raw.get("completed") or []
        blocked = raw.get("blocked") or []
        deferred = raw.get("deferred") or []
        in_flight = raw.get("in_flight") or {}
        status = "live" if in_flight else "done"
        started_at = raw.get("started_at") or _utc_now_iso()
        mode = raw.get("mode") or "auto"
        conn.execute(
            "INSERT OR IGNORE INTO runs "
            "(run_id, project_id, started_at, updated_at, mode, status, "
            " completed_json, blocked_json, deferred_json) "
            "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
            (
                run_id,
                project_id,
                started_at,
                started_at,
                mode,
                status,
                json.dumps(completed),
                json.dumps(blocked),
                json.dumps(deferred),
            ),
        )
        runs += 1
        for tid, d in in_flight.items():
            if not isinstance(d, dict):
                continue
            conn.execute(
                "INSERT OR IGNORE INTO dispatches "
                "(run_id, task_id, project_id, backend, pid, session_id, "
                " started_at, prompt_path, log_path, output_path, attempt, status) "
                "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'in_flight')",
                (
                    run_id,
                    tid,
                    project_id,
                    d.get("backend", "") or "",
                    int(d.get("pid") or 0),
                    d.get("session_id", "") or "",
                    d.get("started_at", started_at) or started_at,
                    d.get("prompt_path", "") or "",
                    d.get("log_path", "") or "",
                    d.get("output_path", "") or "",
                    int(d.get("attempt") or 1),
                ),
            )
            dispatches += 1
    return runs, dispatches


def _import_events(conn: sqlite3.Connection, state_dir: Path, project_id: str) -> int:
    imported = 0
    for path in sorted(state_dir.glob("events-*.jsonl")):
        run_id = path.stem.replace("events-", "", 1)
        batch: list[tuple] = []
        for row in _iter_jsonl_dicts(path):
            ts = row.get("ts") or _utc_now_iso()
            task_id = row.get("task_id") or "-"
            event_type = row.get("event_type") or "dispatch"
            backend = row.get("backend") or ""
            extra = row.get("extra") or {}
            row_pid = row.get("project_id") or project_id
            dedup = _event_hash(row_pid, ts, task_id, event_type, run_id)
            batch.append((
                row_pid,
                run_id,
                event_type,
                task_id,
                backend,
                ts,
                json.dumps(extra),
                dedup,
            ))
            if len(batch) >= _EVENTS_BATCH:
                conn.executemany(
                    "INSERT OR IGNORE INTO events "
                    "(project_id, run_id, event_type, task_id, backend, ts, "
                    " extra_json, dedup_hash) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
                    batch,
                )
                imported += len(batch)
                batch.clear()
        if batch:
            conn.executemany(
                "INSERT OR IGNORE INTO events "
                "(project_id, run_id, event_type, task_id, backend, ts, "
                " extra_json, dedup_hash) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
                batch,
            )
            imported += len(batch)
    return imported


def _import_spend(conn: sqlite3.Connection, state_dir: Path, project_id: str) -> int:
    imported = 0
    for path in sorted(state_dir.glob("spend-*.jsonl")):
        batch: list[tuple] = []
        for row in _iter_jsonl_dicts(path):
            ts = row.get("ts") or _utc_now_iso()
            task_id = row.get("task_id") or "-"
            backend = row.get("backend") or ""
            model = row.get("model") or ""
            cost_usd = float(row.get("cost_usd") or 0.0)
            duration_s = float(row.get("duration_s") or 0.0)
            row_pid = row.get("project_id") or project_id
            dedup = _spend_hash(row_pid, ts, task_id, backend, model, cost_usd, duration_s)
            batch.append((
                row_pid,
                ts,
                task_id,
                backend,
                model,
                int(row.get("tokens_in") or 0),
                int(row.get("tokens_out") or 0),
                cost_usd,
                duration_s,
                0,
                dedup,
            ))
            if len(batch) >= _SPEND_BATCH:
                conn.executemany(
                    "INSERT OR IGNORE INTO spend "
                    "(project_id, ts, task_id, backend, model, tokens_in, "
                    " tokens_out, cost_usd, duration_s, estimated, dedup_hash) "
                    "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
                    batch,
                )
                imported += len(batch)
                batch.clear()
        if batch:
            conn.executemany(
                "INSERT OR IGNORE INTO spend "
                "(project_id, ts, task_id, backend, model, tokens_in, "
                " tokens_out, cost_usd, duration_s, estimated, dedup_hash) "
                "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
                batch,
            )
            imported += len(batch)
    return imported


def _import_tasks_runtime(
    conn: sqlite3.Connection,
    project_id: str,
    project_root: Path,
    tasks_json: Path,
) -> int:
    """Seed `tasks_runtime` from `tasks.json` on first import. Subsequent
    calls no-op via INSERT OR IGNORE.
    """
    try:
        tasks = load_tasks(tasks_json)
    except Exception as exc:  # noqa: BLE001
        log.warning("migrate: could not load tasks.json (%s); skipping", exc)
        return 0
    now = _utc_now_iso()
    imported = 0
    for t in tasks:
        conn.execute(
            "INSERT OR IGNORE INTO tasks_runtime "
            "(project_id, task_id, status, comments_json, updated_at) "
            "VALUES (?, ?, ?, ?, ?)",
            (
                project_id,
                t.id,
                t.status or "todo",
                json.dumps(list(t.comments or [])),
                now,
            ),
        )
        imported += 1
    return imported


# ---- Main entry point ---------------------------------------------------


def run_migrate(argv: list[str] | None = None) -> int:
    """`orch migrate` CLI. See module docstring for flag semantics."""
    p = argparse.ArgumentParser(
        prog="orch migrate",
        description="One-shot file → sqlite migrator with backup + rollback.",
    )
    p.add_argument("--project-root", default=None, metavar="PATH")
    p.add_argument("--project-id", default=None, metavar="ID")
    p.add_argument("--config", default=".orchestrator/config.yaml")
    p.add_argument("--backup-dir", default=None, metavar="DIR")
    p.add_argument("--sqlite-path", default=None, metavar="PATH",
                   help="Override the DB path (default: <state_dir>/orch.db)")
    p.add_argument("--dry-run", action="store_true",
                   help="Log what would be imported; no writes to the DB.")
    p.add_argument("--force", action="store_true",
                   help="Skip the 'migrated_at already set' guard.")
    p.add_argument("--rollback", action="store_true",
                   help="Restore the backup and drop the tenant's rows.")
    p.add_argument("--from", dest="from_backup", default=None, metavar="BACKUP",
                   help="Which backup dir to restore (default: newest).")
    args = p.parse_args(argv)

    paths = resolve_project_paths(
        project_root_arg=args.project_root,
        project_id_arg=args.project_id,
        config_arg=args.config,
    )
    project_id = paths.project_id
    state_dir = paths.state_dir
    # If the namespaced state_dir is empty but the legacy path (parent) has
    # state files, migrate FROM the legacy path so historical file-backend
    # projects can be imported without moving files first.
    if state_dir.exists():
        has_state_files = any(state_dir.glob("run-*.json")) or any(
            state_dir.glob("events-*.jsonl")
        )
    else:
        has_state_files = False
    if not has_state_files and state_dir.parent.exists():
        legacy = state_dir.parent
        if any(legacy.glob("run-*.json")) or any(legacy.glob("events-*.jsonl")):
            log.info(
                "migrate: state_dir %s empty; using legacy path %s as source",
                state_dir,
                legacy,
            )
            state_dir = legacy
    if args.sqlite_path:
        sqlite_path = Path(args.sqlite_path)
        if not sqlite_path.is_absolute():
            sqlite_path = state_dir / args.sqlite_path
    else:
        sqlite_path = state_dir / "orch.db"
    # Default backup dir lives OUTSIDE state_dir to avoid recursive-copy
    # loops during the backup step.
    backup_root = (
        Path(args.backup_dir).expanduser().resolve()
        if args.backup_dir
        else state_dir.parent / f"{state_dir.name}-backups"
    )

    if args.rollback:
        return _do_rollback(
            project_id=project_id,
            state_dir=state_dir,
            sqlite_path=sqlite_path,
            backup_root=backup_root,
            from_backup=args.from_backup,
        )

    if not paths.tasks_json.exists():
        print(f"migrate: tasks.json not found at {paths.tasks_json}", file=sys.stderr)
        return 1
    if not state_dir.exists():
        print(f"migrate: state dir not found at {state_dir}", file=sys.stderr)
        return 1

    # Bootstrap the DB (creates the schema on first run).
    be = SqliteBackend(
        db_path=sqlite_path,
        project_id=project_id,
        project_root=paths.project_root,
    )

    # Check migrated_at guard.
    conn_check = sqlite3.connect(str(sqlite_path))
    try:
        row = conn_check.execute(
            "SELECT migrated_at FROM projects WHERE project_id = ?",
            (project_id,),
        ).fetchone()
    finally:
        conn_check.close()
    if row and row[0] and not args.force and not args.dry_run:
        print(
            f"migrate: project {project_id!r} already migrated at {row[0]} "
            "(use --force to re-import)",
            file=sys.stderr,
        )
        return 1

    if args.dry_run:
        # Count what WOULD be imported without touching anything.
        run_files = list(state_dir.glob("run-*.json"))
        event_files = list(state_dir.glob("events-*.jsonl"))
        spend_files = list(state_dir.glob("spend-*.jsonl"))
        event_lines = sum(1 for f in event_files for _ in _iter_jsonl_dicts(f))
        spend_lines = sum(1 for f in spend_files for _ in _iter_jsonl_dicts(f))
        print("migrate (dry-run):")
        print(f"  project_id  : {project_id}")
        print(f"  state_dir   : {state_dir}")
        print(f"  sqlite_path : {sqlite_path}")
        print(f"  backup_dir  : {backup_root} (skipped in dry-run)")
        print(f"  runs        : {len(run_files)} files")
        print(f"  events      : {event_lines} rows across {len(event_files)} files")
        print(f"  spend       : {spend_lines} rows across {len(spend_files)} files")
        return 0

    # Backup first.
    try:
        backup_path = _make_backup(state_dir, backup_root)
        log.info("migrate: backup created at %s", backup_path)
    except FileNotFoundError as exc:
        print(f"migrate: backup failed: {exc}", file=sys.stderr)
        return 1

    # Import in a single transaction — either everything lands or nothing.
    conn = sqlite3.connect(str(sqlite_path))
    conn.execute("PRAGMA foreign_keys = ON")
    conn.execute("PRAGMA journal_mode = WAL")
    conn.execute("PRAGMA busy_timeout = 5000")
    try:
        conn.execute("BEGIN IMMEDIATE")
        conn.execute(
            "INSERT OR IGNORE INTO projects "
            "(project_id, project_root, created_at, schema_version) "
            "VALUES (?, ?, ?, 1)",
            (project_id, str(paths.project_root), _utc_now_iso()),
        )
        n_tasks = _import_tasks_runtime(
            conn, project_id, paths.project_root, paths.tasks_json
        )
        n_runs, n_disp = _import_runs(conn, state_dir, project_id)
        n_events = _import_events(conn, state_dir, project_id)
        n_spend = _import_spend(conn, state_dir, project_id)
        conn.execute(
            "UPDATE projects SET migrated_at = ? WHERE project_id = ?",
            (_utc_now_iso(), project_id),
        )
        conn.execute("COMMIT")
    except Exception as exc:  # noqa: BLE001
        try:
            conn.execute("ROLLBACK")
        except sqlite3.Error:
            pass
        print(f"migrate: import failed: {exc}", file=sys.stderr)
        return 1
    finally:
        conn.close()

    print("migrate: done")
    print(f"  tasks_runtime : {n_tasks}")
    print(f"  runs          : {n_runs}")
    print(f"  dispatches    : {n_disp}")
    print(f"  events        : {n_events}")
    print(f"  spend         : {n_spend}")
    print(f"  backup        : {backup_path}")
    print(f"  sqlite_path   : {sqlite_path}")
    return 0


def _do_rollback(
    project_id: str,
    state_dir: Path,
    sqlite_path: Path,
    backup_root: Path,
    from_backup: str | None,
) -> int:
    """Restore backup + drop tenant rows from the DB."""
    if from_backup:
        backup_dir = Path(from_backup).expanduser()
        if not backup_dir.is_absolute():
            backup_dir = backup_root / from_backup
    else:
        backup_dir = _newest_backup(backup_root)  # type: ignore[assignment]
    if backup_dir is None or not backup_dir.exists():
        print(f"rollback: no backup found in {backup_root}", file=sys.stderr)
        return 1

    if sqlite_path.exists():
        conn = sqlite3.connect(str(sqlite_path))
        try:
            conn.execute("PRAGMA foreign_keys = ON")
            conn.execute("BEGIN IMMEDIATE")
            # ON DELETE CASCADE walks projects → tasks_runtime + runs
            # (and runs → dispatches). Events + spend have no FK, so drop
            # them explicitly.
            conn.execute("DELETE FROM events WHERE project_id = ?", (project_id,))
            conn.execute("DELETE FROM spend WHERE project_id = ?", (project_id,))
            conn.execute("DELETE FROM projects WHERE project_id = ?", (project_id,))
            conn.execute("COMMIT")
        finally:
            conn.close()

    _restore_backup(backup_dir, state_dir)
    print(f"rollback: restored {backup_dir} → {state_dir}")
    print(f"rollback: dropped project {project_id!r} from {sqlite_path}")
    return 0
