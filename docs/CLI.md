# `orch` (Go) — implemented subcommands

A short, living page: what the Go binary actually does today, updated in the
same PR that wires a new subcommand. The trilingual manual
(`docs/MANUAL.{en,es,pt}.md`) describes the **Python** `orch` and stays valid
as the flag *contract* — a subcommand listed here should accept the same
flags Python does unless a difference is called out.

**Status by phase** (see the "Orch en Go" migration plan):

- **G1.5 — landed**: `status`, `tasks`, `events`, `logs`.
- **G3.6 — landed**: `task set`, `task-status`, `reset`. `stop` was split out
  to **G3.3** (it needs the engine's run tracking, not `Backend.Transition`
  — see `docs/brainstorm/go-migration-notes.md`).
- **G1.6 / G2+ — not yet in this table**: `validate`, `graph`, `router`,
  `config`, `stop`, and everything else in Python's `_SUBCOMMANDS`. Add a
  row here in the same PR that lands one.

## Subcommands

| Subcommand | Flags | `--json` | Parity with Python |
|---|---|---|---|
| `status` | `--json`, `--only GLOB`, `--status LIST`, `--project-root`, `--project-id`, `--config` | yes | Identical wire shape (`build_status_snapshot`) — verified by `scripts/parity.sh` and, for real spend/run data, `internal/cli/testdata/script/status-real.txtar` against `internal/state/testdata/orch-py-0.11.0.db`. `cost_usd`/`project_total_usd`/`filtered_total_usd` (via `state.Backend.SpendSince`), `backend`/`cli_model`/`tier` (via `internal/router`), and `latest_run` (via `state.Backend.LatestRun`) are all wired to real data — `testdata/parity-project` just has none of it recorded, confirmed byte-for-byte against a real Python run in the same PR. **Remaining partial gap:** `latest_run` omits `completed_count`/`blocked_count`/`deferred_count`/`run_file`/`events_file` — `state.Run` doesn't carry the run-state JSON columns or file-backend-style paths those come from (ADR-G4 dropped the file backend); see `docs/brainstorm/go-migration-notes.md`. |
| `tasks` | `--json`, `--only GLOB`, `--status LIST`, `--project-root`, `--project-id`, `--config` | yes | Identical trimmed wire shape — verified by `scripts/parity.sh`. Reuses `status`'s snapshot, so the same real `backend`/`cli_model` route wiring applies (`cost_usd` and `latest_run` were never part of this trimmed view, in Python either). |
| `events` | `TASK_ID` (positional), `--tail N` (default 20, 0=all), `--run RUN_ID`, `--json`, `--project-root`, `--project-id`, `--config` | yes | Identical wire shape (`iter_events`'s row) — verified by `scripts/parity.sh`. `--run` filters client-side (`state.Backend.Events` has no run-id parameter); Python filters at the SQL layer — same result, different mechanism. |
| `logs` | `TASK_ID` (positional), `--tail N` (default 200), `--all`, `--project-root`, `--project-id`, `--config` | **no** — Python's `_run_logs_subcommand` never had one either | Same human output shape and exit codes (0 = printed, 1 = config/layout error, 2 = no log file for that task). Not compared by `scripts/parity.sh` (nothing to diff as JSON); see the golden under `testdata/parity-project/goldens/logs-F2.T3.txt`. |
| `task-status` | `TASK_ID STATUS` (positional), `--author`, `--note`, `--project-root`, `--project-id`, `--config` | n/a — no `--json`, ever (single-writer helper, machine-readable success is "exit 0") | Same exit codes as Python's `_run_task_status_subcommand`: 0 success, 1 config/layout error, 2 unknown task id **or** an invalid `STATUS`, 3 illegal transition. `scripts/task-{start,finish,block,reset}.sh` from `testdata/parity-project` `exec orch task-status …` and are exercised against `bin/orch` in `internal/cli/testdata/script/taskscripts.txtar`. |
| `task set` | `--id` (required), `--status`, `--model`, `--backend`, `--milestone`, `--project-root`, `--project-id`, `--config` | no | **Partial.** `--status` routes through `Backend.Transition`, same exit codes as Python (0 success, 1 missing-flags/unknown-id, 3 illegal transition). `--model`/`--backend`/`--milestone` are registered (so the flags aren't silently rejected) but return a clear error — `state.Backend` has no method to write `tasks_definition` yet. See `docs/brainstorm/go-migration-notes.md`. |
| `reset` | `--requeue`, `--only GLOB`, `--project-root`, `--project-id`, `--config` | no | **Deliberately not a literal port.** Python reads tasks.json's own (F-12-stale) `status` field to find in-progress candidates; Go reads the real runtime status via `Backend.Tasks` — see the doc comment on `newResetCmd` in `internal/cli/reset.go` for why. Dry-run vs `--requeue` output shape and the always-exit-0-after-partial-failure behavior match Python. Exit 2 on invalid project layout, matching Python (a different code than `status`/`tasks`/`events`, which use 1 for the same check). |

## Conventions every row above follows

- **Project flags** (`--project-root`, `--project-id`, `--config`) are
  persistent flags on the root command (`internal/cli/root.go`), not
  repeated per subcommand in the Go source — but they work identically to
  Python's per-subcommand `_add_common_project_flags`.
- **Exit codes**: `0` success, `1` config/project-layout error (except
  `reset`, which uses `2` for that check, matching Python) — beyond that,
  codes are per-command and match Python's own table: `2` for "not found"
  (`logs`'s missing log file, `task-status`'s unknown task id or invalid
  status), `3` for an illegal status transition (`task-status`, `task set`).
  `errNotImplemented`/`exitError` in `root.go` is the shared mechanism any
  command uses to pick a non-1 code.
- **`--json` output** is compact (`encoding/json`'s default, HTML-escaping
  off) — same byte shape category as Python's
  `json.dumps(..., separators=(",", ":"))`, verified command-by-command in
  `scripts/parity.sh` rather than assumed.
