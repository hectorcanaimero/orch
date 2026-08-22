# orch findings — dogfooding loop

Every agent running under orch — atomizers, dispatchers, dashboards, your
own sub-agents — will notice bugs, missing features, and quirks of both
orch itself and the project it's working on. `orch findings` is the paved
path from "the agent noticed something" to "a real GitHub issue on the
orch repo".

Three verbs cover the whole flow:

- **`orch findings capture`** — the agent writes it down.
- **`orch findings review`** — a human reads it and checks for duplicates.
- **`orch findings publish`** — a human ships it to GitHub.

There are two more verbs for hygiene: `list` (see everything) and `dismiss`
(mark noise so nobody publishes it later).

---

## When an agent should capture a finding

- **`type: bug`** — orch or the project misbehaves. Include a repro / stack.
- **`type: fix`** — a subtle regression risk or a small quality-of-life
  correction. Not a full feature.
- **`type: feature`** — a request or improvement.

Always classify **`about`**:

- **`orch`** — the finding is about the orch tool itself. Publishable to
  the orch repo.
- **`project`** — the finding is about the user's own codebase. It stays
  local and CANNOT be published to the orch repo (hard gate). The operator
  moves those to their own tracker manually.

**`confidence`** guides the human reviewer:

- `low` — hunch / partial repro. Cannot publish unless `--force`.
- `medium` — clear observation with evidence.
- `high` — confirmed bug / well-scoped feature ask.

---

## Agent prompt snippet (copy-paste ready)

Drop this into an agent's system prompt so it knows when — and how — to
call `orch findings`:

```
You are running under orch. Whenever you notice a bug in orch itself, a bug
in this project, a missing feature that would have unblocked you, or a
non-obvious quirk worth reporting — capture it BEFORE moving on:

    orch findings capture \
      --type <bug|fix|feature> \
      --about <orch|project> \
      --summary "one-line human title" \
      --evidence "file:line refs, log excerpts, or repro steps" \
      --confidence <low|medium|high>

Rules:
- summary must be ONE line, human-readable, no punctuation trickery.
- use about=orch ONLY for issues with the orch tool. use about=project for
  anything about the codebase you're working on.
- if you're guessing, use confidence=low. don't inflate.
- evidence is where you dump the actual evidence — paths, line numbers,
  log excerpts, symptom strings, repro steps.
- duplicates are refused automatically at the local hash level, so you
  can re-run the command safely.

Do NOT run `orch findings publish`. That's a human decision.
```

---

## Operator workflow

### 1. See what's been captured

```bash
orch findings list                          # everything
orch findings list --status pending         # not yet decided
orch findings list --about orch             # only publishable candidates
orch findings list --json | jq .            # for pipelines
```

### 2. Review one

```bash
orch findings review <id>                   # id or unique prefix
orch findings review a1b2c3 --json
```

Review shows the full finding AND runs `gh api search/issues` against the
target repo, printing every match with a Jaccard word-overlap ratio.
Anything ≥ 0.6 will be flagged again at publish time.

### 3. Publish

```bash
# Dry-run first — inspect the guardrails without hitting GitHub for real
orch findings publish <id> --dry-run

# Actual publish (asks for TTY confirmation)
orch findings publish <id>

# Non-interactive (CI-style) — skips only the FINAL "y/N" prompt
orch findings publish <id> --yes
```

### 4. Dismiss noise

```bash
orch findings dismiss <id> --reason "not actionable"
```

---

## Guardrails (what publish enforces)

`orch findings publish` runs every check before it opens an issue:

| Check | Behavior |
|---|---|
| Not found | Exit 2 |
| Already published | No-op, print existing URL |
| `about: project` | Refuse. Exit 2. Belongs on your own tracker. |
| `confidence: low` | Refuse unless `--force`. Exit 2. |
| Rate limit (3/hour default) | Refuse. Exit 1. |
| GitHub dedup (overlap ≥ 0.6) | Refuse. Exit 1. `--force` overrides. |
| Not a TTY, no `--yes` | Refuse. Exit 130. |
| User answers "N" | Refuse. Exit 130. |
| `--dry-run` | Report only. No writes. |
| Label `auto-reported` missing | Auto-create (idempotent). |

Every check has a test in `orchestrator/tests/test_findings_publish.py`
that would catch its removal.

---

## Config knobs

`orchestrator/config.yaml`:

```yaml
findings:
  publish_repo: "hectorcanaimero/orch"      # default target
  publish_rate_limit_per_hour: 3            # 0 disables
  label: "auto-reported"                    # created on demand
  min_publish_confidence: "medium"          # low can't publish (unless --force)
```

CLI flags override on a per-invocation basis: `--repo`, `--force`,
`--yes`, `--dry-run`.

---

## Storage layout

- **sqlite backend**: table `findings`, applied via migration
  `002_findings.sql` (bumps `PRAGMA user_version` to 2).
- **file backend**: `<state_dir>/findings.jsonl`, one row per line.

Both are multitenant by `project_id`.

---

## Exit codes at a glance

| Command | 0 | 1 | 2 | 3 | 130 |
|---|---|---|---|---|---|
| `capture` | ok | — | duplicate | validation error | — |
| `list` | ok | — | — | — | — |
| `review` | ok | — | not found | — | — |
| `publish` | published / dry-run / already | rate-limit / dedup / gh error | refused (about=project or low confidence) | — | user cancelled |
| `dismiss` | ok | error | not found | — | — |

---

## Design details

Full technical design: [docs/design/sprint-e-1-dogfooding-loop.md](design/sprint-e-1-dogfooding-loop.md).
