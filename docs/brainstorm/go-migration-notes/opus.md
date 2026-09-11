# Go migration notes — lane opus

Append-only. One entry per finding, newest last. Format and numbering follow `docs/brainstorm/go-migration-notes.md`; that file is now history and is not edited after 2026-09-12.

- **Go bug (no number — nothing in Python changes): `router.Load` silently
  discarded every route written below a `{}` line.** Found by sonnet wiring
  `router add-missing`; the same file shape as the Python half of bug 14, with
  a worse failure.

  `orch init` wrote `{}` as its empty router until #141. `AddMissing` appends a
  block mapping, and a block mapping after a complete flow document is a
  *second* YAML document. `yaml.Unmarshal` reads the first and ignores the
  rest — so `AddMissing` reported `added=[claude/x]`, wrote the file, and
  `Load` on that same file returned an **empty router with no error**. Every
  task using those models then failed as unrouted, pointing at the file that
  visibly contains them.

  PyYAML raises on the identical file. Go being quieter than Python is the
  wrong direction, and "reports success, discards the work, no error anywhere"
  is the failure mode that takes longest to trace: the operator is looking at
  a file whose contents contradict the error.

  Fix: `Load` decodes with `yaml.NewDecoder` and refuses anything after the
  first document, with a message naming the `{}` to delete; `AddMissing` drops
  a lone empty flow mapping from the header before appending, keeping every
  comment above it. Only an *empty* flow mapping alone on its line is removed —
  this repairs a stub, it does not reformat a hand-authored file. A file
  already corrupted by the old code is refused rather than guessed at.

  Every project scaffolded before #141 carries one of these stubs.

- **The embedded template tree is a second copy of data that has already
  produced two bugs.** Not a finding, a standing hazard, recorded because the
  next person to fix a template will only see one of the two places.

  `go:embed` cannot reach outside its own package directory, so
  `internal/templates/files/` duplicates `orchestrator/templates/` until the
  Python tree is deleted. That data has produced bug 12 (every `specRef`
  carrying a `specs/` prefix that `spec_root` already supplies) and half of
  bug 14 (the router stub closing its own YAML document). Both were invisible
  by reading and only showed up when something ran.

  `TestGoTreeMatchesPython` walks both trees and fails on a file that is in one
  and not the other, or present in both and different. `expo-mobile` is in an
  explicit four-entry `goOnly` allowlist. The test reads the Python tree by
  relative path, so it stops working the day that tree is removed — at which
  point this becomes the only copy and the test has nothing left to check.
  **Delete it then; do not weaken it now.**

- **`expo-mobile` is the one new feature in the Go port.** H-1e, the fifth
  canonical template, outstanding since before the migration began. Written
  only in Go, because Python is frozen — the one place where "no new features
  in Python" and "finish what was started" point in different directions.

  Two things in it are worth keeping if anyone rewrites it. The jest preset is
  named in the task description (`jest-expo`, because the default `jsdom`
  environment cannot render React Native components and the error it produces
  points nowhere near the cause), and the AGENTS.md says what CI cannot do:
  there is no simulator and no device on a runner, so a task whose acceptance
  criteria need a running app has to say how to verify it by hand.

- **Bug 15: a placeholder in the nextjs-saas AGENTS.md looked like a real
  JWT.** `NEXT_PUBLIC_SUPABASE_ANON_KEY=eyJhbGci...` in an illustrative
  `.env.local` block. Not a secret — a truncated placeholder — but `eyJhbGci`
  is the base64 of a JWT header, so every scanner reads it as one, and every
  other line in the same block uses an unmistakable fake (`pk_test_...`,
  `sk_test_...`, `whsec_...`). Now `example-anon-key-not-real`, in both copies
  of the file at once, which is what the sync test is for.

- **Every writer ported without its reader surfaces weeks later, blocking
  another lane.** Three times now, same shape:

  | writer landed | reader missing until | who it blocked |
  |---|---|---|
  | `RecordSpend` | #116 `SpendSince` | the budget gate (G2.2) |
  | run rows | #130 the three tallies | `latest_run` in `status --json` |
  | `RecordDispatch` | this PR `InFlightDispatches` | resume + reconcile (G3.3) |

  Each one looked complete at the time, because the thing being ported was a
  write path and the tests exercised the write. The parity fixture hid two of
  them further: it has no spend rows and empty run tallies, so an
  implementation that always answered zero passed both the fixture and
  `scripts/parity.sh`.

  Working rule from here (orch-98): **a PR that adds a write method to
  `Backend` adds its read in the same PR.** Not for symmetry — because the
  cost of the gap is paid by whoever needs the read next, and they pay it as a
  blocked task rather than as a missing method.

- **Divergence: the wizard inlines its choice hint only when the hint helps.**
  Python's `prompt` writes `[{'/'.join(choices)}]` unconditionally. For `[y/n]`
  and `[sqlite/file]` that reads well. For the model tier picker it does not —
  the router keys *are* slash-separated paths, so fifteen of them joined by a
  slash produce

  ```
  default premium model [agy/pro/claude/claude-opus-4-7/codex/gpt-5.6/gemini/…]
  ```

  where no reader can tell which slashes separate choices and which are inside
  one. The options are already printed above it, one per line, so the bracket
  adds only the confusion. Go inlines it when every choice is slash-free and
  there are at most six. `orch init` is interactive and its output is not
  compared by `scripts/parity.sh`, so nothing depends on the difference but a
  reader.

  **The eight wizard tests passed with the unreadable version.** None of them
  asserted on the prompt line; they checked the returned options and the
  summary. That is checklist rule 23 from the other side — not a normalisation
  that hid the difference, but a part of the output nobody ever looked at. It
  only surfaced because I printed a full transcript and read it. Two tests now
  cover it: one on the decision directly, one that the tier options appear one
  per line and are *not* joined into the prompt.

  The general form, worth more than the fix: **a green test suite says nothing
  about output no test reads.** Print it and look at it once.

- **The wizard treats exhausted input as an error, not as a yes.** If the line
  reader returned an empty string at EOF, every remaining question would take
  its default — including the H-7 confirm gate, whose default is `y`. Closing
  a pipe would scaffold a project nobody approved, which is the single outcome
  that gate exists to prevent. `io.EOF` comes back wrapped, and a test asserts
  that running out of answers confirms nothing.

- **Bug 17: the wizard asks for a budget preset, shows it in the confirm
  summary, and throws the answer away.** Found wiring the Go wizard's answers
  through to the files they are supposed to change.

  `_post_process_config` rewrites `state.backend`, `budgets_preset` and
  `spec_root` in the scaffolded config with three `re.sub(..., count=1)` calls.
  A regex that matches nothing changes nothing — and **none of the four shipped
  templates contains `budgets_preset`**. So for every templated project the
  wizard asks the question, prints the answer in the H-7 summary, takes the
  operator's "yes", and the project then loads with the packaged default.

  The severity is in the confirm gate, not the setting. A gate that displays a
  choice which then has no effect is worse than never asking: the operator has
  been shown their answer and told it took. The same code path works for a
  blank project, whose packaged config.yaml does have the key, which is why it
  has never been noticed.

  Go appends the key with a comment when the file lacks it. A nested key —
  `backend:` under `state:` — is still never appended, because a bare
  `backend:` at the end of the file would be a different, top-level setting.

  Not fixed in Python: it is a one-line `re.sub` fallback, but the tree is
  frozen and the Go side no longer has the bug.

- **The wizard collected three answers and returned none of them.** My own,
  caught before the PR merged and worth recording because of how it hid.

  `Wizard` read `state backend`, `budget preset` and `spec root` into locals,
  printed them in the summary, and returned an `Options` that had no fields
  for any of them. Eight tests passed: they asserted the summary *displayed*
  each answer, which it did. Nothing asserted the answer *took effect*, and
  for that to be checked at all the test has to look at the file the scaffold
  writes, not at the wizard's output.

  It only surfaced because I went to port `_post_process_config` and found
  there was nothing to pass it. The shape to remember: **a test that an
  interactive tool displayed a choice is not a test that the choice was
  applied**, and the two live in different files.

- **Bug 18: `atomize` computed the spec ref against a root nothing else
  agrees with.** `specs_root` defaulted to `<project-root>/docs`;
  `prompt_builder` resolves a task's `specRef` against `spec_root` from
  config.yaml, default `specs`; and `orch init` creates `specs/` and tells
  the operator to write their first spec there. The `docs/` default has no
  written justification anywhere — not the manual, not the init banner — so
  it is the default that contradicts the product.

  With the two roots disagreeing, `_relpath_for_spec_ref` fell to its
  "outside the root" branch on *every* run and returned the bare filename.
  That is right by accident for a spec directly under the spec root, which is
  why it was never noticed — and wrong for one in a subdirectory:

  ```
  file on disk : <project>/specs/api/auth.md
  specRef      : auth.md#F0.1.T1          ← the "api/" is gone
  prompt says  : specs/auth.md#F0.1.T1    ← does not exist
  ```

  Organising specs by area is the first thing anyone does past three of them.

  Fixed by making `specs_root` default to `project_root / cfg["spec_root"]`.
  `--specs-dir` still wins. The `.name` fallback stays for a genuine
  `--file /elsewhere.md`, but now warns on stderr that the ref lost its
  directory — until this, that branch ran silently on every invocation, which
  is what made a wrong ref look like a working one.

  **Migration**, for a project that really does keep its specs in `docs/`:
  pass `--specs-dir docs`, or set `spec_root: docs` in config.yaml.

- **Bug 19: the spec parser read fenced code blocks as spec content.**
  **A fenced block is documentation *about* the format, never content.**

  `specs/README.md`, which `orch init` writes, documents the minimum format
  inside a ```` ```markdown ```` fence. The parser matches headers line by
  line and knows nothing about fences, so `orch atomize --apply` with no
  `--file` on a project straight out of `init` imported the example as two
  real tasks — "Setup monorepo" and "Root README".

  Found while fixing bug 18, and hidden behind it: `specs_root` pointed at
  `docs/`, which does not exist, so the scan found nothing at all. Fixing one
  root made the other visible — the ordinary way a second bug in the same path
  shows up, the first was stopping the code from running.

  The fix is in the parser, not in the README: any spec that documents its own
  format inline has the same problem, and orch's README is just the one that
  ships. **The Go port inherits it** — `internal/atomize`'s parser is faithful
  to the original, so it needs the same skip and the same test.

- **The gap these five bugs came through was coverage by ROUTE, not by line.**
  12, 14, 17, 18 and 19 all live on the path a new user walks in their first
  five minutes — `init` → write a spec → `atomize` → `dry-run` — and until
  now not one test walked it end to end. `init_cmd.py` and `atomize.py` were
  both well covered by line; every test asserted on a file one of them wrote,
  and the bugs were all in how the files agreed with each other.

  There are three such tests now, in three different files: Python's
  `test_scaffolded_project_dry_runs_clean` and
  `test_spec_ref_resolves_where_the_prompt_looks`, and Go's
  `init.txtar`, which scaffolds and then runs `orch validate` on the result.
