# Sprint G-3: Timeline visual (Gantt ligero) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give milestones a visual timeline. Each milestone already carries `progress {total, done, pct}` + `target_date` (G-2/F-3). This sprint adds a per-milestone **ETA projection** (tasks-based, reusing existing velocity) and a hand-written **SVG Gantt** rendered inside `MilestonesPage` — no page, no chart library.

**Architecture:** One pure helper `milestone_eta()` in `orchestrator/dashboard/metrics.py` (fully unit-testable, no I/O). `GET /api/milestones` attaches `eta_date` + `confidence` per milestone by pairing each milestone's remaining task count with the project's `velocity_per_day` (already computed by `sprint_health`). Frontend gains `GanttChart.tsx` (SVG, mirrors `BarChartByDay` geometry) + a collapsible section in `MilestonesPage` + a "Download SVG" button.

**Design decision (why tasks-based ETA):** `get_milestones()` already returns `total`/`done` per milestone. `sprint_health` already computes `velocity_per_day` (tasks/day, rolling 7d). `eta_days = ceil(remaining / velocity)` needs no new SQL and no `estimate_h` plumbing. Hours-based precision (`estimate_h` per milestone) is a deliberate future refinement, out of scope here.

**Tech Stack:** Python 3.11+, SQLite, pytest, FastAPI, React + TypeScript + shadcn/ui + Tailwind, pnpm.

---

## Baseline

```bash
uv run --extra dev pytest orchestrator/tests/ -q 2>&1 | tail -3
# Expected: 1226 passed, 3 skipped
```

---

## File Map

| File | Action | Layer |
|------|--------|-------|
| `orchestrator/dashboard/metrics.py` | Modify (add `milestone_eta`) | 1 |
| `orchestrator/tests/test_milestone_eta.py` | **Create** | 1 |
| `orchestrator/dashboard/server.py` | Modify (`/api/milestones` attaches eta) | 2 |
| `orchestrator/tests/test_dashboard_milestones.py` | Modify (assert eta fields) | 2 |
| `frontend/src/hooks/useMilestones.ts` | Modify (extend `Milestone` type) | 3 |
| `frontend/src/components/charts/GanttChart.tsx` | **Create** | 3 |
| `frontend/src/pages/MilestonesPage.tsx` | Modify (collapsible Gantt section) | 3 |

**Out of scope (explicit):** PDF export (own sprint), drag-to-reschedule (Serie I), inter-milestone dependencies (concept doesn't exist yet), hours-based ETA (`estimate_h`).

---

## Task 1 — `milestone_eta()` pure helper

**Files:** Modify `orchestrator/dashboard/metrics.py`; Create `orchestrator/tests/test_milestone_eta.py`.

- [ ] **Step 1: Write the failing tests**

Create `orchestrator/tests/test_milestone_eta.py`:

```python
"""Tests for milestone_eta — tasks-based ETA projection (Sprint G-3)."""
from __future__ import annotations

from orchestrator.dashboard.metrics import milestone_eta


def test_eta_none_when_no_velocity():
    # No throughput signal → cannot project, return None (UI renders —).
    assert milestone_eta(remaining=5, velocity_per_day=0.0, today="2026-08-27") is None


def test_eta_none_when_nothing_remaining():
    assert milestone_eta(remaining=0, velocity_per_day=2.0, today="2026-08-27") is None


def test_eta_rounds_days_up():
    # 5 tasks / 2 per day = 2.5 → 3 days → 2026-08-30.
    r = milestone_eta(remaining=5, velocity_per_day=2.0, today="2026-08-27")
    assert r["eta_date"] == "2026-08-30"
    assert r["eta_days"] == 3


def test_confidence_high_when_within_30_days():
    r = milestone_eta(remaining=2, velocity_per_day=2.0, today="2026-08-27")
    assert r["confidence"] == "high"   # 1 day out


def test_confidence_low_when_beyond_30_days():
    r = milestone_eta(remaining=100, velocity_per_day=1.0, today="2026-08-27")
    assert r["confidence"] == "low"    # 100 days out


def test_confidence_high_when_meets_target_date():
    # eta 3 days out, target is 10 days out → comfortably on time.
    r = milestone_eta(
        remaining=6, velocity_per_day=2.0, today="2026-08-27",
        target_date="2026-09-06",
    )
    assert r["confidence"] == "high"


def test_confidence_low_when_misses_target_date():
    # eta 10 days out but target is only 2 days out → late.
    r = milestone_eta(
        remaining=20, velocity_per_day=2.0, today="2026-08-27",
        target_date="2026-08-29",
    )
    assert r["confidence"] == "low"
```

- [ ] **Step 2: Run — expect FAIL** (`ImportError: cannot import name 'milestone_eta'`).

```bash
uv run --extra dev pytest orchestrator/tests/test_milestone_eta.py -q 2>&1 | tail -5
```

- [ ] **Step 3: Implement `milestone_eta` in `metrics.py`**

Add near `eta_hours_remaining` (it's the sibling projector). `today` is injected as a string so the function stays pure and deterministic under test — callers pass `datetime.now(timezone.utc).date().isoformat()`.

```python
import math
from datetime import date, timedelta


def milestone_eta(
    remaining: int,
    velocity_per_day: float,
    today: str,
    target_date: str | None = None,
) -> dict | None:
    """Project a completion date for one milestone from task throughput.

    tasks-based: eta_days = ceil(remaining / velocity_per_day). `today` is an
    ISO date string (injected for testability). Returns None when there is no
    signal (nothing remaining, or zero velocity) — the UI renders '—'.

    confidence:
      - "high"  → eta lands on/before target_date (when set) AND within 30 days
      - "low"   → eta misses target_date, or is > 30 days out with no target
    """
    if remaining <= 0 or velocity_per_day <= 0:
        return None
    eta_days = math.ceil(remaining / velocity_per_day)
    today_d = date.fromisoformat(today)
    eta_d = today_d + timedelta(days=eta_days)

    if target_date:
        try:
            target_d = date.fromisoformat(target_date)
            confidence = "high" if eta_d <= target_d else "low"
        except ValueError:
            confidence = "high" if eta_days <= 30 else "low"
    else:
        confidence = "high" if eta_days <= 30 else "low"

    return {"eta_date": eta_d.isoformat(), "eta_days": eta_days, "confidence": confidence}
```

- [ ] **Step 4: Run — expect PASS** (7 tests).
- [ ] **Step 5: Full suite** → 1233 passed, 3 skipped.
- [ ] **Step 6: Commit**

```bash
git add orchestrator/dashboard/metrics.py orchestrator/tests/test_milestone_eta.py
git commit -m "feat(metrics): milestone_eta — tasks-based per-milestone ETA projection"
```

---

## Task 2 — `/api/milestones` attaches `eta` per milestone

**Files:** Modify `orchestrator/dashboard/server.py`; Modify `orchestrator/tests/test_dashboard_milestones.py`.

- [ ] **Step 1: Extend the failing test**

In `orchestrator/tests/test_dashboard_milestones.py`, add to `test_milestones_returns_progress` (after seeding a done + a non-done task under M1) an assertion that each milestone dict carries an `eta` key (either `None` or `{eta_date, eta_days, confidence}`):

```python
    m = resp.json()["milestones"][0]
    assert "eta" in m            # None or {eta_date, eta_days, confidence}
    if m["eta"] is not None:
        assert set(m["eta"]) == {"eta_date", "eta_days", "confidence"}
```

- [ ] **Step 2: Wire velocity + eta into `api_milestones`**

In `server.py`, the `api_milestones` route currently returns `backend.get_milestones()` verbatim. Pair each milestone with the project velocity and attach `eta`:

```python
    @app.get("/api/milestones", name="api_milestones")
    def api_milestones():
        import yaml
        from datetime import datetime, timezone
        from orchestrator.state.sqlite_backend import SqliteBackend
        from orchestrator.dashboard.metrics import sprint_health, milestone_eta

        cfg_path = app_state.paths.config_yaml
        try:
            raw_cfg = yaml.safe_load(cfg_path.read_text(encoding="utf-8")) or {}
        except (OSError, ValueError, yaml.YAMLError):
            raw_cfg = {}

        backend = _get_state_backend(app_state.paths, raw_cfg)
        if not isinstance(backend, SqliteBackend):
            return JSONResponse({"milestones": [], "backend": "file"})

        milestones = backend.get_milestones()

        # Project velocity (tasks/day, rolling 7d) — same source as /api/sprint.
        tasks = load_tasks(app_state.paths.tasks_json)
        done_7d = backend.count_done_last_n_days(7)
        velocity = sprint_health(tasks, done_7d, {}).get("velocity_per_day", 0.0)
        today = datetime.now(timezone.utc).date().isoformat()

        for m in milestones:
            remaining = m["progress"]["total"] - m["progress"]["done"]
            m["eta"] = milestone_eta(
                remaining=remaining,
                velocity_per_day=velocity,
                today=today,
                target_date=m.get("target_date"),
            )
        return JSONResponse({"milestones": milestones})
```

- [ ] **Step 3: Run the dashboard-milestones tests — expect PASS.**
- [ ] **Step 4: Full suite** → 1233 passed, 3 skipped (no new test count; existing test extended).
- [ ] **Step 5: Commit**

```bash
git add orchestrator/dashboard/server.py orchestrator/tests/test_dashboard_milestones.py
git commit -m "feat(dashboard): /api/milestones attaches per-milestone ETA + confidence"
```

---

## Task 3 — Extend the `Milestone` type

**Files:** Modify `frontend/src/hooks/useMilestones.ts`.

- [ ] **Step 1: Add the `eta` shape to the interface**

```typescript
export interface MilestoneEta {
  eta_date: string
  eta_days: number
  confidence: "high" | "low"
}

export interface Milestone {
  // ...existing fields...
  eta: MilestoneEta | null
}
```

- [ ] **Step 2: TypeScript clean**

```bash
cd frontend && pnpm tsc --noEmit 2>&1 | head -20
```

- [ ] **Step 3: Commit**

```bash
git add frontend/src/hooks/useMilestones.ts
git commit -m "feat(spa): extend Milestone type with eta projection"
```

---

## Task 4 — `GanttChart.tsx` (SVG)

**Files:** Create `frontend/src/components/charts/GanttChart.tsx`.

Mirror `BarChartByDay` conventions: fixed `viewBox`, outer `<svg>` stretches to container, no external deps. One horizontal bar per milestone.

- [ ] **Step 1: Create the component**

Geometry: rows of fixed height; X axis spans `[min(created_at, today), max(target_date, eta_date, today)]`; each bar runs from the milestone's start to its `target_date`; a progress overlay fills `pct` of the bar; a "today" vertical rule; an ETA tick colored by `confidence` (green `high` / amber `low`). Render `—` semantics by simply omitting the ETA tick when `eta === null`.

```typescript
import type { Milestone } from "@/hooks/useMilestones"

export interface GanttChartProps {
  milestones: Milestone[]
  today: string // ISO date; injected so the render is deterministic/testable
}

const VB_WIDTH = 900
const ROW_H = 34
const PAD_LEFT = 160 // milestone label gutter
const PAD_RIGHT = 24
const PAD_TOP = 28

// ... date→x scale over [domainStart, domainEnd], one <g> per milestone row,
//     <r:rect> bar + progress overlay, <line> today rule, <circle> eta tick.
//     Keep all math in local helpers; NO Date.now() inside render — use `today`.

export function GanttChart({ milestones, today }: GanttChartProps) {
  if (milestones.length === 0) return null
  const height = PAD_TOP + milestones.length * ROW_H + 24
  // domain = [earliest created_at | today, latest target_date | eta_date | today]
  // xFor(dateStr): number  — linear scale into [PAD_LEFT, VB_WIDTH - PAD_RIGHT]
  return (
    <svg
      viewBox={`0 0 ${VB_WIDTH} ${height}`}
      className="w-full"
      role="img"
      aria-label="Milestone timeline"
    >
      {/* today rule, axis ticks, one row per milestone */}
    </svg>
  )
}
```

Implement `xFor` as a pure linear map and clamp out-of-domain dates to the edges. Colors via Tailwind CSS variables already used by the other charts (`--chart-*` / `text-emerald-500` / `text-amber-500`) so light/dark themes work.

- [ ] **Step 2: TypeScript clean** (`pnpm tsc --noEmit`).
- [ ] **Step 3: Commit**

```bash
git add frontend/src/components/charts/GanttChart.tsx
git commit -m "feat(spa): GanttChart — hand-written SVG milestone timeline"
```

---

## Task 5 — Integrate into `MilestonesPage` + Download SVG

**Files:** Modify `frontend/src/pages/MilestonesPage.tsx`.

- [ ] **Step 1: Render the Gantt as a collapsible section above the cards**

- Import `GanttChart` + a `Collapsible` (shadcn) — or a plain `<details>` if Collapsible isn't installed (check `frontend/src/components/ui/` first; do NOT add a dep for this).
- Pass `today={new Date().toISOString().slice(0, 10)}` from the page (the ONE place a real clock is read).
- Show the section only when `milestones.length > 0`.
- Each card's ETA badge: `m.eta ? \`ETA ${m.eta.eta_date}\` : "ETA —"`, colored by `m.eta?.confidence`.

- [ ] **Step 2: "Download SVG" button**

Serialize the rendered `<svg>` via a `ref` + `new XMLSerializer().serializeToString(node)`, wrap in a `Blob`, trigger an `<a download>`. No library.

```typescript
function downloadSvg(svg: SVGSVGElement | null) {
  if (!svg) return
  const blob = new Blob([new XMLSerializer().serializeToString(svg)], {
    type: "image/svg+xml",
  })
  const url = URL.createObjectURL(blob)
  const a = document.createElement("a")
  a.href = url
  a.download = "milestones-timeline.svg"
  a.click()
  URL.revokeObjectURL(url)
}
```

- [ ] **Step 3: TypeScript clean** (`pnpm tsc --noEmit`).
- [ ] **Step 4: Full pytest suite** → 1233 passed, 3 skipped (frontend untested by pytest; guard against backend regressions).
- [ ] **Step 5: Commit**

```bash
git add frontend/src/pages/MilestonesPage.tsx
git commit -m "feat(spa): milestone timeline section + Download SVG in MilestonesPage"
```

---

## Task 6 — Docs + PR

- [ ] **Step 1: Update roadmap + checklist**

- `docs/brainstorm/next-sprints.md`: mark `### G-3` done (✅ + PR ref), bump footer "Próximo sprint real: G-4 (exec summary) o G-5 (budget chart)".
- `docs/brainstorm/product-checklist.md`: check `Timeline visual (Gantt-like)`; comparison table row `Timeline / Gantt` → `✅ G-3`.

- [ ] **Step 2: Final suite**

```bash
uv run --extra dev pytest orchestrator/tests/ -q 2>&1 | tail -3
# Expected: ≥ 1233 passed, 3 skipped, 0 new failures
```

- [ ] **Step 3: Bump baseline in `CLAUDE.md`** (1226 → 1233) in the same PR.

- [ ] **Step 4: Push + PR**

```bash
git push -u origin sprint-g3/gantt-timeline
gh pr create --base main \
  --title "feat: Sprint G-3 — milestone timeline (Gantt ligero) + ETA projection" \
  --body "Per-milestone tasks-based ETA (milestone_eta) attached to /api/milestones, hand-written SVG GanttChart in MilestonesPage, Download SVG. Closes the G-2 milestone loop; unblocks PDF export. Baseline 1226→1233."
```

---

## Notes for the implementer

- **Velocity is project-wide**, applied per-milestone via each milestone's remaining count. That's an honest approximation for a "light" Gantt — do NOT try to compute per-milestone velocity (not enough signal per group). If a milestone has 0 done project-wide velocity is 0 → `eta = null` → UI shows `—`. That's correct, not a bug.
- **No new dependency.** If shadcn `Collapsible` is absent, use `<details>`/`<summary>`. If a date lib is tempting — it isn't; ISO strings + `date-fns`-free arithmetic is enough (backend does the math).
- **Determinism:** every `today` flows in as a parameter (backend: injected string; frontend: read once at the page boundary). No `Date.now()` buried in helpers — keeps tests stable and mirrors the repo's existing metric helpers.
