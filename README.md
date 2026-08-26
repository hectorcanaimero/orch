# orch

**Run AI agents as a team. Show clients a live dashboard — not a Slack thread.**

orch is a local task orchestrator for freelancers and agencies building with AI.
You define the work as a DAG (`tasks.json`), dispatch each task to Claude, Codex,
or Gemini in parallel, and share a read-only dashboard URL with your client so
they see progress in real time.

---

## The problem

Your client is paying for AI tokens they can't see. You're shipping features they
can't track. Status updates live in Slack threads that get lost. orch fixes that.

---

## How it works

```
1. orch atomize --apply   # spec.md → tasks.json → SQLite
2. orch run               # dispatch tasks to AI agents in parallel
3. orch dashboard         # share a URL — client sees live progress
```

---

## Quick start

```bash
pipx install orch
cd my-project
orch init
orch run
orch dashboard --profile stakeholder --tunnel
```

---

## What makes it different

| Feature | LangChain | CrewAI | Devin | **orch** |
|---------|:---------:|:------:|:-----:|:--------:|
| Multi-backend in one DAG | ❌ | ❌ | ❌ | ✅ |
| Budget guardrails | ❌ | ❌ | ❌ | ✅ |
| Client-shareable dashboard | ❌ | ❌ | ❌ | ✅ |
| Spec → tasks pipeline | ❌ | ❌ | ❌ | ✅ |
| Git worktree isolation | ❌ | ❌ | ❌ | ✅ |
| PR per task (auto) | ❌ | ❌ | ✅ | 🔜 |

---

## Configuration

One file to start:

```yaml
# .orchestrator/config.yaml
concurrency:
  global_max: 4
  per_provider:
    claude: 2
    gemini: 2
```

Optional overrides: `budgets.yaml`, `model_router.yaml`, `dashboard/dashboard.yaml`.

---

## Documentation

- [English manual](docs/MANUAL.en.md)
- [Manual en español](docs/MANUAL.es.md)
- [Dashboard guide](docs/DELIVERING-TO-STAKEHOLDERS.md)
- [Developer notes](docs/README-dev.md)
