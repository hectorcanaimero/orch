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
