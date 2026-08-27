# Sprint G-5: Budget vs actual chart + spend stakeholder — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the budget/actual asymmetry. Today `budgets.yaml` holds per-provider quota **limits** and `spend-*.jsonl` holds **actual** usage, but nothing cross-references them. This sprint adds `GET /api/budget/summary` (limit vs used, per provider) + a hand-written SVG `BudgetChart` + a `BudgetPage` gated for stakeholders.

## ⚠️ Unit-mismatch decision (read first)

`budgets.yaml` budgets are in **tokens** (`ProviderBudget.token_budget`), NOT dollars. Actual spend rows carry BOTH `tokens_in`/`tokens_out` (via `metrics_by_model`) AND `cost_usd`. So:

- **The budget bar compares tokens vs `token_budget`** — the same unit the guardrail enforces. This is the honest comparison; `pct = used_tokens / token_budget`.
- **`cost_usd` is shown alongside as an informational figure** ("~$12 spent"), NOT as the budget axis. We do NOT invent a USD budget the config doesn't have.

The roadmap's "budget vs actual in USD" framing is imprecise — the config has no USD limit. We surface real USD spend for transparency but gauge "how close to the cap" in tokens.

**Architecture:** New pure helper `budget_vs_actual()` in `orchestrator/dashboard/metrics.py` pairs each provider's `token_budget` (from `load_budget_config`) with rolling-window tokens used (same window logic `budget.py` already applies) + `cost_usd` (from spend rows). `GET /api/budget/summary` serves it. Frontend gains `BudgetChart.tsx` (SVG, mirrors `BarChartByDay`/`GanttChart`) + `BudgetPage.tsx` + a nav item gated by `dashboard.show_spend_to_stakeholder`.

**Tech Stack:** Python 3.11+, SQLite, pytest, FastAPI, React + TypeScript + shadcn/ui + Tailwind, pnpm.

---

## Baseline

```bash
uv run --extra dev pytest orchestrator/tests/ -q 2>&1 | tail -3
# Expected: 1234 passed, 3 skipped
# (Two known time-boundary flakes may fail on slow I/O — not regressions:
#  test_count_done_last_n_days, test_start_writes_atomic_state_json.)
```

---

## File Map

| File | Action | Layer |
|------|--------|-------|
| `orchestrator/dashboard/metrics.py` | Modify (add `budget_vs_actual`) | 1 |
| `orchestrator/tests/test_budget_vs_actual.py` | **Create** | 1 |
| `orchestrator/dashboard/server.py` | Modify (`GET /api/budget/summary`) | 2 |
| `orchestrator/tests/test_dashboard_budget_summary.py` | **Create** | 2 |
| `frontend/src/hooks/useBudgetSummary.ts` | **Create** | 3 |
| `frontend/src/components/charts/BudgetChart.tsx` | **Create** | 3 |
| `frontend/src/pages/BudgetPage.tsx` | **Create** | 3 |
| `frontend/src/App.tsx` | Modify (add `/budget` route) | 3 |
| `frontend/src/components/AppLayout.tsx` | Modify (nav item, gated) | 3 |

**Out of scope (explicit):** spend-per-milestone (needs tasks→milestone→spend join), future-spend projection, budget alerts/notifications (that's G-6).

---

## Task 1 — `budget_vs_actual()` pure helper

**Files:** Modify `orchestrator/dashboard/metrics.py`; Create `orchestrator/tests/test_budget_vs_actual.py`.

- [ ] **Step 1: Write the failing tests**

Create `orchestrator/tests/test_budget_vs_actual.py`:

```python
"""Tests for budget_vs_actual — per-provider limit vs used (Sprint G-5)."""
from __future__ import annotations

from orchestrator.budget import BudgetConfig, ProviderBudget
from orchestrator.dashboard.metrics import budget_vs_actual


def _cfg(**providers: ProviderBudget) -> BudgetConfig:
    return BudgetConfig(providers=dict(providers))


def test_pairs_limit_with_used_tokens():
    cfg = _cfg(claude=ProviderBudget(window_hours=5, token_budget=1000, threshold_pct=80))
    rows = budget_vs_actual(cfg, used_by_provider={"claude": 250}, cost_by_provider={"claude": 3.5})
    assert len(rows) == 1
    r = rows[0]
    assert r["provider"] == "claude"
    assert r["token_budget"] == 1000
    assert r["tokens_used"] == 250
    assert r["pct"] == 25
    assert r["cost_usd"] == 3.5
    assert r["over_threshold"] is False


def test_flags_over_threshold():
    cfg = _cfg(codex=ProviderBudget(window_hours=5, token_budget=1000, threshold_pct=80))
    rows = budget_vs_actual(cfg, used_by_provider={"codex": 850}, cost_by_provider={})
    assert rows[0]["pct"] == 85
    assert rows[0]["over_threshold"] is True   # 85% >= 80% threshold
    assert rows[0]["cost_usd"] == 0.0          # missing cost → 0


def test_provider_with_no_usage_is_zero_not_dropped():
    cfg = _cfg(opencode=ProviderBudget(window_hours=5, token_budget=500, threshold_pct=90))
    rows = budget_vs_actual(cfg, used_by_provider={}, cost_by_provider={})
    assert rows[0]["tokens_used"] == 0
    assert rows[0]["pct"] == 0


def test_zero_budget_guards_division():
    cfg = _cfg(bad=ProviderBudget(window_hours=5, token_budget=0, threshold_pct=80))
    rows = budget_vs_actual(cfg, used_by_provider={"bad": 10}, cost_by_provider={})
    assert rows[0]["pct"] == 0          # no divide-by-zero
    assert rows[0]["over_threshold"] is False


def test_rows_sorted_by_provider_name():
    cfg = _cfg(
        zeta=ProviderBudget(window_hours=5, token_budget=100, threshold_pct=80),
        alpha=ProviderBudget(window_hours=5, token_budget=100, threshold_pct=80),
    )
    rows = budget_vs_actual(cfg, used_by_provider={}, cost_by_provider={})
    assert [r["provider"] for r in rows] == ["alpha", "zeta"]
```

- [ ] **Step 2: Run — expect FAIL** (`ImportError`).

- [ ] **Step 3: Implement `budget_vs_actual` in `metrics.py`**

```python
def budget_vs_actual(
    cfg: "BudgetConfig",
    used_by_provider: dict[str, int],
    cost_by_provider: dict[str, float],
) -> list[dict]:
    """Pair each configured provider's token budget with tokens actually used
    (rolling window) + USD spent. Pure — callers supply the aggregates.

    `pct` and `over_threshold` are computed in TOKENS (the unit the guardrail
    enforces). `cost_usd` rides along as an informational figure only.
    """
    rows = []
    for provider in sorted(cfg.providers):
        pb = cfg.providers[provider]
        used = int(used_by_provider.get(provider, 0))
        budget = int(pb.token_budget)
        pct = int(used / budget * 100) if budget > 0 else 0
        rows.append({
            "provider": provider,
            "token_budget": budget,
            "tokens_used": used,
            "pct": pct,
            "threshold_pct": pb.threshold_pct,
            "over_threshold": budget > 0 and pct >= pb.threshold_pct,
            "cost_usd": round(float(cost_by_provider.get(provider, 0.0)), 4),
        })
    return rows
```

Add `from orchestrator.budget import BudgetConfig` under `TYPE_CHECKING` if a type import is wanted; the runtime body only reads attributes so a string annotation is fine.

- [ ] **Step 4: Run — expect PASS** (5 tests).
- [ ] **Step 5: Full suite** → 1239 passed, 3 skipped.
- [ ] **Step 6: Commit**

```bash
git add orchestrator/dashboard/metrics.py orchestrator/tests/test_budget_vs_actual.py
git commit -m "feat(metrics): budget_vs_actual — per-provider token limit vs used"
```

---

## Task 2 — `GET /api/budget/summary`

**Files:** Modify `orchestrator/dashboard/server.py`; Create `orchestrator/tests/test_dashboard_budget_summary.py`.

The existing `/api/budgets` returns the CONFIG snapshot only. This new route joins config + actual usage. Reuse: `load_budget_config` (already imported for `/api/budgets`), the rolling-window token sum from `budget.py`, and `aggregate_by_provider` (spend_reader) for `cost_usd`.

- [ ] **Step 1: Write failing tests** — a project with a `budgets.yaml` preset + seeded `spend-*.jsonl`; assert `/api/budget/summary` returns `{available, rows:[{provider, token_budget, tokens_used, pct, cost_usd, over_threshold}]}`. Also assert `available: false` when no `budgets.yaml`.

- [ ] **Step 2: Add the route** (after `api_budgets`, ~line 748)

```python
    @app.get("/api/budget/summary", name="api_budget_summary")
    def api_budget_summary():
        """Budget vs actual: configured token budget vs rolling-window tokens
        used + USD spent, per provider. Requires budgets.yaml."""
        import os
        from orchestrator.budget import load_budget_config, tokens_used_by_provider
        from orchestrator.dashboard.metrics import budget_vs_actual
        from orchestrator.spend_reader import iter_today_entries, aggregate_by_provider

        preset = os.environ.get("ORCH_BUDGETS_PRESET") or "conservative"
        candidates = [
            app_state.paths.config_yaml.parent / "budgets.yaml",
            app_state.paths.project_root / "budgets.yaml",
        ]
        cfg = None
        for candidate in candidates:
            try:
                cfg = load_budget_config(candidate, preset=preset)
            except (ValueError, FileNotFoundError):
                cfg = None
            if cfg is not None:
                break
        if cfg is None or not cfg.providers:
            return JSONResponse({"available": False, "rows": []})

        state_dir = app_state.paths.state_dir
        used = tokens_used_by_provider(cfg, state_dir)     # {provider: tokens} in-window
        cost = aggregate_by_provider(iter_today_entries(state_dir))
        rows = budget_vs_actual(cfg, used_by_provider=used, cost_by_provider=cost)
        return JSONResponse({"available": True, "rows": rows})
```

Note: if a `tokens_used_by_provider(cfg, state_dir)` helper does not already exist in `budget.py`, extract the in-window token sum that `budget.py`'s gate uses into that small pure helper (one function; keep the gate calling it too so there's a single source of truth). Add a unit test for it in `test_budget.py` if you create it, and bump the baseline count accordingly.

- [ ] **Step 3: Run the endpoint tests — expect PASS.**
- [ ] **Step 4: Full suite** → 1239+ passed (adjust for any helper test added), 3 skipped.
- [ ] **Step 5: Commit**

```bash
git add orchestrator/dashboard/server.py orchestrator/tests/test_dashboard_budget_summary.py orchestrator/budget.py
git commit -m "feat(dashboard): GET /api/budget/summary — limit vs actual per provider"
```

---

## Task 3 — `useBudgetSummary` hook + type

**Files:** Create `frontend/src/hooks/useBudgetSummary.ts`.

```typescript
import { useQuery } from "@tanstack/react-query"

export interface BudgetRow {
  provider: string
  token_budget: number
  tokens_used: number
  pct: number
  threshold_pct: number
  over_threshold: boolean
  cost_usd: number
}

interface BudgetSummary {
  available: boolean
  rows: BudgetRow[]
}

async function fetchBudgetSummary(): Promise<BudgetSummary> {
  const resp = await fetch("/api/budget/summary")
  if (!resp.ok) throw new Error(`budget summary failed: ${resp.status}`)
  return (await resp.json()) as BudgetSummary
}

export function useBudgetSummary() {
  return useQuery({
    queryKey: ["budget-summary"],
    queryFn: fetchBudgetSummary,
    refetchInterval: 15_000,
  })
}
```

- [ ] TypeScript clean; commit `feat(spa): useBudgetSummary hook`.

---

## Task 4 — `BudgetChart.tsx` (SVG)

**Files:** Create `frontend/src/components/charts/BudgetChart.tsx`.

Mirror `GanttChart`/`BarChartByDay` conventions: fixed `viewBox`, `width="100%"`, Tailwind `dark:` classes, no deps. One horizontal bar per provider: the track is `token_budget`, the fill is `tokens_used`, a vertical rule marks `threshold_pct`, the fill turns amber/red when `over_threshold`. Label each bar with `pct%` and the informational `~$cost` on the right.

- [ ] Implement; TypeScript clean; commit `feat(spa): BudgetChart — token budget vs used SVG`.

---

## Task 5 — `BudgetPage` + route + gated nav

**Files:** Create `frontend/src/pages/BudgetPage.tsx`; Modify `App.tsx`, `AppLayout.tsx`.

- [ ] **Step 1: `BudgetPage.tsx`** — loading/error/empty states (mirror `MilestonesPage`); when `available === false`, render a card explaining `budgets.yaml` isn't configured. Otherwise render `<BudgetChart rows={data.rows} />` plus a per-provider list showing `tokens_used / token_budget` and `~$cost_usd`.

- [ ] **Step 2: Route in `App.tsx`** — add `/budget` inside `<Routes>` following the `ProtectedRoute > AppLayout` pattern the other pages use.

- [ ] **Step 3: Nav item in `AppLayout.tsx`** — add to `NAV_ITEMS`:

```typescript
  { to: "/budget", label: "Budget", icon: Wallet, stakeholderGated: true },
```

**Stakeholder gating decision:** the nav array already filters `operatorOnly` items for stakeholders. Budget spend is sensitive, so it should be HIDDEN from stakeholders UNLESS the project opts in via `dashboard.show_spend_to_stakeholder: true`. Implement a new predicate: the item shows for operators always, and for stakeholders only when the config flag is on. Read the flag from `useProjectConfig()` (already used by `MilestonesPage`); extend `_load_project_config` in `server.py` to expose `dashboard.show_spend_to_stakeholder` (default `false`) in the whitelist. Add the config default to `orchestrator/config.yaml`.

- [ ] **Step 4: TypeScript clean** (`pnpm tsc --noEmit`).
- [ ] **Step 5: Full pytest suite** — no regressions.
- [ ] **Step 6: Commit** `feat(spa): BudgetPage + gated nav + show_spend_to_stakeholder flag`.

---

## Task 6 — Docs + PR

- [ ] `docs/brainstorm/next-sprints.md`: mark `### G-5` done (✅), update footer ("Próximo: G-4 exec summary o G-6 notif").
- [ ] `docs/brainstorm/product-checklist.md`: check `Budget vs actual chart` + `Spend dashboard para el cliente`; comparison table if a row applies.
- [ ] Bump baseline in `CLAUDE.md` (1234 → final count) in the same PR.
- [ ] Final suite (accept the two known flakes on slow runs), push, PR:

```bash
git push -u origin sprint-g5/budget-vs-actual
gh pr create --base main \
  --title "feat: Sprint G-5 — budget vs actual chart + stakeholder spend view" \
  --body "Per-provider token-budget vs used + USD spent via /api/budget/summary, SVG BudgetChart, BudgetPage gated by dashboard.show_spend_to_stakeholder. Unit note: budget is tokens (the guardrail's unit); USD rides along informationally."
```

---

## Notes for the implementer

- **Do NOT invent a USD budget.** The config has token budgets only. The gauge is tokens/token_budget; USD is a read-only transparency figure. Conflating them is the one thing that makes this feature dishonest.
- **Single source of truth for in-window token usage.** If you add `tokens_used_by_provider`, make `budget.py`'s existing gate call it too — don't duplicate the window-sum logic.
- **No new dependency.** SVG by hand, `<details>`/shadcn primitives already present.
- **Stakeholder default is OFF.** `show_spend_to_stakeholder: false`. Spend is sensitive; opt-in only.
- **Two known flakes** (`test_count_done_last_n_days`, `test_start_writes_atomic_state_json`) may fail on slow I/O — a single failure in either is not a regression (see CLAUDE.md).
