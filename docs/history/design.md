# Design: Rupies v2 Task Orchestrator

Evidence: `orchestrator/proposal.md`, `sdd/orchestrator/explore.md`. Scope: HOW.

## 1. Directory Layout

```
v2/orchestrator/
  orch.py             # CLI entry: argparse, flock, main loop, signals
  task_queue.py       # DAG resolver (Kahn variant, dynamic)
  dispatcher.py       # Backend adapter registry + Popen wrappers
  prompt_builder.py   # Template renderer → state/prompts/<id>.txt
  state.py            # tasks.json reader, run-file writer, shell-out
  checkpoints.py      # Semi-mode predicate + stdin gate
  config.yaml         # concurrency, timeouts, budgets
  model_router.yaml   # tasks.json model → RouteEntry
  state/              # gitignored runtime artifacts
    .lock             # advisory flock
    run-<uuid>.json   # per-run operational state
    events-<uuid>.jsonl        # observability
    spend-<YYYY-MM-DD>.jsonl   # cost log (dashboard reads)
    prompts/<task>.txt         # rendered prompts (debug/replay)
    logs/<task>.log            # captured stdout/stderr per dispatch
  tests/{test_task_queue,test_dispatcher,test_prompt_builder,test_checkpoints}.py
  tests/fixtures/tiny_tasks.json  # 5-task DAG for integration
  pyproject.toml      # deps: pyyaml, pytest (dev)
  README.md
```

## 2. Core Data Structures

Python 3.11 dataclasses (frozen where safe). `state.py` owns them.

```python
Status = Literal["backlog","todo","in-progress","done","blocked"]

@dataclass(frozen=True)
class Task:
    id: str; phase: int; title: str; description: str
    model: str; reason: str; status: Status
    dependencies: list[str]; estimate_hours: float
    files: list[str]; spec_ref: str; comments: list[dict]

@dataclass(frozen=True)
class RouteEntry:
    backend: Literal["claude","codex","opencode"]
    cli_model: str
    tier: Literal["premium","standard","cheap"]
    is_premium: bool                     # cached tier=="premium"

@dataclass
class Dispatch:                          # in-flight, mutable
    task_id: str; backend: str; pid: int
    session_id: str                      # uuid4; passed to --session-id
    started_at: str                      # iso8601
    prompt_path: str; log_path: str; output_path: str
    attempt: int                         # 1 first try, 2 after retry

@dataclass
class RunState:
    run_id: str; started_at: str
    mode: Literal["auto","semi"]
    in_flight: dict[str, Dispatch]       # task_id → Dispatch
    completed: list[str]
    blocked:  list[str]                  # reasons in events file
    deferred: list[str]                  # semi-mode "N" answers
```

JSONL row schemas (append-only):

```json
// events-<uuid>.jsonl
{"ts":"2026-08-19T12:00:00Z","event":"dispatch|finish|block|timeout|retry|reconcile","task":"B-020","backend":"claude","pid":12345,"detail":"..."}
// spend-<YYYY-MM-DD>.jsonl
{"ts":"...","task_id":"B-020","backend":"claude","model":"opus","tokens_in":1234,"tokens_out":567,"cost_usd":0.0286,"duration_s":12.4}
```

## 3. Main Loop (orch.py)

```python
def main():
    args = parse_args()                 # --mode --dry-run --resume --only --limit
    cfg    = yaml.safe_load(open("orchestrator/config.yaml"))
    router = load_router("orchestrator/model_router.yaml")
    tasks  = state.load_tasks("tasks.json")
    validate_routes(tasks, router)      # fail-fast: every task.model resolves
    ensure_cwd_is_v2_root()             # detect via tasks.json + scripts/*
    lock = acquire_flock("orchestrator/state/.lock")   # exit 2 if held
    run   = state.new_or_resume(args.resume, args.mode)
    queue = TaskQueue(tasks, only=args.only, limit=args.limit)
    if args.dry_run: print_plan(queue, router); return 0
    gsem = Semaphore(cfg["concurrency"]["global_max"])
    psem = {b: Semaphore(cfg["concurrency"]["per_provider"][b])
            for b in ("claude","codex","opencode")}
    flocks: dict[str, Lock] = {}        # lazy per-file locks (33 tasks only)
    install_signal_handlers(run)        # SIGINT: drain; 2nd SIGINT: SIGKILL
    while queue.pending() or run.in_flight:
        reap_finished(run, queue)       # non-blocking waitpid loop
        for task in queue.ready():
            route = router[task.model]
            if args.mode == "semi" and checkpoints.needs_gate(task, route):
                if not checkpoints.handle(task, route, queue, run): continue
            if not try_acquire(gsem, psem[route.backend], flocks, task.files):
                continue
            dispatch(task, route, run, cfg)
        if not run.in_flight and not queue.ready(): break
        time.sleep(0.2)
    state.flush(run); lock.release()
    return len(run.blocked)              # exit code = blocked count
```

## 4. DAG Resolver (task_queue.py)

Kahn variant with dynamic `mark_done` / `mark_blocked` re-eval. DFS cycle detection at load (safety net — current data is a forest).

```python
class TaskQueue:
    def __init__(self, tasks, only=None, limit=None):
        self._by_id = {t.id: t for t in tasks}
        self._filter(only, limit)              # phase filter, task-count limit
        self._detect_cycles()                  # raise CycleError(ids)
        self._status = {t.id: t.status for t in self._by_id.values()}

    def ready(self) -> list[Task]:
        return [t for t in self._by_id.values()
                if self._status[t.id] == "todo"
                and all(self._status.get(d) == "done" for d in t.dependencies)]

    def mark_in_flight(self, id): self._status[id] = "in-progress"
    def mark_done(self, id):      self._status[id] = "done"
    def mark_blocked(self, id):   self._status[id] = "blocked"
    def pending(self) -> bool:
        return any(s in ("todo","in-progress") for s in self._status.values())
```

## 5. Backend Dispatcher (dispatcher.py)

`Backend` is a `Protocol`; three concrete adapters (`ClaudeBackend`, `CodexBackend`, `OpencodeBackend`).

```python
class Backend(Protocol):
    name: str
    def build_cmd(self, task, route, prompt_path, output_path,
                  session_id, cfg) -> list[str]: ...
    def parse_result(self, exit_code, stdout, stderr,
                     output_path) -> DispatchResult: ...
# DispatchResult: {ok, cost_usd, tokens_in/out, reason, task_finish_id|None}
```

Commands (rationale in `explore.md §1`):

```python
# claude: prompt via stdin (Popen(..., stdin=open(prompt_path)))
["claude","-p","--output-format","json","--session-id",session_id,
 "--model",route.cli_model,"--permission-mode","acceptEdits",
 "--add-dir","v2/","--max-budget-usd",str(cfg["budget"]["per_dispatch_usd"])]
# codex: prompt via stdin; final message JSON goes to -o file
["codex","exec","--skip-git-repo-check","--json","-o",output_path,
 "-C","v2/","-s","workspace-write","--approve-for-me","-m",route.cli_model]
# opencode: prompt via stdin
["opencode","run","--format","json","--auto","--session",session_id,
 "--model",route.cli_model]
```

Cost/token extraction:

| Backend  | Source |
|---|---|
| claude   | stdout JSON: `total_cost_usd`, `usage.{input,output}_tokens` |
| codex    | JSONL in `output_path`: sum `step_finish.cost`, `tokens.{input,output}` |
| opencode | JSONL stdout stream: same fields as codex |

Retry: one retry on non-zero exit OR `is_error=true`, same model, 5s backoff, `attempt=2`. Second failure → `task-block.sh` with stderr tail (first 500 chars).

<!-- ── Amendment 2026-08 (FR-D-8 escalation) ─────────────────────── -->

**Amendment 2026-08 — attempt-3 escalation state machine** (spec FR-D-8):

The reap loop (`orch.py::_reap_once`) implements the retry gate as a decision
tree over `(dispatch.attempt, failure_class, route.escalation_model,
cumulative_task_cost, cfg.budget.per_dispatch_usd)`:

```
on failure:
  classify -> failure_class
  if failure_class in {TIMEOUT, PERMISSION, BUDGET, ID_SPOOF}:
      block                                                  # terminal, no retry
  elif attempt == 1:
      # FR-D-4: one retry, same route (or fallback if VERSION_DRIFT)
      route' = fallback_swap(route) if VERSION_DRIFT else route
      retry(attempt=2, route=route', backoff=class-specific)
  elif attempt == 2 and route.escalation_model is set
       and failure_class != VERSION_DRIFT                    # drift already had fallback
       and cumulative_task_cost < budget.per_dispatch_usd:
      # FR-D-8: escalate to a DIFFERENT route
      route' = router[route.escalation_model]
      emit escalate {from_route, to_route, failure_class, attempt=3}
      retry(attempt=3, route=route', backoff=0)
  else:
      block                                                  # attempts exhausted or ineligible
```

Escalation is per-route metadata (`RouteEntry.escalation_model: str | None`),
not per-task — `tasks.json` schema is untouched. Escalation targets are
validated at startup by `router.load_router`: any non-null value that isn't
also a route key raises `RouterFormatError` (fail-fast, no dispatch).
The `escalate` event is a new locked event type (see `state.EVENT_TYPES`).
Per-task cumulative cost is tracked in a `task_costs: dict[str, float]`
threaded through the reap loop from `main()`.

## 6. Prompt Builder (prompt_builder.py)

Input: `Task`, list of completed dep `Task`s, spec ref path. Output: `state/prompts/<task_id>.txt` (utf-8, overwritten per run). Template body is quoted verbatim from `explore.md §3`.

Graceful handling: `dependencies == []` omits the "Completed dependencies" block; `files == []` renders `Files you may write: [] (no strict-file guard)`; missing/404 spec ref renders `Spec ref (READ FIRST): <path> (NOT FOUND — proceed with description only)` and WARN; each dep last-comment truncated to 500 chars.

```python
def build(task: Task, deps: list[Task], v2_root: Path, out_dir: Path) -> Path: ...
```

## 7. State + Resumability (state.py)

Reads `tasks.json` (never writes it). Writes `run-<uuid>.json` on every transition (atomic: tmp + rename). Shells out to `scripts/task-*.sh` via `subprocess.run(check=True, capture_output=True, cwd=v2_root)`. `author` = `f"{backend}/{cli_model}"` (matches tasks.json convention).

`--resume` reconciliation, per entry in run-file's `in_flight`:

```
try: os.kill(pid, 0)                     # alive check
except ProcessLookupError:               # dead
    if task.files and git_diff_touches(task.files):
        state.finish(id, "recovered: files modified, agent did not report")
        events.append("reconcile", id, "adopted-done")
    else:                                # can't tell → safe default
        jq_inline_set_status(id, "todo") # rewrite tasks.json in place
        events.append("reconcile", id, "reverted-todo")
else:                                    # alive → keep in run.in_flight
    events.append("reconcile", id, "adopted-alive", pid=pid)
```

For `files == []` (301 of 334 tasks) the git-diff heuristic is unreliable → revert to `todo` and WARN.

## 8. Semi-mode Gate (checkpoints.py)

```python
def needs_gate(task, route) -> bool:
    return (route.is_premium
        or _touches(task, "supabase/migrations/")
        or _touches(task, "supabase/functions/")
        or task.estimate_hours >= 10)      # phase10+premium already covered by is_premium

def ask(task, route) -> Literal["y","N","s","q"]:
    print(f"\n=== SEMI GATE — {task.id} [{route.tier}] ===")
    print(f"  {task.title}")
    print(f"  files: {task.files or '[]'}  est: {task.estimate_hours}h")
    print(f"  → dispatch to {route.backend}/{route.cli_model}?")
    while True:
        a = (input("[y]dispatch / [N]defer / [s]skip-permanently / [q]uit > ")
             .strip().lower() or "n")
        if a in ("y","n","s","q"): return a
```

Answer handling: `y` → dispatch; `N` → `queue.defer(id)` (end of run); `s` → `state.block(id,"operator skipped","operator")` + `queue.mark_blocked`; `q` → set `draining=True`, stop new dispatches, wait for in-flight, exit.

## 9. Concurrency Implementation

- Main loop is single-threaded Python; real parallelism = OS subprocesses.
- `threading.Semaphore` used only as counting primitive (no scheduling threads).
- `gsem` + `psem[backend]` acquired non-blocking (`acquire(blocking=False)`); failure → task waits until next tick.
- `flocks: dict[path, Lock]` created lazily for the 33 tasks that declare files; released in `reap_finished` when the Dispatch finalizes.
- Reap: `os.waitpid(-1, os.WNOHANG)` in a loop; correlate PID → Dispatch via `run.in_flight`.

## 10. Spend Log Format

Append-only JSONL at `orchestrator/state/spend-<YYYY-MM-DD>.jsonl` (UTC date), one line per completed dispatch (success or failure-with-cost). Schema in §2. Writer `state.append_spend(entry)` opens `"a"`, writes `json.dumps(entry) + "\n"`, flushes. No lock needed — POSIX guarantees atomic writes below `PIPE_BUF` and lines are ~200 B.

## 11. Dashboard Budget Panel

New `<section id="budget-panel">` in `dashboard/index.html` (after header). Two cards: (1) today by provider — horizontal bars claude/codex/opencode with cumulative USD; (2) recent 20 dispatches — table (ts, task, backend, model, cost, duration).

Added to existing `<script>` block, reuses the 5s poll cadence, no new deps:

```js
const SPEND_URL = () => `../orchestrator/state/spend-${todayUTC()}.jsonl`;
async function fetchSpend() {
  try {
    const r = await fetch(`${SPEND_URL()}?t=${Date.now()}`, {cache:'no-store'});
    if (!r.ok) return hidePanel();
    const rows = (await r.text()).trim().split('\n')
      .map(l => { try { return JSON.parse(l); } catch { return null; }})
      .filter(Boolean);
    renderBudget(rows);
  } catch { hidePanel(); }
}
setInterval(fetchSpend, 5000);
```

`scripts/serve.sh` already serves `v2/` root → spend files reachable via the relative path. No server changes.

## 12. Testing Strategy

| Layer | Module | Approach |
|---|---|---|
| Unit | `task_queue`: ready-set, cycle detection, mark_done re-eval | pytest w/ in-memory tasks |
| Unit | `prompt_builder`: golden-file per variant (empty deps, missing spec, files=[]) | tmp_path bytes compare |
| Unit | `dispatcher`: cost extraction | canned claude/codex/opencode outputs in `fixtures/backend-outputs/` |
| Unit | `checkpoints`: predicate matrix | parametrize |
| Integration | `orch` end-to-end on `fixtures/tiny_tasks.json` (5 tasks) w/ `FakeBackend` | assert final tasks.json + spend jsonl |
| Manual | `--dry-run` on real 334-task json → zero unresolved routes | shell smoke |

`FakeBackend` subclasses `Backend`, returns canned `DispatchResult`, spawns a `python -c 'import time;time.sleep(x)'` child so the waitpid path stays exercised.

## 13. Error Handling Matrix (module → exception)

| Failure (explore §5) | Module | Exception / handling |
|---|---|---|
| CLI timeout | `orch.reap_finished` | `TimeoutExpired`; SIGTERM → 10s → SIGKILL; `state.block(id,"orchestrator timeout")` |
| Non-zero exit / `is_error=true` | `dispatcher.parse_result` | `DispatchFailed`; retry once, else `state.block` |
| Agent ended without reporting | `orch.reap_finished` | tasks.json status still `in-progress` → `state.block(id,"agent ended without reporting")` |
| Unauthorized file edits | `orch.reap_finished` post-check | `git status --porcelain` diff outside `task.files` → WARN; if `cfg.strict_files` → `git checkout --` + `state.block` |
| Race: two agents same file | `orch` dispatch gate | `file_lock.acquire(blocking=False)` → False → skip tick |
| Provider auth expired | `dispatcher.parse_result` | `AuthError`; drain in-flight, exit with actionable msg |
| Budget cap hit | `dispatcher.parse_result` | `BudgetExhausted` on `terminal_reason:"budget_exhausted"`; `state.block` |
| Missing router entry | `orch.validate_routes` | `RouteError`; fail-fast pre-flock |
| Cycle in DAG | `TaskQueue._detect_cycles` | `CycleError(ids)` at load |
| Concurrent orchestrator | `orch.acquire_flock` | `LockedError`; exit 2 |
| Wrong task-finish id (injection) | `dispatcher.parse_result` grep event stream | Id mismatch → `state.block(id,"agent reported wrong task id")` |

## Open Items (flag for user review)

- **OPEN** — All three CLIs get the prompt via stdin (`Popen(..., stdin=open(prompt_path))`). Explore documented positional prompts; stdin is equivalent and avoids shell-escaping the 1-2 KB prompt body. Verify at R-002 (scaffold).
- **NEW RISK not in explore/proposal** — `git checkout -- <paths>` inside strict-files revert can DESTROY concurrent work if another dispatch's file leaked in. Mitigation: strict-files revert is OFF by default; enable only for phase-10 tasks via `cfg.strict_files_phases: [10]`.

## Success Criteria (design-level)

Every module in the proposal has a concrete internal contract; data structures, main-loop pseudocode, and JSONL schemas are present; file paths are concrete.
