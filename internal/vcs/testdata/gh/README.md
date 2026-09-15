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
| `pr-view-state-merged.json` | `gh pr view <PR 277's URL> --json state` — a merged PR, `"MERGED"` (#255) |
| `pr-view-state-closed.json` | the same for #50, the one PR of this repository closed without merging, `"CLOSED"` |
| `pr-view-state-open.json` | the same for #281 while it was open, `"OPEN"`: the capture is a snapshot, so the PR merging later does not change it |
| `issue-list.json` | `gh issue list --state all --limit 3 --json number,title,body,labels,state,url,createdAt` — three real issues of this repository |
| `issue-comments-no-marker.json` | `gh api --paginate repos/hectorcanaimero/orch/issues/279/comments` — merged PR #279: the Gemini reviewer's comment (`<!-- orch:gemini-review -->`) and two replies, none with `<!-- orch:ci-review -->` |
| `issue-comments-with-marker.json` | `gh api --paginate repos/hectorcanaimero/orch/issues/290/comments`, captured right after a real `orch ci review --provider claude --post --pr 290` had created its comment there and a second run had edited it. Holds that `<!-- orch:ci-review -->` comment (id `5678662484`) and the Gemini reviewer's |

`issue-comments-no-marker.json` is the list `vcs.UpsertComment` reads. It holds
one marked comment, so it serves both paths: looked up with the ci-review marker
it finds none and POSTs, and with the Gemini reviewer's marker it finds comment
`5677503661` and PATCHes it.

`issue-comments-with-marker.json` is from an **open** PR, since no merged PR had an `orch ci review`
comment yet. Both of its comments may be edited later on GitHub, but the
file keeps what gh returned at capture time.

**Redacted in both files:** review findings quoted in the comment bodies named
`/home/u/.local/bin/orch`, a made-up path from a test, and the checklist's
`/home/<name>` pattern. Those strings were rewritten to `<home>/` and
`<home dir>` so the files never match the personal-path check. Nothing else was
touched, and no test reads those bodies.

`issue-list.json` is captured from **closed** issues, and says `"state":
"CLOSED"` three times — not because `orch sync issues` reads closed issues by
default (it reads open ones), but because this repository has none open: all 37
of its issues are closed, and a capture of what is actually there beats an
open-state payload edited by hand into looking real. What the fixture is for is
the *shape* — the label objects (`id`, `name`, `description`, `color`), the
UPPERCASE `state`, a multi-paragraph markdown `body` with fenced code in it —
and that shape does not depend on whether an issue is open. The mapping tests
exercise state filtering through `gh`'s own `--state` flag, which is where it
happens: the filtering is server-side, so there is nothing client-side for a
fixture to prove.

The `pr-checks-conclusion-rejected.txt` entry is the evidence, not a fixture: it is the exact refusal that both
binaries have been turning into "pending" since the feature was written.

## Regenerating

```bash
gh pr checks <a merged PR> --json name,state,bucket,event,workflow \
  > 2.100.0/pr-checks-all-pass.json
```

```bash
gh pr view <a merged PR's URL> --json state > 2.100.0/pr-view-state-merged.json
```

```bash
gh issue list --state all --limit 3 \
  --json number,title,body,labels,state,url,createdAt \
  > 2.100.0/issue-list.json
```

Capture against **settled** objects of this repository — a merged PR, closed
issues — so the states do not change under the test. If `gh` gains or renames a field,
add a directory for the new version rather than editing these — the point of
the path is that a reader can tell which CLI produced what.
