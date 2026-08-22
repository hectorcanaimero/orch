# orch

Task orchestrator that walks a `tasks.json` DAG and dispatches each task to the
right local AI CLI (`claude` | `codex` | `opencode`). Single-user, local, no
daemon, no remote queue.

> **New user? Read the step-by-step manual first:**
> **[English](docs/MANUAL.en.md)** · **[Español](docs/MANUAL.es.md)** · **[Português](docs/MANUAL.pt.md)**
>
> The manual covers install → project init → chat-driven spec generation → dashboard,
> with concrete commands and expected output.

Built to run 300+ task pipelines unattended. Handles per-provider concurrency
caps, retries with backoff, per-task timeouts, **budget guardrails against
subscription quotas** so long unattended runs don't lock you out of your own
Claude / Codex / opencode terminal, semi-mode checkpoints for risky tasks, and
a live FastAPI dashboard.

**Status**: v0.2.0 — production-ready for the use case it was built for.

---

## Table of contents

1. [Install](#install)
2. [How orch thinks](#how-orch-thinks)
3. [Project layout you need](#project-layout-you-need)
4. [Minimum tasks.json example](#minimum-tasksjson-example)
5. [The task-*.sh contract](#the-task-sh-contract)
6. [First run](#first-run)
7. [Dashboard](#dashboard)
8. [Configuration](#configuration)
9. [Budget guardrails (Sprint 7)](#budget-guardrails-sprint-7)
10. [Atomizer — markdown → tasks.json](#atomizer--markdown--tasksjson)
11. [State directory layout](#state-directory-layout)
12. [Concurrent instances on disjoint tasks](#concurrent-instances-on-disjoint-tasks)
13. [Exit codes](#exit-codes)
14. [Development](#development)
15. [History](#history)

---

## Install

Requires Python 3.11+ and at least one of `claude`, `codex`, or `opencode` on
your `PATH`, authenticated with a subscription.

### Install (recommended) — prebuilt wheel from GitHub Releases

The wheel published to Releases is built by CI with the compiled SPA
already embedded, so no Node / pnpm toolchain is needed on your machine.

```bash
# Always latest release
pipx install https://github.com/hectorcanaimero/orch/releases/latest/download/orchestrator-0.5.0-py3-none-any.whl
```

Or pin to a specific version:

```bash
pipx install https://github.com/hectorcanaimero/orch/releases/download/v0.5.0/orchestrator-0.5.0-py3-none-any.whl
```

To upgrade later: `pipx install --force <same-url>`.

### Install from source (for contributors)

```bash
pipx install --force git+https://github.com/hectorcanaimero/orch.git@main
```

Note: this clones the repo and builds the wheel locally. Requires Node
+ pnpm for the SPA build step (falls back to npm if pnpm isn't
available). The compiled SPA is committed to the repo, so this works
today, but the CI-built release wheel above is the canonical
distribution.

Alternatives:

```bash
# uv
uv tool install https://github.com/hectorcanaimero/orch/releases/latest/download/orchestrator-0.5.0-py3-none-any.whl

# pip --user
pip install --user https://github.com/hectorcanaimero/orch/releases/latest/download/orchestrator-0.5.0-py3-none-any.whl
```

Verify:

```bash
orch --help
orch init --help
orch dashboard --help
```

---

## Getting started (60 seconds)

`orch init` scaffolds a project with everything wired. Two modes:

```bash
# Interactive wizard (no flags) — prompts for id, backend, budget preset, etc.
orch init

# Or batch mode with a path
orch init ~/work/my-app
```

That creates:

```
my-app/
├── tasks.json                    # empty skeleton
├── specs/README.md               # explains the spec format
├── scripts/task-{start,finish,block}.sh   # executable, functional
├── orchestrator/
│   ├── state/.gitkeep
│   ├── config.yaml               # copy of the packaged default
│   ├── model_router.yaml
│   └── budgets.yaml
└── .gitignore                    # only when absent
```

Then you have two paths to fill `tasks.json`:

**A) Write specs manually** (works today, no extra tooling):

```bash
$EDITOR ~/work/my-app/specs/f0-foundation.md
# ...write specs in the format documented in specs/README.md

# Preview the diff (read-only)
orch atomize --project-root ~/work/my-app --file ~/work/my-app/specs/f0-foundation.md

# Apply — writes tasks.json + creates a .bak-<ts> backup
orch atomize --project-root ~/work/my-app --file ~/work/my-app/specs/f0-foundation.md --apply
```

**B) Use Spec-Driven Development** (via Claude Code + SDD skills):

```bash
orch init ~/work/my-app --sdd           # also creates openspec/ layout
# then in Claude Code:
#   /orch-plan <feature idea>            # full pipeline PRD→ARCH→SPEC→TASKS
# or granular:
#   /sdd-explore <topic>
#   /orch-spec                            # atomizer-ready spec
#   /orch-tasks                           # invokes orch atomize --apply (diff-first)
```

Then run:

```bash
orch --project-root ~/work/my-app --dry-run    # review the plan
orch --project-root ~/work/my-app --mode semi  # dispatch with checkpoints
```

### `orch init` flags

| Flag | Purpose |
|---|---|
| `--interactive` | Force the wizard even when a PATH is present (default: auto-detect) |
| `--non-interactive` | Force batch mode; requires PATH. Useful in CI. |
| `--force` | Overwrite existing files at PATH (default: refuse on conflict) |
| `--sdd` | Also create `openspec/` layout for SDD workflow |
| `--project-name NAME` | Override `meta.project` in `tasks.json` (default: PATH basename) |

The init also **detects if SDD skills are installed** at `~/.claude/skills/`
and prints tailored next-steps. When `stdin` isn't a TTY (CI, piped stdin) and
no flags are given, the wizard auto-falls-back with a clear error message.

### Preflight commands

Two read-only commands verify a scaffold is ready to run:

```bash
orch doctor       # backends installed, scripts exec, jq on PATH, state writable
orch validate     # tasks.json shape, dep cycles, unresolved routes, config sanity
```

Both accept `--json` for CI gates. Exit codes: `0` all ok, `1` warnings only,
`2` any error. See [docs/PREFLIGHT.md](docs/PREFLIGHT.md) for the operator guide.

### Dogfooding loop — `orch findings`

Agents running on top of orch capture bugs / fixes / feature ideas locally,
then a human reviews and publishes them to GitHub as issues on the orch
repo.

```bash
orch findings capture --type bug --about orch \
    --summary "Dashboard crashes on refresh" \
    --evidence "orchestrator/dashboard/main.py:42 …" \
    --confidence high

orch findings list --status pending
orch findings review <id>
orch findings publish <id>            # asks for TTY confirmation
orch findings dismiss <id> --reason "not actionable"
```

Publish is guarded: `about=project` findings can never publish upstream,
`low` confidence needs `--force`, there's a rate limit, and both a local
hash and a GitHub issue search catch duplicates before creating a new
issue. See [docs/DOGFOODING.md](docs/DOGFOODING.md) for the full operator
guide + a copy-pasteable agent prompt snippet.

### Dashboard profiles — public URL for stakeholders

The read-only dashboard now supports three profiles: `operator` (default,
localhost, full access), `stakeholder` (token-gated curated view — phase
progress, milestones, total spend, ETA), and `both` (mixed).

```bash
# One-off — token via flag.
orch dashboard --profile stakeholder --token "$(openssl rand -hex 32)"

# Mixed — operator on /, stakeholder on /stakeholder, one process.
export ORCH_DASHBOARD_TOKEN=my-secret
orch dashboard --profile both
```

Stakeholder sees phase % + task counts + milestones + total spend
(rounded up to nearest $0.50) + ETA. It never sees per-model spend,
raw logs, per-task exit codes, or provider identifiers. Every operator
route returns 403 in stakeholder mode.

Publish behind a Cloudflare Tunnel for a public HTTPS URL — step-by-step
in [docs/DASHBOARD-PROFILES.md](docs/DASHBOARD-PROFILES.md).

---

## How orch thinks

1. You describe your work as a **DAG of tasks** in `tasks.json` (id, model,
   dependencies, files, estimate).
2. `orch` walks the DAG. When a task's dependencies are done and there's
   capacity, it **spawns the right CLI** for that task's model (via
   `model_router.yaml`).
3. Each dispatch runs `scripts/task-start.sh <id>` before, then the CLI does
   the work, then the agent itself calls `scripts/task-finish.sh <id> …` on
   success or `scripts/task-block.sh <id> …` on failure. `orch` reads the
   result, updates `tasks.json`, and continues.
4. **Budget gate** checks token usage per provider in a rolling window before
   every dispatch. If the provider is over threshold, skip it. If ALL
   providers are over → sleep until the next reset.
5. **Semi mode** prompts you for critical tasks; **auto mode** dispatches
   everything without asking.

---

## Project layout you need

`orch` doesn't ship with your project. It expects this shape wherever you
point it (`--project-root PATH` or `ORCH_PROJECT_ROOT` env):

```
your-project/
├── tasks.json                    # your task DAG (see next section)
├── scripts/
│   ├── task-start.sh             # invoked by orch BEFORE each dispatch
│   ├── task-finish.sh            # invoked by the AGENT on success
│   └── task-block.sh             # invoked by the AGENT when blocked
└── orchestrator/
    ├── state/                    # created on first run (spend, events, prompts)
    ├── config.yaml               # optional — override packaged defaults
    ├── model_router.yaml         # optional — override packaged defaults
    └── budgets.yaml              # optional — override packaged defaults
```

If any of the YAML files are missing from your project, `orch` falls back to
the defaults shipped with the installed package.

---

## Minimum tasks.json example

Two tasks: a foundation task done by a cheap model, and a real feature
depending on it done by Claude Sonnet.

```json
{
  "meta": {
    "project": "my-app",
    "generatedAt": "2026-08-21"
  },
  "phases": [
    { "id": 0, "name": "F0 — Foundation" },
    { "id": 1, "name": "F1 — Feature" }
  ],
  "tasks": [
    {
      "id": "T-001",
      "phase": 0,
      "title": "Scaffold monorepo",
      "description": "Create the base workspace + shared configs.",
      "model": "opencode-go/glm-5.1",
      "reason": "Cheap scaffolding, no design decisions.",
      "status": "todo",
      "dependencies": [],
      "estimateHours": 1.0,
      "files": ["package.json", "tsconfig.base.json"],
      "specRef": "specs/f0-foundation.md",
      "comments": []
    },
    {
      "id": "T-002",
      "phase": 1,
      "title": "Implement auth flow",
      "description": "Sign in + sign up + password reset.",
      "model": "claude-sonnet-4-6",
      "reason": "Non-trivial state machine — needs strong reasoning.",
      "status": "todo",
      "dependencies": ["T-001"],
      "estimateHours": 6.0,
      "files": ["src/auth/**"],
      "specRef": "specs/f1-auth.md",
      "comments": []
    }
  ]
}
```

Valid statuses: `backlog`, `todo`, `in-progress`, `done`, `blocked`.

The `model` string must exist in `model_router.yaml`. `orch` fails-fast at
startup if any task points to an unrouted model.

---

## The task-*.sh contract

Three shell scripts you write in your project's `scripts/` directory. They're
the only surface where task state gets mutated — `orch` never edits
`tasks.json` directly.

### `scripts/task-start.sh`

Called by `orch` right before spawning the agent CLI.

```bash
#!/usr/bin/env bash
# scripts/task-start.sh <task-id> [--author "<backend>/<model>"] [--project-root PATH]
# Set the task's status to "in-progress" and append an audit comment.
set -euo pipefail
TASK_ID="$1"
jq --arg id "$TASK_ID" '(.tasks[] | select(.id == $id) | .status) = "in-progress"' \
   tasks.json > tasks.json.tmp && mv tasks.json.tmp tasks.json
```

### `scripts/task-finish.sh`

Called by the agent (inside the CLI) after the work is done.

```bash
#!/usr/bin/env bash
# scripts/task-finish.sh <task-id> "<summary>" "<backend>/<model>"
set -euo pipefail
TASK_ID="$1"; SUMMARY="$2"; AUTHOR="${3:-agent}"
NOW="$(date -u +%FT%TZ)"
jq --arg id "$TASK_ID" --arg s "$SUMMARY" --arg a "$AUTHOR" --arg ts "$NOW" '
  (.tasks[] | select(.id == $id) | .status) = "done"
  | (.tasks[] | select(.id == $id) | .comments) += [{"author":$a,"body":$s,"at":$ts}]
' tasks.json > tasks.json.tmp && mv tasks.json.tmp tasks.json
```

### `scripts/task-block.sh`

Called by the agent when it can't proceed.

```bash
#!/usr/bin/env bash
# scripts/task-block.sh <task-id> "<reason>" "<backend>/<model>"
set -euo pipefail
TASK_ID="$1"; REASON="$2"; AUTHOR="${3:-agent}"
NOW="$(date -u +%FT%TZ)"
jq --arg id "$TASK_ID" --arg r "$REASON" --arg a "$AUTHOR" --arg ts "$NOW" '
  (.tasks[] | select(.id == $id) | .status) = "blocked"
  | (.tasks[] | select(.id == $id) | .comments) += [{"author":$a,"body":$r,"at":$ts}]
' tasks.json > tasks.json.tmp && mv tasks.json.tmp tasks.json
```

`chmod +x scripts/task-*.sh` and you're set.

---

## First run

```bash
cd /path/to/your-project

# Dry-run first — enumerate the plan, spawn NOTHING
orch --dry-run

# Auto mode — dispatch every ready task
orch --mode auto

# Semi mode — prompt for approval on critical tasks (see spec §10 for what's "critical")
orch --mode semi

# Filter — fnmatch glob on task ids
orch --only 'T-0*'

# Cap total dispatches (useful for smoke tests)
orch --max-tasks 3

# Point at a different project without cd'ing
orch --project-root /other/project --mode auto
```

The main loop ticks every 200 ms: reap terminated children → sweep for
timeouts → refill dispatches up to the concurrency caps → repeat. `Ctrl-C`
triggers a graceful drain (waits for in-flight children to finish before
exit) — hit `Ctrl-C` twice to force-kill.

---

## Dashboard

Read-only FastAPI + SSE dashboard. Three profiles: `operator` (default,
localhost, full access), `stakeholder` (token-gated curated view), and
`both` (mixed — operator on `/`, stakeholder on `/stakeholder`).

```bash
# Default: operator profile, host 127.0.0.1, port 7420
orch dashboard --project-root /path/to/your-project
open http://127.0.0.1:7420
```

Flags:

```bash
orch dashboard --port 8080 --host 0.0.0.0            # expose on LAN (careful!)
orch dashboard --reload                              # uvicorn --reload for dev
orch dashboard --config /path/to/custom-config.yaml

# Sprint E-2 — profile split for public stakeholder URLs
orch dashboard --profile stakeholder --token "$(openssl rand -hex 32)"
orch dashboard --profile both        # operator + stakeholder on one port
```

Endpoints:

| Path | Operator | Stakeholder | What |
|---|:--:|:--:|---|
| `/` | ✅ | ❌ 403 | task table (Jira-style) |
| `/kanban` | ✅ | ❌ 403 | phase-grouped kanban view |
| `/metrics` | ✅ | ❌ 403 | cost / spend metrics per model, per day |
| `/logs` | ✅ | ❌ 403 | live event stream (SSE) |
| `/api/tasks` | ✅ | ❌ 403 | JSON dump of all tasks |
| `/api/task/{id}` | ✅ | ❌ 403 | JSON detail for one task |
| `/api/metrics` | ✅ | ❌ 403 | JSON metrics |
| `/api/budgets` | ✅ | ❌ 403 | per-provider budget snapshot |
| `/api/events/stream` | ✅ | ❌ 403 | SSE stream of events |
| `/snapshot` | ✅ | ❌ 403 | full JSON dump, downloadable |
| `/stakeholder` | ✅ (both) | ✅ | curated HTML — progress, milestones, ETA |
| `/stakeholder/summary` | ✅ (both) | ✅ | curated JSON — same fields as HTML |

The dashboard reads `orchestrator/state/spend-*.jsonl` and
`orchestrator/state/events-*.jsonl` directly — no separate DB, no writes.

Full stakeholder / Cloudflare Tunnel walkthrough:
[docs/DASHBOARD-PROFILES.md](docs/DASHBOARD-PROFILES.md).

---

## Configuration

Three YAML files at the project's `orchestrator/` directory OR in the
installed package's directory (project takes priority).

### `config.yaml` — runtime knobs

```yaml
concurrency:
  global_max: 8                    # hard cap across all backends
  per_provider:
    claude: 3
    codex: 2
    opencode: 3
  per_file: 1                      # only one dispatch per declared file
strict_files_phases: [10]          # phases where unauthorized edits get git-checkout'd
default_timeout_multiplier: 1.5    # timeout = estimateHours * mult * 3600s
budget:
  per_dispatch_usd: 5.00           # kill switch per dispatch (in USD)
retry:
  backoff_seconds: 5               # wall-clock delay before retry-once
  rate_limit_backoff_seconds: 60   # longer backoff on RATE_LIMIT failures
spec_root: specs                   # prefix for Task.spec_ref in prompts
budgets_config: budgets.yaml       # Sprint 7 — see below
budgets_preset: conservative
```

### `model_router.yaml` — model → backend routing

Maps every `Task.model` string to `(backend, cli_model, tier)`:

```yaml
"claude-opus-4-7":
  backend: claude
  cli_model: claude-opus-4-7
  tier: premium
  is_premium: true

"opencode-go/glm-5.1":
  backend: opencode
  cli_model: glm-5.1
  tier: cheap
  is_premium: false
  fallback_cli_model: glm-5.0      # version-drift fallback (FR-D-7)
```

Startup validates every task points to a real entry — unrouted model = exit 1.

---

## Budget guardrails (Sprint 7)

Long unattended `--mode auto` runs can burn through your subscription quota
(Claude Max ~800K tokens / 5h, Codex Pro similar, opencode plans vary),
cutting you off from your own interactive terminal for hours.

`orch` tracks token usage per provider in a rolling window from
`state/spend-*.jsonl` and pauses dispatches when usage crosses a configurable
threshold. When ALL providers are capped, the main loop sleeps until the
earliest reset (chunked to 30s so `SIGINT` stays responsive).

Presets shipped in `budgets.yaml`:

| Preset | claude threshold | codex threshold | opencode threshold | When to use |
|---|---|---|---|---|
| `conservative` (default) | 60% | 60% | 70% | Anytime — safe |
| `aggressive` | 90% | 90% | 95% | Overnight runs — no interactive use |
| `shared` | 40% | 40% | 60% | You're also coding — reserves capacity for you |

Select a preset (highest priority first):

```bash
orch --budgets-preset aggressive        # 1. CLI flag
ORCH_BUDGETS_PRESET=shared orch         # 2. Env var
# 3. `budgets_preset: aggressive` in config.yaml
```

**Calibration is empirical.** The shipped `token_budget` values are estimates.
Real procedure:
1. Start with `conservative`.
2. Run a week. Note when Anthropic / OpenAI / opencode actually rate-limits
   you in real usage.
3. Set `token_budget = tokens_consumed_before_cutoff * 0.9`.
4. Bump `threshold_pct` up gradually (60 → 75 → 85) as you gain confidence.

**Disable entirely**: delete or rename `budgets.yaml`. The gate turns off and
everything works exactly like a pre-Sprint 7 orch.

Two new event types get emitted:
- `budget_skip` — one dispatch was skipped because its provider was capped
- `budget_pause` — the whole loop paused because ALL providers were capped

Watch them live: `tail -f orchestrator/state/events-*.jsonl | grep budget`.

---

## Atomizer — markdown → tasks.json

You can maintain tasks as markdown specs and let `orch atomize` generate the
JSON. Format documented in [`docs/SPEC-FORMAT.md`](docs/SPEC-FORMAT.md).

Minimal spec:

```markdown
# F1 — Auth

## F1.1 — Package: authentication

### F1.1.T1 — Setup del package

- **Modelo**: opencode-go/glm-5.1
- **Estimación**: 1h
- **Razón**: Boilerplate simple.
- **Dependencies**:
- **Files**:
  - `packages/auth/pubspec.yaml`

### F1.1.T2 — Domain entities

- **Modelo**: claude-sonnet-4-6
- **Estimación**: 2h
- **Razón**: Necesita razonamiento de tipos.
- **Dependencies**: F1.1.T1
- **Files**:
  - `packages/auth/lib/src/domain/entities.dart`
```

Run:

```bash
# Preview diff
orch atomize --file specs/f1-auth.md

# Apply
orch atomize --file specs/f1-auth.md --apply
```

Idempotent: re-running only adds new task IDs; existing tasks aren't touched.
Every `--apply` also writes a `tasks.json.bak-<timestamp>` backup unless you
pass `--no-backup`.

---

## State directory layout

Everything under `orchestrator/state/` is gitignored — pure runtime.

```
orchestrator/state/
├── .lock                          # flock — protects concurrent orch instances
├── task-locks/                    # optional per-task locks (--task-locks)
├── run-<uuid>.json                # per-run operational state
├── events-<uuid>.jsonl            # observability event stream
├── spend-<YYYY-MM-DD>.jsonl       # cost log — dashboard + budget gate read this
├── logs/<task-id>.log             # captured stdout/stderr per dispatch
├── prompts/<run-id>/<task-id>.txt # rendered prompts (replay/debug)
└── index.json                     # rebuilt after every write — dashboard uses it
```

You can `rm -rf` the whole `state/` dir to reset (only tasks.json holds
persistent progress).

### Optional SQLite backend (Sprint B, v0.3+)

Set `state.backend: sqlite` in `config.yaml` to swap the file layout for a
single multitenant `orch.db` file. Full walk-through — including migration,
rollback, and known limits — lives in
[`docs/SQLITE-BACKEND.md`](docs/SQLITE-BACKEND.md).

---

## Concurrent instances on disjoint tasks

Default flock (`state/.lock`) is single-writer. Add `--task-locks` to swap
for per-task locks — multiple `orch` instances can then work on disjoint
tasks simultaneously:

```bash
orch --only 'F1-*' --task-locks &
orch --only 'F2-*' --task-locks &
```

Overlap on the same task id → the second instance silently skips it.

---

## Exit codes

| Code | Meaning |
|---|---|
| `0` | clean drain — all reachable tasks done, none blocked |
| `1` | config error / unrouted model / dependency cycle / blocked tasks at end |
| `2` | project layout invalid (`tasks.json` or `scripts/` missing) |
| `3` | flock contention — another `orch` holds `state/.lock` |
| `130` | `SIGINT` during graceful drain |

Live status snapshot (needs `jq`):

```bash
./status.sh /path/to/your-project
# → last 5 events, in-flight tasks, recent commands per agent
```

---

## Development

```bash
git clone https://github.com/hectorcanaimero/orch.git
cd orch
python3 -m venv .venv && source .venv/bin/activate
pip install -e '.[dev]'

# Full suite (301 tests + 19 skipped when jinja2 not installed)
pytest

# Or via the helper
./scripts/check.sh

# Individual module
pytest orchestrator/tests/test_budget.py -v
```

---

## History

Originally built as the task orchestrator for the Rupies v2 monorepo rewrite
(334 tasks across 6 phases, Flutter + Supabase + Edge Functions). Extracted
into a standalone tool that any tasks.json-shaped DAG can consume.

Full SDD trail (proposal → spec → design → tasks → verify → archive) preserved
in [`docs/history/`](docs/history/) as engineering-decision provenance.

## License

MIT — see [`LICENSE`](LICENSE).
