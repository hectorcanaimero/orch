---
name: orch-prd
description: Use when the user wants to turn a product or feature idea into a PRD for an orch-managed project (step 1 of orch-plan). Interviews for the missing facts, then writes docs/prd/NNN-<slug>.md with numbered, testable requirements that orch-arch and orch-spec can trace. Never writes code, specs or tasks.json.
---

# orch-prd — idea → product requirements

First stage of the orch pipeline: **PRD → ARCH → SPEC → TASKS**. The output is
a document a human signs off on and the next stage (`orch-arch`) builds from.
Nothing here is dispatched to an agent directly, so optimise for a reader who
has to say "yes, that is what we are building" — not for an implementer.

## Before writing

1. Confirm you are in an orch project: `tasks.json` or `.orchestrator/` at the
   root. If neither exists, stop and suggest `orch init` (or
   `orch init --template <name>`); the later stages need its layout.
2. Read what already exists so the PRD does not contradict it: `docs/prd/`,
   `docs/arch/`, `specs/`, the README, and `orch explain` for where the
   project stands.
3. Ask — in **one** batch, not one question per message — for whatever the
   idea leaves open and the PRD cannot honestly guess:
   - who the users are and what they do today instead;
   - the one outcome that makes this worth building;
   - hard constraints (deadline, budget, stack already chosen, compliance);
   - what is explicitly **out** of scope.

   If the user says "just decide", decide, and record each decision under
   *Assumptions* so it is visible and reversible.

## Where it goes

`docs/prd/NNN-<slug>.md` — `NNN` is the next free three-digit number in that
directory (`001` for the first), `<slug>` is short kebab-case.

**Never under `specs/`.** `orch atomize` walks the spec root and warns about
any file whose frontmatter is not `type: spec`; a PRD there is noise in every
atomize run.

## Frontmatter

```yaml
---
type: prd
project_id: <id>  # the first line of `orch explain` reads "Project <id>"
version: 0.1
generated_by: orch-prd
generated_at: <YYYY-MM-DD>
title: <Feature name>
---
```

`type: prd` is one of the values atomize recognises (`prd`, `arch`, `spec`,
`poc`, `task-batch`); any other value is reported as invalid.

## Sections

```markdown
# <Feature name>

## Problem
Two or three paragraphs: who hurts, how, and what it costs them today.

## Users
One bullet per user type, with the job they are trying to get done.

## Goals
- G1 — <measurable outcome>

## Non-goals
- <thing a reasonable reader might expect, and why it is out>

## Functional requirements
- **FR-1** — <the system does X when Y>. *Acceptance:* <observable check>.

## Non-functional requirements
- **NFR-1** — <performance / security / accessibility / cost bound>.

## Assumptions
- <decision taken without the user, so it can be challenged>

## Open questions
- <what blocks a confident architecture, with who can answer it>

## Milestones
- M1 — <user-visible slice>: FR-1, FR-2
```

## Rules

- **Every requirement is numbered and testable.** `FR-n` / `NFR-n` ids are
  what `orch-arch` and `orch-spec` cite; renumbering one later breaks that
  trace, so append new ones instead.
- **Acceptance is observable.** "Works well" is not a criterion; "a signed-out
  user who opens /orders is redirected to /login" is.
- **No implementation.** No frameworks, tables or endpoints unless the user
  imposed them as a constraint — then they go under *Non-functional
  requirements* as a constraint, not as a design.
- **Milestones are user-visible slices**, each naming the requirements it
  delivers. They become orch milestones and the stakeholder view's progress
  bars, so a client should recognise every one.
- **Open questions are allowed; silent guesses are not.** A PRD with two
  honest open questions beats one that hides them.

## Hand-off

Show the user the path and a five-line summary (goals, FR count, milestones,
open questions). Stop there. The next step is `orch-arch`, and only once the
user accepts the scope — or `orch-plan` resumes it for them.
