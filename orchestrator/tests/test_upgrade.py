"""Tests for `orch upgrade` subcommand."""

from __future__ import annotations

import importlib.metadata
import json
import subprocess
import sys
from unittest.mock import MagicMock, patch

import pytest

from orchestrator.orch import _run_upgrade_subcommand


CURRENT = importlib.metadata.version("orchestrator")
_OLDER = "0.0.1"
_NEWER = "99.0.0"

_WHEEL_URL = "https://github.com/hectorcanaimero/orch/releases/download/v99.0.0/orchestrator-99.0.0-py3-none-any.whl"


def _mock_github_response(version: str, wheel_url: str = _WHEEL_URL):
    payload = json.dumps({
        "tag_name": f"v{version}",
        "assets": [{"name": f"orchestrator-{version}-py3-none-any.whl", "browser_download_url": wheel_url}],
    }).encode()
    mock_resp = MagicMock()
    mock_resp.read.return_value = payload
    mock_resp.__enter__ = lambda s: s
    mock_resp.__exit__ = MagicMock(return_value=False)
    return mock_resp


def _patch_urlopen(version: str):
    return patch("urllib.request.urlopen", return_value=_mock_github_response(version))


def test_already_latest_exits_0(capsys):
    with _patch_urlopen(CURRENT):
        rc = _run_upgrade_subcommand([])
    assert rc == 0
    out = capsys.readouterr().out
    assert "already the latest" in out


def test_check_flag_reports_new_version_no_install(capsys):
    with _patch_urlopen(_NEWER):
        rc = _run_upgrade_subcommand(["--check"])
    assert rc == 0
    out = capsys.readouterr().out
    assert _NEWER in out
    assert "orch upgrade" in out


def test_upgrade_pipx_path_calls_pipx(capsys):
    fake_pipx = "/opt/pipx/venvs/orch/bin/python"
    fake_wheel = _WHEEL_URL
    with (
        _patch_urlopen(_NEWER),
        patch("sys.executable", fake_pipx),
        patch("shutil.which", return_value="/usr/local/bin/pipx"),
        patch("subprocess.run", return_value=MagicMock(returncode=0)) as mock_run,
    ):
        rc = _run_upgrade_subcommand([])

    assert rc == 0
    cmd = mock_run.call_args[0][0]
    assert "pipx" in cmd[0]
    assert "--force" in cmd
    assert fake_wheel in cmd


def test_upgrade_pip_path_calls_pip(capsys):
    fake_pip = "/usr/local/bin/pip3"
    with (
        _patch_urlopen(_NEWER),
        patch("sys.executable", "/usr/local/bin/python3"),  # no "pipx" in path
        patch("shutil.which", return_value=fake_pip),
        patch("subprocess.run", return_value=MagicMock(returncode=0)) as mock_run,
    ):
        rc = _run_upgrade_subcommand([])

    assert rc == 0
    cmd = mock_run.call_args[0][0]
    assert "pip" in cmd[0]
    assert "--force-reinstall" in cmd


def test_github_unreachable_returns_1(capsys):
    with patch("urllib.request.urlopen", side_effect=OSError("timeout")):
        rc = _run_upgrade_subcommand([])
    assert rc == 1
    assert "GitHub" in capsys.readouterr().err
