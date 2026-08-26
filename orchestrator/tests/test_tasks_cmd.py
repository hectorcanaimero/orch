"""Tests for the `orch tasks` subcommand (Sprint C — commit 2)."""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from orchestrator.orch import _run_tasks_subcommand
from orchestrator.state import _reset_backend_cache
from orchestrator.state.sqlite_backend import _reset_schema_cache_for_tests

FIXTURE_TASKS = Path(__file__).parent / "fixtures" / "tiny_tasks.json"


@pytest.fixture(autouse=True)
def _clear_caches():
    _reset_backend_cache()
    _reset_schema_cache_for_tests()
    yield
    _reset_backend_cache()
    _reset_schema_cache_for_tests()


def _make_project(root: Path, backend_kind: str) -> None:
    root.mkdir(parents=True, exist_ok=True)
    (root / "tasks.json").write_bytes(FIXTURE_TASKS.read_bytes())
    scripts = root / "scripts"
    scripts.mkdir()
    for name in ("task-start.sh", "task-finish.sh", "task-block.sh"):
        (scripts / name).write_text("#!/usr/bin/env bash\nexit 0\n")
        (scripts / name).chmod(0o755)
    orch_dir = root / ".orchestrator"
    orch_dir.mkdir()
    (orch_dir / "config.yaml").write_text(
        f"state:\n  backend: {backend_kind}\n  sqlite_path: null\n"
    )
    (orch_dir / "model_router.yaml").write_text(
        "opencode-go/glm-5.1:\n"
        "  backend: opencode\n"
        "  cli_model: glm-5.1\n"
        "  tier: cheap\n"
        "opencode/claude-sonnet-4-6:\n"
        "  backend: claude\n"
        "  cli_model: claude-sonnet-4-6\n"
        "  tier: standard\n"
    )


@pytest.fixture(params=["file", "sqlite"], ids=lambda b: f"backend={b}")
def project(request, tmp_path: Path) -> tuple[Path, str]:
    root = tmp_path / "proj"
    _make_project(root, request.param)
    return root, request.param


def _common_args(root: Path) -> list[str]:
    return [
        "--project-root", str(root),
        "--project-id", "proj-tasks",
        "--config", ".orchestrator/config.yaml",
    ]


def test_tasks_json_returns_trimmed_rows(
    project, capsys: pytest.CaptureFixture
) -> None:
    root, _ = project
    rc = _run_tasks_subcommand([*_common_args(root), "--json"])
    assert rc == 0
    payload = json.loads(capsys.readouterr().out)
    assert isinstance(payload, list)
    assert len(payload) == 5
    row_keys = set(payload[0].keys())
    assert row_keys == {"id", "status", "backend", "cli_model", "phase", "dependencies"}
    # T-C has T-A and T-B as deps in the fixture.
    by_id = {r["id"]: r for r in payload}
    assert by_id["T-C"]["dependencies"] == ["T-A", "T-B"]


def test_tasks_status_filter_pins_to_blocked(
    project, capsys: pytest.CaptureFixture
) -> None:
    root, _ = project
    rc = _run_tasks_subcommand([*_common_args(root), "--json", "--status", "blocked"])
    assert rc == 0
    payload = json.loads(capsys.readouterr().out)
    assert [r["id"] for r in payload] == ["T-E"]


def test_tasks_only_glob_narrows_output(
    project, capsys: pytest.CaptureFixture
) -> None:
    root, _ = project
    rc = _run_tasks_subcommand([*_common_args(root), "--json", "--only", "T-C"])
    assert rc == 0
    payload = json.loads(capsys.readouterr().out)
    assert [r["id"] for r in payload] == ["T-C"]


def test_tasks_human_prints_table_header(
    project, capsys: pytest.CaptureFixture
) -> None:
    root, _ = project
    rc = _run_tasks_subcommand(_common_args(root))
    assert rc == 0
    out = capsys.readouterr().out
    assert "Tasks" in out
    # At least one known task id should appear in the rendered table.
    assert "T-A" in out
