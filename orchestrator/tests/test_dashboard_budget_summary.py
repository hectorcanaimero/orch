"""Tests for GET /api/budget/summary (Sprint G-5).

Cross-references configured per-provider token budgets (budgets.yaml) with
rolling-window tokens used (spend-*.jsonl) + USD spent. A WIDE window (720h) is
used deliberately so the in-window assertions don't race a tight time boundary
(unlike the two known flaky tests that seed at datetime('now','-1 days')).
"""
from __future__ import annotations

import json
from datetime import datetime, timezone
from pathlib import Path

import pytest


def _write_jsonl(path: Path, rows: list[dict]) -> None:
    path.write_text("\n".join(json.dumps(r) for r in rows) + "\n", encoding="utf-8")


def _make_project(tmp_path: Path, *, with_budgets: bool) -> "ProjectPaths":  # type: ignore[name-defined]
    from orchestrator.paths import ProjectPaths

    root = tmp_path / "proj"
    state = root / ".orchestrator" / "state"
    state.mkdir(parents=True)
    (root / "scripts").mkdir(parents=True)
    (root / "scripts" / "task-start.sh").write_text("#!/bin/sh\nexit 0\n")
    (root / "tasks.json").write_text(
        json.dumps({"meta": {}, "phases": [], "tasks": []}), encoding="utf-8"
    )
    (root / ".orchestrator" / "config.yaml").write_text(
        "state:\n  backend: file\n", encoding="utf-8"
    )

    if with_budgets:
        # 720h window so a spend written "today" is comfortably in-window.
        (root / ".orchestrator" / "budgets.yaml").write_text(
            "presets:\n"
            "  conservative:\n"
            "    claude:\n"
            "      window_hours: 720\n"
            "      token_budget: 10000\n"
            "      threshold_pct: 80\n"
            "    codex:\n"
            "      window_hours: 720\n"
            "      token_budget: 5000\n"
            "      threshold_pct: 80\n",
            encoding="utf-8",
        )
        today = datetime.now(timezone.utc).date().isoformat()
        now_ts = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
        _write_jsonl(state / f"spend-{today}.jsonl", [
            {"ts": now_ts, "task_id": "T1", "backend": "claude",
             "model": "claude-sonnet-4-6", "tokens_in": 1500, "tokens_out": 1000,
             "cost_usd": 3.25, "duration_s": 10.0},
        ])

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


def test_budget_summary_unavailable_without_budgets_yaml(tmp_path: Path) -> None:
    client = _client(_make_project(tmp_path, with_budgets=False))
    r = client.get("/api/budget/summary")
    assert r.status_code == 200
    payload = r.json()
    assert payload["available"] is False
    assert payload["rows"] == []


def test_budget_summary_pairs_limit_with_used_and_cost(tmp_path: Path) -> None:
    client = _client(_make_project(tmp_path, with_budgets=True))
    r = client.get("/api/budget/summary")
    assert r.status_code == 200
    payload = r.json()
    assert payload["available"] is True
    rows = {row["provider"]: row for row in payload["rows"]}

    # claude: 1500 + 1000 = 2500 tokens used against a 10000 budget = 25%.
    claude = rows["claude"]
    assert claude["token_budget"] == 10000
    assert claude["tokens_used"] == 2500
    assert claude["pct"] == 25
    assert claude["cost_usd"] == 3.25
    assert claude["over_threshold"] is False

    # codex: configured but no spend → zeroed, not dropped.
    codex = rows["codex"]
    assert codex["tokens_used"] == 0
    assert codex["pct"] == 0
    assert codex["cost_usd"] == 0.0
