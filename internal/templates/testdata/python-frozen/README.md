# python-frozen/

`templates/` is a byte-for-byte copy of `orchestrator/templates/`, taken from
`main` at `5db7091` (2026-09-13) — the Python tree the last Python release is
cut from.

`TestGoTreeMatchesPython` diffs the embedded `files/` tree against it, so a
template cannot drift from what the last Python release scaffolds without
saying so, even after the Python tree is deleted.

**Never edit these files.** To change a template on purpose, edit `files/` and
add the path to `divergedFromPython` (or, for a file Python never had,
`goOnly`) in `templates_test.go` with the reason.
