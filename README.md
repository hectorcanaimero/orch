# orch

Task orchestrator that walks a `tasks.json` DAG and dispatches each task to the
right local CLI (`claude` | `codex` | `opencode`). Single-user, local, no
daemon, no remote queue.

Built to run 300+ task pipelines unattended. Handles per-provider concurrency
caps, retries with backoff, per-task timeouts, budget guardrails against
subscription quotas, semi-mode checkpoints for risky tasks, and a live
FastAPI dashboard.

## Install

Requires Python 3.11+ and at least one of `claude`, `codex`, or `opencode` on
your `PATH`, authenticated.

```bash
# Recommended — isolated venv, `orch` on PATH globally
pipx install git+https://github.com/hectorcanaimero/orch.git

# Or with uv
uv tool install git+https://github.com/hectorcanaimero/orch.git

# Or classic pip --user
pip install --user git+https://github.com/hectorcanaimero/orch.git
```

Verify:

```bash
orch --help
```

## Quickstart

`orch` expects a project layout like:

```
your-project/
├── tasks.json                    # your task DAG
├── scripts/
│   ├── task-start.sh             # called before each dispatch
│   ├── task-finish.sh            # called by the agent on success
│   └── task-block.sh             # called by the agent when blocked
└── orchestrator/
    └── state/                    # created on first run
```

Then, from anywhere:

```bash
# Auto mode — no prompts, dispatches every ready task
orch --project-root /path/to/your-project --mode auto

# Semi mode — pauses on tasks flagged as `critical` for operator approval
orch --project-root /path/to/your-project --mode semi

# Dry-run — enumerate the plan without spawning subprocesses
orch --project-root /path/to/your-project --dry-run

# Filter — `--only` accepts fnmatch globs on task ids
orch --project-root /path/to/your-project --only 'F1-*'
```

`ORCH_PROJECT_ROOT` env var works as a fallback for `--project-root`.

## Configuration

Three YAML files live next to the installed package (override per-project by
placing a file with the same name in your project root):

| File | Purpose |
|---|---|
| `config.yaml` | concurrency caps, timeouts, retry backoff, spec_root prefix |
| `model_router.yaml` | maps `Task.model` strings → `(backend, cli_model)` |
| `budgets.yaml` | provider quota guardrails (Sprint 7) |

### Budget guardrails (Sprint 7)

Long unattended runs can exhaust your Claude Max / Codex Pro / opencode
subscription quota, cutting you off from interactive terminal use for hours.

`orch` tracks token usage per provider in a rolling window from
`state/spend-*.jsonl` and pauses dispatches when usage crosses a configurable
threshold. When ALL providers are capped, the main loop sleeps until the next
reset window (chunked to 30s so `SIGINT` stays responsive).

Presets in `budgets.yaml`:

- `conservative` (default) — threshold 60-70%, safe for shared use
- `aggressive` — threshold 90-95%, maximizes throughput
- `shared` — threshold 40-60%, reserves capacity for interactive terminal use

Select a preset:

```bash
# CLI (highest priority)
orch --budgets-preset aggressive ...

# Env
ORCH_BUDGETS_PRESET=shared orch ...

# In your project's config.yaml
budgets_preset: aggressive
```

Calibration is empirical: start with `conservative`, observe where your
provider actually rate-limits you, then set
`token_budget = observed_tokens * 0.9` and gradually raise `threshold_pct`.

Delete or rename `budgets.yaml` to disable the gate entirely — everything
works exactly like a pre-Sprint 7 orch.

## Dashboard

Read-only FastAPI dashboard for live task state, spend, and budget usage:

```bash
orch dashboard --project-root /path/to/your-project
# → open http://127.0.0.1:8000
```

Endpoints:

| Path | What |
|---|---|
| `/` | kanban-style task table |
| `/kanban` | phase-grouped kanban view |
| `/metrics` | cost/spend metrics |
| `/logs` | live event stream (SSE) |
| `/api/tasks` | JSON dump of all tasks |
| `/api/metrics` | JSON metrics |
| `/api/budgets` | per-provider budget snapshot (Sprint 7) |
| `/snapshot` | download full JSON snapshot |

## Concurrent instances on disjoint tasks

Default flock protects the whole state directory (single writer). Add
`--task-locks` to swap it for per-task locks — multiple `orch` instances can
then work on disjoint tasks simultaneously:

```bash
orch --project-root /path/to/project --only 'F1-*' --task-locks &
orch --project-root /path/to/project --only 'F2-*' --task-locks &
```

## Exit codes

| Code | Meaning |
|---|---|
| `0` | clean drain — all reachable tasks done, none blocked |
| `1` | config error / unrouted model / dependency cycle / blocked tasks at end |
| `2` | project layout invalid (`tasks.json` or `scripts/` missing) |
| `3` | flock contention — another `orch` holds `state/.lock` |
| `130` | `SIGINT` during graceful drain |

## Development

```bash
git clone https://github.com/hectorcanaimero/orch.git
cd orch
python3 -m venv .venv && source .venv/bin/activate
pip install -e '.[dev]'
pytest
```

## History

Originally built as the task orchestrator for the Rupies v2 monorepo rewrite
(334 tasks across 6 phases, Flutter + Supabase + Edge Functions). Extracted
into a standalone tool that any tasks.json-shaped DAG can consume.

SDD artifacts (`proposal.md`, `spec.md`, `design.md`, `tasks.md`,
`verify-report.md`, `archive-report.md`) preserved as historical context in
the repo root.

## License

MIT.
