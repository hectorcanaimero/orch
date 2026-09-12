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

- **G7.1/G7.2 (goreleaser + release CI) — the tag collision was the real
  find, not the goreleaser config itself.** `.github/workflows/release.yml`
  (the Python wheel) has always triggered on a bare `tags: v*` — harmless
  while the only tags matching it were Python releases, but the instant a
  plain `v0.12.0` Go-release tag gets pushed, that same push ALSO fires
  the wheel workflow, which would try to build a Python wheel and attach
  it to what should be a Go binary release. Existing tags confirm the
  history: `v0.5.0`…`v0.10.1` (pre-freeze Python), then `v0.11.0-py` (the
  freeze). Raised with orch-98 before writing `.goreleaser.yaml` rather
  than guessing at a fix to a workflow outside this lane; resolved as a
  suffix split (`v*-py` stays Python's, bare `v*` is Go's from `v0.12.0`
  on) rather than a prefix — "one product, one series," continuing
  Python's own pre-freeze numbering instead of resetting to `v1.0.0`. See
  `docs/RELEASING.md` for the full scheme and why the tag glob alone
  (`v[0-9]*` on the Go side) does NOT exclude `-py` tags by itself — `*`
  matches the suffix too — so `release-go.yml`'s job also checks
  `!endsWith(github.ref, '-py')` at runtime; that check is the real guard.

  **`goreleaser`'s `brews` config key is deprecated in v2.18.1 with no
  documented replacement for a plain CLI-tool Homebrew formula** (only
  `homebrew_casks`, for GUI apps, is new) — checked its own JSON schema to
  confirm `brews` is still the only key that does what's needed; `goreleaser
  check` fails on it (exit 2, "uses deprecated properties") but `goreleaser
  release --snapshot --clean` builds and writes a correct formula anyway,
  verified end to end against a real 4-binary build. Accepted rather than
  worked around: CI's dry-run job runs the real `release` command, not
  `check`, so this never gates a PR — noted here so a future goreleaser
  upgrade that actually removes `brews` doesn't come as a surprise.

  **Not created by this PR, and said so explicitly rather than silently
  assumed**: `github.com/hectorcanaimero/homebrew-orch` (goreleaser pushes
  a commit to it, doesn't create the repo) and the `HOMEBREW_TAP_GITHUB_TOKEN`
  secret it needs — creating a new public repo under the maintainer's own
  GitHub account is a real, visible action outside what a PR should do
  unilaterally. `docs/RELEASING.md` has the one-time `gh repo create`
  command for whoever does this before the first real `v*` tag.

  **`orch doctor` not existing as a CLI command** (found while wiring this
  PR's own G7.2 smoke test) turned out big enough to need its own PR,
  landed ahead of this one — see that PR's own entry in this file rather
  than duplicating it here. `release-go.yml`'s `smoke-install` job treats
  exit 1 (warnings only, e.g. no `.mcp.json` on a bare scratch project) as
  a pass and only fails on exit ≥2 (a real `error`-status check) —
  matching `internal/doctor.ExitCode`'s own convention rather than
  demanding a clean bill of health from a project that was scaffolded
  thirty seconds earlier.

- **`orch doctor`'s `config.parse`/`router.parse` error branches — merged
  on #183 with a red Gemini review, fixed here.** The user merged #183
  anyway (both findings were "new behavior without a test," not a
  correctness bug), and asked for the two missing cases as a small
  follow-up rather than blocking the PR on them. Added two `doctor.txtar`
  fixtures — `badcfg` (a `.orchestrator/config.yaml` that isn't valid YAML)
  and `badrouter` (same for `model_router.yaml`) — each asserting its own
  named error check appears (`config.parse` / `router.parse`) and the
  combined `exit_code` is 2. **Actually seen red first, not just
  described as such**: temporarily deleted both `else` branches in
  `doctor.go` (the ones that append these checks) and reran the test —
  it failed exactly as expected, with the specific assertion that's
  missing named in the output, not a compile error or an unrelated
  failure. Restored the branches, reran, green. Both `badcfg` and
  `badrouter` incidentally also trigger the *other* file's missing-file
  error (a config-only-broken fixture has no `model_router.yaml` at all,
  and vice versa) — harmless, since the assertions only check for the
  presence of their own named check via a substring match, not an exact
  single-line output.

- **Process note, not a code finding: a `fork` launched to send two
  coordination messages (root.go's shared `AddCommand` line) ended up
  also pushing and opening the G7.1/G7.2 release PR (#185) — on the same
  worktree I was concurrently editing by hand.** A fork inherits full
  context, so it acted on the broader task list visible in that context,
  not just the narrow thing it was dispatched for; racing on the same
  files produced a genuinely confusing intermediate state (an
  in-progress rebase I hadn't started, commits I hadn't consciously
  made, a stray leftover conflict marker) before both sides' work
  happened to converge on the same correct end state. Orch-98's
  correction, recorded here rather than re-learned: don't fork onto a
  worktree that's also being edited directly in the same turn, and a
  ping to another session goes through this session's own `SendMessage`
  call, not delegated to a fork.

- **G8.2 (F3.3): migration 006 is the first Go-only schema change since
  the Python freeze — documented as a deliberate break from the
  "byte-for-byte copy" invariant, not silently allowed to happen.**
  `internal/state/migrate.go`'s own header comment used to say migrations
  001-005 "must" round-trip between Go and Python, written while
  `orchestrator/state/sqlite_migrations/` was still a live tree. It has
  been frozen since `v0.11.0-py` (ADR-G0) — no new features land there —
  so a Go-only feature needing a new table (the stakeholder token, moved
  out of config.yaml so `orch dashboard token rotate` can change it
  without a YAML edit or a restart) has nowhere to put a Python-side
  mirror, and shouldn't invent one for a line that gets no more code
  changes. Resolution: `006_stakeholder_tokens.sql` exists only under
  `internal/state/migrations/`, `migrate.go`'s comment now says the mirror
  ends at 005, and `TestEmbeddedMigrationsMatchThePythonTree` only
  compares migrations `<= 5` against the Python tree instead of every
  embedded one. Confirmed harmless in the direction that matters: opening
  `testdata/orch-py-0.11.0.db` (a real Python-written, schema-5 database)
  through Go's `Open` still applies exactly one migration (006) and lands
  on `user_version = 6` — verified via
  `TestOpenPythonWrittenDatabaseAppliesOnlyGoOnlyMigrations` (renamed from
  `...AppliesNothing`, whose old name and assertions were the thing this
  entry is about) — the "swap the binary, keep your database" claim ADR-G3
  makes was never "the schema stops moving," only "nothing Python wrote is
  lost or misread." Every other test hardcoding "0 migrations applied to
  the Python/v0.11.0 fixture" across `internal/state` and
  `internal/budget` needed the same `0 → 1` update; a repo-wide grep for
  that assumption before landing 007 would have caught this faster than
  finding each one by running the suite.

  **Design questions asked and answered before writing any code, not
  guessed at**: orch-98 confirmed the token moves to SQLite but the
  dashboard stays one-project-per-process (no multi-project routing in
  this PR, only the storage groundwork for a future `--portfolio`);
  stores a SHA-256 hash, never plaintext, with the middleware comparing
  hashes; the database wins over `--token`/`dashboard.token`
  unconditionally when a row exists (not just "wins over config.yaml but
  loses to an explicit flag," which is every other flag's rule in this
  codebase but deliberately not this one — a stale `--token` in a saved
  command must not resurrect a rotated-away token); and `orch dashboard
  token rotate`/`show` were needed in this same PR because nobody opens
  `sqlite3` by hand to manage a row.

  **Kept `decide()` pure per opus's review of the plan** (owns
  `internal/dashboard/access.go`, consulted before touching it): it now
  takes `expectedHash` as an explicit fourth argument instead of reading
  a live value off `Config` or looking it up itself, so the
  profile × path × route × token × hash table test stays a table with no
  server, database, or clock. The live part — resolving that hash fresh
  from the database on every gated request, which is what makes rotation
  not need a restart — lives in `Server.expectedTokenHash` (server.go),
  one level up from `decide`, with a precomputed `fallbackTokenHash` for
  when the database has no row yet and a nil-`state`-safe fallback for
  the handful of existing tests that build a server with no `StateReader`
  at all (routes that never otherwise touch state, like `/api/whoami`,
  are still gated, so they now reach this path too — a case that didn't
  exist before this PR).

  **`orch dashboard token rotate` bootstraps the project before writing**
  (`backend.Bootstrap(ctx, loadDAG(paths))`, same one-time idempotent seed
  `status`/`run`/`task set` already do) because `stakeholder_tokens` has a
  foreign key onto `projects`, and `orch dashboard` itself never
  bootstraps — a brand-new project's very first rotation would otherwise
  fail on that FK with no obvious cause.

  **Two real findings from review, both fixed before merging, not after:**

  Gemini flagged `StakeholderToken` silently discarding `ParseTS`'s
  `ok` bool on `rotated_at` (rule 19) — `t, _ := ParseTS(rotatedAtRaw)`.
  That row is written only by `SetStakeholderToken` in a format
  `ParseTS` always accepts, so this could only fire on external
  corruption, but "can't happen" is exactly the case rule 19 exists for:
  fixed to return an error instead of a silent zero `time.Time`, with a
  test that corrupts the column by hand and checks for the error —
  seen red against the un-fixed code first.

  Opus's review of the plan (they own `access.go`) caught a real bug in
  `Server.expectedTokenHash` I'd written and not seen: it collapsed a
  database READ ERROR into the same `ok = false` path as "this project
  never rotated a token," falling back to config.yaml/--token either
  way. Those are not the same state — "never rotated" means the
  config/flag fallback is correct, but "the database couldn't answer"
  says nothing about whether a row exists, and falling back on it would
  resurrect a token an operator just rotated away, at precisely the
  moment (a leaked token) rotating exists to defend against, with a
  transient DB error as the trigger. Same shape as the unknown-profile
  hole from #184: a degradation that looks like robustness. Fixed to
  fail closed (`return ""`, decide()'s own sentinel for "no token" →
  401) on a read error specifically, with `TestGatedRouteFailsClosedOnDatabaseError`
  (renamed from a test that asserted the old, wrong 200) proving it by
  what it does NOT allow (rule 25).

  **Known, accepted gap, written down rather than silently true:** the
  SSE event stream (G5.4, `stream.go`) resolves `expectedTokenHash` once
  when a client connects and holds that connection open for hours, so a
  token rotated while a stream is open keeps working on that stream
  until it reconnects — every other gated route re-resolves per request,
  but a long-lived stream doesn't re-check mid-flight. Not fixed here;
  flagged by opus and orch-98 as acceptable for a local, single-operator
  dashboard, but real enough to write down before someone assumes
  "resolves fresh on every request" means every consumer of that method,
  everywhere.

- **G8.5 (client half): `web/`'s PortfolioPage was written against
  opus-2's proposed `/api/portfolio` shape before their server-side PR
  existed on `main`**, per orch-98's explicit instruction for this
  situation. The agreement (both sides, before either wrote code): flat
  JSON reusing single-project field names (`SprintHealth`'s
  `velocity_per_day`/`eta_days`/`eta_date`/`confidence`/`blockers[]`,
  plus the plain status counters) rather than nesting under `sprint`/
  `summary`, since the real precedent in this codebase
  (`internal/dashboard/sprint.go`'s `sprintPayload`) is already flat; no
  `href` field (the client composes `/p/<project_id>/` itself, a
  one-line rule not worth a second source of truth); `available`+`reason`
  per project plus a separate top-level `unavailable` list, mirroring
  `sprintPayload.Available`'s own degrade-and-say-so pattern.
  `web/src/lib/portfolio.example.ts` is the fixture this was written and
  reasoned against — five projects in different states (done, blocked,
  budget-paused, empty, unavailable) — marked in its own doc comment as
  provisional and never imported by production code; delete it once the
  real endpoint has shipped a few times with a stable shape.

  **Gating is two-layered, not one**: `operatorOnly` (existing NAV_ITEMS
  mechanism) hides Portfolio from a stakeholder session at the profile
  level, same as Tunnel/Metrics/Logs; a new, separate `portfolioGated`
  flag additionally hides it when `/api/portfolio` 404s (dashboard not
  started with `--portfolio`) — the two reasons a nav item disappears
  read differently to whoever edits this list next, so they're not
  folded into one boolean. The portfolio query itself is also gated
  `enabled: !isStakeholder` in `usePortfolio`, so a stakeholder session
  never issues the request at all, not just fails to show its result —
  same reasoning as `TunnelPage`'s D#9 constraint on `/api/tunnel/status`.

  **Could not visually verify in a browser.** This VPS blocks starting
  any dev server (`pnpm dev` included, per this machine's own global
  CLAUDE.md — two ports are already reserved for unrelated projects and
  nothing else may bind one), and `web/vite.config.ts`'s dev proxy
  target (a live `orch dashboard` on :7420) doesn't exist yet for
  `/api/portfolio` regardless. Verified instead by `pnpm build` (tsc
  type-checks the page against the real `Portfolio` type — a field-name
  mismatch against the example fixture would fail the build) and close
  reading against `MilestonesPage`/`TunnelPage`'s established patterns.
  Said explicitly in the PR rather than claimed as tested — see this
  project's own instructions on not claiming UI correctness from a type
  check alone.

  **Bundle size, measured before/after like G5.5**: 483.30 kB → 488.07 kB
  raw JS (151.44 kB → 152.33 kB gzip), 46.69 kB → 47.04 kB raw CSS
  (9.16 kB → 9.22 kB gzip) — measured by stashing this branch's changes
  with a uniquely-tagged `git stash push -u` (never a bare `stash`,
  shared stack) and rebuilding at the branch's base commit, then
  restoring by exact SHA and dropping the entry.

  **Review (Gemini + opus, actually running #201's real server) found
  three real gaps, all fixed before merging:**

  1. **Rule 8, real**: this PR shipped with zero test coverage — `web/`
     had none at all before it, and adding a whole page with none too
     was exactly what the rule exists to catch. Added `web/`'s first
     test suite: vitest (pinned to `^2`, not latest — vitest 5 needs
     vite 6+, this repo is on vite 5) plus React Testing Library for one
     render test file. `@testing-library/react`'s auto-cleanup needs a
     true global `afterEach` (`test.globals: true`, not set here), so
     `vitest.setup.ts` registers it explicitly — without it the second
     render test in a file was finding the first test's still-mounted
     DOM, which reads as "duplicate elements" and has nothing to do with
     the component actually rendering twice.
  2. **Rule 6-adjacent**: `portfolio.example.ts`'s fictional project
     roots were `/home/u/proyectos/...` — genericized to
     `/srv/orch-projects/...`, no home directory or username-shaped
     segment, after Gemini flagged it on the first push.
  3. **The one that mattered most, found by opus actually running #201's
     server**: a single-project `orch dashboard` has no `/api/portfolio`
     route registered at all, so the request falls through to the SPA's
     own catch-all and comes back **200 with the HTML shell**, not a
     404. `isPortfolioDisabled`'s 404-only check never fires; `getPortfolio`
     resolves with a string where a `Portfolio` was promised; the very
     first read of `.projects.length` throws. Added `PortfolioShapeError`
     (api.ts): `getPortfolio` now rejects with it whenever the body isn't
     `Array.isArray(data.projects)`, and `isPortfolioDisabled` treats it
     exactly like a real 404. Defense in depth, not just a workaround for
     today's gap — a proxy or redirect could produce the same "200, wrong
     body" shape later. opus-2 is separately registering a real 404 JSON
     response in #201's mono-project mux; both fixes stand independently
     of each other landing.

  Two smaller findings from the same review pass, both one-line fixes:
  `PortfolioProject.todo` was declared required and filled by this
  file's own fixture, but #201's real server folds `todo` into
  `backlog` on purpose and never emits it — removed from the type and
  every fixture, since nothing but the fixture itself was holding that
  field up. And `PortfolioPage` called `usePortfolio()` with no
  `enabled` option, so a stakeholder navigating to `/portfolio` directly
  (bookmark, typed URL) still issued the request — `usePortfolio`'s own
  doc comment promised otherwise, but only `AppLayout` actually kept
  that promise. Fixed by reading `useWhoami` in the page too, tested by
  asserting the exact `{enabled: false}` / `{enabled: true}` call opus's
  finding said was missing.

- **Bug 26 (Python line, shared — one compiled `web/` bundle serves both
  dashboards): `ProtectedRoute` showed a login wall to an operator whose
  server needed no token at all.** Found by opus running headless
  chromium against a real Go binary while reviewing #203. The old gate
  was `useAuth().isAuthenticated`, defined as
  `Boolean(localStorage.orch_token)` — it never asked the server
  anything, so a fresh browser with nothing saved (the normal case for
  an operator on their own machine) was bounced to the token form for a
  server that was never going to reject it.

  **Confirmed shared with Python before touching anything**, per
  orch-98's standing rule for a `web/` bug found on the Go side: built a
  throwaway venv (`python3 -m venv`; this box's system Python is
  externally-managed and refuses a bare `pip install`), installed
  `fastapi<0.116`+`uvicorn[standard]`, ran `scripts/build-spa.sh` to
  populate `orchestrator/spa/` (a plain `pnpm build` alone only refreshes
  `internal/dashboard/dist/build/`, which only the Go binary's
  `go:embed` reads), and reproduced byte-for-byte: the same 2287-byte
  DOM with the same login form, against Python's own
  `orchestrator.orch dashboard`, on a project with
  `dashboard.profile: operator` and `/api/whoami` answering `200
  {"profile":"operator"}`. Numbered as a Python-line bug, not a porting
  regression — and fixed once, in the shared bundle, rather than twice.

  **Fix**: `ProtectedRoute` now gates on `useWhoami()` succeeding
  instead of on anything being saved locally — the same request
  `apiClient`'s interceptor already attaches whatever token exists to,
  so one check covers both real "let them in" cases (operator needing
  none, stakeholder with a valid saved one) without duplicating that
  logic. A loading skeleton covers the one request's round trip; any
  failure (401, or anything else once `useWhoami`'s own retries are
  exhausted) shows the form. Deliberately NOT reusing AppLayout's
  fail-open reading of the same hook ("assume operator" when whoami
  hasn't answered) — that default is safe there because nothing
  AppLayout gates on is actually protected by it (worst case: an
  operator briefly sees a nav item meant for later), but this component
  IS the actual access boundary, where an ambiguous answer must not
  render protected content. Removed `useAuth().isAuthenticated` (the
  only caller was the code just deleted) rather than leaving a
  now-misleading field nothing reads.

  **Verification limits, disclosed rather than glossed over**: `curl
  /api/whoami` confirms the server side; the four new
  `ProtectedRoute.test.tsx` cases (loading, operator success,
  stakeholder-with-saved-token success, failure→redirect) confirm the
  component logic, with rule-24 red/green actually run. Re-running the
  SAME headless-chromium `--dump-dom` reproduction against the fixed
  binary was NOT conclusive either way — it came back with an empty
  `#root` (613 bytes) regardless of `--virtual-time-budget` (tried 8s,
  20s, and none at all), the same symptom opus already flagged as
  probably a `--dump-dom`-vs-async-React-Query timing artifact rather
  than a real bug, in their own "not conclusive" note about a `?token=`
  case earlier in this same review. Did not chase it further with
  `--dump-dom`; a real Playwright session with `waitForSelector` is what
  opus's own message says would be needed to conclude anything about
  this tool's output, and this PR doesn't have one. opus-2 independently
  hit a related but distinct `--dump-dom` limitation reproducing the
  `?token=` path: the SSE stream a stakeholder session opens
  (`useEventStream` → `/api/events/stream`) never closes, so the page's
  own `load` event never fires and `--dump-dom` hangs until `timeout`
  kills it — confirms this tool needs CDP or a cut stream to render an
  authenticated view at all, not just a timing tweak.

  **Acknowledged, deliberately not fixed here**: `LoginPage`'s
  `setToken` accepts any non-empty string with no server round trip —
  under `operator`, where the server ignores whatever token is sent, the
  literal effect (found by opus-2 reading the code, not by running it)
  was that the wall admitted anyone who typed a single character while
  blocking the honest operator who typed nothing. This PR's fix removes
  the practical exposure (an operator is never routed to `/login` in the
  first place once whoami succeeds with none), but the form itself still
  has no real validation if someone lands on it by hand. Out of scope
  here — it's a `LoginPage` design question (does submitting call
  `/api/whoami` with the typed value before saving it?), not the
  access-gate bug this PR exists to fix — written down so it doesn't
  get rediscovered as if it were new.

  **The bug this PR's own fix uncovered, found by opus-2 running the
  real binary via CDP once the wall no longer blocked every path in:**
  the landing page (`StakeholderSummaryPage`, mounted at `/`) fetches
  `/stakeholder/summary`, which the Go dashboard doesn't implement yet
  (Python's does — `server.py:1731`). Same class of bug as
  `PortfolioShapeError` — the request falls through to the SPA's own
  catch-all and axios resolves 200 with the HTML shell as `data` — but
  here nothing checked the shape before touching it, so `summary.done`
  threw a bare `TypeError` and every path that got past the (now-fixed)
  wall landed on a blank, crashed page. Preexisting — opus-2 reproduced
  it identically on `main` with `?token=` before even looking at this
  branch — but invisible until now because nobody could reach it through
  the wall this PR removes. This is also what my own "empty #root, 613
  bytes" `--dump-dom` result almost certainly was, not a virtual-time
  artifact as first guessed: opus-2's CDP session caught the actual
  `Runtime.exceptionThrown` `--dump-dom` has no way to surface.

  Fixed the same way `PortfolioShapeError` fixes `/api/portfolio`:
  `StakeholderSummaryShapeError` (useStakeholderSummary.ts) rejects when
  the body isn't `typeof data.summary === "object"`, and
  `isStakeholderSummaryUnavailable` (checked by both the retry policy and
  `StakeholderSummaryPage`) treats it exactly like a real 404 — a named
  "Summary not available" state instead of the generic error alert,
  since this is the common case today, not a rare one. orch-98's
  instruction, followed literally: a visible state, not an exception,
  with a test — three new ones (real payload passes through; HTML-shell
  and missing-`summary` bodies both reject with the shape error) plus
  three render tests (shape error and real 404 both show the named
  state; an unrelated error still shows the generic alert). opus-2 is
  separately making every unimplemented `/api/*` and `/stakeholder/*`
  route answer a real 404 instead of falling through, which will replace
  today's shape-error path with the 404 path `isStakeholderSummaryUnavailable`
  already handles identically — no client-side change needed when that
  lands.

- **G3.4 was blocked on "no CLI installed on the VPS"; the real blocker was
  narrower and is only half gone.** All four CLIs are installed now — `codex`
  0.154.0, `opencode` 1.18.30, `gemini` 0.59.0, `agy` 1.2.1 — but *none is
  authenticated*, and rule 21 needs a captured **success**, not just a
  captured failure. What could be captured for real, and now lives in
  `internal/providers/testdata/<backend>/<version>/`:

  | fixture | how | why it is worth having |
  |---|---|---|
  | `opencode/1.18.30/success.json` | `--model opencode/mimo-v2.5-free` | the free tier needs no credential, so the happy path is real |
  | `opencode/1.18.30/unknown-model.json` | `--model no-such-model-xyz` | two error events, see bug 28 |
  | `codex/0.154.0/auth-error.jsonl` | no `~/.codex/auth.json` | plain-text stderr interleaved among the JSONL events |
  | `gemini/0.59.0/auth-error.log` | no `GEMINI_API_KEY` | exit **41**, not 1 |
  | `agy/1.2.1/auth-error.json` | OAuth prompt, timed out | envelope preceded by prose, see bug 29 |

  The success paths for codex, gemini and agy are still synthetic, under
  `<backend>/synthetic/` per the README's convention, and the honest way to
  close this is a login followed by a re-capture — not by promoting the
  hand-made files. `scripts/`-adjacent capture script kept out of the repo on
  purpose; the README documents the argv for each so a re-capture is a copy
  and paste.

  Worth recording because it cost an hour: the codex adapter had been waiting
  on branch `g2/opus2-codex` for 100 commits, and the only thing that had
  drifted was a duplicated `contains` test helper and one assertion that was
  *designed* to fail — `TestCodexSyntheticParseAuthError` pinned "auth expired
  classifies as Other" with a note saying to update the docs if it ever said
  permission. #117 added the `"auth expired"` marker naming codex. The
  tripwire fired exactly as written. That is the argument for pinning a gap
  with a message instead of leaving it untested.

- **Bug 28 — opencode's error message is one level deeper than
  `dispatcher.py` looks, so every opencode failure reaches the operator as a
  CPython dict repr.** `OpencodeBackend.parse_result` reads
  `err.get("message") or str(err)` where `err = ev["error"]`. Real opencode
  1.18.30 writes:

  ```json
  {"type":"error","error":{"name":"UnknownError","data":{"message":"Model not found: no-such-model-xyz/."}}}
  ```

  `error.message` does not exist, so the first branch never fires and the
  operator gets `{'name': 'UnknownError', 'data': {...}}`. Go reads
  `error.data.message` first, keeps the Python-shaped `message` as a
  fallback, and ends at the raw line. Not fixed in Python: the migration rule
  says a bug found mid-port is logged, and this one is cosmetic-but-daily
  rather than P0.

  **A second thing the same fixture shows, deliberately NOT changed.**
  opencode emits a generic `"Unexpected server error. Check server logs for
  details."` *before* the specific diagnosis, and both Python and the Go port
  report the **first** error event — so the operator is shown the useless one
  even after bug 28 is fixed. Preferring a later or "more specific" event
  would be an invented heuristic and would move what `Classify` reads, so the
  behaviour is ported as-is and pinned by two tests:
  `TestOpencodeParseRealUnknownModel` asserts the first event's sentence, and
  `TestOpencodeRealUnknownModelHidesTheUsefulSentence` asserts the useful one
  really is in the stream, one event later. Whoever decides to change this has
  the evidence in a test rather than in a comment.

- **Bug 29 — `AgyBackend.parse_result` runs `json.loads` over the whole log,
  so anything agy prints before its envelope costs the parser the entire
  result.** Not hypothetical: an unauthenticated `agy` 1.2.1 writes

  ```
  Authentication required. Please visit the URL to log in:
    https://accounts.google.com/o/oauth2/auth?...
  Waiting for authentication (timeout 60s)...
  ```

  and only then the JSON object. Python's `json.loads` raises, `payload`
  becomes `None`, and `status`, `response` and every token count are lost —
  on a *successful* run that happened to print a warning first, Python would
  read `status` as absent and report failure. Go tries the whole body first
  (Python parity when there is no preamble) and falls back to the last line
  that decodes as a JSON object, which is the same last-line rule
  `parseClaudeEnvelope` already applies. `TestAgyParseRealAuthError` asserts
  the preamble is still in the fixture, so a future clean re-capture cannot
  quietly turn that test into a test of nothing.

- **A gap pinned, not closed: an unauthenticated `gemini` is classified as
  `FailureOther` and retried.** gemini 0.59.0 exits **41** — worth knowing on
  its own, since nothing should key on exit 1 — and prints one line: `Please
  set an Auth method in your /home/…/.gemini/settings.json or specify one of
  the following environment variables before running: GEMINI_API_KEY, …`.
  That sentence contains none of `permissionMarkers`, so `Classify` returns
  `FailureOther` and the reaper retries a dispatch that cannot ever succeed.
  Adding a marker (`"set an auth method"`, or the bare `"auth method"`)
  changes behaviour for all five backends at once, which is not a call to
  slip into a provider PR. `TestGeminiParseRealAuthError` pins the current
  answer with a message pointing here — the same tripwire shape that worked
  for codex above.

- **`model_router.yaml`'s opencode routes do not resolve against opencode
  1.18.30 on this machine, and DeepSeek is the visible casualty.** `opencode
  models` lists **7** models, all under the `opencode/` free-tier provider;
  the router's `google/…`, `deepseek/…` and `opencode-go/…` entries resolve to
  nothing. Most of that is simply "no provider is authenticated", but the
  CLI's own suggestion is the part that is not explained by credentials:

  ```
  Model not found: deepseek/deepseek-v4-flash. Did you mean: deepseek-v4-flash, deepseek-v4-flash-vision-exp?
  ```

  It proposes the **bare** id, which is at least a hint that the
  `provider/model` spelling the router assumes is not this version's. Cannot
  be settled without one authenticated paid provider, so nothing is changed:
  `OpencodeProvider.Argv` passes `cli_model` verbatim, exactly as Python
  does, and `TestOpencodeArgvPassesModelVerbatim` pins that so a future
  "helpful" prefix-stripper has to argue with a test. **Consequence worth
  stating plainly, because it reads the other way at a glance: the two
  DeepSeek routes in `model_router.yaml` are configuration, not a working
  path. DeepSeek needs no new backend — it is an opencode model — but it needs
  the opencode provider authenticated and the model id confirmed before any
  claim that it works.**
