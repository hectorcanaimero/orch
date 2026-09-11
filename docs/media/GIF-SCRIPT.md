# The 30-second GIF — shooting script

This is the reproducible script for orch's hero GIF. It is also the **week-8
gate of the Go migration**: the Go binary has to reproduce this recording,
beat for beat, with the same commands and the same on-screen result. If a cut
below cannot be reproduced, the rewrite is not done.

Read it as a shot list, not as prose. Every cut names the exact command, what
must be visible on screen, and how long it stays there.

---

## 0. What the GIF has to prove

Four claims, in this order, because that is the order a skeptical viewer asks
them in:

1. **Setup is one command.** `orch init --template python-api` and you have a
   working project.
2. **It dispatches real work.** Tasks go out to a real CLI, in parallel, in
   isolated git worktrees.
3. **The work lands as a PR.** Green CI, not a diff pasted in a terminal.
4. **The client sees progress without asking.** The stakeholder view moves
   from one percentage to the next, with an ETA.

Anything that does not serve one of those four claims is cut.

---

## 1. Recording setup

| Thing | Value |
|---|---|
| Terminal size | 100×30 characters |
| Font | any mono at 16 px; no ligatures |
| Theme | dark background, light text |
| Shell prompt | `$ ` only — no git branch, no hostname, no timestamp |
| Capture | `asciinema rec` for the terminal, `agg` to convert to GIF |
| Browser | Chromium at 1440×900, no extensions, no bookmarks bar |
| Output | ≤ 30 s, ≤ 8 MB, 12 fps, infinite loop |

Set a clean prompt before recording:

```bash
export PS1='$ '
clear
```

Two recordings get stitched together: the terminal (cuts 1–4) and the browser
(cut 5). Do not try to film both at once — a split screen at GIF resolution is
unreadable.

### Provider and model

Record against **one provider only**: `claude`, model `claude-sonnet-4-6`
(routed as `claude/claude-sonnet-4-6`). Reasons:

- It is the model the `python-api` template already routes to, so nothing in
  the recording is hand-edited.
- Sonnet finishes the two demo tasks in ~60–90 s each, which is short enough
  that the time-lapse in cut 3 stays honest (see below).
- One provider keeps the dispatch log readable at 100 columns.

Do **not** record with `--mode semi`: the confirmation prompt adds a beat that
does not survive a 30-second cut.

### Honesty rules for the edit

- Time may be **compressed** (speed up cut 3), never **faked**. Nothing on
  screen may show a state the run did not actually reach.
- Do not re-order events. If CI went red and was re-dispatched, either record
  again or keep the red.
- The numbers in cut 5 must come from the run recorded in cuts 2–4.

---

## 2. The shot list

Total: **30 s**. Timings are cumulative.

### Cut 1 — `orch init` (0:00 → 0:05, 5 s)

```bash
orch init ~/demo/billing-api --template python-api
cd ~/demo/billing-api
```

**On screen:** the scaffolder's file list scrolling past, ending on the
"Next steps" block. Hold the last frame ~1 s.

**What the viewer has to notice:** `tasks.json`, `.orchestrator/config.yaml`,
`scripts/task-*.sh` and `.github/workflows/orch-ci.yml` all appear without a
single prompt.

> The repo must already have a git remote before cut 3 — `orch` degrades to
> "no worktrees, no PRs" without one and cut 4 has nothing to show. Create the
> repo off-camera:
> ```bash
> git init -q && git add -A && git commit -qm "chore: scaffold" \
>   && gh repo create billing-api --private --source=. --push
> ```

### Cut 2 — the plan (0:05 → 0:10, 5 s)

```bash
orch --dry-run
```

**On screen:** the plan table — task id, model, backend, dependencies. Two
tasks are dispatchable now, the rest are waiting on them.

**What the viewer has to notice:** orch knows the order. Nobody typed it.

### Cut 3 — dispatch (0:10 → 0:20, 10 s of screen time)

```bash
orch --max-tasks 2
```

This is the only cut that is **time-compressed**: the real run takes 2–4
minutes, the GIF shows 10 s. Speed up uniformly (`agg --speed`), never by
cutting frames out of the middle.

**On screen, in order:**

1. `worktree mode enabled; base_branch=main` in the startup log.
2. Two `dispatch` lines, different task ids, within a second of each other —
   this is the parallelism claim.
3. Two `success` lines.
4. Two `pr_created` lines, each with a PR URL.

**What the viewer has to notice:** two agents worked at the same time, in
different worktrees, and neither touched the other's files.

### Cut 4 — the PR (0:20 → 0:25, 5 s)

```bash
gh pr list
gh pr checks <the first PR number>
```

**On screen:** the PR list with both PRs, then the checks table with a green
check on `orch-ci`.

**What the viewer has to notice:** the output is a reviewable PR with passing
CI, not a pile of edits in the working tree.

Hold the green check for a full second before cutting. This is the payoff
frame of the terminal half.

### Cut 5 — what the client sees (0:25 → 0:30, 5 s)

Switch to the browser recording.

```bash
orch dashboard --profile stakeholder --token demo-token
```

Open `http://127.0.0.1:7420/stakeholder`, paste the token once (that beat is
edited out), and land on the summary.

**On screen:** the stakeholder view — see [`stakeholder.png`](stakeholder.png)
for the exact frame — and then the number **changing**: the two tasks from cut
3 finish, the page auto-refreshes (10 s cadence), and:

- "Overall progress" goes from one percentage to the next (e.g. 47% → 60%).
- "Done" counter increments by two.
- "ETA" updates.

**What the viewer has to notice:** nobody sent the client a status update. The
page moved on its own.

To get the transition on camera, start the dashboard **before** cut 3's run
finishes and record the browser while the last two tasks land. If the two
recordings cannot be interleaved in one take, run the dispatch twice: once for
the terminal capture, once (with the tasks reset via `orch reset`) for the
browser capture. The percentages must match between the two takes.

---

## 3. Reproducing the stakeholder frame

[`stakeholder.png`](stakeholder.png) is a real render of the SPA against a
seeded project — no mockup, no edited pixels. It is the frame cut 5 opens on.

The fixture behind it: a `python-api` project named `billing-api`, 15 tasks
across 4 phases, 9 done, 1 in progress, 1 blocked (the blocked reason is a
`comments[0].body` entry on the task — that is what the executive summary
quotes), and ~10 days of synthetic `events-*.jsonl` / `spend-*.jsonl` rows so
velocity, ETA and the spend sparkline have real signal to work from.

**No provider tokens are spent to produce it.** The state is written directly:

```bash
orch init /tmp/billing-api --template python-api
cd /tmp/billing-api
# 15 tasks across 4 phases in tasks.json, then:
orch task set --id F0.T1 --status done
orch task set --id F2.T2 --status blocked        # + a comments[] entry with the reason
orch task set --id F3.T1 --status in-progress
# events-*.jsonl and spend-*.jsonl seeded by hand so ETA and spend are non-empty
orch dashboard --profile stakeholder --token demo-token
```

Two deliberate choices in the capture, both documented so nobody mistakes them
for defaults:

- `dashboard.summary_language: en` and English `presentation.status_labels` are
  set in the fixture's `config.yaml`. **The shipped default for both is
  Spanish.** The README is in English, so the screenshot is too.
- The browser is given the bearer token before the first paint. A human pastes
  it into the SPA's token form instead; the rendered page is identical.

Capture with headless Chromium at 1440×1000, `device_scale_factor=2`,
full-page.

---

## 4. Checklist before publishing

- [ ] Under 30 s and under 8 MB.
- [ ] Two dispatch lines are visible in the same second (the parallelism claim).
- [ ] The CI check is green and legible at GIF resolution.
- [ ] The stakeholder percentage visibly changes; it is not two static frames.
- [ ] No token, no API key, no absolute path containing a real username.
- [ ] The repo URL on screen is a throwaway repo, not a client's.
