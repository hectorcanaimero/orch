# python-frozen/

Byte-for-byte copies of `orchestrator/state/sqlite_migrations/001`–`005`,
taken from `main` at `5db7091` (2026-09-13) — the Python tree the last Python
release is cut from.

`TestEmbeddedMigrationsMatchThePythonTree` compares `internal/state/migrations/`
against these instead of the Python tree, so the check survives the Python
tree's deletion: databases written by the last Python release still have to
open here with zero pending migrations (ADR-G3).

**Never edit these files.** A migration that needs to change is a new
migration (`006_…` onward), never an edit to one of these.
