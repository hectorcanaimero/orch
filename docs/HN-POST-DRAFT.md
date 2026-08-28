# Show HN post — draft (H-4 launch prep)

Copy/paste ready. Post from your own account, not `hn`.

**Timing**: Tue–Thu, 08:00–10:00 America/New_York gets the best front-page dwell. Ship after v1.0 tag (H-1 templates in place) so the "quick start" isn't an obvious cliff.

Before posting: cut a fresh v1.0 release so the wheel + SPA icons land on PyPI first (`pipx install orch` has to work on the front-page hour). Verify:

- `pipx install orch` works from a clean shell
- `orch init` produces a working project (config.yaml + tasks.json + workflow)
- The stakeholder URL renders with the new brand and hero screenshot on the README GitHub landing
- No open GitHub issues labeled `regression` or `blocker`

---

## Title (80-char cap on HN)

**Show HN: Orch — orchestrate AI coding agents and share the dashboard with your client**

Alternatives (rank in this order if the first one hits a title match on Algolia):
1. Show HN: Orch — DAG of AI coding agents with a live client dashboard
2. Show HN: Orch — a CLI that runs AI agents in parallel and shows clients ETAs
3. Show HN: Orch — task-orchestrator for freelancers building with Claude / Codex / Gemini

---

## Body (short — HN rewards brevity)

I build with AI agents for clients, and the same friction keeps showing up: the client is paying for AI tokens they can't see, features ship into Slack threads that get lost, and there's no single "when will it be done" answer that both of us trust.

Orch is my attempt at fixing that. It's a local CLI that walks a task DAG (`tasks.json`), dispatches each task to Claude / Codex / OpenCode / Gemini in parallel, opens a PR per task, waits for CI, and — if you flag it on — auto-merges on green. Everything lands in a local FastAPI + React dashboard with three profiles:

- **operator** — full DAG, logs, spend by provider, retry knobs.
- **stakeholder** — milestones with a Gantt-like timeline, an ETA per milestone, a business-language exec summary ("Project 62% complete — ETA 3 Sep. AI spend: $12"), and a "what's blocked" grid.
- **both** — the operator view plus the stakeholder overlay.

`orch dashboard --tunnel` publishes a URL via Pinggy (autossh, no signup). You send the link to your client once and it stays live. Notifications (Slack / Discord webhooks, `orch notify digest` for cron+mail) fire on task blocks and CI failures — the client gets a Monday morning digest without you writing it.

Under the hood: SQLite as the single source of truth, per-provider budget guardrails, git worktrees per task, PR + CI auto-validation, deterministic executive summary (no LLM at read time — the dashboard runs headless), and a `python -m orch` CLI you install with `pipx`. Zero long-running daemon, single-user, local-first. About 1300 tests.

**Try it in three commands:**

```bash
pipx install orch
orch init my-project && cd my-project
orch dashboard --profile stakeholder --tunnel
```

**What's missing (honest):**
- No browser tool use / long-horizon agent autonomy — that's Devin's / Cursor's turf. Orch's differentiator is the client-facing eye, not agent smarts.
- Guided init wizard + 5 canonical templates (`python-api`, `nextjs-saas`, `chatbot-whatsapp`, `expo-mobile`, `data-pipeline`) shipped in v1.0. Custom templates are on the post-1.0 list.
- No PDF export yet — the exec summary is HTML + a copy button; PDF is next.
- No multi-project portfolio view — one project per dashboard today. `orch dashboard --portfolio` is on the roadmap for agencies with several clients.

Repo: https://github.com/hectorcanaimero/orch
Docs & manual: https://github.com/hectorcanaimero/orch#documentation
Roadmap (living doc): https://github.com/hectorcanaimero/orch/blob/main/docs/brainstorm/next-sprints.md

MIT. It's dogfooded — every commit on the repo was orchestrated by the version that opened its own PR.

Happy to answer anything on scope, why-not-<framework>, or the design choices (deterministic summary, single SQLite file, no daemon).

---

## Preparing your top comment (post-immediately after submission)

HN weighs the OP's first comment heavily. Post this in the same minute as the submission (or within 60 seconds — an empty top comment slot after 5 minutes is a signal):

> A few extra notes I left off the body to keep it short:
>
> - **Why local-first, no daemon?** Every task's state is in one SQLite file per project. If orch crashes mid-run you can resume without losing anything, and no long-running server means nothing to secure or babysit. `orch dashboard` is a plain FastAPI you can start and stop.
> - **Why deterministic exec summary instead of an LLM call?** The dashboard runs headless — a live LLM call there would add a dependency on the operator's Anthropic key just to render a page. Every sentence is a template filled from `sprint_health` + spend. Free, instant, testable.
> - **Multi-backend without a lowest-common-denominator adapter.** Each backend has its own tokenizer, prompt shape, cost calc. Orch's `Provider` protocol is thin — routing decisions live in `model_router.yaml` (one line per model). No abstraction tax on the fast path.
> - **The "share this URL with your client" moment is the whole thesis.** If nobody was paying for AI spend they can't see, half of orch wouldn't need to exist. The stakeholder / operator profile split, the exec summary, the budget vs actual chart — all of it exists because the client is the second user, not just the dev.

---

## Q&A prep — expected questions

**Q: Why not just LangGraph / CrewAI?**
Both are great for building autonomous agents. Orch isn't an agent framework — it's a task dispatcher + client-facing dashboard. The DAG is defined by a human (or by `orch atomize` from a spec.md), not decided at runtime by an agent. Different problem space.

**Q: Isn't Devin/Cursor going to eat this?**
Devin is autonomous agent + IDE. Orch is "run agents you already trust and show your client what they're doing". Different buyer (freelancer / agency running Claude/Codex CLIs), different value ($8k/mo per seat vs OSS + your own subscription).

**Q: How do you handle secrets?**
The dashboard has three defense layers: token auth, profile guard, whitelist, loopback gate. The tunnel is opt-in and off by default. Nothing leaves your machine unless you flip `dashboard.tunnel.enabled: true`. Spec content and AI logs never leave — the dashboard only reads structured state from SQLite + JSONL events.

**Q: Why Python?**
Because the target user already has `pipx` and uses Python for orchestration scripts. The SPA is React + Vite (hand-written shadcn, no heavy deps, ~114 kB gzipped). Being Python means `orch atomize` can be extended in-process instead of shelling out.

**Q: What backends?**
`claude`, `codex`, `opencode`, `gemini` CLIs today (installed separately). Adding one is ~40 lines in `dispatcher.py`. Not planning a "bring your own HTTP API" adapter — the CLIs already handle auth, rate limits, and streaming.

**Q: Is this really "orchestration" or just a task queue?**
Task queue is the mechanical layer. The orchestration is: DAG dependencies, per-provider concurrency caps, budget guardrails per provider, retry policies per failure kind (transient / rate-limit / spec-drift), CI-triggered re-dispatch with the CI logs fed back into the prompt. Plain task queues don't do any of that.

**Q: What if my task fails silently and the agent claims success?**
Two guards. First, F-6 (a real bug we hit): before pushing the branch, orch stages + commits inside the worktree. If the tree was clean, we know the agent produced nothing. Second, CI decides. `vcs.ci_max_retries` re-dispatches with the failing logs in-prompt.

**Q: License?**
MIT. Contributions welcome — the repo has a `steward` skill and a PR template.

---

## Post-launch checklist (Day 1)

- Refresh a Slack/Discord notification pointed at yourself so you don't miss the first wave of comments.
- Set `orch notify digest --send` in cron on the demo project so the linked stakeholder URL keeps updating without you.
- Watch `orch dashboard` on the demo project for the traffic bump — Pinggy free tier caps at ~100 req/min; upgrade or shard the URL if traffic is real.
- Every substantive comment gets an answer within an hour for the first ~4 hours (front-page window).
- Log every question that comes up so you can turn recurring ones into README additions post-launch.
