# Stakeholder snapshot — JSON Schema (`schema: 1`)

G6.1. `internal/publish/snapshot.Build` is the one place this document is
assembled — a static export (`orch publish`), a watch loop, or a dashboard
route can all hand a viewer this same shape with no login and no access to
the operator's project tree.

## Contract

- **`schema: 1`.** A future incompatible change bumps this number rather
  than growing an optional field forever; a viewer built against schema 1
  should refuse a `schema: 2` document rather than render it half-understood.
- **No internal ids.** No task id (`F2.T3`), no run id, no database row id.
  Phase numbers are the exception — they are the same small integers
  `tasks.json`'s own `phases[]` list carries and are not a task identity.
- **No backend or provider names.** `claude` / `codex` / `opencode` /
  `gemini` / `agy`, and no per-model or per-provider spend breakdown.
- **No file paths.** Not a task's `files`, not a `specRef`, not a log path.
- **No raw technical text.** No exit codes, no stack traces, no a task's
  own comment body. A blocked task's reason is always one of a fixed set of
  business-language sentences (`internal/publish/snapshot/translate.go`),
  never free text an event or a human wrote — see "Blockers" below for why
  this is a deliberate departure from Python's own stakeholder payload.

`internal/publish/snapshot/snapshot_test.go`'s `TestNoOperatorFields`
enforces the first four points with an explicit forbidden-field list (not a
golden-file comparison): a new field this package emits must be added to
that list on purpose before the test can pass again.

## Top level

| Field | Type | Notes |
|---|---|---|
| `schema` | integer | Always `1` today. |
| `generated_at` | string (RFC 3339, UTC) | When this document was built. |
| `project_name` | string | `tasks.json`'s `meta.project`. |
| `refresh_interval_s` | integer | How often a live viewer should re-fetch. |
| `summary` | object | See below. |
| `milestones` | array of object | One per phase that has at least one task. |
| `blockers` | array of object | One per task currently `blocked`. |
| `budget` | object | See below. |
| `executive_summary` | object | See below. |

## `summary`

The same seven figures `graph.Summarize` already produces for `orch
status`/the operator dashboard, plus one new figure — `eta_hours`.

| Field | Type | Notes |
|---|---|---|
| `total` | integer | |
| `done` | integer | |
| `in_progress` | integer | |
| `blocked` | integer | |
| `backlog` | integer | Everything not done/in-progress/blocked, `todo` included. |
| `percent_done` | number | Rounded to 1 decimal. |
| `estimate_hours_total` | number | Sum of every task's `estimateHours`. Rounded to 1 decimal. |
| `eta_hours` | number or `null` | Remaining estimate scaled by actual pace on done tasks. `null` when there is no signal — nothing left, ever (see below). |

`eta_hours` ports `eta_hours_remaining` (`orchestrator/dashboard/
metrics.py`): sum `estimate_hours` over every not-done task; if that is
`<= 0`, `null`. Otherwise, scale it by `done_actual_hours / done_estimate_hours`
across done tasks when both are `> 0` (the team's own pace so far); fall back
to the raw remaining estimate when there is no pace to measure yet (no task
done, or none with recorded human hours).

## `milestones[]`

One row per phase — this is Python's `phases_timeline`/`milestones_from_
phases` reshaped into one object, not two. No task list: a milestone is a
phase's aggregate, never a breakdown by task.

| Field | Type | Notes |
|---|---|---|
| `phase` | integer | The phase number from `tasks.json`. Not a task id. |
| `name` | string | From `tasks.json`'s `phases[]`; `"Phase N"` when the project has none. |
| `total` | integer | |
| `done` | integer | |
| `in_progress` | integer | |
| `blocked` | integer | |
| `backlog` | integer | |
| `percent_done` | number | Rounded to 1 decimal; `0` when `total` is `0`. |
| `complete` | boolean | `true` iff `total > 0 && done == total`. |

## `blockers[]`

One row per task whose live status is `blocked`, sorted by `(phase, title)`.

| Field | Type | Notes |
|---|---|---|
| `phase` | integer | |
| `title` | string | The task's title. Never its id. |
| `reason` | string | A fixed business-language sentence — see below. |

**Why `reason` is a translation table, not a port.** Python's
`_stakeholder_payload` builds `blocked_reasons` by truncating a blocked
task's first comment to 120 characters and shipping it as-is
(`orchestrator/dashboard/server.py:1682-1687`) — whatever an operator, a CI
log, or the dispatch loop itself wrote there, unredacted. That is exactly
what this contract forbids, so Go does not port it. Instead, `reason` comes
from `internal/providers.Failure` — the closed classification the dispatch
loop already runs every failure through (`internal/providers/classify.go`)
— mapped to one fixed sentence per class per language
(`internal/publish/snapshot/translate.go`). A blocked task whose events
carry no classified failure (blocked by an unmet dependency, or manually
deferred) gets a generic sentence, never the free-text `reason` an event's
own `extra` map may also carry.

## `budget`

| Field | Type | Notes |
|---|---|---|
| `enabled` | boolean | Mirrors `config.yaml`'s `dashboard.show_spend_to_stakeholder` (default `false` — "spend is sensitive"). |
| `spend_usd` | number, omitted when `enabled` is `false` | Total spend across every backend and every task, to 4 decimals. |
| `spend_by_day` | array of `{date, cost_usd}`, omitted when `enabled` is `false` | Daily totals over the trailing 14 days. Never broken down by provider or model. |

Python's `/stakeholder/summary` computes and returns spend regardless of
`show_spend_to_stakeholder` — only the SPA's own rendering respects the
flag, so a direct fetch of that endpoint leaks spend even with it off. This
snapshot closes that gap at the point the document is built: with
`ShowSpend: false`, `spend_usd` and `spend_by_day` are absent from the
document entirely, not merely unrendered. See
`docs/brainstorm/go-migration-notes/sonnet.md` for the full note — this is
not a new feature, it is the flag's own documented intent, finally enforced
where it can't be bypassed by reading the JSON directly.

## `executive_summary`

| Field | Type | Notes |
|---|---|---|
| `text` | string | A deterministic, no-LLM progress sentence — see below. |
| `language` | string | `"es"` or `"en"`. |

Ports `executive_summary` (`orchestrator/dashboard/metrics.py`), trimmed to
the one call shape `_stakeholder_payload` actually uses: it always passes an
hours-based ETA, never a target date, so the date half of Python's function
has nothing here to port. The text mentions progress, in-progress and
blocked counts, up to the first 3 blockers (title + translated reason, never
raw text), the ETA, and total spend (only when `budget.enabled`) — each
sentence appended only when its data is present, so a sparse project still
reads cleanly.

## Example

```json
{
  "schema": 1,
  "generated_at": "2026-09-12T18:30:00Z",
  "project_name": "demo",
  "refresh_interval_s": 30,
  "summary": {
    "total": 3, "done": 1, "in_progress": 1, "blocked": 1, "backlog": 0,
    "percent_done": 33.3, "estimate_hours_total": 3.5, "eta_hours": 2.0
  },
  "milestones": [
    {"phase": 0, "name": "F0 — Foundation", "total": 2, "done": 1,
     "in_progress": 1, "blocked": 0, "backlog": 0, "percent_done": 50.0,
     "complete": false},
    {"phase": 1, "name": "F1 — Routing", "total": 1, "done": 0,
     "in_progress": 0, "blocked": 1, "backlog": 0, "percent_done": 0.0,
     "complete": false}
  ],
  "blockers": [
    {"phase": 1, "title": "Wire the router",
     "reason": "Se alcanzó un límite de uso del proveedor de IA."}
  ],
  "budget": {
    "enabled": true,
    "spend_usd": 1.5,
    "spend_by_day": [{"date": "2026-09-12", "cost_usd": 1.5}]
  },
  "executive_summary": {
    "text": "Proyecto 33% completo — 1 de 3 tareas entregadas. 1 en progreso. 1 bloqueada(s) — requieren atención.\n\nBloqueos:\n• Wire the router: Se alcanzó un límite de uso del proveedor de IA.",
    "language": "es"
  }
}
```
