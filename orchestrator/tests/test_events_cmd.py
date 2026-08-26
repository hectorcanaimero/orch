"""Tests for `orch events` subcommand (Sprint C — commit 3).

Parametrized over both backends to prove the extended `iter_events()`
signature reaches both file and sqlite implementations.
"""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from orchestrator.models import EventEntry
from orchestrator.orch import _run_events_subcommand
from orchestrator.state import _reset_backend_cache, get_backend
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


def _seed_events(root: Path, backend_kind: str) -> str:
    """Bootstrap + create a run + append 5 events for T-A, 1 for T-B."""
    from orchestrator.paths import resolve_project_paths

    paths = resolve_project_paths(
        project_root_arg=str(root),
        project_id_arg="proj-events",
        config_arg=".orchestrator/config.yaml",
    )
    import yaml
    cfg = yaml.safe_load((root / ".orchestrator" / "config.yaml").read_text()) or {}
    backend = get_backend(paths, cfg)
    from orchestrator.state import load_tasks
    backend.bootstrap(load_tasks(paths.tasks_json))
    run_id = "r-events-fixture"
    backend.create_run(run_id=run_id, mode="auto")
    for i in range(5):
        backend.append_event(run_id, EventEntry(
            event_type="dispatch",
            task_id="T-A",
            backend="opencode",
            ts=f"2026-08-20T12:00:0{i}Z",
            extra={"pid": 100 + i},
        ))
    backend.append_event(run_id, EventEntry(
        event_type="dispatch",
        task_id="T-B",
        backend="opencode",
        ts="2026-08-20T13:00:00Z",
    ))
    return run_id


@pytest.fixture(params=["file", "sqlite"], ids=lambda b: f"backend={b}")
def project_with_events(request, tmp_path: Path) -> tuple[Path, str]:
    root = tmp_path / "proj"
    _make_project(root, request.param)
    _seed_events(root, request.param)
    return root, request.param


def _common_args(root: Path) -> list[str]:
    return [
        "--project-root", str(root),
        "--project-id", "proj-events",
        "--config", ".orchestrator/config.yaml",
    ]


def test_events_json_returns_task_filtered_rows(
    project_with_events, capsys: pytest.CaptureFixture
) -> None:
    root, _ = project_with_events
    rc = _run_events_subcommand([
        "T-A", "--json", *_common_args(root),
    ])
    assert rc == 0
    payload = json.loads(capsys.readouterr().out)
    assert isinstance(payload, list)
    assert len(payload) == 5
    assert all(r["task_id"] == "T-A" for r in payload)


def test_events_tail_flag_limits_output(
    project_with_events, capsys: pytest.CaptureFixture
) -> None:
    root, _ = project_with_events
    rc = _run_events_subcommand([
        "T-A", "--json", "--tail", "2", *_common_args(root),
    ])
    assert rc == 0
    payload = json.loads(capsys.readouterr().out)
    assert len(payload) == 2
    # Chronological order preserved in the tail (oldest → newest).
    assert payload[0]["ts"] == "2026-08-20T12:00:03Z"
    assert payload[1]["ts"] == "2026-08-20T12:00:04Z"


def test_events_unknown_task_returns_empty(
    project_with_events, capsys: pytest.CaptureFixture
) -> None:
    root, _ = project_with_events
    rc = _run_events_subcommand([
        "T-DOES-NOT-EXIST", "--json", *_common_args(root),
    ])
    assert rc == 0
    payload = json.loads(capsys.readouterr().out)
    assert payload == []


def test_events_human_output_mentions_task_id(
    project_with_events, capsys: pytest.CaptureFixture
) -> None:
    root, _ = project_with_events
    rc = _run_events_subcommand(["T-A", *_common_args(root)])
    assert rc == 0
    out = capsys.readouterr().out
    assert "T-A" in out or "Events" in out
