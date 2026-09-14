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

`orch init` writes `.github/workflows/orch-ci.yml` (never over an existing
one) and fills `test_command` from what the repo is made of when the config
still has the packaged `pytest`: `go.mod` → `go test ./...`; `package.json`
→ `pnpm test` with `pnpm-lock.yaml`, `yarn test` with `yarn.lock`, else
`npm test`. A template's own command is kept. The workflow's setup steps
follow the command's first word (`go`, `pnpm`, `yarn`, `npm`; anything else
gets Python). Both are plain files afterwards: edit them freely.

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

A side channel, not a record: the event log is still the source of truth for
what happened. Both channels are off by default, and a notifier with neither
configured costs nothing — `orch run` builds one either way rather than
branching on whether you wanted it.

```yaml
notifications:
  slack_webhook: ""      # empty disables the channel
  discord_webhook: ""
  timeout_s: 5
```

| Key | Default | What it does |
|---|---|---|
| `slack_webhook` | `""` (off) | Incoming-webhook URL. Posted as `{"text": ...}` |
| `discord_webhook` | `""` (off) | Webhook URL. Posted as `{"content": ...}` |
| `timeout_s` | `5` | Per-POST timeout. A webhook that does not answer is given up on, never waited out |

**A webhook URL is a credential.** Anyone holding it can post to your channel,
so it belongs in a config file you do not commit — and orch never writes one to
a log line, not even truncated, which is why a failed POST logs
`channel=text` rather than the URL.

Only the two things an operator would otherwise have to go looking for are
announced:

| When | Message |
|---|---|
| a task is blocked | `:no_entry: orch: task ``F1.T3`` blocked — <first line of the reason>` |
| a task is blocked after CI kept failing | `:warning: orch: task ``F1.T3`` blocked after 3 CI attempt(s) (<pr url>)` |

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
`dashboard.summary_language`, and the one figure it rounds are in
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
