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
# or sets Authorization: Bearer <TOKEN>.
```

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

You have three options, in precedence order (first non-empty wins):

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
