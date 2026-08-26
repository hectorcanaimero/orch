"""Unit + integration tests for `orchestrator.dashboard`.

Covers:
    - `pricing.PricingTable` (fallback when cost_usd==0, project overrides)
    - `metrics.metrics_by_model` (aggregation shape)
    - `metrics.metrics_by_day` (date bucketing)
    - `metrics.human_hours_by_task` (event pairing)
    - `metrics.parallelizable_tasks` (dep filter)
    - `metrics.project_summary` (header counts)
    - `log_stream.format_event` (severity + human formatting)
    - `server.create_app` (smoke test)
    - `GET /api/tasks` shape

We construct a self-contained fixture project under `tmp_path` so tests
don't depend on the real orchestrator state directory.

Jinja HTML routes (`/`, `/kanban`, `/metrics`, `/logs`, `/partials/*`,
`/stakeholder`) were removed when the SPA became the sole UI — this
module no longer exercises HTML rendering.
"""

from __future__ import annotations

import json
import textwrap
from pathlib import Path

import pytest

from orchestrator.dashboard.metrics import (
    downstream_impact,
    events_for_task,
    human_hours_by_task,
    metrics_by_day,
    metrics_by_model,
    parallelizable_tasks,
    phase_counts,
    project_summary,
    read_all_events,
    read_all_spends,
    total_cost,
)
from orchestrator.dashboard.log_stream import format_event, load_recent_events
from orchestrator.dashboard.pricing import PricingTable
from orchestrator.models import Task
from orchestrator.paths import ProjectPaths


# ---- helpers ---------------------------------------------------------------


def _mk_task(**kw) -> Task:
    """Build a `Task` with sane defaults for every required field."""
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


def _make_fixture_project(tmp_path: Path) -> ProjectPaths:
    """Set up a tiny project layout the dashboard can point at."""
    root = tmp_path / "proj"
    (root / ".orchestrator" / "state").mkdir(parents=True)
    (root / "scripts").mkdir(parents=True)
    # `paths.ensure_valid` needs task-start.sh — write a stub.
    (root / "scripts" / "task-start.sh").write_text("#!/bin/sh\nexit 0\n")

    # 3-task DAG: T-A done, T-B done, T-C backlog with deps on T-A + T-B.
    tasks_payload = {
        "meta": {"note": "dashboard test fixture"},
        "phases": [{"id": 1, "name": "smoke"}],
        "tasks": [
            {
                "id": "T-A", "phase": 1, "title": "Root A", "description": "d",
                "model": "opencode-go/glm-5.1", "reason": "r", "status": "done",
                "dependencies": [], "estimateHours": 0.5, "files": [],
                "specRef": "", "comments": [],
            },
            {
                "id": "T-B", "phase": 1, "title": "Root B", "description": "d",
                "model": "claude-sonnet-4-6", "reason": "r", "status": "done",
                "dependencies": [], "estimateHours": 1.0, "files": [],
                "specRef": "", "comments": [],
            },
            {
                "id": "T-C", "phase": 2, "title": "Merge", "description": "d",
                "model": "claude-sonnet-4-6", "reason": "r", "status": "backlog",
                "dependencies": ["T-A", "T-B"], "estimateHours": 2.0, "files": [],
                "specRef": "", "comments": [],
            },
        ],
    }
    (root / "tasks.json").write_text(json.dumps(tasks_payload))

    state = root / ".orchestrator" / "state"
    _write_jsonl(state / "events-run1.jsonl", [
        {"event_type": "dispatch", "task_id": "T-A", "backend": "opencode",
         "ts": "2026-08-21T10:00:00Z",
         "extra": {"pid": 1, "cli_model": "glm-5.1", "attempt": 1}},
        {"event_type": "success", "task_id": "T-A", "backend": "opencode",
         "ts": "2026-08-21T10:05:00Z",
         "extra": {"cost_usd": 0.0, "duration_s": 300.0}},
        {"event_type": "dispatch", "task_id": "T-B", "backend": "claude",
         "ts": "2026-08-21T10:05:00Z",
         "extra": {"pid": 2, "cli_model": "claude-sonnet-4-6", "attempt": 1}},
        {"event_type": "success", "task_id": "T-B", "backend": "claude",
         "ts": "2026-08-21T10:15:00Z",
         "extra": {"cost_usd": 0.75, "duration_s": 600.0}},
    ])
    _write_jsonl(state / "spend-2026-08-21.jsonl", [
        {"ts": "2026-08-21T10:05:00Z", "task_id": "T-A", "backend": "opencode",
         "model": "opencode-go/glm-5.1", "tokens_in": 1000, "tokens_out": 500,
         "cost_usd": 0.0, "duration_s": 300.0},
        {"ts": "2026-08-21T10:15:00Z", "task_id": "T-B", "backend": "claude",
         "model": "claude-sonnet-4-6", "tokens_in": 200, "tokens_out": 4000,
         "cost_usd": 0.75, "duration_s": 600.0},
    ])
    _write_jsonl(state / "spend-2026-08-20.jsonl", [
        {"ts": "2026-08-20T10:00:00Z", "task_id": "T-old", "backend": "claude",
         "model": "claude-haiku-4-5", "tokens_in": 100, "tokens_out": 200,
         "cost_usd": 0.02, "duration_s": 20.0},
    ])

    return ProjectPaths(
        project_root=root,
        project_id="proj",
        config_yaml=root / ".orchestrator" / "config.yaml",
        explicit_root=True,
        state_layout="legacy",
    )


# ---- pricing ---------------------------------------------------------------


def test_pricing_fallback_from_yaml(tmp_path: Path) -> None:
    """`resolve_cost` estimates from pricing.yaml when recorded cost == 0."""
    table = PricingTable.load()  # defaults only
    # opencode-go/glm-5.1 → input 0.30, output 1.20 per 1M tokens
    # 1000 in + 500 out = 1000 * 0.30/1e6 + 500 * 1.20/1e6 = 0.0003 + 0.0006
    cost = table.resolve_cost(
        recorded_cost=0.0,
        model="opencode-go/glm-5.1",
        tokens_in=1000,
        tokens_out=500,
    )
    assert cost == pytest.approx(0.0009, rel=1e-3)


def test_pricing_respects_recorded_cost() -> None:
    """When recorded cost > 0 we USE it — pricing table is fallback only."""
    table = PricingTable.load()
    cost = table.resolve_cost(
        recorded_cost=1.23,
        model="opencode-go/glm-5.1",
        tokens_in=1_000_000,  # would compute huge if we ignored recorded
        tokens_out=1_000_000,
    )
    assert cost == 1.23


def test_pricing_override_from_project(tmp_path: Path) -> None:
    """`<project_root>/pricing.yaml` overrides the packaged defaults."""
    project_root = tmp_path / "proj"
    project_root.mkdir()
    (project_root / "pricing.yaml").write_text(textwrap.dedent("""
        models:
          claude-sonnet-4-6:
            input: 999.0
            output: 999.0
    """))
    table = PricingTable.load(project_root)
    price = table.for_model("claude-sonnet-4-6")
    assert price.input == 999.0
    assert price.output == 999.0
    # Default row still there for other models.
    assert table.for_model("opencode-go/glm-5.1").input == 0.30


def test_pricing_default_row_when_model_unknown() -> None:
    table = PricingTable.load()
    unknown = table.for_model("some/unknown-model-42")
    default = table.for_model("default")
    assert unknown.input == default.input and unknown.output == default.output


# ---- metrics: model + day --------------------------------------------------


def test_metrics_by_model_aggregates_tokens_and_cost() -> None:
    pricing = PricingTable.load()
    spends = [
        {"ts": "2026-08-21T10:00Z", "task_id": "T-1", "backend": "claude",
         "model": "claude-sonnet-4-6", "tokens_in": 100, "tokens_out": 200,
         "cost_usd": 0.10, "duration_s": 5},
        {"ts": "2026-08-21T11:00Z", "task_id": "T-2", "backend": "claude",
         "model": "claude-sonnet-4-6", "tokens_in": 400, "tokens_out": 800,
         "cost_usd": 0.30, "duration_s": 8},
        {"ts": "2026-08-21T12:00Z", "task_id": "T-3", "backend": "opencode",
         "model": "opencode-go/glm-5.1", "tokens_in": 1000, "tokens_out": 500,
         "cost_usd": 0.0, "duration_s": 3},  # falls back to pricing
    ]
    stats = metrics_by_model(spends, pricing)
    by_name = {s.model: s for s in stats}

    assert by_name["claude-sonnet-4-6"].tasks_total == 2
    assert by_name["claude-sonnet-4-6"].tokens_in == 500
    assert by_name["claude-sonnet-4-6"].tokens_out == 1000
    assert by_name["claude-sonnet-4-6"].cost_usd == pytest.approx(0.40, rel=1e-6)

    assert by_name["opencode-go/glm-5.1"].tasks_total == 1
    # Falls back: 1000 * 0.30/1e6 + 500 * 1.20/1e6 = 0.0009
    assert by_name["opencode-go/glm-5.1"].cost_usd == pytest.approx(0.0009, rel=1e-3)


def test_metrics_by_day_buckets_by_date() -> None:
    pricing = PricingTable.load()
    spends = [
        {"ts": "2026-08-21T10:00:00Z", "task_id": "T-A", "backend": "claude",
         "model": "claude-sonnet-4-6", "tokens_in": 100, "tokens_out": 100,
         "cost_usd": 0.10, "duration_s": 5},
        {"ts": "2026-08-21T22:00:00Z", "task_id": "T-B", "backend": "claude",
         "model": "claude-sonnet-4-6", "tokens_in": 200, "tokens_out": 200,
         "cost_usd": 0.20, "duration_s": 5},
        {"ts": "2026-08-20T10:00:00Z", "task_id": "T-C", "backend": "claude",
         "model": "claude-sonnet-4-6", "tokens_in": 50, "tokens_out": 50,
         "cost_usd": 0.05, "duration_s": 5},
    ]
    days = metrics_by_day(spends, pricing)
    assert [d.date for d in days] == ["2026-08-21", "2026-08-20"]
    d21 = days[0]
    assert d21.tasks_total == 2
    assert d21.tokens_in == 300
    assert d21.tokens_out == 300
    assert d21.cost_usd == pytest.approx(0.30, rel=1e-6)


def test_metrics_by_day_limit_days() -> None:
    pricing = PricingTable.load()
    spends = [
        {"ts": f"2026-08-{i:02d}T10:00:00Z", "task_id": f"T-{i}", "backend": "claude",
         "model": "claude-sonnet-4-6", "tokens_in": 10, "tokens_out": 10,
         "cost_usd": 0.01, "duration_s": 5}
        for i in range(1, 20)
    ]
    days = metrics_by_day(spends, pricing, days=5)
    assert len(days) == 5
    # Newest first.
    assert days[0].date > days[-1].date


def test_total_cost_uses_fallback_when_recorded_zero() -> None:
    pricing = PricingTable.load()
    spends = [
        {"ts": "2026-08-21T10:00Z", "task_id": "T-1", "backend": "opencode",
         "model": "opencode-go/glm-5.1", "tokens_in": 10_000, "tokens_out": 5_000,
         "cost_usd": 0.0, "duration_s": 3},
    ]
    # 10000 * 0.30/1e6 + 5000 * 1.20/1e6 = 0.003 + 0.006 = 0.009
    assert total_cost(spends, pricing) == pytest.approx(0.009, rel=1e-3)


# ---- metrics: human_hours + parallelizable ---------------------------------


def test_human_hours_prefers_recorded_duration() -> None:
    events = [
        {"event_type": "dispatch", "task_id": "T-A", "backend": "claude",
         "ts": "2026-08-21T10:00:00Z",
         "extra": {"pid": 1, "cli_model": "x", "attempt": 1}},
        {"event_type": "success", "task_id": "T-A", "backend": "claude",
         "ts": "2026-08-21T10:10:00Z",
         "extra": {"cost_usd": 0.10, "duration_s": 300.0}},  # 5m recorded
    ]
    hours = human_hours_by_task(events)
    # duration_s=300s = 300/3600 = 0.0833h (recorded wins over wall-clock 600s)
    assert hours["T-A"] == pytest.approx(0.083, abs=0.01)


def test_human_hours_falls_back_to_wall_clock() -> None:
    events = [
        {"event_type": "dispatch", "task_id": "T-A", "backend": "claude",
         "ts": "2026-08-21T10:00:00Z", "extra": {}},
        {"event_type": "fail", "task_id": "T-A", "backend": "claude",
         "ts": "2026-08-21T11:00:00Z", "extra": {}},  # no duration_s → use delta 3600s
    ]
    hours = human_hours_by_task(events)
    assert hours["T-A"] == pytest.approx(1.0, abs=0.001)


def test_human_hours_stacks_retries() -> None:
    events = [
        {"event_type": "dispatch", "task_id": "T-A", "backend": "claude",
         "ts": "2026-08-21T10:00:00Z", "extra": {}},
        {"event_type": "fail", "task_id": "T-A", "backend": "claude",
         "ts": "2026-08-21T10:05:00Z", "extra": {"duration_s": 300}},
        {"event_type": "dispatch", "task_id": "T-A", "backend": "claude",
         "ts": "2026-08-21T10:10:00Z", "extra": {}},
        {"event_type": "success", "task_id": "T-A", "backend": "claude",
         "ts": "2026-08-21T10:20:00Z", "extra": {"duration_s": 600}},
    ]
    hours = human_hours_by_task(events)
    # 300 + 600 = 900s = 0.25h
    assert hours["T-A"] == pytest.approx(0.25, abs=0.001)


def test_parallelizable_tasks_filters_correctly() -> None:
    tasks = [
        _mk_task(id="T-A", status="done", dependencies=[]),
        _mk_task(id="T-B", status="done", dependencies=[]),
        _mk_task(id="T-C", status="backlog", dependencies=["T-A", "T-B"]),
        _mk_task(id="T-D", status="backlog", dependencies=["T-A", "T-E"]),  # T-E missing
        _mk_task(id="T-F", status="backlog", dependencies=["T-A", "T-B"]),
        _mk_task(id="T-G", status="in-progress", dependencies=["T-A"]),
        _mk_task(id="T-H", status="blocked", dependencies=[]),
    ]
    ready = parallelizable_tasks(tasks)
    ready_ids = {t.id for t in ready}
    assert ready_ids == {"T-C", "T-F"}


def test_project_summary_counts() -> None:
    tasks = [
        _mk_task(id="T-A", status="done", estimate_hours=1),
        _mk_task(id="T-B", status="in-progress", estimate_hours=2),
        _mk_task(id="T-C", status="blocked", estimate_hours=3),
        _mk_task(id="T-D", status="backlog", estimate_hours=4),
        _mk_task(id="T-E", status="todo", estimate_hours=5),
    ]
    s = project_summary(tasks)
    assert s.total == 5
    assert s.done == 1
    assert s.in_progress == 1
    assert s.blocked == 1
    assert s.backlog == 2  # backlog + todo bucketed together
    assert s.percent_done == pytest.approx(20.0)
    assert s.estimate_hours_total == 15.0


def test_phase_counts_groups_by_phase() -> None:
    tasks = [
        _mk_task(id="T-A", phase=1, status="done"),
        _mk_task(id="T-B", phase=1, status="backlog"),
        _mk_task(id="T-C", phase=2, status="done"),
    ]
    ph = phase_counts(tasks)
    assert ph == [
        {"phase": 1, "total": 2, "done": 1, "in_progress": 0, "blocked": 0},
        {"phase": 2, "total": 1, "done": 1, "in_progress": 0, "blocked": 0},
    ]


# ---- log_stream ------------------------------------------------------------


def test_format_event_dispatch_severity() -> None:
    ev = {"event_type": "dispatch", "task_id": "T-A", "backend": "claude",
          "ts": "2026-08-21T10:00:00Z",
          "extra": {"cli_model": "claude-sonnet-4-6", "attempt": 1}}
    out = format_event(ev)
    assert out["severity"] == "info"
    assert "T-A" in out["human"]
    assert "claude-sonnet-4-6" in out["human"]


def test_format_event_success_shows_duration_and_cost() -> None:
    ev = {"event_type": "success", "task_id": "T-A", "backend": "claude",
          "ts": "2026-08-21T10:05:32Z",
          "extra": {"duration_s": 632.4, "cost_usd": 0.215}}
    out = format_event(ev)
    assert out["severity"] == "ok"
    assert "10m" in out["human"] and "s" in out["human"]
    assert "$0.215" in out["human"]


def test_format_event_malformed_still_produces_output() -> None:
    out = format_event({"gibberish": 1})
    assert out["severity"] == "muted"
    assert "?" in out["human"]


def test_load_recent_events_reads_and_sorts(tmp_path: Path) -> None:
    state = tmp_path / "state"
    _write_jsonl(state / "events-run1.jsonl", [
        {"event_type": "dispatch", "task_id": "T-1", "backend": "claude",
         "ts": "2026-08-21T09:00:00Z", "extra": {}},
    ])
    _write_jsonl(state / "events-run2.jsonl", [
        {"event_type": "success", "task_id": "T-2", "backend": "claude",
         "ts": "2026-08-21T10:00:00Z", "extra": {"duration_s": 10}},
    ])
    recent = load_recent_events(state, limit=100)
    assert len(recent) == 2
    # Ascending by ts.
    assert recent[0]["task_id"] == "T-1"
    assert recent[1]["task_id"] == "T-2"


# ---- scanners --------------------------------------------------------------


def test_read_all_events_tolerates_bad_lines(tmp_path: Path) -> None:
    state = tmp_path / "state"
    state.mkdir()
    p = state / "events-run1.jsonl"
    p.write_text('{"event_type":"dispatch","task_id":"T-A","backend":"c","ts":"2026-08-21T10:00:00Z","extra":{}}\n'
                 '{not valid json\n'
                 '\n'
                 'null\n'
                 '{"event_type":"success","task_id":"T-A","backend":"c","ts":"2026-08-21T10:05:00Z","extra":{}}\n')
    events = read_all_events(state)
    assert len(events) == 2
    assert [e["event_type"] for e in events] == ["dispatch", "success"]


def test_read_all_spends_reads_multiple_days(tmp_path: Path) -> None:
    paths = _make_fixture_project(tmp_path)
    spends = read_all_spends(paths.state_dir)
    assert len(spends) == 3
    # Chronological.
    assert spends[0]["ts"] < spends[-1]["ts"]


# ---- server smoke ----------------------------------------------------------


def _test_client_or_skip(tmp_path: Path):
    """Build a TestClient; skip if fastapi isn't available."""
    pytest.importorskip("fastapi")
    from fastapi.testclient import TestClient
    from orchestrator.dashboard.server import create_app

    paths = _make_fixture_project(tmp_path)
    app = create_app(paths=paths)
    return TestClient(app), paths


def test_server_starts(tmp_path: Path) -> None:
    client, _ = _test_client_or_skip(tmp_path)
    # If we got here, `create_app` didn't crash and the TestClient is alive.
    # Ping the docs endpoint (FastAPI ships /docs by default).
    r = client.get("/docs")
    assert r.status_code == 200


def test_api_tasks_shape(tmp_path: Path) -> None:
    client, paths = _test_client_or_skip(tmp_path)
    r = client.get("/api/tasks")
    assert r.status_code == 200
    payload = r.json()
    assert payload["project_id"] == paths.project_id
    assert payload["count"] == 3
    assert payload["total"] == 3
    assert "summary" in payload
    assert isinstance(payload["tasks"], list)
    ids = {t["id"] for t in payload["tasks"]}
    assert ids == {"T-A", "T-B", "T-C"}
    # Each task exposes human_hours + dep_count.
    t_a = next(t for t in payload["tasks"] if t["id"] == "T-A")
    assert "human_hours" in t_a and "dep_count" in t_a and "last_updated" in t_a


def test_api_task_detail_404(tmp_path: Path) -> None:
    client, _ = _test_client_or_skip(tmp_path)
    r = client.get("/api/task/does-not-exist")
    assert r.status_code == 404


def test_api_metrics_shape(tmp_path: Path) -> None:
    client, _ = _test_client_or_skip(tmp_path)
    r = client.get("/api/metrics")
    assert r.status_code == 200
    payload = r.json()
    assert "total_cost_usd" in payload
    assert "by_model" in payload
    assert "by_day" in payload
    # We wrote 3 spend rows across 2 days.
    assert len(payload["by_day"]) == 2


def test_api_budgets_disabled_when_no_config(tmp_path: Path) -> None:
    """No `budgets.yaml` anywhere → endpoint returns disabled=True."""
    client, _ = _test_client_or_skip(tmp_path)
    r = client.get("/api/budgets")
    assert r.status_code == 200
    payload = r.json()
    assert payload["disabled"] is True
    assert payload["providers"] == {}


def test_api_budgets_enabled_when_yaml_present(tmp_path: Path) -> None:
    """Drop a `budgets.yaml` in the project root → endpoint returns provider snapshot."""
    client, paths = _test_client_or_skip(tmp_path)
    # Write a tiny budgets.yaml at project root (one of the two lookup paths).
    (paths.project_root / "budgets.yaml").write_text(
        "presets:\n"
        "  conservative:\n"
        "    claude:   {window_hours: 5,  token_budget: 100000, threshold_pct: 60}\n"
        "    codex:    {window_hours: 3,  token_budget: 50000,  threshold_pct: 60}\n",
        encoding="utf-8",
    )
    r = client.get("/api/budgets")
    assert r.status_code == 200
    payload = r.json()
    assert payload["disabled"] is False
    assert payload["preset"] == "conservative"
    assert set(payload["providers"].keys()) == {"claude", "codex"}
    claude = payload["providers"]["claude"]
    assert claude["token_budget"] == 100000
    assert claude["threshold_pct"] == 60
    assert claude["window_hours"] == 5
    assert "usage_pct" in claude
    assert "capped" in claude


def test_api_budgets_unknown_preset_returns_400(tmp_path: Path, monkeypatch) -> None:
    """Bad preset name → 400 so the operator sees the typo instead of silent disable."""
    client, paths = _test_client_or_skip(tmp_path)
    (paths.project_root / "budgets.yaml").write_text(
        "presets:\n  conservative:\n    claude: {window_hours: 5, token_budget: 100, threshold_pct: 50}\n",
        encoding="utf-8",
    )
    monkeypatch.setenv("ORCH_BUDGETS_PRESET", "doesnotexist")
    r = client.get("/api/budgets")
    assert r.status_code == 400
    assert "doesnotexist" in r.json()["detail"]


# ---- Sprint 2/3: downstream_impact + events_for_task ----------------------


def test_downstream_impact_linear_chain() -> None:
    """A → B → C: A blocks 2 pending, B blocks 1, C blocks 0."""
    a = _mk_task(id="A", status="todo", dependencies=[])
    b = _mk_task(id="B", status="todo", dependencies=["A"])
    c = _mk_task(id="C", status="todo", dependencies=["B"])
    impact = downstream_impact([a, b, c])
    assert impact == {"A": 2, "B": 1, "C": 0}


def test_downstream_impact_excludes_done_from_count() -> None:
    """A done downstream task doesn't count as 'still blocked'."""
    a = _mk_task(id="A", status="todo", dependencies=[])
    b = _mk_task(id="B", status="done", dependencies=["A"])
    c = _mk_task(id="C", status="todo", dependencies=["B"])
    impact = downstream_impact([a, b, c])
    # A → B (done, skip) → C (todo, count). Impact of A = 1 (only C pending).
    assert impact["A"] == 1
    # B is done so nothing waits on it in the pending sense.
    assert impact["B"] == 1  # C still depends on B, and C is not done.
    assert impact["C"] == 0


def test_downstream_impact_missing_dep_is_ignored() -> None:
    """A dep to an unknown id must not crash — orphan handled at another layer."""
    a = _mk_task(id="A", status="todo", dependencies=["ghost"])
    impact = downstream_impact([a])
    assert impact == {"A": 0}


def test_downstream_impact_cycle_is_bounded() -> None:
    """Cyclic graph (would be a bug in tasks.json) must not infinite-loop."""
    a = _mk_task(id="A", status="todo", dependencies=["B"])
    b = _mk_task(id="B", status="todo", dependencies=["A"])
    impact = downstream_impact([a, b])
    # Both count each other, seen set prevents loop.
    assert impact["A"] == 1
    assert impact["B"] == 1


def test_events_for_task_filters_and_orders(tmp_path: Path) -> None:
    """events_for_task returns only matching task_id, sorted ascending."""
    events = [
        {"event_type": "dispatch", "task_id": "T-A", "ts": "2026-08-21T09:00:00Z"},
        {"event_type": "dispatch", "task_id": "T-B", "ts": "2026-08-21T09:01:00Z"},
        {"event_type": "success", "task_id": "T-A", "ts": "2026-08-21T09:05:00Z"},
    ]
    matches = events_for_task(events, "T-A")
    assert [e["event_type"] for e in matches] == ["dispatch", "success"]
    assert all(e["task_id"] == "T-A" for e in matches)


def test_events_for_task_respects_limit() -> None:
    """When more than `limit` matches exist, the newest tail is returned."""
    events = [
        {"event_type": "dispatch", "task_id": "T-A",
         "ts": f"2026-08-21T09:{i:02d}:00Z"}
        for i in range(30)
    ]
    matches = events_for_task(events, "T-A", limit=5)
    assert len(matches) == 5
    # The last 5 (highest timestamps) survive.
    assert matches[0]["ts"] == "2026-08-21T09:25:00Z"
    assert matches[-1]["ts"] == "2026-08-21T09:29:00Z"


def test_events_for_task_empty_when_no_matches() -> None:
    events = [{"event_type": "dispatch", "task_id": "T-A", "ts": "x"}]
    assert events_for_task(events, "T-Z") == []


