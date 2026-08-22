-- 001_init.sql — Sprint B initial schema.
--
-- Every table is multitenant by `project_id`. Runtime status is owned by
-- `tasks_runtime`; `tasks.json` keeps the DAG + spec fields only (see design
-- doc "tasks_json_precedence: deps-only").
--
-- Compatibility: targets SQLite >= 3.32 (macOS-shipped baseline). NO usage
-- of RETURNING (3.35+), STRICT tables (3.37+), or GENERATED columns.

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
