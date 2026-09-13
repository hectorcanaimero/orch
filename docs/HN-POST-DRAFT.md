# Show HN draft — orch in Go

Draft, not published. The author posts it; nothing here is scheduled.

**Every number below has a source.** The "Sources" section at the end gives the
command that produced each one, run in this repo on 2026-09-13 against `main`
at `5db7091`. Anything nobody has measured is absent rather than estimated:
there is no startup time here and no performance comparison against the Python
version, because nobody has measured either.

---

## Before posting

Each item says who can unblock it. The post must not go out with any of them
open — two of them make the first command in the post fail.

| # | Item | State on 2026-09-13 | Who |
|---|---|---|---|
| 1 | **Tag `v0.12.0`** so a Go release exists | No `v*` release without `-py` exists. `scripts/install.sh` asks the API for the newest non-`-py` release, finds none, and **the one-line install in the post fails**. | the author (tagging publishes) |
| 2 | **Run the install line on a clean machine** (Linux and macOS) and paste what it prints into the first comment | not possible before 1 | the author |
| 3 | **Homebrew tap**: create `hectorcanaimero/homebrew-orch` and the `HOMEBREW_TAP_GITHUB_TOKEN` secret, or drop the `brew` line from the post | neither exists; goreleaser's brew step needs both | the author |
| 4 | **The GIF** — `docs/media/GIF-SCRIPT.md` recorded with the Go binary (plan item G7.3, which is also the migration's gate) | not recorded; needs an interactive terminal and real tokens | the author |
| 5 | **GitHub Pages** enabled, so `orch publish --to git` has a live page to link | Pages is off on the repo | the author (repo setting) |
| 6 | Re-measure the numbers below on the release commit and update "Sources" | measured on `5db7091`, before the release | anyone |
| 7 | Re-check the rivals' star counts and features in the comparison table | copied from `README.md`, which says to verify them before citing | anyone |
| 8 | Post on a **Tuesday, Wednesday or Thursday** (plan: week 9) | 2026-09-13 is a Sunday; no date chosen | the author |

---

## Title

> Show HN: Orch – dispatch a task DAG to Claude, Codex or opencode, as a single binary

Alternatives, in case the first reads as a product pitch:

> Show HN: I rewrote my AI-agent orchestrator from Python to Go as a single binary
> Show HN: Orch – single-binary orchestrator for AI coding agents, and the 31 bugs porting found

The phrase to keep is **single binary**: it is what changed for someone
installing this, and it is checkable in one command.

---

## The post

I built orch to stop babysitting AI coding agents.

You write a spec, it becomes a `tasks.json` DAG, and orch walks the graph: it
works out which tasks are ready, dispatches each one to whichever CLI you routed
it to (`claude`, `codex`, `opencode`, `gemini`, `agy`), isolates it in its own
git worktree, opens a PR, waits for CI, and retries or blocks on the result.
There is a per-provider budget guardrail that stops dispatching before a bad
night spends a month's tokens, a dashboard that shows what is running, and a
read-only page for the client who is paying for the work.

It shipped as a Python package for eleven releases. It is now one Go binary with
the dashboard's React app embedded in it. No virtualenv, no pip, no server
process to keep alive.

```
curl -fsSL https://raw.githubusercontent.com/hectorcanaimero/orch/main/scripts/install.sh | sh
orch init my-project --template python-api
```

Most of the code — Python and Go — was written by AI agents (Claude sessions,
several in parallel) that I directed and reviewed, with a second model (Gemini)
reviewing every PR in CI against a checklist in the repo. That is relevant to
what follows, so it goes up front.

The rewrite is not finished and this post does not pretend it is. What follows
is what is true today.

---

## First comment (the one that should actually be read)

**Why Go.** orch spends its life doing four things: starting CLI processes and
supervising them for hours (including killing the whole process group when a
task times out), parsing their JSON output, writing SQLite, and serving HTTP. Go
does all four in its standard library or close to it, and cross-compiles to a
static binary with no C toolchain — the SQLite driver is pure Go, so there is no
cgo and no libsqlite to find. Rust would have done it too; for a first project
in the language, async subprocess supervision and the borrow checker would have
roughly doubled the time for nothing a user would notice.

The reason that mattered most was not performance, it was the install. orch is
handed to people already fighting their own toolchain; "make a virtualenv, it
needs Python ≥3.11" is a step where people leave.

**What got thrown away.** Deliberately, not "not yet":

- `orch findings` — an internal dogfooding tool, not part of the product.
- The architecture-diagram generator, which depended on an external skill.
- The file-based state backend. Go only speaks SQLite; `orch migrate` imports an
  old project once.
- Three dashboard pages (Architecture, Documents, Doctor — the last one is
  `orch doctor` now) and the web setup wizard. The operator JS bundle went from
  680 kB to 483 kB at that trim (489 kB today, after the portfolio page).
- `orch stop`. Ctrl+C drains and exits; a second one kills the agents.
- The plan said the tunnel supervisor would support cloudflared. The Python code
  never had cloudflared, only autossh (Pinggy) and bore, so that is what got
  ported. Porting what the plan said instead of what the code did would have
  been a new feature with nothing real to test it against.

(Jinja was already gone before the rewrite started; the dashboard had become a
React app while still in Python.)

**What it weighs.**

- The binary: **15.7 MB** on linux/amd64, 14.9 MB linux/arm64, 16.0 MB
  darwin/amd64, 15.3 MB darwin/arm64 — built with the release flags
  (`-s -w`), with the operator app (489 kB of JS) and the stakeholder page
  (174 kB) embedded.
- Dependencies: the Python package had 4 runtime dependencies; the Go module has
  6 direct ones that ship in the binary (`cobra`, `yaml.v3`,
  `modernc.org/sqlite`, the MCP SDK, `uuid`, `fpdf`) plus one used only by tests.
- **The Go tree is not smaller.** 32,051 lines of non-test Go against 21,279 of
  non-test Python, and 33,583 lines of Go tests against 26,606 of Python tests.
  The migration plan estimated about 9,500 lines of Go; it was wrong by more
  than 3×. Part of the difference is new capability (below), part is Go being
  explicit about every error, and I have not measured how the two split.

**What the Go version does that Python did not:** an MCP server with seven
`orch_*` tools so a dispatched agent can report status without shell scripts,
`orch explain`, `orch publish` (the client page as a static site, to a
directory or a `gh-pages` branch), `orch report pdf`, `orch sync issues`
(labelled GitHub issues become tasks), a portfolio view of several projects in
one dashboard, and a planning pipeline shipped as agent skills (PRD →
architecture → spec → tasks; in review as #217).

**What is still rough, stated plainly:**

- **All five backends have adapters, and every success path is tested against a
  real captured run** — but capturing them is how I learned that **gemini cannot
  be dispatched by orch today, in either version**: it refuses to run in a
  directory it was not told to trust, and orch always dispatches into a fresh
  worktree. The fix is one flag that switches off a security prompt on a CLI
  running unattended, and I want that argued in its own PR.
- On a ChatGPT-plan login, `codex` refuses every codex model id in the default
  router. I have not tested it with an API key.
- 11 of the 43 opencode model ids in the default router do not exist in
  opencode's current catalogue.
- The migration's own gate — reproduce the demo end to end with the Go binary,
  real dispatch, green PR, published page — has not been run yet.

---

## The part I would actually want to discuss

**Porting found 31 numbered bugs in the Python version. Its test suite — 1,530
passing tests — was green through every one of them.**

They were found by someone trying to reproduce a behaviour in another language
and discovering there was no behaviour to reproduce, or that it was not the one
the code appeared to describe; by capturing the real output of the CLIs orch
drives instead of writing sample output by hand; and in one case by a Go test:
bug 20 surfaced because a Go end-to-end script never terminated, where the
Python loop spun forever on a task whose model had no route.

A few, to show the shape:

- **CI polling never worked, in either version.** orch asked `gh pr checks` for
  a field `gh` does not have, got an error, and read the error as "still
  pending" — forever. Under that, `gh` reports states in upper case and orch
  compared against lower case. No CI result was ever seen, no CI retry ever
  fired, nothing was ever auto-merged. The tests fed the parser output written
  from the parser's own expectations.
- **The budget guardrail did nothing on SQLite projects**, which had been the
  default for a release: the gate read the old log files, the SQLite backend
  only wrote the table. 750,000 tokens against a 600-token cap said "go ahead"
  while the dashboard showed the real spend.
- **The client page showed spend with `show_spend_to_stakeholder` off.** The
  flag gated one route; the summary the page actually renders carried the
  figures unconditionally, and a test asserted them.
- **The retry backoff was computed and ignored.** The ready-set pass relaunched
  the task in the same tick, and could launch it twice. The test checked the
  timestamp was stamped, not that anything waited.
- **Every opencode failure reached the operator as a Python dict repr**, because
  the error sentence is one level deeper in the JSON than the parser looked.
  Found only by capturing a real failure.

**Why the tests did not catch them** is the interesting half, and it was
consistent enough to turn into review rules. The repository's review checklist
has 30 rules; the last eight came out of this migration, each from a specific
failure:

| Rule | Written because |
|---|---|
| 23 — a test that normalises before comparing must justify each normalisation | Three tests kept passing over the bug they existed to catch. One asserted `"…: specs/" in text`, which `specs/specs/…` satisfies. One flattened a Python golden's `null` to `""` — in the file whose job was catching `null` vs `""`. One compared timestamps as strings, where `+00:00` and `Z` spell the same instant. |
| 24 — a new test must be seen to fail | A test written after the fix and never seen red tests the author's model of the bug, not the bug. |
| 25 — a short-circuit is asserted by what it did not do | "Returns the right answer" does not distinguish a gate that skipped the database from one that queried it and ignored the result. |
| 26 — a golden carries a test of its own coverage | Regenerating a fixture can silently drop the boundary case while the comparison keeps passing. |
| 27 — parity expectations come from running Python, not reading it | `999_999` formats as `"1000.0k"`, not `"1m"`. `repr(1.0)` is `"1.0"`, not `"1"`. No careful reading survives those. |
| 28 — a shipped fixture beats an invented one | A hand-written sample tests the parser; the real file also tests that what we ship still parses. The CI-polling bug above is the same failure one step out: invented `gh` output. |
| 29 — run what you built and read its output once | A wizard's model-tier prompt rendered as `[agy/pro/claude/claude-opus-4-7/codex/gpt-5.6/…]` — unreadable. Eight tests passed: each asserted that the summary *displayed* an answer, never that the prompt was legible. |
| 30 — `tasks.json` does not know a task's status; anything that renders one hydrates first | Status and comments moved from the file to SQLite, and readers that nobody moved kept serving the file's frozen copy: a graph that drew a half-done project as untouched, a task API that returned no comments for every task orch had actually run. Empty, and plausible. |

If there is one transferable thing here, it is rule 23. A test that lower-cases,
trims, sorts, or accepts two spellings before comparing has decided not to see a
difference — and that is exactly where the bug is.

The second is rule 28: sample output written by the person who wrote the parser
tests the parser against its author's beliefs. Capture the real thing.

---

## Honest comparison

Against the tools people in this space actually use. Rival columns are copied
from `README.md` and must be re-checked on posting day (see "Before posting").

| | Multica | Vibe Kanban | Agetor | Claude Squad | **orch** |
|---|:---:|:---:|:---:|:---:|:---:|
| GitHub stars | 49.6k | 28.1k | 65 | 8.5k | **1** |
| Stack | Go + Next.js + Postgres | Rust | TypeScript (Electrobun) | Go | Go |
| License | Apache-2.0 + commercial | Apache-2.0 | MIT | AGPL-3.0 | MIT |
| CLIs/agents supported | 26 | 10+ | 5 | ~6 | 5 (gemini blocked by bug 30) |
| Desktop app | ✅ Electron | ❌ | ✅ macOS | ❌ (TUI) | ❌ |
| Mobile app | ✅ iOS | ❌ | ❌ | ❌ | ❌ |
| Cloud-hosted option | ✅ multica.ai | ❌ | ❌ | ❌ | ❌ |
| Single binary | ❌ | ❌ | ❌ | ✅ | ✅ |
| Read-only stakeholder view | ❌ | ❌ | ❌ | ❌ | ✅ |
| Per-provider budget guardrail (blocks dispatch) | ❌ | ❌ | ❌ | ❌ | ✅ |
| Spec → tasks pipeline | ❌ | ❌ | ❌ | ❌ | ✅ |
| Static export of the client page | ❌ | ❌ | ❌ | ❌ | ✅ (`orch publish`) |

And against itself:

| | Python (v0.11.0) | Go (today) |
|---|---|---|
| Install | `pipx install`, needs Python ≥3.11 | one binary, 14.9–16.0 MB |
| Runtime dependencies on the machine | Python + 4 packages | none |
| Dispatch backends | claude, codex, opencode, gemini, agy | same five (gemini blocked in both) |
| Command surfaces | 21 | 24 (no `findings`, no `stop`) |
| Dashboard | FastAPI + uvicorn + a built SPA on disk | `net/http`, SPA embedded |
| New in Go | — | MCP server (7 tools), `explain`, `publish`, `report pdf`, `sync issues`, portfolio |
| Non-test lines | 21,279 | 32,051 |
| Tests | 1,530 passing pytest cases (1,327 `test_` functions) | 1,061 test functions |

Two caveats that belong next to the table, not in a footnote:

- **"No runtime dependencies" is about the machine, not the code.** The Go binary
  compiles its dependencies in. Same parts, shipped differently.
- **1,061 vs 1,530 is not "fewer tests".** They count different things — Go test
  functions, many of them table-driven with a dozen cases each, against pytest's
  collected cases including parametrised ones.

---

## Sources

Run on 2026-09-13 in this repository at `main` = `5db7091`, Go 1.25.0.

| Number | Command |
|---|---|
| binary 15.7 / 14.9 / 16.0 / 15.3 MB (16,490,680 / 15,663,288 / 16,818,480 / 15,995,634 bytes) | `make web`, then for each of linux/amd64, linux/arm64, darwin/amd64, darwin/arm64: `CGO_ENABLED=0 GOOS=… GOARCH=… go build -ldflags='-s -w -X main.version=v0.12.0-dev' ./cmd/orch` and `stat -c %s`; the flags are the `builds.ldflags` of `.goreleaser.yaml`. MB = bytes / 1,048,576. |
| operator JS 489 kB, stakeholder JS 174 kB | the `pnpm build` / `pnpm build:stakeholder` output (`index-*.js`, `stakeholder-*.js`) |
| operator bundle 680 → 483 kB at the trim | PR #163 (the portfolio page, #203, took it to 488–489 kB) |
| 32,051 / 33,583 Go lines | `rg --files -g '*.go' -g '!*_test.go' internal cmd \| xargs cat \| wc -l`, and the same with `-g '*_test.go'` |
| 21,279 / 26,606 Python lines | `rg --files -g '*.py' orchestrator \| rg -v '^orchestrator/tests/' \| xargs cat \| wc -l`, and `rg '^orchestrator/tests/'` for tests |
| ≈9,500 lines estimated | the migration plan's Python → Go map (the "Orch en Go" planning document) |
| 1,061 Go test functions | `rg -c '^func Test' --glob '*_test.go' internal cmd` summed |
| 1,327 Python test functions | `rg -c '^\s*def test_' orchestrator/tests` summed |
| 1,530 passing Python tests | the pytest baseline in `CLAUDE.md` (1530 passed + 3 skipped), kept current by the PRs that add tests |
| 4 Python runtime dependencies | `dependencies` in `pyproject.toml` |
| 6 direct Go modules shipped, 1 test-only | the first `require` block of `go.mod` (7 entries); `rogpeppe/go-internal` is imported only from `_test.go` files |
| 21 Python command surfaces | the 18 entries of `_SUBCOMMANDS` in `orchestrator/orch.py`, plus `task` and `router` (dispatched before it) and the default run loop |
| 24 Go command surfaces | `orch --help` (23, excluding `help` and `completion`) plus the hidden, deprecated `migrate` |
| 30 checklist rules | `rg -c '^[0-9]+\. \*\*' .github/review/CHECKLIST.md` |
| rules 23–30 and their origins | the rules themselves, in `.github/review/CHECKLIST.md` |
| 31 numbered bugs | highest number in `rg -o -i 'bug ([0-9]+)' -r '$1' docs internal web/src .github \| sort -n -u`; the entries live in `docs/brainstorm/go-migration-notes/*.md` and the frozen `go-migration-notes.md`. A 32nd is logged in open PR #217. |
| bug 20 found by a Go test | `docs/brainstorm/go-migration-notes/orch-98.md`: "a testscript hung until its 540 s timeout" |
| 5 backends with adapters | `internal/providers/{claude,codex,opencode,gemini,agy}.go`; real captures listed in `internal/providers/testdata/README.md` (PR #215) |
| gemini blocked (bug 30), codex model refusal, 11 of 43 opencode ids | PR #215 and `docs/brainstorm/go-migration-notes/sonnet-2.md` |
| 126 PRs merged since #91 | `gh pr list --state merged --limit 300 --json number --jq '[.[] \| select(.number >= 91)] \| length'` |
| 29 packages under `internal/` | `rg --files -g '*.go' -g '!*_test.go' internal \| xargs -n1 dirname \| sort -u \| wc -l` (nested packages such as `publish/snapshot` count separately) |
| 7 MCP tools | `docs/MCP.md` |
| no Go release yet | `gh release list` (newest is `v0.11.0-py`) and the release lookup in `scripts/install.sh` |

**Deliberately not in this post**, because nobody has measured them: startup
time, memory use, throughput, and any "N× faster" claim. If someone asks in the
thread, the honest answer is that the rewrite was about distribution and
correctness, and the performance question has not been asked yet.
