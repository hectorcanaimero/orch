-- 002_findings.sql — Sprint E-1 (dogfooding loop, issue #17).
--
-- Adds the `findings` table for the agent → GitHub feedback loop. Scoped by
-- `project_id` like every other table (multitenant). `dedup_hash` is UNIQUE
-- to prevent re-capturing the same finding by hash at the DB layer even if
-- the CLI check races.
--
-- Compatibility: SQLite >= 3.32 (no RETURNING, no STRICT, no GENERATED).

PRAGMA user_version = 2;

CREATE TABLE IF NOT EXISTS findings (
    id                TEXT PRIMARY KEY,
    project_id        TEXT NOT NULL,
    created_at        TEXT NOT NULL,
    type              TEXT NOT NULL CHECK (type IN ('bug','fix','feature')),
    about             TEXT NOT NULL CHECK (about IN ('orch','project')),
    summary           TEXT NOT NULL,
    evidence          TEXT NOT NULL DEFAULT '',
    confidence        TEXT NOT NULL CHECK (confidence IN ('low','medium','high')),
    status            TEXT NOT NULL DEFAULT 'pending'
                        CHECK (status IN ('pending','published','dismissed','duplicate')),
    published_url     TEXT,
    duplicate_of      TEXT,
    dedup_hash        TEXT NOT NULL UNIQUE,
    author            TEXT NOT NULL DEFAULT 'agent',
    dismissed_reason  TEXT
);
CREATE INDEX IF NOT EXISTS idx_findings_project_status ON findings(project_id, status);
CREATE INDEX IF NOT EXISTS idx_findings_project_about  ON findings(project_id, about);
