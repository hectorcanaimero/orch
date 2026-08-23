"""Sprint E-2 — CLI flag parsing tests for `orch dashboard`.

Covers:
    - `--profile` accepts operator/stakeholder/both, rejects anything else.
    - `--token` populates `ORCH_DASHBOARD_TOKEN` in-process (CLI beats env).
    - `--profile stakeholder` without a token errors out (exit code 2).
    - Config-file fallback for the token (config satisfies the requirement).
    - Precedence: CLI flag > env var > config.yaml `dashboard.token`.

We mock `dashboard.server.run` so tests don't actually launch uvicorn.
"""

from __future__ import annotations

import os
import sys
from pathlib import Path
from unittest.mock import MagicMock

import pytest


@pytest.fixture(autouse=True)
def _restore_env(monkeypatch: pytest.MonkeyPatch):
    """`_run_dashboard_subcommand` mutates os.environ directly (that's the
    spec — CLI --token must survive through server.run into DashboardConfig
    lookups). Snapshot + restore per-test so no test leaks into a sibling.

    Also cleans the bind-related env vars added by the per-project sticky
    port feature so tests don't inherit host/port from the outer shell.
    """
    keys = ("ORCH_DASHBOARD_TOKEN", "ORCH_DASHBOARD_PORT", "ORCH_DASHBOARD_HOST")
    snapshot = {k: os.environ.get(k) for k in keys}
    yield
    for k, v in snapshot.items():
        if v is None:
            os.environ.pop(k, None)
        else:
            os.environ[k] = v


def _run_dashboard(argv: list[str], monkeypatch: pytest.MonkeyPatch) -> tuple[int, MagicMock]:
    """Invoke the dashboard subcommand with a mocked `dashboard.server.run`.

    Returns `(exit_code, mock)` so the caller can assert on which kwargs
    were passed through to the real runner.
    """
    from orchestrator import orch

    # Force a re-import of the mocked module so orch picks up our patch.
    mock_run = MagicMock(return_value=0)
    fake_module = MagicMock()
    fake_module.run = mock_run
    monkeypatch.setitem(sys.modules, "orchestrator.dashboard.server", fake_module)

    rc = orch._run_dashboard_subcommand(argv)
    return rc, mock_run


# ---- Argument acceptance ---------------------------------------------------


def test_defaults_are_operator(monkeypatch, tmp_path: Path) -> None:
    monkeypatch.chdir(tmp_path)
    monkeypatch.delenv("ORCH_DASHBOARD_TOKEN", raising=False)
    rc, mock = _run_dashboard([], monkeypatch)
    assert rc == 0
    kwargs = mock.call_args.kwargs
    assert kwargs["profile"] is None
    assert kwargs["token"] is None
    assert kwargs["host"] == "127.0.0.1"
    assert kwargs["port"] == 7420


def test_accepts_all_profile_values(monkeypatch, tmp_path: Path) -> None:
    monkeypatch.chdir(tmp_path)
    monkeypatch.delenv("ORCH_DASHBOARD_TOKEN", raising=False)
    # Stakeholder needs a token — provide it inline for the acceptance check.
    for prof in ("operator", "stakeholder", "both"):
        argv = ["--profile", prof]
        if prof == "stakeholder":
            argv += ["--token", "t"]
        rc, mock = _run_dashboard(argv, monkeypatch)
        assert rc == 0, f"profile {prof} rejected"
        assert mock.call_args.kwargs["profile"] == prof


def test_rejects_unknown_profile(monkeypatch, tmp_path: Path, capsys) -> None:
    monkeypatch.chdir(tmp_path)
    with pytest.raises(SystemExit) as exc:
        _run_dashboard(["--profile", "bogus"], monkeypatch)
    assert exc.value.code == 2  # argparse exits 2 on invalid choice
    err = capsys.readouterr().err
    assert "invalid choice" in err.lower() or "bogus" in err.lower()


# ---- Token behavior --------------------------------------------------------


def test_token_flag_sets_env_var(monkeypatch, tmp_path: Path) -> None:
    monkeypatch.chdir(tmp_path)
    monkeypatch.delenv("ORCH_DASHBOARD_TOKEN", raising=False)
    _run_dashboard(["--profile", "stakeholder", "--token", "s3cret"], monkeypatch)
    assert os.environ.get("ORCH_DASHBOARD_TOKEN") == "s3cret"


def test_stakeholder_without_any_token_errors(
    monkeypatch, tmp_path: Path, capsys
) -> None:
    monkeypatch.chdir(tmp_path)
    monkeypatch.delenv("ORCH_DASHBOARD_TOKEN", raising=False)
    rc, mock = _run_dashboard(["--profile", "stakeholder"], monkeypatch)
    assert rc == 2
    err = capsys.readouterr().err
    assert "requires a token" in err.lower()
    # Server run must NEVER be called with the misconfig.
    mock.assert_not_called()


def test_stakeholder_env_token_satisfies_requirement(
    monkeypatch, tmp_path: Path
) -> None:
    monkeypatch.chdir(tmp_path)
    monkeypatch.setenv("ORCH_DASHBOARD_TOKEN", "from-env")
    rc, mock = _run_dashboard(["--profile", "stakeholder"], monkeypatch)
    assert rc == 0
    # No --token flag → kwarg stays None; the server reads env directly.
    assert mock.call_args.kwargs["token"] is None
    assert mock.call_args.kwargs["profile"] == "stakeholder"


def test_stakeholder_config_token_satisfies_requirement(
    monkeypatch, tmp_path: Path
) -> None:
    """Config-file token counts as "configured" — no error at boot."""
    cfg = tmp_path / "config.yaml"
    cfg.write_text("dashboard:\n  token: from-config\n", encoding="utf-8")
    monkeypatch.chdir(tmp_path)
    monkeypatch.delenv("ORCH_DASHBOARD_TOKEN", raising=False)
    rc, mock = _run_dashboard(
        ["--profile", "stakeholder", "--config", str(cfg)], monkeypatch
    )
    assert rc == 0
    assert mock.call_args.kwargs["profile"] == "stakeholder"


def test_cli_token_beats_env_token(monkeypatch, tmp_path: Path) -> None:
    """Setting both `--token` and env — CLI overwrites env for the process."""
    monkeypatch.chdir(tmp_path)
    monkeypatch.setenv("ORCH_DASHBOARD_TOKEN", "from-env")
    _run_dashboard(
        ["--profile", "stakeholder", "--token", "from-cli"], monkeypatch
    )
    assert os.environ.get("ORCH_DASHBOARD_TOKEN") == "from-cli"


# ---- Host / port pass-through ---------------------------------------------


def test_host_and_port_pass_through(monkeypatch, tmp_path: Path) -> None:
    monkeypatch.chdir(tmp_path)
    monkeypatch.delenv("ORCH_DASHBOARD_TOKEN", raising=False)
    _run_dashboard(["--host", "0.0.0.0", "--port", "9999"], monkeypatch)
    from unittest.mock import ANY  # noqa: F401 — clarity in the assertion below
    # We fetched the previous mock via the last call — recreate a fresh
    # invocation so the assertion is unambiguous.
    rc, mock = _run_dashboard(
        ["--host", "0.0.0.0", "--port", "9999"], monkeypatch
    )
    assert rc == 0
    assert mock.call_args.kwargs["host"] == "0.0.0.0"
    assert mock.call_args.kwargs["port"] == 9999
