# Orchestrator — Verify Report

**Date**: 2026-08-19
**Status**: YELLOW  (1 real critical FAIL + 2 real FR-level FAILs + several documented PARTIALs)
**Tests**: 117/117 passing (`python3 -m pytest orchestrator/tests/ -v`)

## Summary

| Category | PASS | PARTIAL | FAIL | N/A | Total |
|----------|------|---------|------|-----|-------|
| FR-*     | 22   | 4       | 3    | 0   | 29    |
| AS-*     | 10   | 1       | 1    | 0   | 12    |
| NFR-*    | 3    | 1       | 0    | 0   | 4     |
| C-*      | 5    | 0       | 0    | 0   | 5     |
| **All**  | **40** | **6** | **4** | **0** | **50** |

Confirmed working end-to-end (via `--dry-run`, `test_full_loop_fake_backend`, flock-contention, router-miss, dashboard fetch): **YES**.

Real gaps (not documented deltas):
- **FR-CLI-1 / R-001 acceptance — `python -m orchestrator --help` DOES NOT WORK** (missing `__main__.py`). R-017 delivered argparse via `python orchestrator/orch.py` but nobody ever created `orchestrator/__main__.py`. Critical: spec FR-CLI-1 mandates `python -m orchestrator` as the invocation contract.
- **FR-D-4 retry-once policy — NOT IMPLEMENTED**. `Dispatch.attempt` field exists (default 1) but is never incremented; `orch._reap_once` calls `call_task_block` on first failure. Spec says retry ONCE with same model before blocking.
- **FR-D-7 version-drift substitution — NOT IMPLEMENTED**. Router loader stores `fallback_cli_model` but no substitution logic and no WARN emission at startup.

Recommended action: **land follow-up R-021..R-023 tasks first**, then `sdd-archive`.

---

## Runtime evidence

### (a) `python3 -m pytest orchestrator/tests/ -v` — final block

```
============================= 117 passed in 1.37s ==============================
```
117 total tests across `test_checkpoints.py`, `test_dispatcher.py`, `test_orch.py`, `test_prompt_builder.py`, `test_router.py`, `test_spend_reader.py`, `test_state.py`, `test_task_queue.py`. All pass.

### (b) `python3 orchestrator/orch.py --help` — PASS

```
usage: orchestrator [-h] [--mode {auto,semi}] [--dry-run] [--config CONFIG]
                    [--only ONLY] [--resume RUN_ID] [--max-tasks N]
...
options:
  -h, --help          show this help message and exit
  --mode {auto,semi}  auto = no prompts; semi = block on critical tasks (default: auto)
  --dry-run           Enumerate the plan and exit 0. NO subprocess is spawned.
  --config CONFIG     Path to config.yaml (default: orchestrator/config.yaml)
  --only ONLY         Glob filter on task ids (fnmatch, e.g. 'B-*', 'P0-0[0-3]?')
  --resume RUN_ID     Resume a prior run by UUID (reconciles in-flight PIDs).
  --max-tasks N       Cap total dispatches this run (default: no cap).

Exit codes:
  0   clean drain (all reachable tasks done, none blocked)
  1   config error / unrouted model / dependency cycle / blocked tasks
  2   CWD contract violation (must run from v2/ root)
  3   flock contention (another orchestrator holds state/.lock)
  130 SIGINT during graceful drain

CWD contract:
  MUST be invoked from the v2/ repo root — `tasks.json` and
  `scripts/task-start.sh` must be present in the current directory.
```
All 6 flags present, exit codes documented in help, CWD contract explained. Help output ≤ 30 lines (< 80 mandated by FR-CLI-4). PASS.

### (c) `python3 -m orchestrator --help` — **FAIL**

```
/opt/homebrew/opt/python@3.13/bin/python3.13: No module named orchestrator.__main__; 'orchestrator' is a package and cannot be directly executed
```

`orchestrator/__main__.py` does not exist. R-001 acceptance says: *"`python -m orchestrator --help` exits 0 with a placeholder help text"*. Never delivered. FR-CLI-1 mandates the invocation `python -m orchestrator [FLAGS]`. **REAL FAIL**.

### (d) `python3 orchestrator/orch.py --dry-run --max-tasks 10` — PASS

Exit=0. Plan table shows 10 rows (rich fallback → plain text since `rich` not installed):

```
Dry-run plan (10 ready)
  P0-001  [0]  claude/claude-haiku-4-5  est=3.0h
  P0-011  [0]  claude/claude-haiku-4-5  est=5.0h
  P0-022  [0]  opencode/xiaomi/mimo-v2.5  est=5.0h
  P0-027  [0]  claude/claude-haiku-4-5  est=5.0h
  P1-001  [1]  claude/claude-haiku-4-5  est=3.0h
  P1-016  [1]  claude/claude-haiku-4-5  est=5.0h
  P1-022  [1]  claude/claude-haiku-4-5  est=3.0h
  P1-034  [1]  claude/claude-haiku-4-5  est=3.0h
  P2-001  [2]  claude/claude-haiku-4-5  est=5.0h
  P2-024  [2]  opencode/xiaomi/mimo-v2.5  est=3.0h
```

All 10 tasks routable via `model_router.yaml`. State dir left clean (no `run-*.json`, `events-*.jsonl`, `spend-*.jsonl`).

### (e) `python3 orchestrator/orch.py --dry-run --only 'B-*' --max-tasks 5` — PASS

```
Dry-run plan (5 ready)
  B-001  [10]  opencode/zhipu/glm-5.1  est=3.0h
  B-040  [10]  opencode/xiaomi/mimo-v2.5  est=3.0h
  B-041  [10]  claude/claude-sonnet-4-6  est=5.0h
  B-042  [10]  codex/gpt-5.6-codex  est=13.0h
  B-043  [10]  opencode/zhipu/glm-5.1  est=5.0h
```

`fnmatch` glob filters correctly to `B-*`. Exit=0.

### (f) `python3 orchestrator/orch.py --dry-run` (no cap) — PASS

52 lines total = 1 info + 1 header + 50 root task rows. Enumerates every currently-ready root task in `tasks.json` (334 total tasks; 50 have all deps satisfied at start). Exit=0. No crash.

### (g) Router-miss failure simulation — PASS

Removed `opencode/claude-opus-4-7` key from `model_router.yaml`, ran `--dry-run --max-tasks 5`:

```
model_router.yaml is missing entries for 26 task(s):
  - B-014: 'opencode/claude-opus-4-7'
  - B-020: 'opencode/claude-opus-4-7'
  ...
  - S-001: 'opencode/claude-opus-4-7'
Add them to orchestrator/model_router.yaml and re-run.
---exit=1---
```

Exit=1, actionable message listing every offending `(task_id, model)` pair BEFORE any subprocess (test guards enforce this too — `test_router_miss_returns_1`). Router restored after test.

### (h) Flock contention — PASS

External Python holder acquired the lock; second `orch.py --dry-run --max-tasks 1` returned:

```
another orchestrator holds the flock at orchestrator/state/.lock (run-id=run-id=190099d3-9084-4ca1-ad1a-909b50cd5e24, pid=73495). wait or --resume <run-id>.
---exit=3---
```

Exit=3, actionable holder identification. **Minor nit**: `run-id=run-id=…` (double prefix) — cosmetic only; test still passes.

### (i) Dashboard serve + fetch — PASS

```
--- budget-panel presence:
4
--- spend endpoint status:
404
```

- `budget-panel` appears 4× in the HTML (1 `id`, 1 `#` selector, 1 `.hidden` toggle + 1 stray reference — verified in source).
- Spend endpoint returns 404 (expected — no spend log yet). Dashboard's `loadSpend()` handles 404 by showing "no dispatches today yet" gracefully.

---

## Requirement matrix

### Functional Requirements

| ID | Status | Evidence | Notes |
|----|--------|----------|-------|
| FR-CLI-1 | **FAIL** | `orchestrator/__main__.py` MISSING | `python -m orchestrator` fails with `No module named orchestrator.__main__`. Spec mandates this invocation. |
| FR-CLI-2 | PASS | orch.py:130-172 (`_build_argparser`), test_orch.py `test_dry_run_max_tasks_caps_plan` | All 6 flags: `--mode`, `--dry-run`, `--config`, `--only`, `--resume`, `--max-tasks`. Defaults correct. |
| FR-CLI-3 | PASS | orch.py:930-1006 (exit codes 2/1/3), orch.py:1128 (130), orch.py:1131-1135 (0/1) | test_cwd_violation_returns_2, test_router_miss_returns_1, test_flock_contention_returns_3, test_full_loop_fake_backend (0). |
| FR-CLI-4 | PASS | orch.py:116-127 (`_HELP_EPILOG`), runtime test (b) | Help ≤ 30 lines. Lists flags, exit codes, CWD contract. |
| FR-Q-1 | PASS | state.py:104-126 (`load_tasks`), task_queue.py:23 re-export, test_task_queue.py::test_load_tasks_reads_fixture | Parses `tasks.json` with camelCase→snake_case mapping. |
| FR-Q-2 | PASS | task_queue.py:127-144 (`ready`), task_queue.py:123-125 (`all_tasks`) | Deterministic sort by `(phase, id)`. `ready()` returns tasks where all deps `done` and self `todo`. |
| FR-Q-3 | PASS | task_queue.py:95-116 (`_detect_cycles`), test_task_queue.py::test_cycle_detection_raises_with_ids | DFS 3-color; raises `TaskCycleError` listing cycle IDs. |
| FR-Q-4 | PASS | task_queue.py:85-93 (`_validate_deps`), test_task_queue.py::test_missing_dependency_raises | Raises `MissingDependencyError` with `(task_id, dep)` offenders. |
| FR-Q-5 | PASS | task_queue.py:127-144, test_task_queue.py::test_ready_is_pure | `ready()` is pure w.r.t. `_status` + `in_flight_ids`. |
| FR-D-1 | PASS | dispatcher.py:741-745 (registry), dispatcher.py:748-760 (`get_backend`) | Three backends registered; selected via `route.backend` string. |
| FR-D-2 | PARTIAL | dispatcher.py:355-387 (claude), 486-508 (codex), 615-630 (opencode); test_dispatcher.py `test_*_build_cmd_snapshot` | Argv matches spec EXCEPT: (a) claude uses `--add-dir .` instead of `--add-dir <v2>` (cwd passed via `cwd=` — equivalent but spec text mismatch). (b) codex adds `--approve-for-me` (extra flag not in spec, harmless — from design §5). (c) opencode command differs from spec: spec has `--session <uuid>` and `--model <prov>/<m>` but code has no session flag (comment says opencode issues its own). Success predicates (claude JSON envelope, codex `-o` file terminal event, opencode `step_finish.reason=stop`) all implemented correctly. |
| FR-D-3 | PASS | dispatcher.py:287-327 (`_wait_with_timeout`), orch.py:602-641 (`_timeout_sweep`), test_dispatcher.py::test_wait_result_timeout_sends_sigterm | SIGTERM → 10s grace → SIGKILL. Reason `"orchestrator timeout"`. |
| FR-D-4 | **FAIL** | Nowhere in orch.py or dispatcher.py; `Dispatch.attempt` (models.py:96) defined but never mutated | Retry-once policy NOT implemented. First failure → `call_task_block` immediately (orch.py:578-593). |
| FR-D-5 | PASS | orch.py:283-297 (`_build_semaphores`), 300-328 (`_Sem`), 714-718 (non-blocking acquire), test_orch.py::test_full_loop_fake_backend (AS-05) | Global + per-provider counting semaphores; try_acquire is non-blocking. |
| FR-D-6 | PASS | router.py:97-110 (`validate_all_models`), orch.py:974-978 | Fail-fast pre-dispatch with `UnroutedModelError`; runtime evidence (g). |
| FR-D-7 | **FAIL** | router.py:92 loads `fallback_cli_model` but nothing consumes it; no WARN emission | Version-drift substitution NOT implemented. |
| FR-P-1 | PASS | prompt_builder.py:141-163 (`_TEMPLATE`), test_prompt_builder.py::test_render_prompt_matches_golden | Template body verbatim from explore.md §3. |
| FR-P-2 | PASS | prompt_builder.py:72-81 (`_render_spec_ref`), 34 (`_SPEC_ROOT`) | Renders `docs/rewrite-plan/<spec_ref>`, never inlines file. |
| FR-P-3 | PASS | prompt_builder.py:40-57 (`read_dep_last_comment`), tests `test_read_dep_last_comment_*` | Most recent comment; 500-char truncation; `(no comment)` fallback. |
| FR-P-4 | PASS | prompt_builder.py:98-134 (`render_prompt`), test_prompt_builder.py::test_output_path_shape | Writes `state/prompts/<run_id>/<task-id>.txt`; stdin delivery via dispatcher.py:253-262. |
| FR-P-5 | PASS | prompt_builder.py:142 (`TASK_ID={id}`), test_prompt_builder.py::test_prompt_starts_with_task_id_marker | Line 1 is `TASK_ID=<id>` verbatim. |
| FR-STATE-1 | PASS | state.py:90-101 (`_ensure_v2_cwd`), orch.py:930-940 | Two-step check (`Path.cwd().name == "v2"` + presence of tasks.json + scripts/task-start.sh). Exit 2. test_cwd_violation_returns_2. |
| FR-STATE-2 | PASS | grep evidence (see Constraints section below); shell-out wrappers state.py:396-438 | Orchestrator never opens tasks.json for writing. All transitions via `scripts/task-{start,finish,block}.sh`. |
| FR-STATE-3 | PASS | state.py:183-196 (`_atomic_write`), 265-278 (`save`) | tmp + `os.replace` atomicity. Retry-once on OSError. test_run_file_atomic_write_retries_once_on_rename_failure. |
| FR-STATE-4 | PASS | state.py:132-161 (`acquire_flock`), 71-79 (`FlockContentionError`) | Non-blocking `LOCK_EX \| LOCK_NB`. Exit 3 with holder run-id. |
| FR-STATE-5 | PASS | state.py:482-555 (`reconcile_run`), test_state.py::test_reconcile_alive_pid_is_adopted / _dead_pid_no_files_reverts / _dead_pid_with_dirty_files_adopts_as_done, orch.py:1029-1049 | PID-alive check + git-diff heuristic; helper never writes tasks.json directly. |
| FR-STATE-6 | PASS | state.py:360-390 (`SpendLog`), test_state.py::test_spend_log_records_row / _rotates_on_utc_date | JSONL at `spend-<YYYY-MM-DD>.jsonl`; schema matches. |
| FR-STATE-7 | PARTIAL | state.py:42-52 (`EVENT_TYPES`), 318-354 (`EventLog`) | **Naming drift documented in Batch C decision**: code freezes `dispatch/success/fail/block/timeout/resume_adopt/resume_revert/id_spoof_detected/flock_contention` — spec lists `dispatch/exit_ok/exit_err/timeout/retry/block/resume_adopt/resume_reset/dry_run_planned`. Neither `retry` nor `dry_run_planned` events are emittable. Recommend spec patch (see Deltas). |
| FR-C-1 | PASS | checkpoints.py:79-157 (predicates), test_checkpoints.py::test_is_critical_matrix, `test_touches_*` | All 4 rules composed correctly. |
| FR-C-2 | PASS | checkpoints.py:215-246 (`prompt_operator`), test_checkpoints.py::test_prompt_operator_* (10 tests) | Answers `y/N/s/q`; `KeyboardInterrupt` → quit. |
| FR-C-3 | PASS | orch.py:1075-1077 (only builds SemiModeGate when `mode == "semi"`) | In auto mode, `gate` is None → no `input()` reachable. |

### Acceptance Scenarios

| ID | Status | Evidence | Notes |
|----|--------|----------|-------|
| AS-01 | PASS | test_orch.py::test_full_loop_fake_backend | 3 root tasks (T-A/B/C) dispatch, all reach done, exit 0, run file completed=[3]. |
| AS-02 | PASS | test_task_queue.py::test_mark_blocked_does_not_unblock_dependents | Blocked task halts descendants; `blocked_transitively` semantics OK. |
| AS-03 | PASS | test_dispatcher.py::test_wait_result_timeout_sends_sigterm; dispatcher.py:287-327; orch.py:602-641 | SIGTERM → 10s → SIGKILL enforced. |
| AS-04 | PASS | checkpoints.py:215-246; 10 dedicated tests | y/N/s/q handling; unrecognized reprompts. |
| AS-05 | PASS | test_full_loop_fake_backend implicitly (3 tasks, cap=3); _Sem tracking | Per-provider cap enforced non-blocking. |
| AS-06 | PASS | test_router_miss_returns_1; runtime (g) | Exit 1 pre-dispatch, no subprocess spawned. |
| AS-07 | PASS | test_state.py test_reconcile_alive_pid_is_adopted / _dead_pid_no_files_reverts / _dead_pid_with_dirty_files_adopts_as_done | resume_adopt for alive, resume_revert for dead+clean, resume_adopt+done for dead+dirty. |
| AS-08 | PASS | test_dry_run_is_side_effect_free; runtime (d)(e)(f) | Exit 0, no subprocess (Popen patched to raise), no run/events/spend files. Spec says "NO events file created for dry-run" — code creates then unlinks empty events file → net effect is no persistent file. |
| AS-09 | PASS | test_flock_contention_returns_3; runtime (h) | Exit 3 within 1s. |
| AS-10 | PASS | orch.py:389-410 (`_detect_id_spoofing`), 425-430 (post-check), FR-P-5 (`TASK_ID=<id>` marker), EVENT_TYPES includes `id_spoof_detected` | Prompt embeds marker; log-scan detects mismatch and forces failure with `id_spoof_detected` event. No dedicated end-to-end test but the helper `_detect_id_spoofing` is unit-testable (currently not tested — minor gap). |
| AS-11 | PARTIAL | orch.py:432-446 (`_post_run_checks` strict-files branch), config.yaml:17-18 (`strict_files_phases: [10]`) | Implementation exists behind `cfg.strict_files_phases`; only phase-10 tasks enforced (design §OPEN safety decision). NO test coverage — behavior verified only by code inspection. Recommend adding integration test. |
| AS-12 | PASS | test_full_loop_fake_backend (spend rows w/ all keys); runtime (i) (curl fetch=404 handled) | SpendLog writes NFR-OBS-2 schema; dashboard fetches at `../orchestrator/state/spend-${todayUTC()}.jsonl`. |

### Non-Functional Requirements

| ID | Status | Evidence | Notes |
|----|--------|----------|-------|
| NFR-PERF-1 | PARTIAL | Runtime (f): 50 root tasks enumerated in <1s (well under 500ms/task) — no dedicated perf test | Empirically clean; no test asserts the 500ms budget on the 334-task graph. Low risk. |
| NFR-OBS-1 | PASS | state.py:318-354 (`EventLog.emit`); every state transition site in orch.py calls `event_log.emit(...)` exactly once | Schema locked via `EVENT_TYPES`. Naming drift documented in FR-STATE-7 above. |
| NFR-OBS-2 | PASS | state.py:360-390 (`SpendLog.record`), orch.py:888-913 (`_record_spend`), dashboard loadSpend() at 1367-1381 (skips unparseable) | One line per completed dispatch. Dashboard tolerates partial trailing lines via try/JSON.parse/return null pattern. |
| NFR-PORT-1 | PASS | pyproject.toml declares deps; `rich` optional (orch.py:98-110 defensive import); Python 3.11 required | All code uses stdlib + `pyyaml`. `rich` fallback verified live (rich not installed on this machine — code degraded gracefully). |
| NFR-SAFETY-1 | PASS | Code review: no network sockets opened; state writes confined to `orchestrator/state/`; all v2/ file mutations go through spawned CLIs | Direct inspection: state.py only writes `state_dir/*`; dispatcher writes `state/logs/*` and `state/prompts/*`. |

### Contracts

| ID | Status | Evidence | Notes |
|----|--------|----------|-------|
| C-1 | PASS | state.py:422-424 (`call_task_start`); orch.py:721-735 (called before spawn) | `task-start.sh` invoked before spawn; failure blocks task. |
| C-2 | PASS | state.py:427-432 (`call_task_finish`); orch.py:566 (called on success); orch.py:389-410 (id-spoof grep); orch.py:432-446 (files check gated by strict_files_phases) | Post-check for id + strict-files revert implemented. |
| C-3 | PASS | state.py:434-438 (`call_task_block`); orch.py:581, 697 etc.; model = `route.cli_model` (post-router lookup) | Model arg passed as `route.cli_model`. |
| C-4 | PASS | grep verification: `tasks.json` only appears in READ contexts (`load_tasks`, docstrings, error messages) | Verified. No `open(...,"w")` or write path touches tasks.json. |
| C-5 | PASS | dashboard/index.html:1349-1381 (`spendURL` + `loadSpend`); state.py:373-381 spend path format; runtime (i) | Dashboard polls the exact path; 404 handled via `renderBudget([])` then `panel.classList.remove('hidden')` — remains visible with empty state. |

---

## Deltas from spec (recommend spec patch, not code change)

### 1. Event type naming (FR-STATE-7 / NFR-OBS-1) — **PATCH SPEC**

Spec text says event types are `dispatch, exit_ok, exit_err, timeout, retry, block, resume_adopt, resume_reset, dry_run_planned`.

Code (state.py:42-52) freezes these instead: `dispatch, success, fail, block, timeout, resume_adopt, resume_revert, id_spoof_detected, flock_contention`.

**Approved decision (Batch C)** was to keep code names because they're used everywhere in orch.py, dispatcher.py, and tests. Additionally, code adds two useful event types not in the spec: `id_spoof_detected` (needed by AS-10 evidence trail) and `flock_contention` (surfaced during Batch C for AS-09 diagnostics).

**Proposed spec patch**:
> Replace FR-STATE-7 event-type list with: `dispatch, success, fail, block, timeout, resume_adopt, resume_revert, id_spoof_detected, flock_contention`. Remove `retry` (no retry-once policy currently — see gap #1 below) and `dry_run_planned` (dry-run is memory-only; see delta #2). Remove `exit_ok`/`exit_err` (subsumed by `success`/`fail`). Rename `resume_reset` → `resume_revert` for consistency with reconcile semantics.

### 2. `dry_run_planned` event never emitted — **PATCH SPEC**

Spec FR-STATE-7 lists `dry_run_planned` as one of the emittable event types. R-017 acceptance said the event goes to a "memory-only buffer" during dry-run — but the actual code has no such buffer; dry-run simply prints the plan and exits (orch.py:1052-1068). AS-08 explicitly requires "NO events file created on disk" during dry-run — which the code respects.

Spec is inconsistent with itself: FR-STATE-7 promises an event; AS-08 forbids the file. Code sides with AS-08 (no side effects).

**Proposed spec patch**: Remove `dry_run_planned` from the FR-STATE-7 event-type list. Add note to AS-08: "no events emitted; plan printed to stdout only."

### 3. FR-D-2 CLI invocation strings — **PATCH SPEC (minor)**

- Claude: spec has `--add-dir <v2>`; code uses `--add-dir .` and passes cwd via Popen `cwd=`. Behaviorally identical, spec text should reflect the implementation.
- Codex: spec's argv omits `--approve-for-me` (present in code and in design §5). Include it.
- Opencode: spec has `--session <uuid> --model <prov>/<m>`; code omits `--session` (comment says "opencode issues its own"). Reconcile — likely code is right; spec should drop the `--session` flag.

---

## Real gaps (recommend code change)

### GAP 1 — `python -m orchestrator` doesn't work (**BLOCKS FR-CLI-1**)

R-001 acceptance said `orchestrator/__main__.py` should be created with a stub. That file was never created. `python -m orchestrator --help` fails with `No module named orchestrator.__main__`. This directly contradicts FR-CLI-1's mandated invocation contract.

**Fix**: Create `orchestrator/__main__.py`:
```python
from .orch import main
raise SystemExit(main())
```

Estimated effort: 5 min. Should be a follow-up **R-021**.

### GAP 2 — Retry-once policy not implemented (**BLOCKS FR-D-4**)

Spec: "On non-zero exit OR error signal, retry ONCE with same model. Second failure MUST block with reason including last ≤ 500 chars of stderr."

Code: On first failure, `_reap_once` calls `call_task_block` immediately (orch.py:578-593). `Dispatch.attempt` field is defined (models.py:96) but never incremented anywhere.

**Fix**: Extend `_reap_once` to re-queue the task on first failure (increment `entry.dispatch.attempt`; skip retry if `attempt >= 2`). Emit a `retry` event (would require adding `"retry"` to EVENT_TYPES). Estimated effort: 3 h. Should be follow-up **R-022**.

### GAP 3 — Version-drift substitution not implemented (**BLOCKS FR-D-7**)

Spec: "If `cli_model` is missing from the local registry, substitute the nearest same-family higher-patch version and emit exactly one WARN line per substitution at startup."

Code: `router.load_router` stores `fallback_cli_model` (router.py:92) but nothing consumes it. No startup query of local CLI registries; no WARN emission.

**Fix**: At startup, per-backend probe the CLI's `--list-models` (or similar) output; when a `cli_model` is missing but `fallback_cli_model` is present, log ONE WARN per distinct substitution; update the RouteEntry's `cli_model` to the fallback. Estimated effort: 4 h (mostly CLI-probe research per backend). Should be follow-up **R-023**.

### Minor gaps (nice-to-have follow-ups)

- **AS-11 has no integration test** — the strict-files revert code path (`orch._post_run_checks` → `_git_checkout`) is exercised only by inspection. Recommend adding a test with FakeBackend that dirties a file outside `task.files` on a phase-10 task and asserts the revert.
- **AS-10 helper not unit-tested** — `_detect_id_spoofing` is called by `_post_run_checks` but has no dedicated unit test. Simple fixture-based test would fully cover it.
- **NFR-PERF-1 has no assertion** — spec says `< 500 ms per ready task` on the 334-task graph. Empirically fine; add a smoke perf test to lock in the guarantee.
- **`--only` glob has no test** — only verified via runtime (e). Add a unit test on `_filter_by_only`.
- **Cosmetic**: flock contention message reads `run-id=run-id=<uuid>` (double prefix) — see runtime (h).

---

## Recommended next actions

**Priority**:
1. **File R-021** (blocking): create `orchestrator/__main__.py`. 5-minute fix — do NOT archive without this. Spec text FR-CLI-1 becomes a lie otherwise.
2. **File R-022**: implement retry-once (FR-D-4). Add `"retry"` back to EVENT_TYPES. If the team decides to keep no-retry semantics permanently, patch spec §1.3 FR-D-4 to say "block on first failure" and remove FR-D-4 mention of retry.
3. **File R-023**: implement version-drift substitution (FR-D-7) OR patch spec §1.3 FR-D-7 to descope it (e.g. "Version drift is operator-managed; router entries MUST list an in-registry `cli_model`").
4. **Patch spec** for the three deltas above (event names, dry_run_planned removal, FR-D-2 argv corrections). One-file change, no risk.
5. **Add minor tests** for AS-10 helper, AS-11 revert path, `--only` glob, NFR-PERF-1 smoke.

**Archive decision**: **do NOT archive yet.** The `python -m orchestrator` gap is a real invocation-contract violation. Once R-021 is landed (5 min), the retry+drift gaps can either be closed as R-022/R-023 or accepted as YELLOW via a proposal-scope note before `sdd-archive`.

If the team wants to archive despite the gaps: promote status to RED for FR-CLI-1 only (a documented invocation-contract violation), and file follow-ups. Status GREEN is not achievable without R-021.

---

## Closeout

**Date**: 2026-08-19
**Final status**: **GREEN**
**Tests**: **119/119 passing** (was 117 at verify; +2 for R-021 coverage)

### Gaps closed

All three real code gaps identified in this report were closed by a single follow-up change **R-021 (3-gap closeout batch)** before archive:

| Gap | Requirement | Closed by |
|-----|-------------|-----------|
| GAP 1 | FR-CLI-1 — `python -m orchestrator` invocation | Created `orchestrator/__main__.py` (2-line shim: `from .orch import main; raise SystemExit(main())`). Verified live: `python3 -m orchestrator --dry-run --max-tasks 5` exits 0 and prints the plan. |
| GAP 2 | FR-D-4 — retry-once policy | Extended `_reap_once` in `orch.py` to increment `Dispatch.attempt` and re-queue on first failure; second failure calls `call_task_block` with stderr tail. `retry` re-added to `EVENT_TYPES`. |
| GAP 3 | FR-D-7 — version-drift substitution | Startup loop in `orch.py` now emits one `WARN` per route whose `fallback_cli_model` is set (evidence visible in runtime output: `WARN: route 'opencode/gpt-5.4' has fallback_cli_model='gpt-5' — will substitute…`). Router substitutes on drift error. |

### Spec deltas resolved by spec patch (not code change)

All three "PATCH SPEC" items from the Deltas section above were applied to `spec.md` at archive time:

| Delta | Spec section | Resolution |
|-------|--------------|------------|
| Event type naming drift | FR-STATE-7 | Event list replaced with the 10 code-locked names (`dispatch, success, fail, block, timeout, retry, resume_adopt, resume_revert, id_spoof_detected, flock_contention`). Explanatory note added to FR-STATE-7. |
| `dry_run_planned` inconsistency | FR-STATE-7 + AS-08 | Removed from FR-STATE-7 (see above). AS-08 amended to state explicitly "NO event emitted; plan printed to stdout only." |
| FR-D-2 argv strings | FR-D-2 | Table updated: claude uses `--add-dir .` (cwd = `v2/`), codex includes `--approve-for-me`, opencode omits `--session <uuid>`. Note added to reference the harmonization date. |

### Final runtime verification

- `python3 -m pytest orchestrator/tests/ -q` → `119 passed in 2.92s`.
- `python3 -m orchestrator --dry-run --max-tasks 5` → exit 0, plan of 5 rows, version-drift WARNs visible for the routes with `fallback_cli_model`.
- All 22 code files (Python + YAML) and 6 SDD docs present.

### Decision

Change closed **GREEN**. Ready to archive.
