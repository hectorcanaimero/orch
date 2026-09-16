-- 007_spend_cache_tokens.sql — the cache part of a dispatch's input tokens.
--
-- `tokens_in` keeps every input token a dispatch processed, cache reads and
-- cache writes included, as the CLI reported them. These two columns say how
-- much of it was which, so the budget gate can weight them by what the
-- provider bills (a cache read 10% of an input token, a cache write 125%)
-- without the row losing the raw number. Rows written before this migration,
-- and by providers that report no cache usage, carry 0 and are summed as
-- plain input, exactly as before.
--
-- Go-only, like 006: the Python line is frozen (ADR-G0). Python reads spend
-- by column name, so the extra columns do not disturb it.
--
-- Compatibility: SQLite >= 3.32. No RETURNING, STRICT, or GENERATED.

PRAGMA user_version = 7;

ALTER TABLE spend ADD COLUMN cache_read_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE spend ADD COLUMN cache_creation_tokens INTEGER NOT NULL DEFAULT 0;
