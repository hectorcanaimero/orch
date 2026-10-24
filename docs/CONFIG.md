# `config.yaml` reference

Every key orch reads, what it does, and what happens to the keys it no longer
reads.

This documents the **Go** loader (`internal/config`). The Python loader
accepts a superset — see "Keys that are ignored" for the difference and why
it is a warning rather than an error.

The file lives at `.orchestrator/config.yaml` in the project root. Every key
is optional: a project with no config at all gets the defaults below, which is
what `orch init` produces when you pick no template.

---

## Where values come from

Last wins:

1. the defaults in this document
2. `.orchestrator/config.yaml`
3. `dashboard.yaml` at the project root, deep-merged — a compatibility path
   for projects scaffolded before H-2. `orch init` no longer writes one.

`orch config show` prints the effective values **and the files they came
from**, which is the fastest way to answer "why is orch doing that?".

> **Duplicate keys.** YAML allows the same key twice; the last one silently
> replaces the first, taking its whole subtree with it. orch matches that
> behaviour — its own packaged config had a duplicate `dashboard:` for
> several releases — but **warns** when it sees one. If a value you edited
> seems to have no effect, check for a second definition further down.

---

## Keys

### `concurrency`

How many dispatches may be in flight.

```yaml
concurrency:
  global_max: 6          # hard ceiling across every backend
  per_provider:          # second gate, per backend name
    claude: 3
    codex: 2
    opencode: 3
  per_file: 0            # cap per declared file; 0 = off
```

### `spec_root`

```yaml
spec_root: specs         # default
```

Joined to each task's `specRef` to build the `Spec ref (READ FIRST):` line of
the dispatch prompt. A task with `specRef: f0.md` under the default gets
`specs/f0.md`. Point it at wherever your specs live — `docs/specs`, `sdd/`,
whatever.

Before a dispatch, orch looks for the spec where the agent will run (#341,
#342). In worktree mode, a spec the project has and the task's worktree does
not — never committed and pushed to `base_branch` — blocks the task with
`spec_missing: …`; push it, then `orch task-status <id> todo`. A spec that is
nowhere, or has no heading for the ref's `#anchor`, dispatches with a prompt
that says so. `orch atomize` warns about unpushed specs and `orch validate`
reports every dangling `specRef` as a `spec.missing` warning.

### `state`

```yaml
state:
  backend: sqlite        # the only value
  sqlite_path: null      # relative to the state dir, or absolute
```

`sqlite_path` empty means `<state dir>/orch.db`.

> **`backend: file` is an error.** The JSONL backend was removed. orch refuses
> to start rather than open an empty database and report a project with no
> history. Run `orch migrate` to import the JSONL, then drop the key.

### `dispatch`

```yaml
dispatch:
  worktree_mode: true    # each task runs in its own git worktree
  base_branch: main
  worktree_setup: ""     # e.g. "pnpm install --frozen-lockfile"
  isolate_compose: true  # each task gets its own docker compose project
  tdd: false             # agents work test-first
```

**`tdd`** (default `false`, Go only) makes every task test-first. The
dispatch prompt gains one constraint: write each acceptance criterion's test
with the repository's own framework, see it fail for the expected reason, then
write the least code that makes it pass, and name the tests in the done note.
That is the `orch-tdd` skill (`orch install-skills`) in four lines, so it
reaches agents that never installed it. With [`verify`](#verify--new) on, a
change that touches no test file is sent back to its agent before anything
runs. A test file is one named the way its ecosystem names tests
(`*_test.go`, `*.test.ts`, `*.spec.tsx`, `test_*.py`, `*Test.java`, …) or
under a `tests/`, `test/`, `__tests__/` or `spec/` directory. Off, the prompt
is unchanged.

**`worktree_setup`** is a shell command orch runs inside each fresh worktree
before the agent starts, and again when a CI retry recreates it. A worktree is
a clean checkout with no `node_modules` or `.venv`, so without it every
agent's first test run fails and the agent has to install from the lockfile on
its own (#324). Use the command your CI installs with: `pnpm install
--frozen-lockfile`, `npm ci`, `uv sync --frozen`, `go mod download`. It runs
with `sh -c` in the worktree, for at most 10 minutes; a non-zero exit blocks
the task with the tail of its output, and the worktree is removed. Empty (the
default) runs nothing. Go only.

**`isolate_compose`** (default `true`, Go only) gives each dispatched task its
own docker compose project. Parallel tasks run in sibling worktrees of one
repository, and with a fixed top-level `name:` in the compose file every one
of them used the same project: one task's `docker compose up` recreated the
containers another was testing against, and its `down` removed them (#343).
With it on, orch sets `COMPOSE_PROJECT_NAME=<project>-<task-id>` (lowercased,
anything but `a-z0-9_-` turned into `-`, so `usebot` / `F1.T2` is
`usebot-f1-t2`) in the agent's environment and in `worktree_setup`'s,
replacing any value your shell exported. It beats a top-level `name:`; only
`-p` beats it, so the dispatch prompt gains one line telling the agent its
project, not to pass `-p`/`--project-name`, and not to stop or remove another
compose project's containers. Off, orch leaves `COMPOSE_PROJECT_NAME` and the
prompt alone.

Every task, isolated or not, also gets **`ORCH_TASK_ID`** and
**`ORCH_TASK_SLOT`**: a small integer (1, 2, …) no other task running in the
same `orch run` holds, the lowest free one, given back when the task finishes.
A project name does not stop two tasks from binding the same host port, and
orch does not rewrite compose files, so the convention for published ports is
a variable with a default, set per task from the slot:

```yaml
# docker-compose.yml
services:
  api:
    ports:
      - "${API_PORT:-3001}:3001"   # 3001 in your own checkout
```

```yaml
# .orchestrator/config.yaml — compose reads .env from the project directory;
# keep .env gitignored, or orch's auto-commit picks it up.
dispatch:
  worktree_setup: >-
    pnpm install --frozen-lockfile &&
    echo "API_PORT=$((3001 + ORCH_TASK_SLOT))" >> .env
```

Slot 1 is the first task, so the base port stays your own stack's. `orch
doctor` warns (`compose.isolation`) about compose files at the root or one
directory down that set a fixed `name:` or publish a fixed host port.

With `worktree_mode` on and no git repo — or no remote — orch **degrades and
says so** rather than failing: see `orch doctor`. With no remote and
`vcs.provider: github`, each task's work stays on its local `orch/<task-id>`
branch and nothing reaches the base; `vcs.provider: local` merges it in (see
`vcs` below).

**Where worktrees live.** Each task gets `<project>.worktrees/<task-id>/`, a
directory *next to* the project, on branch `orch/<task-id>`:
`/work/usebot` dispatches into `/work/usebot.worktrees/F1.T1`. Not inside the
checkout, because Node, TypeScript and pnpm look for `node_modules` and
`pnpm-workspace.yaml` in every ancestor directory, and a worktree under the
project found the project's own (#249). So the project's parent directory must
be writable, and a project at the filesystem root or one whose name ends in
`.worktrees` is refused: move it, or turn `worktree_mode` off.

- A dispatched agent gets `ORCH_PROJECT_ROOT` and `ORCH_PROJECT_ID` in its
  environment, so every `orch` it runs (`orch mcp`, the task scripts) reaches
  the project's database rather than bootstrapping one in the worktree. Running
  `orch` yourself from inside `<project>.worktrees/<task-id>/` also resolves
  the project, as long as it holds `.orchestrator/`.
- A task's worktree starts from the remote's `base_branch` (freshly fetched)
  plus the branch `orch/<dep>` of every dependency the base does not contain
  yet, since a dependency is done as soon as its agent says so, usually before
  its PR merges (#322). A dependency whose work is already in the base is not
  merged again (#345): its PR is merged, its branch is gone from the remote
  (deleted after the merge — its local copy is not used instead), or the base
  contains its tip. Each skip is logged at INFO. A dependency branch that
  conflicts with the base otherwise blocks the task at dispatch, naming the
  dependency: resolve its PR, then `orch task-status <id> todo` to retry.
- A branch `orch/<task-id>` left by an earlier attempt is deleted before the
  new worktree is made, logged at INFO, once no worktree has it checked out
  (one whose directory was deleted by hand is pruned first). If a worktree that
  still exists has it checked out — most likely another `orch run` — the task
  blocks with a reason naming that path (#345).
- Worktrees an older orch left inside the project, in `.worktrees/<task-id>/`,
  still resolve the same way. `orch run` removes one when it dispatches that
  task again, and `orch doctor` lists the rest (`worktree.orphans`). The
  scaffolded `.gitignore` keeps ignoring `.worktrees/` for them.

**Claude permissions in a worktree.** `claude` runs with
`--permission-mode acceptEdits`: every tool that is not allow-listed (Bash,
WebFetch, `mcp__orch__*`…) is refused without a prompt, and the run still ends
`success`. A worktree only holds what git tracks, so a repo that ignores
`.claude/` would give the agent no allow-list at all. orch passes the project
root's `.claude/settings.json` and `.claude/settings.local.json` with
`--settings` when the worktree lacks them. Refused calls are logged and land
in the outcome event's `permission_denials`. A minimal allow-list for
dispatched agents:

```json
{
  "permissions": {
    "allow": ["Bash(git status:*)", "Bash(git diff:*)", "Bash(git log:*)",
              "Bash(pnpm:*)", "Bash(go test:*)", "Bash(scripts/task-*.sh:*)",
              "WebFetch", "WebSearch", "mcp__orch"],
    "deny": ["Bash(git push:*)"]
  }
}
```

### `vcs`

```yaml
vcs:
  provider: github       # github | gitlab | local
  host: github.com
  auto_pr: true          # open a PR per finished task
  ci_max_retries: 1      # CI-triggered re-dispatches before blocking
  ci_poll_interval_s: 30
```

Needs `gh` (or `glab`) authenticated. Without it, PRs are skipped with a
warning; worktrees still work.

`provider: local` (#455) is for a repository with no remote or no `gh`: with
`dispatch.worktree_mode` on, each task that finishes and passes the local
verify is squash-merged into `dispatch.base_branch` in the repository itself,
one at a time, and its branch deleted — nothing is pushed, no PR is opened,
`auto_pr`, `host` and the `ci_*` keys are ignored. A task whose work conflicts
with the base goes back to its agent with the base merged in
(`retry.conflict`), then blocks naming the files. `orch init` writes `local`
when the repository has no remote or `gh` is not on PATH; a project without
the key keeps `github`. Moving to GitHub later: push the base branch, set
`provider: github`. See the manual's "Starting without GitHub".

### `github`

```yaml
github:
  test_command: pytest   # what the generated CI workflow runs
  auto_merge: false      # requires branch protection you control
```

`orch init` writes `.github/workflows/orch-ci.yml` (never over an existing
one) and fills `test_command` from what the repo is made of when the config
still has the packaged `pytest`: `go.mod` → `go test ./...`; `package.json`
→ its root `check` script if it has one, else `pnpm -r --if-present test`
with `pnpm-lock.yaml`, `yarn test` with `yarn.lock`, else `npm run test
--if-present`; `pubspec.yaml` → `flutter analyze && flutter test`;
`pyproject.toml`, `requirements.txt` or `setup.py` → `pytest`. A repo with
none of those (an empty one, scaffolded before its code) gets
`test_command: ""` and a placeholder workflow that passes and says to edit it
or run `orch ci setup` — not Python (#348). A template's own command is kept,
including a Python template's `pytest`. The workflow's setup steps follow the
command's first word (`go`, `pnpm`, `yarn`, `npm`, `flutter`; anything else
gets Python). Both are plain files afterwards: edit them freely.

With `auto_merge: true`, a green PR is merged before its task is finished,
so dependents start from a base that has its code. A merge refused because
the base just moved is retried (up to 5 attempts over about 2½ minutes); a PR
behind a base that requires up-to-date branches is updated (`gh pr
update-branch`) and merged once its CI passes again. One that never merges
finishes the task with the PR open, and the `pr_auto_merge_failed` event
carries gh's error (#347).

### `retry`

The policy the reaper applies to a failed dispatch. Two forms, and they
compose: the scalars set the baseline, a per-class block overrides it.

```yaml
retry:
  max_attempts: 2               # baseline for every class
  backoff_seconds: 5            # baseline
  rate_limit_backoff_seconds: 60  # seeds rate_limit's backoff

  rate_limit:                   # per failure class
    max_attempts: 5
  version_drift:
    max_attempts: 0             # never retry: a rerun reproduces it
    escalate_model: true
```

Classes: `transient`, `timeout`, `rate_limit`, `version_drift`, `ci_failed`.
Each takes `max_attempts`, `backoff_seconds` and `escalate_model`.

`conflict` is the one class outside the reaper: a PR the CI poller finds
conflicting with its base (#438). Only `max_attempts` is read. **A config
without it is off** (`0`), so upgrading never starts pushing to PRs or paying
for agents unasked; `orch init` and `orch start` write `1` into a new
project's config.

```yaml
retry:
  conflict:
    max_attempts: 1   # agent dispatches to resolve a conflicting PR
```

On a conflict orch rebuilds the task's worktree from its PR branch (its work
kept) and merges the freshly fetched `dispatch.base_branch` into it — a merge,
not a rebase, since orch squash-merges PRs. A clean merge is pushed and CI is
polled again, with no agent and without spending an attempt. A merge that stops
on conflicts dispatches the agent in that worktree, its prompt listing the
conflicting files and the base commits that touched them; local verify runs as
usual before the push to the same PR. Past `max_attempts` the task is blocked
as before, the reason naming the files. `max_attempts: 0` (or no key) turns
the whole thing off, clean merges included: nothing is pushed or dispatched
and a conflicting PR blocks its task exactly as before #438. Each attempt is a
`ci_conflict_retry` event in the task's activity. The count lives in the
running `orch run`, so a new run grants the attempts again.

`max_attempts: 0` means **never retry this class** — which is why an absent
key and an explicit zero are different things.

A config with only the scalars behaves exactly as it did before per-class
rules existed.

### `budget` and budget presets

```yaml
budget:
  per_dispatch_usd: 5.0  # default 5.0; see below for what writing it changes

budgets_config: budgets.yaml
budgets_preset: conservative
typical_dispatch_tokens: 200000
```

Two different guardrails:

- **The rolling window** (`budgets.yaml`) rations each provider's tokens across
  a run. It is **orch's own guardrail, not your provider plan's quota**: a
  ChatGPT or Claude subscription's 5-hour limit is the provider's, and orch
  cannot see it — set `token_budget` and `window_hours` below what your plan
  allows, and the provider may still cut you off first (#417). Every
  dispatch's tokens are summed per provider over the preset's
  `window_hours`: input and output (reasoning included: codex and agy count it
  in their output, and orch adds opencode's, which it reports apart) at full
  weight, prompt-cache reads at 10%
  and cache writes at 125% of an input token (what Anthropic bills for them;
  claude and opencode report the breakdown). Spend rows keep the raw counts,
  and the Budget page shows both; `orch report usage` splits them by kind,
  model, task and attempt. When the weighted sum reaches `threshold_pct` of `token_budget`, orch
  stops sending that provider new tasks (it writes a `budget_skip` event, and
  `orch status` shows `defer_reason: blocked-by-budget:<provider> until <reset>`);
  when every configured provider is capped the run sleeps until the earliest
  reset (`budget_pause`). A provider the preset does not name is never gated.
  A dispatch whose provider reports no usage at all (gemini) counts as
  `typical_dispatch_tokens`, not as zero.
- **`budget.per_dispatch_usd`** is a per-task limit (default 5.0). A task that
  has already cost that much does not get the attempt-3 escalation — that is
  all the default does. **Only when the key is written in config.yaml** is it
  also passed to claude as `--max-budget-usd` on each attempt (the YAML key's
  presence decides, not its value: writing `5.0` sends the cap). `orch init`
  leaves it commented out. It is not a project-wide USD limit; orch has none.

`budgets_config` is looked for next to config.yaml first, then at the project
root; `orch init` writes the packaged presets to `.orchestrator/budgets.yaml`.
A missing file means no window guardrail: `orch doctor` warns, and the
dashboard's Budget page says so. `orch run`, `orch doctor`, MCP and the
dashboard all read `budgets_config` and `budgets_preset` the same way; there
is no flag or environment variable for the preset. `typical_dispatch_tokens`
also drives doctor's warning when a preset's window cannot fit two dispatches.

To change a provider's window without opening the file, `orch budget set
<provider> --tokens N --window-hours H --threshold PCT` edits the active preset
in place (comments kept), and the dashboard's Providers and quota page offers
the same as **Edit limits**.

### `dashboard`

```yaml
dashboard:
  profile: operator              # operator | stakeholder | both
  token: ""                      # REQUIRED when profile is not operator
  show_spend_to_stakeholder: false
  language: es                   # en | es | pt — what the client reads
```

| Key | Default | What it does |
|---|---|---|
| `language` | `es` | The language of everything a client reads: the executive summary, the blocker reasons, the client portal and `orch report pdf`. `en`, `es` or `pt` (Brazilian Portuguese; `pt-BR` is accepted). Any other value is refused when the config loads — it used to become Spanish without a word. The operator dashboard does not follow it: it starts in the browser's language (en, es or pt, else English) and has its own language button. |
| `summary_language` | — | The older name of `language`, still read. When both are set, `language` wins. |

A non-operator profile with an empty token is refused at startup: it would
401 every request, which is a confusing way to find out. See
[`DASHBOARD-PROFILES.md`](DASHBOARD-PROFILES.md).

### `notifications`

A side channel, not a record: the event log is still the source of truth for
what happened. Both channels are off by default, and a notifier with neither
configured costs nothing — `orch run` builds one either way rather than
branching on whether you wanted it.

```yaml
notifications:
  slack_webhook: ""      # empty disables the channel
  discord_webhook: ""
  timeout_s: 5
  budget_alerts: true    # Go only; nothing is sent without a webhook
  budget_alert_pct: 80   # share of a provider's cap, not of token_budget
```

| Key | Default | What it does |
|---|---|---|
| `slack_webhook` | `""` (off) | Incoming-webhook URL. Posted as `{"text": ...}` |
| `discord_webhook` | `""` (off) | Webhook URL. Posted as `{"content": ...}` |
| `timeout_s` | `5` | Per-POST timeout. A webhook that does not answer is given up on, never waited out |
| `budget_alerts` | `true` | Announce a provider's budget window near and at its cap during `orch run`. On by default because it only does anything once a webhook is set and `budgets.yaml` exists |
| `budget_alert_pct` | `80` | When the first budget alert goes out, as a percentage of the provider's **cap** (`threshold_pct` of `token_budget`, the point where the gate starts deferring). Must be in (0, 100]; anything else is refused when the config loads |

**A webhook URL is a credential.** Anyone holding it can post to your channel,
so it belongs in a config file you do not commit — and orch never writes one to
a log line, not even truncated, which is why a failed POST logs
`channel=text` rather than the URL.

Only the things an operator would otherwise have to go looking for are
announced:

| When | Message |
|---|---|
| a task is blocked | `:no_entry: orch: task ``F1.T3`` blocked — <first line of the reason>` |
| a task is blocked after CI kept failing | `:warning: orch: task ``F1.T3`` blocked after 3 CI attempt(s) (<pr url>)` |
| a live `orch run` made no progress for 30 minutes (Go only, #255), once per stall | `:hourglass: orch: no progress for 30m0s — waiting on CI on F1.T3 (<pr url>)` |
| a provider's weighted budget window reaches `budget_alert_pct` of its cap (Go only) | `:warning: orch: claude is at 80% of its budget cap (384,000 of 480,000 tokens in its 5h window).` |
| it reaches the cap and new dispatches to it wait (Go only) | `:octagonal_sign: orch: claude reached its budget cap (481,200 of 480,000 tokens in its 5h window). New claude dispatches wait until about 2026-09-16 14:05 UTC. 2 task(s) waiting.` |
| an `orch run` loop ends (Go only, #456) | `orch: Run <id> finished: 3 done, 1 blocked.` then the rest of `orch report summary`: what was built, why each blocked task stopped, and the next steps with their commands |

The budget alerts use the same weighted count the gate does (see
[`budget`](#budget-and-budget-presets)), add ", part of it estimated" when a
provider that reports no usage was counted as `typical_dispatch_tokens`, and go
out at most once per provider and level per window length — even across a
restarted `orch run`, which reads the `budget_alert` events the earlier run
wrote. A window that drops back below the line and climbs again inside the
same window length is not announced a second time. `orch notify test --budget`
sends an example.

Not successes, and not retries: a task that will try again has not given up,
and a message per attempt is how a team mutes the channel — after which the
blocks go unseen too. The reason is cut to its first line and 200 characters;
the full text is in `orch events` and in the task's log.

Failures are silent by design — a 500, a 404, a refused connection or a hang
is logged once and the dispatch loop carries on. A broken webhook never
reaches it.

Two commands use these channels directly rather than through a run:
`orch notify test` proves a webhook before a run depends on it (exit 0 when a
channel accepted, 1 when none did), and `orch notify digest --send` posts the
stakeholder digest. `digest` is worth cron-ing and orch will not do it for
you — it has no daemon:

```
0 9 * * MON  orch notify digest --send --project-root /path/to/project
```

The digest's wording, its `--language` override of
`dashboard.language`, and the one figure it rounds are in
[`CLI.md`](CLI.md)'s `notify digest` row.

### `presentation`

```yaml
presentation:
  status_labels:
    backlog: Planificado
    todo: Por hacer
    in_progress: En progreso
    in-progress: En progreso
    done: Entregado
    blocked: Bloqueado
    skipped: Omitido
```

Display only. Internal status values never change. Both `in_progress` and
`in-progress` are listed because the two spellings appear in different
surfaces.

#### `presentation.branding` — new (G8.4)

White-label. What a client sees on the three surfaces built for them: the
stakeholder snapshot, the bundle that renders it, and `orch report pdf`. An
agency sets this once and sends a link with their own mark on it.

```yaml
presentation:
  branding:
    name: "Acme Digital"              # replaces the project name for the CLIENT
    logo: assets/acme.png             # a local file, or a data: URI
    accent_color: "#ff6600"           # #rgb or #rrggbb
    footer: "Confidential — Acme Digital"
```

Every field is optional, and **the whole block is optional in a way that is
tested**: with none of it set, the snapshot JSON and the PDF are byte-identical
to what they produced before this feature existed. Nothing you already publish
changes by upgrading.

| Key | Notes |
|---|---|
| `name` | What the client reads. The project's own `meta.project` is untouched — this replaces it in client-facing headers only. |
| `logo` | A path to a local file, or a `data:` URI. A path is **read and embedded at build time**: the snapshot has to stand alone, because a viewer holding that JSON cannot reach your filesystem. **PNG or JPEG only** — the PDF renderer draws raster images, and a logo that appeared on the web but not on the printed page would be worse than one that says which formats work. Capped at 128 KiB, since the logo travels inside a document a live viewer re-fetches on a timer. The format is detected from the file's own bytes, so a PNG named `.jpg` works and a text file named `.png` is refused at startup. |
| `accent_color` | `#rgb` or `#rrggbb`. Anything else is a **startup error**, not a value quietly ignored: branding that does not apply is invisible, and you would go looking at the browser, the PDF and your cache before suspecting the spelling. |
| `footer` | One line at the bottom of the page. |

### `publish` — new (G6.3)

Where the stakeholder snapshot goes. Read by [`orch publish`](CLI.md); every
key here is the default for the flag of the same name, and a flag that is not
passed never overwrites it.

```yaml
publish:
  interval_s: 60         # seconds between --watch checks (default 30)
  to: dir                # dir | git | cloud
  dir: public            # output directory for `to: dir`, relative to the project root
  git_branch: gh-pages   # branch replaced and pushed for `to: git`
```

| Key | Notes |
|---|---|
| `interval_s` | How often `--watch` rebuilds the snapshot and compares it. It re-publishes only when the **content** changed — `generated_at` is excluded from the comparison, or every tick would be a change and `to: git` would grow one commit per interval. |
| `to` | `dir` writes the site to a directory; `git` replaces a branch with it and pushes; `cloud` uploads it to your orch-cloud Worker (G8.1, [`CLOUD.md`](CLOUD.md)). **There is no `publish.cloud_url` key, on purpose**: the Worker's URL and every token live in `~/.orch/credentials`, written by `orch cloud login`, because `config.yaml` is committed and a Worker belongs to an operator, not to a project. CI uses the `ORCH_CLOUD_URL` and `ORCH_CLOUD_PUBLISH_TOKEN` env vars instead. `dir` and `git_branch` are ignored for `to: cloud`. |
| `dir` | Relative paths resolve against the project root, not the working directory — `orch publish --project-root ../other` is a normal thing to type. |
| `git_branch` | The branch is treated as **output, not history**: every publish replaces its whole tree, so an asset a previous export emitted and this one does not is gone rather than left serving. It is created as an **orphan** the first time, so the published site shares no history with the source and a static host serving it never serves your repository. |

### `tunnel`

Share the dashboard from its **Share** panel, through one of two programs,
picked each time you start a share:

- **Cloudflare quick tunnel** (`cloudflared`): a public
  `https://<random>.trycloudflare.com` address, with no Cloudflare account,
  login or DNS. `orch dashboard --tunnel` starts this one.
- **Tailscale** (`tailscale`): `serve` makes the dashboard reachable only
  inside your tailnet; `funnel` makes it public. Both use your node's
  `https://<machine>.<tailnet>.ts.net` name and run in the foreground, so
  stopping the share removes it. Tailscale must be installed and logged in
  (`sudo tailscale up`), the tailnet needs HTTPS certificates on, and
  `funnel` also needs Funnel allowed in the tailnet's policy — when it is
  not, tailscale prints the link to allow it, and the panel shows that
  output.

A program that is missing is shown with the install steps for your system
(and `orch dashboard --tunnel` prints cloudflared's when it is missing). The
panel also shows the repository and branch this dashboard serves and its
local address.

```yaml
tunnel:
  enabled: false
  url_parse_timeout_s: 30   # how long to wait for the provider's URL line
```

| Key | Notes |
|---|---|
| `enabled` | The whole configuration. Nothing is published until you start the tunnel. |
| `url_parse_timeout_s` | Seconds before the status reports `url_parse_timeout` (the tunnel keeps running, and a URL that arrives later clears it). `0` or unset means 30. |

**Every request through the tunnel needs a token.** While the tunnel is up,
a request that does not come straight from this machine (a loopback peer, a
loopback `Host`, and none of the headers Cloudflare or a proxy add) must
carry one of the tokens minted when the tunnel started, which are in the two
links orch prints and the Share panel shows: the **full dashboard** link's
token opens everything; the **client** link's token — like the project's
stakeholder token — reaches only the stakeholder allow-list. Both links open
the same dashboard; on the client's, `/api/whoami` answers `stakeholder`, so
the client sees only the pages on the allow-list, and cannot turn the link
into your view by editing the path. Both
change on every start, so stopping the tunnel revokes them. The operator
profile, which asks nothing of the person at the keyboard, is never what a
stranger with the URL gets.

**Removed: `provider`, `command`, `args`, `url_regex`, `stop_timeout_s`.**
autossh (Pinggy) and bore are gone. A config that still sets `provider`,
`command`, `args` or `url_regex` under `tunnel:` is **refused at load** with
the fix (delete them, keep `enabled: true`) — a warning would let `--tunnel`
start something other than what the file asks for. `stop_timeout_s` only
warns: the stop grace period is fixed.

Quick-tunnel limits worth knowing: the address changes on every start;
Cloudflare caps a quick tunnel at 200 concurrent requests and offers it for
testing, not production; it does not carry server-sent events, so a
dashboard opened through it refreshes every 15 s instead of live; and a
`config.yml`/`config.yaml` in `~/.cloudflared/` (or `/etc/cloudflared/`)
stops it from starting — `orch doctor` and the Tunnel page say so. For a
stable address, run a named Cloudflare Tunnel yourself
([`DASHBOARD-PROFILES.md`](DASHBOARD-PROFILES.md)).

### `telemetry` — new (G8.6)

Anonymous, opt-in usage pings. Off unless `enabled: true`, and the
standard `DO_NOT_TRACK` environment variable always overrides this either
way — see [`TELEMETRY.md`](TELEMETRY.md) for the exact field list and why
each one is safe to send.

```yaml
telemetry:
  enabled: false
  endpoint: ""    # empty uses the built-in default (unset until a real
                   # collector exists — see internal/telemetry's own TODO)
```

### `sync` — new (G8.7)

Which GitHub issues `orch sync issues` ingests. **Go only** — Python has no
`sync` verb, so no Python config has ever carried this key.

```yaml
sync:
  issues_label: orch:task   # the label a human puts on "this is work for orch"
```

`--label` overrides it per run. The label is namespaced on purpose: a tracker
already uses `bug` and `enhancement` for its own triage, and orch must not
claim one of those.

`auto-reported` is **refused** here and on the flag, with the reason printed:
that is the label orch puts on issues it *files itself*, so ingesting it would
turn orch's own bug reports into tasks for orch, and every run would add more.

### `report_findings` — new

```yaml
report_findings:
  enabled: true    # what `orch init` writes; absent = false
```

Turns on the MCP tool `orch_report_finding` (see [`MCP.md`](MCP.md)): an agent
that hits a bug in orch, or sees an improvement or a missing feature, files it
as an `auto-reported` issue with your `gh` login. It also adds a closing,
optional "Feedback about orch itself" block to every dispatch prompt, so
agents are asked for those ideas rather than only reporting what blocked them. **On in new projects,
off when the key is absent** (#399): `orch init` and `orch start` write `enabled: true` with a comment
saying the issues are public and under your login, and print a notice saying so and how to turn it off;
`--no-report-findings` writes `false`. A config without the block — any project from before, or written
by hand — stays off, so upgrading orch never starts publishing under your account. Not the Python line's `findings:` block, which
was removed and still warns.

### `portal` — new

```yaml
portal:
  documents:            # globs relative to the project root
    - docs/prd/*.md
    - docs/arch/*.md
    - specs/f*.md
```

The markdown files the client portal (`orch publish`) lets a client read, in
the order listed. The default is what the orch-plan pipeline writes. Each
document is shown by its first `# ` heading (or its file name), with YAML
frontmatter stripped and **never with its path**. A glob or a file that
resolves outside the project is ignored, a file over 256 KB is skipped, and
the whole set stops at 2 MB. `documents: []` publishes none. Specs name tasks
(`### F1.1.T1`), so drop `specs/f*.md` if those should stay internal.

### `verify` — new

```yaml
verify:
  enabled: true         # what `orch init` writes; absent = false
  commands: []          # [] = what `orch ci setup` detects
  review: true
  reviewer: ""          # "" = another installed provider than the task's
  review_model: ""
  checklist: ""         # "" = .orch/review.md, then .github/orch-review.md, then the built-in one
  app:                  # the task's own app, for E2E; empty start = none
    start: ""           # e.g. docker compose up -d --build
    url: ""             # e.g. http://localhost:$((3000 + ORCH_TASK_SLOT))
    stop: ""            # e.g. docker compose down -v --remove-orphans
    ready_timeout_s: 120
  e2e: []               # run against the app, with ORCH_APP_URL set
  e2e_report: ""        # Playwright's JSON report, relative to the worktree
  max_attempts: 2
  timeout_s: 600
```

**On in new projects, off when the block is absent**: `orch init` writes
`enabled: true` and `review: true`, blank or from a template, and a config
without the block (any project from before) stays off, so upgrading orch never
starts running checks and reviews on a project that did not ask. With it on,
`orch ci setup` adds no AI review job unless you pass `--review`, because the
same diff is already reviewed before the push.

A local gate between an agent's clean exit and the push, in the task's own
worktree. **Worktree mode only**: without `dispatch.worktree_mode` orch prints
a note and verifies nothing.

1. **Commands.** Each runs at the worktree root through `sh -c`, in order,
   bounded by `timeout_s`. `commands: []` runs what `orch ci setup` detects:
   install, lint, typecheck, test and build, per package, where the repository
   has them. A detected command whose tool is not installed is skipped. So
   is a configured one (`commands`, `app.start`, `e2e`) that `sh` cannot find
   (exit 127, e.g. a typo): it is the config, not the agent's work, and the
   run log warns with the key to fix. Without `app.start`, the E2E is skipped.
   The same mistake is said before the run (#440): `orch doctor` fails a
   `config.commands` check per configured command (these keys, plus
   `app.stop` and `dispatch.worktree_setup`) whose first word is not on PATH,
   or, for a path, not in the repository — `verify.app.start: "tue" not found
   on PATH` — `orch run` prints it as a warning before the first dispatch, and
   the dashboard's Tools page lists it.
2. **Review.** With `review: true`, after the commands pass, a provider reads
   the task's diff against the base, read-only, and applies the checklist,
   the same review `orch ci review` runs. The default reviewer is the first
   installed of claude, gemini, codex and opencode that did not write the
   task. Only a blocking finding fails the work. A review that cannot complete
   (no other provider, a provider that is down, an answer that does not
   parse) is logged and the work passes.

Every command runs with the agent's own environment: `ORCH_TASK_ID`,
`ORCH_TASK_SLOT` and, with `dispatch.isolate_compose`, the task's
`COMPOSE_PROJECT_NAME`. So two tasks verifying at once do not share
containers, and their ports can follow the slot as in
[`dispatch`](#dispatch).

**The task's app and E2E.** After the commands pass, `app.start` brings up
the app of the task's worktree, and verify waits up to `ready_timeout_s` for
`app.url` to answer below 500 (a 404 counts as up). The shell expands
`url` with the task's environment, so `$((3000 + ORCH_TASK_SLOT))` gives each
task its own port. Then each `e2e` command runs with `ORCH_APP_URL` set to
that URL, and `app.stop` always runs at the end, even after a failure. An app
that does not start, or never answers, is sent back like a failing test. If
the cause is the machine and not the change, the agent can block the task and
say so. `e2e` without `app` runs too, for a suite that starts its own server
(Playwright's `webServer`).

**E2E results.** With `e2e_report` set to the file Playwright's JSON reporter
writes, verify reads it after the E2E commands, pass or fail, and keeps the
attempt's results under
`.orchestrator/state/<project>/e2e/<run>/<task>/attempt-<n>/`. That
directory holds a `report.json` in orch's own shape and the files each test
left: screenshots, videos, traces. Each attempt is recorded as an
`e2e_report` event with its counts. When the tests fail, the agent is told
which ones, where they failed (`file:line`), the step each one was in, the
first lines of the error, and the screenshot and trace it left in the
worktree, instead of the raw output. Playwright's report is removed from the
worktree after reading, so it is never committed. The dashboard's
**Delivery → Tests** page shows every run, newest first, with each failing
test's evidence. Keep `test-results/`
gitignored, as `npm init playwright` does. A recipe:

```ts
// playwright.config.ts
export default defineConfig({
  reporter: [['line'], ['json', { outputFile: 'e2e-report.json' }]],
  use: {
    baseURL: process.env.ORCH_APP_URL,
    screenshot: 'only-on-failure', video: 'retain-on-failure', trace: 'retain-on-failure',
  },
});
```

```yaml
verify:
  e2e: [pnpm exec playwright test]
  e2e_report: e2e-report.json
```

Work that fails is not pushed. The task goes back to its agent in the same
worktree, with the failure in `.orch-ci-feedback.md` and the prompt telling it
to read that file first. After `max_attempts` send-backs, the task is blocked
with the failure as its reason. Each failed attempt is a `verify_failed` event
(`sent_back: false` on the one that blocked the task), with the review's
findings uncut under `findings` (`severity`, `file`, `line`, `title`, `detail`);
each passed attempt is a `verify_passed` event with the `commands` that ran,
their `duration_s`, and the `reviewer` and `review_model`. The dashboard's
Changes tab and `orch report summary` read them.
Without the block, or with `enabled: false`, a clean exit is pushed as before.

### `update` — new (#448)

```yaml
update:
  check_clis: true   # default
```

`orch doctor` (`backend.<name>.version`), `orch run` (a `note:` line at start)
and the dashboard's Tools page compare each agent CLI's installed version with
its npm package's latest release, read once a day per package and cached in
`~/.orch/cli-versions.json`, and print the command that updates it for how it
was installed. A registry that cannot be reached, or a CLI with no feed (agy,
muse), is "unknown": no warning, never an error. orch never updates a CLI
itself; after updating, `orch router refresh` shows the routes its new models
bring. `false` turns the comparison off; `ORCH_NO_UPDATE_CHECK=1` turns it off
along with orch's own update notice.

### `knowledge` — new (#451)

```yaml
knowledge:
  enabled: true        # what `orch init` / `orch start` write; absent = false
  max_in_prompt: 5     # lessons a dispatch prompt carries; 0 or absent = 5
  auto_capture: true   # orch drafts lessons itself; absent = true
```

The project's memory. A lesson is one markdown file under `.orch/knowledge/`
— front matter `id`, `title`, `area` (paths or globs), `tags`, `source`
(`agent` | `verify` | `review` | `conflict` | `operator`), `task`, `created`,
then a body of a few lines: what happened and what worked. They are versioned
and reviewed in PRs like `tasks.json` and the specs; the FTS5 index in
`orch.db` (migration 011) is a cache rebuilt from the files on every search.

- **Prompt.** Each dispatch carries a `Project knowledge` block with up to
  `max_in_prompt` lessons from the task's own working dir (its worktree: the
  base branch plus its merged dependencies): first those whose `area` covers
  one of the task's `files` or its spec file, then those its title and
  description find by BM25; each lesson is cut at 15 lines / 600 characters
  and the block at 4,000. A line in the constraints invites the agent to record
  what it learned with `orch_learn`. With no lessons picked the block is not
  rendered, and with `enabled` off the prompt is byte-identical to before.
- **Agents** record a lesson with the MCP tool `orch_learn` and search with
  `orch_knowledge_search` ([`MCP.md`](MCP.md)). The file is written in the
  task's worktree, so it is committed with the work and reaches the base
  through the task's PR.
- **orch** (`auto_capture`, worktree mode) drafts a lesson in the task's
  branch when an attempt the local verify rejected is followed by one that
  passes (`source: verify`: what was rejected, and the agent's note), and when
  the agent resolves a conflicting PR (`source: conflict`: the files, the base
  commits, the agent's note). The PR's reviewer keeps, edits or drops it.
- **The operator** uses `orch knowledge add|list|show|rm|stats`
  ([`CLI.md`](CLI.md)); `stats` compares first-try success and retries per
  task before and since the first lesson.

**On in new projects, off when the key is absent**, like `retry.conflict`: a
config written before #451 never starts writing files into the repository.

### Misc

```yaml
strict_files_phases: []        # phases where `files[]` is enforced
default_timeout_multiplier: 1.5  # timeout = estimateHours × this
```

---

## `model_router.yaml` — the routes

`.orchestrator/model_router.yaml` maps each `tasks.json` model key to a
backend CLI, its `cli_model`, a `tier` (premium | standard | cheap), and
optionally `fallback_cli_model`, `escalation_model`, `failover`, `effort`
and `service_tier`. The field list is in the packaged file's header
(`internal/scaffold/defaults/model_router.yaml`); `orch router` maintains it.

**`failover` (#439).** An ordered list of other route keys in the same file
(`failover: [codex/gpt-5.4, gemini/gemini-2.5-flash]`). When the route's
provider runs out of quota — a `rate_limit` failure, or claude's
`rate_limit_event` saying the window is rejected — the retry goes to the
first entry whose provider is not exhausted, with no backoff and in the same
worktree, instead of waiting out `retry.rate_limit`. The provider stays
exhausted until its reset (claude's `resetsAt`, else
`retry.rate_limit_backoff_seconds`, or `retry.rate_limit.backoff_seconds`
when set), and later tasks of the run on that route dispatch straight to the
failover. A target without a `failover` of its own continues down the rest of
the list. The spend row, the `dispatch`/`retry` events and the dashboard name
the backend that ran. An entry that is not a route, or the route itself, is a
load error. Without `failover` nothing changes.

**Where a new project's routes come from (#397).** `orch init` and `orch
start` begin from orch's packaged routes for the CLIs on PATH, and ask each
installed CLI that can list its models which ones it runs now:

| CLI | Model list | |
|---|---|---|
| `opencode` | `opencode models` | one `provider/model` per line, only the providers you configured |
| `agy` | `agy models` | `id<TAB>name` per line |
| `codex` | `codex debug models` | the bundled catalog, JSON; the models it shows (`visibility: list`) |
| `claude`, `gemini`, `muse` | none | packaged routes as they are |

The list only **removes**: a packaged route whose `cli_model` its CLI does
not list is not a tier default and is not seeded (an `escalation_model` that
pointed at it is dropped with it). Tier, effort and fallbacks still come from
the packaged route. Listed models with no route are printed, not added. Each
CLI gets 5 seconds; one that fails, hangs or prints nothing readable keeps
its packaged routes, and the summary says so. Discovery writes no config key
and nothing to turn off: offline, it is the packaged router as before.

"Listed" is not "works" — an account may not reach a model its CLI lists —
so afterwards it is a hint, not a gate: `orch doctor`'s `models.available`
warns about routes whose model their CLI no longer lists, and never fails a
run.

---

## Keys that are ignored

orch warns once per key and carries on. It does **not** fail: a config written
by an older version must keep working, which is the whole point of the
compatibility contract.

| Key | Why it is gone |
|---|---|
| `findings.*` | The findings feature was removed |
| `dashboard.board_url` | The ExcaliDash embed was removed |
| `dashboard.kanban` | Kanban defaults moved into the SPA |
| `dashboard.tunnel` | The tunnel moved to a top-level `tunnel:` block, where `enabled: true` is all it needs |
| `tunnel.stop_timeout_s` | The tunnel stops within a fixed grace period |
| `dashboard.server` | Host and port are CLI flags |

Anything else unrecognised gets a generic warning naming the path. That is
usually a typo — `wortree_mode` parses fine as YAML and does nothing.

Delete the keys when convenient. Nothing breaks if you leave them.

---

## Environment variables

Config lives in `config.yaml`. These few things are environment variables
instead, because they belong to one invocation rather than to the project.

| Variable | What it does |
|---|---|
| `ORCH_FAKE_PROVIDER` | Replay canned CLI output instead of running any coding CLI |
| `ORCH_NO_UPDATE_CHECK` | Turns off orch's own update notice and gate and the agent CLIs' version check (`update.check_clis`) |
| `ORCH_NPM_REGISTRY` | The npm registry the agent CLIs' version check reads (default `https://registry.npmjs.org`), for a mirror |
| `DO_NOT_TRACK` | Disables telemetry (G8.6) regardless of `telemetry.enabled` — see [`TELEMETRY.md`](TELEMETRY.md) |

### `ORCH_FAKE_PROVIDER`

Set it to a directory and no provider CLI is executed: every dispatch replays
a file from that directory instead. Nothing is sent to an API and nothing is
spent, while the rest of the dispatch path — the fork, the process group, the
prompt on stdin, the timeout, the parse, the classification, the spend and
event rows — runs exactly as it does in anger.

The directory holds one file per canned response, per backend:

```
<dir>/<backend>/<task id>.out      what the CLI would have printed
<dir>/<backend>/<task id>.exit     its exit code, as decimal text (default 0)
<dir>/<backend>/<task id>.sleep    seconds to sleep first (default 0)
```

A file named `_default` answers any task the directory has no specific file
for, so one response can cover a whole run:

```
mkdir -p /tmp/fake/claude
echo '{"type":"result","subtype":"success","is_error":false,"total_cost_usd":0,"usage":{}}' \
    > /tmp/fake/claude/_default.out
ORCH_FAKE_PROVIDER=/tmp/fake orch run
```

`.sleep` is what makes a timeout reproducible: a response that sleeps longer
than a task's timeout (`estimateHours × default_timeout_multiplier`) drives
the real SIGTERM → 10s → SIGKILL escalation against a real process.

A task with no matching `.out` file and no `_default.out` is an error, not an
empty response. A fake run that silently dispatched nothing would look green
while testing nothing.

---

## Related

- [`SQLITE-BACKEND.md`](SQLITE-BACKEND.md) — the state backend and `orch migrate`
- [`DASHBOARD-PROFILES.md`](DASHBOARD-PROFILES.md) — profiles, tokens, tunnels
- [`PREFLIGHT.md`](PREFLIGHT.md) — what `orch doctor` checks
- [`TELEMETRY.md`](TELEMETRY.md) — the exact field list for the opt-in usage ping
