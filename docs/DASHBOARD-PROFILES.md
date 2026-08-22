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
