"""CLI tests for `orch router add-missing` (issue #55).

Reuses the project-staging helpers from test_validate_cmd so the fixture
surface stays identical to the validator these entries feed.
"""

from __future__ import annotations

from pathlib import Path

import pytest

from orchestrator.orch import _run_router_add_missing_subcommand, _run_router_subcommand
from orchestrator.router import load_router
from orchestrator.tests.test_validate_cmd import _common, _task, _write_project


def _router_path(root: Path) -> Path:
    return root / ".orchestrator" / "model_router.yaml"


def test_add_missing_appends_and_exits_0(tmp_path: Path, capsys: pytest.CaptureFixture):
    root = tmp_path / "proj"
    _write_project(root, tasks=[_task("A"), _task("B", model="claude/claude-haiku-4-5")])
    rc = _run_router_add_missing_subcommand([*_common(root), "--yes"])
    out = capsys.readouterr().out
    assert rc == 0
    assert "Added 1 entry" in out
    router = load_router(_router_path(root))
    assert router["claude/claude-haiku-4-5"].backend == "claude"
    assert router["claude/claude-haiku-4-5"].cli_model == "claude-haiku-4-5"
    assert router["claude/claude-haiku-4-5"].tier == "standard"


def test_add_missing_nothing_to_add(tmp_path: Path, capsys: pytest.CaptureFixture):
    root = tmp_path / "proj"
    _write_project(root, tasks=[_task("A")])  # only the default routed model
    rc = _run_router_add_missing_subcommand([*_common(root), "--yes"])
    assert rc == 0
    assert "Nothing to add" in capsys.readouterr().out


def test_add_missing_abort_on_no(
    tmp_path: Path, capsys: pytest.CaptureFixture, monkeypatch: pytest.MonkeyPatch
):
    root = tmp_path / "proj"
    _write_project(root, tasks=[_task("B", model="codex/gpt-5.6")])
    monkeypatch.setattr("builtins.input", lambda _prompt: "n")
    rc = _run_router_add_missing_subcommand([*_common(root)])  # no --yes → prompts
    assert rc == 1
    assert "Aborted" in capsys.readouterr().out
    assert "codex/gpt-5.6" not in load_router(_router_path(root))  # unchanged


def test_add_missing_tier_override(tmp_path: Path, capsys: pytest.CaptureFixture):
    root = tmp_path / "proj"
    _write_project(root, tasks=[_task("B", model="gemini/gemini-3.0-pro")])
    rc = _run_router_add_missing_subcommand(
        [*_common(root), "--yes", "--tier", "premium"]
    )
    assert rc == 0
    router = load_router(_router_path(root))
    assert router["gemini/gemini-3.0-pro"].tier == "premium"
    assert router["gemini/gemini-3.0-pro"].is_premium is True


def test_add_missing_skips_uninferable_model(
    tmp_path: Path, capsys: pytest.CaptureFixture
):
    root = tmp_path / "proj"
    _write_project(root, tasks=[_task("B", model="mystery-model")])  # no backend prefix
    rc = _run_router_add_missing_subcommand([*_common(root), "--yes"])
    assert rc == 2
    assert "cannot infer backend" in capsys.readouterr().out


def test_router_bare_command_prints_usage(capsys: pytest.CaptureFixture):
    rc = _run_router_subcommand([])
    assert rc == 2
    assert "usage: orch router" in capsys.readouterr().out


def test_router_unknown_subcommand_errors(capsys: pytest.CaptureFixture):
    rc = _run_router_subcommand(["frobnicate"])
    assert rc == 2
    assert "unknown subcommand" in capsys.readouterr().err
