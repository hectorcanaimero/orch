"""Tests for the `orch doctor` tunnel checks — Sprint E-5, TUN-11.

Covers `preflight.check_tunnel(dashboard_yaml)` which produces two rows:
`tunnel.config` (parse status) and `tunnel.binary` (provider binary on PATH).
The wiring into `build_doctor_report` is exercised by the CLI / dashboard
tests; here we assert the atomic-behavior contract.
"""

from __future__ import annotations

from pathlib import Path

import pytest

from orchestrator.preflight import check_tunnel


def _write(path: Path, body: str) -> Path:
    path.write_text(body, encoding="utf-8")
    return path


def _by_name(results, name: str):
    for r in results:
        if r.name == name:
            return r
    raise AssertionError(f"missing check {name!r}; got {[r.name for r in results]}")


# ---- tunnel.config ----------------------------------------------------------


def test_tunnel_config_ok_when_dashboard_yaml_absent(tmp_path: Path) -> None:
    dash = tmp_path / "dashboard.yaml"
    results = check_tunnel(dash)
    config_row = _by_name(results, "tunnel.config")
    binary_row = _by_name(results, "tunnel.binary")
    assert config_row.status == "ok"
    assert binary_row.status == "ok"


def test_tunnel_config_ok_when_section_absent(tmp_path: Path) -> None:
    dash = _write(
        tmp_path / "dashboard.yaml",
        "kanban:\n  refresh_interval_s: 5\n",
    )
    results = check_tunnel(dash)
    row = _by_name(results, "tunnel.config")
    assert row.status == "ok"
    assert "no tunnel section" in row.detail


def test_tunnel_config_ok_when_section_valid(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    import shutil as _sh

    monkeypatch.setattr(_sh, "which", lambda name: f"/usr/local/bin/{name}")
    dash = _write(
        tmp_path / "dashboard.yaml",
        "tunnel:\n  enabled: true\n  provider: autossh\n",
    )
    results = check_tunnel(dash)
    row = _by_name(results, "tunnel.config")
    assert row.status == "ok"
    assert "provider=autossh" in row.detail
    assert "enabled=True" in row.detail


def test_tunnel_config_fails_on_unknown_provider(tmp_path: Path) -> None:
    dash = _write(
        tmp_path / "dashboard.yaml",
        "tunnel:\n  enabled: true\n  provider: ngrok\n",
    )
    results = check_tunnel(dash)
    config_row = _by_name(results, "tunnel.config")
    binary_row = _by_name(results, "tunnel.binary")
    assert config_row.status == "error"
    assert "tunnel.provider" in config_row.detail
    # Binary probe is skipped so we don't misreport when we couldn't validate
    # the config in the first place.
    assert binary_row.status == "skip"


def test_tunnel_config_fails_on_command_override(tmp_path: Path) -> None:
    dash = _write(
        tmp_path / "dashboard.yaml",
        "tunnel:\n"
        "  enabled: true\n"
        "  provider: autossh\n"
        "  command: rm\n",
    )
    results = check_tunnel(dash)
    row = _by_name(results, "tunnel.config")
    assert row.status == "error"
    assert "tunnel.command" in row.detail


def test_tunnel_config_fails_on_out_of_range_timeout(tmp_path: Path) -> None:
    dash = _write(
        tmp_path / "dashboard.yaml",
        "tunnel:\n"
        "  enabled: true\n"
        "  provider: autossh\n"
        "  startup_probe_timeout_s: 999\n",
    )
    results = check_tunnel(dash)
    row = _by_name(results, "tunnel.config")
    assert row.status == "error"
    assert "startup_probe_timeout_s" in row.detail


def test_tunnel_config_fails_on_malformed_yaml(tmp_path: Path) -> None:
    dash = _write(
        tmp_path / "dashboard.yaml",
        "tunnel: [not a mapping\n",
    )
    results = check_tunnel(dash)
    row = _by_name(results, "tunnel.config")
    assert row.status == "error"


# ---- tunnel.binary ----------------------------------------------------------


def test_tunnel_binary_ok_when_present(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    import shutil as _sh

    monkeypatch.setattr(_sh, "which", lambda name: f"/opt/homebrew/bin/{name}")
    dash = _write(
        tmp_path / "dashboard.yaml",
        "tunnel:\n  enabled: true\n  provider: autossh\n",
    )
    results = check_tunnel(dash)
    row = _by_name(results, "tunnel.binary")
    assert row.status == "ok"
    assert "autossh" in row.detail
    assert "/opt/homebrew/bin/autossh" in row.detail


def test_tunnel_binary_fails_when_missing_and_enabled(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    import shutil as _sh

    monkeypatch.setattr(_sh, "which", lambda name: None)
    dash = _write(
        tmp_path / "dashboard.yaml",
        "tunnel:\n  enabled: true\n  provider: autossh\n",
    )
    results = check_tunnel(dash)
    row = _by_name(results, "tunnel.binary")
    assert row.status == "error"
    assert "autossh" in row.detail
    assert row.remediation and "autossh" in row.remediation


def test_tunnel_binary_warns_when_missing_and_disabled(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    import shutil as _sh

    monkeypatch.setattr(_sh, "which", lambda name: None)
    dash = _write(
        tmp_path / "dashboard.yaml",
        "tunnel:\n  enabled: false\n  provider: autossh\n",
    )
    results = check_tunnel(dash)
    row = _by_name(results, "tunnel.binary")
    assert row.status == "warn"
    assert "autossh" in row.detail


def test_tunnel_binary_ok_when_disabled_and_binary_present(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    import shutil as _sh

    monkeypatch.setattr(_sh, "which", lambda name: f"/usr/bin/{name}")
    dash = _write(
        tmp_path / "dashboard.yaml",
        "tunnel:\n  enabled: false\n  provider: autossh\n",
    )
    results = check_tunnel(dash)
    row = _by_name(results, "tunnel.binary")
    assert row.status == "ok"


def test_tunnel_binary_ok_when_dashboard_absent(tmp_path: Path) -> None:
    dash = tmp_path / "dashboard.yaml"
    results = check_tunnel(dash)
    row = _by_name(results, "tunnel.binary")
    assert row.status == "ok"


# ---- Integration with build_doctor_report ----------------------------------


def test_build_doctor_report_includes_tunnel_checks(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """Wiring check: `orch doctor` payload MUST contain both tunnel rows."""
    import json
    import stat

    from orchestrator.doctor import build_doctor_report
    from orchestrator.paths import ProjectPaths

    root = tmp_path / "proj"
    (root / ".orchestrator" / "state").mkdir(parents=True)
    (root / "scripts").mkdir(parents=True)
    start = root / "scripts" / "task-start.sh"
    start.write_text("#!/bin/sh\nexit 0\n")
    start.chmod(start.stat().st_mode | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)
    (root / "tasks.json").write_text(
        json.dumps({"meta": {}, "phases": [], "tasks": []}),
        encoding="utf-8",
    )
    cfg_path = root / ".orchestrator" / "config.yaml"
    cfg_path.write_text("state:\n  backend: file\n", encoding="utf-8")
    (root / ".orchestrator" / "model_router.yaml").write_text(
        "claude-4:\n  backend: claude\n  model: claude-4\n", encoding="utf-8",
    )
    (root / "dashboard.yaml").write_text(
        "tunnel:\n  enabled: true\n  provider: autossh\n", encoding="utf-8",
    )

    import shutil as _sh

    monkeypatch.setattr(_sh, "which", lambda name: f"/opt/homebrew/bin/{name}")

    paths = ProjectPaths(
        project_root=root,
        project_id="proj",
        config_yaml=cfg_path,
        explicit_root=True,
        state_layout="legacy",
    )

    def _loader(path: Path) -> dict:
        import yaml

        return yaml.safe_load(path.read_text(encoding="utf-8")) or {}

    payload = build_doctor_report(paths, config_loader=_loader)
    names = {c["name"] for c in payload["checks"]}
    assert "tunnel.config" in names
    assert "tunnel.binary" in names
