# Spec: Rupies v2 Task Orchestrator

Behavioral requirements. RFC 2119. WHAT, not HOW — mechanism → `design.md`. Refs: `proposal.md §Decisions`; `explore.md §1,4,6,7`.

---

## 1) Functional Requirements

### 1.1 `orch.py` — CLI

**FR-CLI-1**: Invoked as `python -m orchestrator [FLAGS]` from the `v2/` root.

**FR-CLI-2**: Flags — `--mode {auto,semi}` (default `auto`; semi pauses per §1.6); `--dry-run` (enumerate plan, exit 0, no subprocess); `--config <path>` (default `orchestrator/config.yaml`); `--only <id-glob>` (default `*`, e.g. `B-*`, `P0-0[0-3]?`); `--resume <run-id>` (reconcile prior run, adopt live PIDs); `--max-tasks <n>` (default ∞).

**FR-CLI-3**: Exit codes — `0` clean drain; `1` config/router error caught pre-dispatch; `2` CWD contract violation (§FR-STATE-1); `3` flock contention (another instance owns lock); `130` SIGINT graceful drain complete.

**FR-CLI-4**: `--help` SHALL list all flags, exit codes, and CWD contract in ≤ 80 lines.

### 1.2 `task_queue.py` — DAG

**FR-Q-1**: Input SHALL be parsed `tasks.json` (schema: `id, title, description, phase, model, estimateHours, files, deps, status`).

**FR-Q-2**: Output SHALL expose: `ready()` → set of IDs where every dep is `done` and self is `todo`; `blocked_transitively(id)` → bool (any ancestor `blocked`); `topological_order()` → deterministic (stable sort by `(phase, id)`).

**FR-Q-3**: The resolver SHALL detect cycles at load and fail (exit 1) listing cycle IDs (future-proofing).

**FR-Q-4**: The resolver SHALL fail (exit 1) if any `deps[]` references a non-existent ID.

**FR-Q-5**: `ready()` SHALL be pure — same status snapshot yields same set.

### 1.3 `dispatcher.py` — Backend adapters

**FR-D-1**: Three adapters — `claude`, `codex`, `opencode` — selected by `model_router.yaml` (Q1).

**FR-D-2**: Invocation contracts (evidence: `explore.md §1`):

| Backend | Invocation | Success | Cost |
|---|---|---|---|
| `claude` | `claude -p --output-format json --model <m> --session-id <uuid> --add-dir . --max-budget-usd <cap> --permission-mode acceptEdits` (prompt via stdin; cwd = `v2/`, so `--add-dir .` binds the tree) | `is_error==false && subtype=="success"` | `total_cost_usd` |
| `codex` | `codex exec --skip-git-repo-check --json -o <out> -C <v2> -s workspace-write --approve-for-me -m <m>` (prompt via stdin) | Final msg in `<out>` AND no `type:"error"` event | Σ `step_finish.cost` |
| `opencode` | `opencode run --format json --auto --model <prov>/<m>` (prompt via stdin; opencode manages its own session — no `--session` flag) | Terminal `step_finish` with `reason:"stop"` AND no error | Σ `step_finish.cost` |

> Argv harmonized with implementation on 2026-08-19. Prior spec text had `--add-dir <v2>` for claude, omitted `--approve-for-me` for codex, and required `--session <uuid>` for opencode — those variants are superseded.

**FR-D-3**: Timeout SHALL be `estimateHours × 1.5 × 3600` s. On timeout: SIGTERM, wait 10 s, SIGKILL, then mark blocked with reason `"orchestrator timeout"`.

**FR-D-4**: On non-zero exit OR error signal, retry ONCE with same model. Second failure MUST block with reason including last ≤ 500 chars of stderr.

**FR-D-5**: In-flight count MUST NOT exceed `concurrency.per_provider.<backend>` nor `concurrency.global_max` (config-driven semaphores).

**FR-D-6**: A task whose `model` has no `model_router.yaml` entry MUST NOT be dispatched. Startup validation fails the whole run (exit 1) before any dispatch.

**FR-D-7**: If `cli_model` is missing from the local registry, substitute the nearest same-family higher-patch version and emit exactly one WARN line per substitution at startup (Q2).

<!-- ─── Amendment 2026-08 (FR-D-8) ───────────────────────────────── -->

**FR-D-8** *(Amendment 2026-08 — attempt-3 escalation to a different model)*:
When a retryable failure occurs on `attempt == 2` (i.e., FR-D-4's retry has already been consumed) AND the failing route's `RouteEntry.escalation_model` is set, the dispatcher SHALL perform a 3rd attempt on the ROUTE named by `escalation_model`. The escalation target is looked up as a new route entry — its own `backend`, `cli_model`, and tier apply, and it MAY differ from the primary in any of those axes.

Escalation eligibility:

| Failure class (`dispatcher.classify_failure`) | Attempt-3 escalation? |
|---|---|
| `TRANSIENT`, `RATE_LIMIT`, `PARSER`, `OTHER` | YES (if `escalation_model` set) |
| `VERSION_DRIFT` | NO — handled on attempt 2 by `fallback_cli_model` (do not double-swap) |
| `PERMISSION`, `BUDGET`, `TIMEOUT`, `ID_SPOOF` | NO (never — same policy as FR-D-4 retry gate) |

Constraints:

- If `route.escalation_model` is null OR not set, the task is blocked after attempt 2 exactly as before FR-D-8 (backwards-compatible with all pre-amendment routes).
- The escalation target route MUST already exist in `model_router.yaml`; startup validation (see FR-D-6 wording) fails fast if any route names a missing escalation target.
- Budget cap: attempt 3 counts against `cfg.budget.per_dispatch_usd`. If cumulative spend for the task across attempts 1 + 2 already meets or exceeds the cap, escalation is skipped and the task is blocked.
- The orchestrator SHALL emit an `escalate` event on the events log with `{from_route, to_route, from_cli_model, to_cli_model, failure_class, attempt: 3}` BEFORE the `retry` event for the same attempt (event-log timeline reflects the promotion cause, then the re-dispatch).
- Escalation backoff is `0s` (no wait — the target route is a different model/backend, so the current provider's rate window is irrelevant).
- Escalation is per-route metadata, NOT per-task — `tasks.json` schema is untouched.

*Rationale for the amendment*: The original scope-exclusion "retry-with-different-model auto-escalation (blocked stays blocked)" (§5 below) was over-broad. In practice, provider-side outages (5xx spikes, sudden rate-limit tightening) burn budget on retries that will never succeed against the same route within the run window. An OPT-IN escalation on a per-route basis lets high-value routes fall back to a different family on the 3rd attempt without touching routes that legitimately want the classic "blocked stays blocked" semantic.

### 1.4 `prompt_builder.py`

**FR-P-1**: Output prompt SHALL follow the `explore.md §3` template, populated from `{id, title, description, phase, estimateHours, reason, files, specRef, [dep_id: last_comment]…}`.

**FR-P-2**: Specs SHALL be referenced BY PATH (`docs/rewrite-plan/<specRef>`), never inlined.

**FR-P-3**: Dep comments SHALL come from the most recent `task-finish` event per dep in `task-log.jsonl`. Missing → `(no comment)`.

**FR-P-4**: The prompt SHALL be written to `orchestrator/state/prompts/<run-id>/<task-id>.txt` and passed to the CLI by file path. MUST NOT interpolate the prompt into a shell command line.

**FR-P-5**: The prompt SHALL embed a fixed marker `TASK_ID=<id>` for the spoofing check (§C-2).

### 1.5 `state.py`

**FR-STATE-1**: On startup, CWD MUST contain both `tasks.json` and `scripts/task-start.sh`. Otherwise exit 2: `orchestrator must be run from v2/ root`.

**FR-STATE-2**: The orchestrator MUST NOT write `tasks.json` directly. All status transitions SHALL go through `scripts/task-{start,finish,block}.sh` (§C-1..C-3).

**FR-STATE-3**: Write `orchestrator/state/run-<uuid>.json` = `{run_id, started_at, mode, in_flight{task_id:{pid,backend,session_id,started_at}}, completed[], blocked[]}`. Updates SHALL be atomic (temp+rename).

**FR-STATE-4**: Acquire advisory `flock` on `orchestrator/state/.lock` before any dispatch. Contention → exit 3 with the holder's run-id.

**FR-STATE-5**: `--resume <run-id>` SHALL read the run file; for each `in_flight` PID, `os.kill(pid, 0)`. Alive → adopt. Dead → invoke reconciler that WARNs, and treats the task as needing re-dispatch (revert to `todo` via a reconciliation helper — direct `tasks.json` write forbidden).

**FR-STATE-6**: Every completed dispatch SHALL append one JSONL line to `orchestrator/state/spend-<YYYY-MM-DD>.jsonl` = `{ts, task_id, backend, model, tokens_in, tokens_out, cost_usd}`.

**FR-STATE-7**: Every state transition SHALL emit one JSONL event to `orchestrator/state/events-<run-id>.jsonl` = `{event_type, task_id, backend, ts, extra{}}`. Locked event types (10): `dispatch, success, fail, block, timeout, retry, resume_adopt, resume_revert, id_spoof_detected, flock_contention`.

> Event names harmonized with implementation on 2026-08-19; spec previously used `exit_ok/exit_err/resume_reset/dry_run_planned` — those are superseded. `success`/`fail` replace `exit_ok`/`exit_err`; `resume_revert` replaces `resume_reset`; `dry_run_planned` is removed (dry-run emits nothing to disk — see AS-08); `id_spoof_detected` and `flock_contention` are added for AS-10 and AS-09 diagnostics.

### 1.6 `checkpoints.py` — Semi-mode gate

**FR-C-1**: In `--mode semi`, before each dispatch, the task matches the predicate iff ANY of: (a) `router.tier == "premium"` (opus, gpt-5.6, gemini-3.0-pro, deepseek-v4-pro); (b) `files[]` intersects `supabase/migrations/**`, `supabase/functions/**/index.ts`, or `packages/*/lib/{auth,billing,security}/**`; (c) `phase == 10` AND `router.tier == "premium"`; (d) `estimateHours >= 10`.

**FR-C-2**: On match, block main thread on `input("[y/N/skip/quit] <id> <phase> <model> ({reason}): ")`. Responses: `y` dispatch now; `N`/empty defer (re-queue end of ready-set, re-prompt at end of run); `skip` invoke `task-block.sh <id> "operator skipped" <model>` and do NOT dispatch; `quit` stop taking new dispatches, drain in-flight, exit 0.

**FR-C-3**: In `--mode auto`, `checkpoints.py` MUST be inert — no `input()` calls.

---

## 2) Acceptance Scenarios

**AS-01 — Happy path (3 roots)**
- GIVEN 3 root tasks, all `claude`, `per_provider.claude=3`.
- WHEN `orch --mode auto` runs.
- THEN all 3 dispatch within 1 s of each other, all reach `done`, exit 0, run file shows `completed:[3]`.

**AS-02 — Blocked halts descendants**
- GIVEN task X with dependent Y (`Y.deps=[X]`).
- WHEN X's agent calls `task-block.sh X ...` and exits 0.
- THEN orchestrator records `X:blocked`, does NOT retry X, does NOT dispatch Y; `blocked_transitively(Y)==true`.

**AS-03 — Timeout enforcement**
- GIVEN task T with `estimateHours=0.01` (54 s cap) whose agent sleeps forever.
- WHEN 54 s elapse.
- THEN SIGTERM, wait 10 s, SIGKILL; `task-block.sh` invoked with reason `"orchestrator timeout"`; `events-*.jsonl` has `event_type:"timeout"`.

**AS-04 — Semi-mode premium prompt**
- GIVEN `--mode semi` and a task whose router entry is `tier:premium`.
- WHEN the resolver marks it ready.
- THEN operator sees `[y/N/skip/quit] <id> <phase> <model> ({reason})`; `y` dispatches, `N` defers, `skip` blocks with `"operator skipped"`, `quit` drains and exits 0.

**AS-05 — Per-provider cap holds**
- GIVEN 10 ready claude tasks and `per_provider.claude=3`.
- WHEN `orch --mode auto` runs.
- THEN at NO observed instant does in-flight claude PID count exceed 3; all 10 eventually terminal.

**AS-06 — Router miss fails fast**
- GIVEN a `tasks.json` model string with no `model_router.yaml` entry.
- WHEN `orch --mode auto` runs.
- THEN offending `(task_id, model)` printed, exit 1, BEFORE any subprocess; no run file, no state files created.

**AS-07 — Resume after kill**
- GIVEN 2 in-progress tasks (PIDs 1000, 1001); orchestrator `kill -9`'d; PID 1000 dies, 1001 lives.
- WHEN `orch --resume <run-id>` runs.
- THEN emits `resume_adopt` for 1001, `resume_reset` for 1000's task (re-queued to `todo`); no task in `in_flight` across two run files simultaneously.

**AS-08 — Dry-run is side-effect-free**
- GIVEN any valid `tasks.json`.
- WHEN `orch --dry-run` runs.
- THEN stdout lists each planned `<id> <backend> <cli_model> <prompt-path> <est_cost_usd?>`; exit 0; NO `claude|codex|opencode` child processes spawned; NO run file, spend log, or events file created on disk; NO event emitted (the plan is printed to stdout only — the removed `dry_run_planned` event type reflected an earlier design and is intentionally gone).

**AS-09 — Concurrent instances**
- GIVEN one `orch` holds `orchestrator/state/.lock`.
- WHEN a second `orch` is invoked in same `v2/`.
- THEN second exits 3 within 1 s with `another orchestrator is running: run-id=<uuid>, pid=<pid>. wait or --resume <uuid>`; first is unaffected.

**AS-10 — Id-spoofing defense**
- GIVEN task A dispatched with prompt embedding `TASK_ID=A`.
- WHEN agent invokes `task-finish.sh B ...` (wrong id).
- THEN orchestrator's post-check greps its captured tool-use stream, detects mismatch, REFUSES success, invokes `task-block.sh A "id spoofing detected"`; task B is untouched.

**AS-11 — Unauthorized file edits blocked**
- GIVEN task X with `files:["packages/app/foo.ts"]` and `strict_files: true`.
- WHEN agent exits 0 but `git status` shows edits to `supabase/migrations/999.sql`.
- THEN orchestrator reverts the unauthorized path (`git checkout --`) and calls `task-block.sh X "unauthorized edit outside files[]"`.

**AS-12 — Cost log feeds dashboard**
- GIVEN a running orchestrator that just recorded a `task-finish` for one task.
- WHEN 1 s passes.
- THEN `spend-<today>.jsonl` has ≥ 1 line with all keys `{ts, task_id, backend, model, tokens_in, tokens_out, cost_usd}` non-null; dashboard `fetch('../orchestrator/state/spend-<today>.jsonl', {cache:'no-store'})` returns 200.

---

## 3) Non-Functional Requirements

**NFR-PERF-1**: Dispatch-decision path (DAG walk + router lookup + semaphore check, excluding subprocess spawn) SHALL complete < 500 ms per ready task on the 334-task graph on the reference dev machine.

**NFR-OBS-1**: Every state transition MUST emit exactly one JSONL line to `events-<run-id>.jsonl` (schema §FR-STATE-7 — locked; extension needs spec update).

**NFR-OBS-2**: Every completed dispatch MUST append exactly one line to `spend-<YYYY-MM-DD>.jsonl`. Dashboard MUST tolerate partial trailing lines (skip unparseable).

**NFR-PORT-1**: macOS ≥ 13 and modern Linux glibc; Python ≥ 3.11; no C extensions; runtime deps limited to `pyyaml` and `rich`.

**NFR-SAFETY-1**: Orchestrator MUST NOT open network sockets, MUST NOT write outside `orchestrator/state/`, and MUST NOT modify any file under `v2/` except via spawned CLI subprocesses.

---

## 4) Contracts With Existing Code

**C-1** — `task-start.sh <id>`: orchestrator SHALL invoke BEFORE spawning the agent. Non-zero exit aborts the dispatch and blocks the task with the script's stderr as reason.

**C-2** — `task-finish.sh <id> "<comment>" "<model>"`: MAY be invoked by the agent OR by the orchestrator post-check. Orchestrator MUST post-check: (a) subprocess exit == 0, (b) if `files[]` non-empty, `git status` shows changes intersecting `files[]`, (c) tool-use grep confirms `task-finish.sh <id>` with correct id. ANY check failing → override to `task-block.sh` with the discrepancy as reason.

**C-3** — `task-block.sh <id> "<reason>" "<model>"`: MAY be invoked by agent or orchestrator. `<model>` MUST be the actual `cli_model` used (post version-drift substitution), not the raw `tasks.json` string.

**C-4** — `tasks.json` is read-only. Fields consumed: `id, title, description, phase, model, estimateHours, files, deps, status`. Schema extensions MUST NOT break the orchestrator.

**C-5** — `dashboard/index.html` SHALL fetch `../orchestrator/state/spend-<today>.jsonl` on its existing 5 s tick. Panel layout is `sdd-design` scope; the CONTRACT is: file at that path with NFR-OBS-2 schema; dashboard hides the panel gracefully on 404.

---

## 5) Out of Scope (from proposal §2)

Multi-machine / distributed dispatch; ~~retry-with-different-model auto-escalation (blocked stays blocked)~~ [superseded by FR-D-8 Amendment 2026-08 — LIMITED, OPT-IN, per-route escalation on attempt 3 is now in scope; blanket auto-escalation across ALL routes remains out of scope]; editing `tasks.json` schema or rewriting `scripts/task-*.sh`; auto-fixing model-string mismatches in `tasks.json` (router absorbs drift); sandbox jail beyond native CLI flags; per-file locking as a correctness guarantee (only opportunistic on the 33 tasks with declared files).
