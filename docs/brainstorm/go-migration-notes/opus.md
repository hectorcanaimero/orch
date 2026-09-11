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
