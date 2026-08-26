"""Sprint E-3 — dev-only CORS middleware for the Vite SPA spike.

The dashboard's FastAPI app is normally same-origin (served + consumed
on `127.0.0.1:7420`). During the SPA spike a Vite dev server on
`http://localhost:5173` needs to hit the API cross-origin, so we add a
CORS middleware gated on `ORCH_DASHBOARD_DEV_CORS`.

Three invariants under test:

    A. When the env var is unset, the app never emits
       `Access-Control-Allow-Origin` — no accidental CORS in prod.
    B. When the env var is set, a preflight `OPTIONS` from the allow-
       listed origin gets a 200 with the correct ACAO header AND is
       NOT gated by auth (browsers dispatch preflights without any
       Authorization header — a 401 there breaks the whole flow).
    C. Regular authenticated GETs still succeed with CORS enabled —
       the CORS layer must not swallow the response headers or
       short-circuit the token check for actual requests.
"""

from __future__ import annotations

import json
from pathlib import Path

import pytest


# ---- Fixture builders (mirror the security integration test helpers) -----


def _make_project(tmp_path: Path):
    """Build a minimal project skeleton the dashboard can boot against."""
    from orchestrator.paths import ProjectPaths

    root = tmp_path / "proj"
    (root / ".orchestrator" / "state").mkdir(parents=True)
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

    return ProjectPaths(
        project_root=root,
        project_id="proj",
        config_yaml=root / ".orchestrator" / "config.yaml",
        explicit_root=True,
        state_layout="legacy",
    )


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


# ---- Test A: CORS env var unset → no ACAO header ------------------------


def test_no_cors_headers_when_env_unset(tmp_path: Path, monkeypatch) -> None:
    """Without ORCH_DASHBOARD_DEV_CORS set, responses never carry ACAO."""
    monkeypatch.delenv("ORCH_DASHBOARD_DEV_CORS", raising=False)
    paths = _make_project(tmp_path)
    client = _client(paths)  # operator mode, no auth needed
    r = client.get("/api/tasks", headers={"Origin": "http://localhost:5173"})
    assert r.status_code == 200
    # No CORS middleware installed → Starlette must not emit ACAO.
    assert "access-control-allow-origin" not in {
        k.lower() for k in r.headers.keys()
    }


# ---- Test B: preflight OPTIONS succeeds without auth --------------------


def test_preflight_options_returns_200_without_auth(
    tmp_path: Path, monkeypatch,
) -> None:
    """Preflight from :5173 → /stakeholder/summary is 200 + ACAO, no token."""
    monkeypatch.setenv("ORCH_DASHBOARD_DEV_CORS", "1")
    paths = _make_project(tmp_path)
    client = _client(paths, profile="stakeholder", token="s3cret")

    r = client.options(
        "/stakeholder/summary",
        headers={
            "Origin": "http://localhost:5173",
            "Access-Control-Request-Method": "GET",
            "Access-Control-Request-Headers": "authorization,content-type",
        },
    )
    # CORS middleware must handle the preflight itself → 200, and it must
    # run BEFORE the auth middleware (otherwise no-Authorization → 401).
    assert r.status_code == 200, (
        f"preflight returned {r.status_code}; "
        "CORS middleware likely mis-ordered vs auth"
    )
    assert r.headers.get("access-control-allow-origin") == "http://localhost:5173"
    # Methods + headers echoed back.
    allow_methods = r.headers.get("access-control-allow-methods", "")
    assert "GET" in allow_methods
    allow_headers = r.headers.get("access-control-allow-headers", "").lower()
    assert "authorization" in allow_headers


# ---- Test C: authenticated GET still works with CORS enabled ------------


def test_authenticated_get_still_works_with_cors_enabled(
    tmp_path: Path, monkeypatch,
) -> None:
    """A real GET with a valid Bearer token succeeds AND carries ACAO."""
    monkeypatch.setenv("ORCH_DASHBOARD_DEV_CORS", "true")
    paths = _make_project(tmp_path)
    client = _client(paths, profile="stakeholder", token="s3cret")

    r = client.get(
        "/stakeholder/summary",
        headers={
            "Origin": "http://localhost:5173",
            "Authorization": "Bearer s3cret",
        },
    )
    assert r.status_code == 200
    # Response body is still the curated stakeholder payload.
    payload = r.json()
    assert "summary" in payload and "milestones" in payload
    # CORS layer must annotate the response with ACAO so the browser lets
    # the SPA read it. `allow_credentials=True` requires echoing the exact
    # origin (never `*`).
    acao = r.headers.get("access-control-allow-origin")
    assert acao == "http://localhost:5173"
    assert r.headers.get("access-control-allow-credentials") == "true"


def test_authenticated_get_fails_with_wrong_token_even_with_cors(
    tmp_path: Path, monkeypatch,
) -> None:
    """CORS must not swallow the auth failure — 401 still fires for bad tokens."""
    monkeypatch.setenv("ORCH_DASHBOARD_DEV_CORS", "yes")
    paths = _make_project(tmp_path)
    client = _client(paths, profile="stakeholder", token="s3cret")

    r = client.get(
        "/stakeholder/summary",
        headers={
            "Origin": "http://localhost:5173",
            "Authorization": "Bearer wrong",
        },
    )
    assert r.status_code == 401
