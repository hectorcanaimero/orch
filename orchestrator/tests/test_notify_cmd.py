"""CLI tests for `orch notify test | digest` (Sprint G-6)."""
from __future__ import annotations

from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from orchestrator.orch import _run_notify_subcommand
from orchestrator.tests.test_validate_cmd import _common, _task, _write_project


# ---- `orch notify test` -----------------------------------------------------


def test_notify_test_exits_1_when_no_webhook_configured(
    tmp_path: Path, capsys: pytest.CaptureFixture,
) -> None:
    root = tmp_path / "proj"
    _write_project(root, tasks=[_task("A")])
    rc = _run_notify_subcommand(["test", *_common(root)])
    err = capsys.readouterr().err
    assert rc == 1
    assert "no webhook configured" in err


def test_notify_test_invokes_notify_test_when_webhook_configured(
    tmp_path: Path, capsys: pytest.CaptureFixture,
) -> None:
    root = tmp_path / "proj"
    _write_project(
        root,
        tasks=[_task("A")],
        config_extra="\nnotifications:\n  slack_webhook: https://hooks.slack.com/X\n",
    )
    fake = MagicMock()
    fake.enabled = True
    fake.notify_test.return_value = True
    with patch("orchestrator.orch.Notifier.from_config", return_value=fake):
        rc = _run_notify_subcommand(["test", *_common(root)])
    assert rc == 0
    fake.notify_test.assert_called_once()
    # Custom message override reaches the notifier.
    with patch("orchestrator.orch.Notifier.from_config", return_value=fake):
        _run_notify_subcommand(["test", "--message", "hello", *_common(root)])
    assert fake.notify_test.call_args[0][0] == "hello"


def test_notify_test_exits_1_when_all_channels_reject(
    tmp_path: Path,
) -> None:
    root = tmp_path / "proj"
    _write_project(
        root,
        tasks=[_task("A")],
        config_extra="\nnotifications:\n  slack_webhook: https://hooks.slack.com/X\n",
    )
    fake = MagicMock()
    fake.enabled = True
    fake.notify_test.return_value = False
    with patch("orchestrator.orch.Notifier.from_config", return_value=fake):
        rc = _run_notify_subcommand(["test", *_common(root)])
    assert rc == 1


# ---- `orch notify digest` ---------------------------------------------------


def test_notify_digest_requires_sqlite_backend(
    tmp_path: Path, capsys: pytest.CaptureFixture,
) -> None:
    """File-backend project → exit 2 with a clear message."""
    root = tmp_path / "proj"
    _write_project(root, tasks=[_task("A")])
    # No `orch migrate` was run → default is file backend.
    rc = _run_notify_subcommand(["digest", *_common(root)])
    err = capsys.readouterr().err
    assert rc == 2
    assert "SQLite backend" in err
