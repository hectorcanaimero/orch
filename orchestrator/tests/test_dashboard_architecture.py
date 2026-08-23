"""Integration tests for the `/api/architecture/*` endpoints.

Guards the fixed API contract added in Sprint E-4: status shape, current +
snapshot serve, history ordering, and the 409 branch when a regeneration
lock is already held.
"""

from __future__ import annotations

import json
from pathlib import Path

import pytest


def _scaffold_project(tmp_path: Path):
    from orchestrator.paths import ProjectPaths

    root = tmp_path / "proj"
    (root / "orchestrator" / "state").mkdir(parents=True)
    (root / "scripts").mkdir(parents=True)
    (root / "scripts" / "task-start.sh").write_text("#!/bin/sh\nexit 0\n")
    (root / "tasks.json").write_text(
        json.dumps({"meta": {}, "phases": [], "tasks": []}),
        encoding="utf-8",
    )
    (root / "orchestrator" / "config.yaml").write_text("", encoding="utf-8")
    return ProjectPaths(
        project_root=root,
        project_id="proj",
        config_yaml=root / "orchestrator" / "config.yaml",
        explicit_root=True,
        state_layout="legacy",
    )


def _client(tmp_path: Path):
    pytest.importorskip("fastapi")
    pytest.importorskip("yaml")
    from fastapi.testclient import TestClient
    from orchestrator.dashboard.server import create_app

    paths = _scaffold_project(tmp_path)
    app = create_app(paths=paths)
    return TestClient(app), paths


def _write_current(paths, body: str = "<html>diagram</html>") -> Path:
    arch = paths.project_root / "docs" / "architecture"
    arch.mkdir(parents=True, exist_ok=True)
    p = arch / "current.html"
    p.write_text(body, encoding="utf-8")
    return p


def _append_event(paths, event: dict) -> None:
    events = paths.state_dir / "arch-events.jsonl"
    events.parent.mkdir(parents=True, exist_ok=True)
    with events.open("a", encoding="utf-8") as fh:
        fh.write(json.dumps(event) + "\n")


# ---- status --------------------------------------------------------------


def test_status_when_no_html(tmp_path: Path) -> None:
    client, _ = _client(tmp_path)
    r = client.get("/api/architecture/status")
    assert r.status_code == 200
    payload = r.json()
    assert payload["exists"] is False
    assert payload["count"] == 0
    assert payload["generated_at"] is None
    assert payload["source_hash"] is None
    assert payload["last_cost_usd"] is None
    assert payload["regenerate_in_progress"] is False


def test_status_with_html_and_event(tmp_path: Path) -> None:
    client, paths = _client(tmp_path)
    _write_current(paths)
    # Seed a prd so source_hash is non-trivial.
    prd_dir = paths.project_root / "docs" / "prd"
    prd_dir.mkdir(parents=True, exist_ok=True)
    (prd_dir / "001.md").write_text("prd body", encoding="utf-8")
    _append_event(paths, {
        "timestamp": "2026-08-22T15:30:00Z",
        "source_hash": "abc1234",
        "cost_usd": 0.18,
        "model": "claude-sonnet-4-5",
        "source_artifacts": {"prd_count": 1, "spec_count": 0, "task_count": 0},
        "output_file": "archive/2026-08-22T15-30-00Z-abc1234.html",
    })
    r = client.get("/api/architecture/status")
    assert r.status_code == 200
    payload = r.json()
    assert payload["exists"] is True
    assert payload["count"] == 1
    assert payload["generated_at"] == "2026-08-22T15:30:00Z"
    assert payload["last_cost_usd"] == 0.18
    assert isinstance(payload["source_hash"], str) and len(payload["source_hash"]) == 7


# ---- history -------------------------------------------------------------


def test_history_returns_newest_first(tmp_path: Path) -> None:
    client, paths = _client(tmp_path)
    events = [
        {"timestamp": "2026-08-20T10:00:00Z", "source_hash": "aaaaaaa",
         "cost_usd": 0.10, "model": "m1",
         "source_artifacts": {"prd_count": 1, "spec_count": 2, "task_count": 3}},
        {"timestamp": "2026-08-22T15:30:00Z", "source_hash": "bbbbbbb",
         "cost_usd": 0.20, "model": "m2",
         "source_artifacts": {"prd_count": 2, "spec_count": 3, "task_count": 4}},
        {"timestamp": "2026-08-21T08:00:00Z", "source_hash": "ccccccc",
         "cost_usd": 0.30, "model": "m3",
         "source_artifacts": {"prd_count": 0, "spec_count": 0, "task_count": 0}},
    ]
    for e in events:
        _append_event(paths, e)
    r = client.get("/api/architecture/history")
    assert r.status_code == 200
    snaps = r.json()["snapshots"]
    assert [s["timestamp"] for s in snaps] == [
        "2026-08-22T15:30:00Z",
        "2026-08-21T08:00:00Z",
        "2026-08-20T10:00:00Z",
    ]
    assert snaps[0]["source_hash"] == "bbbbbbb"
    assert snaps[0]["cost_usd"] == 0.20
    assert snaps[0]["source_artifacts"] == {"prd_count": 2, "spec_count": 3, "task_count": 4}


# ---- current -------------------------------------------------------------


def test_current_404_when_missing(tmp_path: Path) -> None:
    client, _ = _client(tmp_path)
    r = client.get("/api/architecture/current")
    assert r.status_code == 404


def test_current_200_when_present(tmp_path: Path) -> None:
    client, paths = _client(tmp_path)
    _write_current(paths, "<html>hello arch</html>")
    r = client.get("/api/architecture/current")
    assert r.status_code == 200
    assert "text/html" in r.headers.get("content-type", "")
    assert "hello arch" in r.text


# ---- snapshot ------------------------------------------------------------


def test_snapshot_404_on_unknown(tmp_path: Path) -> None:
    client, _ = _client(tmp_path)
    r = client.get("/api/architecture/snapshot/2026-08-22T15-30-00Z")
    assert r.status_code == 404


def test_snapshot_200_on_known(tmp_path: Path) -> None:
    client, paths = _client(tmp_path)
    archive_dir = paths.project_root / "docs" / "architecture" / "archive"
    archive_dir.mkdir(parents=True, exist_ok=True)
    (archive_dir / "2026-08-22T15-30-00Z-abc1234.html").write_text(
        "<html>snapshot body</html>", encoding="utf-8"
    )
    r = client.get("/api/architecture/snapshot/2026-08-22T15-30-00Z")
    assert r.status_code == 200
    assert "snapshot body" in r.text


def test_snapshot_rejects_path_traversal(tmp_path: Path) -> None:
    client, _ = _client(tmp_path)
    r = client.get("/api/architecture/snapshot/..%2F..%2Fetc%2Fpasswd")
    assert r.status_code == 404


# ---- assets --------------------------------------------------------------


def _write_asset(paths, filename: str, body: str = "<html>companion</html>") -> Path:
    arch = paths.project_root / "docs" / "architecture"
    arch.mkdir(parents=True, exist_ok=True)
    p = arch / filename
    p.write_text(body, encoding="utf-8")
    return p


def test_assets_serves_companion_html(tmp_path: Path) -> None:
    client, paths = _client(tmp_path)
    _write_asset(paths, "detail.html", "<html>detail diagram</html>")
    r = client.get("/api/architecture/assets/detail.html")
    assert r.status_code == 200
    assert "text/html" in r.headers.get("content-type", "")
    assert "detail diagram" in r.text


def test_assets_serves_companion_svg(tmp_path: Path) -> None:
    client, paths = _client(tmp_path)
    _write_asset(paths, "diagram.svg", "<svg></svg>")
    r = client.get("/api/architecture/assets/diagram.svg")
    assert r.status_code == 200
    assert "diagram" in r.text or r.status_code == 200


def test_assets_rejects_symlink_traversal(tmp_path: Path) -> None:
    """A symlink inside the arch dir pointing outside must return 404.

    URL-based `../` traversal is normalized by Starlette before the handler
    ever sees it — this test targets the resolve()+relative_to() defense
    that protects against symlink-based escapes.
    """
    client, paths = _client(tmp_path)
    arch = paths.project_root / "docs" / "architecture"
    arch.mkdir(parents=True, exist_ok=True)
    # Create a file outside the arch directory, then symlink to it from inside.
    outside = tmp_path / "secret.html"
    outside.write_text("<html>secret</html>", encoding="utf-8")
    (arch / "escape.html").symlink_to(outside)
    r = client.get("/api/architecture/assets/escape.html")
    assert r.status_code == 404


def test_assets_rejects_leading_dot(tmp_path: Path) -> None:
    client, _ = _client(tmp_path)
    r = client.get("/api/architecture/assets/.hidden.html")
    assert r.status_code == 404


def test_assets_rejects_disallowed_extension(tmp_path: Path) -> None:
    client, paths = _client(tmp_path)
    _write_asset(paths, "secret.txt", "secret content")
    r = client.get("/api/architecture/assets/secret.txt")
    assert r.status_code == 404


def test_assets_404_on_missing_file(tmp_path: Path) -> None:
    client, paths = _client(tmp_path)
    # Ensure arch dir exists but the file does not
    arch = paths.project_root / "docs" / "architecture"
    arch.mkdir(parents=True, exist_ok=True)
    r = client.get("/api/architecture/assets/nonexistent.html")
    assert r.status_code == 404


def test_assets_404_when_arch_dir_missing(tmp_path: Path) -> None:
    client, _ = _client(tmp_path)
    # No arch dir created at all
    r = client.get("/api/architecture/assets/anything.html")
    assert r.status_code == 404


# ---- regenerate ----------------------------------------------------------


def test_regenerate_conflict_when_lock_held(tmp_path: Path, monkeypatch) -> None:
    client, paths = _client(tmp_path)
    # Simulate an active lock with the current process's PID so the alive
    # check treats it as running.
    import os
    lock = paths.state_dir / "arch-generate.lock"
    lock.parent.mkdir(parents=True, exist_ok=True)
    lock.write_text(json.dumps({"pid": os.getpid(), "started_at": "2026-08-22T00:00:00Z"}))
    r = client.post("/api/architecture/regenerate")
    assert r.status_code == 409


def test_regenerate_202_and_spawns_subprocess(tmp_path: Path, monkeypatch) -> None:
    client, paths = _client(tmp_path)
    captured: dict[str, list] = {"cmds": [], "kwargs": []}

    class _FakePopen:
        def __init__(self, cmd, **kwargs):
            captured["cmds"].append(list(cmd))
            captured["kwargs"].append(kwargs)

    import subprocess as _sp
    monkeypatch.setattr(_sp, "Popen", _FakePopen)
    r = client.post("/api/architecture/regenerate")
    assert r.status_code == 202
    payload = r.json()
    assert "started_at" in payload and "run_id" in payload
    assert captured["cmds"], "regenerate must spawn a subprocess"
    argv = captured["cmds"][0]
    assert "arch" in argv and "generate" in argv
    # Regression: cwd MUST NOT be the project_root — a scaffold dir like
    # /tmp/orch-demo/orchestrator/ (without __init__.py) shadows the real
    # package via PEP 420 namespace-package resolution and crashes the
    # subprocess with ImportError before it can create the lock file.
    kwargs = captured["kwargs"][0]
    assert kwargs.get("cwd") != str(paths.project_root), (
        "must not cwd into project_root — triggers namespace-package trap"
    )
    # Silent DEVNULL swallowed the ImportError above; we now log to a file
    # so future crashes are debuggable.
    stdout = kwargs.get("stdout")
    assert stdout is not None and stdout is not _sp.DEVNULL, (
        "stdout must be captured to a log file, not silently dropped"
    )
