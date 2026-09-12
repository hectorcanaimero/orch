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

- **`internal/skills` + `install-skills` (G6.5) is new capability layered
  on top of a straight port, not itself a port — the brief asked for both
  explicitly, so this is scope-as-specified, not scope creep:**

  1. **The port half.** `_run_install_skills_subcommand`'s Claude-Code
     path — copy each shipped skill into `~/.claude/skills/<name>/`,
     `--path` override, skip-unless-`--force` on a differing existing
     install, `--dry-run` — is `Install(skill, TargetClaude, opts)` in
     `internal/skills/install.go`. Same flag names, same skip/overwrite
     semantics, same "nothing to do" / "installed N" / "skipped
     (pass --force)" report shape.

  2. **The new half.** `--target codex|opencode|cursor` has no Python
     equivalent — Python ships one static `AGENTS.md` stub, generated once
     at `orch init` time, and nothing for Cursor at all. codex/opencode
     read no `~/.claude/skills/`-equivalent directory, so both targets
     write a clearly-delimited section of the *project's* `AGENTS.md`
     (`<!-- orch:skill:NAME:start/end -->` markers), replaceable in place
     without disturbing anything else a human or another tool put in that
     file — `TestInstallAgentsMDPreservesExistingContentOutsideMarkers`
     pins that. Cursor gets `.cursor/rules/<name>.mdc`, using the
     `description`/`body` SKILL.md-frontmatter helpers.

  3. **Only `orch` exists to install.** The five pipeline skills this
     repo's own `CLAUDE.md` calls out as relevant —
     `orch-plan`/`orch-prd`/`orch-arch`/`orch-spec`/`orch-tasks` — live on
     the operator's machine (per orch-98, confirmed absent from this VPS
     entirely), not in this repository, so there is nothing to embed for
     them yet. `internal/skills` embeds via `//go:embed */SKILL.md`
     against the package directory rather than naming `orch` anywhere in
     Go code, specifically so dropping in
     `internal/skills/orch-plan/SKILL.md` later needs zero code change —
     `List()`/`Get()`/`install-skills --all` all walk whatever the embed
     glob matched. `internal/skills/orch/SKILL.md` itself was rewritten
     from Python's copy (`orchestrator/skills/orch/SKILL.md`) into an
     operational manual — hard rules, how to read/transition state via the
     Go CLI, an `orch migrate` pointer for pre-SQLite projects — rather
     than copied verbatim, since the audience (an agent session dropped
     into a project mid-migration) needs the Go-binary-era command surface
     documented, not just the SQLite architecture Python's version
     already covered.

  Two bugs `install_test.go`/`installskills_test.go` caught before this
  shipped, both wrong on the first pass: `body()`'s frontmatter strip used
  `TrimPrefix(rest, "\n")`, which removes only the closing fence's own
  newline and leaves the conventional blank line before the body in place
  (fixed to `TrimLeft`, since a Cursor `.mdc` body starting with a stray
  blank line is cosmetic but real); and `mergeSection`'s "is the existing
  AGENTS.md section already up to date" check compared the EXTRACTED
  (trimmed, marker-stripped) existing section against the FULL
  marker-wrapped candidate section, so a second `install-skills --target
  codex` on an already-installed project always reported `skipped`
  instead of `unchanged` — fixed to compare like for like (both sides
  trimmed, both sides marker-stripped).

- **`web/` (G5.1) — moved `frontend/` → `web/` via `git mv` (history
  intact) and picked "point vite's `build.outDir` straight into
  `internal/dashboard/dist/build`" over the alternative the brief
  explicitly offered (a `go:generate` copy of `web/dist` into
  `internal/dashboard`), for one reason: a copy step is something to
  forget. `go:embed` cannot reach outside its own package directory
  either way; the copy approach adds a manual "did you re-run
  `go generate`" step between `pnpm build` and a correct binary, whereas
  pointing outDir there directly means `pnpm build` alone is authoritative
  — there is no second copy of the built assets to go stale. Trade-off
  taken deliberately: `web/`'s own `dist/` never exists any more (`pnpm
  build`'s output lives one level up, at
  `internal/dashboard/dist/build/`), which is mildly surprising the first
  time — documented in `web/README.md`, `web/vite.config.ts`'s own
  comment, `internal/dashboard/spa.go`'s package doc, and a CLAUDE.md
  gotcha, specifically so it isn't rediscovered the hard way twice.

  **The `go:embed`-needs-≥1-file constraint, solved without fighting
  `emptyOutDir`.** A `go:embed` pattern matching zero files is a compile
  error, not a runtime one — so `internal/dashboard/dist/` needs a
  tracked, always-present file even before the first `pnpm build`.
  Tracking that file inside the SAME directory vite's `emptyOutDir: true`
  owns would mean every `pnpm build` deletes it from the working tree
  (git would show it as locally deleted — noise nobody wants to see after
  a routine build). Fixed by nesting one level deeper: vite's `outDir` is
  `dist/build/`, not `dist/`, so `emptyOutDir` only ever touches
  `dist/build/`'s contents; `dist/README.md`, one level up, is the file
  `go:embed dist` always finds, untouched by any number of rebuilds.
  `TestSPARequiresABuild` (`internal/dashboard/spa_test.go`) is the
  friendly-failure half the brief asked for: it fails with an explicit
  "run `pnpm build` in web/" message when `dist/build/index.html` isn't
  there yet, rather than a panic once G5.2 wires an HTTP server around
  `SPA()`. Deliberately NOT wired into `make test` (only `make build`
  depends on `make web`) so that message stays reachable for a bare
  `go test ./...` — `make test` auto-building the SPA every time would
  make the friendly-failure test pointless for exactly the case it exists
  for.

  **Discovered along the way, fixed as part of the same rename, not
  scope creep:** `scripts/build-spa.sh` (called by `build-wheel.sh`,
  called by `ci-build.yml`/`release.yml` for the Python wheel) copied
  `frontend/dist` → `orchestrator/spa/` — a path that would have silently
  stopped existing the moment `web/vite.config.ts`'s `outDir` moved
  elsewhere, breaking the wheel's SPA on the next release with no error
  until someone actually opened the shipped dashboard. Fixed to read from
  `internal/dashboard/dist/build/` instead (with an explicit check that
  `index.html` is actually there before copying), so one `pnpm build`
  now feeds both binaries. `.github/workflows/release.yml`'s
  `cache-dependency-path: frontend/pnpm-lock.yaml` had the same
  now-broken pointer and wasn't in the brief's explicit file list
  (`go.yml`/`ci-build.yml` were) — fixed anyway since leaving it wrong
  would have quietly turned off dependency caching on every tagged
  release build.

  **Two things checked and deliberately left untouched:**
  `pyproject.toml`'s `[tool.setuptools.package-data]` never referenced
  `frontend/` at all — it lists `"orchestrator"` → `"spa/**/*"`, and
  `orchestrator/spa/` is populated by `build-spa.sh`, not read from
  directly by setuptools — so the rename needed zero pyproject changes.
  And `orchestrator/dashboard/server.py`'s `_resolve_spa_dist` still
  checks `<project_root>/frontend/dist` as its project-specific override
  tier — that's a generic convention for ANY orch-managed project's own
  custom frontend, unrelated to this repo's own directory layout;
  changing what string it looks for would be a Python behavior change,
  which the migration rule reserves for point fixes with a real bug
  behind them, not a rename in the Go tree. `test_dashboard_spa_mount.py`
  exercises that tier against temp-dir fixtures it builds itself, so it
  was never coupled to this repo's actual `frontend/`/`web/` directory
  and needed no changes either.

- **`web/` (G5.5) trim — one real bug found by just reading what got
  deleted, not by a test:** `ProtectedRoute.tsx` had an `is_setup` gate
  (`GET /api/config/status`, redirect to `/setup` when false) that the
  brief's file list didn't mention at all — it only names
  `SetupWizardPage.tsx` itself. Deleting the wizard page and route without
  also removing this gate would have left a real dead end: a
  never-set-up project hitting any protected route would `Navigate` to
  `/setup`, which no longer resolves to anything, and the catch-all route
  bounces it back to `/`, which redirects to `/setup` again — an infinite
  loop, not a 404, so it wouldn't even show up as a broken link on a
  click-through smoke test. Removed the whole gate (`ProtectedRoute` is
  now just the auth check it always should have reduced to once the
  wizard was gone). `GET /api/config/status` loses its only SPA consumer
  along with `POST /api/config/setup` — both reported to orch-opus (G5.2)
  before this landed, per orch-98's instruction to flag consumer loss
  ahead of the endpoint port.

  **Tunnel's read-only trim keeps the D#9 gate, doesn't relax it.** The
  pre-trim panel's own comments call `can_control` gating
  `useTunnelStatus` "non-negotiable" (`/api/tunnel/status` must never be
  polled when it's false) — that's a backend contract, not a UI nicety
  the Start/Stop removal gets to loosen. `TunnelPage` keeps `can_control`
  as its visibility gate exactly as before; only the mutations
  (`startTunnel`/`stopTunnel`), the conflict-error handling built around
  them, and the SSE log stream (`useTunnelLogs`, `/api/tunnel/logs`) are
  gone. `/api/tunnel/status` and `/api/tunnel/capabilities` keep their
  consumer unchanged.

  **Bundle**, measured before and after on the same fixture (`pnpm
  build`, `web/` on this branch): 680.64 kB / 69.62 kB (JS/CSS,
  gzip 209.46 / 12.36 kB) → 483.30 kB / 46.36 kB (gzip 151.44 / 9.12 kB).
  `react-markdown` + `remark-gfm` (DocumentsPage's only consumers) came
  out as direct `package.json` dependencies too, not just dead imports —
  removed rather than left installed-but-unused.

  **Verified against the real Python dashboard, not just `tsc`/`vite
  build`:** a fresh venv (`pip install -e ".[dev]"` against this
  worktree specifically — an existing cached venv from earlier lane work
  was editable-installed against a different checkout and would have
  silently tested the wrong `orchestrator/spa/`), `scripts/build-spa.sh`,
  then `orch dashboard --project-root <copy of testdata/parity-project>`
  under a 15s `timeout`: `GET /` and the real hashed asset both returned
  200, log showed `SPA mounted at / → .../orchestrator/spa (packaged)`,
  and the process exited cleanly with no leftover `uvicorn`/`orch`
  process after the timeout fired.
