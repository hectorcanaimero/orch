"""Integration tests for `GET /api/doctor`.

Sprint E-3 SPA — the dashboard's Doctor page hits this endpoint and expects
the exact JSON shape produced by ``orch doctor --json`` (so the SPA can share
types with the CLI). The tests fixture a project layout with at least one
passing check so we can assert the summary counts truly line up with the
``checks`` array.

The endpoint must NOT subprocess out to the CLI — we reuse the shared
``orchestrator.doctor.build_doctor_report`` helper. If a future refactor
re-introduces a subprocess, the fixture-level tests here will still pass but
you'd be paying a fork per dashboard request; keep the code path in-process.
"""

from __future__ import annotations

import json
import stat
from pathlib import Path
from textwrap import dedent

import pytest


REQUIRED_TOP_LEVEL_KEYS = {"project", "backend", "checks", "summary", "exit_code"}
REQUIRED_CHECK_KEYS = {"name", "status", "detail", "remediation"}
VALID_STATUSES = {"ok", "warn", "error", "skip"}


_MINIMAL_CONFIG = dedent(
    """\
    concurrency:
      global_max: 4
    state:
      backend: file
    """
)


_MINIMAL_ROUTER = dedent(
    """\
    claude-4:
      backend: claude
      model: claude-4
    """
)


def _make_fixture_project(tmp_path: Path) -> "object":
    """Build a project layout that produces a mix of ok/skip/error checks.

    We create the config family + one executable script + tasks.json so at
    least ``config.parse``, ``router.parse``, ``tasks.parse`` and
    ``scripts.task-start.sh`` land as ``ok``. The other two scripts stay
    missing so we also cover ``error`` — that guarantees the summary logic
    is exercised across multiple buckets.
    """
    from orchestrator.paths import ProjectPaths

    root = tmp_path / "proj"
    (root / "orchestrator" / "state").mkdir(parents=True)
    (root / "scripts").mkdir(parents=True)

    start = root / "scripts" / "task-start.sh"
    start.write_text("#!/bin/sh\nexit 0\n")
    start.chmod(start.stat().st_mode | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)

    (root / "tasks.json").write_text(
        json.dumps({"meta": {}, "phases": [], "tasks": []}),
        encoding="utf-8",
    )

    cfg_path = root / "orchestrator" / "config.yaml"
    cfg_path.write_text(_MINIMAL_CONFIG, encoding="utf-8")
    (root / "orchestrator" / "model_router.yaml").write_text(
        _MINIMAL_ROUTER, encoding="utf-8",
    )

    return ProjectPaths(
        project_root=root,
        project_id="proj",
        config_yaml=cfg_path,
        explicit_root=True,
        state_layout="legacy",
    )


def _client_or_skip(tmp_path: Path):
    pytest.importorskip("jinja2")
    pytest.importorskip("fastapi")
    pytest.importorskip("yaml")
    from fastapi.testclient import TestClient
    from orchestrator.dashboard.server import create_app

    paths = _make_fixture_project(tmp_path)
    app = create_app(paths=paths)
    return TestClient(app), paths


def test_api_doctor_returns_expected_top_level_keys(tmp_path: Path) -> None:
    client, paths = _client_or_skip(tmp_path)
    r = client.get("/api/doctor")
    assert r.status_code == 200, r.text
    payload = r.json()
    assert REQUIRED_TOP_LEVEL_KEYS <= set(payload.keys())
    # project block echoes the fixture identity so the SPA can render it.
    assert payload["project"] == {"id": paths.project_id, "root": str(paths.project_root)}
    # File backend is the fixture default.
    assert payload["backend"] == "file"


def test_api_doctor_summary_counts_match_checks(tmp_path: Path) -> None:
    client, _ = _client_or_skip(tmp_path)
    payload = client.get("/api/doctor").json()

    checks = payload["checks"]
    summary = payload["summary"]

    assert isinstance(checks, list) and len(checks) > 0
    # Every summary bucket must be present (backend contract).
    assert set(summary.keys()) == {"ok", "warn", "error", "skip"}
    counted = {"ok": 0, "warn": 0, "error": 0, "skip": 0}
    for c in checks:
        assert c["status"] in VALID_STATUSES, c
        counted[c["status"]] += 1
    assert counted == summary
    # exit_code ladder — 0 clean, 1 warn-only, 2 any error.
    if summary["error"] > 0:
        assert payload["exit_code"] == 2
    elif summary["warn"] > 0:
        assert payload["exit_code"] == 1
    else:
        assert payload["exit_code"] == 0


def test_api_doctor_checks_have_required_shape(tmp_path: Path) -> None:
    """Every check row exposes the 4 SPA-contract keys with the right types."""
    client, _ = _client_or_skip(tmp_path)
    payload = client.get("/api/doctor").json()
    checks = payload["checks"]

    assert len(checks) > 0
    # We fixtured at least one passing check (`config.parse` / `scripts.task-start.sh`).
    ok_checks = [c for c in checks if c["status"] == "ok"]
    assert ok_checks, f"expected >=1 ok check in fixture, got: {[(c['name'], c['status']) for c in checks]}"

    for c in checks:
        assert set(c.keys()) == REQUIRED_CHECK_KEYS, c
        assert isinstance(c["name"], str) and c["name"]
        assert isinstance(c["status"], str)
        assert isinstance(c["detail"], str)
        # `remediation` is `str | None` — both types are valid.
        assert c["remediation"] is None or isinstance(c["remediation"], str)
