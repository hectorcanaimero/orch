# Go migration notes — lane opus

Append-only. One entry per finding, newest last. Format and numbering follow `docs/brainstorm/go-migration-notes.md`; that file is now history and is not edited after 2026-09-12.

- **Go bug (no number — nothing in Python changes): `router.Load` silently
  discarded every route written below a `{}` line.** Found by sonnet wiring
  `router add-missing`; the same file shape as the Python half of bug 14, with
  a worse failure.

  `orch init` wrote `{}` as its empty router until #141. `AddMissing` appends a
  block mapping, and a block mapping after a complete flow document is a
  *second* YAML document. `yaml.Unmarshal` reads the first and ignores the
  rest — so `AddMissing` reported `added=[claude/x]`, wrote the file, and
  `Load` on that same file returned an **empty router with no error**. Every
  task using those models then failed as unrouted, pointing at the file that
  visibly contains them.

  PyYAML raises on the identical file. Go being quieter than Python is the
  wrong direction, and "reports success, discards the work, no error anywhere"
  is the failure mode that takes longest to trace: the operator is looking at
  a file whose contents contradict the error.

  Fix: `Load` decodes with `yaml.NewDecoder` and refuses anything after the
  first document, with a message naming the `{}` to delete; `AddMissing` drops
  a lone empty flow mapping from the header before appending, keeping every
  comment above it. Only an *empty* flow mapping alone on its line is removed —
  this repairs a stub, it does not reformat a hand-authored file. A file
  already corrupted by the old code is refused rather than guessed at.

  Every project scaffolded before #141 carries one of these stubs.

- **The embedded template tree is a second copy of data that has already
  produced two bugs.** Not a finding, a standing hazard, recorded because the
  next person to fix a template will only see one of the two places.

  `go:embed` cannot reach outside its own package directory, so
  `internal/templates/files/` duplicates `orchestrator/templates/` until the
  Python tree is deleted. That data has produced bug 12 (every `specRef`
  carrying a `specs/` prefix that `spec_root` already supplies) and half of
  bug 14 (the router stub closing its own YAML document). Both were invisible
  by reading and only showed up when something ran.

  `TestGoTreeMatchesPython` walks both trees and fails on a file that is in one
  and not the other, or present in both and different. `expo-mobile` is in an
  explicit four-entry `goOnly` allowlist. The test reads the Python tree by
  relative path, so it stops working the day that tree is removed — at which
  point this becomes the only copy and the test has nothing left to check.
  **Delete it then; do not weaken it now.**

- **`expo-mobile` is the one new feature in the Go port.** H-1e, the fifth
  canonical template, outstanding since before the migration began. Written
  only in Go, because Python is frozen — the one place where "no new features
  in Python" and "finish what was started" point in different directions.

  Two things in it are worth keeping if anyone rewrites it. The jest preset is
  named in the task description (`jest-expo`, because the default `jsdom`
  environment cannot render React Native components and the error it produces
  points nowhere near the cause), and the AGENTS.md says what CI cannot do:
  there is no simulator and no device on a runner, so a task whose acceptance
  criteria need a running app has to say how to verify it by hand.

- **Bug 15: a placeholder in the nextjs-saas AGENTS.md looked like a real
  JWT.** `NEXT_PUBLIC_SUPABASE_ANON_KEY=eyJhbGci...` in an illustrative
  `.env.local` block. Not a secret — a truncated placeholder — but `eyJhbGci`
  is the base64 of a JWT header, so every scanner reads it as one, and every
  other line in the same block uses an unmistakable fake (`pk_test_...`,
  `sk_test_...`, `whsec_...`). Now `example-anon-key-not-real`, in both copies
  of the file at once, which is what the sync test is for.

- **Every writer ported without its reader surfaces weeks later, blocking
  another lane.** Three times now, same shape:

  | writer landed | reader missing until | who it blocked |
  |---|---|---|
  | `RecordSpend` | #116 `SpendSince` | the budget gate (G2.2) |
  | run rows | #130 the three tallies | `latest_run` in `status --json` |
  | `RecordDispatch` | this PR `InFlightDispatches` | resume + reconcile (G3.3) |

  Each one looked complete at the time, because the thing being ported was a
  write path and the tests exercised the write. The parity fixture hid two of
  them further: it has no spend rows and empty run tallies, so an
  implementation that always answered zero passed both the fixture and
  `scripts/parity.sh`.

  Working rule from here (orch-98): **a PR that adds a write method to
  `Backend` adds its read in the same PR.** Not for symmetry — because the
  cost of the gap is paid by whoever needs the read next, and they pay it as a
  blocked task rather than as a missing method.

- **Divergence: the wizard inlines its choice hint only when the hint helps.**
  Python's `prompt` writes `[{'/'.join(choices)}]` unconditionally. For `[y/n]`
  and `[sqlite/file]` that reads well. For the model tier picker it does not —
  the router keys *are* slash-separated paths, so fifteen of them joined by a
  slash produce

  ```
  default premium model [agy/pro/claude/claude-opus-4-7/codex/gpt-5.6/gemini/…]
  ```

  where no reader can tell which slashes separate choices and which are inside
  one. The options are already printed above it, one per line, so the bracket
  adds only the confusion. Go inlines it when every choice is slash-free and
  there are at most six. `orch init` is interactive and its output is not
  compared by `scripts/parity.sh`, so nothing depends on the difference but a
  reader.

  **The eight wizard tests passed with the unreadable version.** None of them
  asserted on the prompt line; they checked the returned options and the
  summary. That is checklist rule 23 from the other side — not a normalisation
  that hid the difference, but a part of the output nobody ever looked at. It
  only surfaced because I printed a full transcript and read it. Two tests now
  cover it: one on the decision directly, one that the tier options appear one
  per line and are *not* joined into the prompt.

  The general form, worth more than the fix: **a green test suite says nothing
  about output no test reads.** Print it and look at it once.

- **The wizard treats exhausted input as an error, not as a yes.** If the line
  reader returned an empty string at EOF, every remaining question would take
  its default — including the H-7 confirm gate, whose default is `y`. Closing
  a pipe would scaffold a project nobody approved, which is the single outcome
  that gate exists to prevent. `io.EOF` comes back wrapped, and a test asserts
  that running out of answers confirms nothing.

- **Bug 17: the wizard asks for a budget preset, shows it in the confirm
  summary, and throws the answer away.** Found wiring the Go wizard's answers
  through to the files they are supposed to change.

  `_post_process_config` rewrites `state.backend`, `budgets_preset` and
  `spec_root` in the scaffolded config with three `re.sub(..., count=1)` calls.
  A regex that matches nothing changes nothing — and **none of the four shipped
  templates contains `budgets_preset`**. So for every templated project the
  wizard asks the question, prints the answer in the H-7 summary, takes the
  operator's "yes", and the project then loads with the packaged default.

  The severity is in the confirm gate, not the setting. A gate that displays a
  choice which then has no effect is worse than never asking: the operator has
  been shown their answer and told it took. The same code path works for a
  blank project, whose packaged config.yaml does have the key, which is why it
  has never been noticed.

  Go appends the key with a comment when the file lacks it. A nested key —
  `backend:` under `state:` — is still never appended, because a bare
  `backend:` at the end of the file would be a different, top-level setting.

  Not fixed in Python: it is a one-line `re.sub` fallback, but the tree is
  frozen and the Go side no longer has the bug.

- **The wizard collected three answers and returned none of them.** My own,
  caught before the PR merged and worth recording because of how it hid.

  `Wizard` read `state backend`, `budget preset` and `spec root` into locals,
  printed them in the summary, and returned an `Options` that had no fields
  for any of them. Eight tests passed: they asserted the summary *displayed*
  each answer, which it did. Nothing asserted the answer *took effect*, and
  for that to be checked at all the test has to look at the file the scaffold
  writes, not at the wizard's output.

  It only surfaced because I went to port `_post_process_config` and found
  there was nothing to pass it. The shape to remember: **a test that an
  interactive tool displayed a choice is not a test that the choice was
  applied**, and the two live in different files.

- **Bug 18: `atomize` computed the spec ref against a root nothing else
  agrees with.** `specs_root` defaulted to `<project-root>/docs`;
  `prompt_builder` resolves a task's `specRef` against `spec_root` from
  config.yaml, default `specs`; and `orch init` creates `specs/` and tells
  the operator to write their first spec there. The `docs/` default has no
  written justification anywhere — not the manual, not the init banner — so
  it is the default that contradicts the product.

  With the two roots disagreeing, `_relpath_for_spec_ref` fell to its
  "outside the root" branch on *every* run and returned the bare filename.
  That is right by accident for a spec directly under the spec root, which is
  why it was never noticed — and wrong for one in a subdirectory:

  ```
  file on disk : <project>/specs/api/auth.md
  specRef      : auth.md#F0.1.T1          ← the "api/" is gone
  prompt says  : specs/auth.md#F0.1.T1    ← does not exist
  ```

  Organising specs by area is the first thing anyone does past three of them.

  Fixed by making `specs_root` default to `project_root / cfg["spec_root"]`.
  `--specs-dir` still wins. The `.name` fallback stays for a genuine
  `--file /elsewhere.md`, but now warns on stderr that the ref lost its
  directory — until this, that branch ran silently on every invocation, which
  is what made a wrong ref look like a working one.

  **Migration**, for a project that really does keep its specs in `docs/`:
  pass `--specs-dir docs`, or set `spec_root: docs` in config.yaml.

- **Bug 19: the spec parser read fenced code blocks as spec content.**
  **A fenced block is documentation *about* the format, never content.**

  `specs/README.md`, which `orch init` writes, documents the minimum format
  inside a ```` ```markdown ```` fence. The parser matches headers line by
  line and knows nothing about fences, so `orch atomize --apply` with no
  `--file` on a project straight out of `init` imported the example as two
  real tasks — "Setup monorepo" and "Root README".

  Found while fixing bug 18, and hidden behind it: `specs_root` pointed at
  `docs/`, which does not exist, so the scan found nothing at all. Fixing one
  root made the other visible — the ordinary way a second bug in the same path
  shows up, the first was stopping the code from running.

  The fix is in the parser, not in the README: any spec that documents its own
  format inline has the same problem, and orch's README is just the one that
  ships. **The Go port inherits it** — `internal/atomize`'s parser is faithful
  to the original, so it needs the same skip and the same test.

- **The gap these five bugs came through was coverage by ROUTE, not by line.**
  12, 14, 17, 18 and 19 all live on the path a new user walks in their first
  five minutes — `init` → write a spec → `atomize` → `dry-run` — and until
  now not one test walked it end to end. `init_cmd.py` and `atomize.py` were
  both well covered by line; every test asserted on a file one of them wrote,
  and the bugs were all in how the files agreed with each other.

  There are three such tests now, in three different files: Python's
  `test_scaffolded_project_dry_runs_clean` and
  `test_spec_ref_resolves_where_the_prompt_looks`, and Go's
  `init.txtar`, which scaffolds and then runs `orch validate` on the result.

- **A fourth set of columns arrived without the methods that use it.** The PR
  and CI columns of `tasks_runtime` landed with migration 005 and `scanTask`
  has read them since the start; nothing could write them or query on them, so
  the whole CI-polling path was unbuildable. Reported by opus-2 starting
  `_check_ci_once`.

  | columns / rows | methods missing until | who it blocked |
  |---|---|---|
  | `spend` | #116 `SpendSince` | the budget gate (G2.2) |
  | `runs` tallies | #130 | `latest_run` in `status --json` |
  | `dispatches` | #148 `InFlightDispatches` | resume + reconcile (G3.3) |
  | `pr_url` / `ci_status` / `ci_attempts` | this PR | the CI poller |

  The working rule from #148 — *a PR adding a write to `Backend` adds its read
  in the same PR* — would have caught the first three. It does not catch this
  one, because these columns arrived in a MIGRATION, with no method at all on
  either side. Widened version: **a migration that adds columns lands with the
  methods that read and write them, or with a note saying which PR will.**

- **Endpoints whose only consumer is being deleted (G5.5).** sonnet-2's operator
  SPA trim removes the pages behind ten of the twenty-two endpoints G5.2 was
  scoped to. Recorded here so the decision is not re-derived later:

  - **Not ported** (the CLI already does the job): `/api/doctor` — `orch doctor`
    exists; `/api/config/setup` — `orch init` exists.
  - **Not ported, tunnel's own phase**: `/api/tunnel/{start,stop,logs}`, and
    `{capabilities,status}` fold into G5.6.
  - **Pending, not discarded — stakeholder content, the `publish/` lane
    decides**: `/api/architecture/{status,history,regenerate,current}` and
    `/api/docs`, `/api/docs/content`. These are PRD/SPEC markdown and diagrams,
    and `api_docs_list`/`api_docs_content` are in the stakeholder profile's
    allow-list by design. The operator SPA losing them says nothing about
    whether G6.1-G6.3 wants them.

- **The dashboard's access model is a decorator, not middleware.** The port
  that would have been faithful is the one that reproduces bug #97.

  Python wraps the whole app and works out, per request, whether the path would
  have reached a data route — matching prefixes, consulting the resolved route
  name, special-casing the SPA mount. That classification is where #97 lived:
  the gate covered the shell and `/assets/*`, a browser asked for the bundle
  with no token, got 401, and the page rendered blank with a **200 already on
  the HTML**.

  Go decorates the handlers that need gating and leaves the static handler
  undecorated. Public is the default for what the SPA serves, so there is
  nothing to classify — and an API route registered without `gated` is a
  visible omission at the registration site rather than an invisible hole in a
  prefix list in another file.

  What settled it was sonnet-2 running a real `pnpm build`: Vite copies
  `web/public` to the **root** of dist, so `favicon.svg`, `manifest.json` and
  the PWA icons sit beside `index.html`, nowhere near `/assets/`. Any
  allow-list of static paths is a list somebody has to keep in step with
  whatever `web/public` holds. Their first proposal — "only `/api/` needs
  auth" — had the opposite hole: `/logs/stream` is not under `/api/` and
  streams log content.

- **Half of `DEFAULT_STAKEHOLDER_ROUTES` names no endpoint.** After G5.2(a),
  `api_docs_list`, `api_docs_content` and `stakeholder_summary_json` are on the
  stakeholder allow-list and nothing serves them — the first two are pending on
  the `publish/` lane, the third is that lane. Not an error: **the list is
  policy, not inventory**, and it says what a stakeholder may see when it
  exists. Worth knowing before someone reads it as a route table.

- **A config option nothing can consume is not an option.** `Validate` accepts
  `port: 0` — "any free one", which is what a test or an ephemeral tunnel
  wants — and `Serve` bound it correctly, but the only address a caller could
  read back was `cfg.Addr()`, still `:0`. The option was legal, documented and
  unusable. `Ready()` + `BoundAddr()` close that; the general shape is worth
  keeping in mind as the Go tree grows: **a setting is finished when something
  can read back what it did**, not when the write path accepts it.

- **`internal/dashboard/manualcheck_test.go` is checklist rule 29 written down
  as code.** It starts the real server with the real embedded SPA and makes ten
  real requests, skipped unless `ORCH_MANUAL_CHECK=1`. The point is that "I ran
  it and looked" stops being a claim in a PR body and becomes something the
  next person can re-run. It is what proved #97 is shut: the shell, the hashed
  bundle and the root-level public files all answer 200 with no token while
  `/api/config/status` answers 401 in the same server.

- **Two Python functions that look total and are not.** Both surfaced while
  porting `metrics.py`, and both would have passed a test written from the
  docstring.

  `critical_path` breaks ties by DICT INSERTION ORDER. `max(dist, key=…)`
  returns the first maximal key, `dist` is filled in the order a LIFO stack
  pops, and the stack is seeded in tasks order. Two branches of equal weight —
  which is what an un-estimated project of parallel work is made of — resolve
  to whichever was declared LAST. The Go port reproduces the pop order and
  keeps its own insertion list, because a Go map would have decided it at
  random: a test that passes four times out of five is worse than one that
  fails. `testdata/make-analytics-golden.py` has the same graph declared in
  both orders and Python answers differently for each; that pair is the test.

  `parallelizable_tasks` iterates `by_id.values()`, so **on a duplicate id only
  the last row is considered at all**. With `[A todo, A done, B deps=[A]]`
  Python answers `["B"]`: B's dependency is satisfied by the second A, and the
  first A — the row a reader would expect to see offered — is not evaluated.
  Verified against Python rather than assumed; it is in the Go tests as
  `TestDuplicateIDsResolveToTheLastOne`.

- **`human_hours_by_task` raises on mixed timestamp forms.** `_parse_ts`
  returns a naive datetime for a timestamp with no offset and an aware one for
  a `Z`-suffixed one, and `(term_ts - pending_dispatch_ts)` on one of each is
  a `TypeError` that escapes the function into whatever endpoint called it —
  `/api/tasks` among them. Both forms are in the wild: the SQLite rows carry
  `Z`, some imported JSONL does not. The Go port reads the offsetless form as
  UTC, which is what `_parse_ts`'s own comment intends, and answers instead of
  failing. A deliberate divergence, not an oversight.

- **`round(x, 3)` is not `math.Round(x*1000)/1000`.** CPython rounds the exact
  binary value, correctly, half to even; the arithmetic form rounds half away
  from zero AND rounds an already-scaled value, so it is wrong twice.
  `strconv.FormatFloat(v, 'f', 3, 64)` parsed back is the correct form, and it
  is what `project.round3` and `dashboard.round1` do. Worth knowing before the
  next port reaches for the multiply.

- **A recorded cost of zero means "nobody billed", not "free".** The rule is
  the whole of `pricing.py` and it is easy to port backwards. The backends
  that genuinely cost nothing to call (a local codex, opencode) are the same
  ones that report no cost at all, so the two are indistinguishable in the
  row — and taking the zero literally would show a project running entirely on
  those backends as having cost nothing. The port keeps Python's reading: above
  zero is the truth and is never second-guessed; anything else — zero, missing,
  NEGATIVE — is estimated from tokens against the price table.

- **Two names for the same fallback, and they are not the same.**
  `metrics_by_model` groups a spend row with no model under `"unknown"`;
  `total_cost` prices the same row as `"default"`. Both land on the default
  price row unless a project's `pricing.yaml` defines a model literally called
  `unknown`, at which point the table and the total disagree by that row.
  Ported as-is rather than unified: unifying it would change one of the two
  numbers, and which one is not obvious.

- **The embedded `pricing.yaml` is a copied data file, which means it drifts.**
  Same shape as the template tree in #145. The guard is the same: the Go test
  compares against a golden generated from the table PYTHON actually loads
  (`internal/pricing/testdata/make-pricing-golden.py`), not against the file it
  was copied from. A model added to Python's table and not to Go's fails the
  test.

- **`int(used / budget * 100)` truncates, and the threshold comparison rides
  on it.** 999 tokens of 1000 is 99%, not 100, so a `threshold_pct: 100` fires
  exactly at the budget and not a token earlier. Worth pinning: the natural Go
  spelling with `math.Round` moves that boundary and nothing else in the system
  would notice.

- **"An hour ago" is not "today".** My own test bug, caught by the clock rather
  than by review: `/api/budget/summary` reads two different windows — a 5h
  rolling one for tokens and midnight UTC for the USD column — and a row dated
  `now - 1h` falls outside the second one for the whole first hour of every UTC
  day. A test that only fails between 00:00 and 01:00 UTC is the kind that gets
  re-run until it passes. Rows in these tests are dated `now`.

- **Bug 21 — velocity counts row touches, not completions.**
  `count_done_last_n_days` filters on `updated_at >= datetime('now', -N days)`,
  which is the row's mtime. A task that finished three weeks ago and was edited
  yesterday counts as finished yesterday, so a burst of edits on old rows reads
  as a productive week. Every ETA the dashboard shows — `/api/sprint`'s and
  every milestone's — is projected from that figure, and velocity is the one
  number on the page that claims to be a measurement rather than a reading of
  state.

  **Fixed in Go, annotated in Python.** `CountDoneLastNDays` counts
  `COALESCE(NULLIF(finished_at, ''), updated_at)`: rows written before the
  column was populated keep the old answer because there is nothing better to
  ask them, and every row since is counted by when the work actually finished.
  Not fixed in Python — it would move the figures of every project on the
  legacy branch and the migration is where the corrected version belongs.

  One detail I had backwards until I read `Transition`: **`finished_at` holds
  the MOST RECENT entry into done, not the first.** The order of the COALESCE
  arguments there is deliberate and pinned by a fixture generated from the
  Python backend, with a comment explaining that a reopened-and-refinished task
  must report the second finish or the same project reports different velocity
  depending on which binary closed it. So a reopened task counts once, on the
  day of the second finish. I wrote the doc comment and the test claiming
  "first" before checking; the test failed for an unrelated reason — the
  transition table has no `done → in-progress` edge — and reading the code to
  fix that is what turned up the real semantics.

- **`/api/sprint` and `/api/milestones` project from two different ETAs with
  similar names.** `sprint_eta` divides REMAINING TASKS by tasks-per-day;
  `eta_hours_remaining` (not on any endpoint in this lane — it belongs to the
  stakeholder snapshot) sums remaining ESTIMATE HOURS and scales them by how
  far past estimate the finished work ran. They answer different questions and
  can disagree by a lot on the same project. Worth knowing before someone
  "unifies" them.

- **A test that HANGS is a test that found something.** `orch dashboard
  --profile stakholder` — the deliberate typo in the testscript — did not
  error. It started the server and served, and the txtar sat there until the
  400-second timeout killed it.

  The hole: `FromConfig` validates the profile it reads from config.yaml, and
  the command then overwrote it with the flag's value and only called
  `Validate`, which checked the token and the port and not the profile. An
  unknown profile is not inert — `decide` reads anything it does not recognise
  as "no stakeholder context", which is **allow everything**. So a misspelled
  flag meant to RESTRICT access opens it, and the symptom is a page that works.

  Fixed in `Validate` rather than in the command, so it covers every
  construction path rather than the one that happened to be wrong. The comment
  in `FromConfig` already said exactly this about the config.yaml spelling; I
  wrote that comment and then left the flag path open two PRs later, which is
  its own lesson about where a rule belongs: **validate where the value is
  used, not where it is first read.**

- **Two different fixtures are called `parity-project`.**
  `internal/graph/testdata/parity-project` is deliberately broken — a
  self-dependency, a cycle, a missing dep, a task with no id — so the
  validators have every error kind to report.
  `testdata/parity-project` at the repo ROOT is a real, healthy project, the
  one `scripts/parity.sh` and every CLI testscript use (the harness copies it
  into `$WORK/proj` for every script, which is why an `orch init proj` at the
  top of a new txtar fails with a conflict). They are not interchangeable and
  the names do not say so.

- **PATH is consulted only after all three tunnel gates pass, and that order
  is the decision.** `/api/tunnel/capabilities` answers 200 to anyone — it has
  to, because the SPA decides whether to draw the tunnel panel before it has
  asked anybody for a token — so what it discloses is the whole of its
  security surface. The gates run config → operator profile → loopback host →
  binary on PATH, and the reported `reason` is the FIRST failure, never a
  list.

  Checking PATH first would be cheaper and would tell a stakeholder on a
  shared URL whether the box has `autossh` installed, before establishing they
  may ask anything at all. Python's TUN-4 states the order; the Go port folds
  it into one function (`tunnel.EvaluateCapabilities` plus the caller's own
  `if` for the binary) so a future handler cannot reorder it by accident. The
  table test fails every row on a different gate with everything after it also
  failing, which is what catches a reordering.

- **`/api/tunnel/{start,stop,logs}` are not ported.** G5.5 made the operator
  SPA read-only, so the three POST/stream routes have no consumer, and this
  server takes a read-only `StateReader` by construction. The lever moved to
  `orch dashboard --tunnel`, which is the safer shape anyway: the person
  holding it is the person who started the process. It refuses under any
  profile but `operator` — raising a public URL from a dashboard whose own
  gate says the operator is absent is not something to do quietly.

- **Bug 25 — the SSE tailer drops events that share a second.** Event
  timestamps have SECOND precision (`_utc_now_iso`, a format shared with
  `scripts/task-*.sh`, so it is a contract rather than a choice). Python's
  SQLite tailer remembers the last timestamp it delivered and polls
  `WHERE ts > last_ts`, so an event written in the same second as the last one
  delivered — but after the poll that delivered it — is skipped, and since
  `last_ts` never goes back, it is never seen again. A dispatch and the block
  it causes land in the same second constantly.

  **Reproduced against the real Python tailer** before reporting it, with
  three events and a 0.2s poll:

  ```
  delivered: ['dispatch', 'success']
  ```

  The `block`, written in the same second as the `dispatch`, is missing.

  Go polls `id > afterID`. The id is monotonic and unique, so there is no
  window at any clock resolution. Annotated in Python rather than fixed — it
  is the legacy dashboard, and the migration is where the corrected version
  belongs.

- **The Go tail starts at the newest id; Python replays the whole log.**
  Python's tailer starts with `last_ts = ""`, so every connect re-delivers
  every event ever recorded as if it were new. The SPA survives it — it
  deduplicates against the history it fetched from `/api/events` — but
  `useEventStream` invalidates the tasks query once per event, so a project
  with a long log pays N refetches for one connect. The Go stream starts at
  `LatestEventID`, carrying only what happens after the client connected.

- **An SSE handler has to clear the server's write deadline.**
  `http.Server.WriteTimeout` is 30s for every other route and a stream lives
  for hours; without `http.NewResponseController(w).SetWriteDeadline(time.Time{})`
  the connection is cut mid-stream at thirty seconds, and the client cannot
  tell that from a network fault. Worth knowing before the next long-lived
  response: the timeout that protects every normal handler is the one that
  breaks this one.

- **A fake cannot test the bug its own shape forbids.** My first version of
  the same-second test drove the dashboard's `fakeState`, which indexes events
  by id — so it could not fail the way Python fails, whatever the handler did.
  The assertion that matters moved to `internal/state`, against a real
  database, where the key actually lives. Same family as the three complicit
  tests in rule 23; this one I caught in my own work rather than in Python's.

- **A corrupt `extra_json` now says so instead of vanishing.** All four event
  readers in `internal/state` had the same three lines: try to parse the
  extra, and on failure set it to nil. That loses the distinction between "this
  event had no extra" and "this event's extra was unreadable", which is
  exactly the information somebody debugging a broken row needs.

  One shared `decodeExtra` now, and on a parse failure it preserves the
  unparsed text under `malformed_extra_json` rather than returning nil. The
  event still ships — the row is evidence something happened and the extra is
  decoration — but the damage is visible in the log view instead of being
  indistinguishable from an empty object. A deliberate one-key divergence from
  Python, on a path only a corrupt row reaches.
- **`orch explain` is new, and the reason is that the answer was already there
  and scattered.** Task counts live in `orch status`, what could run now in
  `orch tasks`, the guardrail in `orch budget` — so somebody arriving at a
  project had to know three commands to assemble a picture none of them draws.
  Python has no equivalent.

  It shares one function with the MCP server's `orch_context`
  (`internal/explain.Gather`), rendered as text for a person and as JSON for an
  agent. That is the point rather than a convenience: the way a story like this
  drifts is by being assembled twice, which this migration has already found in
  the duplicated `dashboard:` block and in two spellings of `burndown_by_day`.

  `internal/explain` and not `internal/project`, because `project` is
  deliberately the narrow "tasks.json + runtime row + event log" layer that
  `orch status` sits on, and putting the budget guardrail in it would make
  every consumer of `Hydrate` depend on the gate.

- **The typed-nil interface trap, closed on purpose and worth naming.**
  `newBudgetGate` returns `*budget.Gate`, and nil means "no budgets.yaml".
  Assigning that nil POINTER to an interface field produces a non-nil
  INTERFACE holding a nil pointer — so `opts.Budget != nil` passes and the
  first method call panics, because `Gate.Disabled` dereferences its config.
  The conversion is three lines with a name and a comment rather than an inline
  assignment, which is the only version of this that survives a refactor.

- **Reading my own output changed it twice.** The first run of `orch explain`
  against a real project printed "(1 in progress; the rest are waiting on
  them)" — asserting a cause it had not checked, since a task with an
  unfinished dependency is waiting on THAT dependency and not necessarily on
  the one in flight. And the "safe to run" list came out with no fourth line at
  all for that project, because none of its cases matched: nothing ready,
  nothing blocked, tasks present. Both are now fixed, and neither would have
  shown up in a test I wrote from the code.
- **The dashboard served comments from the wrong store (bug 24, second half).**
  Reported by opus-2 while looking at `internal/project` for G6.6, and it was
  my code. `internal/dashboard/view.go` publishes `comments` from
  `model.Task` — that is, from tasks.json — but since F-12 the comments a task
  accumulates live in `tasks_runtime.comments_json`, appended by every
  `Transition`. A scaffolded tasks.json has none, so **any project orch had
  actually run showed an empty comments array**, with the notes sitting in the
  database the same request had already opened.

  Confirmed before fixing rather than argued, on a real project: `orch task set
  --id F0.T1 --status in-progress` writes
  `[{"at":…,"author":"operator","body":"manual set via orch task set"}]` into
  `comments_json`, and `/api/task/F0.T1` answered `"comments": []` next to a
  correctly hydrated `"status": "in-progress"`. The status was right because
  `Hydrate` overlaid it; the comments were wrong because it overlaid nothing
  else.

  Fixed in `Hydrate`, not in the handler: every caller that hydrates gets the
  real comments, and there is still one definition of "what this task actually
  looks like now". Nothing can be lost that way — `Bootstrap` seeds
  `comments_json` FROM the file, so a runtime row's comments start as the
  file's and only grow.

  Python has the same hole for the same reason (`_load_tasks_hydrated`
  replaces `status` and nothing else). Annotated there, fixed here.

  The general shape is the one this lane keeps finding: **a field that moved
  stores, and a reader nobody moved with it.** Same family as the four writers
  that landed without their readers.

- **`go-pdf/fpdf` is the first third-party dependency that is not cobra, yaml,
  sqlite or the MCP SDK.** MIT, v0.9.0, the maintained fork of
  `jung-kurt/gofpdf` (archived by its author in 2021). Approved as a decision
  rather than arrived at by a `go get`: the alternative is writing a PDF
  generator, and a one-page A4 of text and rectangles is about 200 lines of
  flat PDF — which is 200 lines of a format nobody here wants to own.

- **The core PDF fonts are single-byte, and that is a trap a byte-level test
  walks straight past.** Helvetica, Times and Courier take CP1252, not UTF-8.
  Hand fpdf a Go string with an accent in it and the PDF is valid, the text is
  "there", and the page reads `FacturaciÃ³n`. The transcoder is twenty lines;
  the test asserts the ENCODED bytes (`0xF3` for `ó`) rather than the Go
  string, because asserting the string is asserting nothing.

  Three characters are not their own code points in CP1252 and all three are in
  this document's vocabulary: the em dash (0x97), the ellipsis (0x85) and the
  euro sign (0x80).

- **A PDF's page count is a thing the document declares, not a thing you
  count.** The first version of the test counted `/Type /Page` objects, which
  also matches `/Type /Pages` — one character away from reporting a two-page
  document as one. It now reads the `/Count` on the page tree AND
  cross-checks it against the objects, so a malformed document fails rather
  than passing quietly.

  Proven it can fail rather than assumed: with the caps removed and automatic
  page breaks on, 40 milestones render as 2 pages and the assertion catches it.

- **No PDF tooling on this VPS.** No `pdftoppm`, `pdftocairo`, `pdfinfo`,
  `qpdf`, `gs`, `mutool` or ImageMagick; no `pypdf` or `pymupdf` in either
  Python. The rule-29 check is therefore structural and textual rather than
  visual: render the same page uncompressed, read its `/MediaBox`, its
  `/Count`, and every `Tj` string back out in order. Installing poppler on a
  shared VPS is the user's decision, not a lane's.

- **"Never discard an error" is three rules, not one.** Gemini flagged two
  identical-looking `_ = x.Close()` in the same PR, and they wanted different
  answers:

  - `closeDB` in a READ-ONLY command: discarded, with one line saying why. The
    process is exiting and the handle never wrote anything; a close error
    there changes nothing a caller could act on.
  - `closeDB` in a command that WRITES a file: reported to stderr, exit code
    unchanged. The artefact on disk is already correct, so it is not a
    failure — but a database handle that will not close is worth knowing about
    next to a file somebody is about to email.
  - `f.Close` in a TEST: `t.Errorf`. A test that cannot close the file it just
    wrote has not verified what it claims to.

  The syntax is identical in all three; what differs is what the error would
  tell the reader. A blanket "always wrap it" produces noise in the first case
  and a missed signal in the third.

- **A gate that cannot tell must fail closed.** From reviewing G8.2's
  per-project stakeholder token: its resolver did `if err != nil { ok = false }`
  and fell back to the token in config.yaml. That collapses two different
  states — "this project never rotated" (fall back, correct) and "I cannot
  read whether it rotated" (fall back, and a token rotated away after a leak
  becomes valid again). A database error should not resurrect a revoked
  credential, and the symptom of this one is a page that works.

  Same family as the unknown profile in #184: a degradation that reads as
  robustness. The tell is a branch where an ERROR and an ABSENCE are handled
  by the same line.

- **An optional field under a frozen schema, and what made it legal.** G8.4's
  `branding` is the first optional field in a schema-1 snapshot, and the
  schema doc itself warns against "growing an optional field forever". The
  warning is about INCOMPATIBLE changes: this key is absent unless configured,
  a schema-1 viewer that ignores unknown keys renders exactly what it rendered
  before, and nothing existing moved. Bumping to `schema: 2` would have told
  every deployed viewer to refuse a document it can read perfectly.

  The property is tested rather than claimed: with no branding, `Build`
  produces **byte-identical** output to what it produced before the field
  existed. That is also why the field is a POINTER — `omitempty` does not omit
  a struct, so a value type would have grown `"branding":{}` on every
  unbranded document and quietly broken the same property.

- **`TestNoOperatorFields` promised something a deny-list cannot deliver.** The
  schema doc said "a new field this package emits must be added to that list
  on purpose before the test can pass again". It could not: the list catches
  key names already known to be operator-only, so a field nobody thought to
  forbid passed in silence. Added `TestEveryEmittedKeyIsInTheSchema`, an
  allow-list over every key name the document emits, and saw it fail by
  adding a fake `operator_db_path`. The doc now says what is true.

  The general form, and it is the third time this lane has hit it: **a
  deny-list tests the failures you already imagined.** An allow-list tests the
  ones you did not.

- **A PDF embeds a creation date, so "the same document" was never the same
  bytes.** fpdf defaults it to the wall clock, which made two renders of one
  snapshot differ for a reason that has nothing to do with the snapshot — and
  quietly made the byte-identity criterion untestable. The date now comes from
  the snapshot's own `generated_at`, so a report is reproducible: the same
  snapshot yields the same file, and a change in it means a change in the
  project rather than in the minute it was printed.

  Worth remembering for any generated artefact: if it embeds a timestamp,
  "identical output" is a claim you cannot make until you decide where that
  timestamp comes from.

- **PNG and JPEG only, decided by the weakest surface.** `fpdf` draws raster
  images, so an SVG logo would appear on the web and be silently missing from
  the printed page. A white-label feature whose logo reaches two surfaces out
  of three is worse than one that says which formats work — so the config
  refuses an SVG, naming the reason. The format is also typed by its MAGIC
  BYTES rather than its extension, so a PNG named `.jpg` works and a text file
  named `.png` is caught at startup instead of as a broken image three
  surfaces later.
