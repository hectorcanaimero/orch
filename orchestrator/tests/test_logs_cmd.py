"""Tests for `orch logs` subcommand (Sprint C — commit 3).

`orch logs` is backend-agnostic on purpose — it reads a plain per-task log
file the dispatcher wrote. We create the file, then verify tail semantics
and the missing-file exit code (2).
"""

from __future__ import annotations

from pathlib import Path

import pytest

from orchestrator.orch import _run_logs_subcommand

FIXTURE_TASKS = Path(__file__).parent / "fixtures" / "tiny_tasks.json"


def _make_project(root: Path) -> None:
    root.mkdir(parents=True, exist_ok=True)
    (root / "tasks.json").write_bytes(FIXTURE_TASKS.read_bytes())
    scripts = root / "scripts"
    scripts.mkdir()
    for name in ("task-start.sh", "task-finish.sh", "task-block.sh"):
        (scripts / name).write_text("#!/usr/bin/env bash\nexit 0\n")
        (scripts / name).chmod(0o755)
    orch_dir = root / "orchestrator"
    orch_dir.mkdir()
    (orch_dir / "config.yaml").write_text("state:\n  backend: file\n")


def _common_args(root: Path) -> list[str]:
    return [
        "--project-root", str(root),
        "--project-id", "proj-logs",
        "--config", "orchestrator/config.yaml",
    ]


def test_logs_missing_file_exits_2(tmp_path: Path) -> None:
    root = tmp_path / "proj"
    _make_project(root)
    rc = _run_logs_subcommand(["T-A", *_common_args(root)])
    assert rc == 2


def test_logs_tail_default_and_custom(
    tmp_path: Path, capsys: pytest.CaptureFixture
) -> None:
    root = tmp_path / "proj"
    _make_project(root)
    # Namespaced layout because --project-root was explicit → state/<project_id>/logs.
    logs_dir = root / "orchestrator" / "state" / "proj-logs" / "logs"
    logs_dir.mkdir(parents=True, exist_ok=True)
    log_lines = [f"line {i}\n" for i in range(300)]
    (logs_dir / "T-A.log").write_text("".join(log_lines))

    # Default tail=200 → last 200 lines.
    rc = _run_logs_subcommand(["T-A", *_common_args(root)])
    assert rc == 0
    out = capsys.readouterr().out
    assert out.count("\n") == 200
    assert out.startswith("line 100\n")
    assert out.rstrip().endswith("line 299")

    # Custom --tail 5.
    rc = _run_logs_subcommand(["T-A", "--tail", "5", *_common_args(root)])
    assert rc == 0
    out = capsys.readouterr().out
    assert out.count("\n") == 5
    assert out.startswith("line 295\n")


def test_logs_all_prints_everything(
    tmp_path: Path, capsys: pytest.CaptureFixture
) -> None:
    root = tmp_path / "proj"
    _make_project(root)
    logs_dir = root / "orchestrator" / "state" / "proj-logs" / "logs"
    logs_dir.mkdir(parents=True, exist_ok=True)
    (logs_dir / "T-A.log").write_text("only-line\n")

    rc = _run_logs_subcommand(["T-A", "--all", *_common_args(root)])
    assert rc == 0
    out = capsys.readouterr().out
    assert out == "only-line\n"


def test_logs_missing_project_layout_returns_1(tmp_path: Path) -> None:
    empty = tmp_path / "not-a-project"
    empty.mkdir()
    rc = _run_logs_subcommand([
        "T-A",
        "--project-root", str(empty),
        "--config", "orchestrator/config.yaml",
    ])
    assert rc == 1
