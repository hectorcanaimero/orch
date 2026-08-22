# SQLite Backend (Sprint B, v0.3+)

The orchestrator now supports an optional SQLite state backend as an
alternative to the historic JSONL/JSON files under `orchestrator/state/`.

## When to use it

- You run multiple projects out of a single orch install and want a unified
  view across them.
- You want SQL-friendly reporting (spend by tenant/day, event tails, etc.).
- You already have `~/.orch/orch.db` from another tool and want to share.

The file backend remains the default and is unchanged — a fresh `orch init`
project still writes JSONL files unless you flip the config knob below.

## Enable it

Edit `orchestrator/config.yaml` (or your project's config):

```yaml
state:
  backend: sqlite
  sqlite_path: null        # defaults to <state_dir>/orch.db when null
tasks_json_precedence: deps-only
```

Restart `orch` (or `orch dashboard`) and the backend is live. The DB file is
created lazily on the first run — no manual `CREATE TABLE` step required.

## Migrate an existing project

If your project already has JSONL state you want in the DB:

```bash
orch migrate --project-root /path/to/project --project-id my-tenant
```

The migrator:
1. Backs up `state/` to `state-backups/<ts>-state/` (JSONL only; `orch.db`
   is skipped so a partial DB never lands in your backup).
2. Creates the schema if missing.
3. Imports projects, tasks_runtime, runs, dispatches, events, spend in a
   single transaction (SIGKILL mid-import leaves the DB untouched).
4. Sets `projects.migrated_at` so a second run refuses unless you pass
   `--force`.

Preview without touching the DB:

```bash
orch migrate --project-root /path/to/project --dry-run
```

Rollback (restore JSONL + drop tenant rows):

```bash
orch migrate --project-root /path/to/project --rollback
# or with a specific backup dir:
orch migrate --project-root /path/to/project --rollback --from 20260822T014752Z-state
```

## Known limits

- WAL mode on network filesystems (NFS, SMB) can corrupt the DB. Startup
  emits a WARN log when the DB path is not on apfs/hfs (macOS only probe).
- `tasks.json` runtime status is IGNORED once the sqlite backend is active
  (see `tasks_json_precedence: "deps-only"`). Edit tasks by re-running
  `orch task-status <id> <status>` — do NOT hand-edit `tasks.json` status
  after migration.
- Dashboard live tail on sqlite polls the `events` table every 500 ms
  (`SELECT WHERE id > last_seen`). Latency floor ≈ 500 ms.
- SQLite < 3.32 (macOS pre-Ventura) is unsupported. Newer sqlite features
  (`RETURNING`, STRICT tables) are intentionally NOT used so the schema
  stays portable.

## Rollback to file backend

The file backend still works after a migration. Flip the config back to
`state.backend: file` and orch will read/write JSONL as before. The DB
tenant rows remain in place (do not conflict). To fully undo the migration
run `orch migrate --rollback`, which restores the JSONL from the backup and
drops the tenant's rows from the DB.
