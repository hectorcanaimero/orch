"""Tests for GET /api/graph — visual DAG endpoint (issue #13).

Validates:
  - Response shape: nodes + edges lists with correct fields.
  - Node status, phase, label, and on_critical_path fields.
  - Edges derived from task dependencies.
  - Empty project returns nodes=[] edges=[].
  - Operator profile: 200.
  - Stakeholder profile: 403.
"""

from __future__ import annotations

import json
from pathlib import Path

import pytest


# ---- Fixture helpers -------------------------------------------------------


def _write_jsonl(path: Path, rows: list[dict]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("w", encoding="utf-8") as fh:
        for r in rows:
            fh.write(json.dumps(r) + "\n")


def _make_project(tmp_path: Path, tasks: list[dict] | None = None):
    """Build a minimal project fixture and return its ProjectPaths."""
    from orchestrator.paths import ProjectPaths

    root = tmp_path / "proj"
    (root / "orchestrator" / "state").mkdir(parents=True)
    (root / "scripts").mkdir(parents=True)
    (root / "scripts" / "task-start.sh").write_text("#!/bin/sh\nexit 0\n")

    if tasks is None:
        tasks = [
            {
                "id": "T-1",
                "phase": 1,
                "title": "Setup",
                "description": "",
                "model": "claude-sonnet-4-6",
                "reason": "",
                "status": "done",
                "dependencies": [],
                "estimateHours": 0.5,
                "files": [],
                "specRef": "",
                "comments": [],
            },
            {
                "id": "T-2",
                "phase": 1,
                "title": "Implement feature",
                "description": "",
                "model": "claude-sonnet-4-6",
                "reason": "",
                "status": "in_progress",
                "dependencies": ["T-1"],
                "estimateHours": 2.0,
                "files": [],
                "specRef": "",
                "comments": [],
            },
            {
                "id": "T-3",
                "phase": 2,
                "title": "Review",
                "description": "",
                "model": "claude-sonnet-4-6",
                "reason": "",
                "status": "backlog",
                "dependencies": ["T-2"],
                "estimateHours": 1.0,
                "files": [],
                "specRef": "",
                "comments": [],
            },
        ]

    payload = {
        "phases": [{"id": 1, "name": "phase-one"}, {"id": 2, "name": "phase-two"}],
        "tasks": tasks,
    }
    (root / "tasks.json").write_text(json.dumps(payload), encoding="utf-8")

    return ProjectPaths(
        project_root=root,
        project_id="proj",
        config_yaml=root / "orchestrator" / "config.yaml",
        explicit_root=True,
        state_layout="legacy",
    )


def _client(tmp_path: Path, **override):
    pytest.importorskip("fastapi")
    from fastapi.testclient import TestClient
    from orchestrator.dashboard.server import create_app

    paths = _make_project(tmp_path, tasks=override.pop("tasks", None))
    app = create_app(
        paths=paths,
        profile_override=override.get("profile"),
        token_override=override.get("token"),
    )
    return TestClient(app)


# ---- Response shape tests --------------------------------------------------


def test_graph_returns_200_for_operator(tmp_path: Path) -> None:
    client = _client(tmp_path)
    r = client.get("/api/graph")
    assert r.status_code == 200, r.text


def test_graph_response_has_nodes_and_edges(tmp_path: Path) -> None:
    client = _client(tmp_path)
    r = client.get("/api/graph")
    data = r.json()
    assert "nodes" in data
    assert "edges" in data
    assert isinstance(data["nodes"], list)
    assert isinstance(data["edges"], list)


def test_graph_node_fields(tmp_path: Path) -> None:
    client = _client(tmp_path)
    r = client.get("/api/graph")
    nodes = r.json()["nodes"]
    assert len(nodes) == 3

    t1 = next(n for n in nodes if n["id"] == "T-1")
    assert t1["label"] == "Setup"
    assert t1["status"] == "done"
    assert t1["phase"] == 1
    assert isinstance(t1["on_critical_path"], bool)


def test_graph_edges_derived_from_dependencies(tmp_path: Path) -> None:
    client = _client(tmp_path)
    r = client.get("/api/graph")
    edges = r.json()["edges"]

    # T-1 → T-2 (T-2 depends on T-1)
    assert {"source": "T-1", "target": "T-2"} in edges
    # T-2 → T-3 (T-3 depends on T-2)
    assert {"source": "T-2", "target": "T-3"} in edges


def test_graph_edge_count_matches_total_dependencies(tmp_path: Path) -> None:
    client = _client(tmp_path)
    r = client.get("/api/graph")
    # T-1: 0 deps, T-2: 1 dep, T-3: 1 dep → 2 edges total
    assert len(r.json()["edges"]) == 2


def test_graph_node_status_values(tmp_path: Path) -> None:
    client = _client(tmp_path)
    r = client.get("/api/graph")
    by_id = {n["id"]: n for n in r.json()["nodes"]}
    assert by_id["T-1"]["status"] == "done"
    assert by_id["T-2"]["status"] == "in_progress"
    assert by_id["T-3"]["status"] == "backlog"


def test_graph_phase_values(tmp_path: Path) -> None:
    client = _client(tmp_path)
    r = client.get("/api/graph")
    by_id = {n["id"]: n for n in r.json()["nodes"]}
    assert by_id["T-1"]["phase"] == 1
    assert by_id["T-2"]["phase"] == 1
    assert by_id["T-3"]["phase"] == 2


# ---- Critical path field ---------------------------------------------------


def test_graph_on_critical_path_is_bool(tmp_path: Path) -> None:
    client = _client(tmp_path)
    r = client.get("/api/graph")
    for node in r.json()["nodes"]:
        assert isinstance(node["on_critical_path"], bool), (
            f"node {node['id']!r} has non-bool on_critical_path"
        )


# ---- Empty project ---------------------------------------------------------


def test_graph_empty_project_returns_empty_lists(tmp_path: Path) -> None:
    client = _client(tmp_path, tasks=[])
    r = client.get("/api/graph")
    assert r.status_code == 200
    data = r.json()
    assert data["nodes"] == []
    assert data["edges"] == []


# ---- Stakeholder gating ----------------------------------------------------


def test_graph_returns_403_in_stakeholder_mode(tmp_path: Path) -> None:
    client = _client(tmp_path, profile="stakeholder", token="s3cr3t")
    r = client.get("/api/graph", headers={"Authorization": "Bearer s3cr3t"})
    assert r.status_code == 403, (
        f"Expected 403 for stakeholder on /api/graph, got {r.status_code}"
    )


def test_graph_returns_401_without_token_in_stakeholder_mode(
    tmp_path: Path,
) -> None:
    client = _client(tmp_path, profile="stakeholder", token="s3cr3t")
    r = client.get("/api/graph")
    assert r.status_code == 401


# ---- Label fallback: task with empty title ---------------------------------


def test_graph_label_falls_back_to_id_when_title_empty(tmp_path: Path) -> None:
    tasks = [
        {
            "id": "T-X",
            "phase": 1,
            "title": "",
            "description": "",
            "model": "claude-sonnet-4-6",
            "reason": "",
            "status": "backlog",
            "dependencies": [],
            "estimateHours": 0.5,
            "files": [],
            "specRef": "",
            "comments": [],
        }
    ]
    client = _client(tmp_path, tasks=tasks)
    r = client.get("/api/graph")
    node = r.json()["nodes"][0]
    # When title is falsy, label falls back to the task id
    assert node["label"] == "T-X"
