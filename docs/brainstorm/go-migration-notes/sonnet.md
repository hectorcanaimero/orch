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
