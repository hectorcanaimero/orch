# orch

**Build AI-powered products. Deliver progress to your stakeholder.**

> *The open-source orchestrator that dispatches tasks to Claude / Codex / opencode CLIs,
> tracks budget + progress, and gives your client a live dashboard from day one —
> without you writing a single status report.*

```bash
pipx install https://github.com/hectorcanaimero/orch/releases/latest/download/orchestrator-0.6.1-py3-none-any.whl
```

---

## The problem every AI-builder has

You're building something with Claude or opencode. The client pings you on Slack every other day:
*"How's it going?"* — and you pause real work to write a status update.

Every other AI tool in the market solves the builder's problem. None of them solve the
**delivery** problem — showing your client that work is actually moving.

| Tool | Builder has visibility | Client/PM has visibility |
|---|:---:|:---:|
| LangChain / LangGraph | ✅ | ❌ |
| CrewAI / AutoGen | ✅ | ❌ |
| Claude Code / Codex CLI | ✅ | ❌ |
| Aider / Cursor | ✅ | ❌ |
| **orch** | ✅ | **✅** |

orch is the only tool in this space that ships a **client-facing stakeholder dashboard** built-in.

---

## What it looks like in practice

```bash
# 1. Init the project
orch init ~/work/client-chatbot

# 2. Fill tasks.json from your specs (or let Claude do it)
orch atomize --file specs/f0-foundation.md --apply

# 3. Start the dashboard — both operator and stakeholder on one port
export ORCH_DASHBOARD_TOKEN="$(openssl rand -hex 16)"
orch dashboard --profile both --token "$ORCH_DASHBOARD_TOKEN" &

# 4. Expose it publicly via tunnel (bore, autossh, or cloudflared)
orch tunnel start
# → https://chatbot-client.bore.pub

# 5. Message your client — once, never again
echo "Hey María — your project dashboard: https://chatbot-client.bore.pub?token=$ORCH_DASHBOARD_TOKEN"
echo "It updates live as tasks complete. ETA and spend visible in real time."

# 6. Run
orch --mode auto
```

That last message? Your client sent it to their CTO. You spent zero time on it.

---

## Who it's for

**Freelancers building for clients** — deliver a live progress URL on day one instead of
weekly status emails. The client sees phase completion, ETA, and total AI spend (rounded
to the nearest $0.50, never raw cost data). You see everything.

**Dev agencies** — one `orch` instance per client project, each with its own token and
tunnel subdomain. The CEO of the agency gets a portfolio view; each client gets their own
scoped URL.

**Product teams reporting to investors** — weekly AI spend, features shipped, blocked
tasks, ETA. Auto-generated. Forwardable to the board.

---

## What the stakeholder dashboard shows

The `/stakeholder/summary` view is a curated read-only window into the project:

- **Phase progress** — % complete per phase, task counts (done / in-progress / blocked / total)
- **Milestones** — phase completion events with timestamps
- **ETA** — computed from remaining estimate hours at historical velocity
- **Total spend** — rounded up to nearest $0.50, no per-model breakdown
- **Project documents** — PRD, spec, architecture docs rendered in Markdown

What it **never** shows: per-model spend, raw logs, per-task exit codes, provider names,
API keys, or any operator-only state. Every operator route returns `403` in stakeholder mode.

---

## Core features

### DAG dispatcher
Walks a `tasks.json` graph. When a task's dependencies are done and there's capacity,
spawns the right CLI (`claude` / `codex` / `opencode`) for that task's model. Handles
per-provider concurrency caps, retries with backoff, per-task timeouts, SIGTERM + kill group
on Ctrl-C.

### Budget guardrails
Tracks token usage per provider in a rolling window. Pauses dispatches when a provider
crosses its threshold. Presets: `conservative` (default, 60%), `aggressive` (90%),
`shared` (40% — when you're also coding interactively). Prevents long unattended runs
from locking you out of your own Claude terminal.

### Live dashboard (operator view)
Full-access React SPA at `http://127.0.0.1:7420`. Pages: Summary, Kanban, List, Board,
Architecture (auto-generated diagram), Doctor (preflight probes), Tunnel manager,
Metrics, Logs. Live SSE event stream updates the UI without polling.

### Tunnel manager
Supervised tunnel process (bore, autossh, or cloudflared) managed by the dashboard.
Start / stop from the UI or CLI. URL extracted from process stdout and displayed for
copy-paste. Survives the dashboard restart.

### Architecture generator
Calls the `archify` skill via Claude to generate a project architecture diagram from
your specs. Versioned snapshots — roll back to see how the architecture evolved.

### Documents view
Renders `docs/`, `specs/`, and `openspec/` markdown files in the dashboard. Both
operator and stakeholder can browse PRDs, feature specs, architecture decision records.

### Findings loop
Agents capture bugs / ideas / blockers as they work. You review and publish them to
GitHub as issues. Closes the agent→maintainer feedback loop without leaving the terminal.

---

## Install

Requires Python 3.11+ and at least one of `claude`, `codex`, or `opencode` on your PATH,
authenticated with a subscription.

```bash
# Recommended — prebuilt wheel from GitHub Releases (SPA already embedded, no Node needed)
pipx install https://github.com/hectorcanaimero/orch/releases/latest/download/orchestrator-0.6.1-py3-none-any.whl

# Self-update
orch upgrade

# From source (contributors)
pipx install --force git+https://github.com/hectorcanaimero/orch.git@main
```

Verify:

```bash
orch list          # all subcommands
orch --help        # main-loop flags
orch doctor        # environment probe (backends, scripts, state, jq)
```

---

## Quick start

```bash
# Scaffold a project (interactive wizard)
orch init

# Or batch mode
orch init ~/work/my-app

# Preflight check
orch doctor
orch validate

# Dry-run — enumerate the plan, spawn nothing
orch --project-root ~/work/my-app --dry-run

# Run with approval prompts on critical tasks
orch --project-root ~/work/my-app --mode semi

# Run fully unattended
orch --project-root ~/work/my-app --mode auto

# Dashboard
orch dashboard --project-root ~/work/my-app
open http://127.0.0.1:7420
```

---

## Generating tasks from specs

Two paths:

**A) Write specs in markdown, let orch atomize them:**

```bash
# Preview
orch atomize --file specs/f0-foundation.md

# Apply (writes tasks.json + .bak-<ts> backup)
orch atomize --file specs/f0-foundation.md --apply
```

Format documented in [`docs/SPEC-FORMAT.md`](docs/SPEC-FORMAT.md).

**B) Use Spec-Driven Development with Claude Code:**

```bash
orch init ~/work/my-app --sdd    # also creates openspec/ layout
# Then in Claude Code:
# /orch-plan <feature idea>       # full pipeline PRD → ARCH → SPEC → TASKS
# /orch-tasks                     # invokes orch atomize --apply (diff-first)
```

---

## Delivering to your client — step by step

Full walkthrough: [**docs/DELIVERING-TO-STAKEHOLDERS.md**](docs/DELIVERING-TO-STAKEHOLDERS.md)

Short version:

```bash
# dashboard.yaml in your project root
cat > dashboard.yaml << EOF
server:
  host: 127.0.0.1
  port: 7420
profile: both
token: "$(openssl rand -hex 16)"
EOF

orch dashboard &
orch tunnel start          # get a public URL
# Send the ?token= URL to your client
```

---

## Dashboard profiles

| Profile | Who | Auth | What they see |
|---|---|---|---|
| `operator` | You (default) | None | Everything |
| `stakeholder` | Your client | Bearer token | Curated progress, ETA, spend, docs |
| `both` | Mixed | Token on `/stakeholder/*` only | Operator paths open, stakeholder paths gated |

```bash
orch dashboard --profile stakeholder --token "$(openssl rand -hex 32)"
orch dashboard --profile both        # one process, both views
```

Full guide: [docs/DASHBOARD-PROFILES.md](docs/DASHBOARD-PROFILES.md).

---

## Configuration

Three YAML files at `orchestrator/` in your project (or packaged defaults if absent):

| File | Controls |
|---|---|
| `config.yaml` | Concurrency caps, timeouts, spec root, budget preset |
| `model_router.yaml` | Maps `task.model` strings to CLI backend + model |
| `budgets.yaml` | Per-provider token thresholds and window duration |
| `dashboard.yaml` | Profile, token, tunnel config, port/host |

`orch init` creates all four with sensible defaults. Edit only what you need.

---

## Subcommands

```
orch [FLAGS]              Run the main dispatch loop
orch init PATH            Scaffold a new project
orch atomize              Markdown specs → tasks.json
orch dashboard            Launch the read-only dashboard
orch upgrade              Self-update to the latest release
orch doctor               Preflight probes (backends, scripts, state)
orch validate             Static graph validation (schema, deps, cycles)
orch status               Project status table
orch tasks                Task listing (ID · STATUS · BACKEND · DEPS)
orch events ID            Tail events for one task
orch logs ID              Tail the per-task log file
orch graph                Emit a self-contained HTML/SVG plan graph
orch findings <verb>      Dogfooding loop (capture/list/review/publish)
orch arch <verb>          Architecture diagram via archify skill
orch migrate              Migrate JSONL state → SQLite
orch list                 Print this list
```

---

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Clean drain — all reachable tasks done, none blocked |
| `1` | Config error / unrouted model / dep cycle / blocked tasks |
| `2` | Project layout invalid (`tasks.json` or `scripts/` missing) |
| `3` | Flock contention — another `orch` holds `state/.lock` |
| `130` | SIGINT during graceful drain |

---

## Development

```bash
git clone https://github.com/hectorcanaimero/orch.git && cd orch
python3 -m venv .venv && source .venv/bin/activate
pip install -e '.[dev]'
pytest                  # 1026 tests
./scripts/check.sh      # lint + type-check + tests
```

---

## Docs

- [Manual (English)](docs/MANUAL.en.md) · [Español](docs/MANUAL.es.md) · [Português](docs/MANUAL.pt.md)
- [Delivering to stakeholders](docs/DELIVERING-TO-STAKEHOLDERS.md) ← **start here if you have a client**
- [Dashboard profiles](docs/DASHBOARD-PROFILES.md)
- [Preflight guide](docs/PREFLIGHT.md)
- [Spec format](docs/SPEC-FORMAT.md)
- [SQLite backend](docs/SQLITE-BACKEND.md)
- [Dogfooding loop](docs/DOGFOODING.md)
- [Observability](docs/OBSERVABILITY.md)

---

## License

MIT — see [LICENSE](LICENSE).
