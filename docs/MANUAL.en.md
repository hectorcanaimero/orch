# orch — Step-by-step user manual

> Also available in: [Español](MANUAL.es.md) · [Português](MANUAL.pt.md)

This manual assumes you use **Claude Code** (or any equivalent CLI with skill
support). The complete workflow is: **chat the feature with Claude → Claude
generates the spec in orch's format → orch atomizes to `tasks.json` → orch
executes → you watch the dashboard**.

**Total setup time**: ~5 minutes.
**Per-feature time**: ~2 minutes of chat + unattended execution.

---

## Contents

1. [Initial setup (once per machine)](#1-initial-setup-once-per-machine)
2. [Create a new project (once per project)](#2-create-a-new-project-once-per-project)
3. [Chat the feature in Claude Code](#3-chat-the-feature-in-claude-code)
4. [How tasks are generated](#4-how-tasks-are-generated)
5. [Preview before running (always)](#5-preview-before-running-always)
6. [Run in auto mode](#6-run-in-auto-mode)
7. [Open the dashboard](#7-open-the-dashboard)
8. [What to watch during a run](#8-what-to-watch-during-a-run)
9. [When something fails](#9-when-something-fails)
10. [Update orch](#10-update-orch)

---

## 1. Initial setup (once per machine)

### Install orch

```bash
# Recommended: isolated venv, `orch` on PATH globally
pipx install git+https://github.com/hectorcanaimero/orch.git

# Verify
orch --help
orch init --help
orch dashboard --help
```

### AI CLIs you need

At least **one** of these three on your PATH, authenticated with a subscription
or API key:

- **`claude`** — Claude Code CLI (Anthropic)
- **`codex`** — GPT Codex CLI (OpenAI)
- **`opencode`** — opencode CLI (multi-provider: DeepSeek, Grok, GLM, MiMo, etc.)

Confirm:

```bash
which claude codex opencode
claude --version
```

If some are missing, orch still works — it will only dispatch tasks to the
backends you have installed. But if your spec calls for `claude-opus-4-7` and
you don't have `claude` installed, orch fails-fast at startup with exit 1.

### SDD skills (optional but recommended)

Verify the skills are installed at `~/.claude/skills/`:

```bash
ls ~/.claude/skills/ | grep -E 'orch|sdd'
# expected:
# orch-plan
# orch-prd
# orch-arch
# orch-spec
# orch-tasks
# sdd-apply
# sdd-archive
# sdd-design
# ...
```

If you don't have them, you can still use orch by writing specs by hand
(see [`SPEC-FORMAT.md`](SPEC-FORMAT.md)) — but the SDD flow is much smoother.

---

## 2. Create a new project (once per project)

```bash
orch init ~/work/my-app --sdd
```

That creates:

```
~/work/my-app/
├── tasks.json                    ← empty skeleton
├── specs/                        ← Claude writes specs here
│   └── README.md                 ← format reference
├── scripts/
│   ├── task-start.sh             ← executable, functional, jq-based
│   ├── task-finish.sh
│   └── task-block.sh
├── orchestrator/
│   ├── state/.gitkeep            ← runtime state (gitignored)
│   ├── config.yaml               ← concurrency, timeouts, retries
│   ├── model_router.yaml         ← model → CLI mapping
│   └── budgets.yaml              ← Sprint 7 guardrails
├── openspec/                     ← SDD (from --sdd flag)
│   ├── README.md
│   ├── changes/                  ← in-flight proposals
│   └── specs/                    ← archived specs (source of truth)
└── .gitignore
```

At the end of init you'll see whether SDD is installed and what to do next:

```
✓ orch project initialized at /Users/you/work/my-app

Next steps:
  1. Write your first spec:
       $EDITOR specs/f0-foundation.md
  2. Preview atomize (dry, shows diff):
       orch atomize --file specs/f0-foundation.md
     Then apply:
       orch atomize --file specs/f0-foundation.md --apply
  ...

Spec-Driven Development:
  ✓ SDD skills detected: orch-plan, orch-spec, orch-tasks, ...
    Use `/sdd-explore <topic>` in Claude Code to design specs.
```

---

## 3. Chat the feature in Claude Code

This is where the magic happens. Open Claude Code **from the project
directory**:

```bash
cd ~/work/my-app
claude
```

You now have three ways to design the feature, from most to least hands-off:

### Option A — Full pipeline with `/orch-plan` (recommended for larger features)

In Claude's chat, type:

```
/orch-plan I want to add authentication with email/password + Google + Apple,
with password reset via email and account verification
```

Claude will run internally:

1. **`/orch-prd`** — generates a PRD (Product Requirements Document) with
   orch-friendly YAML frontmatter
2. **`/orch-arch`** — generates the technical ARCH (Architecture Design)
3. **`/orch-spec`** — generates specs in F<n>.<pkg>.T<n> format (the one
   `orch atomize` knows how to parse)
4. **`/orch-tasks`** — invokes `orch atomize` in **diff-first mode**: shows
   you which NEW tasks will be added to your `tasks.json` and **asks for
   confirmation before writing**

Expected output (Claude will print progressively):

```
[/orch-plan] Generating PRD for "auth email+google+apple"...
✓ openspec/changes/auth/prd.md

[/orch-plan] Generating ARCH...
✓ openspec/changes/auth/arch.md
  Modules: 3 new packages (auth_core, auth_google, auth_apple)
  Key decisions: Riverpod for state, GoRouter for deep links

[/orch-plan] Generating atomizer-ready SPEC...
✓ openspec/changes/auth/spec.md
  15 tasks generated:
    - F1.1.T1: Set up auth_core package
    - F1.1.T2: Domain: entities (User, Credentials, ...)
    - F1.1.T3: Data: AuthRepository interface
    ...

[/orch-tasks] Diff against current tasks.json:
  + 15 new tasks in phase 1
  Models used: claude-sonnet-4-6 (10), opencode-go/glm-5.1 (3), claude-haiku-4-5 (2)
  Total estimate: 24h

Apply? [y/N]
```

You type `y` and Claude runs the real `orch atomize`. **`tasks.json` is now
updated** with the 15 new tasks, `todo` status, correct dependencies,
declared files, assigned models.

### Option B — Granular with `/orch-spec` (when you already know the design)

If you already know WHAT needs to be done and only want Claude to lay out the
spec in the correct format:

```
/orch-spec

I want a Flutter package called auth_core with:
- Domain: entities User, Credentials, AuthMethod
- Data: AuthRepositoryImpl that uses supabase.auth
- Presentation: AuthController with Riverpod
- 3 use cases: signIn, signUp, resetPassword

Models: use claude-sonnet-4-6 for anything domain/data, opencode for pure
tests and boilerplate.

Total estimate: ~8h.
```

Claude returns an atomizer-ready spec. Then:

```
/orch-tasks
```

And merge into `tasks.json`.

### Option C — Manual (when you want full control)

Edit `specs/my-feature.md` by hand following the format:

```markdown
# F1 — Auth

## F1.1 — Package: auth_core

### F1.1.T1 — Set up the package

- **Model**: opencode-go/glm-5.1
- **Estimate**: 30m
- **Reason**: Simple boilerplate.
- **Dependencies**:
- **Files**:
  - `packages/auth_core/pubspec.yaml`
  - `packages/auth_core/lib/auth_core.dart`

### F1.1.T2 — Domain entities

- **Model**: claude-sonnet-4-6
- **Estimate**: 2h
- **Reason**: Type design needs reasoning.
- **Dependencies**: F1.1.T1
- **Files**:
  - `packages/auth_core/lib/src/domain/user.dart`
  - `packages/auth_core/lib/src/domain/credentials.dart`
```

Then in the terminal:

```bash
# Preview (dry-run — shows what will be added without writing)
orch atomize --file specs/my-feature.md

# Apply — writes tasks.json + creates a tasks.json.bak-<ts> backup
orch atomize --file specs/my-feature.md --apply
```

All three flows end at the same place: **`tasks.json` with the new tasks in
`status: backlog`, ready for dispatch**.

---

## 4. How tasks are generated

When `/orch-tasks` (or manual `orch atomize`) runs, it processes the spec
and produces `tasks.json` entries like:

```json
{
  "id": "F1.1.T2",
  "phase": 1,
  "title": "Domain entities",
  "description": "",
  "model": "claude-sonnet-4-6",
  "reason": "Type design needs reasoning.",
  "status": "backlog",
  "dependencies": ["F1.1.T1"],
  "estimateHours": 2.0,
  "files": [
    "packages/auth_core/lib/src/domain/user.dart",
    "packages/auth_core/lib/src/domain/credentials.dart"
  ],
  "specRef": "specs/my-feature.md",
  "comments": []
}
```

Status starts at `backlog` (the atomizer default). orch's main loop promotes
`backlog` → `todo` when dependencies are satisfied, then `todo` →
`in-progress` at dispatch time.

**Atomizer guarantees:**

- **Idempotent**: re-running `atomize` with the same spec doesn't touch
  existing tasks. It only adds IDs that weren't there.
- **Model validation**: if the declared model doesn't exist in
  `model_router.yaml`, orch fails-fast on first startup with exit 1 and
  tells you which task is the offender.
- **Deps preserved as-is**: doesn't validate that they exist (you can declare
  deps on tasks you'll add later).

**What it does NOT guarantee:**

- That `files` are unique across tasks (two tasks can declare the same file
  → orch uses `per_file: 1` from `config.yaml` to only dispatch ONE at a
  time on that file).
- That the DAG has no cycles (orch detects them at startup with exit 1).

---

## 5. Preview before running (always)

**Never run `--mode auto` without seeing the plan first.** Dry-run is free
and shows you exactly what will happen:

```bash
orch --project-root ~/work/my-app --dry-run
```

Output:

```
==== ORCH DRY RUN ====
Project: my-app
Ready tasks: 15
Blocked tasks: 0
Deferred (semi-mode critical): 0

Plan (dispatch order):
  Wave 1 (parallel, no deps):
    F1.1.T1  [opencode/glm-5.1]     Set up auth_core package             0.5h
    F1.2.T1  [opencode/glm-5.1]     Set up auth_google package           0.5h
    F1.3.T1  [opencode/glm-5.1]     Set up auth_apple package            0.5h

  Wave 2 (deps: T1):
    F1.1.T2  [claude/sonnet-4-6]    Domain entities                      2.0h
    F1.2.T2  [claude/sonnet-4-6]    Google OAuth flow                    1.5h
    F1.3.T2  [claude/sonnet-4-6]    Apple Sign-In flow                   1.5h

  Wave 3 (deps: T2):
    ...

Concurrency plan: max 8 in-flight, per-provider caps: claude=3 codex=2 opencode=3
Budget preset: conservative
  claude:   0 / 800000 tokens used (0.0%, threshold 60%)
  codex:    0 / 400000 tokens used (0.0%, threshold 60%)
  opencode: 0 / 2000000 tokens used (0.0%, threshold 70%)

Estimated total: 24h (parallelizable to ~6h wall clock)
Estimated cost: $12-18 USD (opencode ~$0.50, claude ~$14, codex $0)
```

If something doesn't add up — a task with the wrong model, a weird file,
incorrect deps — **this is the time to edit the spec and re-atomize**.

---

## 6. Run in auto mode

When the plan looks right, dispatch:

```bash
# Auto mode — no prompts, dispatches everything
orch --project-root ~/work/my-app --mode auto

# Or with a more aggressive budget preset if you want max throughput
orch --project-root ~/work/my-app --mode auto --budgets-preset aggressive

# Or semi mode — asks before tasks marked as "critical"
orch --project-root ~/work/my-app --mode semi
```

**What you see in the terminal** (auto mode):

```
2026-08-21 14:30:00 INFO project_root=~/work/my-app project_id=my-app config=orchestrator/config.yaml
2026-08-21 14:30:00 INFO budget gate enabled: preset=conservative providers=['claude', 'codex', 'opencode']
2026-08-21 14:30:00 INFO 15 tasks todo, 0 in-flight, 0 done
2026-08-21 14:30:01 INFO dispatch F1.1.T1 → opencode/glm-5.1 (attempt 1)
2026-08-21 14:30:01 INFO dispatch F1.2.T1 → opencode/glm-5.1 (attempt 1)
2026-08-21 14:30:01 INFO dispatch F1.3.T1 → opencode/glm-5.1 (attempt 1)
2026-08-21 14:32:15 INFO success F1.1.T1 (2m14s, 4.2K tokens, $0.001)
2026-08-21 14:32:16 INFO dispatch F1.1.T2 → claude/sonnet-4-6 (attempt 1)
...
```

**Ctrl-C** = graceful drain (waits for in-flight children to finish before
exiting). **Ctrl-C twice** = force kill.

**You can leave it running unattended.** The budget gate makes sure it
doesn't burn your subscription:

- When `claude` reaches 60% (conservative preset threshold) → it pauses
  claude dispatches, keeps going with codex/opencode
- When ALL providers are capped → sleep until the next reset (30s chunks
  so Ctrl-C stays responsive)
- On reset → automatic resume, picks up where it left off

---

## 7. Open the dashboard

**In ANOTHER terminal** (leaving `orch --mode auto` running in the first):

```bash
orch dashboard --project-root ~/work/my-app
```

Output:

```
INFO:     Started server process
INFO:     Waiting for application startup.
INFO:     Application startup complete.
INFO:     Uvicorn running on http://127.0.0.1:7420
```

Open in your browser:

```bash
open http://127.0.0.1:7420
```

### What you see in the dashboard

**Home (`/`)** — Jira-style table with ALL tasks:
- Columns: ID / Title / Phase / Status / Model / Files / Owner
- Filters by phase, status, model
- Live-update via SSE (no need to refresh)
- Click a task → modal with details (deps, comments, latest logs)

**Kanban (`/kanban`)** — Trello-style view grouped by phase:
- Columns: backlog / todo / in-progress / done / blocked
- Cards with model + estimate + progress
- Colors by criticality
- Drag & drop DISABLED (read-only by design)

**Metrics (`/metrics`)** — Cost and burndown:
- Total spend (USD) per day
- Per model (bars)
- Burndown chart (remaining tasks vs time)
- Critical path (longest dep chain)

**Logs (`/logs`)** — Live event feed:
- SSE stream, updates in real time
- Filterable by task-id or event_type
- Shows: dispatch, success, fail, timeout, retry, budget_skip, budget_pause

**Budgets (`/api/budgets` — JSON endpoint or the bar in the UI)**:
```json
{
  "disabled": false,
  "preset": "conservative",
  "providers": {
    "claude": {
      "tokens_used": 240000,
      "token_budget": 800000,
      "usage_pct": 30.0,
      "threshold_pct": 60,
      "window_hours": 5,
      "capped": false,
      "reset_at": null
    },
    "codex": {"tokens_used": 0, "capped": false, ...},
    "opencode": {"tokens_used": 15000, "capped": false, ...}
  }
}
```

In the UI: 3 horizontal bars per provider:
- Green 0-60% → OK
- Amber 60-80% → warning
- Red 80-100% → PAUSED, with countdown to the next reset

---

## 8. What to watch during a run

**"Everything's fine" cheat sheet:**

| Signal | Where | Meaning |
|---|---|---|
| Consistent `success` events | `/logs` | Tasks completing OK |
| Budget bars ≤ 60% green | `/api/budgets` | Healthy consumption |
| Kanban moves left → right | `/kanban` | Normal progress |
| Reasonable cost/hour | `/metrics` | No runaway costs |

**Warning signals:**

| Signal | Where | Action |
|---|---|---|
| Many `retry` events in a row | `/logs` | Possibly rate-limited — check backend |
| `budget_pause` events | `/logs` | All providers capped, sleeping until reset |
| Task stuck in `in-progress` for a long time | `/kanban` | May be hung — check the log |
| `timeout` events | `/logs` | Tune `default_timeout_multiplier` in config.yaml |
| `blocked` tasks piling up | `/kanban` | Check `state/logs/<task>.log` |

**Useful commands from a parallel terminal:**

```bash
# Live tail of all events
tail -f ~/work/my-app/orchestrator/state/events-*.jsonl | jq -r '"\(.ts | .[11:19])  \(.event_type|ascii_upcase)  \(.task_id)  \(.backend)"'

# Single-task log
tail -f ~/work/my-app/orchestrator/state/logs/F1.1.T2.log

# Status snapshot (uses jq)
./status.sh ~/work/my-app
```

---

## 9. When something fails

### Task blocked

1. Open the task modal on the dashboard → look at the latest comment
2. Terminal: `cat ~/work/my-app/orchestrator/state/logs/<task-id>.log | tail -100`
3. Edit the spec or fix the code manually as needed
4. Mark the task as `todo` again:
   ```bash
   jq --arg id "F1.1.T5" '(.tasks[] | select(.id == $id) | .status) = "todo"' \
      tasks.json > tasks.json.tmp && mv tasks.json.tmp tasks.json
   ```
5. Run `orch --mode auto` again — it only picks up `todo` tasks

### Budget capped faster than expected

1. Check the actual consumption: `/api/budgets` on the dashboard
2. If the `token_budget` in `budgets.yaml` is mis-calibrated, bump it up
   (or lower `threshold_pct` for more margin)
3. Changes to `budgets.yaml` are picked up on the next run — no need for a
   restart if it's the same run

### Provider rate limit

Different from the budget gate — this is the real CLI throwing 429 at you.

1. orch catches the failure and does **retry-once with extended backoff** (60s
   default for rate limits, configurable in
   `config.yaml → retry.rate_limit_backoff_seconds`)
2. If it keeps failing, the task ends up `blocked` with the error
3. Typical fix: wait for the reset window (~5h Anthropic, ~3h OpenAI) and
   `orch --mode auto` again

### `orch` won't start — exit 1 with "unrouted model"

Your spec calls for a model that isn't in `model_router.yaml`. The error
tells you which:

```
UnroutedModelError: task F1.1.T2 uses model 'claude-opus-5-0' which is not in router
```

Edit `orchestrator/model_router.yaml`, add:

```yaml
"claude-opus-5-0":
  backend: claude
  cli_model: claude-opus-5-0
  tier: premium
  is_premium: true
```

Then retry.

### `orch` won't start — exit 2 with "project layout invalid"

`tasks.json` or `scripts/task-*.sh` is missing. Run `orch init --force` if
it's a new project, or create what's missing by hand.

### `orch` won't start — exit 3 with "flock contention"

Another `orch` instance is running against the same `state/`. Check:

```bash
lsof ~/work/my-app/orchestrator/state/.lock
```

If it's a zombie instance, kill the PID. If two parallel runs are intentional,
use `--task-locks` on both.

---

## 10. Update orch

When I (or you) push changes to the repo:

```bash
# Try upgrade first
pipx upgrade orchestrator

# If it says "already up to date" but you know there are changes, force:
pipx install --force git+https://github.com/hectorcanaimero/orch.git

# Verify which version you're running
orch --help | head -3
```

**Heads up**: `pipx upgrade` does NOT touch the YAMLs already copied into
your projects (`~/work/my-app/orchestrator/*.yaml`). To get the new defaults
into an old project:

```bash
# Diff first
diff ~/work/my-app/orchestrator/config.yaml \
     $(python3 -c 'import orchestrator, pathlib; print(pathlib.Path(orchestrator.__file__).parent)')/config.yaml

# Apply (⚠️ overwrites your project's custom config)
orch init ~/work/my-app --force
```

It's intentional that overrides aren't clobbered: if you tuned
`budgets.yaml` for a specific project, you don't want an `upgrade` to wipe
it.

---

## Complete workflow — visual summary

```
┌─────────────────────────────────────────────────────────────────┐
│  ONCE PER MACHINE                                               │
│    pipx install git+https://github.com/hectorcanaimero/orch.git │
└─────────────────────────────────────────────────────────────────┘
                            │
                            ▼
┌─────────────────────────────────────────────────────────────────┐
│  ONCE PER PROJECT                                               │
│    orch init ~/work/my-app --sdd                                │
└─────────────────────────────────────────────────────────────────┘
                            │
                            ▼
┌─────────────────────────────────────────────────────────────────┐
│  PER FEATURE                                                    │
│                                                                 │
│  1. In Claude Code (inside the project):                        │
│                                                                 │
│       /orch-plan I want to add email + google auth              │
│                                                                 │
│  2. Claude generates PRD → ARCH → SPEC → proposes diff to       │
│     tasks.json. You confirm with `y`                            │
│                                                                 │
│  3. Preview:                                                    │
│       orch --project-root ~/work/my-app --dry-run               │
│                                                                 │
│  4. Execute:                                                    │
│       orch --project-root ~/work/my-app --mode auto             │
│                                                                 │
│  5. In ANOTHER terminal, dashboard:                             │
│       orch dashboard --project-root ~/work/my-app               │
│       → http://127.0.0.1:7420                                   │
│                                                                 │
│  6. Coffee ☕                                                   │
└─────────────────────────────────────────────────────────────────┘
```

---

## References

- Atomizer spec format: [`SPEC-FORMAT.md`](SPEC-FORMAT.md)
- Project history: [`history/README.md`](history/README.md)
- Full config reference: [`../README.md#configuration`](../README.md#configuration)
- Sprint 7 (budget guardrails): [`../README.md#budget-guardrails-sprint-7`](../README.md#budget-guardrails-sprint-7)

## Feedback

This manual is a living doc. If you hit a case that isn't covered, open an
issue at <https://github.com/hectorcanaimero/orch/issues> or send a PR with
the missing section.
