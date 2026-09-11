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

- ~~**Bug 5 — the budget guardrail never fired on a sqlite project**~~ — **RESOLVED** (fix/budget-reads-sqlite-spend, 2026-09-11). `SqliteSpendLog.record()` wrote only the `spend` table and returned a synthetic JSONL path that was never written; `BudgetGate._entries_since()` read only `state/spend-*.jsonl`. With `state.backend: sqlite` (the default since PR #91) the gate saw zero spend and never blocked a dispatch, while the dashboard (`metrics.read_all_spends`) showed the real number. Reproduced with 5 rows / 750K tokens against a 600-token cap → `can_dispatch` said ok. Found by opus reading `budget.py` for G2.2. Fix: `_entries_since` now reads both sources and de-duplicates rows present in both (a migrated project keeps its JSONL). Regression tests in `test_budget.py`. Go: `internal/budget` reads spend through `state.Backend` (SpendSince), never through files.

## Notes for the Go rewrite

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
