"""Sprint E-6 UX: `GET /api/whoami` returns the resolved dashboard profile.

The SPA uses this to hide operator-only nav entries (Doctor, Tunnel,
Metrics, Logs) when it's serving a stakeholder session. Profile is not
a secret — the token is — so this endpoint is intentionally on the
stakeholder allow-list.

Coverage matrix:
    * default operator profile → "operator"
    * stakeholder profile      → "stakeholder" (with valid token)
    * both profile             → "both" (with valid token on stakeholder prefix,
                                   and no token needed elsewhere)
    * response never leaks a token
"""

from __future__ import annotations

import json
from pathlib import Path

import pytest


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


def _client(tmp_path: Path, **override):
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


def test_whoami_default_reports_operator(tmp_path: Path) -> None:
    """No profile override → operator, no auth needed."""
    client = _client(tmp_path)
    r = client.get("/api/whoami")
    assert r.status_code == 200
    assert r.json() == {"profile": "operator"}


def test_whoami_stakeholder_with_token(tmp_path: Path) -> None:
    """Stakeholder profile → "stakeholder", token required."""
    client = _client(tmp_path, profile="stakeholder", token="s3cr3t")
    r = client.get("/api/whoami", headers={"Authorization": "Bearer s3cr3t"})
    assert r.status_code == 200
    assert r.json() == {"profile": "stakeholder"}


def test_whoami_stakeholder_without_token_401(tmp_path: Path) -> None:
    """Stakeholder mode still requires the bearer token — no free pass."""
    client = _client(tmp_path, profile="stakeholder", token="s3cr3t")
    r = client.get("/api/whoami")
    assert r.status_code == 401


def test_whoami_both_mode_reports_both(tmp_path: Path) -> None:
    """`both` profile keeps operator UX open and gates only /stakeholder/*.

    /api/whoami sits under /api, i.e. the operator side of `both` mode,
    so it answers without a token when profile == "both".
    """
    client = _client(tmp_path, profile="both", token="s3cr3t")
    r = client.get("/api/whoami")
    assert r.status_code == 200
    assert r.json() == {"profile": "both"}


def test_whoami_never_echoes_token(tmp_path: Path) -> None:
    """Even a distinctive token string must not appear anywhere in the body."""
    token = "leaky-marker-XYZ"
    client = _client(tmp_path, profile="stakeholder", token=token)
    r = client.get("/api/whoami", headers={"Authorization": f"Bearer {token}"})
    assert r.status_code == 200
    assert token not in r.text
