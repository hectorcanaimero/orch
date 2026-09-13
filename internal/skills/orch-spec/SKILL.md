---
name: orch-spec
description: Use when an architecture under docs/arch/ needs to become dispatchable work for orch (step 3 of orch-plan). Writes specs/fN-<slug>.md in the exact markdown format orch atomize parses — phase, package and task headers with Model, Estimate, Reason, Dependencies and Files — one agent-sized task per header. Never edits tasks.json.
---

# orch-spec — architecture → atomizable spec

Third stage of **PRD → ARCH → SPEC → TASKS**. A spec is the one document that
is both read by a human and parsed by a program: `orch atomize` turns every
task header in it into a `tasks.json` entry, and when orch dispatches that task
the agent's prompt carries its title, its description, its `Files` as
*Files you may write*, and a *Spec ref (READ FIRST)* pointing back at this very
section. **Write each task as the complete brief for an agent that has read
nothing else.**

## Inputs

- The architecture: `docs/arch/NNN-<slug>.md`, especially *Work breakdown* and
  *Interfaces*. No architecture → run `orch-arch` first.
- The model routes: `.orchestrator/model_router.yaml`. Every `Model` you write
  should be one of its top-level keys, spelled exactly. A project scaffolded
  without a template starts with no routes; then use the `<backend>/<model>`
  form the router expects (`claude/claude-sonnet-4-6`,
  `codex/gpt-5.6`, `opencode/...`) and `orch-tasks` adds the routes.
- The existing task ids: `orch tasks` and every file already in `specs/`.
- The project id for the frontmatter: the first line of `orch explain`
  (`Project <id>`). A different `project_id` makes atomize warn on every run.

## Where it goes

`specs/f<N>-<slug>.md`, **one file per phase**. `specs/` is the project's
`spec_root` (from `.orchestrator/config.yaml`; check it if the project changed
it). `orch atomize` walks that directory and computes each task's `specRef` as
the file's path relative to it, so do not nest specs somewhere else.

## Format

The parser is line-based and forgiving: **anything that does not match is
dropped without an error**, so a typo loses a task silently. Follow this
shape exactly.

```markdown
---
type: spec
project_id: sample-project
phase: 1
version: 0.1
depends_on:
  - docs/arch/001-auth.md
consumed_by:
  - orch-atomizer
generated_by: orch-spec
generated_at: 2026-09-13
title: Auth core
---

# F1 — Auth core

Sign-in, sessions and the user model everything else depends on.

## F1.1 — Package: domain

### F1.1.T1 — User and Session types

Define the User and Session types and their validation, exactly as the
Interfaces section of docs/arch/001-auth.md fixes them. No persistence.

Done when: both types exist with constructors that reject an empty email
and an expired session, and unit tests cover each rejection.

- **Model**: claude/claude-sonnet-4-6
- **Estimate**: 2h
- **Reason**: Plain types and validation; standard tier is enough.
- **Dependencies**:
- **Files**:
  - `src/auth/types.ts`
  - `src/auth/types.test.ts`

## F1.2 — Package: api

### F1.2.T1 — POST /session

Implement sign-in against the User type from F1.1.T1: verify the password
hash, create a Session, return it as the JSON body the Interfaces section
describes. Wrong credentials answer 401 with no hint of which part was wrong.

Done when: integration tests cover success, wrong password, unknown email.

- **Model**: claude/claude-sonnet-4-6
- **Estimate**: 4h
- **Reason**: Auth logic with security edge cases.
- **Dependencies**: F1.1.T1
- **Files**:
  - `src/api/session.ts`
  - `src/api/session.test.ts`
```

### Headers

| Level | Syntax | Becomes |
| --- | --- | --- |
| Phase | `# F<n> — <title>` | `phase` of every task below it |
| Package | `## F<n>.<pkg> — <title>` | grouping only |
| Task | `### F<n>.<pkg>.T<n> — <title>` | one task, id `F<n>.<pkg>.T<n>` |

Integers without padding (`F1`, never `F01`). The separator may be `—`, `–`
or `-`.

### Fields

Bullets under the task, labels in bold, English or Spanish
(`Model`/`Modelo`, `Estimate`/`Estimación`, `Reason`/`Razón`,
`Dependencies`/`Deps`, `Files`/`Archivos`). Any other label is warned about
and ignored — so a `Done when` line goes in the description, **not** as a
field.

- **Estimate** accepts `2h`, `1.5h`, `30m`, `2d` (a day is 8h). It is not
  decoration: orch derives the dispatch timeout from it
  (`estimateHours × default_timeout_multiplier`), so an estimate that is far
  too low gets the agent killed mid-task.
- **Dependencies**: task ids separated by `,` or `;`. Leave the value empty
  when there are none.
- **Files**: backticked paths as sub-bullets. They are the files the agent is
  told it may write, and what orch uses to keep concurrent tasks apart.

Everything between the task header and its first field bullet is the
**description**, multiline.

## Rules for good tasks

- **One task = one agent session = one PR.** Aim for 1–4h, never above 8h.
  Split anything bigger along the architecture's interfaces.
- **The description is the whole brief.** What to build, which contract to
  honour (cite the architecture section), and a *Done when* an agent can check
  by running something. "Implement auth" is not a task.
- **Dependencies are real, and only real.** A task depends on another only if
  it needs that task's output to start. Missing ones make orch run tasks in
  the wrong order; spurious ones serialise work that could run in parallel.
  `orch validate` rejects unknown ids and cycles.
- **Two tasks that can run at the same time never share a file.** If they
  must, make one depend on the other.
- **Pick the cheapest model that will get it right.** Each route in
  `model_router.yaml` has a `tier` (`premium`, `standard`, `cheap`): premium
  for design-heavy or security-sensitive work, cheap for boilerplate, docs and
  mechanical edits. Say why in `Reason`.
- **Never renumber a task that has been atomized.** `orch atomize` merges by
  id: a renumbered task becomes a new task *plus* an orphan of the old one,
  with its status and history left behind. Append new ids instead
  (`F1.1.T4`), even out of order.
- **Nothing field-like inside code fences.** The parser skips fenced blocks
  entirely — a task written inside one is not imported.

## Check before handing off

```bash
orch atomize --file specs/f1-<slug>.md --list    # what the parser sees
```

Compare the listed ids with the tasks you meant to write, and read every
warning — each one is a line the parser dropped or misread. Fix and re-run
until the list is exactly your tasks and there are no warnings. Then hand off
to `orch-tasks`; do not run `--apply` from this skill.
