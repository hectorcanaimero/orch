"""Tests for `orchestrator.observability.build_status_snapshot`.

We seed a small project on both backends via the `backend` fixture and
assert the aggregate shape matches what `orch status` / `orch graph`
downstream will consume.
"""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from orchestrator.models import EventEntry, SpendEntry
from orchestrator.observability import build_status_snapshot
from orchestrator.state import FileBackend, _reset_backend_cache
from orchestrator.state.interface import StateBackend
from orchestrator.state.sqlite_backend import SqliteBackend


def _write_tasks_json(root: Path) -> None:
    (root / "tasks.json").write_text(json.dumps({
        "tasks": [
            {
                "id": "T-A", "phase": 1, "title": "Task A", "description": "",
                "model": "opencode/glm-5.1", "reason": "", "status": "todo",
                "dependencies": [], "estimateHours": 0.1, "files": [],
                "specRef": "", "comments": [],
            },
            {
                "id": "T-B", "phase": 2, "title": "Task B", "description": "",
                "model": "opencode/glm-5.1", "reason": "", "status": "todo",
                "dependencies": ["T-A"], "estimateHours": 0.1, "files": [],
                "specRef": "", "comments": [],
            },
        ],
    }))


def _write_router_yaml(root: Path) -> None:
    yaml_dir = root / "orchestrator"
    yaml_dir.mkdir(parents=True, exist_ok=True)
    (yaml_dir / "model_router.yaml").write_text(
        "opencode/glm-5.1:\n"
        "  backend: opencode\n"
        "  cli_model: glm-5.1\n"
        "  tier: cheap\n"
    )


class _FakePaths:
    """Duck-typed ProjectPaths that lets us pin `state_dir` to the fixture's dir.

    `get_backend()` only reads `state_dir`, `project_id`, `project_root` from
    the paths object, so a lightweight namespace suffices — we don't need
    the legacy vs namespaced computation.
    """

    def __init__(
        self,
        project_root: Path,
        project_id: str,
        state_dir: Path,
    ) -> None:
        self.project_root = project_root
        self.project_id = project_id
        self.state_dir = state_dir
        self.tasks_json = project_root / "tasks.json"
        self.router_yaml = project_root / "orchestrator" / "model_router.yaml"
        self.config_yaml = project_root / "config.yaml"


def _paths_and_cfg(
    backend: StateBackend, tmp_path: Path
) -> tuple[_FakePaths, dict]:
    """Build fake paths + `cfg` that match the fixture backend.

    `get_backend()` in observability re-derives the backend from `cfg`, so
    tests must pass a cfg that yields the SAME backend the fixture writes
    to. For sqlite we point `sqlite_path` at the fixture's DB. The state
    dir is pinned to the fixture's `state_dir` so the freshly-instantiated
    file backend sees the same JSONL files the fixture wrote.
    """
    project_id = getattr(backend, "project_id", None) or f"pytest-{tmp_path.name}"
    # Both backends in the fixture use tmp_path/'state' — reuse it.
    fixture_state = tmp_path / "state"
    paths = _FakePaths(
        project_root=tmp_path,
        project_id=project_id,
        state_dir=fixture_state,
    )
    if isinstance(backend, SqliteBackend):
        cfg = {
            "state": {
                "backend": "sqlite",
                "sqlite_path": str(backend.db_path),
            }
        }
    else:
        cfg = {}
    _reset_backend_cache()
    return paths, cfg


@pytest.fixture
def seeded_backend(backend: StateBackend, tmp_path: Path) -> StateBackend:
    """Seed tasks.json + router.yaml + one spend row for T-A."""
    _write_tasks_json(tmp_path)
    _write_router_yaml(tmp_path)
    # Sync backend with the DAG so both file + sqlite know the tasks.
    from orchestrator.state import load_tasks

    backend.bootstrap(load_tasks(tmp_path / "tasks.json"))

    backend.append_spend(SpendEntry(
        ts="2026-08-20T12:00:00Z",
        task_id="T-A",
        backend="opencode",
        model="glm-5.1",
        tokens_in=10,
        tokens_out=5,
        cost_usd=0.42,
        duration_s=1.0,
    ))
    return backend


def test_snapshot_has_expected_top_level_keys(seeded_backend: StateBackend, tmp_path: Path) -> None:
    paths, cfg = _paths_and_cfg(seeded_backend, tmp_path)
    snap = build_status_snapshot(paths, cfg=cfg)
    for key in ("project", "totals", "cost", "latest_run", "tasks", "filters"):
        assert key in snap, f"missing key: {key!r}"


def test_snapshot_task_rows_carry_backend_and_cost(
    seeded_backend: StateBackend, tmp_path: Path
) -> None:
    paths, cfg = _paths_and_cfg(seeded_backend, tmp_path)
    snap = build_status_snapshot(paths, cfg=cfg)
    by_id = {r["id"]: r for r in snap["tasks"]}
    assert set(by_id.keys()) == {"T-A", "T-B"}
    assert by_id["T-A"]["backend"] == "opencode"
    assert by_id["T-A"]["cli_model"] == "glm-5.1"
    # Cost from the seeded spend row must land on T-A only.
    assert by_id["T-A"]["cost_usd"] == 0.42
    assert by_id["T-B"]["cost_usd"] == 0.0


def test_snapshot_last_event_populates_after_append(
    seeded_backend: StateBackend, tmp_path: Path
) -> None:
    seeded_backend.create_run(run_id="r-obs", mode="auto")
    seeded_backend.append_event("r-obs", EventEntry(
        event_type="dispatch", task_id="T-A", backend="opencode",
        ts="2026-08-20T13:00:00Z",
    ))
    paths, cfg = _paths_and_cfg(seeded_backend, tmp_path)
    snap = build_status_snapshot(paths, cfg=cfg)
    by_id = {r["id"]: r for r in snap["tasks"]}
    assert by_id["T-A"]["last_event"] is not None
    assert by_id["T-A"]["last_event"]["event_type"] == "dispatch"
    assert by_id["T-B"]["last_event"] is None


def test_snapshot_only_filter_narrows_tasks_but_not_totals(
    seeded_backend: StateBackend, tmp_path: Path
) -> None:
    paths, cfg = _paths_and_cfg(seeded_backend, tmp_path)
    snap = build_status_snapshot(paths, cfg=cfg, only="T-A")
    ids = [r["id"] for r in snap["tasks"]]
    assert ids == ["T-A"]
    # Totals stay project-wide so status headers don't lie.
    assert snap["totals"]["_total"] == 2


def test_snapshot_status_filter(seeded_backend: StateBackend, tmp_path: Path) -> None:
    # All tasks are `todo` after bootstrap → filtering by `done` yields none.
    paths, cfg = _paths_and_cfg(seeded_backend, tmp_path)
    snap = build_status_snapshot(paths, cfg=cfg, status_filter={"done"})
    assert snap["tasks"] == []


def test_snapshot_reports_backend_kind(
    seeded_backend: StateBackend, tmp_path: Path
) -> None:
    paths, cfg = _paths_and_cfg(seeded_backend, tmp_path)
    snap = build_status_snapshot(paths, cfg=cfg)
    expected = "file" if isinstance(seeded_backend, FileBackend) else "sqlite"
    assert snap["project"]["backend"] == expected
