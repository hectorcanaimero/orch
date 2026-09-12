# `internal/vcs/testdata/gh`

Real `gh` output, captured off the wire. Nothing here is hand-written.

## Why these exist

The CI-status tests used to feed shapes real `gh` never emits:

```json
[{"state": "completed", "conclusion": "success"}]
```

written from what the Python code *reads* rather than from what the CLI
*writes*. They passed for months while the command underneath could not run at
all — `gh pr checks` has no `conclusion` field, so asking for one is a usage
error and `gh` exits non-zero. Both binaries swallowed that into "pending", so
CI never resolved for anybody. See the `opus-2.md` migration note.

## `2.100.0/`

Captured with `gh version 2.100.0 (2026-09-03)` against merged PRs of this
repository. The version is in the path because the field set is the thing
being pinned, and it is `gh`'s to change.

| file | what it is |
|---|---|
| `pr-checks-all-pass.json` | `gh pr checks 209 --json name,state,bucket,event,workflow` — nine checks, every `state` is `SUCCESS` |
| `pr-checks-with-neutral.json` | the same for #208 — eight `SUCCESS` and one `NEUTRAL`, which is what proves the conclusion map is reached |
| `pr-checks-conclusion-rejected.txt` | what `gh pr checks 209 --json state,conclusion` actually prints, and the field list it offers instead |

The last one is the evidence, not a fixture: it is the exact refusal that both
binaries have been turning into "pending" since the feature was written.

## Regenerating

```bash
gh pr checks <a merged PR> --json name,state,bucket,event,workflow \
  > 2.100.0/pr-checks-all-pass.json
```

Capture against a **merged** PR of this repository, so the states are settled
and the file does not change under the test. If `gh` gains or renames a field,
add a directory for the new version rather than editing these — the point of
the path is that a reader can tell which CLI produced what.
