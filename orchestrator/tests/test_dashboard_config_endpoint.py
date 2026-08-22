"""Integration tests for `GET /api/config`.

Guards the whitelist boundary: the endpoint must NEVER dump the raw YAML
untouched, so a config addition (e.g. `secret_token`) cannot accidentally
appear on the wire without an explicit code change.
"""

from __future__ import annotations

import json
from pathlib import Path
from textwrap import dedent

import pytest


EXPECTED_TOP_LEVEL_KEYS = {
    "concurrency",
    "budget",
    "state",
    "retry",
    "spec_root",
    "budgets",
    "findings",
    "dashboard",
    "strict_files_phases",
    "default_timeout_multiplier",
}


def _make_fixture_project(tmp_path: Path, *, config_yaml: str | None):
    from orchestrator.paths import ProjectPaths

    root = tmp_path / "proj"
    (root / "orchestrator" / "state").mkdir(parents=True)
    (root / "scripts").mkdir(parents=True)
    (root / "scripts" / "task-start.sh").write_text("#!/bin/sh\nexit 0\n")

    (root / "tasks.json").write_text(
        json.dumps({"meta": {}, "phases": [], "tasks": []}),
        encoding="utf-8",
    )

    cfg_path = root / "orchestrator" / "config.yaml"
    if config_yaml is not None:
        cfg_path.write_text(config_yaml, encoding="utf-8")

    return ProjectPaths(
        project_root=root,
        project_id="proj",
        config_yaml=cfg_path,
        explicit_root=True,
        state_layout="legacy",
    )


def _client_or_skip(tmp_path: Path, *, config_yaml: str | None):
    pytest.importorskip("fastapi")
    pytest.importorskip("yaml")
    from fastapi.testclient import TestClient
    from orchestrator.dashboard.server import create_app

    paths = _make_fixture_project(tmp_path, config_yaml=config_yaml)
    app = create_app(paths=paths)
    return TestClient(app), paths


_FULL_CONFIG = dedent(
    """\
    concurrency:
      global_max: 8
      per_provider:
        claude: 3
        codex: 2
        opencode: 3
      per_file: 1

    strict_files_phases:
      - 10

    default_timeout_multiplier: 1.5

    budget:
      per_dispatch_usd: 5.00

    retry:
      backoff_seconds: 5
      rate_limit_backoff_seconds: 60

    spec_root: specs

    state:
      backend: file
      sqlite_path: null
    tasks_json_precedence: deps-only

    budgets_config: budgets.yaml
    budgets_preset: conservative
    typical_dispatch_tokens: 200000

    findings:
      publish_repo: "hectorcanaimero/orch"
      publish_rate_limit_per_hour: 3
      label: "auto-reported"
      min_publish_confidence: "medium"
    """
)


def test_api_config_returns_expected_keys(tmp_path: Path) -> None:
    client, _ = _client_or_skip(tmp_path, config_yaml=_FULL_CONFIG)
    r = client.get("/api/config")
    assert r.status_code == 200
    payload = r.json()
    assert set(payload.keys()) == EXPECTED_TOP_LEVEL_KEYS


def test_api_config_reflects_yaml_values(tmp_path: Path) -> None:
    client, _ = _client_or_skip(tmp_path, config_yaml=_FULL_CONFIG)
    payload = client.get("/api/config").json()

    assert payload["concurrency"] == {
        "global_max": 8,
        "per_provider": {"claude": 3, "codex": 2, "opencode": 3},
        "per_file": 1,
    }
    assert payload["budget"] == {"per_dispatch_usd": 5.0}
    assert payload["state"] == {
        "backend": "file",
        "sqlite_path": None,
        "tasks_json_precedence": "deps-only",
    }
    assert payload["retry"] == {
        "backoff_seconds": 5,
        "rate_limit_backoff_seconds": 60,
    }
    assert payload["spec_root"] == "specs"
    assert payload["budgets"] == {
        "config_path": "budgets.yaml",
        "preset": "conservative",
        "typical_dispatch_tokens": 200000,
    }
    assert payload["findings"] == {
        "publish_repo": "hectorcanaimero/orch",
        "publish_rate_limit_per_hour": 3,
        "label": "auto-reported",
        "min_publish_confidence": "medium",
    }
    assert payload["strict_files_phases"] == [10]
    assert payload["default_timeout_multiplier"] == 1.5


def test_api_config_structure_shapes(tmp_path: Path) -> None:
    client, _ = _client_or_skip(tmp_path, config_yaml=_FULL_CONFIG)
    payload = client.get("/api/config").json()

    assert isinstance(payload["concurrency"], dict)
    assert isinstance(payload["concurrency"]["per_provider"], dict)
    assert isinstance(payload["state"], dict)
    assert isinstance(payload["retry"], dict)
    assert {"backoff_seconds", "rate_limit_backoff_seconds"} <= set(payload["retry"].keys())
    assert isinstance(payload["strict_files_phases"], list)
    assert isinstance(payload["budgets"], dict)
    assert isinstance(payload["findings"], dict)


def test_api_config_returns_empty_when_yaml_missing(tmp_path: Path) -> None:
    client, _ = _client_or_skip(tmp_path, config_yaml=None)
    r = client.get("/api/config")
    assert r.status_code == 200
    assert r.json() == {}


def test_api_config_enforces_whitelist_on_unknown_keys(tmp_path: Path) -> None:
    """The endpoint must NEVER echo unknown top-level keys, even if valid YAML."""
    tainted = _FULL_CONFIG + dedent(
        """\

        secret_token: super-secret-value
        undocumented_flag: true
        api_keys:
          openai: sk-should-never-leak
        """
    )
    client, _ = _client_or_skip(tmp_path, config_yaml=tainted)
    r = client.get("/api/config")
    assert r.status_code == 200
    payload = r.json()
    # Whitelist boundary: unknown keys stripped.
    assert "secret_token" not in payload
    assert "undocumented_flag" not in payload
    assert "api_keys" not in payload
    # And they must not leak in the serialized body either.
    raw_body = r.text
    assert "super-secret-value" not in raw_body
    assert "sk-should-never-leak" not in raw_body


def test_api_config_returns_empty_on_malformed_yaml(tmp_path: Path) -> None:
    client, _ = _client_or_skip(tmp_path, config_yaml=":\n  - not: valid: yaml: [")
    r = client.get("/api/config")
    assert r.status_code == 200
    assert r.json() == {}


def test_api_config_partial_yaml_uses_nulls_not_defaults(tmp_path: Path) -> None:
    """When a field is absent, the endpoint must reflect None — not invent a default."""
    partial = dedent(
        """\
        concurrency:
          global_max: 2
        """
    )
    client, _ = _client_or_skip(tmp_path, config_yaml=partial)
    payload = client.get("/api/config").json()
    assert payload["concurrency"]["global_max"] == 2
    assert payload["concurrency"]["per_provider"] == {}
    assert payload["concurrency"]["per_file"] is None
    assert payload["budget"]["per_dispatch_usd"] is None
    assert payload["spec_root"] is None
    assert payload["strict_files_phases"] == []


# ---- Sprint E-3 — `dashboard.board_url` per-project SPA config ------------


def test_api_config_exposes_dashboard_board_url_when_set(tmp_path: Path) -> None:
    """When `dashboard.board_url` is set, the endpoint surfaces it verbatim."""
    with_board = _FULL_CONFIG + dedent(
        """\

        dashboard:
          board_url: "https://draw.z0n.co/editor/abc-123"
        """
    )
    client, _ = _client_or_skip(tmp_path, config_yaml=with_board)
    payload = client.get("/api/config").json()
    assert payload["dashboard"] == {"board_url": "https://draw.z0n.co/editor/abc-123"}


def test_api_config_dashboard_board_url_is_null_when_missing(tmp_path: Path) -> None:
    """When the key is absent, the endpoint reflects None (matching other optionals).

    Doubles as a whitelist guard: even when the operator sets other keys in
    the SAME `dashboard:` YAML block (see `dashboard_config.py` — `token`,
    `stakeholder_routes`, etc.), the endpoint MUST NOT echo them. Grouping
    both assertions here keeps us at the spec'd 9-test count.

    We stay on the default `operator` profile so the middleware doesn't gate
    the request — this test targets `_load_project_config`, not auth.
    """
    tainted = _FULL_CONFIG + dedent(
        """\

        dashboard:
          token: hunter2-super-secret
          stakeholder_routes:
            - some_custom_route
        """
    )
    client, _ = _client_or_skip(tmp_path, config_yaml=tainted)
    r = client.get("/api/config")
    assert r.status_code == 200
    payload = r.json()
    # `dashboard` bucket is always present (whitelist shape); only its inner
    # keys go None when not set — same pattern as `state.sqlite_path`.
    assert payload["dashboard"] == {"board_url": None}
    assert "token" not in payload["dashboard"]
    assert "stakeholder_routes" not in payload["dashboard"]
    assert "hunter2-super-secret" not in r.text
