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


def test_unknown_language_falls_back_to_es():
    r = executive_summary(_health(), total_spend_usd=None, language="fr")
    assert r["language"] == "es"
