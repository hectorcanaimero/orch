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
on a client-side route like `/work/kanban` keeps working.

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

## Publishing via Cloudflare quick tunnel (built in)

The built-in tunnel is for the "send a link now" case: a Cloudflare **quick
tunnel**, which gives this dashboard a random
`https://<words>.trycloudflare.com` address with no Cloudflare account, no
login and no DNS record. It is the only provider — autossh (Pinggy) and bore
were removed.

### When to prefer this over a named Cloudflare Tunnel

| Need                                                   | Use                  |
|--------------------------------------------------------|----------------------|
| A stable URL your stakeholder bookmarks for weeks      | Named Cloudflare Tunnel (below) |
| A share link for a 30-min live-review call             | Quick tunnel         |
| Custom domain, TLS handled by CF, systemd-managed      | Named Cloudflare Tunnel |
| No account, no DNS, one click                          | Quick tunnel         |

### Prerequisites

- `cloudflared` on PATH. The Tunnel page lists the steps for your system
  (Homebrew, apt, dnf, or the single binary from GitHub), and
  `orch dashboard --tunnel` prints them when the binary is missing.
- Outbound HTTPS from the dashboard host.
- The `operator` profile: the tunnel is started by the person at the machine.
- No `config.yml` / `config.yaml` in `~/.cloudflared/` (or
  `/etc/cloudflared/`): Cloudflare does not start quick tunnels while one
  exists. `orch doctor` and the Tunnel page point it out.

### Enabling it

```yaml
# .orchestrator/config.yaml
tunnel:
  enabled: true
```

That is the whole configuration. A config that still sets `provider`,
`command`, `args` or `url_regex` is refused at load, with the fix.

### Starting it

**From the dashboard** — open **Delivery → Share** (`/delivery/share`; the old `/tunnel` redirects there) at `http://127.0.0.1:7420`. The page
shows one of:

- **The tunnel is off** — `tunnel.enabled` is not set; it shows the two lines
  to add.
- **Install cloudflared** — step-by-step commands for this machine, each with
  a copy button, and **Check again** (no restart needed).
- **Sharing** — **Start the tunnel** / **Stop sharing**, the state and uptime,
  and once Cloudflare assigns the address, two links, each with its own token:
  the **client portal** (`/stakeholder/`, what to send a client) and the
  **full dashboard** (for yourself or your team).

**From the CLI** — `orch dashboard --tunnel` starts it once the dashboard is
listening and prints both links; Ctrl+C stops both. A tunnel started from the
page also stops when the dashboard exits.

### Security model

This is the load-bearing part.

**Every request through the tunnel needs a token.** cloudflared connects to
the dashboard from `127.0.0.1`, so the internet arrives looking local by
address. A request counts as local only when all three hold: a loopback
peer, a loopback `Host`, and none of the headers Cloudflare or a proxy adds
(`Cf-Connecting-Ip`, `Cf-Ray`, `X-Forwarded-For`, `X-Forwarded-Host`,
`Forwarded`). While the tunnel is up, any other request to a data route —
the API, the live stream, the portal's `data.json` — answers **401** unless
it carries a token, and the token decides how far it gets:

| Token | Reaches |
|---|---|
| Full-dashboard link token (minted on each start) | Everything — for you or your team |
| Client-portal link token (minted alongside it) | The stakeholder allow-list only; anything else is **403** |
| The project's stakeholder token (`dashboard.token`, `--token`, `orch dashboard token rotate`) | The stakeholder allow-list only |

So a client handed the portal link cannot turn it into your view by editing
the path. The static page shell is served either way; it holds no data.

Both link tokens change on every start, so **stopping the tunnel revokes the
links**. The operator profile, which asks nothing of the person at the
keyboard, is never what a stranger with the URL gets.

**Control is local-only.** `/api/tunnel/status`, `/start` and `/stop` pass
three gates in a fixed order, each failure revealing less than the next:

| Order | Gate                                             | Failure          |
|-------|--------------------------------------------------|------------------|
| 1     | `tunnel.enabled` is `true`                       | `404 Not Found`  |
| 2     | Profile is `operator`                            | `403 Forbidden`  |
| 3     | The request is local (the three conditions above) | `403 Forbidden`  |

`/start` and `/stop` also refuse a browser `Origin` that is not loopback, so
a page on another site cannot press them through the operator's browser.
Start answers **409** when cloudflared is missing or a config file blocks
quick tunnels, and when a tunnel is already running.

`/api/tunnel/capabilities` answers everyone with 200 (a stakeholder SPA reads
it before it has a token). Only a caller who passes all three gates gets the
details: the binary's path and version, the detected system, the install
guides and any blocking config file.

A reverse proxy in front of the dashboard that rewrites `Host` or adds
forwarding headers makes every request non-local: tunnel control then refuses
even the operator. Use `http://127.0.0.1:7420` directly to control the tunnel.

### Limits

- The address changes on every start; send the new link.
- Cloudflare caps a quick tunnel at 200 concurrent requests and offers it
  for testing, with no uptime guarantee.
- Quick tunnels do not carry server-sent events: a dashboard opened through
  one refreshes every 15 seconds instead of live.

### `orch doctor` integration

`tunnel.cloudflared` is **skip** while `tunnel.enabled` is false, **warn**
when it is true and cloudflared is missing (the JSON report's `remediation`
holds the install steps) or a config file blocks quick tunnels, and **ok**
with the binary's path and version otherwise.

### Rollback

Set `tunnel.enabled: false`. On the next dashboard restart the tunnel routes
answer 404, the page shows how to enable it, and nothing is spawned.

---

## Publishing via a named Cloudflare Tunnel

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
