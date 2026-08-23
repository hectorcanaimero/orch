"""Tests for the `server:` block in `dashboard.yaml` — per-project sticky
bind (host/port) support.

Covers two layers:
    1. `parse_server_section` and `DashboardConfig.load()` — YAML parsing
       and validation of the top-level `server:` block.
    2. `_resolve_dashboard_bind` — precedence ladder used by `orch
       dashboard` after argparse (CLI flag > env > config > default).
"""

from __future__ import annotations

import os
from pathlib import Path
from textwrap import dedent

import pytest

from orchestrator.dashboard.dashboard_config import (
    ConfigError,
    DashboardConfig,
    parse_server_section,
)


# ---- parse_server_section --------------------------------------------------


def test_parse_absent_block_returns_two_nones() -> None:
    assert parse_server_section(None) == (None, None)


def test_parse_empty_mapping_returns_two_nones() -> None:
    # `server: {}` in YAML — mapping present but no keys set.
    assert parse_server_section({}) == (None, None)


def test_parse_only_port_leaves_host_none() -> None:
    port, host = parse_server_section({"port": 7500})
    assert port == 7500
    assert host is None


def test_parse_only_host_leaves_port_none() -> None:
    port, host = parse_server_section({"host": "0.0.0.0"})
    assert port is None
    assert host == "0.0.0.0"


def test_parse_full_block() -> None:
    port, host = parse_server_section({"port": 7421, "host": "127.0.0.1"})
    assert port == 7421
    assert host == "127.0.0.1"


def test_parse_strips_whitespace_from_host() -> None:
    port, host = parse_server_section({"host": "  0.0.0.0  "})
    assert host == "0.0.0.0"
    assert port is None


def test_parse_rejects_non_mapping_section() -> None:
    with pytest.raises(ConfigError, match="server"):
        parse_server_section(["port", 7420])


@pytest.mark.parametrize("bad_port", [0, -1, 65536, 70000])
def test_parse_rejects_out_of_range_port(bad_port: int) -> None:
    with pytest.raises(ConfigError, match="server.port"):
        parse_server_section({"port": bad_port})


def test_parse_rejects_non_int_port() -> None:
    with pytest.raises(ConfigError, match="server.port"):
        parse_server_section({"port": "abc"})


def test_parse_rejects_bool_port() -> None:
    # `bool` is an int subclass in Python — reject explicitly.
    with pytest.raises(ConfigError, match="server.port"):
        parse_server_section({"port": True})


def test_parse_rejects_empty_host() -> None:
    with pytest.raises(ConfigError, match="server.host"):
        parse_server_section({"host": "   "})


def test_parse_rejects_non_string_host() -> None:
    with pytest.raises(ConfigError, match="server.host"):
        parse_server_section({"host": 12345})


# ---- DashboardConfig.load integration --------------------------------------


def test_dashboard_config_load_absent_server_stays_none(tmp_path: Path) -> None:
    proj = tmp_path / "proj"
    proj.mkdir()
    (proj / "dashboard.yaml").write_text(
        "kanban:\n  refresh_interval_s: 3\n", encoding="utf-8"
    )
    cfg = DashboardConfig.load(proj)
    assert cfg.server_port is None
    assert cfg.server_host is None


def test_dashboard_config_load_reads_server_block(tmp_path: Path) -> None:
    proj = tmp_path / "proj"
    proj.mkdir()
    (proj / "dashboard.yaml").write_text(
        dedent(
            """\
            server:
              port: 7500
              host: 0.0.0.0
            """
        ),
        encoding="utf-8",
    )
    cfg = DashboardConfig.load(proj)
    assert cfg.server_port == 7500
    assert cfg.server_host == "0.0.0.0"


def test_dashboard_config_load_raises_on_invalid_server_port(tmp_path: Path) -> None:
    proj = tmp_path / "proj"
    proj.mkdir()
    (proj / "dashboard.yaml").write_text(
        "server:\n  port: 0\n", encoding="utf-8"
    )
    with pytest.raises(ConfigError, match="server.port"):
        DashboardConfig.load(proj)


def test_dashboard_config_load_raises_on_invalid_server_host(tmp_path: Path) -> None:
    proj = tmp_path / "proj"
    proj.mkdir()
    (proj / "dashboard.yaml").write_text(
        "server:\n  host: ''\n", encoding="utf-8"
    )
    with pytest.raises(ConfigError, match="server.host"):
        DashboardConfig.load(proj)


# ---- _resolve_dashboard_bind precedence -----------------------------------


@pytest.fixture(autouse=True)
def _clean_bind_env(monkeypatch: pytest.MonkeyPatch):
    """Strip the bind-related env vars so tests don't inherit shell state."""
    for k in ("ORCH_DASHBOARD_PORT", "ORCH_DASHBOARD_HOST"):
        monkeypatch.delenv(k, raising=False)
    yield


def _write_server_yaml(root: Path, *, port: int | None, host: str | None) -> None:
    root.mkdir(parents=True, exist_ok=True)
    lines = ["server:"]
    if port is not None:
        lines.append(f"  port: {port}")
    if host is not None:
        lines.append(f"  host: {host}")
    (root / "dashboard.yaml").write_text("\n".join(lines) + "\n", encoding="utf-8")


def test_resolve_falls_back_to_hardcoded_defaults(tmp_path: Path) -> None:
    from orchestrator.orch import _resolve_dashboard_bind

    port, host = _resolve_dashboard_bind(
        cli_port=None, cli_host=None, project_root=str(tmp_path)
    )
    assert port == 7420
    assert host == "127.0.0.1"


def test_resolve_config_wins_over_default(tmp_path: Path) -> None:
    from orchestrator.orch import _resolve_dashboard_bind

    _write_server_yaml(tmp_path, port=7500, host="0.0.0.0")
    port, host = _resolve_dashboard_bind(
        cli_port=None, cli_host=None, project_root=str(tmp_path)
    )
    assert port == 7500
    assert host == "0.0.0.0"


def test_resolve_env_wins_over_config(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    from orchestrator.orch import _resolve_dashboard_bind

    _write_server_yaml(tmp_path, port=7500, host="0.0.0.0")
    monkeypatch.setenv("ORCH_DASHBOARD_PORT", "7600")
    monkeypatch.setenv("ORCH_DASHBOARD_HOST", "10.0.0.5")
    port, host = _resolve_dashboard_bind(
        cli_port=None, cli_host=None, project_root=str(tmp_path)
    )
    assert port == 7600
    assert host == "10.0.0.5"


def test_resolve_cli_wins_over_env_and_config(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    from orchestrator.orch import _resolve_dashboard_bind

    _write_server_yaml(tmp_path, port=7500, host="0.0.0.0")
    monkeypatch.setenv("ORCH_DASHBOARD_PORT", "7600")
    monkeypatch.setenv("ORCH_DASHBOARD_HOST", "10.0.0.5")
    port, host = _resolve_dashboard_bind(
        cli_port=7777, cli_host="192.168.1.1", project_root=str(tmp_path)
    )
    assert port == 7777
    assert host == "192.168.1.1"


def test_resolve_channels_are_independent(tmp_path: Path) -> None:
    """CLI --port + config-only host → (cli_port, config_host)."""
    from orchestrator.orch import _resolve_dashboard_bind

    _write_server_yaml(tmp_path, port=7500, host="0.0.0.0")
    port, host = _resolve_dashboard_bind(
        cli_port=9000, cli_host=None, project_root=str(tmp_path)
    )
    assert port == 9000
    assert host == "0.0.0.0"


def test_resolve_ignores_invalid_env_port(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, capsys
) -> None:
    from orchestrator.orch import _resolve_dashboard_bind

    _write_server_yaml(tmp_path, port=7500, host=None)
    monkeypatch.setenv("ORCH_DASHBOARD_PORT", "notanumber")
    port, host = _resolve_dashboard_bind(
        cli_port=None, cli_host=None, project_root=str(tmp_path)
    )
    # Bad env falls through to config.
    assert port == 7500
    err = capsys.readouterr().err
    assert "ORCH_DASHBOARD_PORT" in err
