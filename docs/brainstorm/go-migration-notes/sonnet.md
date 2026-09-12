# Go migration notes — lane sonnet

Append-only. One entry per finding, newest last. Format and numbering follow `docs/brainstorm/go-migration-notes.md`; that file is now history and is not edited after 2026-09-12.

- **`orch router validate`/`orch router add-missing` land in G1.6.**
  `router validate` is new-in-Go: no `_run_router_subcommand` equivalent in
  Python, just a thin CLI wrapper over `internal/router.Router.Validate`
  (already enforced before dispatch, AS-06) so an operator can ask the
  route-coverage question without a full `orch validate` run or a live
  dispatch. `router add-missing` ports `_run_router_add_missing_subcommand`
  faithfully — same plan/confirm/apply flow, same exit codes, plan and
  abort text verified byte-for-byte against a real Python run.

- ~~**Bug 15 — `internal/router.AddMissing` silently corrupts a router file
  that starts as a bare `{}`.**~~ — **RESOLVED** (#144, same day). Found
  while wiring `router add-missing` against `testdata/parity-project`,
  whose `model_router.yaml` is exactly `{}` (the shape every project
  scaffolded before #141 has). `{}` is a complete YAML document;
  `AddMissing` appended a block mapping after it with no `---` separator,
  which is invalid YAML — Python's equivalent bug (#14, fixed in #141) at
  least raised loudly. Go's `Load()` on the resulting file returned an
  **empty router with no error**: `AddMissing` reported success, the file
  visibly contained the new routes, and re-loading it silently discarded
  all of them — worse than Python's crash, because nothing anywhere said a
  problem existed. Reported to `internal/router`'s owner rather than
  patched here; #144 removes the bare `{}` in place (keeping any comments
  above it) instead of appending below it, and rejects a file the old code
  already corrupted rather than guessing at a repair.

  Verified end to end through the CLI once #144 landed:
  `internal/cli/testdata/script/router.txtar` now runs `router add-missing
  --yes` against a real copy of `testdata/parity-project` (its actual `{}`
  stub, untouched) and confirms the added route reloads via `router
  validate` afterward — the exact round trip the old code silently broke.

- **`orch config show` lands in G1.6, new-in-Go.** Python's only `orch
  config` verb is `consolidate` (H-2) — `show` has no source to port.
  `internal/config.Show` already existed, built for exactly this ("`orch
  config show` is a three-line command on top of this", its own doc
  comment says), so the wiring is `resolveAndValidate` → `config.Load` →
  `config.Show(stdout, res)`. `newConfigCmd` is a parent command (like
  `router`) so `consolidate` can land under it later without moving `show`
  or breaking the CLI shape.

- **`orch atomize` lands in G1.6, closing G1.6/G4.3's CLI side.**
  `internal/atomize` (G4.3) already had every piece
  (`WalkSpecFiles`/`ParseFiles`/`LoadExisting`/`MergeTasks`/`RenderDiff`/
  `Apply`) — the CLI is the flag parsing and call order `orchestrator/
  atomize.py`'s `main` already establishes, ported flag-for-flag
  (`--specs-dir`, `--file`, `--tasks-json`, `--list`, `--apply`,
  `--no-backup`) and exit-code-for-exit-code. Verified against a real
  `python -m orchestrator.atomize` run over the same spec fixture: section
  layout, counts, and every literal message match — the one difference
  (`16h` vs `16.0h` on an estimate) is inside `RenderDiff` itself, already
  a documented non-goal there, not something this PR touches.

  **Known gap, same shape as `task set`'s:** Python's `--apply` also
  best-effort syncs the parsed tasks into SQLite's `tasks_definition` via
  `state_backend.upsert_task_definition` after writing tasks.json.
  `state.Backend` has no method shaped for that write — `task set
  --model/--backend/--milestone` already documents the same absence — so
  the sync step is skipped rather than faked. tasks.json itself is
  authoritative either way; the sync is `orch status`/`tasks`'s route
  resolution catching up faster, not a second source of truth.

- **Bugs 18 and 19 (#156, opus, Python side) — `orch atomize`'s default
  specs root disagreed with `spec_root`, and the parser had no idea what a
  fence was.** Ported both fixes here as a follow-up to the PR above.

  **Bug 18:** the default specs root was `<project-root>/docs` — a value
  with no written justification anywhere, while `prompt_builder` resolves
  a task's `specRef` against `spec_root` from config.yaml (default
  `specs`), and `orch init` scaffolds `specs/` and tells the operator to
  write there. With the two roots disagreeing, `relpathForSpecRef` fell to
  its "outside the root" branch on **every** run and returned the bare
  filename — right by accident for a spec sitting directly under
  `spec_root` (why nobody noticed), wrong one level down:
  `specs/api/auth.md` became `specRef: "auth.md#..."`, and the composed
  prompt path `specs/auth.md` does not exist. Fixed: the default is now
  `<project-root>/<cfg.SpecRoot>` (`--specs-dir` still overrides), and the
  bare-filename fallback for a `--file` genuinely outside the root now
  warns on stderr — message text ported from Python's, since until this
  fix that branch ran silently on every single invocation, which is what
  let a wrong ref look like a working one.

  **Bug 19, found by fixing bug 18:** `internal/atomize`'s parser (like
  Python's) matches headers line by line with no concept of a fenced code
  block. `specs/README.md` — written by `orch init` — documents the
  minimum spec format inside a ```` ```markdown ```` fence containing a
  realistic-looking example (`F0.1.T1` "Setup monorepo", `F0.1.T2` "Root
  README"). Once bug 18 made `specs/` the root actually scanned (it used
  to fall on a nonexistent `docs/` and find nothing), `orch atomize
  --apply` with no `--file` on a project fresh out of `orch init` imported
  that example as two real tasks. A fenced block is documentation *about*
  the format, never content — the parser now tracks fence state
  (` ```starts-with ``` toggles it, same as Python) and drops every line
  inside one before the header/task/field regexes ever see it.

  Verified both ways bug 18/19 would have shown red: a temporary revert of
  the fence-skip loop reproduces the exact two fake tasks
  `TestParseSkipsTheShippedSpecsReadmeExample` catches, reading the real
  embedded `internal/templates` copy of `specs/README.md` rather than a
  hand-copied string, so the test tracks the shipped file. End-to-end
  regression: `orch init` (blank) → `orch atomize --apply` with no `--file`
  now finds zero tasks, in `internal/cli/testdata/script/atomize.txtar`.

- **G3.5 (the CI parity harness) found three Go-port bugs in
  `internal/model` before the harness itself existed** — walking the
  recorrido by hand (`orch init` → `orch atomize --apply`, diffing the
  resulting tasks.json against Python's byte for byte) surfaced them
  before a single line of harness or CI code was written. These are bugs
  in the Go port itself, not in Python — there is no number for them in
  the Python bug series `docs/brainstorm/go-migration-notes.md` tracks.

  1. **`Task`/`Meta`/`Phase.MarshalJSON` alphabetized every key.** Each
     builds its fields in the right order, into a
     `map[string]json.RawMessage` — and `encoding/json` always sorts a
     plain map's keys alphabetically on the final `Marshal` call,
     regardless of insertion order. So every tasks.json Go ever wrote
     (via `orch init`, `orch atomize --apply`, `SaveTasksFile` generally)
     came out `comments, dependencies, description, estimateHours, files,
     id, model, phase, reason, specRef, status, title` instead of
     `orchestrator/models.py`'s real dataclass order (`id, phase, title,
     description, model, reason, status, dependencies, estimateHours,
     files, specRef, comments`). No existing test caught it because every
     one of them asserts a field's *value*, never the file's own key
     order — and `Phase`'s two fields (`id`, `name`) happen to sort the
     same alphabetically as declared, which is exactly why this package's
     own tests, all green, never noticed.

  2. **`description` and `model` were swapped relative to Python's real
     field order**, found while fixing bug 1 above — a second, unrelated
     ordering mistake baked into `Task.MarshalJSON` since it was first
     written (G1.3), not something the map-sorting bug caused.

  3. **`estimateHours` rendered `1`, not `1.0`, for a whole number** —
     the same Python `sum()`/`json.dump` float-vs-int quirk `pyfmt.Float`
     already exists to handle everywhere else (`internal/state`,
     `internal/cli`'s cost fields), just never wired into
     `Task.MarshalJSON`.

  Fixed with a small `orderedJSON` builder (same pattern as
  `internal/cli`'s `orderedCount` and `internal/graph`'s `Problem`) in
  place of the map, plus `pyfmt.Float` for `estimateHours`. Verified end
  to end: `orch init` and `orch atomize --apply` now produce **byte-
  identical** tasks.json between the Python and Go binaries over the same
  project and spec — confirmed by actually running both and diffing, not
  by re-reading the code. New tests pin the key order via
  `encoding/json`'s token-by-token `Decoder` (`Unmarshal` into a map would
  discard the very thing under test).

- **G3.5: `scripts/parity.sh` grows a real workflow check, and a `parity`
  CI job runs it.** Before this, the script was a local-only, exit-0 gate
  over the committed `testdata/parity-project` fixture, diffing five
  read-only `--json` commands as text — nothing in CI ever ran it. Two
  additions:

  1. **`compare_tree`**, a second comparison mode alongside the existing
     text-based `compare`: it diffs a whole directory tree, for commands
     that *write* files rather than print JSON. Wired to a real workflow —
     `orch init` (blank project, same name on both sides) then `orch
     atomize --apply` (a real spec) — each binary scaffolding its own
     project rather than sharing a copy of one fixture, so a bug that
     leaks an absolute path into a written file would show up as a real
     diff instead of being masked by both sides using the same temp dir.
     This is the exact recorrido that found the three `internal/model`
     bugs above, run by hand before either the function or the CI job
     existed — walking it manually first, rather than writing the harness
     on faith, is what caught them before they could hide behind a "the
     harness must be right" assumption.

  2. **Explicit, commented exclusions — never silent normalization** for
     the parts of the recorrido that cannot be compared today: `orch
     graph` (Graphviz DOT vs Python's HTML+SVG, no shared format), `orch
     run` with `ORCH_FAKE_PROVIDER` (Go-only env var — Python's dispatch
     loop has no fake-provider mode, so there is no deterministic way to
     run it in CI without a real coding CLI), `orch run --dry-run`
     (registered on Python, not yet on Go — `docs/CLI.md`'s `run` row
     already lists it under flags deliberately not implemented), and
     `model_router.yaml`'s stub header comment (different wording between
     the two binaries, cosmetic — `compare_tree` diffs that one file with
     comments and blank lines stripped instead of skipping it outright, so
     a real route-entry difference still fails loudly). Each has a comment
     in `scripts/parity.sh` explaining why, not a normalization step that
     would make the difference invisible instead of unclaimed.

  Also excluded, for reasons that are about the comparison mechanics
  rather than a real divergence: `.orchestrator/state/` (Python's `atomize
  --apply` best-effort syncs to SQLite, a gap already documented above;
  Go genuinely does not write this directory, so there is nothing to
  normalize) and `tasks.json.bak-<timestamp>` (the name is never going to
  match between two runs a moment apart, and its content is a copy of a
  file already compared elsewhere in the same check).

  Sanity-checked both directions: reverted the `internal/model` fix
  locally and confirmed `compare_tree` fails loudly on the exact tasks.json
  difference it exists to catch, then restored the fix and confirmed green
  again — the same "seen it fail" standard applied to a shell script, not
  just to a Go test.

  The new `parity` job in `.github/workflows/go.yml` mirrors `go-test`'s
  pnpm/node setup (for `make build`'s `web` dependency) plus a Python 3.12
  venv (`pip install -e ".[dev]"`), then runs `make build && make parity`
  — the first time this repo's CI has ever run the Python package at all.

- **G6.1 — `internal/publish/snapshot`, new-in-Go, not a port.** The
  stakeholder snapshot's contract (agreed with orch-98) is stricter than
  anything Python ships: no task ids, no backend/provider names, no file
  paths, no raw technical text. `docs/SNAPSHOT-SCHEMA.md` has the full
  shape; two things are worth recording here because they are findings
  about Python, not just design notes about Go.

  1. **Python's own stakeholder JSON leaks spend past its own opt-in flag.**
     `config.yaml`'s `dashboard.show_spend_to_stakeholder` defaults to
     `false` with the comment "spend is sensitive"
     (`orchestrator/dashboard/server.py:486-490`) — but that flag only
     gates the SPA's own rendering. `_stakeholder_payload()` (the function
     behind `/stakeholder/summary`) computes and returns
     `spend_rounded_usd`/`spend_by_day` unconditionally
     (`orchestrator/dashboard/server.py:1707-1718`); a direct `curl` of the
     JSON endpoint sees spend with the flag off. This is a real gap, not a
     migration-notes hypothetical, but per the migration rule (fixes yes,
     features no, and Python is frozen) it is recorded here rather than
     patched there. Go's `snapshot.Build` honors the flag itself
     (`Input.ShowSpend`): with it false, `spend_usd`/`spend_by_day` are
     absent from the document, not merely zeroed or unrendered — the
     stricter behavior the flag's own comment already promised.

  2. **Python's `blocked_reasons` ships a blocked task's raw comment text,
     truncated to 120 characters, unredacted**
     (`orchestrator/dashboard/server.py:1682-1687`) — whatever a human,
     a CI log, or the dispatch loop itself wrote there, which can include
     exit codes or provider error text. The Go contract explicitly forbids
     this, so `blockers[].reason` is not a port of that field: it comes from
     a small translation table (`internal/publish/snapshot/translate.go`)
     over `internal/providers.Failure` — the closed classification the
     dispatch loop already runs every failure through
     (`internal/providers/classify.go`) — one fixed sentence per class per
     language. A blocked task with no classified failure (an unmet
     dependency, a manual defer) gets a generic sentence. This is new
     vocabulary, not a translation of anything Python has; Python has no
     structured "why is this blocked" signal to translate from.

  Coordinated with opus (G5.2 b1/b2, in progress) before writing anything:
  `graph.Summarize`/`PhaseCounts`/`Parallelizable`/`DownstreamImpact`/
  `CriticalPath`/`OrphanDependencies` and `project.Hydrate`/
  `HumanHoursByTask`/`LastUpdatedByTask` had already landed (PR #172) and
  cover most of what the snapshot needs — deliberately NOT the id-bearing
  ones (`CriticalPath`, `DownstreamImpact`, `Parallelizable` return per-task
  detail), since the sanitized snapshot only ever consumes the aggregate
  `Summarize`/`PhaseCounts`. `eta_hours_remaining` and `executive_summary`
  (metrics.py) had no Go equivalent anywhere and no second consumer other
  than this snapshot (opus confirmed by checking call sites — `orch.py`'s
  `/api/sprint` uses a different, velocity-based ETA under a confusingly
  similar name, `sprint_eta`; `/api/summary`'s `executive_summary` caller
  is not one of the nine endpoints Go's dashboard is porting), so both are
  built here rather than in `internal/project`, per his own reasoning: a
  pure function with tests is cheap to move later if a second consumer
  appears, guessing the right shared package ahead of one is not.

  Known follow-up, not blocking this PR: `totalSpend` sums `state.Spend`'s
  `CostUSD` directly, same as `internal/cli`'s existing `computeCostByTask`
  — a recorded `0` is ambiguous (some backends, e.g. opencode, never report
  a cost at all, so their real spend and "no data" both read as zero).
  `internal/pricing` (opus, PR #175, not yet merged) is where Python's own
  cost-vs-estimate logic (`orchestrator/dashboard/pricing.py`) is landing in
  Go; once it exposes something shared, this package should switch to it
  instead of the raw sum, in a small follow-up.
