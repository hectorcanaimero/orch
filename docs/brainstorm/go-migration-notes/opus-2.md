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

- **Bug 20 — the main loop spins forever on a task it can never dispatch.**
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

## The CI poller

- **Nothing new about Python here, and that is worth saying.** `_check_ci_once`
  ported cleanly: the poll interval, the both-conditions filter (a PR **and**
  an unresolved CI status), the attempts-before-increment comparison, the
  re-dispatch through the ordinary retry queue, the `.orch-ci-feedback.md`
  file in the recreated worktree, and the pair of `pr_auto_merged` /
  `pr_auto_merge_failed` events. Every piece had a reason that survived
  reading, which after four rounds of finding the opposite is a result in
  itself.

  Three details that would be easy to get wrong and are pinned by their own
  tests, because each is invisible in normal operation:

  1. **The retry event counts the attempt just started**, not the stored
     counter: Python emits `ci_attempts + 1`. An off-by-one only shows with
     `ci_max_retries > 1`, which no default config has.
  2. **The counter goes up after the re-dispatch is queued**, not before. If
     the process dies between the two, the task is queued with the count
     unchanged and the next run tries again — biased towards retrying once
     too often rather than giving up too early, which for CI is the right
     side of the trade.
  3. **A row with an empty `pr_url` is skipped before the provider is asked.**
     Asking `gh` about an empty URL is an error every tick, forever, and the
     filter that should prevent it lives in SQL where this code cannot see
     it.

## G3.3 (b) — turning worktree mode, auto-PR and CI on

Three things found by **running** the chain end to end against a real bare git
remote and a fake `gh`, not by reading it. Two are mine; the third is a
question for Python.

- **The database never learned a task was in-progress.** Mine, from G3.1.
  `spawnOne` marked the in-memory queue and nothing else, so a running agent
  showed as `todo` to `orch status`, to the dashboard, and to any other orch
  sharing the project. Python does this by shelling `scripts/task-start.sh`,
  which shells straight back into `orch task-status <id> in-progress`; the Go
  port goes to the backend directly, which is the same write without the round
  trip and is the source of truth since F-12. Caught because the first real
  run ended with the task reading `todo` while its PR was open.

- **The CI poller marked the queue done but not the database.** Also mine,
  from the poller PR. `ciSucceeded` wrote `ci_status = success` and marked the
  queue, so the run behaved correctly and `orch status` showed the task
  in-progress forever afterwards. Python's queue mirrors every mark into the
  backend (F-8's `_persist`); the Go queue deliberately does not, and the
  scheduler owns that write — which means every place that finishes a task has
  to do it, and this one did not. Both now go through a transition.

  The pair is worth stating as one lesson: **splitting "the run's view" from
  "the stored truth" is right, and it makes every new terminal path a place to
  forget the second half.** Both were invisible to the unit tests, which
  assert on the queue, and obvious the moment a real run's output was read
  back with `orch tasks --json`.

- **A run that opens a PR exits before CI is ever polled.** Not fixed, and a
  question rather than a bug report, because Python does the same and it may
  be deliberate. The terminate condition is "nothing in flight, nothing ready,
  no retries pending": a task waiting on CI is none of those, so the loop ends
  the moment the last PR is opened. The task stays `in-progress` with
  `ci_status = pending`, and the poll happens on the *next* `orch run` — one
  poll per invocation, so a green CI needs a second run to be noticed and a
  red one needs a third to be retried.

  Verified: first run opens the PR and exits 0 with zero `gh pr checks` calls;
  a second run polls once and finishes the task. That is what
  `run-worktree-pr.txtar` asserts, because it is what both binaries do.

  The alternative — keeping the run alive while any task waits on CI — needs a
  timeout, or `orch run` hangs whenever CI never reports. That is a product
  decision, not a port decision, so it is recorded rather than taken.

- **Unverified assumption shared by both binaries**: `internal/vcs` and
  `orchestrator/vcs/github.py` both compare `gh pr checks --json
  state,conclusion` output against **lowercase** keys (`success`, `failure`,
  `in_progress`). If a real `gh` emits upper-case values, both read every
  check as pending and no CI ever resolves. Not verifiable here — `gh` is
  installed on this machine but not authenticated, so its real output cannot
  be captured. Flagged rather than guessed: inventing the casing is exactly
  the fixture that passes while the real CLI breaks.

## G6.7 — internal/notify

- **Nothing wrong with `notifications.py` either.** Second clean port in a row,
  which is worth recording as carefully as the bugs: best-effort sends, an
  empty URL disabling a channel, a notifier with no channels being a working
  no-op, and the reason cut to its first line all had reasons that survived
  reading.

- **What capturing the payloads settled that reading would not have.** The
  fixtures were recorded off the wire — a local `http.server` standing in for
  Slack and Discord while Python's `Notifier` posted to it — and two details
  came out of it:

  1. `notify_blocked` sends only the reason's **first line**. A multi-line
     block reason loses everything after the newline, which is easy to port as
     "the whole reason, truncated" and then never notice.
  2. An absent reason renders as the literal `unknown`, not as an empty tail.

  A third is a difference rather than a finding, and is why the test compares
  the decoded message rather than the raw body: Python's `json.dumps` defaults
  put a space after the colon and escape non-ASCII (`\u2014` where Go writes a
  literal em dash). Both are the same JSON to both services, so pinning the
  bytes would pin `json.dumps`'s defaults rather than anything a human sees.
  The message text is the contract and is compared byte for byte; the envelope
  is not.

- **Nearly wrote the documentation form of bug 4.** `docs/CONFIG.md` already
  had a `### notifications` section, and the first draft of this PR appended a
  second, top-level one. Two sections describing one config block is the same
  failure as two `dashboard:` keys in one YAML file: both are present, one wins
  the reader's attention, and the other rots. Caught by grepping the file
  before committing rather than after. The lesson generalises past YAML —
  **before adding a section, check whether the thing already has one.**

## G6.4 — the MCP server

- **Bug 24 — the dispatch prompt's "Completed dependencies (context)" block is
  always empty.** Found building `orch_context`, which answers the same
  question the prompt's dep block does, so the first version reused
  `prompt.LastComment` — and it returned `""` for a dependency that had just
  reported a summary through the MCP tool.

  `prompt_builder._read_dep_last_comment` reads the entry's **`text`** key:

  ```python
  text = str(last.get("text", "")) if isinstance(last, dict) else str(last)
  ```

  Nothing in `orchestrator/` writes a `text` key into a task comment. Every
  writer is in `state/sqlite_backend.py` and every one of its three call sites
  writes `{"author", "body", "at"}`:

  ```python
  comments.append({"author": author, "body": note or status, "at": ts})
  ```

  `rg '"text"' orchestrator/ --glob '*.py'` finds the key in exactly three
  places: `prompt_builder.py`'s reader, Slack's webhook payload, and
  `test_prompt_builder.py`'s own fixtures. So the block renders the id and the
  title with an empty comment for every dependency, on every dispatch. An
  agent is told which tasks came before it and never what they concluded —
  which is the entire reason the block exists (FR-P-3).

  **The test is complicit, again.** `test_prompt_builder.py` builds its deps
  with `comments=[{"author": "agent", "ts": "2026-01-01", "text": text}]` —
  a shape no writer produces, including the `ts`/`at` mismatch. Three tests
  (truncation at 500 chars, "most recent entry wins", the rendered block) all
  pass against a comment shape that never reaches the renderer in production.
  Checklist rule 21's lesson outside `internal/providers`: the fixture was
  written from the reader's docstring instead of captured from the writer.

  **There is a second layer, and it survives fixing the key.** Since F-12 the
  comments live in `tasks_runtime.comments_json`, and the prompt's deps come
  from `tasks.json` — where `project.Hydrate` overlays `Status` and nothing
  else. So even with `body` spelled correctly, `dep.Comments` is whatever the
  file held when it was last written, which for a project orch has been
  running is an empty list. Both halves have to move for the block to carry
  anything.

  **Not fixed here.** `internal/prompt` is a byte-for-byte port with goldens
  rendered by Python (G2.3); changing what the block renders changes those
  goldens, and it is G6.6's own subject matter — the prompt is being rewritten
  there to offer MCP first. `internal/mcp` reads `body` from the runtime row
  instead, with the reasoning at `lastComment` in `readtools.go`, because a
  tool whose whole purpose is telling an agent what its dependencies reported
  cannot ship the empty string while waiting.

  Found the way the last four were: by running the thing and reading one line
  of its output.

  **Renumbered from 21 to 24** after orch-98 reconciled the lanes: 21 is opus's
  velocity-by-`updated_at`, 22 the stakeholder spend, 23 sonnet's raw
  `blocked_reasons`.

- **An illegal transition is a tool error, not a protocol error, and the
  difference is the whole feature.** The SDK makes both easy: a handler that
  returns a non-nil `error` becomes `IsError` with the error text as content,
  which is the protocol-level shape. That loses the structured payload — and
  the payload is the point, because the caller is a model that can retry.
  `orch_set_status` returns `(result{IsError: true}, out, nil)` instead, so
  `structuredContent` still carries `{code, message, from, valid_transitions}`
  alongside the human-readable text. A refusal an agent can act on:

  ```
  F0.T1 cannot move from done to in-progress (valid from here: todo, done)
  ```

  `state.Transition`'s own error already names the legal destinations, but as
  prose inside an error string. The list of fields is the same facts without
  the English.

- **`invalid_status` deliberately leaves `valid_transitions` empty.** The first
  version filled it with all five statuses, which is wrong in the way that
  matters: the field means "legal from `from`", and nothing was looked up yet,
  so there is no `from`. A list that means two different things depending on
  the sibling `code` is how a client ends up offering `done` from `backlog`.
  `ParseStatus`'s own message enumerates the five; the structured field stays
  relative to a task or stays absent.

- **A long-lived server goes stale in a way a per-invocation CLI cannot.**
  `orch task-status` calls `Bootstrap` every time it runs, so a task added to
  tasks.json a minute ago is writable. `orch mcp` runs for the length of an
  agent session: bootstrapping only at startup would make that task
  permanently "unknown task" to the agent dispatched for it. `currentStatus`
  seeds on the miss path only — one read in the common case, and `Bootstrap`
  never overwrites a runtime status.

- **`.mcp.json` is soft even under `--force`, unlike `AGENTS.md`.** Both are
  hand-edited, but `AGENTS.md` is orch's file and `.mcp.json` is shared: a
  project that already has one is very likely listing other servers in it, and
  replacing it disconnects them silently. `orch doctor`'s `mcp.config` check is
  how an older project finds out it has none — its remediation points at
  `docs/MCP.md` rather than at `orch init --force`, which would need to get
  past the conflict gate and would overwrite `tasks.json` on the way.

- **`scripts/parity.sh` needed a fourth exclusion and it is a real one.**
  Python's `run_init_cli` has no MCP server to point at, so `.mcp.json` is Go
  only — verified by removing the `-x` and watching both tree checks fail with
  `Only in …/workflow-go/parity-workflow: .mcp.json`. An intended divergence
  documented in `docs/CLI.md`'s `init` row, not a normalisation hiding a
  difference: the file's content is pinned by `internal/scaffold`'s own tests.

- **The SDK is the official one and its floor matches ours.**
  `gh api repos/modelcontextprotocol/go-sdk` reports
  `"The official Go SDK for Model Context Protocol servers and clients.
  Maintained in collaboration with Google."`, not a fork, not archived. v1.7.0
  declares `go 1.25.0` — the same floor `modernc.org/sqlite` already forces, so
  it costs nothing. It pulls four new indirect modules
  (`google/jsonschema-go`, `segmentio/encoding`, `yosida95/uritemplate`,
  `golang.org/x/oauth2`); none is a runtime endpoint, and the stdio transport
  opens no sockets.

- **Tested through a real session, not by calling the handlers.** The handlers
  are the easy part. What had to be proved is that the seven are reachable *as
  tools*: schemas inferable, arguments surviving JSON-RPC, a refusal arriving
  with its payload intact. `mcp.NewInMemoryTransports()` gives a real client
  and a real server in one process, and the test decodes
  `structuredContent` off the wire rather than reading the Go value the
  handler returned — which is how the `[]json.RawMessage` comment field was
  caught inferring a schema of `string`.

  Then the binary itself, over a pipe, driven by a hand-written JSON-RPC
  client with no SDK on the other side: `initialize`, `tools/list`, the
  `start → finish` cycle, the refusal, and `orch_context` on the task whose
  dependency had just finished. Closing stdin exits 0 with an empty stderr.
  Rule 29 is not satisfied by the in-process test — it was the printed
  transcript that showed `last_comment` empty.


## G6.6 — the prompt offers MCP first

- **Bug 24, both halves, fixed on the Go side.** The key (`text` → `body`) was
  the half that reads like the whole bug. It is not.

  1. **The key.** `prompt_builder._read_dep_last_comment` reads `text`. Every
     writer writes `body`. Changing the key alone would still have rendered
     nothing, because —
  2. **The source.** Since F-12 the trail lives in
     `tasks_runtime.comments_json`, and the prompt's dependencies come from the
     queue, which is built from tasks.json, whose `comments` array is whatever
     was in the file. `project.Hydrate` overlays `Status` and nothing else. So
     the renderer was reading the right field off the wrong object.
  3. **And a third, which only showed up by running it.** Even with the key and
     the source fixed, `comments[-1]` is the wrong entry. A normally-finished
     task's trail, dumped from a real run:

     ```
     orch                       dispatched to claude/claude-sonnet-4-6
     orch                       dispatch succeeded
     claude/claude-sonnet-4-6   created pyproject.toml and app/main.py …
     ```

     The agent reports from inside its run; the reaper transitions the task to
     done *afterwards*, with its own note (`dispatch succeeded`, or `CI passed`
     from the poller, or the bare status name for a hand-made `orch task set`).
     So the last entry is always orch's bookkeeping. `AgentComment` scans back
     for the first entry whose `author` is not `orch` — the field is already
     there, already meaningful, and already distinguishes the two writers.

  The engine reads the trail at **dispatch time** (`Scheduler.Comments`, a
  one-method `CommentReader`), not from a snapshot taken when the queue was
  built: the dependency most often finishes during the same run, minutes
  before, so a startup snapshot would be empty for exactly the summary that
  matters most.

  Verified end to end against the built binary, not just in tests: scaffolded a
  `python-api` project, dispatched F0.T1 with `ORCH_FAKE_PROVIDER`, wrote an
  agent-authored note, appended an engine note *after* it to reproduce the real
  order, then dispatched F1.T1 and read its prompt file:

  ```
  Completed dependencies (context):
    - F0.T1: created pyproject.toml and app/main.py with the FastAPI app
  ```

- **One prompt, not a variant switch.** G2.3's doc comment predicted a
  scripts/MCP variant pair chosen by something. There is nothing to choose it
  with: `.mcp.json` existing on disk does not mean the agent CLI loaded it, a
  project scaffolded before G6.4 has none, and orch never sees the agent's tool
  list. So the protocol block names both, MCP first, with an explicit "if you
  do not have those tools". Being wrong costs the agent one tool-not-found
  error it recovers from by reading the next line; guessing wrong costs a task
  that cannot report at all.

- **`orch_set_status`'s author default had to change, and finding out why took
  a failing test.** It was `orch`, "matching `orch task-status`". That default
  is right for a command a human also runs. For a tool only agents call it is a
  trap: `AgentComment` skips `orch`-authored entries, so an agent that omitted
  `author` would write a summary nothing downstream ever shows — silently, and
  visible only two tasks later as a `(no comment)` in someone else's prompt.
  Now `agent`, with a test asserting it is never `prompt.EngineAuthor`, because
  the two constants live in different packages and that is how a rename drifts
  them apart.

- **The goldens split rather than moved.** Everything from `TASK_ID=` down to
  `Spec ref (READ FIRST):` is unchanged and is still compared byte for byte
  against Python's goldens; the two blocks below it have Go goldens of their
  own. The split is on a line the template itself emits, not a guessed offset.

  The Python files earn their keep twice over now: they are the head's
  reference, and they are the **evidence** for bug 24 —
  `TestPythonRendersEveryDependencyAsNoComment` asserts that every dependency
  line in every Python golden reads `(no comment)`, including the one whose
  note sits in `cases.json` three lines away. That test fails if anyone
  regenerates them from a fixture shape that humours the reader again, which is
  exactly how the bug survived three passing tests in `test_prompt_builder.py`.

- **The fixture was the bug's accomplice, and it was in the Go tree too.**
  `make-goldens.py`'s `dep()` helper wrote `{"author", "ts", "text"}` — copied
  from the reader's docstring, not captured from a writer. Rule 21's lesson is
  filed under `internal/providers`, but it is not about providers: *a fixture
  written from the consumer's own description of its input tests the consumer
  against itself.* The new helper's docstring pastes the `sqlite3` query it
  came from.

- **Not touched, and it has the same cause.** `internal/dashboard/view.go`
  ships `comments` (and `has_comments`) straight off `model.Task`, i.e. off
  tasks.json — so the dashboard reports an empty comment array for every task
  in any project orch has actually run. Same root as bug 24's second half.
  It belongs to whoever owns `internal/project`/`internal/dashboard`: either
  `Hydrate` overlays `Comments` too, or `view.go` reads the runtime row.
  Raised with opus rather than fixed across a lane boundary mid-PR.

## G6.7 — `orch notify`, and the event a finished run writes

- **Two ETAs with similar names and different meanings, ten lines apart in
  `metrics.py`.** Flagged by opus while reviewing the digest's wording, and
  worth writing down before someone "unifies" them:

  - `sprint_eta` divides remaining **tasks** by tasks-per-day and yields a
    **date**. `/api/sprint` and each milestone's row report this one.
  - `eta_hours_remaining` sums remaining **estimated hours** and scales them
    by how far the finished tasks ran over their estimates. The executive
    summary reports this one.

  On the same project they can disagree substantially. They are answers to
  different questions, so a digest that says "~12.5h remaining" while
  `/api/sprint` says a date is not an inconsistency to fix.

  Python's `orch notify digest` called `executive_summary` with the **date**;
  Go's summary (`internal/publish/snapshot`) is trimmed to the hours branch,
  the only one the stakeholder view uses. The digest inherits hours. Kept as a
  deliberate divergence rather than restored: one wording per number beats
  literal parity, and hours are the more honest claim — a date implies a
  precision the calculation does not have.

- **Reusing the stakeholder summary brings its rounding with it.** The same
  function rounds spend UP to the nearest $0.50 (`round_up_to_step`), so a
  stakeholder page can never under-report. Python's digest printed the exact
  `round(sum, 2)`. The digest now over-reports the operator's own cost by
  under fifty cents — accepted, because the alternative is a second wording of
  the same figure, which is the thing the command was asked to avoid. Worth
  knowing it is a *consequence* of sharing, not a decision taken on its own.

  The spend condition is the rows, not the sum: Python's
  `total_spend = round(sum(...), 2) if spend else None` tests a non-empty
  dict, so a day whose dispatches all cost zero prints `$0.00` and a day with
  no dispatches prints no spend clause at all. A free run and a run that did
  not happen are different facts, and the port keeps them different.

- **`MilestoneETADate` is a thin export, not a new package.** `milestoneETA`
  lives in `internal/dashboard` because it is the arithmetic `/api/milestones`
  computes; `internal/cli` already imports that package for `orch dashboard`.
  A package holding one function would be a worse home than the consumer it
  already has. Agreed with opus: a third caller is when moving it starts to
  pay, and by then there will be three call sites to say where it goes.

- **`sprint_done` (F4.7) is the first Go-only event type, and the direction is
  what makes it safe.** `internal/state`'s type set is pinned byte for byte
  against the one Python declares, generated by `testdata/make-event-types.py`
  — that comparison exists because bug 9 had `orch.py` emitting seven types
  its own validator refused, and the CI poller died silently for months.

  Adding `sprint_done` needed a carve-out, so the carve-out had to be checked
  rather than assumed: Python's validator refuses an unknown type only on
  **write**, and `iter_events` filters on project, run, task and id — never on
  the type. Read the WHERE clause to confirm it. So a row Go writes reads back
  in a Python `orch events` intact, and Python never writes one for Go to
  reject. `goOnlyEventTypes` names it with that reasoning next to it, and the
  test fails if Python ever declares it (the carve-out must come down) or if
  Go stops accepting it.

  Same shape as `TestGoTreeMatchesPython`'s `goOnly` allowlist for the
  `expo-mobile` template: a deliberate Go-only addition, named, with its
  reason — not drift discovered later.

- **"The queue emptied" is not "everything is done", and picking one meaning
  would be wrong half the time.** A run whose remaining tasks are all blocked
  reaches the same exit. Rather than suppress the event there (leaving a cron
  job unable to tell "finished" from "still going") or emit a bare "done"
  (claiming work that did not happen), the counts ride in `extra` and the
  reader decides: `blocked > 0` is what "finished with work left" looks like.

  Emitted only on that exit. A signal drains out through `case <-signals` and
  a permanently-stuck task returns an error; neither is a sprint that ended,
  and emitting there would tell a cron job the work was finished when a human
  had just pressed Ctrl-C.

- **A guard no test could reach, caught by trying to make it fail.** The
  once-per-run flag looked well covered: a test drove the semi-mode re-offer,
  which sends the loop back round after the termination test has already
  passed once. Deleting the guard did not turn it red — because the re-offer
  `continue`s *before* the call site, so the loop can never reach it twice.
  The test proved the re-offer works and nothing about the guard.

  The guard is kept (four exit paths have already been added to that loop; the
  fifth author should not have to notice this is an event that must not
  repeat) and is now exercised where it lives, by calling the method twice.
  Seen failing with the guard removed: "two calls wrote 2 events, want 1".

  Generalises past this function: **when a guard is only reachable through a
  path that short-circuits before it, a loop-level test cannot see it.** Try
  deleting the thing under test; if nothing goes red, the test is about
  something else.

- **Not announced on the webhooks, and that was a decision.** `sprint_done` is
  an event, not a notification. `internal/notify` announces exactly two things
  — a blocked task and a CI-blocked task — and its own doc says why: "not
  successes, and not retries: a task that will try again has not given up, and
  a message per attempt is how a team mutes the channel". A per-run success
  message is the same argument one level up. The digest is the surface for
  "how is it going", and it is cron'd by whoever wants it.

- **A run-level event has no CLI surface, and that is worth knowing before the
  next one.** `orch events` takes a required `TASK_ID` — Python's does too —
  so `sprint_done`, whose `task_id` is empty by design, cannot be listed by
  the command an operator would reach for first. It *is* visible in the
  dashboard's `/api/events`, in the live stream (G5.4), and through the MCP
  `orch_events` tool with no `task_id`, which is how it was verified: driving
  `orch mcp` over a pipe against a real finished run returned the row with
  `task=''` and `{"blocked": 1, "done": 1, "total": 2, "dispatched": 2}`.

  Left alone rather than fixed in this PR: making the positional optional is a
  CLI-surface change on a command in a different slice, and `scripts/parity.sh`
  diffs `events F2.T3 --json` against Python. Raised with orch-98 as its own
  decision. Whoever adds the second run-level event should settle it first —
  one invisible row is a gap, two is a pattern.

## G8.5 — the portfolio view

- **It is a front door, not a second dashboard, and that is the whole design.**
  `/p/<project_id>/…` hands the request to that project's own `*Server` with
  the prefix stripped — `http.StripPrefix(prefix, proj.Server.Handler())` — so
  each project keeps its own profile, its own rotated token and its own
  stakeholder allow-list, and this package contains **no second copy of the
  access model** to drift from the first.

  The alternative was re-registering each project's routes on the portfolio's
  own mux. It looks equivalent and is not: those handlers are the *ungated*
  ones, and `registerRoutes` is where `gated` is applied. Verified rather than
  reasoned about — a build with the routes re-registered returns 200 where the
  test demands 401:

  ```
  /p/gated/api/whoami = 200, want 401 ({"profile":"stakeholder"})
  ```

  Worth recording because the first version of that test did **not** catch it.
  It compared `proj.Server.Handler()` against `proj.Server.mux`, which are the
  same object, so the "bypass" it was written against was not a bypass at all.
  The test only became real when the counterfactual was the one an author would
  actually write.

- **"Operator only" and "authenticated" are not the same claim, and the docs
  had to say which one this is.** `NewPortfolio` refuses a non-operator
  profile, and `/api/portfolio` is ungated — exactly like a single-project
  operator dashboard, where the operator profile never gates anything. The
  boundary is the listener on `127.0.0.1`.

  That leaves a real asymmetry: a project whose own routes demand a token still
  contributes counters to a portfolio row that does not. Stated in
  `docs/DASHBOARD-PROFILES.md` rather than left for someone to discover, and it
  is the reason `--profile stakeholder` is refused instead of being made to
  work — a stakeholder portfolio would show every project's numbers to a token
  holder scoped to one of them.

- **`--token` and `--tunnel` are refused, not ignored.** A `--token` would
  imply a shared portfolio token exists; there is none, and each project's is
  resolved against its own row. A tunnel is configured per project — its
  config, its state file, its URL — and there is no single project to take one
  from. Same family as the run flags that are *not registered* until they do
  something (`--budgets-preset`, `--dry-run`): a flag that is accepted and
  quietly does nothing is the failure mode this port keeps finding.

- **Degrading is per section, not per project.** A row whose counters read fine
  keeps them even when the event log, the velocity query or the spend table
  fails; only a failure to load the project's view at all makes the row
  `available: false`. Dropping the summary because a later read failed would be
  throwing away the answer to keep the question tidy. Seen failing: a build
  that zeroed the row on any error fails with *"a project whose counters read
  fine reports unavailable"*.

- **`graph.Summary` folds backlog and todo on purpose, and the portfolio
  inherits that.** The first draft of the payload had a `todo` counter, which
  would have been a number that exists nowhere else in the product —
  `Summarize`'s own comment says two adjacent numbers that always move together
  read as one number badly. Agreeing the payload with sonnet-2 *before* writing
  it is what caught it: she checked `/api/sprint`'s real shape and pushed back
  on nesting, and the field list got compared against what already exists
  rather than against what seemed tidy.

- **The 30-second write timeout reaches through the front door, and the SSE
  stream still survives it.** A project's `/api/events/stream` now runs under
  the *portfolio's* `http.Server`, not its own, so the portfolio sets the same
  four timeouts — different limits would make one endpoint behave differently
  depending on which door it came through. The stream clears its own deadline
  with an `http.ResponseController`, which acts on the underlying connection
  and is unaffected by the `StripPrefix` in between (that wraps the handler,
  not the ResponseWriter). Checked by streaming through `/p/<id>/` against a
  real listener and reading the headers back, not by assuming.

- **A struct conversion instead of a field-by-field literal, on purpose.**
  `UnavailableProject` (the caller's type) and `unavailableRow` (the wire type)
  are identical today. staticcheck suggested the conversion; the reason to take
  the suggestion is not the lint: with a literal, the day the wire row grows a
  field it is silently zero for every unavailable project, and with the
  conversion the build stops until somebody decides what the caller's type
  should say about it.

- **A boundary that matters has to be typed, not documented.** The first cut of
  the portfolio wrote "the listener is the boundary" in
  `docs/DASHBOARD-PROFILES.md` and left it there. orch-98 pushed back: a single
  operator dashboard already carries that risk, but N projects — every one's
  counters, blockers and spend on one unauthenticated route — multiply it, and
  a note in a file the operator may never open is not a decision.

  `--allow-remote` is the answer rather than an outright refusal, because
  running this on a box reached over a network is an ordinary case: it is where
  several projects live, and closing the door would push an operator to
  something worse. The flag makes the exposure something they typed, and the
  banner names what they exposed — "exposed" in the abstract is easy to wave
  past, a list of your own project names is not.

  Two details that turned out to be the load-bearing ones:

  - **Refused before anything is opened**, not merely before `Serve`. A first
    version checked after `openPortfolio` so the message could count the
    projects. That makes the refusal depend on N databases opening first, and a
    refused command should touch nothing. It names the glob instead — which is
    what the operator typed and can edit.
  - **`hostIsLoopback` cannot be `host != "127.0.0.1"`.** `0.0.0.0` and `::`
    parse as valid IPs and are *not* loopback — binding every interface is the
    exact case the gate exists for — while `::1` and the whole `127.0.0.0/8`
    range are. A hostname that is not `localhost` is assumed reachable rather
    than resolved: a DNS lookup would make a security decision depend on what a
    resolver happened to answer. Seen failing: the naive predicate reports
    `hostIsLoopback("::") = true`.

- **`cmd.Flags().Changed` had leaked into a function that is not a command.**
  `runPortfolio` took its flags as a struct and then consulted
  `cmd.Flags().Changed("host")` to decide whether to apply them, so a test
  calling it directly got the default host no matter what it passed — the first
  version of the exposure test passed while the gate was never reached. The
  dance exists in the single-project path because config.yaml may hold a value
  an unset flag must not overwrite; a portfolio has no single config.yaml, so
  the flag values are already the answer.

  The empty-host fallback that replaced it is not cosmetic: `hostIsLoopback("")`
  says true while an empty `Host` makes `Addr()` `":port"`, which binds every
  interface. The predicate and the bind have to agree, or the gate is checking
  something the listener does not do.

- **A route that does not exist answered 200 with HTML, and a whole feature
  gate was built on its 404.** `/api/portfolio` is registered only under
  `--portfolio`; on a single-project dashboard it fell through to the SPA at
  `"/"`. G8.6's page asks "is this process a portfolio?" by reading a 404, so
  it got a page of HTML as a truthy payload, showed the Portfolio nav item on
  every single-project dashboard, and then indexed a string.

  Found by orch-opus reviewing this PR **against the other one**, in a seam
  neither PR owns: each half was right about itself. Worth naming as a class —
  *a client's negative case tested against a server that never returns it* —
  because both sides pass their own tests and the bug lives only where they
  meet. The cheap fix is on the server: register the route and refuse it.

  The refusal is **ungated**, which makes it the second exception to "every
  data route goes through `gated`" after the SPA. It carries no data; it is
  the absence of a feature. Gating it would make a stakeholder dashboard answer
  401 for something that does not exist in that process — the same unusable
  answer as the 200, by another route.

- **Two sections sharing one `else`, and a test that only looked at one of
  them.** `portfolioRow` nested the blocker lookup inside the `else` of the
  velocity read, so a failed `CountDoneLastNDays` silently took the blocker
  reasons with it: the row went on saying "3 blocked" and listed none, and an
  operator could not tell "no reasons recorded" from "a query failed". Against
  this function's own stated rule, in the same file that states it.

  `TestPartialReadKeepsTheCounters` did not see it because it asserted
  `VelocityPerDay == 0` and nothing about the blockers. The replacement asserts
  the **pair** — the count and the reasons — because the count alone is what
  made the old behaviour look right.

- **`project_name` was the id a second time.** Both fields were `paths.ID`, so
  a project's "name" was its directory's base name and the payload carried one
  value under two keys — an invitation for the page to render the wrong one.
  It now comes from `tasks.json`'s `meta.project`, falling back to the id when
  the file names none. Visible immediately on a real copy: a directory renamed
  to `billing-api` whose tasks.json still says `e2e` now reports
  `project_id: "billing-api"`, `project_name: "e2e"`, which is the honest
  answer and the reason the two keys exist.

- **`Config.Addr()` could not spell an IPv6 host.** `fmt.Sprintf("%s:%d")`
  produces `:::7420` for `--host ::`, which `net.Listen` rejects with "too many
  colons in address". Pre-existing and unreachable while every host anyone
  passed was v4 — `--allow-remote` is what made binding `::` something an
  operator can ask for, so the same commit that opened the door fixes the step
  behind it. `net.JoinHostPort` now, with a table that checks every case is
  still parseable by `net.SplitHostPort` rather than only that the string looks
  right.

## The 404 rule — a class, not three patches

- **Three times is a rule.** A path the Go port had not implemented answered
  **200 with the SPA's HTML**, because `"/"` takes whatever no route claimed.
  Every client that fetched one got a page of markup where it expected data:

  - `/api/portfolio` broke the portfolio page's "is this a portfolio?" check
    (found by orch-opus reviewing G8.5 against G8.6);
  - `/stakeholder/summary` **crashes the landing page** — axios hands the HTML
    string through, `if (!data) return null` finds a non-empty string truthy,
    and `data.summary.done` throws `Cannot read properties of undefined
    (reading 'done')`. Python serves that route (`server.py:1731`); the Go
    port never wired it;
  - `/api/tunnel/logs`, the third, waiting for a consumer.

  Found by sweeping all fifteen paths the SPA fetches against the running Go
  binary — eleven JSON, two 404, two HTML — which is a two-minute check nobody
  had run.

- **The measurement that made the case.** With the rule, the landing page goes
  from 597 bytes and an empty root to 9471 bytes and the SPA's own
  `Failed to load summary` alert. That branch was written all along and was
  unreachable while the server answered 200. *A client's error handling cannot
  run if the server never produces the error* — the same shape as the seam
  between two PRs, one level down.

- **Prefixes, not `"/"`.** `/api/` and `/stakeholder/` are the data prefixes:
  everything under them is fetched by code, never typed by a person, so the
  answer has to be machine-readable. Registering the rule at `"/"` would 404 the
  app itself, so there is a test that a hard refresh on `/kanban` still reaches
  the SPA — checking the belief about `http.ServeMux` specificity rather than
  restating it, because a prefix that shadowed real routes is a worse failure
  than the one being fixed.

- **Ungated, and it is the second exception after the SPA.** A 404 carries no
  data: it is the statement that a route exists nowhere. Gating it would make a
  stakeholder dashboard answer 401 for something that exists nowhere, which is
  the same unusable answer as the 200 was, one status code along — and would
  break the same client check in a new place.

## `/stakeholder/summary` — the allow-list named a route nothing served

- **The name shipped without the route, and that is the whole shape of it.**
  `stakeholder_summary_json` has been in `DefaultStakeholderRoutes` since the
  allow-list was ported (#165), with a comment calling it "the summary the
  whole profile exists for". Nothing ever registered it. So the stakeholder
  profile advertised a page, the server answered it with the SPA's HTML at 200,
  and the page crashed on the markup.

  A third variety of the family already in this file: a writer with no reader,
  a constructor with no caller, a guard no path reaches — and now **a name with
  no thing behind it**. All four pass every test that exists, because what is
  missing is the other end of a pair nobody looked at together.

- **The port is the payload, not the computation.** `snapshot.Build` already
  computes every figure; this route maps its output onto the key names the SPA
  reads. Serving the snapshot's own shape would have swapped one crash for
  another — the page destructures `project_id`, `spend_rounded_usd`,
  `phases_timeline`, `exec_summary`, and the snapshot emits `project_name`,
  `budget`, `milestones` with different field names, `executive_summary.text`.

  One compiled `web/` bundle serves both dashboards, so one JSON shape is the
  architecture and not a detail — confirmed with sonnet-2, who owns the page,
  before the mapping was written rather than after it broke.

- **One figure is computed here and it is worth naming.**
  `phases_timeline[].estimate_hours` is a per-phase sum the snapshot does not
  carry (it totals the project). It is a sum over tasks already loaded, not a
  second definition of anything — every other number in the payload is mapped.

- **`int(x)` is not `round(x)`.** The phase percentage first read
  `int(round2(done/total*100))`, which truncates after rounding to two
  decimals: 66.666 became 66 where Python's `round()` gives 67.
  `roundDecimals(_, 0)` formats with `strconv.FormatFloat(..., 'f', 0, _)`,
  which rounds half to even exactly as CPython does.

  **And the first test for it could not have caught it.** It pinned a phase
  that is one of two done — 50%, which needs no rounding at all, so truncating
  and rounding give the same answer. Gemini blocked the PR on exactly that
  (rule 24), and it was right: the gap was written down in this file's own
  first draft ("50%, no rounding") and then not closed. Noticing a hole and
  describing it is not the same as testing it. The cases now are 2 of 3 (66.666
  → 67, where truncation gives 66) and 1 of 8 (12.5 → 12, which separates half
  to even from half up), both seen red against the truncating form.

- **The measurement, across three PRs, on the same page and binary:**

  | | bytes | `#root` | what the operator sees |
  |---|---|---|---|
  | before #206 | 597 | empty | blank page, `TypeError` in console |
  | #206 (the 404 rule) | 9471 | mounted | the SPA's own "Failed to load summary" |
  | this | 21692 | mounted | the summary, zero exceptions |

  Each step is an improvement on its own, which is what made the merge order
  safe to interleave with #205 rather than having to land all three at once.

## Looking with a browser — the tooling, and what it cost to get right

- **`--dump-dom` cannot see the view that matters.** It prints after `load`,
  and the dashboard opens `/api/events/stream` as soon as it is authenticated,
  so the command hangs until `timeout` kills it (`rc=124`).
  `--virtual-time-budget` does not help: it advances virtual time and does not
  close a real socket. The unauthenticated view dumps fine — which is exactly
  why the tool looked adequate until someone logged in.

  `scripts/ui-dom.py` drives CDP instead: navigate, settle, read
  `document.documentElement.outerHTML`. Forty lines of RFC 6455 over stdlib
  sockets, which is cheaper than a dependency in a repo whose Python side is
  frozen.

- **The console listener is the half that turns a symptom into a diagnosis.**
  Subscribing to `Runtime.exceptionThrown` *before* navigating is what changed
  the finding from "the page is blank" — which nobody can act on — to
  `TypeError: Cannot read properties of undefined (reading 'done')` at a named
  line, and from there to a route answering HTML with a 200. Two sessions had
  already looked at that blank page and set it aside as inconclusive.

- **A page that failed to load still produces a DOM**, and it greps like a
  real one. Navigating before the server was listening gave Chrome's own error
  page: 188 KB of markup, no assertion matched, and the reason was only visible
  in `<title>127.0.0.1</title>`. Hence the `until curl -sf …` in the recipe
  rather than a `sleep`, and the advice to check the title first.

- **`pgrep -f <pattern>` matches its own shell**, so a clean machine reports
  one survivor and "did I leave a browser running?" answers wrong. Cost several
  minutes of chasing a process that was the question itself.

- **Bytes and the mount point, not the status code.** A page that throws during
  render still returns 200 and still serves the shell. `wc -c` plus
  `<div id="root">` is what made three PRs a measurable progression — 597 bytes
  and an empty root, 9471 with the SPA's own error alert, 21692 with the page
  — instead of three assertions that it worked.

- **A stale comment above fresh code.** `review.yml`'s block comment still
  says `no key, crash, bad output -> neutral, never success`, which is what the
  step did *before* the conclusion logic right below it was rewritten to fail
  on everything but a missing key. Reported to orch-98 rather than edited: it
  is their file and their change, and a comment that contradicts the code under
  it is worth one message. Same class as the `BudgetReporter` comment that said
  "aliased" when it was a copy — the fix there was to make the comment true.
