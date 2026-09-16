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

- `orch` installed (`curl -fsSL https://raw.githubusercontent.com/hectorcanaimero/orch/main/scripts/install.sh | sh`, or `brew install hectorcanaimero/orch/orch` — see the README)
- At least one AI CLI on your PATH: `claude`, `codex`, or `opencode`
- For sharing a link: `cloudflared` (Cloudflare's tunnel client — no Cloudflare account needed). The dashboard's Tunnel page shows the install steps for your system; on macOS it is `brew install cloudflared`.

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

Turn the tunnel on in `.orchestrator/config.yaml`:

```yaml
tunnel:
  enabled: true
```

That is all. The dashboard stays the operator's:
- **You** hit `http://127.0.0.1:7420` and see everything (no token needed locally)
- **Your client** gets a link with a token that reaches only the client portal —
  they cannot turn it into your view by editing the address

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
orch dashboard --tunnel
```

Or start `orch dashboard` and press **Start the tunnel** on the Tunnel page
(`http://127.0.0.1:7420/tunnel`). If `cloudflared` is missing, the page shows
the install steps for your system and a **Check again** button.

Once Cloudflare assigns the address (a few seconds) you get two links:

- **Client portal** — `https://<words>.trycloudflare.com/stakeholder/?token=…` — send this one.
- **Full dashboard** — for you or your team only.

---

## Step 5 — Send your client the link (once)

```
Hi María,

Your project is live. You can follow progress here:

  <client portal link>

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

**What happens when they click it.** The page loads, the SPA picks the token
out of `?token=` and stores it in the browser, and the token disappears from
the address bar — so a screenshot of their tab, or a URL pasted onward, does
not carry it. Every later request sends it as a `Bearer` header. If they
bookmark the scrubbed URL and come back later, the stored token still works;
if they clear their browser data, they land on a "paste your token" form and
you resend the link.

---

## Step 6 — Run

```bash
# Review the dispatch plan first
orch --dry-run

# Semi mode — prompts for critical tasks (recommended for first run)
orch run --mode semi

# Full auto — no prompts (overnight runs)
orch run --mode auto
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
# Client A — port 7420, its own quick tunnel
orch dashboard --project-root ~/work/client-a --tunnel &

# Client B — port 7421, its own quick tunnel
orch dashboard --project-root ~/work/client-b --port 7421 --tunnel &
```

Each tunnel mints its own tokens. Clients never share a session.

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
The link is from an earlier start: every start mints new tokens and a new address. Send
the current client-portal link from the Tunnel page.

**Client gets 403**
They opened a page outside the client portal with the portal link's token — expected.
Send the client-portal link, not the full-dashboard one.

**Tunnel URL changes on restart**
Quick tunnels get a new random address on every start. For a stable public URL, run a
named Cloudflare Tunnel — full guide in [docs/DASHBOARD-PROFILES.md](DASHBOARD-PROFILES.md).

**The tunnel does not start**
Check the Tunnel page or `orch doctor`: `cloudflared` must be on PATH, and a
`config.yml`/`config.yaml` in `~/.cloudflared/` stops quick tunnels (rename it while sharing).

**Dashboard not updating live**
Quick tunnels do not carry server-sent events, so through the tunnel the dashboard
refreshes every 15 seconds instead of live. The same happens behind proxies that buffer
responses (corporate networks, some VPNs).

---

## Next steps

- [Dashboard profiles reference](DASHBOARD-PROFILES.md)
- [Preflight and validation](PREFLIGHT.md)
- [Spec format](SPEC-FORMAT.md)
- [Budget guardrails](../README.md#budget-guardrails)
