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


def test_upgrade_uv_path_calls_uv_tool_install(capsys):
    """A uv-installed tool env (`.../uv/tools/orch/bin/python`) must use
    `uv tool install --force <wheel>`, not pip."""
    fake_uv_exe = "/home/user/.local/share/uv/tools/orch/bin/python"
    with (
        _patch_urlopen(_NEWER),
        patch("sys.executable", fake_uv_exe),
        patch("shutil.which", return_value="/usr/local/bin/uv"),
        patch("subprocess.run", return_value=MagicMock(returncode=0)) as mock_run,
    ):
        rc = _run_upgrade_subcommand([])

    assert rc == 0
    cmd = mock_run.call_args[0][0]
    assert cmd[0].endswith("/uv")
    assert cmd[1:4] == ["tool", "install", "--force"]
    assert _WHEEL_URL in cmd


def test_check_flag_no_upgrade_reports_already_latest(capsys):
    """Regression: `orch upgrade --check` on a current install must NOT
    print 'Install with' — that's misleading."""
    with _patch_urlopen(CURRENT):
        rc = _run_upgrade_subcommand(["--check"])
    assert rc == 0
    out = capsys.readouterr().out
    assert "already the latest" in out
    assert "Install with" not in out


# ---- stdlib version fallback ----------------------------------------------


def test_version_gt_uses_packaging_when_available():
    from orchestrator.orch import _version_gt
    assert _version_gt("1.0.0", "0.9.9") is True
    assert _version_gt("0.9.0", "0.9.1") is False
    assert _version_gt("1.0.0", "1.0.0") is False


def test_version_gt_falls_back_to_stdlib_without_packaging():
    """If `packaging` isn't importable (bare uv tool env), the naive
    tuple-of-ints parser must still get common cases right."""
    from orchestrator.orch import _version_gt

    real_import = __import__

    def _no_packaging(name, *args, **kwargs):
        if name == "packaging.version" or name == "packaging":
            raise ImportError(name)
        return real_import(name, *args, **kwargs)

    with patch("builtins.__import__", side_effect=_no_packaging):
        assert _version_gt("1.0.0", "0.9.9") is True
        assert _version_gt("0.9.1", "0.9.1") is False
        assert _version_gt("0.9.0", "0.9.1") is False
        assert _version_gt("1.2.3", "1.2.2") is True


# ---- install-method detection ---------------------------------------------


def test_detect_install_method_pipx():
    from orchestrator.orch import _detect_install_method
    with patch("sys.executable", "/opt/pipx/venvs/orch/bin/python"):
        assert _detect_install_method() == "pipx"


def test_detect_install_method_uv():
    from orchestrator.orch import _detect_install_method
    with patch("sys.executable", "/home/user/.local/share/uv/tools/orch/bin/python"):
        assert _detect_install_method() == "uv"


def test_detect_install_method_fallback_pip():
    from orchestrator.orch import _detect_install_method
    with patch("sys.executable", "/usr/bin/python3"):
        assert _detect_install_method() == "pip"
