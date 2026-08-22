"""Integration tests for `GET /api/events`.

The endpoint is a one-shot JSON companion to `/api/events/stream` — used by the
SPA to seed the Logs page with recent history before switching to the live
SSE tail. Tests here confirm:

    1. Empty state (no events files) → `{events: [], count: 0}`.
    2. `limit` query param actually caps the returned rows.
    3. `task_id` filter narrows the payload to a single task.

We reuse the same tiny-project fixture pattern used by the other
`test_dashboard_*` modules.
"""

from __future__ import annotations

import json
from pathlib import Path

import pytest


def _write_jsonl(path: Path, rows: list[dict]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("w", encoding="utf-8") as fh:
        for r in rows:
            fh.write(json.dumps(r) + "\n")


def _make_fixture_project(tmp_path: Path):
    from orchestrator.paths import ProjectPaths

    root = tmp_path / "proj"
    (root / "orchestrator" / "state").mkdir(parents=True)
    (root / "scripts").mkdir(parents=True)
    (root / "scripts" / "task-start.sh").write_text("#!/bin/sh\nexit 0\n")

    (root / "tasks.json").write_text(
        json.dumps({"meta": {}, "phases": [], "tasks": []}),
        encoding="utf-8",
    )

    return ProjectPaths(
        project_root=root,
        project_id="proj",
        config_yaml=root / "orchestrator" / "config.yaml",
        explicit_root=True,
        state_layout="legacy",
    )


def _client_or_skip(tmp_path: Path):
    pytest.importorskip("fastapi")
    from fastapi.testclient import TestClient
    from orchestrator.dashboard.server import create_app

    paths = _make_fixture_project(tmp_path)
    app = create_app(paths=paths)
    return TestClient(app), paths


# ---- 1. Empty state --------------------------------------------------------


def test_api_events_empty_state(tmp_path: Path) -> None:
    """No events-*.jsonl files → `{events: [], count: 0}`."""
    client, _ = _client_or_skip(tmp_path)
    r = client.get("/api/events")
    assert r.status_code == 200
    payload = r.json()
    assert payload == {"events": [], "count": 0}


# ---- 2. Limit is respected -------------------------------------------------


def test_api_events_limit_caps_row_count(tmp_path: Path) -> None:
    """`?limit=N` must return at most N rows, even when more exist on disk."""
    client, paths = _client_or_skip(tmp_path)
    # 25 events across a single file.
    rows = [
        {
            "event_type": "dispatch",
            "task_id": f"T-{i:02d}",
            "backend": "claude",
            "ts": f"2026-08-21T10:{i:02d}:00Z",
            "extra": {"pid": i, "cli_model": "claude-sonnet-4-6", "attempt": 1},
        }
        for i in range(25)
    ]
    _write_jsonl(paths.state_dir / "events-run1.jsonl", rows)

    r = client.get("/api/events?limit=5")
    assert r.status_code == 200
    payload = r.json()
    assert payload["count"] == len(payload["events"])
    assert payload["count"] <= 5
    # We wrote 25, capped at 5 → exactly 5.
    assert payload["count"] == 5


# ---- 3. task_id filter narrows the payload ---------------------------------


def test_api_events_task_id_filter(tmp_path: Path) -> None:
    """`?task_id=X` must only return rows where `task_id == X`."""
    client, paths = _client_or_skip(tmp_path)
    _write_jsonl(paths.state_dir / "events-run1.jsonl", [
        {"event_type": "dispatch", "task_id": "T-A", "backend": "claude",
         "ts": "2026-08-21T10:00:00Z",
         "extra": {"pid": 1, "cli_model": "claude-sonnet-4-6", "attempt": 1}},
        {"event_type": "success", "task_id": "T-A", "backend": "claude",
         "ts": "2026-08-21T10:05:00Z",
         "extra": {"cost_usd": 0.10, "duration_s": 300.0}},
        {"event_type": "dispatch", "task_id": "T-B", "backend": "opencode",
         "ts": "2026-08-21T10:10:00Z",
         "extra": {"pid": 2, "cli_model": "glm-5.1", "attempt": 1}},
        {"event_type": "success", "task_id": "T-B", "backend": "opencode",
         "ts": "2026-08-21T10:15:00Z",
         "extra": {"cost_usd": 0.0, "duration_s": 300.0}},
    ])

    r = client.get("/api/events?task_id=T-A")
    assert r.status_code == 200
    payload = r.json()
    assert payload["count"] == 2
    assert payload["count"] == len(payload["events"])
    assert {e["task_id"] for e in payload["events"]} == {"T-A"}
    # And confirm shape — each row is the formatted event (has severity + human).
    for e in payload["events"]:
        assert "ts" in e
        assert "task_id" in e
        assert "event_type" in e
        assert "severity" in e
        assert "human" in e
        assert "raw" in e
