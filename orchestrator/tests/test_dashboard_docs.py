"""Tests for GET /api/docs and GET /api/docs/content endpoints.

Coverage:
  - /api/docs returns list of markdown files from docs/, specs/, openspec/
  - /api/docs/content serves file content
  - path traversal is rejected (400)
  - non-.md extension is rejected (400)
  - missing file returns 404
  - both endpoints are on the stakeholder allow-list (200 with valid token)
"""

from __future__ import annotations

import json
from pathlib import Path

import pytest


def _make_project(tmp_path: Path):
    from orchestrator.paths import ProjectPaths

    root = tmp_path / "proj"
    (root / "orchestrator" / "state").mkdir(parents=True)
    (root / "scripts").mkdir(parents=True)
    (root / "scripts" / "task-start.sh").write_text("#!/bin/sh\nexit 0\n")
    (root / "tasks.json").write_text(
        json.dumps({"meta": {}, "phases": [], "tasks": []}), encoding="utf-8"
    )

    # Scaffold doc directories
    (root / "docs" / "prd").mkdir(parents=True)
    (root / "docs" / "arch").mkdir(parents=True)
    (root / "specs" / "F0").mkdir(parents=True)
    (root / "openspec").mkdir(parents=True)

    (root / "docs" / "prd" / "001-vision.md").write_text(
        "# Product Vision\n\nWe build cool stuff.\n", encoding="utf-8"
    )
    (root / "docs" / "arch" / "overview.md").write_text(
        "# Architecture Overview\n\nHexagonal.\n", encoding="utf-8"
    )
    (root / "specs" / "F0" / "auth.md").write_text(
        "# Auth Spec\n\nJWT tokens.\n", encoding="utf-8"
    )
    # File without heading — title falls back to stem
    (root / "openspec" / "README.md").write_text(
        "Plain text, no heading.\n", encoding="utf-8"
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

    paths = _make_project(tmp_path)
    app = create_app(
        paths=paths,
        profile_override=override.get("profile"),
        token_override=override.get("token"),
    )
    return TestClient(app)


# ---- /api/docs list ---------------------------------------------------------


def test_docs_list_returns_all_md_files(tmp_path: Path) -> None:
    client = _client(tmp_path)
    r = client.get("/api/docs")
    assert r.status_code == 200
    paths = {d["path"] for d in r.json()["docs"]}
    assert "docs/prd/001-vision.md" in paths
    assert "docs/arch/overview.md" in paths
    assert "specs/F0/auth.md" in paths
    assert "openspec/README.md" in paths


def test_docs_list_extracts_heading_as_title(tmp_path: Path) -> None:
    client = _client(tmp_path)
    r = client.get("/api/docs")
    docs = {d["path"]: d for d in r.json()["docs"]}
    assert docs["docs/prd/001-vision.md"]["title"] == "Product Vision"
    assert docs["docs/arch/overview.md"]["title"] == "Architecture Overview"


def test_docs_list_fallback_title_when_no_heading(tmp_path: Path) -> None:
    client = _client(tmp_path)
    r = client.get("/api/docs")
    docs = {d["path"]: d for d in r.json()["docs"]}
    # README has no # heading → stem "README" or title-cased
    assert "readme" in docs["openspec/README.md"]["title"].lower()


def test_docs_list_includes_category(tmp_path: Path) -> None:
    client = _client(tmp_path)
    r = client.get("/api/docs")
    docs = {d["path"]: d for d in r.json()["docs"]}
    assert docs["docs/prd/001-vision.md"]["category"] == "Docs"
    assert docs["specs/F0/auth.md"]["category"] == "Specs"
    assert docs["openspec/README.md"]["category"] == "OpenSpec"


# ---- /api/docs/content ------------------------------------------------------


def test_docs_content_serves_file(tmp_path: Path) -> None:
    client = _client(tmp_path)
    r = client.get("/api/docs/content?path=docs/prd/001-vision.md")
    assert r.status_code == 200
    assert "Product Vision" in r.text


def test_docs_content_rejects_path_traversal(tmp_path: Path) -> None:
    client = _client(tmp_path)
    r = client.get("/api/docs/content?path=../../etc/passwd")
    assert r.status_code == 400


def test_docs_content_rejects_non_md_extension(tmp_path: Path) -> None:
    client = _client(tmp_path)
    r = client.get("/api/docs/content?path=docs/prd/001-vision.txt")
    assert r.status_code == 400


def test_docs_content_404_for_missing_file(tmp_path: Path) -> None:
    client = _client(tmp_path)
    r = client.get("/api/docs/content?path=docs/prd/nonexistent.md")
    assert r.status_code == 404


# ---- Stakeholder allow-list -------------------------------------------------


def test_docs_list_accessible_in_stakeholder_mode(tmp_path: Path) -> None:
    client = _client(tmp_path, profile="stakeholder", token="s3cr3t")
    r = client.get("/api/docs", headers={"Authorization": "Bearer s3cr3t"})
    assert r.status_code == 200


def test_docs_content_accessible_in_stakeholder_mode(tmp_path: Path) -> None:
    client = _client(tmp_path, profile="stakeholder", token="s3cr3t")
    r = client.get(
        "/api/docs/content?path=docs/prd/001-vision.md",
        headers={"Authorization": "Bearer s3cr3t"},
    )
    assert r.status_code == 200
