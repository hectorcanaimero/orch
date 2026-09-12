-- 006_stakeholder_tokens.sql — G8.2 (F3.3): per-project stakeholder token.
--
-- Moves the dashboard's stakeholder token out of config.yaml into SQLite,
-- keyed by project_id like everything else, so it can be rotated with
-- `orch dashboard token rotate` (no YAML edit, no restart) and lays the
-- storage groundwork for a future --portfolio mode (several projects
-- behind one dashboard instance) — this migration does not add that
-- routing, only the table it will need.
--
-- Stores a SHA-256 hash, never the token itself, the same reasoning a
-- password table uses: `orch dashboard token show` can report WHEN a
-- token was last rotated without ever being able to leak WHAT it is.
--
-- Go-only from here on. Migrations 001-005 are byte-for-byte copies of
-- orchestrator/state/sqlite_migrations/ (ADR-G3) because that tree was
-- still live when they were written. It has been frozen since the
-- v0.11.0-py cut (ADR-G0) — no new features land on the Python line — so
-- 006 has no Python counterpart and never will; see migrate.go's own
-- comment and docs/brainstorm/go-migration-notes/sonnet-2.md for the
-- note. A database this migration has touched still opens in Python with
-- everything Python understands intact (user_version moving past what an
-- old binary recognizes is the same forward-compatibility question any
-- schema migration raises, not something specific to the freeze) — it
-- just also carries one table an old Python build has never heard of.
--
-- Compatibility: SQLite >= 3.32. No RETURNING, STRICT, or GENERATED.

PRAGMA user_version = 6;

CREATE TABLE IF NOT EXISTS stakeholder_tokens (
  project_id   TEXT PRIMARY KEY REFERENCES projects(project_id) ON DELETE CASCADE,
  token_hash   TEXT NOT NULL,
  rotated_at   TEXT NOT NULL
);
