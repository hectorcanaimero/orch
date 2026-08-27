"""Tests for executive_summary — deterministic business-language render (G-4).

Single source of truth for /api/summary AND the stakeholder payload — the
Sprint E-7 inline summary was folded into this helper, so these tests cover
progress %, in-progress, blocked + reasons, ETA (date or hours), and spend.
"""
from __future__ import annotations

from orchestrator.dashboard.metrics import executive_summary


def test_summary_reports_pct_done_and_total_es():
    r = executive_summary(done=5, total=8, language="es")
    assert "5" in r["text"] and "8" in r["text"]
    assert "62%" in r["text"] or "63%" in r["text"]  # 5/8 = 62.5 → 62/63
    assert "entregad" in r["text"].lower()
    assert r["language"] == "es"


def test_summary_includes_spend_when_present():
    r = executive_summary(done=5, total=8, total_spend_usd=12.5, language="es")
    assert "$12.50" in r["text"]


def test_summary_omits_spend_when_none():
    r = executive_summary(done=5, total=8, total_spend_usd=None, language="es")
    assert "$" not in r["text"]


def test_summary_mentions_in_progress():
    r = executive_summary(done=2, total=8, in_progress=3, language="es")
    assert "3" in r["text"]
    assert "progreso" in r["text"].lower()


def test_summary_mentions_blocked_count_and_reasons():
    r = executive_summary(
        done=2, total=8, blocked=1,
        blocked_reasons=["• Deploy: waiting creds"], language="es",
    )
    assert "1" in r["text"]
    assert "bloque" in r["text"].lower()
    assert "waiting creds" in r["text"]


def test_summary_eta_date_preferred_over_hours():
    r = executive_summary(
        done=5, total=8, eta_date="2026-09-03", eta_hours=40.0, language="es"
    )
    assert "2026-09-03" in r["text"]
    assert "40" not in r["text"]  # date wins, hours suppressed


def test_summary_falls_back_to_eta_hours():
    r = executive_summary(done=5, total=8, eta_hours=12.5, language="es")
    assert "12.5" in r["text"]


def test_summary_english_language():
    r = executive_summary(done=5, total=8, language="en")
    assert r["language"] == "en"
    assert "delivered" in r["text"].lower()
    assert "complete" in r["text"].lower()


def test_summary_zero_total_guards_pct():
    r = executive_summary(done=0, total=0, language="es")
    assert "0%" in r["text"]  # no divide-by-zero


def test_unknown_language_falls_back_to_es():
    r = executive_summary(done=5, total=8, language="fr")
    assert r["language"] == "es"


def test_generated_from_echoes_inputs():
    r = executive_summary(done=5, total=8, in_progress=1, total_spend_usd=12.0)
    assert r["generated_from"]["done"] == 5
    assert r["generated_from"]["total"] == 8
    assert r["generated_from"]["in_progress"] == 1
    assert r["generated_from"]["total_spend_usd"] == 12.0


def test_blocked_reasons_capped_at_three():
    reasons = [f"• T{i}: reason {i}" for i in range(5)]
    r = executive_summary(done=1, total=8, blocked=5, blocked_reasons=reasons, language="es")
    assert "reason 0" in r["text"] and "reason 2" in r["text"]
    assert "reason 3" not in r["text"]  # capped at 3
