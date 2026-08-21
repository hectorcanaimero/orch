# Proposal: Rupies v2 Task Orchestrator

Evidence base: `sdd/orchestrator/explore.md` (do not re-derive). This proposal answers the 5 open questions and freezes scope.

## Intent

Single-user local dev tool that walks the `v2/tasks.json` DAG (334 tasks), spawns the correct local CLI (`claude`|`codex`|`opencode`) per task `model`, respects per-provider rate limits, reuses `scripts/task-*.sh` for state mutation, and exposes spend to the existing dashboard. Solves "334 tasks, three CLIs, no coordination" without daemon, queue, or remote infra.

## Scope

### In
- Python CLI at `v2/orchestrator/` with `--mode {auto,semi}`, `--dry-run`, `--resume <run-id>`, `--only <phase>`, `--limit N`.
- DAG walk — dispatch every task whose deps are `done`, honor per-provider + global concurrency caps.
- Three backend adapters (`claude`, `codex`, `opencode`) with JSON parsing, cost extraction, timeout, retry-once, sandbox flags.
- `model_router.yaml` mapping `tasks.json` model strings → (backend, real model name). See Q1.
- Semi-mode tick-tock CLI `[y/N/skip/quit]` on premium-tier / critical-file predicates (explore §4).
- Run-file resumability at `orchestrator/state/run-<uuid>.json` + PID reconciliation.
- Rolling spend log `orchestrator/state/spend-<YYYY-MM-DD>.jsonl` + budget panel in `dashboard/index.html`.
- Reuse of `scripts/task-*.sh` — orchestrator wraps them, never rewrites `tasks.json` directly.

### Out
- Multi-machine / distributed dispatch, remote queue, web UI beyond existing dashboard.
- Retry-with-different-model fallback (blocked stays blocked — human decides).
- Editing `tasks.json` schema or the state scripts.
- Auto-fixing model-string mismatch in `tasks.json` (see Q1).
- Sandbox jail beyond native CLI flags.

## Approach

Python 3.11+ CLI. `asyncio.subprocess` dispatch loop, per-backend semaphores, one `Runner` coroutine per in-flight task. DAG resolved once at startup (topological ready-set). State mutations delegated to shell scripts. Spend events appended to JSONL tailed by dashboard via `fetch()` polling.

## Decisions (5 open questions)

### Q1 — Model routing → `model_router.yaml` mapping table
- **Decision**: Checked-in `orchestrator/model_router.yaml` maps every `tasks.json` model string to `{backend, cli_model, tier}`. `tasks.json` untouched.
- **Reason**: `tasks.json` is truth for humans + dashboard; rewriting forks meaning from intent (the `opencode/` vs `opencode-go/` prefix encodes which subscription the user *intended* to bill). Table is reversible, greppable, adapts to provider renames.
- **Tradeoff**: Wrong entry = wrong CLI dispatched. Mitigated by startup validation that fails fast if any model string has no route.

### Q2 — Version drift → auto-upgrade to nearest, warn once
- **Decision**: If exact `cli_model` isn't in the local registry, fall back to closest (same family, higher patch). Single WARN on startup listing substitutions; do not fail.
- **Reason**: Racing 334 tasks — hard-fail on `glm-5.1 → glm-5.3` blocks the run for no safety benefit. Family-level pinning is the real intent.
- **Tradeoff**: A silent shift could hide a genuinely wrong model. Mitigated by WARN + spend panel (cost drift is visible).

### Q3 — Semi-mode UX → blocking tick-tock CLI `[y/N/skip/quit]`
- **Decision**: `input()` on the main thread when a task matches the semi predicate. `y` dispatch, `N`/enter defer to end of run, `skip` block with reason `"operator skipped"`, `quit` graceful drain.
- **Reason**: Simplest thing that works; no TUI dependency; user is already at the terminal.
- **Tradeoff**: Blocks parallel dispatch during keystroke wait — acceptable, semi-mode is *for* critical decisions.

### Q4 — CWD contract → orchestrator runs from `v2/`
- **Decision**: `pwd` MUST be `/…/v2/` at invocation. Shells out to `./scripts/task-*.sh` via relative path; passes `-C v2/` to each backend CLI. Fail fast if `pwd` isn't a `v2/` root (detect via `tasks.json` + `scripts/task-start.sh`).
- **Reason**: Matches README; keeps prompts, spec refs, and git-diff checks trivial.
- **Tradeoff**: Running from `v2/orchestrator/` errors with a clear message — small cost.

### Q5 — `--dry-run` scope → enumerate & print only
- **Decision**: `--dry-run` prints resolved dispatch order (task id, backend, real model, prompt-file path, historical-cost estimate if available), then exits 0. No subprocesses.
- **Reason**: User needs a preview before burning $100+ in inference. Read-only sandbox spawns are still 334 real API calls.
- **Tradeoff**: Doesn't catch CLI auth failures (smoke-test flag = future work).

## Module Layout

```
orchestrator/
├── orch.py             # CLI entry: argparse, main loop, signals
├── task_queue.py       # DAG resolver, ready-set, dep-satisfied check
├── dispatcher.py       # Backend adapters, semaphores, retry
├── prompt_builder.py   # Per-task prompt from template + dep summaries + spec ref
├── state.py            # tasks.json reader, run-file writer, PID reconcile, spend log
├── checkpoints.py      # Semi-mode predicates + tick-tock prompt
├── model_router.yaml   # tasks.json model → {backend, cli_model, tier}
├── config.yaml         # Concurrency, timeouts, budget caps, spend-log path
└── state/              # Runtime artifacts (run-*.json, spend-*.jsonl) — gitignored
```

## Dashboard Budget Panel (scoped, not designed)

- **Source**: Orchestrator appends one JSONL line per completed task to `orchestrator/state/spend-<YYYY-MM-DD>.jsonl`: `{"ts":"…","task":"B-020","backend":"claude","model":"opus","cost_usd":0.42,"tokens_in":…,"tokens_out":…}`. Cost from `claude.total_cost_usd`, `codex`/`opencode` `step_finish.cost` (explore §1).
- **Display**: New panel in `dashboard/index.html`: today's cumulative spend per backend (horizontal bar), top 5 most-expensive tasks today, daily total + delta vs yesterday.
- **Fetch**: Dashboard polls `../orchestrator/state/spend-<today>.jsonl` with `cache: no-store` on the existing 5 s tick. Static-HTML pattern — no server, no build. Panel hides if file 404s.

Detailed layout + JS is `sdd-design` concern; scope here is "file exists and dashboard reads it."

## Rollout / Build Order (walking-skeleton first)

1. `state.py` + `task_queue.py` — read `tasks.json`, compute ready-set, write stub run-file. Prove the graph with a no-op dispatcher.
2. `dispatcher.py` with ONE backend (`claude`) — end-to-end: pick a haiku task, spawn, parse JSON, call `task-finish.sh`. Risk-reducing spike.
3. `prompt_builder.py` — replace hard-coded prompt with explore §3 template.
4. Add `codex` + `opencode` adapters — same shape.
5. `checkpoints.py` + semi-mode.
6. `--resume` + PID reconcile.
7. Spend log + dashboard budget panel — last; observability, not correctness.

Ship after step 4 = "auto-mode MVP for non-critical tasks."

## Affected Areas

| Area | Impact | Description |
|---|---|---|
| `v2/orchestrator/` | New | Python package + config + runtime state |
| `v2/dashboard/index.html` | Modified | Add budget panel + fetch/poll for spend JSONL |
| `v2/scripts/task-*.sh` | Unchanged | Orchestrator invokes, never edits |
| `v2/tasks.json` | Unchanged | Read-only from orchestrator |
| `v2/.gitignore` | Modified | Add `orchestrator/state/` |

## Risks

| Risk | Likelihood | Mitigation |
|---|---|---|
| Missing router entry → task can't dispatch | Med | Startup validation fails fast; router is source-controlled |
| Silent version upgrade masks a wrong model | Low-Med | WARN log + visible spend drift |
| Agent lies about `task-finish` (injection or hallucination) | Med | Post-hoc git-diff check + JSONL event grep for `task-finish.sh <expected-id>` (explore §7) |
| Orchestrator crash leaves tasks `in-progress` | Med | Run-file + PID reconcile on resume; orphaned in-progress → `todo` |
| Anthropic sub-daily rate limit stalls the whole `claude` bucket | High on opus-heavy phases | Per-provider semaphore capped at 3; stall visible in dashboard; user drops to semi-mode |
| Spend runs away silently | Low w/ panel, High w/o | Panel makes it visible; `--max-budget-usd` per `claude` invocation |
| Dashboard reads partial JSONL mid-append | Low | Line-delimited JSON tolerates it; dashboard skips unparseable lines |
| **NEW — Concurrent orchestrator instances corrupt run-file** | Low | Advisory `flock` on `orchestrator/state/.lock`; second instance exits with clear message |

(The `flock` risk was not in the explore — surfaced while thinking about resumability + concurrent operator invocations.)

## Non-goals (from explore §9)

- Making `files: []` do meaningful concurrency work (90% of tasks empty — do NOT rely on per-file lock as a safety net).
- Rebuilding state scripts in Python.
- Distributed execution.
- ~~Retry-with-different-model auto-escalation.~~ *(See Amendment 2026-08 below — limited, per-route opt-in escalation is now in scope; blanket auto-escalation across ALL routes remains a non-goal.)*

---

## Amendment 2026-08 — FR-D-8 attempt-3 escalation

Original proposal marked "retry-with-different-model auto-escalation" as a non-goal ("blocked stays blocked — human decides"). Real-world traffic (Fase B rollout) showed the trade-off is asymmetric: a small number of routes carry provider-side outage risk (5xx spikes, sudden rate-limit tightening) where retrying the same model is wasted budget. This amendment adds a per-route, opt-in escape hatch:

- `RouteEntry.escalation_model: str | None` — names another route KEY to promote to on attempt 3 after a retryable failure on attempt 2.
- Router-load validation ensures the target route exists (fail-fast).
- Terminal failure classes (PERMISSION / BUDGET / TIMEOUT / ID_SPOOF) never escalate. VERSION_DRIFT is already handled on attempt 2 via `fallback_cli_model` — attempt-3 does not double-swap.
- Budget cap (`cfg.budget.per_dispatch_usd`) still applies; escalation is skipped when cumulative task spend has reached it.
- New locked event type `escalate` in `state.EVENT_TYPES` — dashboards can tally escalation frequency per route for the operator to prune dead escalation targets.

Scope: 3 seed routes get `escalation_model` in this amendment (`opencode-go/glm-5.1`, `opencode/gemini-3.0-pro`, `opencode-go/mimo-v2.5`). All other routes keep the classic FR-D-4 max=2-attempt semantic — no behavior change for them. Full test suite (170 → 178 tests) stays green.

## Rollback Plan

1. Ctrl-C → graceful drain; second Ctrl-C → SIGKILL of children.
2. `python -m orchestrator.state reconcile` — orphaned `in-progress` (dead PID) → `todo`.
3. Delete `orchestrator/state/run-<uuid>.json` for the aborted run.
4. `tasks.json` remains valid — mutations went through reviewed scripts; `task-log.jsonl` is the audit trail.
5. Full nuke: `rm -rf v2/orchestrator/state/` + revert `dashboard/index.html` diff. No schema migrations to undo.

## Dependencies

- `python >= 3.11` (async task groups), `PyYAML`.
- `claude`, `codex`, `opencode` CLIs installed and authenticated (verified versions in explore §1).
- `jq` (already required by state scripts).

## Success Criteria

- [ ] `orchestrator --dry-run` prints the full dispatch plan for all 334 tasks with zero unresolved routes.
- [ ] `orchestrator --mode auto --only 11` (bootstrap phase, cheap models) completes without operator intervention.
- [ ] Kill at task 47/334, `--resume <id>` picks up cleanly — no lost or double-dispatched tasks.
- [ ] Dashboard shows non-zero spend for the active backend within 5 s of first `task-finish`.
- [ ] Semi-mode pauses on B-014 (RLS) and premium-tier phase-10 tasks; auto-mode does not.
- [ ] After a full run, `git diff --stat` outside declared `files[]` is empty (strict-files guard holds).

---

**Next SDD phase**: `sdd-spec` (behavioral requirements per module) in parallel with `sdd-design` (concurrency + resumability + spend-log formats).
