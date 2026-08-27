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
