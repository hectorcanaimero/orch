"""Tests for the `orch status` subcommand (Sprint C — commit 2).

Parametrized over both backends via a shared `project` fixture that mimics
the layout in `test_task_status_cmd.py` (tasks.json + scripts/task-*.sh
+ orchestrator/config.yaml + orchestrator/model_router.yaml).
"""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from orchestrator.orch import _run_status_subcommand
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
    """Scaffold a minimal orch project rooted at `root` for `backend_kind`."""
    root.mkdir(parents=True, exist_ok=True)
    (root / "tasks.json").write_bytes(FIXTURE_TASKS.read_bytes())
    scripts = root / "scripts"
    scripts.mkdir()
    for name in ("task-start.sh", "task-finish.sh", "task-block.sh"):
        (scripts / name).write_text("#!/usr/bin/env bash\nexit 0\n")
        (scripts / name).chmod(0o755)
    orch_dir = root / "orchestrator"
    orch_dir.mkdir()
    (orch_dir / "config.yaml").write_text(
        f"state:\n  backend: {backend_kind}\n  sqlite_path: null\n"
    )
    # Router entries for the two models used in the fixture.
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
        "--project-id", "proj-status",
        "--config", "orchestrator/config.yaml",
    ]


def test_status_json_shape(project, capsys: pytest.CaptureFixture) -> None:
    root, _ = project
    rc = _run_status_subcommand([*_common_args(root), "--json"])
    assert rc == 0
    payload = json.loads(capsys.readouterr().out)
    assert set(payload.keys()) >= {
        "project", "totals", "cost", "latest_run", "tasks", "filters",
    }
    assert payload["project"]["project_id"] == "proj-status"
    # 5 rows from the tiny fixture.
    ids = {row["id"] for row in payload["tasks"]}
    assert ids == {"T-A", "T-B", "T-C", "T-D", "T-E"}
    for row in payload["tasks"]:
        # Backend/model resolve for both known routes.
        assert row["backend"] in {"opencode", "claude"}
        # Every row has a defer_reason key (null for reads from disk).
        assert "defer_reason" in row
        assert row["defer_reason"] is None
        # cost_usd defaults to 0.0 with no spend recorded.
        assert row["cost_usd"] == 0.0


def test_status_only_filter(project, capsys: pytest.CaptureFixture) -> None:
    root, _ = project
    rc = _run_status_subcommand([*_common_args(root), "--json", "--only", "T-A"])
    assert rc == 0
    payload = json.loads(capsys.readouterr().out)
    ids = [r["id"] for r in payload["tasks"]]
    assert ids == ["T-A"]
    # Totals still count all 5.
    assert payload["totals"]["_total"] == 5


def test_status_status_filter(project, capsys: pytest.CaptureFixture) -> None:
    root, _ = project
    rc = _run_status_subcommand([*_common_args(root), "--json", "--status", "blocked"])
    assert rc == 0
    payload = json.loads(capsys.readouterr().out)
    ids = [r["id"] for r in payload["tasks"]]
    # T-E starts blocked in the fixture.
    assert ids == ["T-E"]


def test_status_human_prints_header(project, capsys: pytest.CaptureFixture) -> None:
    root, _ = project
    rc = _run_status_subcommand(_common_args(root))
    assert rc == 0
    out = capsys.readouterr().out
    # Header line mentions the project id and backend kind.
    assert "proj-status" in out
    assert ("backend=file" in out) or ("backend=sqlite" in out)


def test_status_missing_project_layout_returns_1(tmp_path: Path) -> None:
    empty = tmp_path / "no-proj"
    empty.mkdir()
    rc = _run_status_subcommand([
        "--project-root", str(empty),
        "--config", "orchestrator/config.yaml",
    ])
    assert rc == 1
