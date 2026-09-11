# Go migration notes

Bugs found in the Python `orch` while the Go rewrite (see CLAUDE.md → "Migración a Go (en curso)") is in progress get logged here instead of fixed with a new feature or a large refactor in Python. Small, targeted fixes are still fine — this file is for anything that would otherwise tempt scope creep in the version being replaced.

## Porting rules

**When the plan cites an FR and the code does something else with a written
justification, the code wins — and the divergence gets reported.** The plan's
FR numbers can predate the code by several sprints, so a requirement quoted in
a task brief is evidence of what was once intended, not of what ships. Port
the behaviour, keep the reason, and say so in the PR; a spec is not a reason to
reintroduce something a later sprint removed on purpose.

First hit: `fallback_cli_model` (see below), where FR-D-7 asked for a WARN per
substitution and Sprint C had already replaced it with an INFO summary,
because the WARN version spammed the console on every startup.

The inverse also holds. Where the code's behaviour looks like an accident
rather than a decision — no comment, no test, no sprint note — it is a bug to
report, not a contract to preserve. The four found so far (template key
casing, the blank stakeholder URL, `spec_root`, the duplicate `dashboard:`
block) were all of that kind.

## Open notes

- ~~**P0: the budget guardrail did nothing on a sqlite project**~~ — **RESOLVED** (#115, 2026-09-11). Found while reading `budget.py` for
  G2.2 (opus), reproduced before reporting.

  `SqliteSpendLog.record()` (`state/adapters.py:151`) writes only to the
  `spend` table and returns a **synthetic** path — its own comment says
  "not written to". `BudgetGate._entries_since()` (`budget.py:322`) reads
  `state/spend-<date>.jsonl` from disk and never looks at SQLite. With
  `state.backend: sqlite` no such file exists, so the gate reads zero spend
  and never trips.

  ```
  spend log type: SqliteSpendLog
  rows in the sqlite spend table: 5, tokens: 750,000
  spend-*.jsonl files on disk:    NONE
  can_dispatch('claude') -> ok=True     (token_budget 1000, threshold 60% = 600)
  ```

  Worse: `metrics.read_all_spends` DOES read both sources, so the dashboard
  shows the budget filling while the gate keeps dispatching.

  Scope: sqlite was opt-in until PR #91 made it the default, so every project
  scaffolded since has a guardrail that announces itself and does nothing.
  The bug predates that (sprint B, when `SqliteSpendLog` landed); #91 turned
  it from a rare case into the common one.

  Fix (#115): `_entries_since` reads both sources and de-duplicates rows
  present in both, so a migrated project that still has its JSONL is not
  double-counted. Regression tests in `test_budget.py`. It filters the window
  with `_parse_ts` rather than comparing strings — for the reason in the
  timestamp note below, which was found independently while writing
  `state.SpendSince`.

  **This cannot recur in Go**: `internal/budget` reads spend through
  `state.Backend.SpendSince`, and SQLite is the only place a spend row lives.

- ~~Stale `jinja2` mentions~~ — **RESOLVED** (g0/sonnet-cleanup): `orchestrator/orch.py`'s
  dashboard-missing-deps hint no longer tells the user to install `jinja2 >= 3.1`, and
  `orchestrator/dashboard/__init__.py`'s module docstring now describes FastAPI serving
  the embedded React SPA instead of calling out `jinja2` as a "heavy import".

- ~~**Frontend leftovers of `board_url`**~~ — **RESOLVED** (g0/sonnet-cleanup, 2026-09-11):
  `frontend/src/lib/types.ts:202`'s `board_url?` field and `frontend/env.example`'s
  `dashboard.board_url` comment are both removed.

- ~~**Project templates wrote snake_case task keys `Task.from_json` doesn't
  read**~~ — **RESOLVED** (g0/sonnet-templates, 2026-09-11): found by opus during
  G0.3 (PR #95). All four `orchestrator/templates/projects/*/tasks.json.tmpl`
  used `estimate_hours` / `spec_ref`, but `Task.from_json` (orchestrator/models.py)
  only reads `estimateHours` / `specRef` — every task scaffolded from a template
  silently got `estimate_hours=0.0` and `spec_ref=""`, so the dashboard ETA showed
  "—" and the dispatch prompt dropped the "Spec ref (READ FIRST)" line for every
  templated project. Renamed the 32 occurrences across the 4 templates; added a
  parametrized regression test (`test_template_tasks_are_readable_by_task_from_json`
  in `orchestrator/tests/test_init.py`) that scaffolds each template and asserts
  every task has `estimate_hours > 0` and a non-empty `spec_ref`.

- ~~**P0: the stakeholder URL was unusable in a browser**~~ — **RESOLVED**
  (g0/opus-stakeholder-url, 2026-09-11): `TokenAuthMiddleware` returned 401 for
  `index.html` and `/assets/*` when no token was present, so a stakeholder URL
  with `?token=` loaded the SPA shell but the JS/CSS asset requests (which don't
  carry the query param) 401'd and the page stayed blank — the token-entry form
  inside the SPA was unreachable because the SPA itself never rendered. Fixed in
  two halves. Server: `_is_public_shell` in `orchestrator/dashboard/middleware.py`
  exempts requests that resolve to the SPA StaticFiles mount, plus the
  `/assets/` prefix; `/api/` is a hard never-public prefix on top of route
  resolution so a method-fuzz or an unknown `/api/*` path can't fall through to
  the mount. SPA: `adoptTokenFromQuery()` (`frontend/src/hooks/useAuth.ts`, called
  from `main.tsx` before the first render) moves `?token=` into `localStorage`
  and scrubs it out of the address bar with `history.replaceState`.
  `orchestrator/tests/test_dashboard_security_public_shell.py` sweeps the live
  route table and fails if any data route ever escapes the token gate.

- ~~**`spec_root` defaulted to a path from orch's own repo**~~ — **RESOLVED**
  (g0/opus-spec-root, 2026-09-11): found while generating the config goldens
  for G1.2. `prompt_builder.DEFAULT_SPEC_ROOT` was `docs/rewrite-plan` — a
  leftover from orch's early layout — while the packaged `config.yaml` and the
  `orch init` wizard both said `specs`. None of the four templates set the key,
  so every templated project resolved it through `_apply_defaults` to
  `docs/rewrite-plan`. `_render_spec_ref` composes `{spec_root}/{spec_ref}`, so
  each dispatched agent was told to read
  `docs/rewrite-plan/specs/f0-foundation.md#T1` — a path in neither the user's
  project nor anywhere else. The default is now `specs`, the four templates pin
  `spec_root: specs` explicitly so the written config says it out loud, and
  `test_init.py` asserts that a prompt rendered from a templated task points at
  the project's own `specs/`.

- ~~**Bug 4: `dashboard:` defined twice in config.yaml, second block silently
  won**~~ — **RESOLVED** (g0/sonnet-config-dup, 2026-09-11): found by opus while
  porting config for G1.2 (PR #105). `orchestrator/config.yaml` had two
  top-level `dashboard:` keys — Sprint E-2/E-3's (`show_spend_to_stakeholder`,
  `summary_language`) and Sprint H-2's (`kanban`, `tunnel`), added further
  down without noticing the first. PyYAML's default behavior on a duplicate
  mapping key is to silently keep only the LAST one, so
  `show_spend_to_stakeholder`/`summary_language` had had zero effect since H-2
  shipped — editing them in the packaged config, or in a project's own
  `config.yaml`, did nothing, silently. Merged into one `dashboard:` block
  carrying all four keys and every original comment. Checked the four
  `orchestrator/templates/projects/*/config.yaml.tmpl` and `init_cmd.py` for
  the same pattern — none repeat it. Added `_DuplicateKeyWarningLoader` in
  `orchestrator/config_loader.py` (a `yaml.SafeLoader` subclass overriding
  `construct_mapping`) so a duplicate key at ANY nesting level now prints a
  warning naming the key and line — behavior unchanged (still last-wins, so
  an already-written project config doesn't suddenly break), just no longer
  silent. Tests in `test_config_loader.py`: the packaged config has all four
  `dashboard` keys, has no duplicate top-level key at all (parses the raw
  node tree via `yaml.compose`, not the constructed dict, so a future
  duplicate can't hide behind last-wins), and the warning loader fires (or
  stays silent) exactly when expected.

- ~~**Bugs 6, 7, 8 — `classify_failure` markers against real claude 2.1.269 output**~~ — **RESOLVED in Python** (fix/classify-failure-markers, 2026-09-11); the Go mirror in `internal/providers` follows in a separate PR with the same fixtures (until then its tests deliberately assert the old answers). Found by opus-2 capturing real CLI bytes for G2.4. (6) None of `_VERSION_DRIFT_MARKERS` matched claude's real rejection text "It may not exist or you may not have access to it" nor its `[claude-code:unrecognized_model]` line, so the `fallback_cli_model` retry never fired for the one case it exists for — markers added. (7) Numeric status codes `401/403/429/500..504` were bare substrings against a haystack that includes 2 KB of stdout; a claude envelope is full of numbers (`"cacheReadInputTokens":40321` contains `403`), so a truncated envelope — PARSER, retryable — classified PERMISSION, terminal. Codes now match only as standalone numbers (`_has_status_code`). (8) Real auth failures spell it `authentication_error` / `auth expired`, neither a substring of the old markers — added. Fixtures: `orchestrator/tests/fixtures/dispatcher/claude-2.1.269/`. Go rule: same three changes in `providers.Classify`, same fixtures. **Go mirror done in the same PR as the port (#117)**, so neither binary ever carried a different answer: `providers.Classify` gained the same two drift markers and three auth spellings, and the status codes moved behind `hasStatusCode`. Go's RE2 has no lookbehind, so Python's `(?<![0-9.])<code>(?![0-9.])` is hand-rolled there — `TestHasStatusCode`'s table was checked case-by-case against Python's own `_has_status_code`, and the obvious regexp rewrite `(^|[^0-9.])code([^0-9.]|$)` is wrong because it consumes the separator and would miss the second code in "429 429".
- ~~**Bug 9 — `orch.py` emitted seven event types its own validator rejected**~~ — **RESOLVED** (fix/event-types-ci, 2026-09-11). Found by opus-2 while aligning `internal/state` event types for G2.5. `EventLog.emit` raises `ValueError` for any type outside `EVENT_TYPES`, and the Sprint F-4 / G-1 PR+CI path emits `pr_created`, `ci_redispatch`, `ci_success`, `pr_auto_merged`, `pr_auto_merge_failed`, `ci_failure_retry`, `ci_blocked` — none declared. `pr_created` was swallowed by a broad `except` (logged as "set_task_pr failed"); the other six crashed the CI poller. Never caught because `test_orch.py` drives that path with a fake `emit` that does not validate. Fix: the seven are declared; `test_event_types_emitted.py` walks every real emitter with `ast` and fails on an undeclared literal. Go: `internal/state` `eventTypes` is Python's 14 plus these 7 (21 total); `exit_ok`/`exit_err`/`resume_reset`/`dry_run_planned` from the plan sketch are gone and the engine emits `success`/`fail`.
- **Bug 11 — `burndown_by_day` filters on an event type nothing emits.** Found
  while aligning Go's `eventTypes` with Python's (the same sweep as bug 9).
  `dashboard/metrics.py:609` keeps only events whose `event_type == "exit_ok"`.
  `exit_ok` is not an event type: it comes from the superseded FR-STATE-7 list
  (`docs/history/spec.md:107`, dated 2026-08-19) and no version of orch has
  ever emitted it. The function therefore returns `done_count: 0` for every
  day, on any real database.

  Severity is low and that is the interesting part: `server.py` imports it and
  never calls it, and it has no test. Nothing is broken today because nothing
  uses it — but the next person to wire a burndown chart gets a flat line and
  no error, and goes looking in the data.

  Not fixed (migration rule: annotate, do not add features), unless someone
  wires that chart before the gate. If Go ports it, the filter is `success` —
  which is the point of bug 9's alignment: with one vocabulary on both sides,
  this class of bug has nowhere to live.

- ~~**Bug 12: every templated project sent its agents to `specs/specs/...`**~~ —
  **RESOLVED** (this PR). Found rendering the Python goldens for G2.3 (prompt).

  The four `tasks.json.tmpl` files carried `"specRef": "specs/f0-foundation.md#T1"`,
  and `spec_root` defaults to `specs`, and `prompt_builder` composes
  `f"{spec_root}/{spec_ref}"`. So every task of every template rendered:

  ```
  Spec ref (READ FIRST): specs/specs/f0-foundation.md#T1
  ```

  `orch init` creates `<project>/specs/` and tells the user to write
  `<project>/specs/f0-foundation.md` (`init_cmd.py:318, 429`). The prompt sent
  the agent one directory deeper, on the line that says READ FIRST.

  The `#T1` anchor is a red herring — it is concatenated with no special
  handling. What it does is disguise the line: `specs/specs/f0-foundation.md#T1`
  reads like a well-formed path with a section anchor and the eye slides past
  the doubled segment.

  It is a consequence of PR #102, which changed `DEFAULT_SPEC_ROOT` from
  `docs/rewrite-plan` to `specs`. Before that the same templates rendered
  `docs/rewrite-plan/specs/f0-foundation.md#T1` — equally broken, differently
  shaped — so the redundant prefix had been in the templates since they were
  written. #102 changed the default without checking the other side of the
  concatenation.

  **The test was complicit**, and that is the part worth keeping. #102 added
  `test_templated_task_prompt_points_at_the_projects_own_specs`, whose
  assertion was:

  ```python
  assert f"Spec ref (READ FIRST): specs/{task.spec_ref.split('specs/')[-1]}" in text \
      or "Spec ref (READ FIRST): specs/" in text
  ```

  The second clause is satisfied by `specs/specs/...`, and the first splits the
  `specs/` prefix back out of `spec_ref` — accommodating the duplication rather
  than catching it. A test written to pass. It now asserts the whole resolved
  path, runs over all four templates and every task in them, and checks that
  the file the prompt names is the file the wizard tells the user to write.
  Verified failing against the unfixed templates before the fix landed.

  This is the fourth template bug found during the migration (snake_case keys,
  the `spec_root` default, `dashboard:` twice, this) and all four came out of
  building something against the code rather than reading it.

## Notes for the Go rewrite

- **A run's three tallies degrade to zero; Python's `list_runs` raises.**
  `state.Run` exposes `CompletedCount`/`BlockedCount`/`DeferredCount` from the
  `completed_json`/`blocked_json`/`deferred_json` columns. Python does
  `json.loads(col or "[]")` with nothing around it, so one column holding text
  that is not a JSON array takes `orch status` down entirely. Go counts that
  column as 0 and reads the other two.

  The line this draws is worth keeping: these three feed no decision — they are
  rendered by `orch status --json` and nothing reads the ids back — so trading
  "a tally says 0" for "orch cannot tell you anything about this project" is the
  wrong way round. Where the same lists DO feed a decision (`blocked_json` in
  the reconciler, `sqlite_backend.py:1129`) the error has to surface, and that
  path is not this one.

  Also worth knowing before writing a test here: the parity fixture's three
  arrays are all empty, so an implementation that always answered 0 passes both
  the fixture and `scripts/parity.sh`. The rows that prove the parsing are built
  by hand in `reads_test.go`.

- **The budget gate returns its read errors; Python swallows them.** A
  deliberate divergence, agreed with the coordinator, recorded so neither side
  looks like a bug to the other.

  `BudgetGate._sqlite_entries_since` catches `sqlite3.Error` and yields
  nothing, on the reasoning that "the gate must never crash a run because the
  database is briefly locked". The effect is that a failed read is
  indistinguishable from zero spend, and zero spend always means "go ahead" —
  the same shape as bug 5, where the gate read the wrong source, saw nothing,
  and never fired. The symptom was visible (the dashboard showed the budget
  filling) but the cause was not.

  `budget.Gate` returns the error from `CanDispatch`, `AllCapped`,
  `EarliestReset` and `Snapshot`. The run loop still continues — fail-open is
  right for a guardrail that cannot read its own data — but it does so at the
  call site, with a log line next to the decision it is affecting, instead of
  inside the gate where nothing can see it. Python keeps #115's behaviour: it
  is the existing pattern there, and changing it would be a feature.

  Two `test_budget.py` cases therefore have no Go counterpart, and their
  absence is the point rather than a gap:
  `test_sqlite_source_degrades_to_nothing_on_unreadable_db` asserts the
  swallowing, and `test_can_dispatch_dedups_rows_present_in_jsonl_and_sqlite`
  de-duplicates between two spend sources. Go has one source, so there is
  nothing to de-duplicate and nowhere for a row to hide.

- **The event-type set comes from Python, not from the plan.** Found when
  `internal/state` was holding a set of 11 taken from FR-STATE-7 in the
  artifact: `exit_ok`, `exit_err`, `resume_reset`, `dry_run_planned`. None of
  those four exist. The repo already said so — `docs/history/spec.md:107`
  records that the names were harmonised with the implementation on
  2026-08-19, `success`/`fail` replacing `exit_ok`/`exit_err`, `resume_revert`
  replacing `resume_reset`, and `dry_run_planned` removed because dry-run
  writes nothing to disk. `verify-report.md` flagged the same drift as
  FR-STATE-7 PARTIAL. The stale list was read; the correction next to it was
  not.

  The real set is 21: the 14 in `EVENT_TYPES`
  (`orchestrator/state/file_backend.py`) plus 7 that `orch.py` emits and that
  tuple does not list, so Python raises `ValueError` on each of them today
  (`pr_created`, `ci_redispatch`, `ci_success`, `pr_auto_merged`,
  `pr_auto_merge_failed`, `ci_failure_retry`, `ci_blocked` — a separate Python
  fix is in flight). Go accepts all 21, because a closed set is only worth
  having if it is the *same* closed set on both sides: a type Go rejects and
  Python writes leaves a hole in the run history that nothing reports.

  Held by `TestEventTypesMatchPython` against `testdata/event-types.json`,
  which is exported from the Python tree rather than transcribed.

  **The rule this is an instance of:** where the artifact and the code
  disagree, the code wins — and `docs/history/` is code for this purpose,
  because it records the decisions the artifact was never updated with. The
  four bogus names had been dead for three weeks before anyone wrote them into
  Go.


- **Timestamps in `orch.db` are stored in two forms.** Not a bug to fix, a
  fact to code against, found writing `state.SpendSince`. The same database
  written by one Python version holds `2026-09-01T10:30:00+00:00` in
  `spend.ts` and `2026-09-11T18:32:54Z` in `runs.started_at`, because
  different call sites use `datetime.isoformat()` and `strftime("...Z")`.
  Python's `budget._parse_ts` already accepts both "for robustness with older
  rows".

  The consequence for anyone writing a query: **a rolling window cannot be
  `WHERE ts >= ?` in SQL.** Lexically `+` (0x2B) sorts before `Z` (0x5A), so a
  cutoff formatted one way silently drops rows stored the other way — which
  under-reports spend, which is how a guardrail stops guarding with no error
  anywhere. `state.parseTS` handles both and the filtering happens in Go. The
  same applies to `ORDER BY started_at`: Python sorts runs lexically in SQL
  and would mis-order such a database; Go sorts by parsed time.

- **`orch stop` reads runs from SQLite in Go, not `state/run-*.json`.**
  Python's `_run_stop_subcommand` finds the newest running orch by globbing
  `state/run-*.json` for a live `parent_pid` and sending it SIGTERM. That
  file doesn't exist in Go — no engine writes it, and ADR-G4 dropped the
  file backend entirely. Decision (orch-98, during G3.6 scoping,
  2026-09-11): when `stop` lands in **G3.3** (once the engine exists and
  actually starts runs), it reads the `runs` table instead, via
  `Backend.Runs`/`Backend.LatestRun` (#116, opus) — `parent_pid` lives on
  that row already. `stop` itself is out of scope for G3.6, which only
  covers `task`/`task-status`/`reset` (the three that route through
  `Backend.Transition`).

- **The graph package: three gaps between the plan and the Python tree.**
  Found while writing `internal/graph` (G1.6). They are listed together
  because they are one discovery seen from three sides, and because the
  artifact needs a single edit rather than three.

  1. **Python emits HTML, not DOT.** `graph.py` writes a self-contained page
     with inline SVG (`build_html`, `_svg_nodes`, `_svg_edges`). A search for
     `digraph|graphviz|--dot` across `orchestrator/` returns nothing.
  2. **So Go's DOT replaces it rather than porting it.** `orch graph` keeps
     the subcommand and changes the output: DOT on stdout, `--out file.dot`,
     `--only` kept. The visual DAG lives in the dashboard (GraphPage +
     `/api/graph`), so the HTML generator is dropped. This is a **visible
     contract change** for anyone scripting `orch graph --out plan.html` and
     belongs in the artifact's "Cambia o desaparece" column.
  3. **Python has no topological sort either.** `TaskQueue.all()` sorts by
     `(phase, id)` for display; `TaskQueue.ready()` picks whatever is
     unblocked right now. Nothing produces a dependency order, so
     `graph.TopoOrder` is new as well.

  **What this means for the parity harness.** Two of the four capabilities in
  `internal/graph` have no Python counterpart, so neither DOT nor the
  topological order can be diffed between the binaries. Parity for this
  package is measured on the three things that exist on both sides:
  `validate` messages, the cycle set, and the `(phase, id)` display order.
  DOT and `TopoOrder` carry Go-generated goldens and their own tests instead.

  **One consequence worth keeping.** Because `validate` output IS diffed,
  its text is a contract down to the quoting: Python renders ids with
  `{x!r}`, i.e. `'NOPE.T9'`, and Go's `%q` would write `"NOPE.T9"` and turn
  every matching line into a permanent diff. `graph.pyQuote` exists for that
  and nothing else.

- **`fallback_cli_model` is announced, not warned about.** The G2.1 brief
  asked for "a WARN per substitution", quoting FR-D-7. Sprint C deliberately
  revised that: `orch.py:_warn_fallback_routes` says the original spec
  "produces a double-emit that spams the console every startup", because the
  actual substitution happens later in the reap loop on a version-drift
  failure, not at startup. What Python does now is one INFO summary line, or
  one INFO per route under `-v`. `router.Fallbacks()` returns the data and
  leaves the level to the caller, so the CLI can reproduce the current
  behaviour rather than the superseded spec. Worth knowing that the FR numbers
  in the plan can predate the code.

- **`internal/dashboard` — where the auth gate belongs.** Put the token check on
  the **data** routes (`/api/*`, `/stakeholder/summary`, `/snapshot`,
  `/logs/stream`), never on the static surface. The embedded SPA's `index.html`
  and its `/assets/*` bundle must be served **without** a token: a browser
  cannot attach a token to the `<script src>` requests the shell makes, so
  gating them means the shared URL renders a blank page and the token form is
  unreachable. Carry over the two invariants the Python fix pins: an unknown
  path under `/api/` is never public (don't let a catch-all static handler win
  it), and a wrong method on a data route resolves to that route, not to the
  static fallback. The equivalent of the route-table sweep test is cheap in Go
  and worth keeping.

- ~~**`state.Backend` has no way to read spend or runs back**~~ — **RESOLVED**:
  the read side landed in #116 (`SpendSince`/`TotalSpendUSD`/`LatestRun`/
  `Runs`), and the CLI wiring (`cost_usd`/`project_total_usd`/
  `filtered_total_usd`/`latest_run` in `orch status`/`tasks --json`) landed in
  the PR that added this line. Verified two ways: `internal/cli/testdata/
  script/status-real.txtar` against `internal/state/testdata/
  orch-py-0.11.0.db` (1.42 USD spend, one real run), and a direct call to
  Python's `build_status_snapshot` against the same fixture db, whose output
  matches Go's byte-for-byte on every field Go produces (see the "latest_run
  is a partial shape" note below for the one exception). `testdata/
  parity-project` has no spend and no runs, so it was never going to catch a
  regression here either way — confirmed by running Python's own
  `build_status_snapshot` against it: it genuinely produces `"project_total_
  usd": 0` (bare int, Python's `sum()`-of-nothing quirk), `"filtered_total_
  usd": 0.0`, and `"latest_run": null`, which is exactly what Go already
  emitted before this fix. No new golden was needed for that fixture.

- ~~**`internal/router` doesn't exist yet, so `status`/`tasks` never resolve a
  route.**~~ — **RESOLVED**: `internal/router` landed in #113, and
  `buildStatusRows` (`internal/cli/project.go`) now resolves real `backend`/
  `cli_model`/`tier` through it, loaded tolerantly (`loadRouterTolerant`) so a
  missing/broken `model_router.yaml` degrades those three columns instead of
  failing the whole command, matching Python's own `try/except` around
  `load_router` in `build_status_snapshot`. Verified against the same
  `status-real.txtar` fixture, which ships a `model_router.yaml` routing both
  models the fixture db's tasks use.

- **`latest_run` is a partial shape: Go's `state.Run` doesn't carry
  everything Python's `list_runs()` dict does.** Confirmed by diffing real
  output side by side (`orch-py-0.11.0.db`, see above). Two different kinds
  of gap, not one:
  - `run_file`/`events_file` (literal `f"run-{run_id}.json"` /
    `f"events-{run_id}.jsonl"` paths) are a file-backend naming convention
    that's meaningless now that ADR-G4 dropped that backend entirely, even
    though Python still emits it unconditionally for a sqlite project. This
    one really is discarded by design — nothing to port.
  - `completed_count`/`blocked_count`/`deferred_count` are **not** discarded
    by design, just not wired yet: they come from the `completed_json`/
    `blocked_json`/`deferred_json` columns already sitting on SQLite's
    `runs` table (`state.Backend` just doesn't read them back). This is
    reachable — `internal/state` (opus's package) needs to add these three
    counts to `Run`/`LatestRun`, then `internal/cli/status.go`'s `runJSON`
    picks them up. Tracked as a small follow-up PR, not closed here.

  `internal/cli/status.go`'s `runJSON` carries every field `state.Run` has
  today (`run_id`/`started_at`/`updated_at`/`mode`/`status`/`parent_pid`/
  `in_flight_count`, same order Python uses for the fields it shares) and
  stops there — it cannot fake the three counts from what it already has.

- **`internal/atomize` (G4.3) — project templates have no markdown specs to
  atomize.** The G4 brief said to generate goldens by atomizing the specs
  under `orchestrator/templates/projects/*/`; those directories only ship a
  pre-built `tasks.json.tmpl`, never a spec `.md` (there is nothing for the
  atomizer to consume there). The goldens instead come from the real specs
  `orchestrator/atomize.py`'s own test suite already uses —
  `sample_spec.md` and `orch_spec_output.md`, copied verbatim into
  `internal/atomize/testdata/specs/` — plus two synthetic specs
  (`merge_a.md`/`merge_b.md`) written for this port to exercise merge/
  orphan/dep-warning/cross-file-duplicate-ID behavior the two real fixtures
  don't touch. `internal/atomize/testdata/make-goldens.py` documents this.

- **A preexisting Python bug in `extract_frontmatter` is ported as-is, not
  fixed.** When a spec's frontmatter block's top-level YAML value isn't a
  mapping (a list, a scalar, ...), `orchestrator/atomize.py` builds a
  warning on one `Frontmatter` object and then returns a *different*,
  freshly constructed `Frontmatter(present=False)` — discarding the warning
  it just built. No test exercises this path, so it has shipped silently.
  `internal/atomize/frontmatter.go`'s `extractFrontmatter` reproduces the
  same observable behavior (treated as absent frontmatter, zero warnings)
  rather than "fixing" it, per the migration rule that Go ports match
  Python's actual behavior; a real fix belongs in a small Python PR of its
  own; on the Go side this pins `TestExtractFrontmatterTopLevelNotAMapIsDiscardedSilently`.

- **The atomizer's merge always emits every optional `tasks.json` field, by
  construction rather than by touching `model.Task`'s private state.**
  `model.Task.present` (unexported) exists so `LoadTasksFile`→`SaveTasksFile`
  reproduces a row's exact source key set — the opposite of what the
  Python atomizer does, where `merge_tasks`'s output dict for a touched row
  always ends up with the full field set (`dict(old)` plus every changed
  declarative key, plus the three runtime keys the loop always writes).
  `internal/atomize` can't reach across the package boundary to flip
  `present` anyway, so `mergeRow` (merge.go) sidesteps the question:
  every merged/new row is a fresh `model.Task{...}` struct literal, which
  has a nil `present` map and therefore always marshals every optional
  field — matching Python's real output without needing model-package
  changes. The only place this could theoretically diverge is a hand-edited
  `tasks.json` missing an optional key entirely on a row that then goes
  through a no-op merge; no shipped project's `tasks.json` looks like that.

- **`internal/providers` — where the ported interface departs from the plan,
  and why.** The G2.4 brief sketched `Argv(prompt, route, budgetUSD) []string`,
  `Parse(stdout []byte) (Result, error)` and a per-provider
  `Classify(exit int, stderr []byte)`. `orchestrator/dispatcher.py` does none
  of those three things, and the code won:

  - **`Argv(Request)`.** The inputs are not just a prompt, a route and a
    budget. codex needs the resolved `-o` artefact path (Python writes a
    `__OUTPUT__<task id>` placeholder into the argv and has `spawn` rewrite it
    in place); opencode needs an absolute `--dir`; gemini and agy need the
    prompt text itself, while claude and codex take it on stdin. A struct of
    plain values carries all of that and keeps `Argv` pure — which also moves
    claude's `--session-id` uuid out of `build_cmd`, so nothing has to dig it
    back out of the argv afterwards the way `_extract_session_id` does.
  - **`Parse(exitCode, output) Result`, no error.** The exit code is a third
    of claude's success predicate (`exit == 0 && !is_error && subtype ==
    "success"`), so a parser that never sees it cannot decide success. And a
    parse failure is not an error return: Python builds a `DispatchResult`
    whose `error_message` is what `classify_failure` maps to `PARSER`, and
    still extracts cost from output that failed, because a failed dispatch has
    usually already been paid for.
  - **`Classify` is package-level, over a `Result`.** In Python it is one
    module-level pure function every backend shares. Per-provider copies would
    drift; and it reads `error_message` plus the stdout tail, not stderr — the
    two highest-priority classes, `ID_SPOOF` and `TIMEOUT`, are set by orch and
    never printed by any CLI.

- **`pyFloat` now exists twice.** `internal/state/pyfloat.go` needs CPython's
  `repr(float)` for the spend dedup preimage; `internal/providers/pyjson.go`
  needs it because claude's `--max-budget-usd` value is built as
  `str(budget)`. Checklist rule 14 keeps `providers` off everything but
  `internal/model`, so the shared home would have to be `internal/model`
  itself — a move across another lane's package mid-migration. Worth doing
  once the lanes converge; both copies carry the same golden table so a drift
  between them fails a test.

- **Python's `extract_cost` can crash the reap loop on malformed output.**
  `float(...)` / `int(...)` raise `ValueError` on an uncoercible value, and
  `ClaudeBackend.extract_cost` catches nothing, so a CLI that writes
  `"input_tokens": "n/a"` takes down `wait_result` rather than costing a wrong
  number. (codex's and opencode's summers do catch it.) Go's `toFloat`/`toInt`
  return 0 instead — a deliberate divergence, identical on well-formed output.

- **`internal/worktree` (G4.1) — real git repos instead of mocked
  subprocess, and a documented-not-enforced caller contract.** Two notes:

  1. `orchestrator/tests/test_worktree.py` mocks every `subprocess.run` call
     and asserts on the exact argv Python built. Go has no equivalent of
     `unittest.mock.patch("subprocess.run")` that's worth reaching for here,
     and asserting on argv shape is a weaker test than asserting on outcome
     anyway — so `internal/worktree`'s tests run every case (including the
     G4.1 brief's named scenario, a `git worktree add -b` that leaves an
     orphan branch behind after a partial failure) against real temporary
     git repositories instead. `TestCreatePurgesOrphanBranchLeftByAPartialFailure`
     is the one that matters: it hand-creates the orphan branch ref with no
     worktree directory (the exact state a partial `add -b` leaves) and
     asserts the next `Create` succeeds.
  2. `orchestrator/tests/test_orch_worktree.py` is NOT a `WorktreeManager`
     test at all — every `WorktreeManager` call in it is a `MagicMock`, and
     what it actually tests is `orchestrator/orch.py`'s `_reap_once`/
     `_install_sigint` wiring: call `commit_pending` → `push` → `remove` in
     that order, skip `push` on failure, never let a `push` failure skip
     `remove` or downgrade a successful task, and call `remove_all` after
     the drain wait rather than from the SIGTERM handler. `internal/engine`
     (the `_reap_once` equivalent) doesn't exist yet, so there is nothing to
     port this test file's cases *into* — the ordering is a contract for
     whoever writes `internal/engine` against `internal/worktree`, not
     something `internal/worktree` itself can test. It's written down as a
     "Caller contract" section in `worktree.go`'s package doc comment so it
     isn't lost between now and G-whatever-does-the-engine.

- **`internal/vcs` (G4.2) — "skipped" is not a distinct CI state in the
  Python source, despite the brief.** The G4 coordination brief for this
  package said to port "the states Python distinguishes (including
  `skipped`, which is a CI-run state, not a task state)". Read
  `orchestrator/vcs/github.py`'s and `gitlab.py`'s `_CI_STATE_MAP`: both map
  a `skipped` conclusion/status to `"success"`, exactly like `completed`/
  `neutral` (GitHub) or a normal green pipeline (GitLab) — `get_ci_status`
  only ever returns `success`/`failure`/`pending`, three values, never a
  fourth `skipped`. `internal/vcs.CIState` ports that three-value reality;
  `TestGitHubCIStatusSkippedConclusionMapsToSuccess` and its GitLab
  counterpart pin it. (The "skipped" the brief may have had in mind is
  likely the CI-run status enum bug 9 declared in the state layer — see
  PR #121 — a different, unrelated concept from what this package returns.)

- **`internal/vcs` deliberately does not mirror Python's silent
  degradation on a missing/unauthenticated CLI — this was an explicit ask
  in the brief, not a unilateral improvement, but the mechanism is worth
  recording.** Python's `create_pr`/`get_ci_status`/`merge_pr` return a
  zero value (`None`/`"pending"`/`False`) on ANY subprocess failure,
  indistinguishable from "CI just hasn't finished yet". Every exported
  method here checks `exec.LookPath` first (cheap, no subprocess) and
  returns a typed `*CLIError` if gh/glab isn't installed — always, on every
  call, since a missing binary is a permanent, structural problem no
  amount of polling fixes. `CreatePR`/`MergePR` additionally run
  `<binary> auth status` on failure (one extra subprocess call, acceptable
  since both are called once per task, never in the poll loop) to tell
  "not authenticated" apart from an ordinary business failure (PR already
  exists, not mergeable yet). `CIStatus`/`CILogs` — the methods
  `internal/engine`'s poll loop will call on a timer — deliberately do NOT
  make that extra auth-check call on every tick, matching Python's cheap
  steady-state failure behavior there.

- **`MergePR(prURL string, squash, auto bool) error` parametrizes flags
  Python hardcodes — also an explicit brief ask, not a spec deviation, but
  it changes GitLab's actual CLI surface.** Python's `GitHubProvider.merge_pr`
  always passes `--squash --auto`; `GitLabProvider.merge_pr` always passes
  `--squash --yes` and has no auto-merge equivalent at all. Making `squash`/
  `auto` real parameters (so a caller can opt into auto-merge rather than
  always getting it) means GitLab's `auto=true` needed a real flag to map
  to: `--when-pipeline-succeeds` is glab's actual "merge once CI passes"
  flag (there is no `--auto` in glab's `mr merge`). This is new behavior
  for the GitLab side, not a port of anything Python does today — flagged
  here per the migration rule so it's a documented decision, not a silent
  divergence, if a GitLab project ever exercises it.

- **`cmd.StdoutPipe` plus a copy goroutine loses the tail of a child's
  output.** Found in `internal/engine` (G2.5), as a test that failed about one
  full `-race` run in three and passed every time in isolation.

  `exec.Cmd.Wait` closes a pipe created by `StdoutPipe` as soon as the child
  exits — its own doc says it is "incorrect to call Wait before all reads from
  the pipe have completed". Supervising a child means calling `Wait`
  concurrently with reading, so a child that exits while its last writes are
  still in flight simply loses them. The symptom was a claude envelope that
  arrived truncated and classified as `PARSER`: a real dispatch would have
  been marked failed, retried, and paid for twice.

  The fix is what `dispatcher.py` was doing all along: pass the log file
  itself as `stdout` and `stderr` (`Popen(stdout=log_fh, stderr=STDOUT)`), so
  the child writes into the fd directly and the parent never copies anything.
  There is then no pipe to truncate, no pipe buffer to fill, and no buffer
  size to tune — which also answers the G2.5 brief's "stream stdout to the
  parser with a 1 MiB bufio buffer": there is no parent-side stream to buffer,
  and `Parse` is a pure function over the finished log, exactly as Python
  parses `_read_log`'s result once at the end.

  Worth recording as a porting lesson rather than a Python bug: the Go-native
  design looked more sophisticated and was simply wrong, and the Python it was
  replacing had the right shape already.

- **`internal/engine` — the dispatch timeout's multiplier, and the one place
  it diverges.** `orch.py::_timeout_for` computes
  `estimate_hours * multiplier * 3600` and floors a non-positive result at
  60s. It reads the multiplier as
  `float(cfg.get("default_timeout_multiplier", 1.5) or 1.5)`, so a missing key
  and an explicit `0` both become 1.5 — `0 or 1.5` is 1.5. A **negative**
  value is the one that survives: `-2 or 1.5` is -2, `seconds` goes negative,
  and an 8-hour task lands on the 60-second floor. `engine.TimeoutFor` treats
  any non-positive multiplier as "not configured" and uses 1.5, so that task
  keeps its real timeout. Reachable only by writing a negative number into
  `config.yaml` on purpose; pinned by a test that says so.
