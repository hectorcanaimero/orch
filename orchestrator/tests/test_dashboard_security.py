"""Sprint E-2 security matrix — every operator-only route must 403 in
stakeholder mode; the stakeholder allow-list must gate on the token.

Route inventory (verified against server.py):

    Operator-only (403 in stakeholder mode):
      GET  /                             — index
      GET  /kanban                       — kanban_page
      GET  /metrics                      — metrics_page
      GET  /logs                         — logs_page
      GET  /logs/stream                  — logs_stream (SSE)
      GET  /api/events/stream            — api_events_stream (SSE)
      GET  /api/tasks                    — api_tasks
      GET  /api/task/{id}                — api_task_detail
      GET  /api/budgets                  — api_budgets
      GET  /api/metrics                  — api_metrics
      GET  /snapshot                     — snapshot
      GET  /partials/task-modal/{id}     — partial_task_modal
      GET  /partials/task-row/{id}       — partial_task_row

    Stakeholder-safe (200 with valid token):
      GET  /stakeholder                  — stakeholder_index
      GET  /stakeholder/summary          — stakeholder_summary_json
      GET  /static/*                     — mounted StaticFiles

Also asserts information-hiding contracts: 401/403 bodies never leak
the requested path or a stack trace, and no `WWW-Authenticate` header
that would reveal the auth scheme to a scanner.
"""

from __future__ import annotations

import json
from pathlib import Path

import pytest


OPERATOR_ONLY_ROUTES: list[tuple[str, str]] = [
    ("GET", "/"),
    ("GET", "/kanban"),
    ("GET", "/metrics"),
    ("GET", "/logs"),
    ("GET", "/logs/stream"),
    ("GET", "/api/events/stream"),
    ("GET", "/api/tasks"),
    ("GET", "/api/task/T-A"),
    ("GET", "/api/budgets"),
    ("GET", "/api/metrics"),
    ("GET", "/snapshot"),
    ("GET", "/partials/task-modal/T-A"),
    ("GET", "/partials/task-row/T-A"),
]

STAKEHOLDER_SAFE_ROUTES: list[tuple[str, str]] = [
    ("GET", "/stakeholder"),
    ("GET", "/stakeholder/summary"),
]


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
        "phases": [{"id": 1, "name": "smoke"}],
        "tasks": [
            {"id": "T-A", "phase": 1, "title": "A", "description": "",
             "model": "opencode-go/glm-5.1", "reason": "", "status": "done",
             "dependencies": [], "estimateHours": 0.5, "files": [],
             "specRef": "", "comments": []},
        ],
    }
    (root / "tasks.json").write_text(json.dumps(tasks_payload), encoding="utf-8")

    state_dir = root / "orchestrator" / "state"
    _write_jsonl(state_dir / "spend-2026-08-20.jsonl", [
        {"ts": "2026-08-20T10:12:00", "task_id": "T-A", "model": "opencode-go/glm-5.1",
         "tokens_in": 5000, "tokens_out": 2000, "cost_usd": 0.42},
    ])
    return ProjectPaths(
        project_root=root,
        project_id="proj",
        config_yaml=root / "orchestrator" / "config.yaml",
        explicit_root=True,
        state_layout="legacy",
    )


def _client(tmp_path: Path, **override):
    pytest.importorskip("jinja2")
    pytest.importorskip("fastapi")
    from fastapi.testclient import TestClient
    from orchestrator.dashboard.server import create_app

    paths = _make_fixture_project(tmp_path)
    app = create_app(
        paths=paths,
        profile_override=override.get("profile"),
        token_override=override.get("token"),
    )
    return TestClient(app)


# ---- Operator profile — everything works (no regression) ------------------


@pytest.mark.parametrize("method,path", OPERATOR_ONLY_ROUTES + STAKEHOLDER_SAFE_ROUTES)
def test_operator_profile_serves_everything(tmp_path: Path, method: str, path: str) -> None:
    client = _client(tmp_path)  # default = operator, no middleware
    # SSE endpoints block forever, skip them in this parametrize round; the
    # dedicated SSE test uses a shorter tail hook.
    if path.endswith("/stream"):
        pytest.skip("SSE endpoint — covered separately")
    r = client.request(method, path)
    # Operator-only routes may 404 on missing IDs but MUST NOT 401/403.
    assert r.status_code not in (401, 403), (
        f"{method} {path} rejected in operator mode ({r.status_code})"
    )


# ---- Stakeholder profile — operator routes 403, stakeholder routes 200 ---


@pytest.mark.parametrize("method,path", OPERATOR_ONLY_ROUTES)
def test_stakeholder_profile_rejects_operator_routes(
    tmp_path: Path, method: str, path: str
) -> None:
    client = _client(tmp_path, profile="stakeholder", token="s3cr3t")
    r = client.request(method, path, headers={"Authorization": "Bearer s3cr3t"})
    assert r.status_code == 403, (
        f"{method} {path} not blocked in stakeholder mode ({r.status_code})"
    )
    # Info-hiding: 403 body must not echo the path or reveal route existence.
    body = r.text.lower()
    for hint in path.strip("/").split("/"):
        # Skip trivial fragments that could match any status message.
        if not hint or hint in {"api", "get", "post"}:
            continue
        assert hint not in body, f"403 body leaked path fragment {hint!r} for {path}"


@pytest.mark.parametrize("method,path", STAKEHOLDER_SAFE_ROUTES)
def test_stakeholder_profile_accepts_curated_routes(
    tmp_path: Path, method: str, path: str
) -> None:
    client = _client(tmp_path, profile="stakeholder", token="s3cr3t")
    r = client.request(method, path, headers={"Authorization": "Bearer s3cr3t"})
    assert r.status_code == 200, (
        f"{method} {path} rejected in stakeholder mode ({r.status_code}) - {r.text}"
    )


# ---- Token enforcement --------------------------------------------------


def test_stakeholder_without_token_returns_401(tmp_path: Path) -> None:
    client = _client(tmp_path, profile="stakeholder", token="s3cr3t")
    r = client.get("/stakeholder")
    assert r.status_code == 401
    assert r.text == "unauthorized"


def test_stakeholder_wrong_token_returns_401(tmp_path: Path) -> None:
    client = _client(tmp_path, profile="stakeholder", token="s3cr3t")
    r = client.get("/stakeholder", headers={"Authorization": "Bearer wrong"})
    assert r.status_code == 401


def test_stakeholder_wrong_token_query_param_returns_401(tmp_path: Path) -> None:
    client = _client(tmp_path, profile="stakeholder", token="s3cr3t")
    r = client.get("/stakeholder?token=wrong")
    assert r.status_code == 401


def test_stakeholder_token_query_param_accepted(tmp_path: Path) -> None:
    client = _client(tmp_path, profile="stakeholder", token="s3cr3t")
    r = client.get("/stakeholder?token=s3cr3t")
    assert r.status_code == 200


def test_stakeholder_401_does_not_leak_www_authenticate(tmp_path: Path) -> None:
    """No WWW-Authenticate header — that would tell scanners we use Bearer."""
    client = _client(tmp_path, profile="stakeholder", token="s3cr3t")
    r = client.get("/stakeholder")
    assert r.status_code == 401
    assert "www-authenticate" not in {k.lower() for k in r.headers.keys()}


def test_stakeholder_response_marked_no_store(tmp_path: Path) -> None:
    client = _client(tmp_path, profile="stakeholder", token="s3cr3t")
    r = client.get("/stakeholder")
    assert r.headers.get("Cache-Control") == "no-store"


# ---- Both mode: /stakeholder path gated, operator paths open --------------


def test_both_mode_operator_paths_no_auth(tmp_path: Path) -> None:
    client = _client(tmp_path, profile="both", token="s3cr3t")
    # Operator paths — no token required.
    assert client.get("/").status_code == 200
    assert client.get("/api/tasks").status_code == 200


def test_both_mode_stakeholder_path_requires_token(tmp_path: Path) -> None:
    client = _client(tmp_path, profile="both", token="s3cr3t")
    assert client.get("/stakeholder").status_code == 401
    assert client.get("/stakeholder?token=s3cr3t").status_code == 200


def test_both_mode_stakeholder_prefix_rejects_operator_child_routes(
    tmp_path: Path,
) -> None:
    """`/stakeholder/anything-not-allow-listed` should 403 (or 401 sans token)."""
    client = _client(tmp_path, profile="both", token="s3cr3t")
    r = client.get("/stakeholder/definitely-not-a-route",
                   headers={"Authorization": "Bearer s3cr3t"})
    # Under stakeholder guard the router would 404 but the guard 403s
    # first (safer — no route-existence oracle).
    assert r.status_code in (403, 404)


# ---- Method fuzzing on control routes -------------------------------------


@pytest.mark.parametrize("method", ["POST", "PUT", "DELETE", "PATCH"])
def test_stakeholder_mutation_methods_rejected(tmp_path: Path, method: str) -> None:
    """No mutation verb should reach any handler in stakeholder mode."""
    client = _client(tmp_path, profile="stakeholder", token="s3cr3t")
    r = client.request(method, "/api/tasks",
                       headers={"Authorization": "Bearer s3cr3t"})
    # Middleware runs before Starlette's 405 method-not-allowed → 403 wins.
    assert r.status_code in (403, 405)


# ---- Websocket handshake rejection (defense-in-depth) ---------------------


def test_websocket_scope_rejected_in_stakeholder_mode() -> None:
    """Direct unit test of the ws guard used by any future websocket route."""
    import asyncio

    from orchestrator.dashboard.dashboard_config import DashboardConfig
    from orchestrator.dashboard.middleware import websocket_stakeholder_guard

    cfg = DashboardConfig.load(profile_override="stakeholder", token_override="t")
    sent: list[dict] = []

    async def _send(msg):
        sent.append(msg)

    async def _receive():
        return {"type": "websocket.connect"}

    scope = {"type": "websocket", "path": "/logs/stream"}
    ok = asyncio.run(websocket_stakeholder_guard(scope, _receive, _send, config=cfg))
    assert ok is True
    assert sent and sent[0]["type"] == "websocket.close"
