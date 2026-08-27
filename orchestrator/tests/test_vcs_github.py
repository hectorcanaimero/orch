"""Tests for GitHub VCS provider."""
import json
from unittest.mock import MagicMock, patch

import pytest

from orchestrator.vcs import get_vcs_provider
from orchestrator.vcs.github import GitHubProvider
from orchestrator.vcs.protocol import VcsProvider


def test_get_vcs_provider_returns_github_by_default():
    cfg = {"vcs": {"provider": "github"}}
    provider = get_vcs_provider(cfg)
    assert hasattr(provider, "create_pr")
    assert hasattr(provider, "get_ci_status")
    assert hasattr(provider, "get_ci_logs")


def test_get_vcs_provider_returns_gitlab_when_configured():
    cfg = {"vcs": {"provider": "gitlab", "host": "gitlab.example.com"}}
    provider = get_vcs_provider(cfg)
    assert hasattr(provider, "create_pr")
    assert hasattr(provider, "get_ci_status")
    assert hasattr(provider, "get_ci_logs")


# ---------------------------------------------------------------------------
# GitHubProvider — create_pr
# ---------------------------------------------------------------------------

def _make_proc(returncode: int, stdout: str = "", stderr: str = "") -> MagicMock:
    m = MagicMock()
    m.returncode = returncode
    m.stdout = stdout
    m.stderr = stderr
    return m


@patch("orchestrator.vcs.github.subprocess.run")
def test_create_pr_calls_gh_with_correct_args(mock_run):
    mock_run.return_value = _make_proc(0, stdout="https://github.com/org/repo/pull/42\n")
    provider = GitHubProvider()
    url = provider.create_pr(
        task_id="t1",
        title="My PR",
        body="body text",
        head="orch/t1",
        base="main",
    )
    assert url == "https://github.com/org/repo/pull/42"
    args = mock_run.call_args[0][0]
    assert args[0] == "gh"
    assert "pr" in args and "create" in args
    assert "--title" in args and "My PR" in args
    assert "--head" in args and "orch/t1" in args
    assert "--base" in args and "main" in args


@patch("orchestrator.vcs.github.subprocess.run")
def test_create_pr_returns_none_on_failure(mock_run):
    mock_run.return_value = _make_proc(1, stderr="error")
    provider = GitHubProvider()
    assert provider.create_pr("t1", "title", "body", "head", "main") is None


# ---------------------------------------------------------------------------
# GitHubProvider — get_ci_status
# ---------------------------------------------------------------------------

@patch("orchestrator.vcs.github.subprocess.run")
def test_get_ci_status_success(mock_run):
    checks = [{"state": "completed", "conclusion": "success"}]
    mock_run.return_value = _make_proc(0, stdout=json.dumps(checks))
    assert GitHubProvider().get_ci_status("https://github.com/org/repo/pull/1") == "success"


@patch("orchestrator.vcs.github.subprocess.run")
def test_get_ci_status_failure(mock_run):
    checks = [{"state": "completed", "conclusion": "failure"}]
    mock_run.return_value = _make_proc(0, stdout=json.dumps(checks))
    assert GitHubProvider().get_ci_status("https://github.com/org/repo/pull/1") == "failure"


@patch("orchestrator.vcs.github.subprocess.run")
def test_get_ci_status_pending_when_in_progress(mock_run):
    checks = [{"state": "in_progress", "conclusion": ""}]
    mock_run.return_value = _make_proc(0, stdout=json.dumps(checks))
    assert GitHubProvider().get_ci_status("https://github.com/org/repo/pull/1") == "pending"


@patch("orchestrator.vcs.github.subprocess.run")
def test_get_ci_status_pending_on_gh_error(mock_run):
    mock_run.return_value = _make_proc(1)
    assert GitHubProvider().get_ci_status("https://github.com/org/repo/pull/1") == "pending"


@patch("orchestrator.vcs.github.subprocess.run")
def test_get_ci_status_pending_on_empty_checks(mock_run):
    mock_run.return_value = _make_proc(0, stdout="[]")
    assert GitHubProvider().get_ci_status("https://github.com/org/repo/pull/1") == "pending"


# ---------------------------------------------------------------------------
# GitHubProvider — get_ci_logs
# ---------------------------------------------------------------------------

@patch("orchestrator.vcs.github.subprocess.run")
def test_get_ci_logs_returns_failed_run_output(mock_run):
    pr_view = {
        "statusCheckRollup": [
            {"conclusion": "failure", "detailsUrl": "https://github.com/org/repo/actions/runs/99999/jobs/1"}
        ]
    }
    log_output = "ERROR: test failed\n" * 10

    def side_effect(cmd, **kwargs):
        if len(cmd) > 1 and cmd[1] == "pr":
            return _make_proc(0, stdout=json.dumps(pr_view))
        return _make_proc(0, stdout=log_output)

    mock_run.side_effect = side_effect
    logs = GitHubProvider().get_ci_logs("https://github.com/org/repo/pull/1")
    assert "ERROR" in logs


@patch("orchestrator.vcs.github.subprocess.run")
def test_get_ci_logs_truncates_to_8000_chars(mock_run):
    pr_view = {
        "statusCheckRollup": [
            {"conclusion": "failure", "detailsUrl": "https://github.com/org/repo/actions/runs/1/jobs/1"}
        ]
    }
    long_log = "x" * 20_000

    def side_effect(cmd, **kwargs):
        if "view" in cmd and "runs" not in cmd:
            return _make_proc(0, stdout=json.dumps(pr_view))
        return _make_proc(0, stdout=long_log)

    mock_run.side_effect = side_effect
    logs = GitHubProvider().get_ci_logs("https://github.com/org/repo/pull/1")
    assert len(logs) <= 8000


@patch("orchestrator.vcs.github.subprocess.run")
def test_get_ci_logs_returns_empty_on_no_failures(mock_run):
    pr_view = {"statusCheckRollup": [{"conclusion": "success", "detailsUrl": ""}]}
    mock_run.return_value = _make_proc(0, stdout=json.dumps(pr_view))
    logs = GitHubProvider().get_ci_logs("https://github.com/org/repo/pull/1")
    assert logs == ""


# ---------------------------------------------------------------------------
# GitHubProvider — merge_pr (Sprint G-1)
# ---------------------------------------------------------------------------

@patch("orchestrator.vcs.github.subprocess.run")
def test_merge_pr_calls_gh_with_squash_auto(mock_run):
    mock_run.return_value = _make_proc(0, stdout="Merged")
    assert GitHubProvider().merge_pr("https://github.com/org/repo/pull/1") is True
    args = mock_run.call_args[0][0]
    assert args[0] == "gh"
    assert "pr" in args and "merge" in args
    assert "--squash" in args and "--auto" in args
    assert "https://github.com/org/repo/pull/1" in args


@patch("orchestrator.vcs.github.subprocess.run")
def test_merge_pr_returns_false_on_failure(mock_run):
    mock_run.return_value = _make_proc(1, stderr="not mergeable")
    assert GitHubProvider().merge_pr("https://github.com/org/repo/pull/1") is False


def test_vcs_provider_protocol_declares_merge_pr():
    assert hasattr(VcsProvider, "merge_pr")
