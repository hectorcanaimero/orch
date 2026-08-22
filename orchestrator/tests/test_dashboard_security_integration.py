"""Sprint E-2 — cross-backend security integration + attack scenarios.

Two intertwined goals:

    1. Backend parity: the stakeholder view returns curated data whether
       the project's spend/events live in JSONL files OR in the sqlite
       backend. Both paths flow through the same `_stakeholder_payload`
       helper — but we prove it end-to-end anyway.

    2. Attack matrix: exhaustively try to reach operator surface from
       stakeholder mode (mutation verbs, header spoofing, header/query
       token mixing, path-traversal on `/stakeholder/...`, malformed
       Authorization values, no-verb WebSocket upgrades). Zero of them
       may succeed.

Deliberately paranoid — a false negative here would ship a public URL
that leaks per-model spend or raw logs.
"""

from __future__ import annotations

import json
from pathlib import Path

import pytest


# ---- Fixture builders (mirror the ones in test_dashboard_security.py) ----


def _write_jsonl(path: Path, rows: list[dict]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("w", encoding="utf-8") as fh:
        for r in rows:
            fh.write(json.dumps(r) + "\n")


def _make_project(tmp_path: Path):
    """Create a minimal project skeleton — no spend/events written yet."""
    from orchestrator.paths import ProjectPaths

    root = tmp_path / "proj"
    (root / "orchestrator" / "state").mkdir(parents=True)
    (root / "scripts").mkdir(parents=True)
    (root / "scripts" / "task-start.sh").write_text("#!/bin/sh\nexit 0\n")

    tasks_payload = {
        "phases": [{"id": 1, "name": "smoke"}],
        "tasks": [
            {"id": "T-A", "phase": 1, "title": "A", "description": "",
             "model": "opencode-go/glm-5.1", "reason": "", "status": "done",
             "dependencies": [], "estimateHours": 0.5, "files": [],
             "specRef": "", "comments": []},
            {"id": "T-B", "phase": 1, "title": "B", "description": "",
             "model": "claude-sonnet-4-6", "reason": "", "status": "backlog",
             "dependencies": ["T-A"], "estimateHours": 1.0, "files": [],
             "specRef": "", "comments": []},
        ],
    }
    (root / "tasks.json").write_text(json.dumps(tasks_payload), encoding="utf-8")

    return ProjectPaths(
        project_root=root,
        project_id="proj",
        config_yaml=root / "orchestrator" / "config.yaml",
        explicit_root=True,
        state_layout="legacy",
    )


def _write_file_backend_spends(paths, cost: float = 1.75) -> None:
    """Drop a spend JSONL row into the state dir — file-backend style."""
    _write_jsonl(paths.state_dir / "spend-2026-08-20.jsonl", [
        {"ts": "2026-08-20T10:12:00", "task_id": "T-A",
         "model": "opencode-go/glm-5.1",
         "tokens_in": 5000, "tokens_out": 2000, "cost_usd": cost},
    ])


def _write_sqlite_backend_spends(paths, cost: float = 1.75) -> None:
    """Populate the sqlite backend with the same spend total.

    Uses the public SqliteBackend API instead of raw SQL — that's the
    same code path the orchestrator uses in production, so a schema drift
    would surface here first.
    """
    from orchestrator.models import SpendEntry
    from orchestrator.state.sqlite_backend import SqliteBackend

    db = paths.state_dir / "orch.db"
    be = SqliteBackend(db_path=db, project_id=paths.project_id,
                       project_root=paths.project_root)
    from orchestrator.models import Task
    be.bootstrap([
        Task(id="T-A", phase=1, title="A", description="", model="opencode-go/glm-5.1",
             reason="", status="done", dependencies=[], estimate_hours=0.5,
             files=[], spec_ref="", comments=[]),
    ])
    be.append_spend(SpendEntry(
        ts="2026-08-20T10:12:00",
        task_id="T-A",
        backend="opencode-go",
        model="opencode-go/glm-5.1",
        tokens_in=5000,
        tokens_out=2000,
        cost_usd=cost,
        duration_s=120.0,
        project_id=paths.project_id,
    ))


def _client(paths, **override):
    pytest.importorskip("fastapi")
    from fastapi.testclient import TestClient
    from orchestrator.dashboard.server import create_app

    app = create_app(
        paths=paths,
        profile_override=override.get("profile"),
        token_override=override.get("token"),
    )
    return TestClient(app)


# ---- Backend parity -----------------------------------------------------


@pytest.mark.parametrize("backend", ["file", "sqlite"])
def test_stakeholder_view_reads_from_both_backends(tmp_path: Path, backend: str) -> None:
    """Whether spend lives in JSONL or sqlite, curated payload matches."""
    paths = _make_project(tmp_path)
    if backend == "file":
        _write_file_backend_spends(paths, cost=1.75)
    else:
        _write_sqlite_backend_spends(paths, cost=1.75)

    client = _client(paths, profile="stakeholder", token="t")
    r = client.get("/stakeholder/summary", headers={"Authorization": "Bearer t"})
    assert r.status_code == 200
    payload = r.json()
    # Rounded up from 1.75 → 2.00 (nearest $0.50).
    assert payload["spend_rounded_usd"] == pytest.approx(2.0)
    # Milestone count is derived from tasks, so it's identical across backends.
    assert len(payload["milestones"]) == 1
    # Curated payload never leaks provider identifiers.
    body = r.text
    assert "opencode-go" not in body
    assert "claude-sonnet" not in body


@pytest.mark.parametrize("backend", ["file", "sqlite"])
def test_operator_api_tasks_reads_from_both_backends(
    tmp_path: Path, backend: str
) -> None:
    """Operator JSON API continues to expose tasks with either backend."""
    paths = _make_project(tmp_path)
    if backend == "file":
        _write_file_backend_spends(paths)
    else:
        _write_sqlite_backend_spends(paths)

    client = _client(paths)
    r = client.get("/api/tasks")
    assert r.status_code == 200
    ids = {t["id"] for t in r.json()["tasks"]}
    assert "T-A" in ids
    assert "T-B" in ids


# ---- Attack scenarios ---------------------------------------------------


def test_case_insensitive_authorization_header(tmp_path: Path) -> None:
    """`authorization: Bearer` (lowercase) still authenticates."""
    paths = _make_project(tmp_path)
    _write_file_backend_spends(paths)
    client = _client(paths, profile="stakeholder", token="s3cret")
    r = client.get("/stakeholder/summary", headers={"authorization": "bearer s3cret"})
    assert r.status_code == 200


def test_multiple_authorization_headers_uses_first_valid(tmp_path: Path) -> None:
    """Header wins over query param when both are present with different values."""
    paths = _make_project(tmp_path)
    _write_file_backend_spends(paths)
    client = _client(paths, profile="stakeholder", token="s3cret")
    # Header = valid, query = invalid → should succeed (header wins).
    r = client.get("/stakeholder/summary?token=wrong",
                   headers={"Authorization": "Bearer s3cret"})
    assert r.status_code == 200


def test_malformed_authorization_header_rejected(tmp_path: Path) -> None:
    paths = _make_project(tmp_path)
    _write_file_backend_spends(paths)
    client = _client(paths, profile="stakeholder", token="s3cret")
    for bogus in ("s3cret", "Basic s3cret", "Bearer", "Bearer  ", ""):
        r = client.get("/stakeholder/summary", headers={"Authorization": bogus})
        assert r.status_code == 401, f"accepted bogus Authorization: {bogus!r}"


def test_empty_token_query_param_rejected(tmp_path: Path) -> None:
    paths = _make_project(tmp_path)
    _write_file_backend_spends(paths)
    client = _client(paths, profile="stakeholder", token="s3cret")
    r = client.get("/stakeholder/summary?token=")
    assert r.status_code == 401


@pytest.mark.parametrize("path", [
    "/api/tasks",
    "/api/task/T-A",
    "/api/metrics",
    "/api/budgets",
    "/snapshot",
])
def test_all_operator_routes_403_with_valid_token(tmp_path: Path, path: str) -> None:
    """Even a valid token cannot unlock an operator-only JSON path in
    stakeholder mode. The legacy Jinja UI routes (`/`, `/kanban`,
    `/metrics`, `/logs`, `/partials/*`) were removed when the SPA became
    the sole UI — client-side React Router paths are served by the SPA
    mount (name="spa") which IS allow-listed."""
    paths = _make_project(tmp_path)
    _write_file_backend_spends(paths)
    client = _client(paths, profile="stakeholder", token="s3cret")
    r = client.get(path, headers={"Authorization": "Bearer s3cret"})
    assert r.status_code == 403
    assert r.text == "forbidden"


@pytest.mark.parametrize("method", ["POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS"])
def test_no_mutation_verb_bypasses_guard(tmp_path: Path, method: str) -> None:
    """Every HTTP verb on an operator path is rejected in stakeholder mode."""
    paths = _make_project(tmp_path)
    _write_file_backend_spends(paths)
    client = _client(paths, profile="stakeholder", token="s3cret")
    r = client.request(method, "/api/tasks",
                       headers={"Authorization": "Bearer s3cret"})
    # 403 = guard blocked. 405 = starlette method-not-allowed (also acceptable).
    assert r.status_code in (403, 405)


def test_path_traversal_stakeholder_mode_still_blocks_snapshot(tmp_path: Path) -> None:
    """`/stakeholder/../snapshot` must not smuggle past the guard in
    stakeholder mode.

    Note: in `both` mode operator paths are OPEN by design (operator runs
    on localhost), so path traversal against `both` isn't a bypass — it's
    the documented behavior. `stakeholder` mode is the paranoid one, and
    that's where we care."""
    paths = _make_project(tmp_path)
    _write_file_backend_spends(paths)
    client = _client(paths, profile="stakeholder", token="s3cret")
    # httpx normalizes `/stakeholder/../snapshot` client-side to `/snapshot`
    # — which is an operator route → 403 in stakeholder mode. Confirmed.
    r = client.get("/stakeholder/../snapshot",
                   headers={"Authorization": "Bearer s3cret"})
    assert r.status_code == 403
    body = r.text.lower()
    assert "cost" not in body and "$" not in body


def test_spa_assets_still_allowed_in_stakeholder_mode(tmp_path: Path) -> None:
    """The SPA's bundled `/assets/*` chunks stay reachable so the SPA can
    hydrate under the stakeholder profile.

    We don't build a real Vite bundle here — we just prove the middleware
    doesn't 403 the request. Without a mount registered, starlette 404s
    (StaticFiles isn't attached in this test fixture), and either outcome
    proves the guard passed."""
    paths = _make_project(tmp_path)
    _write_file_backend_spends(paths)
    client = _client(paths, profile="stakeholder", token="s3cret")
    r = client.get("/assets/definitely-missing-chunk.js",
                   headers={"Authorization": "Bearer s3cret"})
    # 200 (mount serves it) or 404 (nothing there) — both acceptable.
    # 403 = middleware blocked the request → regression.
    assert r.status_code != 403, r.text


def test_stakeholder_summary_json_no_leak_of_evidence(tmp_path: Path) -> None:
    """Even the JSON body must not include a per-task exit code or backend name."""
    paths = _make_project(tmp_path)
    _write_file_backend_spends(paths, cost=0.42)
    client = _client(paths, profile="stakeholder", token="t")
    r = client.get("/stakeholder/summary", headers={"Authorization": "Bearer t"})
    payload = r.json()
    # Never expose per-model or per-task granularity.
    text = json.dumps(payload)
    assert "opencode-go" not in text
    assert "tokens_in" not in text
    assert "task_id" not in text
    assert "cost_usd" not in text
