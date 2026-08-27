"""Tests for GET /api/summary — deterministic executive summary (G-4).

Reuses the SQLite-backed project fixture shape from test_dashboard_milestones.
"""
from __future__ import annotations

import json
from pathlib import Path

import pytest


def _make_project(tmp_path: Path, *, backend: str = "sqlite") -> "ProjectPaths":  # type: ignore[name-defined]
    from orchestrator.paths import ProjectPaths

    root = tmp_path / "proj"
    (root / ".orchestrator" / "state").mkdir(parents=True)
    (root / "scripts").mkdir(parents=True)
    (root / "scripts" / "task-start.sh").write_text("#!/bin/sh\nexit 0\n")
    (root / "tasks.json").write_text(
        json.dumps({"meta": {}, "phases": [], "tasks": []}), encoding="utf-8"
    )
    (root / ".orchestrator" / "config.yaml").write_text(
        f"state:\n  backend: {backend}\ndashboard:\n  summary_language: es\n",
        encoding="utf-8",
    )
    return ProjectPaths(
        project_root=root,
        project_id="proj",
        config_yaml=root / ".orchestrator" / "config.yaml",
        explicit_root=True,
        state_layout="legacy",
    )


def _client(paths):  # type: ignore[no-untyped-def]
    pytest.importorskip("fastapi")
    from fastapi.testclient import TestClient
    from orchestrator.dashboard.server import create_app

    return TestClient(create_app(paths=paths))


def _reset_caches() -> None:
    from orchestrator.state import _reset_backend_cache

    _reset_backend_cache()
    try:
        from orchestrator.state.sqlite_backend import _reset_schema_cache_for_tests

        _reset_schema_cache_for_tests()
    except ImportError:
        pass


def test_summary_unavailable_on_file_backend(tmp_path: Path) -> None:
    _reset_caches()
    try:
        client = _client(_make_project(tmp_path, backend="file"))
        r = client.get("/api/summary")
        assert r.status_code == 200
        payload = r.json()
        assert payload["available"] is False
        assert payload["summary"] is None
    finally:
        _reset_caches()


def test_summary_available_on_sqlite_backend(tmp_path: Path) -> None:
    pytest.importorskip("sqlite3")
    _reset_caches()
    try:
        paths = _make_project(tmp_path, backend="sqlite")
        # Bootstrap the SQLite backend so /api/summary can read it.
        from orchestrator.state.sqlite_backend import SqliteBackend

        db = paths.state_dir / "orch.db"
        be = SqliteBackend(
            db_path=db, project_id=paths.project_id, project_root=paths.project_root
        )
        be.bootstrap([])

        client = _client(paths)
        r = client.get("/api/summary")
        assert r.status_code == 200
        payload = r.json()
        assert payload["available"] is True
        summary = payload["summary"]
        assert summary["language"] == "es"
        assert isinstance(summary["text"], str)
        assert summary["text"]  # non-empty
        assert "generated_from" in summary
    finally:
        _reset_caches()
