"""Tests for `orch findings review` + `search_github_issues_for_duplicate`.

Sprint E-1 / #17 — commit 3 scope. All GitHub calls are mocked at
`subprocess.run` (the `gh api` shell-out surface).
"""

from __future__ import annotations

import io
import json
import subprocess
import sys
from pathlib import Path
from unittest.mock import patch

import pytest

from orchestrator import findings as f_mod
from orchestrator.findings import search_github_issues_for_duplicate
from orchestrator.state import FileBackend
from orchestrator.state.interface import StateBackend


# ---- helpers ------------------------------------------------------------


def _mk_completed(stdout: str = "", stderr: str = "", rc: int = 0) -> subprocess.CompletedProcess:
    return subprocess.CompletedProcess(
        args=["gh"], returncode=rc, stdout=stdout, stderr=stderr
    )


@pytest.fixture
def backend(tmp_path: Path) -> StateBackend:
    state_dir = tmp_path / "state"
    state_dir.mkdir()
    return FileBackend(state_dir=state_dir, project_id="rt", project_root=tmp_path)


def _bootstrap_project(tmp_path: Path) -> None:
    (tmp_path / "orchestrator").mkdir(exist_ok=True)
    (tmp_path / "orchestrator" / "config.yaml").write_text(
        "concurrency: {global_max: 1}\n"
        "state: {backend: file}\n"
        "findings: {publish_repo: 'ownr/reporepo'}\n",
        encoding="utf-8",
    )
    (tmp_path / "tasks.json").write_text('{"tasks": []}', encoding="utf-8")
    (tmp_path / "scripts").mkdir(exist_ok=True)
    for name in ("task-start.sh", "task-finish.sh", "task-block.sh"):
        (tmp_path / "scripts" / name).write_text("#!/bin/sh\nexit 0\n")
        (tmp_path / "scripts" / name).chmod(0o755)


def _run_cli(argv: list[str], tmp_path: Path) -> tuple[int, str, str]:
    from orchestrator.orch import main

    stdout, stderr = sys.stdout, sys.stderr
    sio_out, sio_err = io.StringIO(), io.StringIO()
    sys.stdout, sys.stderr = sio_out, sio_err
    try:
        rc = main(["findings", *argv, "--project-root", str(tmp_path)])
    except SystemExit as exc:
        rc = int(exc.code) if isinstance(exc.code, int) else 2
    finally:
        sys.stdout, sys.stderr = stdout, stderr
    return rc, sio_out.getvalue(), sio_err.getvalue()


# ---- search_github_issues_for_duplicate --------------------------------


def test_search_returns_ranked_matches() -> None:
    payload = {
        "items": [
            {
                "number": 11,
                "title": "Dashboard crashes on refresh with sqlite backend",
                "html_url": "https://github.com/owner/repo/issues/11",
            },
            {
                "number": 22,
                "title": "Add a color theme picker",
                "html_url": "https://github.com/owner/repo/issues/22",
            },
        ]
    }

    fake = _mk_completed(stdout=json.dumps(payload))

    def runner(cmd, **_kwargs):
        assert cmd[:2] == ["gh", "api"]
        assert "search/issues" in cmd[2]
        return fake

    matches = search_github_issues_for_duplicate(
        "Dashboard crashes on refresh",
        "owner/repo",
        runner=runner,
    )
    # Higher-overlap first.
    assert matches[0]["number"] == 11
    assert matches[0]["overlap"] > matches[1]["overlap"]
    assert matches[0]["html_url"].endswith("/11")


def test_search_returns_empty_on_gh_failure() -> None:
    def runner(cmd, **_kwargs):
        return _mk_completed(rc=1, stderr="auth error")

    assert search_github_issues_for_duplicate("anything", "o/r", runner=runner) == []


def test_search_returns_empty_when_gh_missing() -> None:
    def runner(cmd, **_kwargs):
        raise FileNotFoundError("gh not installed")

    assert search_github_issues_for_duplicate("x y z", "o/r", runner=runner) == []


def test_search_ignores_malformed_json() -> None:
    def runner(cmd, **_kwargs):
        return _mk_completed(stdout="{not json")

    assert search_github_issues_for_duplicate("anything happens", "o/r", runner=runner) == []


def test_search_short_summary_yields_no_query() -> None:
    """A summary with only stopwords produces no keywords → no gh call."""
    called = {"n": 0}

    def runner(cmd, **_kwargs):
        called["n"] += 1
        return _mk_completed(stdout="{}")

    assert search_github_issues_for_duplicate("a the on", "o/r", runner=runner) == []
    assert called["n"] == 0


# ---- CLI review ---------------------------------------------------------


def test_cli_review_renders_finding_and_matches(tmp_path: Path) -> None:
    _bootstrap_project(tmp_path)
    # Capture a finding first via CLI so we have a real id.
    _run_cli(
        [
            "capture", "--type", "bug", "--about", "orch",
            "--summary", "Runtime error when starting the dashboard",
            "--evidence", "file: orchestrator/dashboard/main.py:42\nstack: ...",
        ],
        tmp_path,
    )
    # Get the id via the JSON list.
    rc, out, _err = _run_cli(["list", "--json"], tmp_path)
    fid = json.loads(out.strip())[0]["id"]

    fake_response = {
        "items": [
            {
                "number": 99,
                "title": "Runtime error when starting the dashboard",
                "html_url": "https://github.com/ownr/reporepo/issues/99",
            }
        ]
    }

    def runner(cmd, **_kwargs):
        return _mk_completed(stdout=json.dumps(fake_response))

    with patch("orchestrator.findings.subprocess.run", side_effect=runner):
        rc, out, err = _run_cli(["review", fid], tmp_path)
    assert rc == 0, err
    assert "Runtime error" in out
    assert "#99" in out
    assert "overlap=" in out


def test_cli_review_json_shape(tmp_path: Path) -> None:
    _bootstrap_project(tmp_path)
    _run_cli(
        ["capture", "--type", "feature", "--about", "orch",
         "--summary", "Sprint E-1 dogfooding loop lands"],
        tmp_path,
    )
    rc, out, _err = _run_cli(["list", "--json"], tmp_path)
    fid = json.loads(out.strip())[0]["id"]

    with patch("orchestrator.findings.subprocess.run",
               return_value=_mk_completed(stdout=json.dumps({"items": []}))):
        rc, out, err = _run_cli(["review", fid, "--json"], tmp_path)
    assert rc == 0, err
    payload = json.loads(out.strip())
    assert "finding" in payload
    assert "matches" in payload
    assert payload["repo"] == "ownr/reporepo"
    assert payload["finding"]["id"] == fid


def test_cli_review_not_found(tmp_path: Path) -> None:
    _bootstrap_project(tmp_path)
    rc, _out, err = _run_cli(["review", "no-such-id"], tmp_path)
    assert rc == 2
    assert "not found" in err.lower()


def test_cli_review_accepts_id_prefix(tmp_path: Path) -> None:
    _bootstrap_project(tmp_path)
    _run_cli(
        ["capture", "--type", "bug", "--about", "orch",
         "--summary", "unique prefix test target"],
        tmp_path,
    )
    rc, out, _err = _run_cli(["list", "--json"], tmp_path)
    fid = json.loads(out.strip())[0]["id"]
    prefix = fid[:6]

    with patch("orchestrator.findings.subprocess.run",
               return_value=_mk_completed(stdout=json.dumps({"items": []}))):
        rc, out, err = _run_cli(["review", prefix], tmp_path)
    assert rc == 0, err
    assert fid in out
