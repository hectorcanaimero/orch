"""Unit tests for `orchestrator.budget` (Sprint 7 — budget guardrails).

Covers the read-side contract the dispatcher and dashboard depend on:
    - Sliding window aggregation across day boundaries.
    - `can_dispatch(provider)` gate returns (ok, reason, reset_at).
    - `all_capped()` when every configured provider is over threshold.
    - `snapshot()` for the dashboard (per-provider usage_pct + reset_at).
    - Missing spend log → 0 usage (fresh runs never block).
    - Unknown providers → no-op (`can_dispatch` returns ok).
    - Preset selection from `budgets.yaml` (conservative/aggressive/shared).
"""

from __future__ import annotations

from datetime import datetime, timedelta, timezone
from pathlib import Path

import pytest

from orchestrator.budget import (
    BudgetConfig,
    BudgetGate,
    ProviderBudget,
    load_budget_config,
)
from orchestrator.models import SpendEntry
from orchestrator.state import SpendLog


# ---- Helpers -------------------------------------------------------------


def _iso(ts: datetime) -> str:
    return ts.strftime("%Y-%m-%dT%H:%M:%SZ")


def _entry(
    *,
    backend: str,
    tokens_in: int = 1000,
    tokens_out: int = 500,
    ts: datetime | None = None,
    task_id: str = "T-1",
) -> SpendEntry:
    if ts is None:
        ts = datetime.now(timezone.utc)
    return SpendEntry(
        ts=_iso(ts),
        task_id=task_id,
        backend=backend,  # type: ignore[arg-type]
        model=f"{backend}-model",
        tokens_in=tokens_in,
        tokens_out=tokens_out,
        cost_usd=0.01,
        duration_s=1.0,
    )


def _write_entry(state_dir: Path, entry: SpendEntry) -> None:
    """Write a SpendEntry to the correct daily file (based on entry.ts)."""
    # SpendLog picks today's file — for backdated writes we need to place them
    # into the day matching entry.ts, so tests can populate history explicitly.
    import json
    from dataclasses import asdict

    day = entry.ts[:10]  # YYYY-MM-DD prefix of ISO string
    path = state_dir / f"spend-{day}.jsonl"
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("a", encoding="utf-8") as fh:
        fh.write(json.dumps(asdict(entry), separators=(",", ":")) + "\n")


def _cfg(**overrides: ProviderBudget) -> BudgetConfig:
    defaults = {
        "claude": ProviderBudget(
            window_hours=5, token_budget=100_000, threshold_pct=80
        ),
        "codex": ProviderBudget(
            window_hours=3, token_budget=50_000, threshold_pct=80
        ),
        "opencode": ProviderBudget(
            window_hours=24, token_budget=200_000, threshold_pct=90
        ),
    }
    defaults.update(overrides)
    return BudgetConfig(providers=defaults)


# ---- can_dispatch --------------------------------------------------------


def test_can_dispatch_empty_history_returns_ok(tmp_path: Path) -> None:
    gate = BudgetGate(state_dir=tmp_path, config=_cfg())
    ok, reason, reset_at = gate.can_dispatch("claude")
    assert ok is True
    assert reason is None
    assert reset_at is None


def test_can_dispatch_under_threshold_returns_ok(tmp_path: Path) -> None:
    now = datetime.now(timezone.utc)
    # Claude budget=100K, threshold=80% → cap at 80K. Write 40K (well under).
    _write_entry(
        tmp_path,
        _entry(backend="claude", tokens_in=25_000, tokens_out=15_000, ts=now),
    )
    gate = BudgetGate(state_dir=tmp_path, config=_cfg())
    ok, reason, reset_at = gate.can_dispatch("claude")
    assert ok is True
    assert reason is None


def test_can_dispatch_over_threshold_returns_blocked(tmp_path: Path) -> None:
    now = datetime.now(timezone.utc)
    # 90K > 80K cap → blocked.
    _write_entry(
        tmp_path,
        _entry(backend="claude", tokens_in=60_000, tokens_out=30_000, ts=now),
    )
    gate = BudgetGate(state_dir=tmp_path, config=_cfg())
    ok, reason, reset_at = gate.can_dispatch("claude")
    assert ok is False
    assert reason is not None
    assert "threshold" in reason.lower() or "budget" in reason.lower()
    assert reset_at is not None
    # Reset should be the oldest-in-window ts + window_hours.
    assert reset_at > now


def test_can_dispatch_ignores_entries_older_than_window(tmp_path: Path) -> None:
    now = datetime.now(timezone.utc)
    # Old entry, 10h ago, way outside the 5h claude window.
    _write_entry(
        tmp_path,
        _entry(
            backend="claude",
            tokens_in=60_000,
            tokens_out=30_000,
            ts=now - timedelta(hours=10),
        ),
    )
    gate = BudgetGate(state_dir=tmp_path, config=_cfg())
    ok, reason, reset_at = gate.can_dispatch("claude")
    assert ok is True


def test_can_dispatch_sliding_window_across_day_boundary(tmp_path: Path) -> None:
    # An entry 2h ago that lives in yesterday's file (UTC midnight rollover).
    # opencode window is 24h — must still see it.
    now = datetime.now(timezone.utc)
    two_hours_ago = now - timedelta(hours=2)
    _write_entry(
        tmp_path,
        _entry(
            backend="opencode",
            tokens_in=100_000,
            tokens_out=90_000,  # 190K vs 200K budget * 90% = 180K cap → over
            ts=two_hours_ago,
        ),
    )
    gate = BudgetGate(state_dir=tmp_path, config=_cfg())
    ok, reason, reset_at = gate.can_dispatch("opencode")
    assert ok is False


def test_can_dispatch_only_sums_matching_provider(tmp_path: Path) -> None:
    now = datetime.now(timezone.utc)
    # Codex uses 100K (way over its 40K cap) but claude has zero.
    _write_entry(
        tmp_path,
        _entry(backend="codex", tokens_in=60_000, tokens_out=40_000, ts=now),
    )
    gate = BudgetGate(state_dir=tmp_path, config=_cfg())
    assert gate.can_dispatch("claude")[0] is True
    assert gate.can_dispatch("codex")[0] is False


def test_can_dispatch_unknown_provider_is_no_op(tmp_path: Path) -> None:
    gate = BudgetGate(state_dir=tmp_path, config=_cfg())
    ok, reason, reset_at = gate.can_dispatch("mysteryprovider")
    assert ok is True
    assert reason is None


# ---- all_capped ---------------------------------------------------------


def test_all_capped_false_when_any_provider_ok(tmp_path: Path) -> None:
    now = datetime.now(timezone.utc)
    _write_entry(
        tmp_path,
        _entry(backend="claude", tokens_in=60_000, tokens_out=30_000, ts=now),
    )
    gate = BudgetGate(state_dir=tmp_path, config=_cfg())
    # claude over, codex + opencode fine.
    assert gate.all_capped() is False


def test_all_capped_true_when_every_provider_over(tmp_path: Path) -> None:
    now = datetime.now(timezone.utc)
    _write_entry(
        tmp_path,
        _entry(backend="claude", tokens_in=60_000, tokens_out=30_000, ts=now),
    )
    _write_entry(
        tmp_path,
        _entry(backend="codex", tokens_in=30_000, tokens_out=15_000, ts=now),
    )
    _write_entry(
        tmp_path,
        _entry(backend="opencode", tokens_in=100_000, tokens_out=90_000, ts=now),
    )
    gate = BudgetGate(state_dir=tmp_path, config=_cfg())
    assert gate.all_capped() is True


def test_earliest_reset_returns_soonest_reset_across_providers(
    tmp_path: Path,
) -> None:
    now = datetime.now(timezone.utc)
    # Codex entry 1h ago (3h window → resets in 2h).
    # Claude entry 3h ago (5h window → resets in 2h).
    # Opencode entry 5h ago (24h window → resets in 19h).
    _write_entry(
        tmp_path,
        _entry(
            backend="codex",
            tokens_in=30_000,
            tokens_out=15_000,
            ts=now - timedelta(hours=1),
        ),
    )
    _write_entry(
        tmp_path,
        _entry(
            backend="claude",
            tokens_in=60_000,
            tokens_out=30_000,
            ts=now - timedelta(hours=3),
        ),
    )
    _write_entry(
        tmp_path,
        _entry(
            backend="opencode",
            tokens_in=100_000,
            tokens_out=90_000,
            ts=now - timedelta(hours=5),
        ),
    )
    gate = BudgetGate(state_dir=tmp_path, config=_cfg())
    reset = gate.earliest_reset()
    assert reset is not None
    # Should be ~2h from now (codex or claude), well before opencode (19h out).
    delta = (reset - now).total_seconds()
    assert 1.5 * 3600 < delta < 2.5 * 3600


# ---- snapshot -----------------------------------------------------------


def test_snapshot_returns_all_providers_with_usage(tmp_path: Path) -> None:
    now = datetime.now(timezone.utc)
    _write_entry(
        tmp_path,
        _entry(backend="claude", tokens_in=25_000, tokens_out=15_000, ts=now),
    )
    gate = BudgetGate(state_dir=tmp_path, config=_cfg())
    snap = gate.snapshot()
    assert set(snap.keys()) == {"claude", "codex", "opencode"}
    claude = snap["claude"]
    assert claude["tokens_used"] == 40_000
    assert claude["token_budget"] == 100_000
    assert abs(claude["usage_pct"] - 40.0) < 0.01
    assert claude["threshold_pct"] == 80
    assert claude["window_hours"] == 5
    assert claude["capped"] is False
    # Zero-usage providers still appear, just with 0 usage.
    assert snap["codex"]["tokens_used"] == 0
    assert snap["codex"]["capped"] is False


def test_snapshot_marks_capped_when_over_threshold(tmp_path: Path) -> None:
    now = datetime.now(timezone.utc)
    _write_entry(
        tmp_path,
        _entry(backend="claude", tokens_in=60_000, tokens_out=30_000, ts=now),
    )
    gate = BudgetGate(state_dir=tmp_path, config=_cfg())
    snap = gate.snapshot()
    assert snap["claude"]["capped"] is True
    assert snap["claude"]["reset_at"] is not None


# ---- Config loader ------------------------------------------------------


def test_load_budget_config_reads_preset(tmp_path: Path) -> None:
    path = tmp_path / "budgets.yaml"
    path.write_text(
        """
presets:
  conservative:
    claude:   {window_hours: 5,  token_budget: 800000,  threshold_pct: 60}
    codex:    {window_hours: 3,  token_budget: 400000,  threshold_pct: 60}
    opencode: {window_hours: 24, token_budget: 2000000, threshold_pct: 70}
  aggressive:
    claude:   {window_hours: 5,  token_budget: 800000,  threshold_pct: 90}
    codex:    {window_hours: 3,  token_budget: 400000,  threshold_pct: 90}
    opencode: {window_hours: 24, token_budget: 2000000, threshold_pct: 95}
""",
        encoding="utf-8",
    )
    cfg = load_budget_config(path, preset="aggressive")
    assert cfg.providers["claude"].threshold_pct == 90
    assert cfg.providers["opencode"].token_budget == 2_000_000


def test_load_budget_config_unknown_preset_raises(tmp_path: Path) -> None:
    path = tmp_path / "budgets.yaml"
    path.write_text(
        "presets:\n  conservative:\n    claude: {window_hours: 5, token_budget: 100, threshold_pct: 50}\n",
        encoding="utf-8",
    )
    with pytest.raises(ValueError, match="preset"):
        load_budget_config(path, preset="doesnotexist")


def test_load_budget_config_missing_file_returns_none() -> None:
    # A missing budgets file is not fatal — the gate is disabled.
    cfg = load_budget_config(Path("/nonexistent/budgets.yaml"), preset="conservative")
    assert cfg is None


# ---- Disabled gate -------------------------------------------------------


def test_disabled_gate_never_blocks(tmp_path: Path) -> None:
    """When BudgetGate.disabled=True, can_dispatch always returns ok."""
    now = datetime.now(timezone.utc)
    _write_entry(
        tmp_path,
        _entry(backend="claude", tokens_in=60_000, tokens_out=30_000, ts=now),
    )
    # Disabled gate: instantiate with config=None.
    gate = BudgetGate(state_dir=tmp_path, config=None)
    ok, reason, reset_at = gate.can_dispatch("claude")
    assert ok is True
    assert reason is None
    assert gate.all_capped() is False
    assert gate.snapshot() == {}
