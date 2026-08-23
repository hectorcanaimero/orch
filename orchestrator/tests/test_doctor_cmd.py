"""Tests for the `orch doctor` subcommand (Sprint D — commit 2).

Covers:
    * JSON output shape + summary counts.
    * Exit codes: 0 (all ok), 1 (warn only), 2 (any error).
    * Backend detection uses `shutil.which` — mocked so tests are hermetic.
    * State backend probe: file (skips DB), sqlite (opens fresh DB).
    * `--only` substring filter narrows the checklist.
"""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from orchestrator.orch import _run_doctor_subcommand
from orchestrator.state import _reset_backend_cache
from orchestrator.state.sqlite_backend import _reset_schema_cache_for_tests


@pytest.fixture(autouse=True)
def _clear_caches():
    _reset_backend_cache()
    _reset_schema_cache_for_tests()
    yield
    _reset_backend_cache()
    _reset_schema_cache_for_tests()


def _scaffold_project(root: Path, backend_kind: str = "file") -> None:
    """Minimal orch project layout that satisfies `paths.ensure_valid`."""
    root.mkdir(parents=True, exist_ok=True)
    (root / "tasks.json").write_text(
        json.dumps(
            {
                "meta": {},
                "phases": [{"id": 1, "name": "test"}],
                "tasks": [
                    {
                        "id": "T-A",
                        "phase": 1,
                        "title": "Test",
                        "description": "",
                        "model": "opencode/claude-sonnet-4-6",
                        "reason": "",
                        "status": "todo",
                        "dependencies": [],
                        "estimateHours": 0.5,
                        "files": [],
                        "specRef": "",
                        "comments": [],
                    }
                ],
            }
        )
    )
    scripts = root / "scripts"
    scripts.mkdir()
    for name in ("task-start.sh", "task-finish.sh", "task-block.sh"):
        (scripts / name).write_text("#!/usr/bin/env bash\nexit 0\n")
        (scripts / name).chmod(0o755)
    orch_dir = root / "orchestrator"
    orch_dir.mkdir()
    (orch_dir / "config.yaml").write_text(
        f"state:\n  backend: {backend_kind}\n  sqlite_path: null\n"
        "budgets_config: budgets.yaml\n"
        "budgets_preset: conservative\n"
        "typical_dispatch_tokens: 200000\n"
    )
    (orch_dir / "model_router.yaml").write_text(
        "opencode/claude-sonnet-4-6:\n"
        "  backend: claude\n"
        "  cli_model: claude-sonnet-4-6\n"
        "  tier: standard\n"
    )
    (orch_dir / "budgets.yaml").write_text(
        "presets:\n"
        "  conservative:\n"
        "    claude:\n"
        "      window_hours: 5\n"
        "      token_budget: 800000\n"
        "      threshold_pct: 60\n"
    )


def _common_args(root: Path) -> list[str]:
    return [
        "--project-root", str(root),
        "--project-id", "proj-doctor",
        "--config", "orchestrator/config.yaml",
    ]


@pytest.fixture(params=["file", "sqlite"], ids=lambda b: f"backend={b}")
def project(request, tmp_path: Path) -> tuple[Path, str]:
    root = tmp_path / "proj"
    _scaffold_project(root, backend_kind=request.param)
    return root, request.param


def _fake_which(hits: dict[str, str]):
    def _inner(name: str) -> str | None:
        return hits.get(name)
    return _inner


def test_doctor_json_shape_with_all_backends_present(
    project, capsys: pytest.CaptureFixture, monkeypatch
) -> None:
    root, backend = project
    monkeypatch.setattr(
        "shutil.which",
        _fake_which({
            "claude": "/usr/local/bin/claude",
            "codex": "/usr/local/bin/codex",
            "opencode": "/usr/local/bin/opencode",
            "jq": "/usr/bin/jq",
        }),
    )
    # Version + auth probes shell out — stub them so the test is hermetic.
    monkeypatch.setattr(
        "orchestrator.preflight._probe_version",
        lambda cli: (True, f"{cli} 1.0.0"),
    )
    monkeypatch.setattr(
        "orchestrator.preflight._probe_opencode_auth",
        lambda: __import__("orchestrator.preflight", fromlist=["CheckResult"]).CheckResult(
            name="backend.opencode.auth", status="ok", detail="ok"
        ),
    )
    rc = _run_doctor_subcommand([*_common_args(root), "--json"])
    payload = json.loads(capsys.readouterr().out)
    assert set(payload.keys()) >= {"project", "backend", "checks", "summary", "exit_code"}
    assert payload["project"]["id"] == "proj-doctor"
    assert payload["backend"] == backend
    assert payload["exit_code"] == rc
    # tasks.json references only opencode/claude-sonnet-4-6 → backend=claude
    # is the only one probed. This is the referenced-backends-only shortcut.
    names = {c["name"] for c in payload["checks"]}
    assert "backend.claude" in names
    assert payload["summary"]["error"] == 0


def test_doctor_exit_code_2_when_backend_missing(
    project, capsys: pytest.CaptureFixture, monkeypatch
) -> None:
    root, _ = project
    # No CLIs on PATH except jq — every backend probe fails.
    monkeypatch.setattr(
        "shutil.which",
        _fake_which({"jq": "/usr/bin/jq"}),
    )
    rc = _run_doctor_subcommand([*_common_args(root), "--json"])
    payload = json.loads(capsys.readouterr().out)
    assert rc == 2
    assert payload["exit_code"] == 2
    assert payload["summary"]["error"] >= 1


def test_doctor_exit_code_1_when_only_warn(
    project, capsys: pytest.CaptureFixture, monkeypatch
) -> None:
    root, backend = project
    # Non-executable scripts trigger a warn, no errors otherwise.
    scripts = root / "scripts"
    for name in ("task-start.sh", "task-finish.sh", "task-block.sh"):
        (scripts / name).chmod(0o644)
    monkeypatch.setattr(
        "shutil.which",
        _fake_which({
            "claude": "/usr/local/bin/claude",
            "codex": "/usr/local/bin/codex",
            "opencode": "/usr/local/bin/opencode",
            "jq": "/usr/bin/jq",
        }),
    )
    monkeypatch.setattr(
        "orchestrator.preflight._probe_version",
        lambda cli: (True, f"{cli} 1.0.0"),
    )
    monkeypatch.setattr(
        "orchestrator.preflight._probe_opencode_auth",
        lambda: __import__("orchestrator.preflight", fromlist=["CheckResult"]).CheckResult(
            name="backend.opencode.auth", status="ok", detail="ok"
        ),
    )
    rc = _run_doctor_subcommand([*_common_args(root), "--json"])
    payload = json.loads(capsys.readouterr().out)
    assert rc == 1
    assert payload["summary"]["warn"] >= 1
    assert payload["summary"]["error"] == 0


def test_doctor_human_output_shows_summary_line(
    project, capsys: pytest.CaptureFixture, monkeypatch
) -> None:
    root, _ = project
    monkeypatch.setattr(
        "shutil.which",
        _fake_which({
            "claude": "/usr/local/bin/claude",
            "codex": "/usr/local/bin/codex",
            "opencode": "/usr/local/bin/opencode",
            "jq": "/usr/bin/jq",
        }),
    )
    monkeypatch.setattr(
        "orchestrator.preflight._probe_version",
        lambda cli: (True, f"{cli} 1.0.0"),
    )
    monkeypatch.setattr(
        "orchestrator.preflight._probe_opencode_auth",
        lambda: __import__("orchestrator.preflight", fromlist=["CheckResult"]).CheckResult(
            name="backend.opencode.auth", status="ok", detail="ok"
        ),
    )
    _run_doctor_subcommand(_common_args(root))
    out = capsys.readouterr().out
    assert "proj-doctor" in out
    assert "ok" in out.lower()


def test_doctor_only_filter_narrows_checks(
    project, capsys: pytest.CaptureFixture, monkeypatch
) -> None:
    root, _ = project
    monkeypatch.setattr("shutil.which", _fake_which({"jq": "/usr/bin/jq"}))
    _run_doctor_subcommand([*_common_args(root), "--json", "--only", "scripts"])
    payload = json.loads(capsys.readouterr().out)
    names = {c["name"] for c in payload["checks"]}
    # Every remaining check should contain 'scripts' in its name.
    assert all("scripts" in n for n in names)
    assert len(names) >= 3  # 3 task-*.sh scripts


def test_doctor_reports_state_db_ok_for_sqlite(
    tmp_path: Path, capsys: pytest.CaptureFixture, monkeypatch
) -> None:
    root = tmp_path / "proj"
    _scaffold_project(root, backend_kind="sqlite")
    monkeypatch.setattr(
        "shutil.which",
        _fake_which({
            "claude": "/usr/local/bin/claude",
            "codex": "/usr/local/bin/codex",
            "opencode": "/usr/local/bin/opencode",
            "jq": "/usr/bin/jq",
        }),
    )
    monkeypatch.setattr(
        "orchestrator.preflight._probe_version",
        lambda cli: (True, f"{cli} 1.0.0"),
    )
    monkeypatch.setattr(
        "orchestrator.preflight._probe_opencode_auth",
        lambda: __import__("orchestrator.preflight", fromlist=["CheckResult"]).CheckResult(
            name="backend.opencode.auth", status="ok", detail="ok"
        ),
    )
    _run_doctor_subcommand([*_common_args(root), "--json"])
    payload = json.loads(capsys.readouterr().out)
    db_check = next(c for c in payload["checks"] if c["name"] == "state.db.accessible")
    # Fresh DB — schema version 0, expected 2 → warn.
    assert db_check["status"] in ("ok", "warn")


def test_doctor_missing_config_file_returns_error(
    tmp_path: Path, capsys: pytest.CaptureFixture, monkeypatch
) -> None:
    root = tmp_path / "bare"
    root.mkdir()
    # Only scripts + jq, no config/router/tasks.
    monkeypatch.setattr("shutil.which", _fake_which({"jq": "/usr/bin/jq"}))
    rc = _run_doctor_subcommand([
        "--project-root", str(root),
        "--config", "orchestrator/config.yaml",
        "--json",
    ])
    payload = json.loads(capsys.readouterr().out)
    assert rc == 2
    # config.parse should surface as an error.
    cfg_check = next(c for c in payload["checks"] if c["name"] == "config.parse")
    assert cfg_check["status"] == "error"


def test_doctor_referenced_backends_only(
    project, capsys: pytest.CaptureFixture, monkeypatch
) -> None:
    """When the router resolves, only reference-in-use backends are probed."""
    root, _ = project
    # tasks.json uses opencode/claude-sonnet-4-6 → backend=claude only.
    monkeypatch.setattr("shutil.which", _fake_which({
        "claude": "/usr/local/bin/claude",
        "jq": "/usr/bin/jq",
    }))
    monkeypatch.setattr(
        "orchestrator.preflight._probe_version",
        lambda cli: (True, f"{cli} 1.0.0"),
    )
    _run_doctor_subcommand([*_common_args(root), "--json"])
    payload = json.loads(capsys.readouterr().out)
    names = {c["name"] for c in payload["checks"]}
    assert "backend.claude" in names
    assert "backend.codex" not in names
    assert "backend.opencode" not in names
