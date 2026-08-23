# Delivering AI-built products to your client with orch

This guide walks through the full workflow: from scaffolding a project to handing
your client a live dashboard URL — without writing a single status report.

---

## The one-sentence pitch to your client

> *"I'm building this with AI. You'll have a live dashboard from day one —
> phase progress, ETA, and total spend, updated automatically. I'll send you a link."*

That sentence. That's it. No spreadsheet, no Slack thread, no weekly email.

---

## Prerequisites

- `orch` installed (`pipx install https://github.com/hectorcanaimero/orch/releases/latest/download/orchestrator-0.6.1-py3-none-any.whl`)
- At least one AI CLI on your PATH: `claude`, `codex`, or `opencode`
- Optional but recommended: `bore` for tunneling (`brew install bore-cli` or `cargo install bore-cli`)

---

## Step 1 — Scaffold the project

```bash
orch init ~/work/client-name
cd ~/work/client-name
```

The wizard asks a few questions (project name, state backend, budget preset). Defaults are fine.

What you get:

```
client-name/
├── tasks.json                    # empty DAG skeleton
├── specs/README.md               # spec format reference
├── scripts/task-{start,finish,block}.sh
├── orchestrator/
│   ├── config.yaml
│   ├── model_router.yaml
│   └── budgets.yaml
└── .gitignore
```

---

## Step 2 — Configure the stakeholder dashboard

Create `dashboard.yaml` at the project root:

```bash
TOKEN="$(openssl rand -hex 16)"
cat > dashboard.yaml << EOF
profile: both
token: "${TOKEN}"
server:
  host: 127.0.0.1
  port: 7420
tunnel:
  enabled: true
  provider: bore
  command: bore
  args: ["local", "7420", "--to", "bore.pub"]
EOF

echo "Client token: ${TOKEN}"
echo "Save this — you'll send it to your client."
```

`profile: both` means:
- **You** hit `http://127.0.0.1:7420` and see everything (no token needed locally)
- **Your client** uses the public URL with `?token=<TOKEN>` and sees only the curated view

---

## Step 3 — Write your specs and generate tasks

Put your specs in `specs/`. The format is markdown with structured task sections
(see [`docs/SPEC-FORMAT.md`](SPEC-FORMAT.md)).

Or use the SDD workflow with Claude Code to generate specs from a feature description:

```bash
# In Claude Code:
# /orch-plan "WhatsApp chatbot for a restaurant — order taking, reservations, menu queries"
# /orch-tasks
```

Then atomize into `tasks.json`:

```bash
orch atomize --file specs/f0-foundation.md --apply
orch validate     # check for cycles, unrouted models, schema errors
```

---

## Step 4 — Start the dashboard and tunnel

```bash
# Start dashboard (stays in background)
orch dashboard &

# Start tunnel (bore, autossh, or cloudflared — configured in dashboard.yaml)
orch tunnel start
```

The dashboard UI shows the tunnel URL once it's live. Or check it:

```bash
orch dashboard   # visit http://127.0.0.1:7420 → Tunnel tab
```

You'll see something like: `https://chatbot-client.bore.pub`

---

## Step 5 — Send your client the link (once)

```
Hi María,

Your project is live. You can follow progress here:

  https://chatbot-client.bore.pub?token=<YOUR_TOKEN>

What you'll see:
- Phase completion (% done per phase, tasks done / in-progress / blocked)
- ETA — computed from velocity as tasks complete
- Total AI spend (rounded to nearest $0.50)
- Project documents — PRD, specs, architecture

The dashboard updates automatically. No refresh needed.

Talk soon,
[You]
```

That's your entire client communication for the duration of the project. They'll check
the dashboard. You'll get questions only when something is actually blocked.

---

## Step 6 — Run

```bash
# Review the dispatch plan first
orch --dry-run

# Semi mode — prompts for critical tasks (recommended for first run)
orch --mode semi

# Full auto — no prompts (overnight runs)
orch --mode auto
```

The dashboard updates live as tasks complete. Your client sees progress moving in real time.

---

## What your client sees (and doesn't see)

### They see

| Field | Detail |
|---|---|
| Phase progress | % complete per phase, counts by status |
| Milestones | Completed phases with timestamps |
| ETA | Hours remaining based on velocity |
| Total spend | Rounded to nearest $0.50 |
| Project documents | PRD, specs, architecture docs |

### They never see

- Per-model spend breakdown
- Which AI CLI you're using (claude, codex, opencode)
- Raw logs or error messages
- Per-task exit codes
- Your API keys or any internal config
- Any endpoint that returns `403` in stakeholder mode

This boundary is enforced server-side, not just by the frontend. A client who knows
the API structure still can't see operator-only data — every restricted route returns
`403` with an opaque body.

---

## Multiple clients

Running several projects at once? Each gets its own `orch dashboard` instance on a different port:

```bash
# Client A — port 7420, tunnel to bore.pub
orch dashboard --project-root ~/work/client-a &

# Client B — port 7421, tunnel to bore.pub
orch dashboard --project-root ~/work/client-b --port 7421 &
```

Each `dashboard.yaml` has its own token. Clients never share a session.

For subdomain routing across all clients (agency setup), put an nginx or Caddy reverse
proxy in front and route by Host header to each port.

---

## Keeping the dashboard running

For long projects (days, weeks), run the dashboard under a process supervisor:

```bash
# Simple — tmux session
tmux new -s client-a
orch dashboard --project-root ~/work/client-a

# Or launchd / systemd on a remote server
# See docs/DASHBOARD-PROFILES.md for the Cloudflare Tunnel + server setup
```

The dashboard is stateless — it reads `orchestrator/state/*.jsonl` and `tasks.json` on
every request. Restart it anytime without losing data.

---

## Customizing the stakeholder view

The curated summary endpoint (`/stakeholder/summary`) is computed server-side from
your project state. You don't configure it per-project today — what your client sees
is determined by the structure of your `tasks.json` (phases, estimates, status).

To make the stakeholder view more meaningful:

- **Use descriptive phase names** in `tasks.json` — they appear verbatim in the UI
  (`"F0 — Foundation"` → shows as "F0 — Foundation")
- **Set realistic `estimateHours`** — ETA is computed from these
- **Keep tasks unblocked** — blocked tasks without a comment leave the client wondering.
  The `scripts/task-block.sh` script appends a comment; write a human-readable reason.
- **Add docs** — put a `docs/prd/` folder with your PRD markdown. The client can
  read it from the dashboard's Documents view.

---

## Troubleshooting

**Client gets 401**
The token in the URL doesn't match `dashboard.yaml`. Regenerate: `openssl rand -hex 16`,
update `dashboard.yaml`, restart the dashboard.

**Client gets 403 on all routes**
The dashboard is running in `operator` profile. Change to `profile: both` or `stakeholder`
in `dashboard.yaml`.

**Tunnel URL changes on restart**
bore and autossh don't guarantee a stable URL. For a stable public URL, use Cloudflare
Tunnel with a named tunnel — full guide in [docs/DASHBOARD-PROFILES.md](DASHBOARD-PROFILES.md).

**Dashboard not updating**
The SPA uses SSE for live updates. If the client's browser is behind a proxy that buffers
SSE (corporate networks, some VPNs), they may need to disable the proxy or use the
direct URL.

---

## Next steps

- [Dashboard profiles reference](DASHBOARD-PROFILES.md)
- [Preflight and validation](PREFLIGHT.md)
- [Spec format](SPEC-FORMAT.md)
- [Budget guardrails](../README.md#budget-guardrails)
