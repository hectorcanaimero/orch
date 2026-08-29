"""Tests for `_load_tasks_hydrated` (Sprint F-8, fix #72).

The dashboard endpoints (`/api/milestones`, `/api/sprint`, `/api/summary`)
must show runtime status from the SQLite backend, not the stale
`tasks.json` seed. The shared helper is the enforcement point; a unit test
here catches regressions before the endpoint-level tests do.
"""

from __future__ import annotations

import json
import sqlite3
from pathlib import Path

import pytest

pytest.importorskip("fastapi")


def _reset_caches() -> None:
    from orchestrator.state import _reset_backend_cache
    _reset_backend_cache()
    try:
        from orchestrator.state.sqlite_backend import _reset_schema_cache_for_tests
        _reset_schema_cache_for_tests()
    except ImportError:
        pass


def _make_project(tmp_path: Path, *, backend: str) -> "ProjectPaths":  # type: ignore[name-defined]
    from orchestrator.paths import ProjectPaths

    root = tmp_path / "proj"
    (root / ".orchestrator" / "state").mkdir(parents=True)

    (root / "tasks.json").write_text(
        json.dumps({
            "meta": {"project": "proj"},
            "phases": [{"id": 0, "name": "F0"}],
            "tasks": [
                {
                    "id": "T-A", "phase": 0, "title": "A", "description": "",
                    "model": "claude/claude-sonnet-4-6", "reason": "",
                    "status": "todo", "dependencies": [], "estimate_hours": 0.1,
                    "files": [], "spec_ref": "", "comments": [],
                },
                {
                    "id": "T-B", "phase": 0, "title": "B", "description": "",
                    "model": "claude/claude-sonnet-4-6", "reason": "",
                    "status": "todo", "dependencies": [], "estimate_hours": 0.1,
                    "files": [], "spec_ref": "", "comments": [],
                },
            ],
        }),
        encoding="utf-8",
    )
    cfg = root / ".orchestrator" / "config.yaml"
    cfg.write_text(f"state:\n  backend: {backend}\n", encoding="utf-8")

    return ProjectPaths(
        project_root=root, project_id="proj",
        config_yaml=cfg, explicit_root=True, state_layout="legacy",
    )


def test_hydrated_loader_overlays_backend_status(tmp_path: Path) -> None:
    """SQLite says T-A is `done` → hydrated loader must reflect it,
    even though tasks.json still says `todo`."""
    _reset_caches()
    try:
        paths = _make_project(tmp_path, backend="sqlite")

        # Bootstrap the DB and flip T-A to done directly.
        from orchestrator.state import get_backend, load_tasks
        backend = get_backend(paths, {"state": {"backend": "sqlite"}})
        backend.bootstrap(load_tasks(paths.tasks_json))
        backend.set_task_status(
            "T-A", "done", author="test",
            note="test", ts="2026-08-29T00:00:00Z",
        )

        from orchestrator.dashboard.server import _load_tasks_hydrated
        tasks = _load_tasks_hydrated(paths)
        by_id = {t.id: t for t in tasks}
        assert by_id["T-A"].status == "done"    # DB wins
        assert by_id["T-B"].status == "todo"    # not touched
    finally:
        _reset_caches()


def test_hydrated_loader_falls_back_to_tasksjson_for_file_backend(
    tmp_path: Path,
) -> None:
    """File backend has no `get_all_task_status()` → helper must still
    return tasks (with tasks.json statuses), not crash."""
    _reset_caches()
    try:
        paths = _make_project(tmp_path, backend="file")

        from orchestrator.dashboard.server import _load_tasks_hydrated
        tasks = _load_tasks_hydrated(paths)
        # tasks.json seed is `todo` for both.
        assert {t.id: t.status for t in tasks} == {"T-A": "todo", "T-B": "todo"}
    finally:
        _reset_caches()


def test_hydrated_loader_returns_empty_when_tasksjson_missing(
    tmp_path: Path,
) -> None:
    from orchestrator.paths import ProjectPaths
    root = tmp_path / "proj"
    (root / ".orchestrator").mkdir(parents=True)
    cfg = root / ".orchestrator" / "config.yaml"
    cfg.write_text("state:\n  backend: file\n", encoding="utf-8")
    paths = ProjectPaths(
        project_root=root, project_id="proj",
        config_yaml=cfg, explicit_root=True, state_layout="legacy",
    )

    from orchestrator.dashboard.server import _load_tasks_hydrated
    assert _load_tasks_hydrated(paths) == []
