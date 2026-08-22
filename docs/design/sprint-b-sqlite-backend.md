# Sprint B — SQLite as Optional Multitenant State Backend

**Branch:** `sprint-b/sqlite-backend`  |  **Closes:** #14

## Executive summary

Sprint B introduces an optional SQLite state backend at `~/.orch/orch.db`, selected via `config.yaml → state.backend: file | sqlite` (default `file`). A `StateBackend` Protocol is extracted from `state.py`, with a `FileBackend` that mechanically wraps today's logic and a new `SqliteBackend`. The DB is multitenant by `project_id` on every table so cross-project reporting works. `tasks.json` remains human-editable spec; only mutable runtime slice (status, PIDs, runs, events, spend) moves to the DB. The `task-*.sh` scripts stay sole writers of task status by shelling into a new `orch task-status <id> <status>` helper. `orch migrate` handles file→sqlite conversion idempotently with backup, dry-run, rollback. Pytest parity harness runs every state-touching test against both backends.

## Product decisions

- `tasks_json_precedence: "deps-only"` — DB owns runtime status; tasks.json owns DAG + spec_ref.
- Non-local FS: startup warning, not hard fail.
- Dashboard live tail on sqlite: 500ms polling on `events.id > last_seen`.

## Schema (SQLite, WAL mode, foreign_keys=ON, busy_timeout=5000)

```sql
PRAGMA user_version = 1;

CREATE TABLE IF NOT EXISTS projects (
  project_id     TEXT PRIMARY KEY,
  project_root   TEXT NOT NULL,
  created_at     TEXT NOT NULL,
  migrated_at    TEXT,
  spec_root      TEXT DEFAULT 'specs',
  schema_version INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE IF NOT EXISTS tasks_runtime (
  project_id       TEXT NOT NULL REFERENCES projects(project_id) ON DELETE CASCADE,
  task_id          TEXT NOT NULL,
  status           TEXT NOT NULL CHECK (status IN ('backlog','todo','in-progress','done','blocked')),
  started_at       TEXT,
  finished_at      TEXT,
  in_flight_pid    INTEGER,
  in_flight_run_id TEXT,
  attempts         INTEGER NOT NULL DEFAULT 0,
  last_backend     TEXT,
  last_model       TEXT,
  comments_json    TEXT NOT NULL DEFAULT '[]',
  updated_at       TEXT NOT NULL,
  PRIMARY KEY (project_id, task_id)
);
CREATE INDEX IF NOT EXISTS idx_tasks_runtime_status ON tasks_runtime(project_id, status);

CREATE TABLE IF NOT EXISTS runs (
  run_id         TEXT PRIMARY KEY,
  project_id     TEXT NOT NULL REFERENCES projects(project_id) ON DELETE CASCADE,
  started_at     TEXT NOT NULL,
  updated_at     TEXT NOT NULL,
  mode           TEXT NOT NULL CHECK (mode IN ('auto','semi')),
  parent_pid     INTEGER,
  status         TEXT NOT NULL CHECK (status IN ('live','done')),
  completed_json TEXT NOT NULL DEFAULT '[]',
  blocked_json   TEXT NOT NULL DEFAULT '[]',
  deferred_json  TEXT NOT NULL DEFAULT '[]'
);
CREATE INDEX IF NOT EXISTS idx_runs_project_started ON runs(project_id, started_at DESC);

CREATE TABLE IF NOT EXISTS dispatches (
  run_id       TEXT NOT NULL REFERENCES runs(run_id) ON DELETE CASCADE,
  task_id      TEXT NOT NULL,
  project_id   TEXT NOT NULL,
  backend      TEXT NOT NULL,
  pid          INTEGER NOT NULL,
  session_id   TEXT NOT NULL DEFAULT '',
  started_at   TEXT NOT NULL,
  prompt_path  TEXT NOT NULL DEFAULT '',
  log_path     TEXT NOT NULL DEFAULT '',
  output_path  TEXT NOT NULL DEFAULT '',
  attempt      INTEGER NOT NULL DEFAULT 1,
  status       TEXT NOT NULL CHECK (status IN ('in_flight','done')) DEFAULT 'in_flight',
  PRIMARY KEY (run_id, task_id)
);
CREATE INDEX IF NOT EXISTS idx_dispatches_status ON dispatches(status, project_id);

CREATE TABLE IF NOT EXISTS events (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id   TEXT NOT NULL,
  run_id       TEXT NOT NULL,
  event_type   TEXT NOT NULL,
  task_id      TEXT NOT NULL,
  backend      TEXT NOT NULL DEFAULT '',
  ts           TEXT NOT NULL,
  extra_json   TEXT NOT NULL DEFAULT '{}',
  dedup_hash   TEXT UNIQUE
);
CREATE INDEX IF NOT EXISTS idx_events_run_ts ON events(run_id, ts);
CREATE INDEX IF NOT EXISTS idx_events_project_ts ON events(project_id, ts DESC);

CREATE TABLE IF NOT EXISTS spend (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id   TEXT NOT NULL,
  ts           TEXT NOT NULL,
  task_id      TEXT NOT NULL,
  backend      TEXT NOT NULL,
  model        TEXT NOT NULL,
  tokens_in    INTEGER NOT NULL DEFAULT 0,
  tokens_out   INTEGER NOT NULL DEFAULT 0,
  cost_usd     REAL NOT NULL DEFAULT 0.0,
  duration_s   REAL NOT NULL DEFAULT 0.0,
  estimated    INTEGER NOT NULL DEFAULT 0,
  dedup_hash   TEXT UNIQUE
);
CREATE INDEX IF NOT EXISTS idx_spend_project_ts ON spend(project_id, ts DESC);
CREATE INDEX IF NOT EXISTS idx_spend_project_backend ON spend(project_id, backend);
```

Schema versioning: `PRAGMA user_version`, migrations under `orchestrator/state/sqlite_migrations/NNN_*.sql`.

## Package layout

```
orchestrator/state/
  __init__.py            # backend factory + re-exports (existing imports keep working)
  interface.py           # StateBackend Protocol + value types
  file_backend.py        # today's logic wrapped as FileBackend
  sqlite_backend.py      # new
  sqlite_migrations/
    001_init.sql
  shell.py               # call_task_start/finish/block (moved verbatim)
  flock.py               # acquire_flock etc. (moved verbatim)
```

## StateBackend Protocol (essential methods)

- `bootstrap(tasks) -> None` — ensure project + tasks_runtime rows exist (idempotent)
- `get_task_status(task_id) -> Status | None`
- `get_all_task_status() -> dict[str, Status]`
- `set_task_status(task_id, status, author, note, ts) -> None`  (called only from `orch task-status`)
- `create_run/load_run/save_run/list_runs`
- `add_dispatch/remove_dispatch/iter_in_flight`
- `append_event/iter_events`
- `append_spend/iter_spend(since?, until?)/iter_all_spend`
- `reconcile_in_flight() -> dict`
- `reset_task(task_id, author, note, ts) -> None`
- `clear_in_flight_for_run(run_id) -> list[str]`

Factory: `get_backend(paths, cfg) -> StateBackend`, cached per-process.

## Shell scripts strategy

Rewrite `templates/scripts/task-start.sh|finish.sh|block.sh|reset.sh` to `exec orch task-status "$id" <status> --author "$AUTHOR" --note "$NOTE"`. New subcommand `orch task-status <id> <status>` dispatches to active backend.

Contract preserved: shell scripts remain the only path that mutates task status. `backend.set_task_status()` is called ONLY from this subcommand.

## `orch migrate` command

```
orch migrate [--project-root PATH] [--project-id ID]
             [--backup-dir DIR] [--dry-run] [--force] [--rollback [--from BACKUP]]
```

Steps: backup → create tables → import projects → tasks_runtime (from tasks.json) → runs + dispatches (from run-*.json) → events (batch 500 from events-*.jsonl) → spend (batch 500 from spend-*.jsonl) → set migrated_at. Entire import in a single transaction. UNIQUE `dedup_hash` on events/spend for idempotent re-run.

Dedup hash formula:
- events: `sha256(f"{project_id}|{ts}|{task_id}|{event_type}|{run_id}")`
- spend: `sha256(f"{project_id}|{ts}|{task_id}|{backend}|{model}|{cost_usd}|{duration_s}")`

Rollback: `orch migrate --rollback [--from BACKUP]` — deletes rows for project + restores backup dir.

## Test strategy

Parity fixture in `conftest.py::backend` parametrized `["file", "sqlite"]`. Apply to:
- `test_state.py`, `test_spend_reader.py`, `test_orch.py`, `test_dashboard.py`, `test_only_filter.py`.

New tests: `test_sqlite_backend.py` (transactions, isolation, WAL, schema version), `test_migrate.py` (round-trip, idempotency, backup, dry-run, rollback).

Backwards compat gate: full suite with only `file` backend must pass unchanged.

## Risks (documented for future work)

1. tasks.json divergence after migration — resolved via `tasks_json_precedence: "deps-only"` default.
2. WAL on network filesystems — startup warning if mount type not apfs/hfs.
3. Dashboard live tail latency — 500ms polling.
4. Migration interrupted — single transaction guarantees byte-identical rollback on SIGKILL.
5. `estimated` bool stored as INTEGER 0|1.
