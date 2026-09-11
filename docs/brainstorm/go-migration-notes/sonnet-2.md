# Go migration notes — lane sonnet-2

Append-only. One entry per finding, newest last. Format and numbering follow `docs/brainstorm/go-migration-notes.md`; that file is now history and is not edited after 2026-09-12.

- **`internal/cli` `migrate` (G4.6) is a narrower port than "port
  migrate.py" suggests, on two axes — both explicit brief asks (reuse
  `Backend`, keep it small), not scope creep found along the way:**

  1. **`state/run-*.json` (runs + in-flight dispatches) is not imported.**
     `migrate.py`'s `_import_runs` writes `completed_json`/`blocked_json`/
     `deferred_json` and an arbitrary historical `status` ('live'/'done')
     straight into the `runs`/`dispatches` tables with raw SQL — there is
     no shape for that on `state.Backend`: `StartRun`/`RecordDispatch`
     write a run's CURRENT state (hardcoded `status='live'`, no
     completed/blocked/deferred columns at all), not an arbitrary past
     one. Confirmed this can't affect the acceptance check before cutting
     it: neither `buildStatusRows` (`status`) nor `tasks` reads the
     `runs`/`dispatches` tables at all — `run-*.json` is coordination
     state for resuming a crashed run, not reported history.
  2. **`Backend.AppendEvent` validates `event_type` against the closed set
     `eventTypes` (see `internal/state/types.go`); `migrate.py`'s raw
     `INSERT OR IGNORE` does not.** A file-mode project can be years old
     and carry an event type the current set doesn't recognize (the set
     itself has already grown once, bug 9/#121). Rather than let one
     unrecognized row abort the whole transaction, `importEvents` catches
     `state.ErrUnknownEventType` specifically, skips that row, counts it,
     and keeps going — reported in the command's own output
     (`skipped N event(s) of unknown type %q`), never silently. Covered by
     `TestMigrateSkipsUnknownEventTypeInsteadOfFailing`.

- **Rediscovered `migrate.py`'s own legacy-state-dir fallback the hard
  way — assumed `config.Paths` already covered it, it doesn't.**
  `resolve_project_paths`'s Go port (`config.ResolvePaths`) picks
  NAMESPACED layout (`.orchestrator/state/<id>/`) whenever the project
  root was given explicitly (`--project-root`), which every CLI test in
  this tree does. But a file-mode project almost always predates
  namespacing entirely — its `events-*.jsonl`/`spend-*.jsonl` sit flat
  under `.orchestrator/state/`. `ResolvePaths`' own namespace detection
  (`hasNamespacedState`) only ever flips LEGACY→NAMESPACED on evidence a
  project already has namespaced state; it has no reason to flip the
  other way, because every OTHER command wants the namespaced path to
  exist even when empty (that's where it will write next). `migrate.py`
  itself carries an explicit fallback for exactly this
  (`state_dir`/`has_state_files` check in `run_migrate`) that a first
  read of `config.Paths` doesn't surface at all — first attempt at the
  fixture failed with "state dir not found" pointing at the empty
  namespaced path, which is what caught it. `resolveSourceStateDir`
  ports the same fallback: read from the namespaced dir if it has state
  files, else from its parent if THAT has them, else report the
  namespaced path as missing (same error Python would give). The fixture
  (`testdata/file-project/`) deliberately keeps its state flat, so this
  is exercised by every test in the file, not a special case.

- **`config.ErrFileBackend` — the one place this command must treat a
  "hard stop" config error as the reason it exists.** `config.Load`
  refuses `state.backend: file` outright (ADR-G4: "quietly treating it as
  sqlite would open an empty database and report a project with no
  history"), and `ErrFileBackend`'s own message says
  "Import the existing state with `orch migrate`". A literal `if err !=
  nil { return err }` — the pattern every other project-scoped command
  in `internal/cli` uses — would make `migrate` unable to load the
  config of the exact project it's meant to migrate.
  `loadConfigTolerantOfFileBackend` is the one exception: it still treats
  every OTHER config error as fatal, and falls back to `config.Defaults()`
  only for this specific sentinel.

- **`--dry-run` is stricter than `migrate.py`'s own docstring promise,
  matching the promise rather than the implementation.** Python's
  `run_migrate` constructs `SqliteBackend(...)` — which creates the
  schema on a fresh `orch.db` — BEFORE checking `args.dry_run`, so a
  dry-run against a project with no `orch.db` yet leaves one behind with
  an initialized (empty) schema. The module docstring says `--dry-run`
  should "log what WOULD be imported; touch no rows" — creating the
  database file is a violation of that promise, not a feature anyone
  asked for (no test in `test_orch_migrate.py`-equivalent pins it, and
  no comment explains it as intentional). Go's `--dry-run` never calls
  `state.Open` at all; `TestMigrateDryRunTouchesNothing` asserts neither
  the namespaced nor the legacy `orch.db` path exists afterward. Filed
  here rather than fixed in the Python source per the migration rule
  (bugs get reported, not patched, while both lines coexist).
