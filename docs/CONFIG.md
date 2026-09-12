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
```

With `worktree_mode` on and no git repo — or no remote — orch **degrades and
says so** rather than failing: see `orch doctor`.

### `vcs`

```yaml
vcs:
  provider: github       # github | gitlab
  host: github.com
  auto_pr: true          # open a PR per finished task
  ci_max_retries: 1      # CI-triggered re-dispatches before blocking
  ci_poll_interval_s: 30
```

Needs `gh` (or `glab`) authenticated. Without it, PRs are skipped with a
warning; worktrees still work.

### `github`

```yaml
github:
  test_command: pytest   # what the generated CI workflow runs
  auto_merge: false      # requires branch protection you control
```

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

`max_attempts: 0` means **never retry this class** — which is why an absent
key and an explicit zero are different things.

A config with only the scalars behaves exactly as it did before per-class
rules existed.

### `budget` and budget presets

```yaml
budget:
  per_dispatch_usd: 5.0  # passed to the provider CLI as its own cap

budgets_config: budgets.yaml
budgets_preset: conservative
typical_dispatch_tokens: 200000
```

The rolling-window guardrail lives in `budgets.yaml`, not here.
`typical_dispatch_tokens` is used at startup to warn when a preset's window is
too small to fit two dispatches.

### `dashboard`

```yaml
dashboard:
  profile: operator              # operator | stakeholder | both
  token: ""                      # REQUIRED when profile is not operator
  show_spend_to_stakeholder: false
  summary_language: es           # es | en
```

A non-operator profile with an empty token is refused at startup: it would
401 every request, which is a confusing way to find out. See
[`DASHBOARD-PROFILES.md`](DASHBOARD-PROFILES.md).

### `notifications`

```yaml
notifications:
  slack_webhook: ""      # empty disables the channel
  discord_webhook: ""
  timeout_s: 5
```

Failures are silent by design — a broken webhook never reaches the dispatch
loop.

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

### `publish` — new

Where the stakeholder snapshot goes. **Nothing reads this yet**; it ships now
so a project's config does not need rewriting when `orch publish` lands.

```yaml
publish:
  interval_s: 60         # how often --watch re-exports during a run
  to: dir                # dir | git | cloud
  dir: public            # output directory for `to: dir`
  git_branch: gh-pages   # branch pushed for `to: git`
```

### `tunnel` — new (G5.6)

The dashboard's optional public-URL tunnel (Sprint E-5). Two providers only —
`autossh` (Pinggy, over SSH) and `bore` (bore.pub); there is no `cloudflared`.

```yaml
tunnel:
  enabled: false
  provider: autossh       # autossh | bore
  command: autossh        # binary on PATH
  args: []                # empty means "the provider's own defaults"
  url_regex: ""           # empty means "the provider's own pattern"
  url_parse_timeout_s: 30
  stop_timeout_s: 5
```

### Misc

```yaml
strict_files_phases: []        # phases where `files[]` is enforced
default_timeout_multiplier: 1.5  # timeout = estimateHours × this
```

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
| `dashboard.tunnel` | Only autossh and bore remain — configure under `tunnel` |
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
