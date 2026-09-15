# orch — facts for the architecture diagrams (main @ 723351e, 2026-09-13)

Every node, edge and label in a diagram must trace to a line here. Nothing invented.
Language of authored content: Spanish (omit meta.locale; viewer UI falls back to English).
Keep code identifiers, commands, package paths, file names in English exactly.

## 1. Package architecture (from `go list`, internal imports only)

cmd/orch → internal/cli (the only thing main imports)

internal/cli imports everything user-facing: atomize budget config dashboard doctor engine
explain export graph mcp model notify project providers publish publish/snapshot pyfmt
report router scaffold skills state telemetry templates tunnel vcs worktree.

Real import edges (non-cli):
- engine → budget, config, graph, model, prompt, providers, pyfmt, state, vcs, worktree
  (engine does NOT import router, notify, dashboard or publish; cli resolves routes and wires notify)
- dashboard → budget, config, graph, model, pricing, project, publish/snapshot, state, tunnel
- mcp → budget, explain, graph, model, project, prompt, state
- publish → publish/snapshot ; report → publish/snapshot
- publish/snapshot → graph, model, project, providers, state
- project → model, state
- doctor → budget, model, router, state, vcs, worktree
- explain → budget, graph, model, pyfmt
- export → graph, model          (orch export multica)
- scaffold → model, router, templates   (orch init)
- budget → pyfmt, state
- state → model, pyfmt
- atomize → model ; graph → model, pyfmt ; prompt → model, pyfmt ; providers → model, pyfmt ; router → model, pyfmt
- model → pyfmt
- leaves (no internal imports): config, notify, pricing, pyfmt, skills, telemetry, templates, tunnel, vcs, worktree

Roles:
- state: SQLite, single source of truth for runtime status (orch.db, WAL, one writer). Migrations 001–006.
- engine: run loop — Refill (retry queue first, then ready set), spawnOne, Reap, CIPoller.
- providers: five CLI adapters: claude, codex, opencode, gemini, agy (Argv / Parse / Classify).
- dashboard: HTTP server + embedded SPA (web/), profiles operator|stakeholder, SSE.
- mcp: stdio MCP server, seven orch_* tools, shares state.Backend.
- publish: static client site (dir | git | cloud) from publish/snapshot.
External processes: agent CLIs (claude, codex, opencode, gemini, agy), git, gh/glab, Slack/Discord webhooks,
Cloudflare Worker orch-cloud (for publish --to cloud).

## 2. Data flow (a project)

specs/*.md (markdown spec) → `orch atomize` (dry-run diff, --apply) → tasks.json (static DAG, never runtime status)
.orchestrator/config.yaml + model_router.yaml (model → backend CLI) + budgets
tasks.json → `orch run` (engine) → .orchestrator/state/<project>/orch.db (SQLite: tasks_runtime, runs, dispatches, events, spend)
engine → agent CLI in a git worktree → branch → pull request (gh) → CI status back into orch.db (CIPoller)
orch.db + tasks.json → project.Hydrate → publish/snapshot.Build (schema: 1, no internal ids)
snapshot → dashboard (/api/*, /stakeholder/summary, SSE) | orch publish → dir / gh-pages branch / orch-cloud Worker (KV) | orch report pdf | orch notify digest
agents report back through MCP tools (orch_set_status, orch_block) or scripts/task-*.sh → orch task-status → orch.db

## 3. Dispatch sequence (one task, `orch run`)

Participants: operator, orch run (engine Scheduler), state (orch.db), budget, worktree (git), prompt, agent CLI (provider), gh (vcs).
1. Refill: drain retry queue whose backoff expired, then Queue.Ready (deps done, status todo, not owned by retry queue).
2. Route lookup Routes[task.Model] (resolved by cli from model_router.yaml before the run).
3. (semi mode, critical task) operator gate.
4. spawnOne: (1) per-task lock if --task-locks; (2) budget gate BEFORE semaphores (capped provider → skip);
   (3) semaphores: per-provider then global.
5. worktree.Create (branch per task) — before the prompt, because the child runs in it.
6. prompt.Write (title, description, files, spec ref READ FIRST, completed-dependency notes).
7. Spawn agent CLI (process group, timeout = estimateHours × default_timeout_multiplier; SIGTERM → 10 s → SIGKILL).
8. RecordDispatch + Transition todo → in-progress in orch.db.
9. Agent works; reports via MCP orch_set_status / scripts/task-finish.sh (note becomes the next task's context).
10. Reap: release slots and lock; providers Parse output; Classify failure; RecordSpend.
11. Success: CommitPending → Push → Remove worktree → gh CreatePR → SetTaskPR (ci_status pending).
    Failure: DecideRetry → retry (back to todo, backoff, escalate model on 3rd attempt) or block (+ notify Blocked).
12. CIPoller: gh pr checks → green: done (and merge if auto_merge) ; red: redispatch with CI logs (.orch-ci-feedback.md) up to ci_max_retries. A PR merged outside orch finishes the task; one closed without merging blocks it (gh pr view --json state). The PR state is read only when CI cannot settle it: at once for a red or conflicting PR, at most every 5 minutes for one pending or unreadable, never for a green one.
13. Queue empty → sprint_done event; SIGINT drains and exits 130.

## 4. Task lifecycle (internal/model/status.go — legal transitions)

States: backlog, todo, in-progress, done, blocked.
- backlog → todo | in-progress | blocked | done   (atomize creates tasks in backlog; only todo is dispatched)
- todo → in-progress | blocked | done
- in-progress → done | blocked | todo            (todo = retry after a failure)
- blocked → todo | in-progress | done            (blocked → todo = operator recovers / orch reset)
- done → todo                                    (the only reopen)
Illegal moves are rejected (e.g. done → in-progress, done → blocked). Self-transitions allowed.
Triggers: operator `orch task set --status`, engine dispatch (todo→in-progress), agent orch_set_status/task-finish (→done),
orch_block/task-block (→blocked), reaper retry (in-progress→todo), `orch reset --requeue` (stuck in-progress→todo).

## 5. Publish / cloud topology

Operator machine: orch binary (publish/snapshot.Build → publish.Export writes the site: index.html, data.json, data.js, assets/, robots.txt).
Destinations:
- `--to dir`: a folder (serve anywhere; opens from file:// thanks to data.js).
- `--to git`: temp clone of origin → orphan gh-pages branch replaced whole, push; never touches the checkout. GitHub Pages serves it.
- `--to cloud`: ~/.orch/credentials (0600: Worker URL, admin token, per-project publish+view tokens) → HTTPS PUT /api/v1/projects/<id>/site
  (JSON, base64 files, file-set digest) → orch-cloud Cloudflare Worker (operator's own account) → KV namespace ORCH
  (project:<id>, view:<sha256>, manifest:<id>, blob:<sha256>; tokens stored only as SHA-256).
- Client (stakeholder) browser → GET https://<worker>/v/<view_token>/ → Worker reads manifest + blobs → page
  (Referrer-Policy no-referrer, noindex). Rotating the view token (orch cloud rotate) kills the old link.
- `--watch`: re-publishes when the snapshot digest changes (generated_at excluded).
- Alternative live view: `orch dashboard --profile stakeholder --tunnel` (autossh/Pinggy or bore).
