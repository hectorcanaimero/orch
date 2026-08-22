# Observability

Sprint C added six read-only subcommands so you can watch orch without
leaving the terminal. All of them respect `--project-root` /
`--project-id` / `--config` — the same triple the main loop takes.

## Cheat sheet

| Command | What it shows |
|---|---|
| `orch status` | Project table: ID · STATUS · BACKEND/MODEL · LAST EVENT · COST · PHASE. Header includes run summary. |
| `orch tasks` | Thinner listing: ID · STATUS · BACKEND/MODEL · DEPS · PHASE. |
| `orch events <ID>` | Event stream for one task. Default: last 20. |
| `orch logs <ID>` | Raw log tail from `<state_dir>/logs/<ID>.log`. Default: last 200 lines. Exits 2 if the file is missing. |
| `orch --dry-run --json` | Machine-readable plan (`{plan: [...], count}`). `--json` alone is an error. |
| `orch graph --out plan.html` | Self-contained HTML/SVG snapshot. No external deps, works offline. |

All human commands render with `rich` when available and fall back to
plain text. Every command also accepts `--json` where it makes sense.

## Verbosity

`orch` now has `-v/--verbose` and `-q/--quiet` flags. Precedence:

1. `--quiet` — ERROR only.
2. `--verbose` — DEBUG.
3. `ORCH_LOG_LEVEL` env var — whatever you set.
4. Default — INFO.

The router fallback announcement used to double-emit at startup (WARN
log + printed stderr line). It's now a single INFO summary; use `-v` to
see the per-route detail.

## End-of-run summary

Every clean run now prints a compact recap (completed / blocked /
deferred / still-in-flight counts + total cost + top 5 costliest tasks)
before the process exits. Skipped on `--dry-run`, SIGINT drain, or
config-error early exits.

## Example workflow

```bash
# 1. Inspect the plan.
orch status --only 'F1-*'

# 2. Bail out to JSON for scripts.
orch status --json --only 'F1-*' | jq '.tasks[] | .id + " → " + .status'

# 3. Something looks stuck; drill into events + logs.
orch events F1-04 --tail 40
orch logs F1-04 --tail 50

# 4. Send someone a visual snapshot.
orch graph --out plan.html --only 'F1-*'
open plan.html
```

## Caveats

- `orch graph` renders inline SVG. It's comfortable up to about 500
  tasks; use `--only` beyond that.
- `defer_reasons` (used by `orch status`'s `defer_reason` field) live in
  the running orch's memory only — reads from disk report `null`. If you
  need it persisted, that's a follow-up (Sprint C decision #6).
- `SpendEntry` has no `run_id` column yet, so the end-of-run summary uses
  the in-memory `task_costs` dict from the main loop rather than
  re-reading spend files (Sprint C decision #5).
