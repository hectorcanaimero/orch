# Sprint F-3: Stability & Polish — Design Spec

**Date:** 2026-08-26
**Status:** Draft — pending implementation
**Scope:** Config consolidation, Milestone model, Status labels, README, Board removal

---

## Goal

Fix the things that are structurally wrong before adding more features. Four workstreams organized in dependency order: foundation (config + retry) → data model (milestones) → presentation (labels + dashboard) → docs (README + cleanup).

---

## Layer 1 — Config consolidation + retry policy

### Problem

Five YAML files greet the new user: `config.yaml`, `budgets.yaml`, `model_router.yaml`, `dashboard.yaml`, `pricing.yaml`. None of them explains which are mandatory. The onboarding experience is hostile.

### Design

`config.yaml` becomes the single mandatory file. It ships with defaults for every section that today lives in a separate file. The other files become **optional override files** — if they exist on disk, their values take precedence over the defaults in `config.yaml`; if they don't exist, the defaults apply silently.

**Loading contract:**

```
_load_config(path) → base_cfg
_try_load_override("budgets.yaml")  → deep_merge(base_cfg, override)
_try_load_override("model_router.yaml") → deep_merge(base_cfg, override)
_try_load_override("dashboard/dashboard.yaml") → deep_merge(base_cfg, override)
```

`_deep_merge(base, override)` performs recursive dict merge — override keys win, missing keys fall back to base. Implemented in `orchestrator/config_loader.py` (new file, ~40 lines).

**`orch init` change:** generates only `config.yaml`. The init template includes commented-out stubs for every optional section with a comment pointing to the override file pattern:

```yaml
# Budget guardrails — override in budgets.yaml or add a `budgets:` section here.
# budget:
#   per_dispatch_usd: 5.00
```

**Backwards compatibility:** projects with existing separate files continue working unchanged. The merge runs on every startup regardless.

### Retry policy

`max_attempts` is hardcoded at lines 1148–1151 of `orch.py` (`3 if escalation_allowed else 2`). It moves to config:

```yaml
retry:
  max_attempts: 2                    # base; escalation routes get +1 automatically
  backoff_seconds: 5
  rate_limit_backoff_seconds: 60
```

`orch.py` reads `cfg["retry"].get("max_attempts", 2)`. The escalation `+1` logic stays in code — it's system knowledge, not user config.

### Files touched

| File | Action |
|------|--------|
| `orchestrator/config_loader.py` | **Create** — `load_config()`, `_deep_merge()`, `_try_load_override()` |
| `orchestrator/orch.py` | Modify — use `config_loader.load_config()`, read `max_attempts` from cfg |
| `orchestrator/config.yaml` | Modify — add default stubs for budgets/router/dashboard sections |
| `orchestrator/templates/config.tmpl` | Modify — `orch init` generates leaner file |

### Tests

- `_deep_merge` merges nested dicts correctly; override wins on conflict
- With no override files present: all defaults from `config.yaml` apply
- With `budgets.yaml` present: its values override the defaults
- `cfg["retry"]["max_attempts"]` is read and respected in dispatch loop

---

## Layer 2 — Milestone SQLite schema

### Problem

Tasks are grouped by sprint ID (`F-1.T3`). Stakeholders think in deliverables ("Login feature"), not sprint codes. No data model exists for this grouping.

### Design

Migration `004_milestones.sql`:

```sql
CREATE TABLE IF NOT EXISTS milestones (
    id          TEXT PRIMARY KEY,
    title       TEXT NOT NULL,
    description TEXT,
    target_date TEXT,            -- ISO 8601 date string, nullable
    status      TEXT NOT NULL DEFAULT 'open'
                CHECK (status IN ('open', 'completed', 'cancelled')),
    created_at  TEXT NOT NULL
                DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

ALTER TABLE tasks_definition
    ADD COLUMN milestone_id TEXT
    REFERENCES milestones(id) ON DELETE SET NULL;
```

**`SqliteBackend` additions:**

```python
def upsert_milestone(self, id: str, title: str,
                     description: str | None = None,
                     target_date: str | None = None) -> None: ...

def get_milestones(self) -> list[dict]:
    # Returns milestones with computed progress via SQL GROUP BY
    # progress = {"done": int, "total": int, "pct": int}

def set_task_milestone(self, task_id: str, milestone_id: str) -> None:
    # Raises KeyError if task_id or milestone_id not found

def complete_milestone(self, milestone_id: str) -> None: ...
```

Progress calculation happens in SQL — no Python iteration:

```sql
SELECT
    m.*,
    COUNT(td.id) AS total,
    SUM(CASE WHEN tr.status = 'done' THEN 1 ELSE 0 END) AS done
FROM milestones m
LEFT JOIN tasks_definition td ON td.milestone_id = m.id
LEFT JOIN tasks_runtime tr ON tr.task_id = td.id
GROUP BY m.id
```

**CLI extension:** `orch task set` gains `--milestone MILESTONE_ID`. Tasks with no milestone assigned continue to work unchanged — `milestone_id` is nullable everywhere.

### Files touched

| File | Action |
|------|--------|
| `orchestrator/state/migrations/004_milestones.sql` | **Create** |
| `orchestrator/state/sqlite_backend.py` | Modify — add 4 new methods |
| `orchestrator/orch.py` | Modify — `orch task set --milestone` flag |
| `orchestrator/dashboard/router.py` | Modify — `GET /api/milestones` endpoint |

### Tests

- Migration 004 applies cleanly on top of 003 schema
- `get_milestones()` returns correct progress counts
- `set_task_milestone()` raises `KeyError` for unknown task or milestone ID
- Tasks without `milestone_id` are unaffected by all milestone operations
- `GET /api/milestones` returns 200 with empty list when no milestones exist

---

## Layer 3 — Status labels + dashboard milestone view

### Problem

The dashboard shows `backlog`, `in_progress`, `done` — dev vocabulary. Stakeholders need "Planificado", "En progreso", "Entregado". And there is no way to see milestone progress in the UI.

### Design

**3a. Status labels in config**

New optional section in `config.yaml`:

```yaml
presentation:
  status_labels:
    backlog:     "Planificado"
    in_progress: "En progreso"
    done:        "Entregado"
    blocked:     "Bloqueado"
    skipped:     "Omitido"
```

`GET /api/config` already returns the full config object to the SPA. The frontend reads `config.presentation.status_labels` and applies the mapping in `frontend/src/lib/status.ts` (new helper `labelForStatus(status, labels)`). Internal values (`backlog`, `in_progress`, etc.) never change — the mapping is view-only.

**3b. Milestone view in SPA**

New page: `frontend/src/pages/MilestonesPage.tsx`

Route: `/milestones` — replaces `/board` in the nav.

Layout per milestone card:
- Title + status badge (using presentation labels)
- Progress bar: `done / total tasks` with percentage
- Target date (if set), days remaining or overdue
- Expandable task list grouped by status label

New API hook: `frontend/src/hooks/useMilestones.ts` — polls `GET /api/milestones` every 10s.

**Stakeholder view** (`StakeholderSummaryPage`): adds a "Milestones" section at the top using the same labels and hiding all technical fields (task IDs, backend names, error codes).

### Files touched

| File | Action |
|------|--------|
| `orchestrator/config.yaml` | Modify — add `presentation.status_labels` defaults |
| `frontend/src/lib/status.ts` | Modify — add `labelForStatus()` helper |
| `frontend/src/pages/MilestonesPage.tsx` | **Create** |
| `frontend/src/hooks/useMilestones.ts` | **Create** |
| `frontend/src/App.tsx` | Modify — add `/milestones` route, remove `/board` |
| `frontend/src/components/AppLayout.tsx` | Modify — swap Board nav item for Milestones |
| `frontend/src/pages/StakeholderSummaryPage.tsx` | Modify — add milestones section |

### Tests

- `labelForStatus("in_progress", labels)` returns configured label
- `labelForStatus("in_progress", {})` falls back to `"in_progress"` (no crash)
- `GET /api/milestones` returns 200 with progress field populated
- Stakeholder page does not include raw status values in response

---

## Layer 4 — README rewrite + remove Board tab

### Problem

`BoardPage` is an iframe embedding an external ExcaliDash canvas. It's non-functional without manual URL configuration, never finished, and adds maintenance surface. The README is dev-oriented and doesn't communicate the stakeholder differentiator.

### Design

**4a. Remove Board tab**

Delete: `frontend/src/pages/BoardPage.tsx` only. `KanbanColumn.tsx` is used by `KanbanPage` — keep it.

Remove from `App.tsx`: the `/board` route and `BoardPage` import.

Remove from `AppLayout.tsx`: the Board nav item.

Remove from `config.yaml` defaults: `dashboard.board_url` key. Existing projects that have `board_url` configured are unaffected at runtime — the key is ignored since the route no longer exists. No migration needed.

`KanbanPage.tsx` and the `/kanban` route are **kept** — Kanban is functional task management, unrelated to ExcaliDash.

**4b. README rewrite**

Structure:

```
# orch

[tagline — one line]

[hero screenshot: stakeholder dashboard view]

## The problem
## How it works (3 steps)
## Quick start
## What makes it different (comparison table)
## Configuration
## Documentation
```

Current README content moves to `docs/README-dev.md` — no content is lost.

### Files touched

| File | Action |
|------|--------|
| `frontend/src/pages/BoardPage.tsx` | **Delete** |
| `frontend/src/App.tsx` | Modify — remove `/board` route |
| `frontend/src/components/AppLayout.tsx` | Modify — remove Board nav item |
| `orchestrator/config.yaml` | Modify — remove `dashboard.board_url` default |
| `README.md` | **Rewrite** |
| `docs/README-dev.md` | **Create** — current README content |

### Tests

- Full pytest suite stays green after Board removal (no test referenced `BoardPage`)
- SPA build (`pnpm build`) completes without errors

---

## Test baseline

Current: **1113 passed, 2 skipped**. Each layer must keep the suite green before the next layer starts. Layer 4 (Board removal) must verify `pnpm build` passes in addition to pytest.

---

## Branch

`sprint-f3/stability-polish`

---

## Out of scope

- Live log tail / log search (observability improvements)
- ETA auto-calculation from `estimate_h`
- Email/Slack notifications
- Multi-project dashboard

*Spec written: 2026-08-26*
