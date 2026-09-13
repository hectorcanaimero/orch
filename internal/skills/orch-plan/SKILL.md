---
name: orch-plan
description: Use when the user describes a feature or product idea and wants it planned end to end for orch ("plan this", "/orch-plan <idea>"). Drives the pipeline PRD → ARCH → SPEC → TASKS through orch-prd, orch-arch, orch-spec and orch-tasks, stopping for the user at each gate and resuming from whatever artifact already exists.
---

# orch-plan — the whole pipeline, with gates

`orch-plan` does not write anything itself. It sequences the four stage skills,
decides where to resume, and stops at the points where a human has to agree
before more work is built on top.

```
idea ─► orch-prd ─► [gate: scope] ─► orch-arch ─► [gate: design]
     ─► orch-spec ─► orch-tasks ─► [gate: apply the diff] ─► [gate: dispatch]
```

## 0. Preconditions

- An orch project: `tasks.json` or `.orchestrator/` at the root. If not,
  propose `orch init` (with `--template <name>` when one fits) and stop until
  the user has run it.
- The four stage skills. If one is not available to you, it can be installed
  with `orch install-skills --skill <name>` — or all of them with
  `orch install-skills --all` (add `--target codex`, `opencode` or `cursor` for
  other agents).
- `orch doctor` passes, or its failures are understood. A broken router or
  config found now is cheaper than one found at dispatch.

## 1. Find where to resume

Match the idea to existing artifacts before creating new ones:

| Exists already | Resume at |
| --- | --- |
| nothing for this idea | `orch-prd` |
| `docs/prd/NNN-<slug>.md` | `orch-arch` |
| `docs/arch/NNN-<slug>.md` | `orch-spec` |
| `specs/f<N>-<slug>.md` citing that architecture | `orch-tasks` |

Say which stage you are resuming at and why. Never regenerate an artifact the
user has already accepted unless they ask; edit it instead, keeping its
requirement and task ids.

## 2. Run the stages

For each stage, load its skill and follow it **completely** — its rules
(numbered requirements, files per package, the exact spec format) are what
make the next stage possible.

1. **`orch-prd`** → `docs/prd/NNN-<slug>.md`.
   **Gate — scope:** the user accepts goals, non-goals and milestones, and
   answers or explicitly defers the open questions.
2. **`orch-arch`** → `docs/arch/NNN-<slug>.md`.
   **Gate — design:** the user confirms the decisions and the phase/package
   split. Skip this gate only if they told you to run straight through.
3. **`orch-spec`** → `specs/f<N>-<slug>.md`, one per phase, each checked with
   `orch atomize --file ... --list` until it has no warnings.
4. **`orch-tasks`** → preview the diff.
   **Gate — apply:** nothing is written to `tasks.json` until the user has seen
   the diff and said yes. Then apply, validate, and promote what they choose to
   `todo`.

"Run it all" from the user removes gates 1 and 2, never gate 3: applying
changes what orch will dispatch.

## 3. Finish

Report the four paths, the task count per phase, what is `todo`, and the output
of `orch explain`. Offer `orch run --max-tasks 1` as the next step — and stop.
**Dispatching is a separate decision:** it spends tokens and opens PRs, and
this skill never runs `orch run` on its own.

## When something does not fit

- **The idea is small** (one or two tasks): say so, skip the PRD and
  architecture with the user's agreement, and go straight to `orch-spec` with
  the requirements written into the task descriptions.
- **A later stage exposes a gap in an earlier one** (an FR nobody can build, a
  package that needs a missing interface): go back and fix that artifact, tell
  the user what changed, and continue from there. Do not paper over it in the
  spec.
- **The user edits a spec by hand afterwards**: run `orch-tasks` alone.
