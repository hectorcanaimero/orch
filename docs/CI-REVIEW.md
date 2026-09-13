# CI review

Every pull request gets an automated review from Gemini. It leaves one comment
and two status checks. The reviewer itself has never merged anything and cannot
— it is handed no credentials. What can merge is the `policy` job beside it,
for PRs that touch no protected zone, and only while the `ORCH_AUTOMERGE`
repository variable says `on`.

---

## What runs

`.github/workflows/review.yml`, on `pull_request` (`opened`, `synchronize`,
`reopened`), for branches in this repository only. Two jobs:

| Job | Check it publishes | What it does |
|---|---|---|
| `gemini-review` | `gemini-review` | Sends the diff + `.github/review/CHECKLIST.md` to Gemini, gets JSON back, posts the comment |
| `policy` | `automerge-eligible` | Decides whether the PR qualifies for auto-merge, and arms or disarms GitHub's auto-merge accordingly |

A new push cancels the review of the commit it replaced.

### What the reviewer can and cannot do

The reviewer is handed a diff and a checklist. That is all it gets:

- **No GitHub token.** The step that runs Gemini has no credentials for this
  repo. It cannot comment, push, merge, or read anything it was not given.
- **No tools.** The Gemini CLI runs with `"tools": {"core": []}` — no shell,
  no file access, no MCP servers.
- **The workflow publishes on its behalf.** The comment and the checks are
  written by the job, from the JSON the model returned, after that JSON has
  been validated against a fixed schema.

`pull_request`, never `pull_request_target`: the latter would run this with a
writable token in the base repo's context while reading a contributor's diff.
Both jobs also skip PRs from forks outright.

---

It never opens a browser, so nothing about a rendered page is covered here —
a route that answers 200 with the wrong body, a component that throws on
first render, a login form shown to a profile that needs no token. See
[`UI-CHECKS.md`](UI-CHECKS.md) for how those are checked.

## Before you open the PR

Run what you built and read its output once (checklist rule 29). The reviewer
reads a diff; it cannot see that a prompt renders as an unreadable run-on or
that a banner names a file that is not there. If the PR changes anything a
user sees — a prompt, a banner, an error, a report — paste it in the body.

A green suite is not evidence about output no test reads.

## Reading the comment

One comment per PR, edited in place on every push — so the newest review is
always the one you are looking at, and it never buries the human conversation.

```
## Gemini review

🛑 Changes requested.

Two issues in the dispatch loop; the rest of the diff is clean.

**Blocking**

- `internal/engine/loop.go:88` — **os/exec timeout**: no context deadline on the provider call.

**Minor**

- `internal/cli/run.go` — **table tests**: three near-identical test cases.

🔒 Not auto-merge eligible — touches protected paths: internal/engine/loop.go.
```

**Verdicts.** `approve` — nothing to flag. `comment` — minor findings only.
`request_changes` — at least one blocking finding.

**Blocking vs minor.** Blocking means a rule in
[`.github/review/CHECKLIST.md`](../.github/review/CHECKLIST.md) was broken, and
every finding names the rule it came from. If a finding does not name a rule
you can point at, the review is wrong — and the fix is a PR against the
checklist, not an argument in the thread. That is why it is versioned.

**The check.**

| `gemini-review` | Meaning |
|---|---|
| ✅ success | Verdict was `approve` or `comment` |
| ❌ failure | Verdict was `request_changes`, **or the review did not produce one** — the CLI failed, the reviewer returned nothing twice, or it answered off-schema |
| ⚪ neutral | **Only** "no API key" — a repository setting, not a review outcome |

**A review that did not happen is a failure, not a neutral**, and that is a
deliberate change. A neutral check leaves the PR reading "clean and
mergeable" in the checks summary, so an unread verdict looks exactly like an
approval at a glance — #206 nearly merged that way. The only thing that stays
neutral is a missing API key, because that is a repository that has not
switched the reviewer on rather than a review that went wrong.

The failure title says which happened:

- **"Reviewer failed"** — the Gemini CLI step did not complete.
- **"Reviewer returned nothing"** — it ran and produced an empty response,
  twice. This one is non-deterministic (the CLI does it occasionally on a long
  prompt), so the workflow retries once before giving up. If it starts
  happening often, pin a stronger model with `GEMINI_REVIEW_MODEL` — the empty
  responses observed so far came from `gemini-3.1-flash-lite`, the CLI's
  default.
- **"Unreadable verdict"** — it answered, but not to the schema.

### When the verdict is unreadable, read what it said

The raw reviewer output is uploaded as an artifact **before** parsing, so a
parser crash cannot lose it:

```bash
gh run download <run-id> -n reviewer-raw-<pr>-<attempt>
cat reviewer-raw.txt
```

Kept for 7 days. **Download it before re-running.** A re-run may well produce
a readable verdict and hide the reason the first one was not — the unreadable
verdicts seen so far were a finding about the PR itself (its title) with no
`file` to attach it to, which the parser required and now treats as optional,
rendering it as "(PR)".

Neither neutral nor an unread failure is a pass. Both mean "nobody reviewed
this".

**It is advisory.** Nothing is enforced until branch protection is turned on
(see below), and a human owns the merge either way. A wrong `request_changes`
is a reason to merge anyway and open a PR against the checklist.

---

## Auto-merge eligibility

`.github/review/policy.json` holds the rules. A PR is eligible when **all** of:

- **≤ 400 changed lines** (additions + deletions)
- **no protected paths touched** — `.github/**`, `go.mod`, `go.sum`,
  `.goreleaser.yaml`, `internal/engine/**`, `internal/state/**`,
  `internal/providers/**`, `internal/dashboard/auth*`
- **no `needs-human` label**

The reasoning behind each protected path is in `policy.json` itself.

This publishes the `automerge-eligible` check (`success` when eligible,
`neutral` with the reason when not) and a line in the comment — and, when
`ORCH_AUTOMERGE` is `on`, arms GitHub's auto-merge on the eligible ones.
Arming is not merging: GitHub holds the PR until every required check passes,
so the reviewer's verdict is what releases it. An ineligible PR is never
armed, and one that *was* armed and has stopped qualifying is disarmed. See
[Branch protection and auto-merge](#branch-protection-and-auto-merge-g47).

To force a human review regardless of size: `gh pr edit <N> --add-label needs-human`.

The rules are covered by fixtures. Run them anywhere:

```bash
bash .github/review/policy.test.sh        # the eligibility rules
bash .github/review/parse-review.test.sh  # the verdict parser
```

Both also run inside their own jobs, before the rules they test are trusted.

---

## Setup

One secret, set once by someone with admin on the repo:

```bash
gh secret set GEMINI_API_KEY --repo hectorcanaimero/orch
# paste the key from https://aistudio.google.com/apikey
```

Without it, `gemini-review` publishes a `neutral` check saying so and posts no
comment. It does not fail the build, and it does not quietly pass either.

### Choosing a model

The workflow leaves `gemini_model` empty, which means the Gemini CLI picks its
own current default — the same thing the action's own examples do. Pinning a
model name in a workflow file is a guess that goes stale.

To pin one:

```bash
gh variable set GEMINI_REVIEW_MODEL --body 'gemini-2.5-pro' --repo hectorcanaimero/orch
gh variable delete GEMINI_REVIEW_MODEL --repo hectorcanaimero/orch   # back to the default
```

The action itself is pinned to a commit SHA (`v0.1.22`) because it is the step
that receives the API key. Bumping it is a deliberate edit, not a surprise.

---

## Turning it off

Least invasive first:

```bash
# 1. Stop reviewing, keep everything in place: delete the key.
gh secret delete GEMINI_API_KEY --repo hectorcanaimero/orch

# 2. Disable the workflow entirely (checks stop appearing).
gh workflow disable "PR review" --repo hectorcanaimero/orch
gh workflow enable  "PR review" --repo hectorcanaimero/orch

# 3. Skip it for one PR.
gh pr edit <N> --add-label needs-human   # blocks eligibility, review still runs
```

Deleting `.github/workflows/review.yml` also works and is the honest option if
the thing turns out not to earn its keep.

---

## Branch protection and auto-merge (G4.7)

The code side ships enabled. What makes it act is two repository settings and
one variable, none of which live in the repo — apply them by hand, in this
order.

### 1. Protect `main`

```bash
gh api -X PUT repos/hectorcanaimero/orch/branches/main/protection \
  --input - <<'JSON'
{
  "required_status_checks": {
    "strict": true,
    "contexts": ["go-test", "go-lint", "gemini-review"]
  },
  "enforce_admins": false,
  "required_pull_request_reviews": null,
  "restrictions": null,
  "allow_force_pushes": false,
  "allow_deletions": false
}
JSON
```

Required checks match on **name**, and a name that matches nothing never
arrives — the PR waits on it forever. So the list above is the job names as
they are actually written today:

| Context | Where it comes from |
| --- | --- |
| `go-test` | the `go-test` job in `.github/workflows/go.yml` |
| `go-lint` | the `go-lint` job in the same file — it is `go-lint`, not `lint` |
| `gemini-review` | a check run this workflow publishes through the API; there is no job by that name |

`parity` (Go against Python on the same project) and `smoke` (the Python wheel)
were jobs until G7.5 archived the Python line on `python-legacy`; neither
exists any more, and a protection rule still naming either would hold every PR
forever. `automerge-eligible` is deliberately **not** required: it publishes
`neutral` when a PR needs a human, and `neutral` does not satisfy a required
check, so requiring it would block exactly the PRs it is meant to route to a
person.

`go.yml` does not filter on `paths`, so a docs-only PR does run `go-test` and
can genuinely go green. Nothing to special-case.

### 2. Let PRs merge themselves

```bash
gh api -X PATCH repos/hectorcanaimero/orch --field allow_auto_merge=true
gh variable set ORCH_AUTOMERGE --body 'on' --repo hectorcanaimero/orch
```

Both are needed. Without `allow_auto_merge` the `Arm auto-merge` step fails
with a GraphQL error that names no cause — the step turns it into an
annotation pointing back here, but the fix is the flag.

### The switch

`ORCH_AUTOMERGE` is global and reversible:

```bash
gh variable set ORCH_AUTOMERGE --body 'off' --repo hectorcanaimero/orch
```

Anything other than `on` — including the variable not existing — means off.
The `policy` job then not only stops arming but **disarms** what is already
armed, on the next event each open PR receives. That matters: auto-merge is a
standing instruction stored on the PR, so a switch that only stopped arming
would leave yesterday's eligible PRs merging themselves today. The same step
covers a PR that was eligible and stopped being one, when a later push added
a protected path or someone applied `needs-human`.

To take one PR out without touching the switch:

```bash
gh pr edit <N> --add-label needs-human   # ineligible; disarmed on the next push
gh pr merge <N> --disable-auto           # right now, this PR only
```

### Worth knowing

- A `neutral` conclusion does **not** satisfy a required check. A missing
  `GEMINI_API_KEY` therefore blocks merges rather than waving them through —
  the right way round, but it does make the key load-bearing.
- `enforce_admins: false` leaves you a way out when the reviewer is wrong and
  the fix is urgent. It also means "no direct pushes to `main`" holds for
  everyone *except* admins; set it to `true` if you want that literally, and
  accept that the escape hatch closes with it.
- `strict: true` requires a PR to carry the tip of `main`. Every merge
  therefore re-runs the checks of the PRs still open, and they land one at a
  time. That is fine at this volume; if it stops being fine, `strict: false`
  or a merge queue is the trade.
- Protected zones are enforced by *not arming* the PR, not by GitHub. A human
  still has to press merge, and branch protection still holds the required
  checks — but nothing stops an admin merging a protected-zone PR without a
  second pair of eyes. Add `required_pull_request_reviews` or a CODEOWNERS
  file the day this repo has more than one maintainer.
- Both jobs skip fork PRs, so `gemini-review` never appears on one and it can
  never satisfy its required check. Fork contributions need an admin merge
  until that is addressed.

---

## Related

- [`.github/review/CHECKLIST.md`](../.github/review/CHECKLIST.md) — the rules the reviewer applies
- [`.github/review/policy.json`](../.github/review/policy.json) — the eligibility rules
- [`docs/brainstorm/go-migration-notes.md`](brainstorm/go-migration-notes.md) — the rewrite this CI is being built for
