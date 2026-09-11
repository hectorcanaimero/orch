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

## Notes for the Go rewrite

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

- ~~**`state.Backend` has no way to read spend or runs back**~~ — **the read
  side landed in #116**; the CLI wiring is still open. `orch status
  --json` needs `cost_usd` per task and `latest_run` project-wide — Python's
  `build_status_snapshot` gets them from `iter_all_spend()` and `list_runs()`.
  The Backend interface from #107 has `RecordSpend`/`StartRun` (write) but
  nothing symmetric to read them back. `internal/cli/status.go` currently
  hardcodes `cost_usd`/`project_total_usd`/`filtered_total_usd` to zero and
  `latest_run` to `null` — which happens to be the CORRECT output for
  testdata/parity-project (no spend ever recorded, no run ever started), so
  `scripts/parity.sh` passes, but this is a known gap, not a finished
  feature.

  `SpendSince`, `TotalSpendUSD`, `LatestRun` and `Runs` now exist on the
  Backend (#116). What remains is `buildStatusRows`/`buildSnapshot` in
  `internal/cli/project.go` and `status.go` calling them instead of writing
  zeroes. Note for whoever does: the parity fixture has no spend and no runs,
  so `scripts/parity.sh` will keep passing either way — a project with real
  history is the only thing that tells you it worked.

- **`internal/router` doesn't exist yet, so `status`/`tasks` never resolve a
  route.** `backend`/`cli_model`/`tier` in `orch status`/`tasks --json`
  always report the "no route found" fallback (`"?"`, the task's own model
  string, `null`) rather than actually reading `model_router.yaml`. Same
  situation as spend/runs above: this happens to match Python's real output
  on testdata/parity-project (its `model_router.yaml` has no entry for
  `claude/claude-sonnet-4-6` either), so parity holds, but it's a stand-in
  for real router resolution, not a design choice. Revisit
  `buildStatusRows` in `internal/cli/project.go` once `internal/router`
  exists.

- **`classify_failure`'s marker table has three real holes.** All three were
  found by pinning `internal/providers` against captured CLI output (G2.4),
  all three are Python bugs, and all three are ported as-is — the Go tests
  assert the wrong-but-current answer and name the note, so fixing one means
  changing Python, Go and the test together.

  1. **Version drift never classifies as version drift, for claude.** The
     whole point of `FailureClass.VERSION_DRIFT` is to trigger the one
     `fallback_cli_model` retry when a backend rejects the requested model.
     Real `claude 2.1.269` answers a bad `--model` with
     `"It may not exist or you may not have access to it"` on stdout and
     `[claude-code:unrecognized_model]` on stderr. None of the six
     `_VERSION_DRIFT_MARKERS` ("model not found", "unknown model", "no such
     model", "invalid model", "model does not exist", "unsupported model")
     appears in either. So the dispatch lands in `OTHER` and retries the same
     broken model instead of the fallback. Fixture:
     `internal/providers/testdata/claude/2.1.269/unrecognized-model.log`.
     The fix is to add the CLI's actual phrasing (and `api_error_status: 404`
     with an `unrecognized_model` marker) to the table.

  2. **The numeric markers match inside longer numbers.** `"401"`, `"403"`,
     `"429"`, `"500"` are plain substring checks against a haystack that
     includes 2 KB of stdout — and a claude envelope is nothing but numbers.
     `"cache_read_input_tokens":40321` contains `403`, so a truncated
     envelope, which is squarely a `PARSER` failure, classifies as
     `PERMISSION` — terminal, never retried. A `duration_ms` of 1500 or a
     token count of 4291 do the same for `TRANSIENT` and `RATE_LIMIT`. Pinned
     by `TestClaude2_1_269TruncatedEnvelopeMisclassifiesAsPermission`. The fix
     is to anchor those four to an HTTP-status context (`http 429`,
     `status 403`, `"api_error_status": 401`, …) rather than matching bare
     digits.

  3. **Real auth failures do not match the permission markers.** claude sends
     `code: "authentication_error"` with the message `"Invalid API key"`;
     codex sends `"OpenAI API auth expired; run codex auth"`. The table has
     `"authentication failed"` and `"auth error"` — neither is a substring of
     either. So the one failure class that is guaranteed not to fix itself
     gets `OTHER`'s retry-once treatment. Note this interacts with (2): the
     claude case only *looks* right today when a stray `401`/`403` happens to
     appear in the token counts.

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

- **codex's `-o` file is markdown, not JSONL.** The brief (and the older
  design note) describe cost coming from `Σ step_finish.cost` in the `-o`
  file. Since codex 0.148 the `--json` event stream goes to **stdout** and
  `-o` (`--output-last-message`) holds only the final message in markdown;
  `dispatcher.py` says so in a comment above `CodexBackend.wait_result` and
  parses the captured log. `internal/providers/codex.go` does the same. The
  `-o` flag is still passed because the argv is a contract, but nothing reads
  the file.

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
