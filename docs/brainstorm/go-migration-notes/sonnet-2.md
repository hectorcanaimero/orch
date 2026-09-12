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

- **`internal/tunnel` (G5.6) — the "cloudflared" bug had three sites, not
  one, all downstream of the same source.** Before writing anything,
  read `orchestrator/dashboard/tunnel/{manager,providers,deps}.py`
  end-to-end: the real `PROVIDERS` registry has exactly two entries,
  `autossh` (Pinggy, over SSH) and `bore`. No `cloudflared` anywhere.
  orch-98 traced the origin: `docs/DASHBOARD-PROFILES.md` documents
  cloudflared as a tunnel an operator mounts by hand, OUTSIDE orch,
  which got miscopied into the "Orch en Go" plan text as if it were a
  manager provider, which got copied again into
  `internal/config/load.go`'s `droppedKeys["dashboard.tunnel"]` message
  and `docs/CONFIG.md`'s ignored-keys table (both said "pinggy and
  cloudflared remain"). Python never had this bug — the string only
  ever existed in Go. Fixed both to say "autossh and bore" in this PR
  (own commit; internal/config isn't this lane's package, but it's a
  one-line string with no behavior attached, and orch-98 confirmed the
  fix in-band rather than routing it through a separate PR). Ported
  exactly the two real providers — no `cloudflared` ProviderSpec added,
  new-in-Go or otherwise: adding a provider with no real tunnel binary
  to test against would be a feature invented for this migration, which
  the migration rule doesn't sanction just because a stale doc handed us
  the name.

- **Go's `exec.Cmd.Wait()` cannot be called twice or from two
  goroutines — Python's `subprocess.Popen.wait()` can, and
  `TunnelManager` relies on exactly that.** Python's `stop()` (caller's
  thread) and `_on_child_exit()` (the reader thread) both call
  `proc.wait(...)`, safely, because CPython's Popen serializes that
  internally (`_waitpid_lock`). A literal port — `Stop()` calling
  `proc.Wait()` itself AND the reader goroutine's `onChildExit` also
  calling it — is a data race `go test -race` catches immediately (it
  did, on the very first run: two goroutines racing inside
  `os/exec.(*Cmd).awaitGoroutines`). Redesigned so `onChildExit` (run
  once, by the reader goroutine, when stdout hits EOF) is the ONLY
  caller of `proc.Wait()` in the package; `Stop()` signals the process
  and waits on `readerDone` — a channel `onChildExit` closes — instead
  of reaping it directly. A `stopRequested` bool takes over the job
  Python's two-sided `state.update()` calls did implicitly (a manual
  stop bumps `restart_count`; a crash outside `Stop()` does not) — set
  by `Stop()` before signaling, read and cleared by `onChildExit`, since
  the single physical reaper needs an explicit signal for what Python
  got for free from two racing writers agreeing by construction.

- **`SweepStaleLock(cfg=nil)` does NOT mean "adopt any live pid" —
  written into a test first, caught by the test failing, not by reading
  closely enough the first time.** `manager.py`'s `sweep_stale_lock`
  computes `adopted = True` when `cfg is None` and the pid is alive, but
  the ADOPTION branch is gated on `adopted and cfg is not None` — so
  `cfg=None` always falls through to `_release_lock_files()` regardless
  of `adopted`, live pid or not. The computed `adopted` value is
  genuinely never read again on that path; it appears to exist only so
  the alive-check runs the same way whether or not a command is being
  verified. Go's `SweepStaleLock` ports this exactly, including the
  apparently-pointless computation, on the theory that a Python method
  this deliberately structured (a named boolean computed, then gated
  behind a second condition that makes half its branches unreachable) is
  more likely to be a deliberate no-op left in place than an accident —
  matching this lane's standing rule that code with a written reason
  outranks a plan, a doc, or a guess.
  `TestSweepStaleLockWithNilConfigAlwaysCleansUpRegardlessOfLiveness`
  pins the behavior actually observed rather than the one a first read
  suggests.

- **The Go binary warned "unknown key" on any real project with a
  `tunnel:` block, from whenever `internal/config` first shipped until
  this PR.** `Config` had no `Tunnel` field and `knownKeys` had no
  `tunnel.*` entries — a project migrated to the Go binary before G5.6
  landed would see a spurious warning on every run for a config block
  that has always been valid. Added `config.Tunnel` (field-for-field the
  same shape as `TunnelManagerConfig`) and the seven `tunnel.*`
  `knownKeys` entries in this PR's own commit; verified with a grep that
  `internal/tunnel.ManagerConfig` construction (wherever G5.2's HTTP
  layer ends up building one from `config.Tunnel`) will read every field
  the allowlist admits — the same "declared but never read" shape as
  bugs 11 and 13 opus-2 flagged while coordinating this same file.
  Coordinated the `internal/config` edit with opus-2 first (concurrent
  PR touching the same struct, different fields — Dispatch/VCS/GitHub
  predate both of us) to land on non-overlapping struct positions with
  no rebase conflict expected.

- **Gemini's PR #169 review found one real bug beyond what
  `go test -race` already had:** compiling the URL/reconnect regexes
  happened lazily, inside the reader goroutine, so a bad `URLRegex`
  override spawned a real, unmonitored child process with no reader ever
  attached to reap it — a genuine leak, caught while writing the
  rule-8 coverage test for that path rather than by the test itself
  failing (the first draft of the test passed; the leak was only visible
  by reasoning about what `Stop()` would do afterward: signal a process
  whose `readerDone` channel was already closed by the early-return
  path, so `Stop` believed it had reaped a child it never called
  `.Wait()` on). Fixed by moving both `regexp.Compile` calls into
  `Start()` itself, before spawning — which also happens to match
  `manager.py` more closely than the original port did: Python's
  `re.compile` runs in `start()`'s own call stack via
  `_start_reader_thread`, so a bad pattern there fails `start()`
  synchronously, before anything is ever spawned, not asynchronously
  inside a thread nobody is watching yet.
  `TestStartBadURLRegexFailsBeforeSpawning` pins the corrected contract.
  The other two rule-8 findings (`Start` on an unknown provider, on a
  spawn failure) were coverage gaps only — both paths were already
  correct, just untested — verified by seeing `go test -race` genuinely
  fail on the very first run (the double-`Wait()` race, in this same
  PR's first draft) as the calibration that this reviewer's rule-8/22
  findings are worth taking seriously rather than dismissed as
  reviewer-being-pedantic.

  Also fixed: a 32-hex placeholder in a redaction test that read as a
  real credential shape (swapped for a `strings.Repeat("deadbeef", 4)`
  placeholder — same length, obviously not a real token); three
  `time.Sleep`-in-a-polling-loop instances, replaced with a
  `stateChanged` notification channel (`nil` in production, a test-only
  hook) so a test blocks on an actual state-write event instead of
  guessing a poll interval; and two bare `_ = ...` error discards
  (`os.Remove` on a stale lock file, `writeStateLocked`'s ~10 call
  sites) — the latter consolidated behind a `writeStateBestEffort`
  helper that logs via `slog.Warn` rather than a scattered `_ =` at
  every call site, so the fix reads as one deliberate policy rather
  than ten silent patches.

- **`web/stakeholder.html` (G6.2) — new capability, no Python
  equivalent (Python's static export is `/stakeholder/summary` JSON
  consumed live by the operator SPA, never a standalone offline
  bundle).** Two design decisions worth recording:

  1. **A second, separate vite config, not a second entry in the
     existing one.** Vite's `base` (the prefix every emitted asset URL
     gets) is a whole-build setting, not per-entry. The operator
     dashboard is mounted at `/` by a real server (`base: '/'`); this
     bundle is read straight off disk — a static host, or literally
     `file://` — with `data.json` sitting next to it, so every asset
     reference has to be relative (`base: './'`) or it resolves against
     whatever origin happens to be hosting it. Those two values can't
     coexist in one `build.rollupOptions.input` multi-page config, so
     `vite.stakeholder.config.ts` is its own file with its own
     `publicDir` (so the dev-only example `data.json` never leaks into
     the operator SPA's build) and its own `pnpm build:stakeholder`
     script. Verified for real: built, served with `python3 -m
     http.server`, confirmed the HTML/JS/`data.json` all load with the
     emitted relative paths.
  2. **`fetch('./data.json')` is blocked by the browser under literal
     `file://`, not just theoretically.** Chrome (and most browsers)
     refuse a `file://` page's `fetch()` of a sibling local file as a
     cross-origin request — this bundle has no server to make same-origin
     mean anything under `file://`. The brief says "renders opening the
     file, no network," which reads as "no API/login dependency," not
     necessarily "must work via a bare double-click with zero HTTP
     server" — `orch publish`'s other destinations (`to: git` → gh-pages,
     `to: cloud`) already imply real HTTP hosting. Rather than guess,
     `App.tsx` handles both: it tries the fetch, and on a `TypeError`
     specifically while `location.protocol === 'file:'`, shows an
     actionable message ("serve this folder over HTTP instead") instead
     of a bare fetch failure. No environment-detection hack for the
     common case (HTTP hosting), just a better error for the
     `file://`-without-a-server one.

  **`internal/publish` (G6.1) doesn't exist yet — schema coordinated
  with orch-sonnet directly rather than guessed.** Their draft schema 1
  has a real bug worth a heads-up rather than a silent workaround: the
  milestone object's example has two keys both named `"done"` — an
  `int` count and later a `bool` completeness flag. A JSON object can't
  hold two same-named keys without one silently winning at parse time
  (every parser checked — JS, Python, Go — keeps the last one), so as
  drafted the count would vanish and every milestone would just read
  `done: true`. Flagged to orch-sonnet before they commit the real
  generator; `types.ts`'s `isMilestoneComplete()` sidesteps it for now
  by deriving completeness from `done >= total` rather than trusting
  either literal `done` key. `build.outDir` (`internal/publish/dist/
  stakeholder`) is an explicit placeholder — commented as such in
  `vite.stakeholder.config.ts` — pending the real path their embed.FS
  will expect.

- **`orch doctor` (G4.5's missing other half) — found while wiring G7.2's
  release smoke test, not while working on doctor itself.** CLAUDE.md's
  "(existe)" tag next to `doctor/` was true of the *package*
  (`internal/doctor`, the seven `Check*` functions) and silently false of
  the *command* — `internal/cli` never imported `internal/doctor` at all,
  so `orch doctor` returned cobra's "unknown command" until this PR. The
  gap only surfaced because G7.2's smoke test tries to run it against a
  real installed binary; every existing Go test exercises `internal/doctor`
  directly, never through a `doctor` subcommand, since there wasn't one.
  Wired as `internal/cli/doctor.go`, deliberately narrower than Python's
  `build_doctor_report` — see the `doctor` row in `docs/CLI.md` for
  exactly what's not ported and why (both pieces already have a Go home
  under `validate` or nowhere at all). Tolerant like `validate`: a config
  or router load failure becomes its own `error`-status check rather than
  a CLI error that exits before any check runs, since a broken project is
  the normal case this command exists for.

  `docs/CLI.md`'s row and `internal/cli/testdata/script/doctor.txtar`'s
  assertions deliberately avoid `backend.*` checks by name — which
  provider CLIs happen to be installed varies by machine (a dev laptop
  with `codex` vs. a bare CI runner), so asserting on their presence/
  absence would be a test of the environment, not the command. `mcp.config`
  is the one check asserted on by name: a freshly copied fixture never
  ships a `.mcp.json`, so that result is the same everywhere.
