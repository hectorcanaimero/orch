# Sprint G-4: Executive summary (deterministic template) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give the stakeholder a copy-paste-ready progress summary in business language, generated automatically. The dev stops writing the weekly client update — orch writes it.

## ⚠️ Design decision (read first): DETERMINISTIC TEMPLATE, not a live LLM call

The roadmap floated "call Claude". We are NOT doing that. Decision (confirmed with the user):

- **The summary is a deterministic template** filled from existing structured metrics (`sprint_health` → done/remaining/blocked/velocity/ETA, `budget_vs_actual` → spend). No LLM, no subprocess, no `claude` CLI dependency on the dashboard host, no per-regen cost, no non-determinism.
- **Why:** the dashboard can run headless on a server where `claude` isn't installed; an LLM call there would just break. All the data an LLM would paraphrase is already structured — we render it, not "generate" it. Deterministic ⇒ fully unit-testable, free, instant.
- **Tone/language configurable via template strings**, not a prompt. `dashboard.summary_language` (`es` | `en`, default `es`) picks the phrasing table.

**Architecture:** New pure helper `executive_summary()` in `orchestrator/dashboard/metrics.py` takes the already-computed `sprint_health` dict + optional budget total + a language and returns `{text, generated_from, language}`. `GET /api/summary` assembles the inputs and serves it. Frontend gains `useExecutiveSummary` + an `ExecutiveSummary` card rendered on `StakeholderSummaryPage`, with a "Copy" button (Clipboard API, no lib).

**Tech Stack:** Python 3.11+, pytest, FastAPI, React + TypeScript + shadcn/ui + Tailwind, pnpm.

---

## Baseline

```bash
uv run --extra dev pytest orchestrator/tests/ -q 2>&1 | tail -3
# Expected: 1243 passed, 3 skipped
# (Two known time-boundary flakes may fail on slow I/O — not regressions.)
```

---

## File Map

| File | Action | Layer |
|------|--------|-------|
| `orchestrator/dashboard/metrics.py` | Modify (add `executive_summary`) | 1 |
| `orchestrator/tests/test_executive_summary.py` | **Create** | 1 |
| `orchestrator/dashboard/server.py` | Modify (`GET /api/summary`) | 2 |
| `orchestrator/tests/test_dashboard_summary.py` | **Create** | 2 |
| `orchestrator/config.yaml` | Modify (`dashboard.summary_language`) | 2 |
| `frontend/src/hooks/useExecutiveSummary.ts` | **Create** | 3 |
| `frontend/src/components/ExecutiveSummary.tsx` | **Create** | 3 |
| `frontend/src/pages/StakeholderSummaryPage.tsx` | Modify (mount the card) | 3 |

**Out of scope (explicit):** live LLM generation, email/Slack delivery (that's G-6), per-milestone summaries, custom free-form prompts.

---

## Task 1 — `executive_summary()` pure helper

**Files:** Modify `orchestrator/dashboard/metrics.py`; Create `orchestrator/tests/test_executive_summary.py`.

Input: the `sprint_health` dict (keys: `velocity_per_day`, `done_count`, `remaining_tasks`, `remaining_hours`, `blocked_count`, `blockers` list, plus the `eta` spread — `eta_date`/`eta_days`/`confidence` when present). Plus `total_spend_usd: float | None` and `language: str`.

Output: `{"text": str, "language": str, "generated_from": {...}}` — `generated_from` echoes the raw figures so the UI can show a "based on" tooltip and tests can assert provenance.

- [ ] **Step 1: Write the failing tests**

Create `orchestrator/tests/test_executive_summary.py`:

```python
"""Tests for executive_summary — deterministic business-language render (G-4)."""
from __future__ import annotations

from orchestrator.dashboard.metrics import executive_summary


def _health(**over):
    base = {
        "velocity_per_day": 2.0,
        "done_count": 5,
        "remaining_tasks": 3,
        "remaining_hours": 6.0,
        "blocked_count": 0,
        "blockers": [],
    }
    base.update(over)
    return base


def test_summary_mentions_done_and_remaining_es():
    r = executive_summary(_health(), total_spend_usd=12.0, language="es")
    assert "5" in r["text"] and "entregad" in r["text"].lower()
    assert "3" in r["text"]
    assert r["language"] == "es"


def test_summary_includes_spend_when_present():
    r = executive_summary(_health(), total_spend_usd=12.5, language="es")
    assert "12.5" in r["text"] or "12,5" in r["text"] or "$12" in r["text"]


def test_summary_omits_spend_when_none():
    r = executive_summary(_health(), total_spend_usd=None, language="es")
    # No dollar figure fabricated when spend is unknown.
    assert "$" not in r["text"]


def test_summary_mentions_blockers_when_present():
    health = _health(
        blocked_count=1,
        blockers=[{"task_id": "T9", "title": "Deploy", "reason": "waiting creds"}],
    )
    r = executive_summary(health, total_spend_usd=None, language="es")
    assert "1" in r["text"]
    assert "bloque" in r["text"].lower()


def test_summary_includes_eta_when_present():
    health = _health(eta_date="2026-09-03", eta_days=4, confidence="high")
    r = executive_summary(health, total_spend_usd=None, language="es")
    assert "2026-09-03" in r["text"]


def test_summary_english_language():
    r = executive_summary(_health(), total_spend_usd=None, language="en")
    assert r["language"] == "en"
    assert "delivered" in r["text"].lower() or "completed" in r["text"].lower()


def test_summary_all_done_reads_as_complete():
    health = _health(done_count=8, remaining_tasks=0, remaining_hours=0.0)
    r = executive_summary(health, total_spend_usd=None, language="es")
    assert "8" in r["text"]


def test_generated_from_echoes_inputs():
    r = executive_summary(_health(done_count=5), total_spend_usd=12.0, language="es")
    assert r["generated_from"]["done_count"] == 5
    assert r["generated_from"]["total_spend_usd"] == 12.0
```

- [ ] **Step 2: Run — expect FAIL** (`ImportError`).

- [ ] **Step 3: Implement `executive_summary` in `metrics.py`**

Keep it a straight-line template. Two small phrasing tables (`es`/`en`). Assemble 2-4 short sentences: progress (done/remaining), ETA (if present), blockers (if any), spend (if present). Fall back to `es` on an unknown language.

```python
def executive_summary(
    health: dict,
    total_spend_usd: float | None,
    language: str = "es",
) -> dict:
    """Render a deterministic business-language progress summary from the
    already-computed sprint_health dict. No LLM — every sentence is a template
    filled from real figures. `generated_from` echoes the inputs for provenance.
    """
    lang = language if language in ("es", "en") else "es"
    done = int(health.get("done_count", 0))
    remaining = int(health.get("remaining_tasks", 0))
    blocked = int(health.get("blocked_count", 0))
    eta_date = health.get("eta_date")
    parts: list[str] = []

    if lang == "es":
        parts.append(f"{done} tareas entregadas, {remaining} restantes.")
        if eta_date:
            parts.append(f"ETA estimado: {eta_date}.")
        if blocked:
            parts.append(f"{blocked} bloqueada(s) — requieren atención.")
        if total_spend_usd is not None:
            parts.append(f"Gastado en AI: ${total_spend_usd:.2f}.")
    else:  # en
        parts.append(f"{done} tasks delivered, {remaining} remaining.")
        if eta_date:
            parts.append(f"Estimated ETA: {eta_date}.")
        if blocked:
            parts.append(f"{blocked} blocked — needs attention.")
        if total_spend_usd is not None:
            parts.append(f"AI spend: ${total_spend_usd:.2f}.")

    return {
        "text": " ".join(parts),
        "language": lang,
        "generated_from": {
            "done_count": done,
            "remaining_tasks": remaining,
            "blocked_count": blocked,
            "eta_date": eta_date,
            "total_spend_usd": total_spend_usd,
        },
    }
```

- [ ] **Step 4: Run — expect PASS** (8 tests).
- [ ] **Step 5: Full suite** → 1251 passed, 3 skipped.
- [ ] **Step 6: Commit** `feat(metrics): executive_summary — deterministic business-language render`.

---

## Task 2 — `GET /api/summary`

**Files:** Modify `orchestrator/dashboard/server.py`; Create `orchestrator/tests/test_dashboard_summary.py`; Modify `orchestrator/config.yaml`.

Assemble the inputs the same way the existing endpoints do: `sprint_health` (as in `/api/sprint`) for the health dict, and the budget/spend total (sum of `cost_usd` from today's spend, via `aggregate_by_provider` — the same figure `/api/budget/summary` surfaces). Read `dashboard.summary_language` from config (default `es`).

- [ ] **Step 1: Write failing tests** — a project with seeded tasks/spend; assert `/api/summary` returns `{available, summary:{text, language, generated_from}}` and that `text` is non-empty. Assert `available: false` / graceful shape for the file backend if sprint_health needs SQLite (mirror `/api/sprint`'s `available` guard).

- [ ] **Step 2: Add the route** (near `/api/sprint`)

```python
    @app.get("/api/summary", name="api_summary")
    def api_summary():
        """Deterministic executive summary from sprint health + spend (G-4)."""
        import yaml
        from orchestrator.state.sqlite_backend import SqliteBackend
        from orchestrator.dashboard.metrics import (
            executive_summary, sprint_health,
        )
        from orchestrator.spend_reader import (
            aggregate_by_provider, iter_today_entries,
        )

        cfg_path = app_state.paths.config_yaml
        try:
            raw_cfg = yaml.safe_load(cfg_path.read_text(encoding="utf-8")) or {}
        except (OSError, ValueError, yaml.YAMLError):
            raw_cfg = {}
        language = (raw_cfg.get("dashboard") or {}).get("summary_language") or "es"

        backend = _get_state_backend(app_state.paths, raw_cfg)
        if not isinstance(backend, SqliteBackend):
            return JSONResponse({"available": False, "summary": None})

        tasks = load_tasks(app_state.paths.tasks_json)
        done_7d = backend.count_done_last_n_days(7)
        blocked_ids = [t.id for t in tasks if t.status == "blocked"]
        last_events = backend.get_task_last_events(blocked_ids) if blocked_ids else {}
        health = sprint_health(tasks, done_7d, last_events)

        spend = aggregate_by_provider(iter_today_entries(app_state.paths.state_dir))
        total_spend = round(sum(spend.values()), 2) if spend else None

        summary = executive_summary(health, total_spend_usd=total_spend, language=language)
        return JSONResponse({"available": True, "summary": summary})
```

- [ ] **Step 3: Add `dashboard.summary_language: es` default to `config.yaml`.**
- [ ] **Step 4: Run tests — expect PASS.**
- [ ] **Step 5: Full suite** → 1251+ passed, 3 skipped.
- [ ] **Step 6: Commit** `feat(dashboard): GET /api/summary — deterministic exec summary`.

---

## Task 3 — `useExecutiveSummary` hook

**Files:** Create `frontend/src/hooks/useExecutiveSummary.ts`.

```typescript
import { useQuery } from "@tanstack/react-query"

export interface ExecutiveSummary {
  text: string
  language: string
  generated_from: Record<string, unknown>
}

interface SummaryResponse {
  available: boolean
  summary: ExecutiveSummary | null
}

async function fetchSummary(): Promise<SummaryResponse> {
  const resp = await fetch("/api/summary")
  if (!resp.ok) throw new Error(`summary failed: ${resp.status}`)
  return (await resp.json()) as SummaryResponse
}

export function useExecutiveSummary() {
  return useQuery({
    queryKey: ["executive-summary"],
    queryFn: fetchSummary,
    refetchInterval: 30_000,
  })
}
```

- [ ] TypeScript clean; commit.

---

## Task 4 — `ExecutiveSummary` card + Copy button

**Files:** Create `frontend/src/components/ExecutiveSummary.tsx`.

- Card titled "Resumen ejecutivo" / "Executive summary".
- Renders `summary.text`.
- "Copy" button → `navigator.clipboard.writeText(summary.text)` (no lib); show a transient "Copiado ✓" state.
- When `available === false`, render nothing (or a muted "requires SQLite backend" note) — don't crash.

- [ ] Implement; TypeScript clean; commit `feat(spa): ExecutiveSummary card with copy-to-clipboard`.

---

## Task 5 — Mount on `StakeholderSummaryPage`

**Files:** Modify `frontend/src/pages/StakeholderSummaryPage.tsx`.

- Import `ExecutiveSummary`, render it near the top (above the existing summary widgets) so it's the first thing a stakeholder reads.
- It uses its own hook, so no prop threading.

- [ ] **Step 1:** Mount the component.
- [ ] **Step 2:** TypeScript clean (`pnpm tsc --noEmit`).
- [ ] **Step 3:** Full pytest suite — no regressions.
- [ ] **Step 4:** Commit `feat(spa): mount ExecutiveSummary on StakeholderSummaryPage`.

---

## Task 6 — Docs + PR

- [ ] `docs/brainstorm/next-sprints.md`: mark `### G-4` done (✅), note the deterministic-template decision, update footer ("Próximo: G-6 notif").
- [ ] `docs/brainstorm/product-checklist.md`: check `Executive summary auto-generado`; comparison table `Executive summary por IA` → note it's template-based (honest: not "por IA").
- [ ] Bump baseline in `CLAUDE.md` (1243 → final).
- [ ] Final suite (accept the two known flakes), push, PR:

```bash
git push -u origin sprint-g4/exec-summary
gh pr create --base main \
  --title "feat: Sprint G-4 — executive summary (deterministic template)" \
  --body "Deterministic business-language progress summary from sprint_health + spend — no LLM, no CLI dependency, fully testable. GET /api/summary + ExecutiveSummary card with copy-to-clipboard on StakeholderSummaryPage. Language via dashboard.summary_language."
```

---

## Notes for the implementer

- **NO LLM, NO subprocess.** The whole point is determinism + zero host dependency. If you find yourself importing the dispatcher or shelling out to `claude`, stop — that's the rejected design.
- **Honesty in docs:** the comparison-table row says "Executive summary por IA". It's NOT AI — it's a template. Update the row to reflect that (e.g. "Executive summary (auto)") rather than claim an LLM we don't call.
- **Reuse, don't recompute.** `sprint_health` and `aggregate_by_provider` already exist and are what `/api/sprint` + `/api/budget/summary` use. `executive_summary` only formats.
- **No new dependency.** Clipboard API is native; phrasing tables are plain dicts.
- **Two known flakes** may fail on slow I/O — a single failure in either is not a regression (see CLAUDE.md).
```
