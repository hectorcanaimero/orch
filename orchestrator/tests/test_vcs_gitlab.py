"""Tests for GitLab VCS provider."""
import json
import os
from unittest.mock import MagicMock, patch

from orchestrator.vcs.gitlab import GitLabProvider


def _make_proc(returncode: int, stdout: str = "", stderr: str = "") -> MagicMock:
    m = MagicMock()
    m.returncode = returncode
    m.stdout = stdout
    m.stderr = stderr
    return m


# ---------------------------------------------------------------------------
# create_pr
# ---------------------------------------------------------------------------

@patch("orchestrator.vcs.gitlab.subprocess.run")
def test_create_pr_calls_glab_with_correct_args(mock_run):
    mock_run.return_value = _make_proc(0, stdout="https://gitlab.com/org/repo/-/merge_requests/7\n")
    provider = GitLabProvider(host="gitlab.com")
    url = provider.create_pr("t1", "My MR", "body", "orch/t1", "main")
    assert url == "https://gitlab.com/org/repo/-/merge_requests/7"
    args = mock_run.call_args[0][0]
    assert args[0] == "glab"
    assert "mr" in args and "create" in args
    assert "--source-branch" in args and "orch/t1" in args
    assert "--target-branch" in args and "main" in args


@patch("orchestrator.vcs.gitlab.subprocess.run")
def test_create_pr_sets_gitlab_host_env(mock_run):
    mock_run.return_value = _make_proc(0, stdout="https://gl.example.com/org/repo/-/merge_requests/1\n")
    provider = GitLabProvider(host="gl.example.com")
    provider.create_pr("t1", "title", "body", "head", "main")
    env_passed = mock_run.call_args[1]["env"]
    assert env_passed["GITLAB_HOST"] == "gl.example.com"


@patch("orchestrator.vcs.gitlab.subprocess.run")
def test_create_pr_returns_none_on_failure(mock_run):
    mock_run.return_value = _make_proc(1, stderr="error")
    assert GitLabProvider().create_pr("t1", "t", "b", "h", "main") is None


# ---------------------------------------------------------------------------
# get_ci_status
# ---------------------------------------------------------------------------

@patch("orchestrator.vcs.gitlab.subprocess.run")
def test_get_ci_status_success(mock_run):
    mr_data = {"head_pipeline": {"id": 1, "status": "success"}}
    mock_run.return_value = _make_proc(0, stdout=json.dumps(mr_data))
    assert GitLabProvider().get_ci_status("https://gitlab.com/org/repo/-/merge_requests/7") == "success"


@patch("orchestrator.vcs.gitlab.subprocess.run")
def test_get_ci_status_failure(mock_run):
    mr_data = {"head_pipeline": {"id": 1, "status": "failed"}}
    mock_run.return_value = _make_proc(0, stdout=json.dumps(mr_data))
    assert GitLabProvider().get_ci_status("https://gitlab.com/org/repo/-/merge_requests/7") == "failure"


@patch("orchestrator.vcs.gitlab.subprocess.run")
def test_get_ci_status_pending_when_running(mock_run):
    mr_data = {"head_pipeline": {"id": 1, "status": "running"}}
    mock_run.return_value = _make_proc(0, stdout=json.dumps(mr_data))
    assert GitLabProvider().get_ci_status("https://gitlab.com/org/repo/-/merge_requests/7") == "pending"


@patch("orchestrator.vcs.gitlab.subprocess.run")
def test_get_ci_status_pending_on_glab_error(mock_run):
    mock_run.return_value = _make_proc(1)
    assert GitLabProvider().get_ci_status("https://gitlab.com/org/repo/-/merge_requests/7") == "pending"


# ---------------------------------------------------------------------------
# _iid_from_url
# ---------------------------------------------------------------------------

def test_iid_from_url_extracts_iid():
    url = "https://gitlab.com/org/repo/-/merge_requests/42"
    assert GitLabProvider._iid_from_url(url) == "42"


def test_iid_from_url_returns_empty_on_invalid():
    assert GitLabProvider._iid_from_url("https://github.com/org/repo/pull/1") == ""


# ---------------------------------------------------------------------------
# merge_pr (Sprint G-1)
# ---------------------------------------------------------------------------

@patch("orchestrator.vcs.gitlab.subprocess.run")
def test_merge_pr_calls_glab_with_squash(mock_run):
    mock_run.return_value = _make_proc(0, stdout="Merged")
    provider = GitLabProvider(host="gitlab.com")
    assert provider.merge_pr("https://gitlab.com/org/repo/-/merge_requests/7") is True
    args = mock_run.call_args[0][0]
    assert args[0] == "glab"
    assert "mr" in args and "merge" in args and "7" in args
    assert "--squash" in args


@patch("orchestrator.vcs.gitlab.subprocess.run")
def test_merge_pr_returns_false_on_failure(mock_run):
    mock_run.return_value = _make_proc(1, stderr="error")
    assert GitLabProvider().merge_pr("https://gitlab.com/org/repo/-/merge_requests/7") is False


def test_merge_pr_returns_false_on_invalid_url():
    assert GitLabProvider().merge_pr("https://github.com/org/repo/pull/1") is False
