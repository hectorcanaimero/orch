"""Sprint C tests: `--dry-run --json` shape + end-of-run summary printer.

The full main loop is already exercised by `test_orch.py`. Here we cover
just the new bits: the JSON dry-run output shape, the parser's `--json
requires --dry-run` validator, and the standalone `_print_run_summary`
renderer that the main loop hooks at drain-end.
"""

from __future__ import annotations

import io
import json
import shutil
from pathlib import Path
from types import SimpleNamespace

import pytest

from orchestrator import orch as orch_mod


FIXTURES = Path(__file__).parent / "fixtures"


def _stage_project(root: Path) -> Path:
    """Stage the same v2/ layout `test_orch` uses (no chdir)."""
    v2 = root / "v2"
    v2.mkdir(parents=True, exist_ok=True)
    (v2 / "scripts").mkdir(exist_ok=True)
    (v2 / "orchestrator" / "state").mkdir(parents=True, exist_ok=True)
    shutil.copy(FIXTURES / "main_loop_tasks.json", v2 / "tasks.json")
    for name in ("task-start.sh", "task-finish.sh", "task-block.sh"):
        (v2 / "scripts" / name).write_text("#!/bin/sh\nexit 0\n")
        (v2 / "scripts" / name).chmod(0o755)
    shutil.copy(FIXTURES / "main_loop_router.yaml",
                v2 / "orchestrator" / "model_router.yaml")
    shutil.copy(FIXTURES / "main_loop_config.yaml",
                v2 / "orchestrator" / "config.yaml")
    return v2


def test_dry_run_json_shape(
    tmp_path: Path, capsys: pytest.CaptureFixture, monkeypatch: pytest.MonkeyPatch,
) -> None:
    v2 = _stage_project(tmp_path)
    monkeypatch.chdir(v2)
    rc = orch_mod.main(["--dry-run", "--json"])
    assert rc == 0
    payload = json.loads(capsys.readouterr().out)
    assert set(payload.keys()) == {"plan", "count"}
    assert payload["count"] == len(payload["plan"])
    for row in payload["plan"]:
        assert {
            "task_id", "phase", "backend", "cli_model", "tier",
            "model", "estimate_hours",
        } <= set(row.keys())


def test_dry_run_json_requires_dry_run_flag(
    tmp_path: Path, capsys: pytest.CaptureFixture, monkeypatch: pytest.MonkeyPatch,
) -> None:
    v2 = _stage_project(tmp_path)
    monkeypatch.chdir(v2)
    with pytest.raises(SystemExit) as exc_info:
        orch_mod.main(["--json"])
    assert exc_info.value.code == 2
    err = capsys.readouterr().err
    assert "--json requires --dry-run" in err


def test_dry_run_no_json_still_prints_table(
    tmp_path: Path, capsys: pytest.CaptureFixture, monkeypatch: pytest.MonkeyPatch,
) -> None:
    """Regression guard: `--dry-run` alone must keep the human table output."""
    v2 = _stage_project(tmp_path)
    monkeypatch.chdir(v2)
    rc = orch_mod.main(["--dry-run"])
    assert rc == 0
    out = capsys.readouterr().out
    # Human plan title from _print_plan_table.
    assert "Dry-run plan" in out


# ---- _print_run_summary -------------------------------------------------


def _fake_run(**overrides) -> SimpleNamespace:
    """Build a minimal run_file duck for `_print_run_summary`."""
    state = SimpleNamespace(
        completed=overrides.get("completed", ["T-A", "T-B"]),
        blocked=overrides.get("blocked", []),
        deferred=overrides.get("deferred", []),
        in_flight=overrides.get("in_flight", {}),
    )
    return SimpleNamespace(state=state)


def test_run_summary_prints_costs_and_counts(capsys: pytest.CaptureFixture) -> None:
    run_file = _fake_run(completed=["T-A", "T-B"], blocked=["T-C"])
    orch_mod._print_run_summary(
        run_id="abcdef123456789",
        run_file=run_file,
        task_costs={"T-A": 0.10, "T-B": 0.05, "T-C": 0.0},
    )
    out = capsys.readouterr().out
    # Header line assertions.
    assert "Run summary" in out
    assert "completed=2" in out
    assert "blocked=1" in out
    assert "$0.1500" in out


def test_run_summary_empty_state_is_safe(capsys: pytest.CaptureFixture) -> None:
    run_file = _fake_run(completed=[], blocked=[], deferred=[])
    orch_mod._print_run_summary(
        run_id="zerocase",
        run_file=run_file,
        task_costs={},
    )
    out = capsys.readouterr().out
    assert "Run summary" in out
    assert "completed=0" in out
    assert "cost=$0.0000" in out


def test_run_summary_reports_defer_reasons(capsys: pytest.CaptureFixture) -> None:
    run_file = _fake_run(completed=[], deferred=[])
    orch_mod._print_run_summary(
        run_id="deferred-run",
        run_file=run_file,
        task_costs={},
        deferred={"T-X"},
        defer_reasons={"T-X": "blocked-by-budget:claude"},
    )
    out = capsys.readouterr().out
    assert "deferred=1" in out
    assert "T-X" in out
