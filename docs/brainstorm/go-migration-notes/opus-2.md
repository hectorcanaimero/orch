# Go migration notes — lane opus-2

Append-only. One entry per finding, newest last. Format and numbering follow `docs/brainstorm/go-migration-notes.md`; that file is now history and is not edited after 2026-09-12.

## G3.2 — the reaper

- **Bug 15 — the retry backoff is computed and then bypassed.** Found porting
  the reap loop. `retry.backoff_seconds` (5s) and
  `retry.rate_limit_backoff_seconds` (60s) never delay anything.

  The retry branch stamps a `_RetryItem` with
  `retry_earliest_at = monotonic() + backoff` and resets the task to `todo` so
  the queue will consider it again. `_refill` then drains the retry queue
  first — correctly skipping an item whose time has not come — and falls
  through to the ready-set pass, which has no notion of the retry queue at
  all. `queue.ready()` returns the task, because it is `todo` and its deps are
  done, and `_spawn_one` dispatches it. Same tick. The backoff it just
  computed is never waited out.

  Verified by running it, not by reading: `_refill`'s ready-set pass mentions
  `retry_queue` nowhere, and a `TaskQueue` whose status was reset the way the
  retry branch resets it answers `ready()` with that task immediately.

  Two consequences, the second worse than the first:
  1. a rate-limited provider is hammered again at once instead of waiting for
     its window — which is the entire reason `rate_limit_backoff_seconds`
     exists;
  2. the `_RetryItem` stays in the queue, so when its backoff finally does
     expire the same task is dispatched a **second** time, potentially while
     the first retry is still in flight.

  Why it was never caught: `test_rate_limit_failure_uses_long_backoff` asserts
  the `retry_earliest_at` stamp on the item and never runs `_refill`, so it
  passes while the behaviour is broken. A test of the value, not of the
  effect.

  **Fixed in Go** rather than ported, because porting it would mean shipping a
  backoff that does nothing: `ReadyOpts.Waiting` excludes whatever the retry
  queue owns, so the queue holds its task until the backoff expires and
  releases it exactly once. `TestRetryQueueOwnsItsTaskUntilTheBackoffExpires`
  pins both halves. The Python fix is the same idea and belongs in its own PR.

- **Bug 14 — the sub-agent-finished guard died when SQLite became the
  default.** The reap loop's terminal branch checks
  `_task_status_in_file(task_id, cwd / "tasks.json") == "done"` before
  blocking a task, to respect a sub-agent that already called
  `task-finish.sh` while the CLI wrapper reported a spurious failure (a
  skills-shortening warning, a `step_finish` buffering race — the comment
  names those cases).

  `task-finish.sh` shells into `orch task-status <id> done`, which writes
  through the active state backend. Under `file` that reaches tasks.json;
  under `sqlite` — the default since v0.11 — it writes the database and leaves
  tasks.json untouched. So the guard reads a file nothing updates and is
  always false on every project scaffolded since.

  Reproduced: bootstrap a sqlite project, run the transitions `task-start.sh`
  and `task-finish.sh` perform, then read both. The backend says `done`;
  tasks.json still says `todo`.

  The Go port reads the status back **from the backend**, which is the port of
  the intent rather than of the dead line. That makes Go do something Python
  currently does not, so it needs the same Python-first fix the classify
  markers got if the two are to stay in step.

- **Per-class retry rules exist in `internal/config` and nowhere in Python.**
  `config.Retry` carries `transient` / `timeout` / `rate_limit` /
  `version_drift` / `ci_failed` blocks with pointer fields, and `load.go`
  registers their key paths as known. Python reads only the three scalars, and
  no shipped `config.yaml` carries a per-class block.

  The reaper uses the blocks through `RetryRule.Attempts(fallback)` and
  `RetryRule.Backoff(fallback)`, whose fallbacks are exactly those scalars. So
  for every existing project — none of which has a block — behaviour is
  identical to Python, and a project that opts in gets something Python has no
  way to express. Not the `per_file` situation (bug 13): there the key
  promises a behaviour and nothing implements it; here the structure is
  implemented and simply has no Python counterpart.

## G3.3 — the run loop

- **Bug 17 — the main loop spins forever on a task it can never dispatch.**
  `orch.py`'s terminate condition is "nothing in flight, nothing ready, no
  retries pending". A task whose model has no route keeps the ready set
  non-empty forever: `_refill` logs `route missing at dispatch time` and
  `continue`s, the ready set is unchanged, and the loop ticks on at 200 ms a
  pass. Forever, with one log line per pass and no exit.

  The same happens for any dispatch that can never succeed rather than merely
  not-yet: in the Go tree, a backend with no adapter or a backend with no
  configured concurrency cap. Those are mid-migration states, but the route
  case is reachable in Python today — delete an entry from `model_router.yaml`
  that a task still references, and `orch run` hangs.

  Found when a `orch run` testscript did not terminate. A hung orchestrator
  that prints nothing is worse than one that errors, so the Go loop stops and
  says which tasks and why: `permanentlyStuck` distinguishes "waiting" —
  capacity, a budget window, a retry backoff, all of which resolve — from
  "never", and only the second ends the run. `TestLoopStopsWhenNothingCanEverBeDispatched`
  covers the three permanent cases, and
  `TestLoopKeepsGoingWhileSomethingCanStillRun` pins the other half: one
  unroutable task must not stop a run that still has work it can do.

- **A spawn failure reached the queue but not the database.** Not a Python
  bug — mine, caught by the same testscript. `spawnOne` marked the task
  blocked in the in-memory queue and returned, so a run that had given up on
  a task left the database still calling it `todo`. Python calls
  `call_task_block` as well as `queue.mark_blocked`, for the good reason that
  the two are read by different people: the queue decides what this run does
  next, the row is what `orch status` shows afterwards. `blockAtDispatch`
  now does both, and the prompt-render failure path goes through it too.

- **`orch run` renders its own prompts.** Worth recording because the first
  version of the CLI test supplied a prompt file by hand and passed for the
  wrong reason. `_spawn_one` calls `render_prompt` and writes the file just
  before forking, because the provider pipes it to the child's stdin — a
  dispatch without one is a CLI with no instructions. The Go scheduler calls
  `prompt.Write` at the same point, and a render failure blocks the task
  rather than spawning an agent that would be told nothing.

- **Observation, not a divergence: a task stranded `in-progress` makes
  `orch run` exit 0 with nothing to say.** Seen running the real loop against
  `testdata/parity-project`, which ships one `in-progress` task and one
  `blocked` one. The run exited 0 having dispatched nothing, because
  `ready()` returns only `todo` tasks and the orphan sweep only reverts rows
  that have a `dispatches` entry — and a task left `in-progress` by a run
  whose record is gone has neither. Python behaves identically: same `ready()`
  filter, same sweep condition, and `orch reset --requeue` is the tool that
  exists for it.

  So there is nothing to fix for parity, and it is recorded because the
  surface reads wrong: a run that exits 0 having done nothing looks like "all
  work finished" when it can also mean "one task is stuck half-done and
  nobody is going to notice". If `orch run` ever grows a closing summary, the
  count of tasks left `in-progress` belongs in it.
