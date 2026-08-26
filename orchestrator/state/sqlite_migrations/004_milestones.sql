-- 004_milestones.sql — Sprint F-3.
-- Adds `milestones` table for grouping tasks by stakeholder-visible deliverable.
-- Adds nullable `milestone_id` column on `tasks_definition`.

PRAGMA user_version = 4;

CREATE TABLE IF NOT EXISTS milestones (
    project_id   TEXT NOT NULL REFERENCES projects(project_id) ON DELETE CASCADE,
    id           TEXT NOT NULL,
    title        TEXT NOT NULL,
    description  TEXT,
    target_date  TEXT,
    status       TEXT NOT NULL DEFAULT 'open'
                 CHECK (status IN ('open', 'completed', 'cancelled')),
    created_at   TEXT NOT NULL
                 DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    PRIMARY KEY (project_id, id)
);

CREATE INDEX IF NOT EXISTS idx_milestones_project ON milestones(project_id);

ALTER TABLE tasks_definition
    ADD COLUMN milestone_id TEXT;
