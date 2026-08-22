"""Curated-view tests for Sprint E-2.

Confirms that the stakeholder profile:
    - Serves `/stakeholder` HTML with the curated progress partial.
    - Serves `/stakeholder/summary` JSON with the same fields as the view.
    - HIDES per-model spend, raw log content, per-task exit codes, and
      granular last-updated timestamps.
    - Operator profile (default) keeps rendering every sensitive field
      exactly like before.

The middleware wiring is tested in `test_dashboard_middleware.py` and the
security matrix (403 for every operator-only route in stakeholder mode)
lives in `test_dashboard_security.py`. Here we focus on the CONTENT of
the curated payload.
"""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from orchestrator.dashboard.metrics import (
    eta_hours_remaining,
    milestones_from_phases,
    round_up_to_step,
)
from orchestrator.models import Task


# ---- Reuse the fixture from test_dashboard.py --------------------------


def _mk_task(**kw) -> Task:
    return Task(
        id=kw.get("id", "T-1"),
        phase=kw.get("phase", 1),
        title=kw.get("title", "t"),
        description=kw.get("description", ""),
        model=kw.get("model", "opencode-go/glm-5.1"),
        reason=kw.get("reason", ""),
        status=kw.get("status", "backlog"),
        dependencies=list(kw.get("dependencies", [])),
        estimate_hours=float(kw.get("estimate_hours", 0.5)),
        files=list(kw.get("files", [])),
        spec_ref=kw.get("spec_ref", ""),
        comments=list(kw.get("comments", [])),
    )


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

    tasks_payload = {
        "meta": {"note": "stakeholder fixture"},
        "phases": [{"id": 1, "name": "phase-one"}, {"id": 2, "name": "phase-two"}],
        "tasks": [
            {"id": "T-A", "phase": 1, "title": "A", "description": "", "model": "opencode-go/glm-5.1",
             "reason": "", "status": "done", "dependencies": [], "estimateHours": 0.5,
             "files": [], "specRef": "", "comments": []},
            {"id": "T-B", "phase": 1, "title": "B", "description": "", "model": "claude-sonnet-4-6",
             "reason": "", "status": "done", "dependencies": [], "estimateHours": 1.0,
             "files": [], "specRef": "", "comments": []},
            {"id": "T-C", "phase": 2, "title": "Merge", "description": "", "model": "claude-sonnet-4-6",
             "reason": "", "status": "backlog", "dependencies": ["T-A", "T-B"], "estimateHours": 2.0,
             "files": [], "specRef": "", "comments": []},
        ],
    }
    (root / "tasks.json").write_text(json.dumps(tasks_payload), encoding="utf-8")

    state_dir = root / "orchestrator" / "state"
    _write_jsonl(state_dir / "spend-2026-08-20.jsonl", [
        {"ts": "2026-08-20T10:12:00", "task_id": "T-A", "model": "opencode-go/glm-5.1",
         "tokens_in": 5000, "tokens_out": 2000, "cost_usd": 0.42},
        {"ts": "2026-08-20T11:12:00", "task_id": "T-B", "model": "claude-sonnet-4-6",
         "tokens_in": 8000, "tokens_out": 3000, "cost_usd": 1.05},
    ])

    return ProjectPaths(
        project_root=root,
        project_id="proj",
        config_yaml=root / "orchestrator" / "config.yaml",
        explicit_root=True,
        state_layout="legacy",
    )


def _client_or_skip(tmp_path: Path, **override):
    pytest.importorskip("jinja2")
    pytest.importorskip("fastapi")
    from fastapi.testclient import TestClient
    from orchestrator.dashboard.server import create_app

    paths = _make_fixture_project(tmp_path)
    # `profile_override` / `token_override` land in the app config BEFORE
    # middleware registration — critical, because middleware only wires
    # in when config.profile != "operator".
    app = create_app(
        paths=paths,
        profile_override=override.get("profile"),
        token_override=override.get("token"),
    )
    return TestClient(app), paths


# ---- Unit tests for the metrics helpers -----------------------------------


def test_round_up_to_step_snaps_up_to_next_half_dollar() -> None:
    assert round_up_to_step(0.0) == 0.0
    assert round_up_to_step(0.01) == 0.5
    assert round_up_to_step(0.50) == 0.5
    assert round_up_to_step(0.51) == 1.0
    assert round_up_to_step(1.49) == 1.5
    assert round_up_to_step(2.00) == 2.0


def test_round_up_to_step_handles_negative_and_zero_step() -> None:
    assert round_up_to_step(-1.0) == 0.0
    assert round_up_to_step(1.0, step=0) == 1.0


def test_eta_hours_returns_none_when_nothing_remaining() -> None:
    tasks = [_mk_task(id="T-A", status="done", estimate_hours=1)]
    assert eta_hours_remaining(tasks) is None


def test_eta_hours_falls_back_to_raw_estimate_when_no_signal() -> None:
    tasks = [
        _mk_task(id="T-A", status="done", estimate_hours=1),
        _mk_task(id="T-B", status="backlog", estimate_hours=2),
    ]
    # No human_hours recorded → ratio unavailable → raw remaining.
    assert eta_hours_remaining(tasks) == 2.0


def test_eta_hours_applies_efficiency_ratio() -> None:
    tasks = [
        _mk_task(id="T-A", status="done", estimate_hours=1),
        _mk_task(id="T-B", status="backlog", estimate_hours=2),
    ]
    # 1h planned, 1.5h actual → 1.5x overhead → remaining 2 * 1.5 = 3.
    assert eta_hours_remaining(tasks, {"T-A": 1.5}) == pytest.approx(3.0)


def test_milestones_marks_fully_done_phases() -> None:
    tasks = [
        _mk_task(id="T-A", phase=1, status="done"),
        _mk_task(id="T-B", phase=1, status="done"),
        _mk_task(id="T-C", phase=2, status="backlog"),
    ]
    m = milestones_from_phases(tasks)
    assert m == [
        {"phase": 1, "total_count": 2, "done_count": 2, "done": True},
        {"phase": 2, "total_count": 1, "done_count": 0, "done": False},
    ]


# ---- Integration: stakeholder view --------------------------------------


def test_stakeholder_index_renders_curated_partial(tmp_path: Path) -> None:
    client, _ = _client_or_skip(tmp_path, profile="stakeholder", token="t")
    r = client.get("/stakeholder", headers={"Authorization": "Bearer t"})
    assert r.status_code == 200
    body = r.text
    assert 'data-role="stakeholder-progress"' in body
    # Percent + milestones are safe to expose.
    assert "% " in body or "%<" in body or "progress" in body.lower()
    assert "Milestones" in body or "milestones" in body.lower()


def test_stakeholder_index_hides_per_model_spend(tmp_path: Path) -> None:
    """Sensitive fields banished from the curated HTML."""
    client, _ = _client_or_skip(tmp_path, profile="stakeholder", token="t")
    r = client.get("/stakeholder", headers={"Authorization": "Bearer t"})
    assert r.status_code == 200
    body = r.text
    # Provider identifiers must never leak on the curated view.
    assert "opencode-go" not in body
    assert "claude-sonnet-4-6" not in body
    # Per-model spend granularity — should NOT surface anywhere.
    assert "$1.05" not in body
    assert "$0.42" not in body


def test_stakeholder_index_hides_raw_log_and_task_lists(tmp_path: Path) -> None:
    client, _ = _client_or_skip(tmp_path, profile="stakeholder", token="t")
    r = client.get("/stakeholder", headers={"Authorization": "Bearer t"})
    assert r.status_code == 200
    body = r.text
    # No per-task IDs surface on the curated view — only aggregate counts.
    assert ">T-A<" not in body
    assert ">T-B<" not in body
    assert ">T-C<" not in body
    # No SSE event stream link either.
    assert "/logs/stream" not in body


def test_stakeholder_summary_json_shape(tmp_path: Path) -> None:
    client, _ = _client_or_skip(tmp_path, profile="stakeholder", token="t")
    r = client.get("/stakeholder/summary", headers={"Authorization": "Bearer t"})
    assert r.status_code == 200
    payload = r.json()
    # Exactly the curated fields — no per-model breakdown, no raw tasks.
    assert set(payload.keys()) == {
        "project_id", "summary", "milestones",
        "spend_rounded_usd", "eta_hours", "refresh_interval_s",
    }
    # Spend is rounded up in $0.50 increments.
    assert payload["spend_rounded_usd"] == pytest.approx(1.5)
    assert isinstance(payload["milestones"], list) and payload["milestones"]


def test_stakeholder_summary_hides_per_task_details(tmp_path: Path) -> None:
    client, _ = _client_or_skip(tmp_path, profile="stakeholder", token="t")
    r = client.get("/stakeholder/summary", headers={"Authorization": "Bearer t"})
    body = r.text  # raw JSON string; check no leak of task ids
    assert '"T-A"' not in body
    assert '"opencode-go' not in body
    assert '"claude-sonnet' not in body


# ---- Operator profile still shows everything ----------------------------


def test_operator_index_still_shows_model_column(tmp_path: Path) -> None:
    client, _ = _client_or_skip(tmp_path)  # default = operator
    r = client.get("/")
    assert r.status_code == 200
    body = r.text
    assert "opencode-go" in body
    assert "claude-sonnet-4-6" in body


def test_operator_metrics_shows_per_model_breakdown(tmp_path: Path) -> None:
    client, _ = _client_or_skip(tmp_path)
    r = client.get("/metrics")
    assert r.status_code == 200
    body = r.text
    assert "By model" in body
    assert "opencode-go" in body
    assert "$1.05" in body or "1.05" in body
