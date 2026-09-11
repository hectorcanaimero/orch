-- 003_task_definition.sql — Sprint F-1.
--
-- Adds `tasks_definition` table to hold the declarative (static) fields
-- of each task: model assignment, dependencies, spec_ref, files, etc.
-- Previously these lived in tasks.json only; now SQLite is the single
-- runtime owner. tasks.json remains as atomize input format only.
--
-- Compatibility: SQLite >= 3.32. No RETURNING, STRICT, or GENERATED.

PRAGMA user_version = 3;

CREATE TABLE IF NOT EXISTS tasks_definition (
  project_id   TEXT NOT NULL REFERENCES projects(project_id) ON DELETE CASCADE,
  task_id      TEXT NOT NULL,
  title        TEXT NOT NULL DEFAULT '',
  model        TEXT,
  backend      TEXT,
  deps_json    TEXT NOT NULL DEFAULT '[]',
  spec_ref     TEXT,
  phase        INTEGER,
  estimate_h   REAL,
  reason       TEXT,
  files_json   TEXT NOT NULL DEFAULT '[]',
  updated_at   TEXT NOT NULL,
  PRIMARY KEY (project_id, task_id)
);
CREATE INDEX IF NOT EXISTS idx_tasks_def_project ON tasks_definition(project_id);
