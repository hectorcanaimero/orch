# Dashboard profiles + public URL setup

The orch dashboard ships in three profiles:

- **`operator`** (default) — every route open, no auth. Meant for localhost.
- **`stakeholder`** — token-gated read-only curated view for external
  viewers. Every operator route returns 403.
- **`both`** — one server serving both. Operator paths stay open (localhost
  responsibility). `/stakeholder/*` is token-gated + curated.

Existing operator setups need to change nothing. Everything below is opt-in.

---

## When to use each

| You want to…                                                    | Profile        |
|-----------------------------------------------------------------|----------------|
| Run the dashboard on your laptop for your own use               | `operator` (default) |
| Share a live progress link with a stakeholder / manager / client | `stakeholder`  |
| Do both simultaneously on one process                           | `both`         |

You cannot skip the token in `stakeholder` mode — the CLI refuses to boot.
See "Setting up the shared secret" below.

---

## Quick start — stakeholder-only

```bash
# 1. Pick a token. Use `openssl rand -hex 32` or similar.
export ORCH_DASHBOARD_TOKEN="$(openssl rand -hex 32)"

# 2. Launch the dashboard in stakeholder mode.
orch dashboard --profile stakeholder

# 3. The dashboard reports:
#    Profile: stakeholder
#    Token auth: ENABLED
#    running on http://127.0.0.1:7420

# 4. The stakeholder opens:
#    http://127.0.0.1:7420/stakeholder?token=<TOKEN>
```

Send that one URL and nothing else. The SPA reads `?token=` on boot, stores
it in `localStorage`, and strips it back out of the address bar with
`history.replaceState` — so the token does not linger in the client's URL
bar, in `document.referrer`, or in a screenshot of their tab. Every later
request carries it as `Authorization: Bearer <TOKEN>`.

Open the URL **without** a token and you get the SPA's token form instead of
a rejection: the static shell (`index.html` and `/assets/*`) is served
without auth on purpose, because a browser cannot attach a token to the
`<script src>` requests the shell makes. Nothing but static files is public
— every data route (`/api/*`, `/stakeholder/summary`, `/snapshot`,
`/logs/stream`) still answers `401 unauthorized` without a valid token, and
`orchestrator/tests/test_dashboard_security_public_shell.py` sweeps the live
route table on every run to keep it that way.

## Quick start — mixed (operator + stakeholder)

```bash
export ORCH_DASHBOARD_TOKEN="$(openssl rand -hex 32)"
orch dashboard --profile both --host 0.0.0.0 --port 7420
```

Now:

- `http://127.0.0.1:7420/` — operator dashboard (no auth, full data).
- `http://127.0.0.1:7420/stakeholder?token=<TOKEN>` — curated view.

If you're binding to `0.0.0.0`, you almost certainly want to put the
process behind a tunnel or reverse proxy. Read on.

---

## Setting up the shared secret

> **Go binary note (G8.2/F3.3).** This whole doc otherwise describes the
> Python dashboard, `ORCH_DASHBOARD_TOKEN` included — the Go binary has no
> such env var; see [`CLI.md`](CLI.md)'s `dashboard` row for its real
> `--token`/`dashboard.token` flags. What's new there: `orch dashboard
> token rotate` generates a token, stores only its SHA-256 hash in the
> project's database, and prints it once; `orch dashboard token show`
> reports where the active token came from (never the token itself). A
> database-stored token **wins unconditionally** over both `--token` and
> `dashboard.token` — ahead of the CLI-flag precedence below, which
> otherwise still applies — specifically so a rotation can't be silently
> undone by a stale `--token` left in a saved command or shell alias.
> The Go precedence is database, then `--token`, then `dashboard.token`.
> Ignoring a given token is not silent: under `stakeholder` or `both`,
> startup prints `[warn] --token is ignored: …` (or `dashboard.token in
> config.yaml is ignored`) with the way out — use the token `orch dashboard
> token rotate` printed, or rotate again — and never either token. It
> also takes effect on an already-running `orch dashboard` with no
> restart: the server re-checks the database on every gated request. A
> plain SHA-256 hash is only as strong as what it's hashing: `rotate`
> generates a random 32-byte token, where that's a non-issue, but a token
> **you** chose and put in `dashboard.token`/`--token` is only as hard to
> guess as that string is — a short or dictionary-word token is
> dictionary-attackable against its hash the same way a weak password
> would be. Use `rotate` rather than hand-picking one if that matters to
> you.

You have three options, in precedence order (first non-empty wins) — this
list is Python's; see the note above for how the Go binary's database
source fits in:

### 1. CLI flag (highest priority)

```bash
orch dashboard --profile stakeholder --token my-secret
```

The flag is copied into `ORCH_DASHBOARD_TOKEN` in the process env so the
underlying config loader sees it too. Use for one-off runs.

### 2. Environment variable

```bash
export ORCH_DASHBOARD_TOKEN=my-secret
orch dashboard --profile stakeholder
```

Best for CI / systemd / launchctl / .envrc setups.

### 3. `config.yaml`

```yaml
# orchestrator/config.yaml
dashboard:
  profile: stakeholder
  token: my-secret
  # Optional — extends the built-in stakeholder allow-list.
  # stakeholder_routes:
  #   - /public-extra/
  #   - some_custom_route_name
```

Boot with just `orch dashboard`. Least surprising for team setups where
one config file is committed alongside `tasks.json`. Keep the token OUT
of git when the config is shared.

---

## What the stakeholder sees

`GET /stakeholder` renders a single curated page with:

- Overall progress percentage + count of tasks by status.
- Milestones: each phase, tasks-done / tasks-total, ✓ when phase is 100%.
- Total spend, rounded UP to the nearest $0.50 (so we never accidentally
  under-report).
- Estimated remaining hours: computed from planned estimate scaled by
  observed plan-vs-actual ratio on completed tasks.

`GET /stakeholder/summary` returns the same fields as JSON — good for a
Slack cron post or embedding in a status page.

What the stakeholder never sees, in either the HTML or the JSON:

- Per-model or per-provider spend.
- Per-task IDs, exit codes, or raw error messages.
- Log content.
- Provider names (`opencode-go`, `claude-sonnet-4-6`, …).

---

## The portfolio view — one process, N projects (G8.5)

```bash
orch dashboard --portfolio '~/projects/*'
```

One process opens every orch project the glob matches and serves:

| Path | What it is |
|---|---|
| `/api/portfolio` | one row per project: counters, velocity, ETA, blocked count and the first three blockers, spend, and the newest event |
| `/p/<project_id>/…` | **that project's own dashboard**, unchanged |
| `/` | the SPA |

**Quote the glob.** The shell expands `~/projects/*` before orch sees it;
quoting is what hands the pattern to orch. A pattern that matches nothing is an
error that says so.

### Each project keeps its own access model

`/p/<project_id>/…` is not a copy of the project's routes — it *is* that
project's server, with the prefix stripped. So a project configured
`profile: stakeholder` still demands its own token on its own routes, resolved
against its own row, and a route missing from that project's allow-list is
still a 403. One process, N independent gates.

### `--portfolio` is operator-only, and that means one specific thing

It refuses a non-operator `--profile`, and **nothing on `/api/portfolio` is
token-gated**. The boundary is the listener, bound to `127.0.0.1` by default —
the same boundary a single-project operator dashboard has today. "Operator
only" here means *there is no stakeholder portfolio*, not *authenticated*.

The asymmetry is real and worth stating: a project whose own routes require a
token still contributes counters to a portfolio row that does not. If that
matters for your setup, do not bind the portfolio beyond localhost.

`--token` and `--tunnel` are refused with `--portfolio` rather than ignored: a
token would suggest a shared one exists, and a tunnel is configured per project
— there is no single project here to take one from.

### What a stakeholder session sees: the client portal

A stakeholder dashboard serves the **client portal** — the same page
`orch publish` writes as a static site — at `/stakeholder/`, and sends every
other page request there with its query intact, so a link shared as
`/?token=…` opens the portal. The portal's files are ungated (they are code,
like the SPA's); its data, `/stakeholder/data.json`, is gated by the token
(`stakeholder_snapshot_json` on the default allow-list) and is built per
request by the same function `orch publish` uses, with
`refresh_interval_s: 30`, so the open page refreshes itself. A wrong or
rotated token shows "this link is no longer valid" in words.

An operator dashboard keeps its SPA at `/` and can open `/stakeholder/` to see
exactly what a client sees. `/api/whoami` still returns a stakeholder session's
allow-list (`routes`), which the SPA uses for its own navigation.

### `/stakeholder/summary`

The one route the stakeholder profile exists for, and the landing page of both
profiles. It is on the default allow-list as `stakeholder_summary_json`, so a
stakeholder session reaches it with its token and nothing else does without
one.

Its body is Python's, field for field — one compiled `web/` bundle serves both
dashboards, so one JSON shape is the architecture rather than a detail. Every
figure comes from `publish/snapshot.Build`, the same function `orch publish`
and `orch report pdf` use, so the executive sentence and the cards cannot
quote different numbers.

`show_spend_to_stakeholder` (off by default) governs **every** spend figure
here: the total, the daily series, and the spend sentence of the summary. With
it off, `spend_rounded_usd` is `null` rather than `0` — "nothing was spent" and
"you may not see it" are different claims — and `spend_by_day` is an empty
list. With it on, the total is rounded **up** to the nearest $0.50, which is
the same step the summary sentence quotes, so the card and the sentence agree.

### Unimplemented data routes are 404, never the SPA

Any path under `/api/` or `/stakeholder/` that no route claims answers **404
with JSON**, rather than falling through to the single-page app:

```
$ curl -s -w '\n[%{http_code} %{content_type}]' localhost:7420/stakeholder/summary
{"error":"not found","path":"/stakeholder/summary"}
[404 application/json]
```

It is a rule rather than a per-route patch because the same bug appeared three
times. A path the Go port has not implemented used to answer **200 with the
SPA's HTML**, and a client fetching JSON then parsed a page of markup: it broke
the portfolio page's "is this a portfolio?" check, and it crashed the landing
page outright (`Cannot read properties of undefined (reading 'done')` — axios
passes the HTML string through and a non-empty string is truthy). A 404 is what
every one of those clients already handles; their error branches exist and were
unreachable while the server answered 200.

Everything outside those two prefixes still reaches the SPA, so a hard refresh
on a client-side route like `/kanban` keeps working.

### Binding beyond localhost needs `--allow-remote`

Because the listener is the boundary, moving it is a decision you make on the
command line:

```
$ orch dashboard --portfolio '/path/to/projects/*' --host 0.0.0.0
--portfolio with --host 0.0.0.0 would expose every project matching
'/path/to/projects/*' on an unauthenticated /api/portfolio: it is
operator-only, which means there is no stakeholder portfolio, NOT that the
route asks for a token. Bind to 127.0.0.1, or pass --allow-remote if that is
what you meant
```

Refused before anything is opened and long before anything listens — a process
that binds and then warns has already exposed what it was warning about.

Running on a box you reach over a network is an ordinary case (it is where
several projects live), so this is opt-in rather than forbidden. With the flag,
the banner says what you exposed, by name:

```
Orch portfolio dashboard running on http://[::]:34998
  3 project(s): billing-api, data-lake, e2e
  Operator profile: nothing on /api/portfolio is token-gated. Each project's own routes under /p/<id>/ keep theirs.
  !! --allow-remote: bound to 0.0.0.0, so anyone who can reach this port sees the counters, blockers and spend of: billing-api, data-lake, e2e
```

`0.0.0.0` and `::` count as remote — they bind every interface — and so does
any hostname that is not `localhost`, which is assumed reachable rather than
resolved: a DNS lookup would make this decision depend on what a resolver
happened to answer. The whole `127.0.0.0/8` range and `::1` are local.

`--allow-remote` is refused without `--portfolio`: a single project's exposure
is decided by its profile and its token, and a second knob for the same
question would be a third story about it.

Putting a tunnel in front (below) is the other way to reach it from elsewhere,
and a stakeholder-profile project is the shape meant for that — the portfolio
is not.

### One broken project does not blank the page

A glob over a working directory matches things that are not projects. Each one
becomes a row rather than a reason to refuse to start:

```
[warn] /path/to/projects/notes: no tasks.json — not an orch project
Orch portfolio dashboard running on http://127.0.0.1:7420
  2 project(s): billing-api, data-lake
  1 unavailable (listed above, and on the page)
```

A project that opens but whose database will not answer is listed too, with
`available: false` and the reason — the counters it *did* produce are kept, so
a failing event log does not throw away the summary.

Two directories that resolve to the same project id (the id is the directory's
base name) collide under `/p/`. The second is reported, naming the first, since
renaming one is the only fix and only you can make it.

---

## Publishing via ephemeral tunnel (Pinggy via autossh)

Sprint E-5 ships a built-in tunnel manager for the "throw a quick link at
a stakeholder" use case. It spawns `autossh` against Pinggy's free tier
under the hood, so you get an ephemeral `https://<random>.a.pinggy.link`
URL without registering a domain, editing DNS, or installing
`cloudflared`.

### When to prefer this over Cloudflare Tunnel

| You want…                                              | Use               |
|--------------------------------------------------------|-------------------|
| A stable URL your stakeholder bookmarks for weeks      | Cloudflare Tunnel |
| A share link for a 30-min live-review call             | Ephemeral tunnel  |
| Custom domain, TLS handled by CF, systemd-managed      | Cloudflare Tunnel |
| No new DNS record, no CF account, just an SSH tunnel   | Ephemeral tunnel  |

The two are not mutually exclusive — Cloudflare Tunnel remains the
production-grade option, the ephemeral tunnel is the low-friction one.

### Prerequisites

- `autossh` installed on the host running the dashboard.
  - macOS: `brew install autossh`
  - Debian/Ubuntu: `sudo apt install autossh`
- Outbound TCP/443 to `a.pinggy.io` from the dashboard host.
- Dashboard running in `operator` (or `both`) profile — the ephemeral
  tunnel is an operator-only feature.

No new Python dependencies are pulled in; the manager is stdlib-only.

### Config example

Add a `tunnel:` section to `dashboard.yaml`. Bundled default ships with
`enabled: false`, so upgrading orch is a no-op — you opt in explicitly.

```yaml
# dashboard.yaml — tunnel manager (Sprint E-5).
tunnel:
  enabled: true
  provider: autossh          # v1 allowlist: only "autossh"
  command: autossh           # MUST match the provider's pinned binary
  args:
    - "-M"
    - "0"
    - "-o"
    - "StrictHostKeyChecking=no"
    - "-o"
    - "ServerAliveInterval=30"
    - "-o"
    - "ExitOnForwardFailure=yes"
    - "-p"
    - "443"
    - "-R"
    - "0:localhost:7420"
    - "a.pinggy.io"
  auto_start: false          # true = spawn on dashboard boot (see below)
  startup_probe_timeout_s: 3 # 1..30
  url_parse_timeout_s: 30    # 5..300
```

If `args:` is omitted the provider default (identical to the block above)
is used — so the minimum viable config is `tunnel: { enabled: true }`.

### Starting the tunnel from the SPA

Navigate to `/tunnel` in the dashboard (there's a dedicated route in the
sidebar). The panel renders three states depending on capabilities:

- **Not available** — `tunnel.enabled: false`, or the request did not
  reach the dashboard via loopback. The panel shows a short explainer
  and stays out of the way.
- **`autossh` missing** — feature enabled but the binary isn't on PATH.
  The panel points at the install line for your OS.
- **Ready** — Start / Stop buttons, live status, a URL with a copy
  button once autossh reports it, and a scrolling log tail.

Click **Start**. The manager spawns `autossh`, tails stdout, extracts the
first Pinggy URL it sees, and surfaces it in the panel within
`url_parse_timeout_s` seconds. Click the URL to copy it.

### Starting the tunnel from the CLI

```bash
# Start.
curl -X POST http://127.0.0.1:7420/api/tunnel/start \
  -H "Authorization: Bearer $ORCH_DASHBOARD_TOKEN"

# Status.
curl -s http://127.0.0.1:7420/api/tunnel/status \
  -H "Authorization: Bearer $ORCH_DASHBOARD_TOKEN" | jq

# Stop.
curl -X POST http://127.0.0.1:7420/api/tunnel/stop \
  -H "Authorization: Bearer $ORCH_DASHBOARD_TOKEN"
```

For unattended boots, set `tunnel.auto_start: true` in `dashboard.yaml`.
The dashboard will bind uvicorn, serve, then self-probe `GET /` against
`http://127.0.0.1:<port>/` with `startup_probe_timeout_s`. On probe
success it invokes the same code path as `POST /api/tunnel/start`. On
probe failure it logs `auto_start_skipped: self_probe_failed` and leaves
state `idle` — the dashboard itself never fails to boot because of the
tunnel.

### Host-gate safety guardrail

This is the load-bearing security property of the tunnel manager. Read
this section even if you skim the rest.

Every request to `/api/tunnel/start`, `/stop`, `/logs`, `/status`
passes three gates in a fixed order — short-circuiting on the first
failure:

| Order | Gate     | Failure response |
|-------|----------|------------------|
| 1     | Config: `tunnel.enabled` is `true`                        | `404 Not Found`  |
| 2     | Profile: caller presents a valid `operator` token         | `403 Forbidden`  |
| 3     | Host: request `Host` header host is `127.0.0.1`, `localhost`, or `[::1]` (port ignored) | `403 Forbidden` |

Gate 3 is the interesting one. A request that reaches the dashboard
**via the tunnel domain itself** (e.g. `Host: xyz.a.pinggy.link`) fails
gate 3 and returns `403` — even with a valid operator token. Two
consequences that matter:

- **You cannot cut the branch you are sitting on.** A stakeholder who
  guesses the operator token and hits `/api/tunnel/stop` over the
  pinggy URL is stopped at the door.
- **A leaked token cannot be used from the tunnel domain to disable the
  tunnel.** All control lives on loopback.

`X-Forwarded-For` and `X-Forwarded-Host` are **explicitly ignored** by
gate 3 — you cannot spoof loopback via a proxy header. This is by design
(TUN-3, resolved decision 2).

The `/api/tunnel/capabilities` probe is intentionally auth-free and
always returns `200`. Its body reports which gate the caller failed:

```json
{
  "enabled": true,
  "provider": "autossh",
  "can_control": false,
  "reason": "host_gate"
}
```

`reason` is one of `ok` / `config_disabled` / `profile_gate` /
`host_gate` / `autossh_missing`. The SPA reads this and renders the
right empty state (e.g. "Access from the local dashboard to manage the
tunnel") instead of the Start button when `can_control` is `false`.

**Reverse-proxy caveat (unsupported in v1).** If you front the dashboard
with nginx / Caddy / Traefik and let it rewrite `Host` to your public
hostname, gate 3 will reject even loopback callers because the proxy
overwrote the header. There is no trusted-proxy allowlist and no XFF
consumption in v1. Use loopback direct (`http://127.0.0.1:7420`) for
tunnel control, and keep the reverse proxy in front of the read-only
stakeholder surface only.

### `orch doctor` integration

`orch doctor` reports two new checks:

| Check          | PASS                                                       | WARN                                           | FAIL                                        |
|----------------|------------------------------------------------------------|------------------------------------------------|---------------------------------------------|
| `tunnel.config` | `dashboard.yaml → tunnel` absent OR validates per schema  | (n/a)                                          | any validation error (message names the key) |
| `tunnel.binary` | provider binary on PATH, OR `tunnel.enabled: false`, OR section absent | `enabled: false` AND binary missing (prep-ahead hint) | `enabled: true` AND binary missing         |

Run `orch doctor` before flipping `enabled: true` in a fresh
environment. `tunnel.config` failures point at the offending YAML key
verbatim; `tunnel.binary` FAIL includes the install command for your OS.

### Rollback

Set `tunnel.enabled: false` (or delete the section). On the next
dashboard restart:

- `/api/tunnel/*` routes return `404` (config gate closed).
- `/tunnel` in the SPA renders the "not available" empty state.
- The tunnel manager module is not instantiated, no subprocess ever
  runs, no lock or PID file is created.

This is the intended rollback path — no code changes, no reinstall.

---

## Publishing via Cloudflare Tunnel

Cloudflare Tunnel gives you a stable public URL that terminates TLS +
proxies to your local orch process, with no port forwarding or exposed
public IP.

### Prerequisites

- A domain on Cloudflare (free tier is fine).
- `cloudflared` installed locally.
  - macOS: `brew install cloudflared`
  - Debian/Ubuntu: `sudo apt install cloudflared`
- Authenticated: `cloudflared tunnel login` (opens browser).

### Step by step

```bash
# 1. Create the tunnel. `orch-progress` is any short slug.
cloudflared tunnel create orch-progress
# → writes ~/.cloudflared/<UUID>.json (credentials)

# 2. Point a DNS name at the tunnel.
cloudflared tunnel route dns orch-progress orch.example.com

# 3. Write the tunnel config. Path = ~/.cloudflared/config.yml
cat > ~/.cloudflared/config.yml <<'EOF'
tunnel: orch-progress
credentials-file: /Users/YOU/.cloudflared/<UUID>.json

ingress:
  - hostname: orch.example.com
    service: http://127.0.0.1:7420
  - service: http_status:404
EOF

# 4. Start orch dashboard in stakeholder or both mode.
export ORCH_DASHBOARD_TOKEN="$(openssl rand -hex 32)"
orch dashboard --profile stakeholder --host 127.0.0.1 --port 7420 &

# 5. Run the tunnel daemon.
cloudflared tunnel run orch-progress
```

Your stakeholder now uses:

```
https://orch.example.com/stakeholder?token=<TOKEN>
```

Bookmark-friendly. TLS handled by Cloudflare. No exposed local port.

### Upgrade path — Cloudflare Access email PIN

The shared token is a fine MVP but has the usual footgun (leak = replace
everywhere). Cloudflare Access lets you require an email-PIN check on top
of the tunnel:

1. Cloudflare Dashboard → Zero Trust → Access → Applications → "Add".
2. Type = Self-hosted. App domain = `orch.example.com`.
3. Policy: `Allow` if `Include → Emails → [stakeholder@example.com]`.
4. Session duration: 24h or your preference.

Now the stakeholder receives a one-time PIN by email and only needs the
static token URL once — Cloudflare handles the identity check.

### Systemd unit (Linux)

```ini
# /etc/systemd/system/orch-dashboard.service
[Unit]
Description=orch stakeholder dashboard
After=network.target

[Service]
Type=simple
Environment=ORCH_DASHBOARD_TOKEN=paste-your-token-here
ExecStart=/usr/local/bin/orch dashboard --profile stakeholder --host 127.0.0.1 --port 7420
WorkingDirectory=/srv/orch-projects/my-project
Restart=on-failure
User=orch

[Install]
WantedBy=multi-user.target
```

Pair with a `cloudflared` service — the cloudflared install script sets
one up automatically.

### launchd (macOS)

`~/Library/LaunchAgents/lat.guria.orch-dashboard.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
 "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>            <string>lat.guria.orch-dashboard</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/local/bin/orch</string>
    <string>dashboard</string>
    <string>--profile</string>
    <string>stakeholder</string>
    <string>--host</string><string>127.0.0.1</string>
    <string>--port</string><string>7420</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>ORCH_DASHBOARD_TOKEN</key><string>paste-your-token-here</string>
  </dict>
  <key>RunAtLoad</key>        <true/>
  <key>KeepAlive</key>        <true/>
  <key>StandardOutPath</key>  <string>/tmp/orch-dashboard.log</string>
  <key>StandardErrorPath</key><string>/tmp/orch-dashboard.err</string>
</dict>
</plist>
```

Load with `launchctl load ~/Library/LaunchAgents/lat.guria.orch-dashboard.plist`.

---

## Security checklist (non-negotiables)

- [ ] Token is at least 32 chars of `openssl rand`-quality entropy.
- [ ] Token is NOT committed to git (or `.gitignore` covers the file).
- [ ] Dashboard binds to `127.0.0.1` (or your Docker network IP) — NEVER
      `0.0.0.0` on the public internet without a tunnel or reverse proxy
      in front.
- [ ] Cloudflare Tunnel or an equivalent SSH tunnel/nginx-proxy terminates
      TLS. Never serve raw HTTP with a token in the URL over the public
      internet.
- [ ] Token rotation plan: replace the env var + restart the service.
      No client-side sessions to invalidate.
- [ ] For teams > 1 stakeholder, put Cloudflare Access with email-PIN on
      top of the tunnel.
- [ ] Logs of `cloudflared` and `orch dashboard` do NOT contain the
      token (verified — the boot banner prints "Token auth: ENABLED"
      but never the secret itself).

---

## Troubleshooting

**Every request returns 401 in stakeholder mode.**
→ The token in your CLI/env doesn't match the token in the running
process. Restart with the correct token. Confirm with `env | rg
ORCH_DASHBOARD_TOKEN`.

**Every request returns 403 in stakeholder mode, even `/stakeholder`.**
→ You're missing the `TokenAuthMiddleware` because you're using
`--profile operator`. Switch to `--profile stakeholder`.

**`/api/budgets` returns 403.**
→ Working as intended. `/api/budgets` is operator-only because it
leaks per-provider granularity. If you want the operator dashboard,
run a second `orch dashboard --profile operator` on a different port.

**The stakeholder page shows `spend: $0.00`.**
→ No spend rows in the state dir yet. Once your first dispatch completes
the value updates. `spend_rounded_usd` rounds UP so any positive value
appears as at least `$0.50`.

**Cloudflare says "no healthy origin".**
→ orch dashboard isn't running on the port your `~/.cloudflared/config.yml`
points at. Confirm with `curl -sI http://127.0.0.1:7420/`.

**The stakeholder view says `ETA: —`.**
→ ETA needs at least one done task with recorded human hours to compute
the plan-vs-actual ratio. Before that we deliberately show `—` instead
of a misleading raw estimate.
