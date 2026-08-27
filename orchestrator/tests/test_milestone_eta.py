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


def test_bad_target_date_falls_back_to_30_day_rule():
    r = milestone_eta(
        remaining=2, velocity_per_day=2.0, today="2026-08-27",
        target_date="not-a-date",
    )
    assert r["confidence"] == "high"   # 1 day out, target unparseable → 30d rule
