# Issue triage rubric

Applied by `.github/workflows/triage.yml` to every new issue. The answer is a
proposal: the maintainer changes the labels when they disagree, and nothing is
worked on until they add `claude:go`.

## Priority

**urgent** — goes into the next patch release (`vX.Y.Z+1`). At least one of:

- **State is lost or corrupted**, or two components disagree about it (a task
  written to one database and read from another).
- **The main flow cannot complete**: `orch run` hangs, exits early, or tasks
  can never reach `done` or `blocked`.
- **Wrong verdicts that release dependents**: a task marked `done` whose work
  does not exist, a PR merged that should not be.
- **Installation or upgrade is broken** for everyone on a supported platform.
- **Security**: a secret leaked, a permission widened, untrusted input executed.
- **A regression** of something that worked in the last release.

…**and** there is no reasonable workaround. A bug with a one-line config
workaround is `planned`, whatever its severity.

**planned** — goes into the next minor release (`vX.Y+1.0`), batched with the
rest:

- Features and improvements.
- Bugs with a workaround, or that only affect an unusual setup.
- Confusing messages, missing docs, cosmetic problems in the dashboard.

When in doubt between the two, choose **planned** and say why in the
reasoning: an urgent label interrupts a human.

## Type

- **bug** — orch does something wrong or crashes.
- **improvement** — orch works, but could work better (clearer output, fewer
  steps, faster).
- **feature** — orch lacks a capability.

## Severity

`critical` (data loss, security, everyone blocked), `high` (a main flow
broken for some), `medium` (a secondary flow, or a workaround exists), `low`
(cosmetic, docs).

## Areas

The `internal/…` packages (or `web/`, `docs/`, `scripts/`, `.github/`) the fix
most likely touches, from the issue text and the repository map. Empty when the
issue does not say enough to guess.
