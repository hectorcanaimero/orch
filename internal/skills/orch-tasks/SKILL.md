---
name: orch-tasks
description: Use when specs under specs/ are ready to become tasks in an orch project (step 4 of orch-plan, or on its own after editing a spec). Runs orch atomize as a dry-run, shows the diff, applies it only after the user confirms, then makes the DAG valid with orch router and orch validate and explains how to promote tasks out of backlog.
---

# orch-tasks — spec → tasks.json

Last stage of **PRD → ARCH → SPEC → TASKS**, and the only one that changes
what orch will run. Everything here goes through the `orch` binary — never
edit `tasks.json` by hand, and never apply a change the user has not seen.

## 1. Preview

```bash
orch atomize                            # every spec under the spec root
orch atomize --file specs/f1-<slug>.md  # or just one
```

Without `--apply` this writes nothing. It prints the merge diff, with its
headings in Spanish:

- `Parser warnings` — lines the parser dropped or misread;
- `+ NUEVAS` — ids not yet in `tasks.json`;
- `~ ACTUALIZADAS` — title, description, model, reason, dependencies,
  estimate, specRef or phase changed in the spec, one line per field;
- `= SIN CAMBIOS` — a count, nothing to review;
- `⚠ HUÉRFANAS en tasks.json` — tasks no scanned spec mentions. Atomize
  **never deletes** them. With `--file`, every task from other specs shows up
  here simply because it was not scanned; that is expected.
- `⚠ DEPENDENCIAS HUÉRFANAS` — a dependency naming an id that exists nowhere.

Stop on any parser warning or orphan dependency and fix the spec (see
`orch-spec`) — either one usually means a task or a field silently went
missing. The one warning that is safe to leave is a `project_id` that does not
match the active project, and only when the user confirms the spec belongs
here; the id to use is the one on the first line of `orch explain`.

Things to point out to the user before they confirm:

- an **updated** task that is already `in-progress` or `done`: its runtime
  status and comments survive the merge, but its brief changes under it;
- orphans that look like a *renumbered* task rather than a removed one —
  renumbering loses history; restore the old id in the spec instead;
- a large number of new tasks in one go, which usually means the spec needs
  splitting.

## 2. Apply — only after the user says yes

```bash
orch atomize --apply                    # same scope as the preview
```

It writes `tasks.json` and a `tasks.json.bak-<timestamp>` next to it. Leave the
backups where they are; they are the undo.

Tasks created by atomize start as **`backlog`**. Existing tasks keep their
status, files and comments.

## 3. Make the DAG valid

```bash
orch router validate        # every task model resolves to a route?
orch validate               # schema, dependencies, cycles
```

**Both** have to exit 0. `orch validate` alone is not enough: when
`model_router.yaml` has no routes at all it skips the route check and reports
zero errors, while `orch router validate` still lists every unrouted task.

If a model does not resolve and the router already has routes, prefer fixing
the spec: change `Model` to one of its keys and run steps 1–2 again. When the
router is empty — normal for a project scaffolded without `--template` — or the
user really wants a new model:

```bash
orch router add-missing
```

It prints the entry it infers for each unrouted model (backend, `cli_model`,
tier) and asks `[y/N]`; anything but `y` aborts without writing. Show the user
those entries before they answer — a wrong `cli_model` only fails at dispatch
time — and remind them that every added entry gets tier `standard` unless
`--tier` says otherwise. `--yes` skips the prompt; do not pass it on the
user's behalf.

## 4. Promote what should run

orch only dispatches tasks whose status is **`todo`**, so freshly atomized
work does nothing until someone promotes it. Ask the user which tasks — often
the first phase, rarely everything — then, one per task:

```bash
orch task set --id F1.1.T1 --status todo
```

## 5. Report

Summarise: tasks added, updated and orphaned, what is now `todo`, and what
`orch explain` says is ready. Suggest the next command without running it:

```bash
orch explain                # what is ready and what is safe to do next
orch run --max-tasks 1      # a first dispatch, when the user wants one
```

Dispatching spends real tokens and opens PRs. It is the user's call, never this
skill's.
