"""Issue #33 — Dashboard shows stale statuses when using SQLite backend.

With `tasks_json_precedence: deps-only`, orch never writes task status back
to tasks.json — only to SQLite.  Before the fix, `_load_project_view()` only
read from tasks.json, so the dashboard always showed the frozen pre-run state.

The fix overlays `get_all_task_status()` from the configured backend on top of
the tasks loaded from tasks.json.
"""

from __future__ import annotations

import json
from pathlib import Path

import pytest


def _write_config(root: Path, backend: str = "sqlite") -> None:
    cfg_dir = root / "orchestrator"
    cfg_dir.mkdir(parents=True, exist_ok=True)
    (cfg_dir / "config.yaml").write_text(
        f"state:\n  backend: {backend}\ntasks_json_precedence: deps-only\n",
        encoding="utf-8",
    )


def _make_project(tmp_path: Path, *, tasks_statuses: dict[str, str]) -> "ProjectPaths":  # type: ignore[name-defined]
    from orchestrator.paths import ProjectPaths

    root = tmp_path / "proj"
    (root / "orchestrator" / "state").mkdir(parents=True)
    (root / "scripts").mkdir(parents=True)
    (root / "scripts" / "task-start.sh").write_text("#!/bin/sh\nexit 0\n")

    tasks_payload = {
        "phases": [{"id": 1, "name": "alpha"}, {"id": 2, "name": "beta"}],
        "tasks": [
            {
                "id": tid, "phase": 1, "title": tid, "description": "",
                "model": "claude-sonnet-4-6", "reason": "",
                "status": status,  # frozen status in tasks.json
                "dependencies": [], "estimateHours": 1.0,
                "files": [], "specRef": "", "comments": [],
            }
            for tid, status in tasks_statuses.items()
        ],
    }
    (root / "tasks.json").write_text(json.dumps(tasks_payload), encoding="utf-8")

    paths = ProjectPaths(
        project_root=root,
        project_id="proj",
        config_yaml=root / "orchestrator" / "config.yaml",
        explicit_root=True,
        state_layout="legacy",
    )
    return paths


def _bootstrap_sqlite(paths: "ProjectPaths", live_statuses: dict[str, str]) -> None:  # type: ignore[name-defined]
    """Seed the SQLite backend with live statuses (simulating in-flight run)."""
    from orchestrator.models import Task
    from orchestrator.state.sqlite_backend import SqliteBackend

    db = paths.state_dir / "orch.db"
    be = SqliteBackend(
        db_path=db, project_id=paths.project_id, project_root=paths.project_root
    )
    tasks = [
        Task(
            id=tid, phase=1, title=tid, description="",
            model="claude-sonnet-4-6", reason="",
            status=status,  # type: ignore[arg-type]
            dependencies=[], estimate_hours=1.0, files=[], spec_ref="", comments=[],
        )
        for tid, status in live_statuses.items()
    ]
    be.bootstrap(tasks)
    # bootstrap uses INSERT OR IGNORE; tasks were seeded with frozen status.
    # Now update to the live statuses.
    for tid, status in live_statuses.items():
        be.set_task_status(
            task_id=tid,
            status=status,  # type: ignore[arg-type]
            author="orch",
            note="live run",
            ts="2026-08-23T09:00:00",
        )


def _client(paths: "ProjectPaths") -> "TestClient":  # type: ignore[name-defined]
    pytest.importorskip("fastapi")
    from fastapi.testclient import TestClient
    from orchestrator.dashboard.server import create_app

    app = create_app(paths=paths)
    return TestClient(app)


# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------


def test_api_tasks_reflects_sqlite_live_status(tmp_path: Path) -> None:
    """With sqlite backend, /api/tasks must show the live DB status."""
    frozen = {"T-1": "backlog", "T-2": "backlog"}
    live = {"T-1": "done", "T-2": "in-progress"}

    paths = _make_project(tmp_path, tasks_statuses=frozen)
    _write_config(paths.project_root)
    _bootstrap_sqlite(paths, live_statuses=live)

    client = _client(paths)
    r = client.get("/api/tasks")
    assert r.status_code == 200

    by_id = {t["id"]: t["status"] for t in r.json()["tasks"]}
    assert by_id["T-1"] == "done", f"Expected done, got {by_id['T-1']}"
    assert by_id["T-2"] == "in-progress", f"Expected in-progress, got {by_id['T-2']}"


def test_api_tasks_summary_counts_use_live_status(tmp_path: Path) -> None:
    """Summary counts (done/in_progress/backlog) must reflect the DB state."""
    frozen = {"T-1": "backlog", "T-2": "backlog", "T-3": "backlog"}
    live = {"T-1": "done", "T-2": "in-progress", "T-3": "backlog"}

    paths = _make_project(tmp_path, tasks_statuses=frozen)
    _write_config(paths.project_root)
    _bootstrap_sqlite(paths, live_statuses=live)

    client = _client(paths)
    r = client.get("/api/tasks")
    assert r.status_code == 200

    summary = r.json()["summary"]
    assert summary["done"] == 1
    assert summary["in_progress"] == 1
    assert summary["backlog"] == 1


def test_api_tasks_falls_back_gracefully_when_no_config(tmp_path: Path) -> None:
    """If config.yaml is absent, tasks.json statuses are used (no crash)."""
    frozen = {"T-1": "backlog"}

    paths = _make_project(tmp_path, tasks_statuses=frozen)
    # No config.yaml written → file backend assumed, no overlay.

    client = _client(paths)
    r = client.get("/api/tasks")
    assert r.status_code == 200
    by_id = {t["id"]: t["status"] for t in r.json()["tasks"]}
    assert by_id["T-1"] == "backlog"


def test_file_backend_no_regression(tmp_path: Path) -> None:
    """File backend path must still work correctly (statuses from tasks.json)."""
    frozen = {"T-1": "done", "T-2": "backlog"}

    paths = _make_project(tmp_path, tasks_statuses=frozen)
    _write_config(paths.project_root, backend="file")

    client = _client(paths)
    r = client.get("/api/tasks")
    assert r.status_code == 200

    by_id = {t["id"]: t["status"] for t in r.json()["tasks"]}
    assert by_id["T-1"] == "done"
    assert by_id["T-2"] == "backlog"


def test_stakeholder_summary_counts_reflect_sqlite_live_status(tmp_path: Path) -> None:
    """/stakeholder/summary task counters must also use the live DB statuses."""
    frozen = {"T-1": "backlog", "T-2": "backlog"}
    live = {"T-1": "done", "T-2": "done"}

    paths = _make_project(tmp_path, tasks_statuses=frozen)
    _write_config(paths.project_root)
    _bootstrap_sqlite(paths, live_statuses=live)

    pytest.importorskip("fastapi")
    from fastapi.testclient import TestClient
    from orchestrator.dashboard.server import create_app

    app = create_app(paths=paths, profile_override="stakeholder", token_override="tok")
    client = TestClient(app)

    r = client.get("/stakeholder/summary", headers={"Authorization": "Bearer tok"})
    assert r.status_code == 200

    payload = r.json()
    # Both tasks are now done — milestones should reflect that.
    assert payload["summary"]["done"] == 2
    assert payload["summary"]["backlog"] == 0
