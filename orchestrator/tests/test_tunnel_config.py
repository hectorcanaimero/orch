"""Tests for `parse_tunnel_section` and `DashboardConfig.tunnel` — Sprint E-5 (TUN-1, TUN-2)."""

from __future__ import annotations

from pathlib import Path

import pytest

from orchestrator.dashboard.dashboard_config import (
    ConfigError,
    DashboardConfig,
    TunnelConfig,
    parse_tunnel_section,
)


# ---- parse_tunnel_section: happy paths ----------------------------------


def test_parse_absent_section_returns_disabled_default() -> None:
    cfg = parse_tunnel_section(None)
    assert isinstance(cfg, TunnelConfig)
    assert cfg.enabled is False
    assert cfg.provider == "autossh"


def test_parse_valid_enabled_block() -> None:
    cfg = parse_tunnel_section({"enabled": True, "provider": "autossh"})
    assert cfg.enabled is True
    assert cfg.provider == "autossh"
    assert cfg.command == "autossh"
    assert cfg.args, "default args from provider spec should be picked up"


def test_parse_accepts_explicit_matching_command() -> None:
    cfg = parse_tunnel_section(
        {"enabled": True, "provider": "autossh", "command": "autossh"}
    )
    assert cfg.command == "autossh"


def test_parse_accepts_string_arg_list() -> None:
    cfg = parse_tunnel_section(
        {"enabled": True, "args": ["-p", "443", "a.pinggy.io"]}
    )
    assert cfg.args == ("-p", "443", "a.pinggy.io")


def test_parse_accepts_dict_args_shorthand() -> None:
    cfg = parse_tunnel_section(
        {"enabled": True, "args": {"remote": "a.pinggy.io", "port": 443}}
    )
    assert "--remote" in cfg.args
    assert "a.pinggy.io" in cfg.args
    assert "--port" in cfg.args
    assert "443" in cfg.args


def test_parse_accepts_bounded_timeouts() -> None:
    cfg = parse_tunnel_section(
        {
            "enabled": True,
            "startup_probe_timeout_s": 5,
            "url_parse_timeout_s": 60,
        }
    )
    assert cfg.startup_probe_timeout_s == 5
    assert cfg.url_parse_timeout_s == 60


# ---- parse_tunnel_section: rejections (TUN-1, TUN-2) --------------------


def test_parse_rejects_unknown_provider() -> None:
    with pytest.raises(ConfigError, match="tunnel.provider"):
        parse_tunnel_section({"enabled": True, "provider": "ngrok"})


def test_parse_rejects_command_override_for_autossh() -> None:
    with pytest.raises(ConfigError, match="tunnel.command"):
        parse_tunnel_section(
            {"enabled": True, "provider": "autossh", "command": "rm"}
        )


def test_parse_rejects_non_string_provider() -> None:
    with pytest.raises(ConfigError, match="tunnel.provider"):
        parse_tunnel_section({"enabled": True, "provider": 42})


def test_parse_rejects_non_mapping_section() -> None:
    with pytest.raises(ConfigError, match="tunnel"):
        parse_tunnel_section(["enabled", True])


def test_parse_rejects_out_of_range_startup_timeout() -> None:
    with pytest.raises(ConfigError, match="startup_probe_timeout_s"):
        parse_tunnel_section({"enabled": True, "startup_probe_timeout_s": 0})
    with pytest.raises(ConfigError, match="startup_probe_timeout_s"):
        parse_tunnel_section({"enabled": True, "startup_probe_timeout_s": 999})


def test_parse_rejects_out_of_range_url_parse_timeout() -> None:
    with pytest.raises(ConfigError, match="url_parse_timeout_s"):
        parse_tunnel_section({"enabled": True, "url_parse_timeout_s": 4})
    with pytest.raises(ConfigError, match="url_parse_timeout_s"):
        parse_tunnel_section({"enabled": True, "url_parse_timeout_s": 500})


def test_parse_rejects_non_list_string_args() -> None:
    with pytest.raises(ConfigError, match="tunnel.args"):
        parse_tunnel_section({"enabled": True, "args": [1, 2, 3]})


def test_parse_rejects_scalar_args() -> None:
    with pytest.raises(ConfigError, match="tunnel.args"):
        parse_tunnel_section({"enabled": True, "args": "single string"})


# ---- DashboardConfig.load integration -----------------------------------


def test_dashboard_config_load_absent_tunnel_stays_disabled(tmp_path: Path) -> None:
    proj = tmp_path / "proj"
    proj.mkdir()
    (proj / "dashboard.yaml").write_text(
        "kanban:\n  refresh_interval_s: 3\n", encoding="utf-8"
    )
    cfg = DashboardConfig.load(proj)
    assert cfg.tunnel.enabled is False


def test_dashboard_config_load_merges_valid_tunnel_block(tmp_path: Path) -> None:
    proj = tmp_path / "proj"
    proj.mkdir()
    (proj / "dashboard.yaml").write_text(
        "tunnel:\n"
        "  enabled: true\n"
        "  provider: autossh\n"
        "  auto_start: true\n"
        "  startup_probe_timeout_s: 5\n",
        encoding="utf-8",
    )
    cfg = DashboardConfig.load(proj)
    assert cfg.tunnel.enabled is True
    assert cfg.tunnel.auto_start is True
    assert cfg.tunnel.startup_probe_timeout_s == 5


def test_dashboard_config_load_raises_on_invalid_tunnel_block(tmp_path: Path) -> None:
    proj = tmp_path / "proj"
    proj.mkdir()
    (proj / "dashboard.yaml").write_text(
        "tunnel:\n  enabled: true\n  provider: ngrok\n", encoding="utf-8"
    )
    with pytest.raises(ConfigError, match="tunnel.provider"):
        DashboardConfig.load(proj)
