"""Tests for GitHub VCS provider."""
import json
from pathlib import Path
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

# Bug 27 of the Go port. These tests used to feed `{"state": "completed",
# "conclusion": "success"}` — a field `gh pr checks` does not have and a state
# value it never emits — so they were green for months over a command that
# could not run (exit 1, "Unknown JSON field"). The fixtures below are real
# gh 2.100.0 output, captured and never edited; see fixtures/gh/2.100.0/README.md.

_GH_FIXTURES = Path(__file__).parent / "fixtures" / "gh" / "2.100.0"


def _gh_fixture(name: str) -> str:
    return (_GH_FIXTURES / name).read_text(encoding="utf-8")


@patch("orchestrator.vcs.github.subprocess.run")
def test_get_ci_status_asks_gh_for_fields_it_has(mock_run):
    mock_run.return_value = _make_proc(0, stdout=_gh_fixture("pr-checks-all-pass.json"))
    GitHubProvider().get_ci_status("https://github.com/org/repo/pull/1")
    args = mock_run.call_args[0][0]
    assert "--json" in args
    fields = args[args.index("--json") + 1].split(",")
    assert "conclusion" not in fields, "gh pr checks has no `conclusion` field (exit 1)"
    assert "state" in fields


@patch("orchestrator.vcs.github.subprocess.run")
def test_get_ci_status_success_on_real_all_pass_output(mock_run):
    mock_run.return_value = _make_proc(0, stdout=_gh_fixture("pr-checks-all-pass.json"))
    assert GitHubProvider().get_ci_status("https://github.com/org/repo/pull/1") == "success"


@patch("orchestrator.vcs.github.subprocess.run")
def test_get_ci_status_failure_on_real_output_with_one_failed_check(mock_run):
    mock_run.return_value = _make_proc(0, stdout=_gh_fixture("pr-checks-one-failure.json"))
    assert GitHubProvider().get_ci_status("https://github.com/org/repo/pull/1") == "failure"


@patch("orchestrator.vcs.github.subprocess.run")
def test_get_ci_status_neutral_check_does_not_block_success(mock_run):
    mock_run.return_value = _make_proc(0, stdout=_gh_fixture("pr-checks-with-neutral.json"))
    assert GitHubProvider().get_ci_status("https://github.com/org/repo/pull/1") == "success"


@patch("orchestrator.vcs.github.subprocess.run")
def test_get_ci_status_pending_when_a_check_is_still_running(mock_run):
    # Derived from the real shape (no PR with CI in flight was available to
    # capture): one row of pr-checks-all-pass.json with gh's own IN_PROGRESS
    # state, which it emits uppercase like every other.
    rows = json.loads(_gh_fixture("pr-checks-all-pass.json"))
    rows[0]["state"], rows[0]["bucket"] = "IN_PROGRESS", "pending"
    mock_run.return_value = _make_proc(0, stdout=json.dumps(rows))
    assert GitHubProvider().get_ci_status("https://github.com/org/repo/pull/1") == "pending"


@patch("orchestrator.vcs.github.subprocess.run")
def test_get_ci_status_pending_and_loud_when_gh_rejects_the_call(mock_run, capsys):
    mock_run.return_value = _make_proc(1, stderr=_gh_fixture("pr-checks-conclusion-rejected.txt"))
    assert GitHubProvider().get_ci_status("https://github.com/org/repo/pull/1") == "pending"
    err = capsys.readouterr().err
    assert "gh pr checks failed" in err and "Unknown JSON field" in err, (
        "a gh that cannot run must not look like a CI that is still waiting"
    )


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
