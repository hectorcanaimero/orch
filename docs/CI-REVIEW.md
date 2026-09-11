# CI review

Every pull request gets an automated review from Gemini. It leaves one comment
and two status checks. It has never merged anything and, as shipped, it cannot.

---

## What runs

`.github/workflows/review.yml`, on `pull_request` (`opened`, `synchronize`,
`reopened`), for branches in this repository only. Two jobs:

| Job | Check it publishes | What it does |
|---|---|---|
| `gemini-review` | `gemini-review` | Sends the diff + `.github/review/CHECKLIST.md` to Gemini, gets JSON back, posts the comment |
| `policy` | `automerge-eligible` | Decides whether the PR *would* qualify for auto-merge. Informational — nothing acts on it yet |

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
| ❌ failure | Verdict was `request_changes` |
| ⚪ neutral | The review **did not run** — no API key, the CLI failed, or the model returned something that was not the required JSON |

Neutral is never a pass. It is the state that says "nobody reviewed this".

**It is advisory.** Nothing is enforced until branch protection is turned on
(see below), and a human owns the merge either way. A wrong `request_changes`
is a reason to merge anyway and open a PR against the checklist.

---

## Auto-merge eligibility

`.github/review/policy.json` holds the rules. A PR is eligible when **all** of:

- **≤ 400 changed lines** (additions + deletions)
- **no protected paths touched** — `.github/**`, `go.mod`, `go.sum`,
  `.goreleaser.yaml`, `internal/engine/**`, `internal/state/**`,
  `internal/providers/**`, `internal/dashboard/auth*`, and while the Python
  tree exists: `orchestrator/dispatcher.py`, `orchestrator/state/**`,
  `orchestrator/dashboard/middleware.py`, `pyproject.toml`
- **no `needs-human` label**

The reasoning behind each protected path is in `policy.json` itself.

Today this only publishes the `automerge-eligible` check (`success` when
eligible, `neutral` with the reason when not) and a line in the comment.
**Nothing merges automatically.** The step that would act on it is written and
commented out at the bottom of the `policy` job, so what gets enabled in G4.7
is reviewable now.

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

## Branch protection — G4.7, not yet applied

These are the commands that will make the review binding. **Do not run them
now**; they are here so the change is reviewable before it happens.

```bash
gh api -X PUT repos/hectorcanaimero/orch/branches/main/protection \
  --input - <<'JSON'
{
  "required_status_checks": {
    "strict": true,
    "contexts": ["smoke", "gemini-review"]
  },
  "enforce_admins": false,
  "required_pull_request_reviews": null,
  "restrictions": null,
  "allow_force_pushes": false,
  "allow_deletions": false
}
JSON
```

Then, to let eligible PRs merge themselves:

```bash
gh api -X PATCH repos/hectorcanaimero/orch --field allow_auto_merge=true
gh variable set ORCH_AUTOMERGE --body 'on' --repo hectorcanaimero/orch
```

and uncomment the `Enable auto-merge` step in the `policy` job, which also
needs `pull-requests: write` on that job.

Three things worth knowing before that day:

- `smoke` is the job name in `ci-build.yml`; `gemini-review` is the check name
  this workflow publishes. Required checks match on name, so renaming either
  silently stops enforcing it.
- A `neutral` conclusion does **not** satisfy a required check. So a missing
  API key blocks merges rather than waving them through — which is the right
  way round, but it does mean the key becomes load-bearing.
- `enforce_admins: false` leaves you a way out when the reviewer is wrong and
  the fix is urgent.

---

## Related

- [`.github/review/CHECKLIST.md`](../.github/review/CHECKLIST.md) — the rules the reviewer applies
- [`.github/review/policy.json`](../.github/review/policy.json) — the eligibility rules
- [`docs/brainstorm/go-migration-notes.md`](brainstorm/go-migration-notes.md) — the rewrite this CI is being built for
