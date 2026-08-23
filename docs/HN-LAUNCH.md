# HN Launch — copy and strategy

> Internal doc. Not shipped to users. Use before the Show HN post.

---

## Title options (pick one)

**Primary (recommended):**
> Show HN: orch — open-source AI orchestrator with a stakeholder dashboard built-in

**Alternatives:**
> Show HN: orch — dispatch tasks to Claude/Codex CLIs, give your client a live progress dashboard
> Show HN: I built an open-source tool that dispatches AI tasks and auto-generates a client dashboard

---

## Post body

```
orch is an open-source CLI that walks a tasks.json DAG and dispatches each
task to the right local AI CLI (claude, codex, or opencode). Think: Airflow
for AI CLIs, with budget guardrails so long overnight runs don't lock you out
of your own Claude terminal.

The thing that makes it different: it ships a client-facing stakeholder dashboard
built-in. You start the dashboard with --profile both, expose it via tunnel, send
your client a token-gated URL — and they can watch phase progress, ETA, and total
spend update live without you writing a status report.

I built this because I was spending 30-40 minutes per week writing project updates
to clients while Claude was doing the actual work. Now I send one URL at project
kickoff and I'm done.

Tech:
- Python CLI + FastAPI backend (no daemon, no remote queue, single-user)
- React SPA (Vite + shadcn) embedded in the wheel — no Node needed to install
- Auth: TokenAuth middleware + ProfileGuard — server-enforced, not just frontend
- State: JSONL files (default) or SQLite via `orch migrate`
- Tunnel: bore, autossh, or cloudflared — supervised by the dashboard
- 1026 tests

What the client sees (and only this):
- Phase completion % and task counts (done/in-progress/blocked)
- ETA computed from historical velocity
- Total spend rounded to nearest $0.50 (never per-model breakdown)
- Project docs (PRD, specs, arch) rendered from your markdown files

What they never see: raw logs, per-model spend, exit codes, provider names, or
anything that returns 403 in stakeholder mode.

GitHub: https://github.com/hectorcanaimero/orch
Install: pipx install https://github.com/hectorcanaimero/orch/releases/latest/download/orchestrator-0.6.1-py3-none-any.whl
Docs: https://github.com/hectorcanaimero/orch/blob/main/docs/DELIVERING-TO-STAKEHOLDERS.md

Happy to answer questions about the architecture, the auth model, or the dispatch
logic. Also open to feedback on the stakeholder UX — I'm planning to add timeline
view and PDF export next.
```

---

## Timing strategy

**When to post:**
- Tuesday or Wednesday, 9–11 AM Eastern (peak HN traffic)
- Avoid Mondays (Show HN gets buried under Ask HN / announcements)
- Avoid Friday afternoon (weekend dump)

**What needs to be ready before posting:**
- [ ] README with comparison table ← done
- [ ] `docs/DELIVERING-TO-STAKEHOLDERS.md` ← done
- [ ] `orch upgrade` works (so early adopters can self-update) ← done
- [ ] Latest release wheel installable in < 60 seconds
- [ ] GitHub Issues open (people will file bugs immediately)
- [ ] You have 2-3 hours free to respond to comments on launch day

**What would make the post land harder (nice to have):**
- [ ] Screenshot of the stakeholder dashboard as the repo's social card (og:image)
- [ ] GIF or short video showing the URL-sharing moment
- [ ] One real client project as a case study (anonymized is fine)

---

## Likely HN comments and how to respond

**"This is just a CRON job / Airflow"**
> orch doesn't schedule by time — it walks a dependency graph (DAG). Tasks dispatch
> when their dependencies are done, not at a fixed time. The key difference is the
> stakeholder dashboard: Airflow doesn't give your client a live progress URL.

**"Why not just use GitHub Projects / Linear?"**
> Those track tickets, not AI compute. orch tracks which LLM is running which task,
> how much it's spending, what it blocked on, and computes ETA from token velocity.
> It's complementary — you can use Linear for planning and orch for execution.

**"The client-facing dashboard is just a checkbox, LangSmith does this"**
> LangSmith is observability for LLM calls, not project delivery. It shows token traces
> to engineers. orch's stakeholder view shows business progress to non-technical clients —
> no logs, no provider names, no API internals. Different audience, different contract.

**"Why not build this as a web service / SaaS?"**
> Single-user, local-first, no remote queue. Your tasks.json and state files never leave
> your machine. The tunnel is yours (bore/autossh/cloudflared). This is intentional —
> no vendor lock-in, works offline, works behind a firewall.

**"Dependency on Claude/Codex CLIs seems fragile"**
> Yes — it's a real tradeoff. orch dispatches to whatever CLI you have installed and
> authenticated. If the CLI changes its exit codes or stdout format, orch may need updates.
> The model_router.yaml makes it easy to swap providers. The alternative (calling APIs
> directly) adds auth management and removes the benefit of using subscriptions you already pay for.

**"1026 tests seems like a lot for a personal tool"**
> It started as a 334-task pipeline for a client project. When your mistakes show up as
> a $50 API bill at 2 AM, you write tests.

---

## Metrics to watch after launch

**48 hours:**
- Points (200+ = positioning landed)
- Comments (engaged discussion = people care)
- GitHub stars delta
- `pipx install` from release logs (proxy: release download count)

**1 week:**
- Issues filed (bugs are engagement)
- PRs (community interest)
- Forks (people building on it)

**If HN doesn't land (< 50 points, few comments):**
- Don't iterate on code — iterate on the pitch
- Try Reddit r/MachineLearning or r/SideProject with a different angle
- Post a "Show Reddit" with a concrete use case story (the WhatsApp chatbot scenario)
- Wait 2-3 weeks, then try HN again with a tighter title

---

## The one thing HN cares about

HN readers are builders. They'll skip the marketing and go straight to: *"Does this
actually work? Can I install it in 60 seconds? Is the code embarrassing?"*

Make sure:
1. `pipx install <url>` works, first try, clean machine
2. `orch init` doesn't crash
3. The GitHub repo looks maintained (recent commits, real issues, real CI)
4. The README answers "what does this actually do" in the first paragraph
