---
name: orch-arch
description: Use when an accepted PRD under docs/prd/ needs a technical architecture before specs are written (step 2 of orch-plan). Reads the code and the PRD, writes docs/arch/NNN-<slug>.md with components, contracts, decisions and the phase/package split orch-spec turns into tasks. Never writes code, specs or tasks.json.
---

# orch-arch — PRD → architecture

Second stage of **PRD → ARCH → SPEC → TASKS**. The architecture decides *how*
the PRD gets built and, just as important for orch, **how the work splits**:
the phases and packages you name here become the `# F<n>` and `## F<n>.<pkg>`
headers of the specs, and every task in `tasks.json` hangs off one of them.

## Inputs

1. The PRD: `docs/prd/NNN-<slug>.md`. If there is none, stop and run
   `orch-prd` first — an architecture without requirements has nothing to be
   right about.
2. The code that exists. Read the layout, the dependency manifests and the
   modules the PRD touches **before** proposing anything; a design that ignores
   the stack already in the repo is a rewrite nobody asked for.
3. Earlier architecture under `docs/arch/` — extend or supersede it
   explicitly, never contradict it silently.

If the PRD still has open questions that change the design, ask them now,
in one batch. Questions that do not change the design can stay open.

## Where it goes

`docs/arch/NNN-<slug>.md`, reusing the PRD's `NNN-<slug>` when it covers one
PRD. **Never under `specs/`** — `orch atomize` walks the spec root and warns
about any non-spec file it meets.

```yaml
---
type: arch
project_id: <same as the PRD>
version: 0.1
depends_on:
  - docs/prd/NNN-<slug>.md
generated_by: orch-arch
generated_at: <YYYY-MM-DD>
title: <Feature name> — architecture
---
```

## Sections

```markdown
# <Feature name> — architecture

## Context
What exists today and what the PRD changes about it, in a paragraph.

## Components
One subsection per component: responsibility, what it owns, what it calls.

## Data model
Entities, key fields, ownership, and migrations if storage already exists.

## Interfaces
The contracts between components: routes, events, function signatures,
file formats. Precise enough that two agents working in parallel on either
side of one agree without talking.

## Decisions
- **D1 — <decision>.** Chosen: <option>. Rejected: <option> because <reason>.

## Risks
- <what could sink it>, *mitigation:* <what the plan does about it>.

## Requirement coverage
| Requirement | Component(s) |
| --- | --- |
| FR-1 | ... |

## Work breakdown
### F1 — <phase goal>
- **F1.1 — <package>**: <scope>. Files/dirs: `<path>`, `<path>`.
- **F1.2 — <package>**: <scope>. Depends on: F1.1.
```

## Rules

- **Every FR and NFR appears in *Requirement coverage*.** A requirement with no
  component is either out of scope — say so — or a hole in the design.
- **Phases are ordered delivery, packages are parallel work.** Packages in the
  same phase should be buildable at the same time by different agents; put a
  package in a later phase, or name its dependency, when it cannot be. orch
  dispatches ready tasks concurrently, so a hidden dependency here becomes two
  agents editing the same thing at once.
- **Name the files and directories each package owns.** `orch-spec` copies
  them into each task's `Files` list, and orch uses those lists to keep
  concurrent tasks off the same files.
- **Interfaces before implementations.** When two packages meet, the contract
  goes in *Interfaces* and belongs to the earlier phase, so the later tasks can
  start from something fixed.
- **Decisions show the alternative.** A decision with no rejected option is an
  assumption; list it as one.
- **Match the phase numbering already in `specs/`.** If `F0` and `F1` exist,
  new work starts at the next free phase — task ids are how `orch atomize`
  merges, and reusing a phase number collides with tasks already in
  `tasks.json`.

## Hand-off

Show the path, the phase/package list and any decision the user should
confirm. The next step is `orch-spec`, one spec file per phase.
