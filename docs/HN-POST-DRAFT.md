# Show HN draft — orch in Go

Draft, not published. No date and no GIF — those are the user's call.

**Every number below has a source.** The "Sources" section at the end gives the
command that produced each one, run in this repo. Anything nobody has measured
is absent rather than estimated: there is no startup time here and no
performance comparison against the Python version, because nobody has measured
either.

One marker is deliberate and must be updated before posting:
**`[update with the v0.12.0 release]`**.

---

## Title

> Show HN: Orch – a task orchestrator that dispatches your DAG to Claude, Codex or opencode, as a single binary

Alternatives, in case the first reads as a product pitch:

> Show HN: I rewrote my Python AI-task orchestrator in Go and found 25 bugs no test had
> Show HN: Orch – single-binary orchestrator for AI coding agents (rewritten from Python)

The phrase to keep is **single binary**: it is the thing that changed for
someone installing this, and it is checkable in one command.

---

## The post

I built orch to stop babysitting AI coding agents.

You write a spec, it becomes a `tasks.json` DAG, and orch walks the graph:
it works out which tasks are ready, dispatches each one to whichever CLI you
routed it to (`claude`, `codex`, `opencode`), isolates it in its own git
worktree, opens a PR, waits for CI, and retries or blocks on the result. There
is a budget guardrail so a bad night cannot spend a month's tokens, and a
dashboard that shows what is running.

It ran as a Python package for eleven releases. It is now a Go binary you
download and run, with the dashboard's React SPA embedded inside it. No
virtualenv, no pip, no `uvicorn` to keep alive.

```
curl -L https://github.com/hectorcanaimero/orch/releases/latest/download/orch_linux_amd64 -o orch
chmod +x orch && ./orch init my-project --template python-api
```

`[update with the v0.12.0 release]` — the exact install line depends on the
release assets, which do not exist yet at the time of this draft.

The rewrite is not finished and the post should not pretend it is. What
follows is what is true today.

---

## First comment (the one that should actually be read)

**Why Go.** Three reasons, in the order they mattered.

The first is the install. orch is a tool you hand to someone who is already
fighting their own toolchain; "create a virtualenv, then `pipx install`, and
by the way it needs Python 3.11" is a step where people leave. A single file
they can `chmod +x` is not a performance argument, it is a distribution one.

The second is that orch supervises child processes for hours. Python can do
that; the version that does it well is not much shorter than the Go one, and
it costs a runtime on every machine that runs it.

The third is the one I did not expect: **porting is the best bug-finding
technique I have used.** More on that below.

**What got thrown away.**

The Python package had four runtime dependencies: `pyyaml`, `rich`, `fastapi`
(pinned `<0.116` because 0.116+ regressed a `Request` annotation resolution we
depended on) and `uvicorn[standard]`. The Go binary has six direct module
dependencies, of which the ones doing real work are `cobra`, `yaml.v3`,
`modernc.org/sqlite` (a pure-Go SQLite, so there is no cgo and no libsqlite to
find) and the MCP SDK.

Gone with FastAPI: `jinja2` — the dashboard had already become a React SPA, so
the server-rendered templates were dead weight that still had to be shipped
and imported.

Also gone, for now: **three of the four dispatch backends.** Python can hand a
task to `claude`, `codex`, `opencode` or `agy`. The Go engine has one adapter,
for `claude`. A task routed to any other backend is skipped with
`backend-unavailable:<name>` rather than blocked, so a half-ported binary
still makes progress on the tasks it can run — but if your project routes
everything to `codex`, this binary does nothing useful for you yet. That is
the single biggest gap and it belongs in the first paragraph of any honest
comparison.

**What the Python version still does that the Go one does not.** Three
subcommands have no Go implementation yet: `findings`, `notify` and `stop`.
Twenty command surfaces are ported.

**The binary.** ~16 MB, linux/amd64, built with Go 1.27.1, stripped (`-s -w`),
with the dashboard SPA embedded — a 483 kB JS bundle plus its CSS and icons.
Comparing that against a "hello world" Go binary would be flattering and
wrong; comparing it against a Python install is not apples to apples either,
since the Python side needs an interpreter you probably already have.
`[update with the v0.12.0 release]` for the per-platform sizes.

---

## The part I would actually want to discuss

**25 numbered bugs in the Python version, and not one of them was found by a
test.**

They were found by someone trying to reproduce the behaviour in another
language and discovering there was no behaviour to reproduce — or that the
behaviour was not the one the code appeared to describe.

The Python suite is 1528 passing tests. It was green for every one of these.

A few, to show the shape rather than to list them:

- **A dashboard chart filtered on an event type nothing ever emitted.** The
  chart rendered, empty, for as long as it had existed.
- **A config key (`concurrency.per_file`) announced a cap nothing enforced.**
  It was in the config file, in the docs, and in nothing else.
- **The live event stream dropped events that shared a second.** Event
  timestamps have second precision; the tailer polled `WHERE ts > last_ts`,
  so an event written in the same second as the last one delivered — but after
  the poll that delivered it — was skipped and never re-sent. A dispatch and
  the block it causes land in the same second constantly. Reproduced against
  the real Python tailer: three events, two sharing a second, and the middle
  one never arrives.
- **A velocity figure counted row mtimes, not completions.** Every ETA on the
  dashboard was projected from it. A task finished three weeks ago and edited
  yesterday counted as yesterday's work.
- **The scaffolder wrote a router stub that made the command it recommended
  fail.** `orch init` printed "now run `orch router add-missing`", and running
  it broke the file further, because the stub ended with `{}`.

**Why the tests did not catch them** is the interesting half, and it is
consistent enough that we turned it into review rules. The repository's review
checklist has 29 rules; the last seven were written during this migration,
each from a specific failure:

| Rule | Written because |
|---|---|
| 23 — a test that normalises before comparing must justify each normalisation | Three tests kept passing over the bug they existed to catch. One asserted `"…: specs/" in text`, which `specs/specs/…` satisfies. One had a `deref(*string) string` helper flattening a Python golden's `null` to `""` — in the file whose job was catching `null` vs `""`. One compared timestamps as strings, where `+00:00` and `Z` spell the same instant. |
| 24 — a new test must be seen to fail | A test written after the fix and never seen red tests the author's model of the bug, not the bug. |
| 25 — a short-circuit is asserted by what it did not do | "Returns the right answer" does not distinguish a gate that skipped the database from one that queried it and ignored the result. |
| 26 — a golden carries a test of its own coverage | Regenerating a fixture can silently drop the boundary case while the comparison keeps passing. |
| 27 — parity expectations come from running Python, not reading it | `999_999` formats as `"1000.0k"`, not `"1m"`. `repr(1.0)` is `"1.0"`, not `"1"`. No careful reading survives those. |
| 28 — a shipped fixture beats an invented one | A hand-written sample tests the parser; the real file also tests that what we ship still parses. |
| 29 — run what you built and read its output once | A wizard's model-tier prompt rendered as `[agy/pro/claude/claude-opus-4-7/codex/gpt-5.6/…]` — unreadable, because the router keys are themselves slash-separated. Eight tests passed: each asserted that the summary *displayed* an answer, never that the prompt was legible. |

If there is one transferable thing in this post, it is rule 23. A test that
lower-cases, trims, sorts, or `or`s two acceptable spellings before comparing
has decided not to see a difference — and that is exactly where the bug is.

The second is rule 29, which is embarrassing in how basic it is: run the thing
and read the output. A green suite says nothing about output no test reads.

---

## Honest comparison

| | Python (v0.11.0) | Go (today) |
|---|---|---|
| Install | `pipx install orch`, needs Python ≥3.11 | one binary |
| Runtime deps | 4 | 0 (SQLite is pure Go, no cgo) |
| Dispatch backends | claude, codex, opencode, agy | claude only; others skip with `backend-unavailable` |
| Subcommands | 18 | 20 surfaces, minus `findings`, `notify`, `stop` |
| Dashboard | FastAPI + uvicorn + a built SPA on disk | `net/http`, SPA embedded in the binary |
| New in Go | — | an MCP server (7 tools), `orch explain` |
| Tests | 1528 | 863 test functions |

Two lines in that table deserve their caveats spelled out rather than left to
a footnote:

- **"0 runtime deps" is about the machine, not the repo.** The Go binary
  vendors its dependencies at build time. It is not fewer moving parts; it is
  the same parts, shipped.
- **863 vs 1528 is not "fewer tests, same coverage".** They count differently
  (Go test functions vs pytest cases, and many Go tests are table-driven with
  a dozen rows each), and the Go tree does not yet cover the three
  unimplemented subcommands or the three missing backends.

---

## Sources

Run in the repository at the commit this draft was written against.

| Number | Command |
|---|---|
| binary ~16 MB (16,105,735 bytes) | `make build && ls -l bin/orch` |
| Go 1.27.1, linux/amd64 | `go version` |
| stripped | `-s -w` in the `build` target of the `Makefile` |
| SPA bundle 483 kB | `make web` output (`index-*.js`) |
| 26 packages under `internal/` | `go list ./... \| grep -c internal/` |
| 863 Go test functions | `grep -rh "^func Test" --include="*_test.go" . \| wc -l` |
| 1528 Python tests | the "Tests" line in `CLAUDE.md`, kept current by the suite |
| 4 Python runtime deps | `[project.dependencies]` in `pyproject.toml` |
| 6 direct Go modules | the first `require` block in `go.mod` |
| 18 Python subcommands | `_SUBCOMMANDS` in `orchestrator/orch.py` |
| 20 Go command surfaces | rows in `docs/CLI.md` |
| 29 checklist rules | `grep -cE "^[0-9]+\. \*\*" .github/review/CHECKLIST.md` |
| rules 23–29 and their origins | the rules themselves, in `.github/review/CHECKLIST.md` |
| 25 numbered bugs | `docs/brainstorm/go-migration-notes/*.md` plus the frozen `go-migration-notes.md`; the series is shared across the lanes doing the port |
| 101 PRs merged since #91 | `gh pr list --state merged --limit 200 --json number --jq '[.[] \| select(.number >= 91)] \| length'` |
| one dispatch adapter | `internal/providers/` — `claude.go` is the only backend |

**Deliberately not in this post**, because nobody has measured them: startup
time, memory use, throughput, and any "N× faster" claim. If someone asks in
the thread, the honest answer is that the rewrite was about distribution and
correctness, and the performance question has not been asked yet.
