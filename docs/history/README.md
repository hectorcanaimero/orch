# orch — Build history

These are the original SDD (Spec-Driven Development) artifacts from when `orch`
was first built as the task orchestrator for the Rupies v2 monorepo rewrite.

They're preserved verbatim as engineering-decision provenance:

| File | Phase |
|---|---|
| `proposal.md` | Intent, scope, constraints, alternatives considered |
| `spec.md` | Functional (FR-*) and non-functional (NFR-*) requirements |
| `design.md` | Module breakdown, dataclasses, main-loop pseudocode |
| `tasks.md` | R-001..R-020 rollout order + per-task acceptance criteria |
| `verify-report.md` | Real gaps found during verify + closure plan |
| `archive-report.md` | Final delivery report, metrics, deliverables vs proposal |

If you're just here to USE `orch`, you don't need any of these — the top-level
[`README.md`](../../README.md) is enough. These are useful if you want to
understand WHY a design decision was made (retry-once semantics, flock scope,
strict-files revert behavior, cost-extraction approach per backend, etc.).
