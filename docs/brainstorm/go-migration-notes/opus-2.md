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
