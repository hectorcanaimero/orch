# `orch` (Go) — implemented subcommands

A short, living page: what the Go binary actually does today, updated in the
same PR that wires a new subcommand. The trilingual manual
(`docs/MANUAL.{en,es,pt}.md`) describes the **Python** `orch` and stays valid
as the flag *contract* — a subcommand listed here should accept the same
flags Python does unless a difference is called out.

**Status by phase** (see the "Orch en Go" migration plan):

- **G1.5 — landed**: `status`, `tasks`, `events`, `logs`.
- **G1.6 / G2+ — not yet in this table**: `validate`, `graph`, `router`,
  `config`, `task`, `task-status`, `reset`, `stop`, and everything else in
  Python's `_SUBCOMMANDS`. Add a row here in the same PR that lands one.

## Subcommands

| Subcommand | Flags | `--json` | Parity with Python |
|---|---|---|---|
| `status` | `--json`, `--only GLOB`, `--status LIST`, `--project-root`, `--project-id`, `--config` | yes | Identical wire shape (`build_status_snapshot`) — verified by `scripts/parity.sh`. `cost_usd`/`project_total_usd`/`filtered_total_usd` and `latest_run`, and the `backend`/`cli_model`/`tier` route fields, are documented gaps — see `docs/brainstorm/go-migration-notes.md`. |
| `tasks` | `--json`, `--only GLOB`, `--status LIST`, `--project-root`, `--project-id`, `--config` | yes | Identical trimmed wire shape — verified by `scripts/parity.sh`. Same route-field gap as `status`. |
| `events` | `TASK_ID` (positional), `--tail N` (default 20, 0=all), `--run RUN_ID`, `--json`, `--project-root`, `--project-id`, `--config` | yes | Identical wire shape (`iter_events`'s row) — verified by `scripts/parity.sh`. `--run` filters client-side (`state.Backend.Events` has no run-id parameter); Python filters at the SQL layer — same result, different mechanism. |
| `logs` | `TASK_ID` (positional), `--tail N` (default 200), `--all`, `--project-root`, `--project-id`, `--config` | **no** — Python's `_run_logs_subcommand` never had one either | Same human output shape and exit codes (0 = printed, 1 = config/layout error, 2 = no log file for that task). Not compared by `scripts/parity.sh` (nothing to diff as JSON); see the golden under `testdata/parity-project/goldens/logs-F2.T3.txt`. |

## Conventions every row above follows

- **Project flags** (`--project-root`, `--project-id`, `--config`) are
  persistent flags on the root command (`internal/cli/root.go`), not
  repeated per subcommand in the Go source — but they work identically to
  Python's per-subcommand `_add_common_project_flags`.
- **Exit codes**: `0` success, `1` config/project-layout error, `2` reserved
  for a command-specific "not found" case (`logs`'s missing log file today;
  `errNotImplemented`/`exitError` in `root.go` is the shared mechanism for
  future subcommands that need a third code).
- **`--json` output** is compact (`encoding/json`'s default, HTML-escaping
  off) — same byte shape category as Python's
  `json.dumps(..., separators=(",", ":"))`, verified command-by-command in
  `scripts/parity.sh` rather than assumed.
