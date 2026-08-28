"""Unit tests for orchestrator.notifications.Notifier (Sprint G-6)."""
from __future__ import annotations

import json
from unittest.mock import MagicMock, patch

from orchestrator.notifications import Notifier


# ---- construction / introspection ------------------------------------------


def test_from_config_returns_disabled_when_no_notifications_section():
    n = Notifier.from_config({})
    assert n.enabled is False
    assert n.slack_webhook == ""
    assert n.discord_webhook == ""


def test_from_config_reads_slack_and_discord():
    cfg = {"notifications": {
        "slack_webhook": "https://hooks.slack.com/A",
        "discord_webhook": "https://discord.com/api/webhooks/B",
        "timeout_s": 12,
    }}
    n = Notifier.from_config(cfg)
    assert n.enabled is True
    assert n.slack_webhook == "https://hooks.slack.com/A"
    assert n.discord_webhook == "https://discord.com/api/webhooks/B"
    assert n.timeout_s == 12.0


def test_from_config_handles_none_notifications_section():
    """A `notifications: null` yaml block must not crash."""
    n = Notifier.from_config({"notifications": None})
    assert n.enabled is False


def test_enabled_is_true_when_only_discord_set():
    n = Notifier(discord_webhook="https://discord.com/api/webhooks/X")
    assert n.enabled is True


# ---- no-op behaviour when disabled -----------------------------------------


def test_notify_blocked_is_noop_when_no_webhook_configured():
    n = Notifier()  # both empty
    with patch("urllib.request.urlopen") as mock_open:
        n.notify_blocked("F1.T1", reason="something")
    mock_open.assert_not_called()


def test_notify_ci_blocked_is_noop_when_no_webhook_configured():
    n = Notifier()
    with patch("urllib.request.urlopen") as mock_open:
        n.notify_ci_blocked("F1.T1", pr_url="http://x", attempts=3)
    mock_open.assert_not_called()


# ---- POST shape / payload correctness --------------------------------------


def _mock_urlopen_ok(status: int = 200):
    """Context-manager-friendly urlopen mock returning an OK response."""
    resp = MagicMock()
    resp.status = status
    resp.__enter__ = MagicMock(return_value=resp)
    resp.__exit__ = MagicMock(return_value=False)
    return MagicMock(return_value=resp)


def test_notify_blocked_posts_to_slack_with_text_payload():
    n = Notifier(slack_webhook="https://hooks.slack.com/A")
    with patch("urllib.request.urlopen", _mock_urlopen_ok()) as mock_open:
        n.notify_blocked("F1.T3", reason="spec ambiguous")
    assert mock_open.call_count == 1
    req = mock_open.call_args[0][0]
    body = json.loads(req.data.decode("utf-8"))
    # Slack: {"text": "..."}
    assert "text" in body
    assert "F1.T3" in body["text"]
    assert "spec ambiguous" in body["text"]


def test_notify_ci_blocked_posts_to_discord_with_content_payload():
    n = Notifier(discord_webhook="https://discord.com/api/webhooks/B")
    with patch("urllib.request.urlopen", _mock_urlopen_ok(204)) as mock_open:
        n.notify_ci_blocked("F1.T3", pr_url="https://gh/pull/9", attempts=3)
    req = mock_open.call_args[0][0]
    body = json.loads(req.data.decode("utf-8"))
    # Discord: {"content": "..."}
    assert "content" in body
    assert "F1.T3" in body["content"]
    assert "3" in body["content"]  # attempts count in the message
    assert "https://gh/pull/9" in body["content"]


def test_notify_posts_to_both_channels_when_both_configured():
    n = Notifier(
        slack_webhook="https://hooks.slack.com/A",
        discord_webhook="https://discord.com/api/webhooks/B",
    )
    with patch("urllib.request.urlopen", _mock_urlopen_ok()) as mock_open:
        n.notify_blocked("F1.T1", reason="r")
    assert mock_open.call_count == 2


def test_notify_blocked_snips_multi_line_reason():
    """A multi-line reason must render as a single line (webhook payload)."""
    n = Notifier(slack_webhook="https://hooks.slack.com/A")
    with patch("urllib.request.urlopen", _mock_urlopen_ok()) as mock_open:
        n.notify_blocked("F1.T1", reason="line one\nline two\nline three")
    req = mock_open.call_args[0][0]
    body = json.loads(req.data.decode("utf-8"))
    assert "line one" in body["text"]
    assert "\n" not in body["text"]  # snipped to first line


# ---- silent-fail contract ---------------------------------------------------


def test_notify_swallows_urlopen_errors():
    """A broken webhook must NEVER surface an exception."""
    import urllib.error
    n = Notifier(slack_webhook="https://hooks.slack.com/A")

    def boom(*a, **kw):
        raise urllib.error.URLError("connection refused")

    with patch("urllib.request.urlopen", side_effect=boom):
        # Must not raise.
        n.notify_blocked("F1.T1", reason="r")
        n.notify_ci_blocked("F1.T1", pr_url="", attempts=1)


def test_notify_test_returns_false_when_all_channels_fail():
    """notify_test surfaces the failure so `orch notify test` can exit 1."""
    import urllib.error
    n = Notifier(slack_webhook="https://hooks.slack.com/A")

    with patch(
        "urllib.request.urlopen",
        side_effect=urllib.error.URLError("nope"),
    ):
        assert n.notify_test("hi") is False


def test_notify_test_returns_true_when_at_least_one_channel_succeeds():
    n = Notifier(slack_webhook="https://hooks.slack.com/A")
    with patch("urllib.request.urlopen", _mock_urlopen_ok()):
        assert n.notify_test("hi") is True


# ---- digest_text ------------------------------------------------------------


def test_digest_text_uses_summary_verbatim():
    n = Notifier()
    text = n.digest_text({"text": "Project 50% complete."}, milestones=[])
    assert text.strip() == "Project 50% complete."


def test_digest_text_appends_milestone_table():
    n = Notifier()
    milestones = [
        {"name": "Auth", "progress": {"done": 5, "total": 7},
         "eta": {"eta_date": "2026-09-03"}},
        {"name": "API", "progress": {"done": 2, "total": 8}, "eta": None},
    ]
    text = n.digest_text({"text": "Project 40% complete."}, milestones=milestones)
    assert "Milestones:" in text
    assert "Auth: 5/7 — ETA 2026-09-03" in text
    assert "API: 2/8 — ETA —" in text


def test_digest_text_omits_milestone_section_when_empty():
    n = Notifier()
    text = n.digest_text({"text": "x"}, milestones=[])
    assert "Milestones:" not in text


def test_digest_text_uses_id_fallback_when_name_absent():
    n = Notifier()
    milestones = [{"id": "m1", "progress": {"done": 0, "total": 1}, "eta": None}]
    text = n.digest_text({"text": "x"}, milestones=milestones)
    assert "m1: 0/1" in text
