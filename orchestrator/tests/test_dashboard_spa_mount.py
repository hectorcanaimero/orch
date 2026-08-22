"""Sprint E-6 — SPA mount at /spa/ served by FastAPI.

Two invariants under test:

    A. When `<project_root>/frontend/dist/index.html` exists, `GET /spa/`
       returns 200 and the served body contains the marker HTML we wrote
       to the fixture. This proves the mount is wired and StaticFiles is
       finding the file.

    B. When `<project_root>/frontend/dist/` doesn't exist, the dashboard
       still boots (no crash) and `GET /spa/` returns 404. This proves
       the mount is OPTIONAL — a fresh checkout without `pnpm build`
       keeps working.

We deliberately don't test the full client-side routing fallback
(`GET /spa/kanban` → index.html) here — that's `StaticFiles(html=True)`
behavior which FastAPI/Starlette owns and covers in their own suite.
The manual `curl` step in the deliverable verifies it end-to-end.
"""

from __future__ import annotations

import json
from pathlib import Path

import pytest


# ---- Fixture builders (mirror the CORS test helpers) --------------------


def _make_project(tmp_path: Path):
    """Build a minimal project skeleton the dashboard can boot against."""
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

    return ProjectPaths(
        project_root=root,
        project_id="proj",
        config_yaml=root / "orchestrator" / "config.yaml",
        explicit_root=True,
        state_layout="legacy",
    )


def _client(paths):
    pytest.importorskip("jinja2")
    pytest.importorskip("fastapi")
    from fastapi.testclient import TestClient
    from orchestrator.dashboard.server import create_app

    app = create_app(paths=paths)
    return TestClient(app)


# ---- Test A: /spa/ returns 200 with the fixture body --------------------


def test_spa_mount_serves_index_when_dist_exists(tmp_path: Path) -> None:
    """With frontend/dist/index.html present, GET /spa/ returns it 200."""
    paths = _make_project(tmp_path)

    # Materialize a fake compiled SPA — one marker-bearing index.html is
    # enough to prove the mount is wired. StaticFiles doesn't validate the
    # bytes, so we don't need a real Vite bundle here.
    dist = paths.project_root / "frontend" / "dist"
    dist.mkdir(parents=True)
    marker = "<!-- spa-mount-fixture-marker -->"
    (dist / "index.html").write_text(
        f"<!doctype html><html><body>{marker}</body></html>",
        encoding="utf-8",
    )

    client = _client(paths)
    r = client.get("/spa/")
    assert r.status_code == 200, r.text
    assert marker in r.text

    # Client-side route fallback: React Router hard-reload on `/spa/kanban`
    # must return `index.html` too — StaticFiles(html=True) alone doesn't
    # do this, so we subclass. Missing bundled assets still stay 404.
    r_route = client.get("/spa/kanban")
    assert r_route.status_code == 200, r_route.text
    assert marker in r_route.text

    r_missing_asset = client.get("/spa/assets/does-not-exist.js")
    assert r_missing_asset.status_code == 404, (
        "missing bundled asset must NOT fall back to index.html — "
        "that would mask build breakage"
    )


# ---- Test B: absent dist → 404 but boot succeeds ------------------------


def test_spa_mount_absent_does_not_crash_and_returns_404(tmp_path: Path) -> None:
    """No frontend/dist → dashboard still boots, /spa/ is 404."""
    paths = _make_project(tmp_path)
    # Deliberately do NOT create frontend/dist/.
    assert not (paths.project_root / "frontend" / "dist").exists()

    # Boot must not raise — this is the "OPTIONAL mount" invariant.
    client = _client(paths)

    # Jinja dashboard still works on `/` — the SPA absence doesn't break
    # the app at large.
    r_root = client.get("/")
    assert r_root.status_code == 200

    # /spa/ has nothing to serve → 404 (no mount registered).
    r_spa = client.get("/spa/")
    assert r_spa.status_code == 404
