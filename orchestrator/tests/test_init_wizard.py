"""Tests for the `orch init` interactive wizard (Sprint D — commit 4).

The wizard is opt-in: batch mode with existing tests in `test_init.py`
continues to work unchanged. New tests here cover:

    * `prompt()` helper: defaults, validation, choices.
    * `run_wizard()`: happy-path scaffold using an injected input queue.
    * TTY fallback: `run_init_cli` errors out when stdin isn't a TTY and
      no flags/path were provided.
    * Overwrite prompt: existing files kept by default, replaced on "y".
    * Config post-processing: state.backend + budgets_preset + spec_root
      reflect wizard answers.
    * Batch mode preservation: passing `--project-name` etc. bypasses
      the wizard entirely.
"""

from __future__ import annotations

import io
import json
import sys
from pathlib import Path

import pytest

from orchestrator.init_cmd import (
    _is_scaffolder_flag_provided,
    _validate_project_id,
    _validate_project_root,
    orch_init,
    prompt,
    run_init_cli,
    run_wizard,
)


# ---- prompt() unit tests ------------------------------------------------


def _queue_input(answers: list[str]):
    """Return a callable suitable for `input_fn=` that returns answers in order."""
    it = iter(answers)

    def _inner(_prompt: str) -> str:
        try:
            return next(it)
        except StopIteration:
            raise AssertionError(
                "wizard asked for more input than the test provided"
            )
    return _inner


def test_prompt_returns_default_on_empty() -> None:
    fn = _queue_input([""])
    assert prompt("x", default="hello", input_fn=fn) == "hello"


def test_prompt_reprompts_on_empty_when_no_default(
    capsys: pytest.CaptureFixture,
) -> None:
    fn = _queue_input(["", "eventual"])
    assert prompt("x", input_fn=fn) == "eventual"
    out = capsys.readouterr().out
    assert "value required" in out


def test_prompt_enforces_choices(capsys: pytest.CaptureFixture) -> None:
    fn = _queue_input(["xxx", "yes"])
    assert prompt("q", choices=["yes", "no"], input_fn=fn) == "yes"
    assert "must be one of" in capsys.readouterr().out


def test_prompt_runs_validate(capsys: pytest.CaptureFixture) -> None:
    def _v(s: str) -> str | None:
        return None if s.isdigit() else "not a number"

    fn = _queue_input(["abc", "42"])
    assert prompt("q", validate=_v, input_fn=fn) == "42"
    assert "not a number" in capsys.readouterr().out


def test_validate_project_id_rules() -> None:
    assert _validate_project_id("myproj") is None
    assert _validate_project_id("myproj_2") is None
    assert _validate_project_id("myproj-2") is None
    # Invalid: uppercase, spaces, leading dash.
    assert _validate_project_id("MyProj") is not None
    assert _validate_project_id("my proj") is not None
    assert _validate_project_id("-leading") is not None


def test_validate_project_root_missing_parent(tmp_path: Path) -> None:
    err = _validate_project_root(str(tmp_path / "missing" / "nope"))
    assert err is not None and "does not exist" in err


def test_validate_project_root_ok(tmp_path: Path) -> None:
    assert _validate_project_root(str(tmp_path / "new-dir")) is None


# ---- flag detection -----------------------------------------------------


def test_is_scaffolder_flag_provided_true_with_path() -> None:
    import argparse
    ns = argparse.Namespace(path="/some/path", force=False, sdd=False, project_name=None)
    assert _is_scaffolder_flag_provided(ns) is True


def test_is_scaffolder_flag_provided_true_with_force() -> None:
    import argparse
    ns = argparse.Namespace(path=None, force=True, sdd=False, project_name=None)
    assert _is_scaffolder_flag_provided(ns) is True


def test_is_scaffolder_flag_provided_false_when_bare() -> None:
    import argparse
    ns = argparse.Namespace(path=None, force=False, sdd=False, project_name=None)
    assert _is_scaffolder_flag_provided(ns) is False


# ---- run_wizard end-to-end ---------------------------------------------


def _wizard_answers(
    project_id: str,
    project_root: Path,
    *,
    state_backend: str = "file",
    budget_preset: str = "conservative",
    spec_root: str = "specs",
    sdd: str = "n",
) -> list[str]:
    """Build the canonical answer sequence the wizard consumes.

    Order matches the run_wizard() prompt sequence:
        project_id, project_root, state_backend, budget_preset, spec_root,
        3 tier picks (defaults), sdd flag.
    """
    return [
        project_id,
        str(project_root),
        state_backend,
        budget_preset,
        spec_root,
        "",  # premium default
        "",  # standard default
        "",  # cheap default
        sdd,
    ]


def test_wizard_scaffolds_fresh_project(tmp_path: Path) -> None:
    import argparse

    project_root = tmp_path / "new-project"
    answers = _wizard_answers("myproj", project_root)
    args = argparse.Namespace()
    out = io.StringIO()
    rc = run_wizard(args, input_fn=_queue_input(answers), output_stream=out)
    assert rc == 0
    # Every file the batch scaffolder writes is present.
    assert (project_root / "tasks.json").exists()
    assert (project_root / "orchestrator" / "config.yaml").exists()
    assert (project_root / "orchestrator" / "model_router.yaml").exists()
    assert (project_root / "scripts" / "task-start.sh").exists()
    # Post-processing wrote the project id into meta.
    tasks_payload = json.loads((project_root / "tasks.json").read_text())
    assert tasks_payload["meta"]["project"] == "myproj"
    # And validate ran ok in the wizard summary.
    log = out.getvalue()
    assert "no issues" in log.lower() or "issue(s)" in log.lower()


def test_wizard_writes_selected_state_backend(tmp_path: Path) -> None:
    import argparse

    project_root = tmp_path / "sqlite-proj"
    answers = _wizard_answers("sqproj", project_root, state_backend="sqlite")
    args = argparse.Namespace()
    rc = run_wizard(args, input_fn=_queue_input(answers), output_stream=io.StringIO())
    assert rc == 0
    cfg = (project_root / "orchestrator" / "config.yaml").read_text()
    assert "backend: sqlite" in cfg


def test_wizard_writes_selected_budget_preset(tmp_path: Path) -> None:
    import argparse

    project_root = tmp_path / "aggr-proj"
    answers = _wizard_answers("aproj", project_root, budget_preset="aggressive")
    args = argparse.Namespace()
    rc = run_wizard(args, input_fn=_queue_input(answers), output_stream=io.StringIO())
    assert rc == 0
    cfg = (project_root / "orchestrator" / "config.yaml").read_text()
    assert "budgets_preset: aggressive" in cfg


def test_wizard_writes_selected_spec_root(tmp_path: Path) -> None:
    import argparse

    project_root = tmp_path / "spec-proj"
    answers = _wizard_answers("sproj", project_root, spec_root="docs/specs")
    args = argparse.Namespace()
    rc = run_wizard(args, input_fn=_queue_input(answers), output_stream=io.StringIO())
    assert rc == 0
    cfg = (project_root / "orchestrator" / "config.yaml").read_text()
    assert "spec_root: docs/specs" in cfg


def test_wizard_overwrite_prompt_keeps_existing(tmp_path: Path) -> None:
    import argparse

    project_root = tmp_path / "existing"
    project_root.mkdir()
    (project_root / "tasks.json").write_text('{"custom":"file"}')

    # Answers now include one extra "n" for the overwrite prompt.
    answers = _wizard_answers("existing", project_root)
    answers.append("n")  # keep existing tasks.json
    args = argparse.Namespace()
    rc = run_wizard(args, input_fn=_queue_input(answers), output_stream=io.StringIO())
    assert rc == 0
    # Existing content preserved because user chose "n".
    assert (project_root / "tasks.json").read_text() == '{"custom":"file"}'


def test_wizard_overwrite_prompt_replaces_on_yes(tmp_path: Path) -> None:
    import argparse

    project_root = tmp_path / "existing2"
    project_root.mkdir()
    (project_root / "tasks.json").write_text('{"old":"data"}')

    answers = _wizard_answers("existing2", project_root)
    answers.append("y")  # replace tasks.json
    args = argparse.Namespace()
    rc = run_wizard(args, input_fn=_queue_input(answers), output_stream=io.StringIO())
    assert rc == 0
    payload = json.loads((project_root / "tasks.json").read_text())
    assert "meta" in payload
    assert payload["meta"]["project"] == "existing2"


# ---- run_init_cli routing ------------------------------------------------


def test_run_init_cli_batch_path_bypasses_wizard(tmp_path: Path) -> None:
    """Passing PATH means batch mode — no TTY check, no wizard."""
    project_root = tmp_path / "batch"
    rc = run_init_cli([str(project_root), "--project-name", "batchproj"])
    assert rc == 0
    assert (project_root / "tasks.json").exists()
    payload = json.loads((project_root / "tasks.json").read_text())
    assert payload["meta"]["project"] == "batchproj"


def test_run_init_cli_non_interactive_without_path_errors(
    tmp_path: Path,
    monkeypatch,
    capsys: pytest.CaptureFixture,
) -> None:
    # Ensure isatty is False so the wizard would also be blocked — but
    # --non-interactive means we exit with an argparse error either way.
    monkeypatch.setattr("sys.stdin.isatty", lambda: False)
    with pytest.raises(SystemExit):
        run_init_cli(["--non-interactive"])


def test_run_init_cli_no_flags_no_tty_errors(monkeypatch) -> None:
    """CI/pipe safety: without a TTY, blank invocation must error."""
    monkeypatch.setattr("sys.stdin.isatty", lambda: False)
    with pytest.raises(SystemExit):
        run_init_cli([])


def test_run_init_cli_interactive_flag_forces_wizard(
    tmp_path: Path,
    monkeypatch,
) -> None:
    """--interactive must trigger wizard even when a PATH is present.

    We provide the answers via prompt input via a monkeypatched builtins.input.
    """
    project_root = tmp_path / "force-wizard"
    answers = iter(_wizard_answers("forced", project_root))

    def fake_input(_prompt: str) -> str:
        return next(answers)

    monkeypatch.setattr("sys.stdin.isatty", lambda: True)
    monkeypatch.setattr("builtins.input", fake_input)
    # Suppress noisy stdout during the wizard.
    monkeypatch.setattr(sys, "stdout", io.StringIO())

    rc = run_init_cli(["--interactive"])
    assert rc == 0
    assert (project_root / "tasks.json").exists()


# ---- backwards-compat with existing batch behavior ---------------------


def test_batch_scaffold_still_refuses_without_force(tmp_path: Path) -> None:
    """Regression: batch mode without --force still errors on conflicts."""
    project_root = tmp_path / "b1"
    project_root.mkdir()
    (project_root / "tasks.json").write_text("existing")
    # No --force → should exit 1 (existing test in test_init.py covers this
    # too; we assert here that the CLI wrapper preserves the behavior).
    rc = run_init_cli([str(project_root), "--project-name", "b1"])
    assert rc == 1
    # And the existing file was not touched.
    assert (project_root / "tasks.json").read_text() == "existing"


def test_batch_scaffold_force_flag_still_overwrites(tmp_path: Path) -> None:
    project_root = tmp_path / "b2"
    project_root.mkdir()
    (project_root / "tasks.json").write_text("existing")
    rc = run_init_cli([str(project_root), "--force", "--project-name", "b2"])
    assert rc == 0
    payload = json.loads((project_root / "tasks.json").read_text())
    assert payload["meta"]["project"] == "b2"
