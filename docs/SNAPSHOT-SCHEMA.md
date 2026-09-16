# Stakeholder snapshot — JSON Schema (`schema: 1`)

G6.1. `internal/publish/snapshot.Build` is the one place this document is
assembled — a static export (`orch publish`), a watch loop, or a dashboard
route can all hand a viewer this same shape with no login and no access to
the operator's project tree.

## Contract

- **`schema: 1`.** A future incompatible change bumps this number rather
  than growing an optional field forever; a viewer built against schema 1
  should refuse a `schema: 2` document rather than render it half-understood.

  **`branding` is the one optional field, added under schema 1 on purpose
  (G8.4).** The rule above is about *incompatible* changes, and this is not
  one: the key is absent entirely unless an operator configures it, a schema-1
  viewer that ignores unknown keys renders exactly what it rendered before,
  and nothing existing moved or changed meaning. Bumping to 2 would have said
  "refuse to render this" to every viewer already deployed, for a document
  they can read perfectly.

  The property that makes it safe is tested rather than asserted: with no
  branding configured, `Build` produces **byte-identical** output to what it
  produced before the field existed
  (`TestWithoutBrandingTheDocumentIsByteIdentical`). That is also why the
  field is a POINTER — `omitempty` does not omit a struct, so a value type
  would have grown `"branding":{}` on every unbranded document.
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
golden-file comparison).

That is a DENY-list, and a deny-list cannot catch a field nobody thought to
forbid — this paragraph used to claim that "a new field this package emits
must be added to that list on purpose before the test can pass again", and
that was not true: a new key passed silently unless its name was already
known to be operator-only. `TestEveryEmittedKeyIsInTheSchema` (added in G8.4)
is the allow-list that makes the claim true: every key name the document emits
must appear in this file and in that test's `allowedKeys`, or the test fails
naming the key.

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
| `branding` | object | **Optional**, absent unless `presentation.branding` is configured. See below. |
| `executive_summary` | object | See below. |
| `deliveries` | array of object | **Optional**, absent when nothing was finished in the last 30 days. See below. |
| `documents` | array of object | **Optional**, absent when `portal.documents` matches nothing. See below. |
| `quality` | object | **Optional**, absent unless tasks go through pull requests (`vcs.auto_pr` with `dispatch.worktree_mode`) and a workflow running on `pull_request` checks something. See below. |

## `summary`

The same seven figures `graph.Summarize` already produces for `orch
status`/the operator dashboard, plus the finish estimate — `eta_hours`, and
`eta_date`/`eta_confidence` when a pace is known.

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
| `eta_date` | string, optional | Projected finish day (`YYYY-MM-DD`, UTC) at the pace of the last 7 days: tasks neither done nor blocked, divided by tasks finished per day. The same projection the dashboard's Pace page shows. **Absent** when nothing remains or nothing finished in the window. Additive, so still schema 1 (like `branding`). |
| `eta_confidence` | string, optional | `"high"` when `eta_date` is within 30 days, `"low"` beyond. Present exactly when `eta_date` is. |

`eta_hours` ports `eta_hours_remaining` (`orchestrator/dashboard/
metrics.py`): sum `estimate_hours` over every not-done task; if that is
`<= 0`, `null`. Otherwise, scale it by `done_actual_hours / done_estimate_hours`
across done tasks when both are `> 0` (the team's own pace so far); fall back
to the raw remaining estimate when there is no pace to measure yet (no task
done, or none with recorded human hours).

## `milestones[]`

One row per phase — this is Python's `phases_timeline`/`milestones_from_
phases` reshaped into one object, not two. The aggregate comes first; the
optional `packages[]` breaks it down into deliverables by **title** — never by
task id, file or spec path — which is what the client portal's roadmap reads.

| Field | Type | Notes |
|---|---|---|
| `phase` | integer | The phase number from `tasks.json`. Not a task id. |
| `name` | string | From `tasks.json`'s `phases[]`; else the spec's `# F<n> — <title>` header; `"Phase N"` when neither names it. |
| `total` | integer | |
| `done` | integer | |
| `in_progress` | integer | |
| `blocked` | integer | |
| `backlog` | integer | |
| `percent_done` | number | Rounded to 1 decimal; `0` when `total` is `0`. |
| `complete` | boolean | `true` iff `total > 0 && done == total`. |
| `packages` | array of object | **Optional** (additive, schema 1). The phase's tasks grouped by package, in task-id order: `name` (the spec's `## F<n>.<k> — <title>` header, a leading `Package: ` dropped; `"F<n>.<k>"` when the spec has no header; `""` for the group of tasks outside the `F<n>.<k>.T<m>` scheme, listed last), `total`, `done`, and `deliverables[]`. |

Each `deliverables[]` entry is `title`, `status` — `done`, `in_progress`,
`blocked` or `pending` (backlog and todo are one state to a client: not
started) — and `finished_at` (RFC 3339) on a done deliverable whose finish
time is recorded.

## `documents[]`

The project files the operator shares (`portal.documents` in
[`CONFIG.md`](CONFIG.md)): `id` (a slug of the title, unique in the document,
for links — never a path), `title` (the first `# ` heading, a plan's `F<n> — `
dropped, else the file name), `updated_at` (the file's modification time, RFC
3339 UTC) and `markdown` (the body, YAML frontmatter stripped). Rendering is
the viewer's; the portal sanitises the HTML it produces.

## `deliveries[]`

What was finished in the last 30 days, newest first, at most 30: `title`,
`phase` (the phase's `name`) and `finished_at` (RFC 3339, UTC). The portal's
"since your last visit". Absent when nothing was.

## `quality`

How every delivery is checked before it counts, in a client's terms. No model
names, no costs, no ids. Additive, so still schema 1.

| Field | Type | Notes |
|---|---|---|
| `gates` | array of string | The kinds of check the workflows running on `pull_request` amount to, in this order: `tests`, `typecheck`, `lint`, `build`, `review`. Read from job and step names (`internal/ci.Gates`); a job whose name says review counts as the review whatever its steps are called. |
| `delivered` | integer | Done tasks that went through a pull request. |
| `verified` | integer | Of those, the ones whose CI passed. A PR merged by hand over a red check is delivered but not verified. |
| `first_pass` | integer | Of the verified, the ones that passed without a CI retry. |

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
(`internal/publish/snapshot/translate.go`). The sentence follows the most
recent signal in the task's events, by parsed timestamp: a failure's class
(a class the table does not know reads as a technical problem), a pull
request whose checks kept failing (`ci_blocked`), or a task the engine could
not start (`block`). A task with none of those — blocked by a person or an
agent — gets a generic sentence. Never the free-text `reason` an event's own
`extra` map may also carry.

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
| `language` | string | `"en"`, `"es"` or `"pt"` (config.yaml's `dashboard.language`). The client portal and the PDF render their own labels in it; a portal handed any other value shows English and warns in the console. |

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

## Branding (optional)

Absent entirely unless an operator sets `presentation.branding` in
config.yaml. Every field inside it is optional too.

| Field | Type | Notes |
|---|---|---|
| `name` | string | What the CLIENT reads. The project's own `meta.project` is unchanged; this replaces it in a client-facing header. |
| `logo` | string | Always a `data:` URI, **never a path**. The document has to stand alone: a viewer holding this JSON has no access to the operator's filesystem. PNG or JPEG only — the PDF renderer draws raster images, and a white-label logo that appears on the web and is missing from the printed page is worse than one that says which formats work. Capped at 128 KiB raw, because a live viewer re-fetches this document on a timer. |
| `accent_color` | string | `#rgb` or `#rrggbb`. `config.Load` refuses anything else at startup rather than ignoring it — a colour that does not apply is invisible, and the operator would go looking in the wrong place. |
| `footer` | string | One line at the bottom of the page. |

Nothing here is an internal id, a path, a backend name or raw technical text:
it is what the operator chose to show a client, which is the one category of
operator-authored text this document is *for*.
