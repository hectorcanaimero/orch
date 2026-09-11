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
