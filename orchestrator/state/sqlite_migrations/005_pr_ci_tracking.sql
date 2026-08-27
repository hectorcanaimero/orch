-- 005_pr_ci_tracking.sql — Sprint F-4: PR automation + CI polling columns.
-- Tracks the GitHub/GitLab PR created after each worktree push and
-- the resulting CI check result. NULL pr_url = worktree mode off or auto_pr disabled.

PRAGMA user_version = 5;

ALTER TABLE tasks_runtime ADD COLUMN pr_url      TEXT;
ALTER TABLE tasks_runtime ADD COLUMN ci_status   TEXT
    CHECK (ci_status IN ('pending', 'success', 'failure', 'skipped'));
ALTER TABLE tasks_runtime ADD COLUMN ci_attempts INTEGER NOT NULL DEFAULT 0;
