# Tasks: Rupies v2 Task Orchestrator (Rollout)

Traceability: task IDs `R-NNN`. Every acceptance line points back to a `FR-*` / `AS-*` / `NFR-*` / `C-*` in `orchestrator/spec.md`, or a design section (`§N`) in `orchestrator/design.md`.

Rollout order enforces the walking-skeleton locked in `proposal.md §Rollout`: **queue+state → dispatcher(claude) → prompt_builder → codex/opencode → checkpoints → resume/reconcile → dashboard**.

---

## Phase 1 — Scaffolding (R-001 .. R-003)

### R-001 — Create package skeleton and `pyproject.toml`

- **Depends on**: —
- **Files touched**:
  - `orchestrator/pyproject.toml`
  - `orchestrator/__init__.py`
  - `orchestrator/__main__.py` (stub: `from .orch import main; raise SystemExit(main())`)
  - `orchestrator/README.md` (stub)
- **Estimate**: 1 h
- **Acceptance**:
  - `python -m orchestrator --help` exits 0 with a placeholder help text (foundation for FR-CLI-1, FR-CLI-4).
  - `pyproject.toml` declares Python `>=3.11`, runtime deps `pyyaml`, `rich` (NFR-PORT-1), dev dep `pytest`.
  - Package importable via `python -c "import orchestrator"`.
- **Notes**: No logic yet — the stub `main()` returns `0` so R-021 can replace it.

### R-002 — Establish directory layout and `.gitignore`

- **Depends on**: R-001
- **Files touched**:
  - `orchestrator/state/.gitkeep`
  - `orchestrator/state/prompts/.gitkeep`
  - `orchestrator/state/logs/.gitkeep`
  - `orchestrator/tests/__init__.py`
  - `orchestrator/tests/fixtures/.gitkeep`
  - `.gitignore` (append `orchestrator/state/*` with `!.gitkeep`)
- **Estimate**: 1 h
- **Acceptance**:
  - Directory tree matches `design.md §1`.
  - `git status` after a fake run-file write shows no tracked artifacts under `state/` (NFR-SAFETY-1).
- **Notes**: Root proposal locks state as gitignored.

### R-003 — Add tiny-DAG fixture for downstream tests

- **Depends on**: R-002
- **Files touched**:
  - `orchestrator/tests/fixtures/tiny_tasks.json` (5-task DAG: 2 roots, 1 chain, 1 leaf, 1 blocked)
  - `orchestrator/tests/fixtures/backend-outputs/claude_ok.json`
  - `orchestrator/tests/fixtures/backend-outputs/codex_ok.jsonl`
  - `orchestrator/tests/fixtures/backend-outputs/opencode_ok.jsonl`
- **Estimate**: 2 h
- **Acceptance**:
  - Fixture DAG covers ready-set, blocked propagation, and one `files[]` task (feeds R-008, R-016, AS-01, AS-02).
  - Canned backend outputs contain the fields cost extraction needs (`design.md §5` table).

---

## Phase 2 — Data model + routing (R-004 .. R-007)

### R-004 — Implement dataclasses in `state.py` module seed

- **Depends on**: R-001
- **Files touched**:
  - `orchestrator/state.py` (dataclasses only, no I/O)
- **Estimate**: 2 h
- **Acceptance**:
  - `Task`, `RouteEntry`, `Dispatch`, `RunState` present with fields per `design.md §2`.
  - `Task` and `RouteEntry` are `frozen=True`; `Dispatch`/`RunState` are mutable.
  - Round-trip test: dataclass → `asdict` → JSON → back matches (foundation for FR-STATE-3).

### R-005 — Seed `model_router.yaml` covering every `tasks.json` model

- **Depends on**: R-004
- **Files touched**:
  - `orchestrator/model_router.yaml`
  - `orchestrator/config.yaml` (concurrency caps, timeouts, `budget.per_dispatch_usd`, `strict_files_phases: [10]`)
- **Estimate**: 3 h
- **Acceptance**:
  - Every unique `model` string in `/Volumes/PortableSSD/rupies/v2/tasks.json` has a `{backend, cli_model, tier}` entry (FR-D-1, FR-D-6).
  - `tier` values restricted to `premium|standard|cheap`; opus / gpt-5.6 / gemini-3.0-pro / deepseek-v4-pro marked `premium` (FR-C-1a).
  - `config.yaml` has `concurrency.per_provider.{claude,codex,opencode}` and `concurrency.global_max` (FR-D-5).
- **Notes**: Do NOT edit `tasks.json` (proposal Q1 decision).

### R-006 — Implement router loader + validator

- **Depends on**: R-005
- **Files touched**:
  - `orchestrator/router.py`
  - `orchestrator/tests/test_router.py`
- **Estimate**: 2 h
- **Acceptance**:
  - `load_router(path) -> dict[str, RouteEntry]` parses YAML and computes `is_premium`.
  - `validate_routes(tasks, router)` raises `RouteError` listing offending `(task_id, model)` pairs when unrouted (FR-D-6, AS-06).
  - Test proves unrouted model triggers exit-1 pathway with no subprocess side effects.

### R-007 — Add cost-extraction table + `DispatchResult` type

- **Depends on**: R-004, R-003
- **Files touched**:
  - `orchestrator/dispatcher.py` (types + parse helpers only, no Popen yet)
  - `orchestrator/tests/test_cost_extraction.py`
- **Estimate**: 3 h
- **Acceptance**:
  - `DispatchResult` dataclass `{ok, cost_usd, tokens_in, tokens_out, reason, task_finish_id}` present (`design.md §5`).
  - `parse_claude`, `parse_codex`, `parse_opencode` extract `cost_usd`, `tokens_in/out` from R-003 fixtures (FR-D-2 rows).
  - Failure paths (missing fields, `is_error:true`) return `ok=False` with reason (FR-D-4).

---

## Phase 3 — DAG queue (R-008 .. R-009)

### R-008 — Implement `task_queue.py` with dynamic Kahn + cycle detection

- **Depends on**: R-004, R-003
- **Files touched**:
  - `orchestrator/task_queue.py`
- **Estimate**: 4 h
- **Acceptance**:
  - `TaskQueue(tasks, only=None, limit=None)` filters by phase glob and caps count (FR-CLI-2 `--only`, `--max-tasks`).
  - `ready()` returns tasks with all deps `done` and self `todo` (FR-Q-2, FR-Q-5).
  - `_detect_cycles()` raises `CycleError(ids)` at load (FR-Q-3).
  - Missing dep ID → raises at load (FR-Q-4).
  - `mark_done` / `mark_blocked` / `mark_in_flight` mutate the ready-set correctly (`design.md §4`).

### R-009 — Unit tests for `task_queue`

- **Depends on**: R-008
- **Files touched**:
  - `orchestrator/tests/test_task_queue.py`
- **Estimate**: 2 h
- **Acceptance**:
  - Tests cover: happy ready-set, blocked propagation to descendants (AS-02), cycle detection (FR-Q-3), missing dep (FR-Q-4), `--only` glob, `--max-tasks` cap.
  - NFR-PERF-1 spot check: `ready()` on 334-task fixture-equivalent runs < 500 ms.

---

## Phase 4 — State + shell-out + flock (R-010 .. R-011)

### R-010 — Implement `state.py` I/O layer (tasks.json read, run-file, events, spend, flock)

- **Depends on**: R-004
- **Files touched**:
  - `orchestrator/state.py` (extend R-004)
- **Estimate**: 5 h
- **Acceptance**:
  - `load_tasks(path)` reads `tasks.json` read-only; verifies `ensure_cwd_is_v2_root()` (`tasks.json` + `scripts/task-start.sh` present) or raises exit-2 (FR-STATE-1, AS-08 no-side-effect guard).
  - `acquire_flock(path)` returns lock or raises `LockedError` with holder run-id → exit 3 (FR-STATE-4, AS-09).
  - `write_run_file(run)` atomic (tmp + `os.replace`) at `state/run-<uuid>.json` (FR-STATE-3).
  - `append_event(event_type, task_id, backend, extra)` writes one line to `state/events-<run-id>.jsonl` with locked schema `{event_type, task_id, backend, ts, extra}` and event types `dispatch|exit_ok|exit_err|timeout|retry|block|resume_adopt|resume_reset|dry_run_planned` (FR-STATE-7, NFR-OBS-1).
  - `append_spend(entry)` writes one line to `state/spend-<YYYY-MM-DD>.jsonl` (UTC) with schema `{ts, task_id, backend, model, tokens_in, tokens_out, cost_usd, duration_s}` (FR-STATE-6, NFR-OBS-2, AS-12).
  - `shell_task_start/finish/block(id, ...)` wrap `scripts/task-*.sh` via `subprocess.run(cwd=v2_root, check=True)`; orchestrator never writes `tasks.json` directly (FR-STATE-2, C-1..C-3).

### R-011 — `--resume` reconciliation logic

- **Depends on**: R-010, R-008
- **Files touched**:
  - `orchestrator/state.py` (add `reconcile()`)
  - `orchestrator/tests/test_state_reconcile.py`
- **Estimate**: 3 h
- **Acceptance**:
  - `reconcile(run_state, tasks)` per `in_flight`: `os.kill(pid, 0)` alive → emit `resume_adopt`; dead + `files[]` non-empty + `git diff` touches them → invoke `task-finish.sh` with `recovered: ...` comment; else revert to `todo` via reconciler helper and emit `resume_reset` (FR-STATE-5, AS-07, `design.md §7`).
  - For `files == []` (301/334 tasks) the heuristic path unconditionally reverts to `todo` and WARNs.
  - Test proves no task appears in `in_flight` across two run files (AS-07).

---

## Phase 5 — Prompt builder (R-012)

### R-012 — Implement `prompt_builder.py` with golden-file test

- **Depends on**: R-004, R-010
- **Files touched**:
  - `orchestrator/prompt_builder.py`
  - `orchestrator/tests/test_prompt_builder.py`
  - `orchestrator/tests/fixtures/prompts/*.golden.txt` (3 variants)
- **Estimate**: 3 h
- **Acceptance**:
  - `build(task, deps, v2_root, out_dir) -> Path` writes `state/prompts/<run-id>/<task-id>.txt` (FR-P-4, approved decision on prompt path).
  - Template body follows `explore.md §3` exactly (FR-P-1).
  - Spec ref rendered BY PATH, never inlined (FR-P-2); missing spec file renders `NOT FOUND` marker + WARN.
  - Dep comments pulled from last `task-finish` in `task-log.jsonl`, truncated to 500 chars; missing → `(no comment)` (FR-P-3).
  - Prompt embeds `TASK_ID=<id>` marker for spoofing check (FR-P-5, AS-10).
  - Three golden files cover: empty deps, missing spec, `files == []`.

---

## Phase 6 — Dispatcher backends (R-013 .. R-015)

### R-013 — Implement `Backend` protocol + `ClaudeBackend` adapter (walking-skeleton)

- **Depends on**: R-007, R-012, R-010
- **Files touched**:
  - `orchestrator/dispatcher.py` (extend R-007)
  - `orchestrator/tests/test_dispatcher_claude.py`
- **Estimate**: 4 h
- **Acceptance**:
  - `Backend` `Protocol` per `design.md §5`.
  - `ClaudeBackend.build_cmd` matches FR-D-2 row for claude (`-p --output-format json --model --session-id --add-dir v2 --max-budget-usd --permission-mode acceptEdits`); prompt delivered via `stdin=open(prompt_path)` (approved decision, `design.md OPEN`).
  - `parse_result` uses R-007 helper; retry-once path returns `attempt=2` (FR-D-4).
  - Timeout wrapper: SIGTERM → 10 s wait → SIGKILL, reason `"orchestrator timeout"` (FR-D-3, AS-03).
  - Version-drift substitution emits ONE WARN per unique substitution at startup (FR-D-7).

### R-014 — Add `CodexBackend` and `OpencodeBackend` adapters

- **Depends on**: R-013
- **Files touched**:
  - `orchestrator/dispatcher.py` (extend)
  - `orchestrator/tests/test_dispatcher_codex.py`
  - `orchestrator/tests/test_dispatcher_opencode.py`
- **Estimate**: 4 h
- **Acceptance**:
  - `CodexBackend.build_cmd` matches FR-D-2 codex row (`codex exec --skip-git-repo-check --json -o <out> -C v2 -s workspace-write -m <m>`); prompt via stdin.
  - `OpencodeBackend.build_cmd` matches FR-D-2 opencode row (`opencode run --format json --auto --session <uuid> --model <m>`); prompt via stdin.
  - Success predicates match spec: codex final message in `-o` file with no `type:"error"`; opencode terminal `step_finish` `reason:"stop"` (FR-D-2).
  - Cost extraction sums `step_finish.cost` for both (`design.md §5` table).

### R-015 — `FakeBackend` + end-to-end integration test on tiny DAG

- **Depends on**: R-013, R-011, R-008
- **Files touched**:
  - `orchestrator/tests/fakes.py`
  - `orchestrator/tests/test_integration_tiny.py`
- **Estimate**: 3 h
- **Acceptance**:
  - `FakeBackend` returns canned `DispatchResult` and spawns a real short-lived child so `os.waitpid(-1, WNOHANG)` path is exercised (`design.md §9`, §12).
  - Integration test drives tiny 5-task DAG end-to-end with the fake, asserting: `task-start`/`task-finish` invoked in order, `spend-*.jsonl` has 5 lines with all keys non-null (AS-12), `events-*.jsonl` covers `dispatch|exit_ok` per task (NFR-OBS-1), semaphore never exceeded (AS-05).
  - Post-check: id-spoofing (agent calls `task-finish.sh B` for task A) triggers `state.block(A, "agent reported wrong task id")` (AS-10, C-2).

---

## Phase 7 — Semi-mode gate (R-016)

### R-016 — Implement `checkpoints.py` (predicate + stdin gate)

- **Depends on**: R-008, R-006
- **Files touched**:
  - `orchestrator/checkpoints.py`
  - `orchestrator/tests/test_checkpoints.py`
- **Estimate**: 3 h
- **Acceptance**:
  - `needs_gate(task, route)` returns True iff ANY: `route.is_premium`; `files[]` touches `supabase/migrations/**` or `supabase/functions/**/index.ts` or `packages/*/lib/{auth,billing,security}/**`; `phase == 10 AND route.is_premium`; `estimate_hours >= 10` (FR-C-1).
  - `ask(...)` returns `y|N|s|q` from `input()` prompt matching FR-C-2 wording `[y/N/skip/quit] <id> <phase> <model> ({reason})` (AS-04).
  - Handler: `y`→dispatch, `N`→defer to end of run, `s`→`task-block.sh <id> "operator skipped" <model>` + `mark_blocked`, `q`→set `draining=True` and drain in-flight (AS-04, FR-C-2).
  - In `--mode auto`, `checkpoints` module is inert — no `input()` reached (FR-C-3).
  - Parametrized predicate matrix covers all four branches.

---

## Phase 8 — CLI entry + main loop (R-017 .. R-018)

### R-017 — Implement `orch.py` argparse + `--dry-run` path

- **Depends on**: R-006, R-008, R-010
- **Files touched**:
  - `orchestrator/orch.py`
  - `orchestrator/tests/test_orch_dry_run.py`
- **Estimate**: 4 h
- **Acceptance**:
  - argparse defines `--mode {auto,semi}`, `--dry-run`, `--config`, `--only`, `--resume`, `--max-tasks` with defaults per FR-CLI-2 (approved rename to `--max-tasks`).
  - `--help` output ≤ 80 lines listing flags, exit codes, and CWD contract (FR-CLI-4).
  - `--dry-run` prints `<id> <backend> <cli_model> <prompt-path> <est_cost_usd?>`, exits 0, spawns NO subprocesses, creates NO run/spend/events files (AS-08).
  - Exit codes 0/1/2/3/130 wired to their triggers (FR-CLI-3, approved decision).
  - Emits `dry_run_planned` events into a memory-only buffer (never touches disk in dry-run) — proves the schema.
- **Notes**: This is the earliest slice usable against real `tasks.json`. Ship after this task.

### R-018 — Implement main dispatch loop + signal handlers + concurrency caps

- **Depends on**: R-017, R-015, R-016, R-011
- **Files touched**:
  - `orchestrator/orch.py` (extend R-017)
  - `orchestrator/tests/test_orch_main_loop.py`
- **Estimate**: 6 h
- **Acceptance**:
  - Main loop implements `design.md §3` pseudocode using `threading.Semaphore` + `os.waitpid(-1, WNOHANG)` in a single-threaded loop (approved decision — NOT `asyncio.subprocess`).
  - Per-provider + global semaphores acquired non-blocking; in-flight count never exceeds cap on 10-ready-3-cap fixture (AS-05, FR-D-5).
  - SIGINT → set `draining=True`, wait for in-flight, exit 130; 2nd SIGINT → SIGKILL children (`proposal.md §Rollback`).
  - `--resume <run-id>` calls `state.reconcile()` before entering loop (FR-STATE-5, AS-07).
  - Post-dispatch checks: git-diff-outside-files → WARN, and if `task.phase in cfg.strict_files_phases` (default `[10]`) → `git checkout --` + `state.block(id, "unauthorized edit outside files[]")` (AS-11, approved decision, `design.md OPEN`).
  - Happy-path AS-01: 3 root claude tasks dispatch within 1 s, all reach `done`, exit 0, run file `completed:[3]`.

---

## Phase 9 — Dashboard budget panel (R-019 .. R-020)

### R-019 — Add budget panel HTML/CSS/JS to `dashboard/index.html`

- **Depends on**: R-010
- **Files touched**:
  - `../dashboard/index.html` (i.e. `/Volumes/PortableSSD/rupies/v2/dashboard/index.html`)
- **Estimate**: 3 h
- **Acceptance**:
  - New `<section id="budget-panel">` with two cards: today-by-provider bars + last-20-dispatches table (`design.md §11`).
  - `fetchSpend()` polls `../orchestrator/state/spend-${todayUTC()}.jsonl` on the existing 5 s tick with `cache:'no-store'`; skips unparseable lines (NFR-OBS-2, C-5).
  - Panel hides on 404 (dashboard tolerates missing file — C-5).
  - No new build steps or JS dependencies.

### R-020 — Smoke test: dashboard reads a live spend file within 5 s

- **Depends on**: R-019, R-018
- **Files touched**:
  - `orchestrator/tests/test_dashboard_smoke.md` (manual checklist doc)
- **Estimate**: 1 h
- **Acceptance**:
  - Manual: run `scripts/serve.sh`, launch orchestrator on tiny DAG, observe budget panel populated with ≥ 1 row within 5 s of first `task-finish` (AS-12, `proposal.md §Success Criteria`).
  - Malformed trailing line does NOT crash panel (NFR-OBS-2).

---

## Dependency graph summary

```
R-001 ──▶ R-002 ──▶ R-003 ──▶ (R-008, R-013, R-015)
  │
  └──▶ R-004 ──▶ R-005 ──▶ R-006 ──▶ R-017 ──▶ R-018 ──▶ R-020
              └──▶ R-007 ──▶ R-013 ──▶ R-014
              └──▶ R-010 ──▶ R-011 ──▶ R-015
                          └──▶ R-012 ──▶ R-013
                          └──▶ R-019 ──▶ R-020
       R-008 ──▶ R-009
       R-008 ──▶ R-016 ──▶ R-018
```

Longest chain: `R-001 → R-004 → R-010 → R-011 → R-015 → R-018 → R-020` (7 nodes).

Total: **20 tasks**, estimate ~63 hours.
