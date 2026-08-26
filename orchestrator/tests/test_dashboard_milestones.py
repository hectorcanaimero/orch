"""Tests for `GET /api/milestones`.

Sprint F-3: the endpoint returns all milestones with task-progress data when
the SQLite backend is configured, or an empty list with `"backend": "file"` for
the file backend.
"""

from __future__ import annotations

import json
from pathlib import Path

import pytest


# ---- helpers -----------------------------------------------------------------


def _make_project(tmp_path: Path, *, backend: str = "file") -> "ProjectPaths":  # type: ignore[name-defined]
    from orchestrator.paths import ProjectPaths

    root = tmp_path / "proj"
    (root / ".orchestrator" / "state").mkdir(parents=True)
    (root / "scripts").mkdir(parents=True)
    (root / "scripts" / "task-start.sh").write_text("#!/bin/sh\nexit 0\n")

    (root / "tasks.json").write_text(
        json.dumps({"meta": {}, "phases": [], "tasks": []}),
        encoding="utf-8",
    )

    cfg_path = root / ".orchestrator" / "config.yaml"
    cfg_path.write_text(
        f"state:\n  backend: {backend}\n",
        encoding="utf-8",
    )

    return ProjectPaths(
        project_root=root,
        project_id="proj",
        config_yaml=cfg_path,
        explicit_root=True,
        state_layout="legacy",
    )


def _client(paths: "ProjectPaths") -> "TestClient":  # type: ignore[name-defined]
    pytest.importorskip("fastapi")
    from fastapi.testclient import TestClient
    from orchestrator.dashboard.server import create_app

    app = create_app(paths=paths)
    return TestClient(app)


def _reset_caches() -> None:
    from orchestrator.state import _reset_backend_cache

    _reset_backend_cache()
    try:
        from orchestrator.state.sqlite_backend import _reset_schema_cache_for_tests

        _reset_schema_cache_for_tests()
    except ImportError:
        pass


# ---- tests -------------------------------------------------------------------


def test_milestones_returns_empty_list_for_file_backend(tmp_path: Path) -> None:
    """File backend → 200 with milestones=[] and backend='file'."""
    _reset_caches()
    try:
        paths = _make_project(tmp_path, backend="file")
        client = _client(paths)

        r = client.get("/api/milestones")
        assert r.status_code == 200
        payload = r.json()
        assert payload["milestones"] == []
        assert payload["backend"] == "file"
    finally:
        _reset_caches()


def test_milestones_returns_empty_list_when_no_milestones(tmp_path: Path) -> None:
    """SQLite backend with no milestones → 200 with milestones=[]."""
    pytest.importorskip("sqlite3")
    _reset_caches()
    try:
        paths = _make_project(tmp_path, backend="sqlite")
        client = _client(paths)

        r = client.get("/api/milestones")
        assert r.status_code == 200
        payload = r.json()
        assert payload["milestones"] == []
        assert "backend" not in payload
    finally:
        _reset_caches()


def test_milestones_returns_milestone_data(tmp_path: Path) -> None:
    """SQLite backend with one milestone → milestone dict with progress included."""
    pytest.importorskip("sqlite3")
    _reset_caches()
    try:
        paths = _make_project(tmp_path, backend="sqlite")

        # Seed the SQLite backend with a milestone.
        from orchestrator.state.sqlite_backend import SqliteBackend

        db = paths.state_dir / "orch.db"
        be = SqliteBackend(
            db_path=db,
            project_id=paths.project_id,
            project_root=paths.project_root,
        )
        be.bootstrap([])
        be.upsert_milestone("M1", title="First Milestone")

        client = _client(paths)
        r = client.get("/api/milestones")
        assert r.status_code == 200
        payload = r.json()
        milestones = payload["milestones"]
        assert len(milestones) == 1
        m = milestones[0]
        assert m["id"] == "M1"
        assert m["title"] == "First Milestone"
        assert "progress" in m
        assert m["progress"]["total"] == 0
        assert m["progress"]["done"] == 0
        assert m["progress"]["pct"] == 0
    finally:
        _reset_caches()


def test_milestones_missing_config_falls_back_to_file(tmp_path: Path) -> None:
    """If config.yaml is absent, file backend is assumed → milestones=[]."""
    from orchestrator.paths import ProjectPaths

    root = tmp_path / "proj2"
    (root / ".orchestrator" / "state").mkdir(parents=True)
    (root / "scripts").mkdir(parents=True)
    (root / "scripts" / "task-start.sh").write_text("#!/bin/sh\nexit 0\n")
    (root / "tasks.json").write_text(
        json.dumps({"meta": {}, "phases": [], "tasks": []}),
        encoding="utf-8",
    )

    # config_yaml does NOT exist.
    cfg_path = root / ".orchestrator" / "config.yaml"
    paths = ProjectPaths(
        project_root=root,
        project_id="proj2",
        config_yaml=cfg_path,
        explicit_root=True,
        state_layout="legacy",
    )

    _reset_caches()
    try:
        client = _client(paths)
        r = client.get("/api/milestones")
        assert r.status_code == 200
        payload = r.json()
        assert payload["milestones"] == []
        assert payload["backend"] == "file"
    finally:
        _reset_caches()
