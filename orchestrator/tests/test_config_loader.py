"""Tests for orchestrator.config_loader — deep_merge + override loading."""
from __future__ import annotations

import textwrap
from pathlib import Path

import pytest

from orchestrator.config_loader import deep_merge, load_config


# ---- deep_merge -------------------------------------------------------------


def test_deep_merge_override_wins_on_conflict():
    base = {"a": 1, "nested": {"x": 10, "y": 20}}
    override = {"nested": {"x": 99}}
    result = deep_merge(base, override)
    assert result["nested"]["x"] == 99
    assert result["nested"]["y"] == 20  # untouched key preserved


def test_deep_merge_adds_missing_keys():
    base = {"a": 1}
    override = {"b": 2}
    result = deep_merge(base, override)
    assert result == {"a": 1, "b": 2}


def test_deep_merge_does_not_mutate_base():
    base = {"a": {"x": 1}}
    override = {"a": {"y": 2}}
    deep_merge(base, override)
    assert base == {"a": {"x": 1}}


def test_deep_merge_non_dict_override_replaces():
    base = {"retry": {"backoff_seconds": 5}}
    override = {"retry": 99}
    result = deep_merge(base, override)
    assert result["retry"] == 99


# ---- load_config ------------------------------------------------------------


def test_load_config_returns_defaults_with_no_overrides(tmp_path: Path):
    cfg_file = tmp_path / "config.yaml"
    cfg_file.write_text("concurrency:\n  global_max: 4\n", encoding="utf-8")
    cfg = load_config(cfg_file)
    assert cfg["concurrency"]["global_max"] == 4
    assert cfg["retry"]["backoff_seconds"] == 5.0   # default


def test_load_config_applies_budgets_override(tmp_path: Path):
    cfg_file = tmp_path / "config.yaml"
    cfg_file.write_text("", encoding="utf-8")
    budgets_file = tmp_path / "budgets.yaml"
    budgets_file.write_text("budgets_preset: aggressive\n", encoding="utf-8")
    cfg = load_config(cfg_file, project_root=tmp_path)
    assert cfg["budgets_preset"] == "aggressive"


def test_load_config_missing_override_is_silently_skipped(tmp_path: Path):
    cfg_file = tmp_path / "config.yaml"
    cfg_file.write_text("", encoding="utf-8")
    # No budgets.yaml → load_config must not raise
    cfg = load_config(cfg_file, project_root=tmp_path)
    assert isinstance(cfg, dict)


def test_load_config_raises_on_missing_config_file(tmp_path: Path):
    with pytest.raises(FileNotFoundError):
        load_config(tmp_path / "nonexistent.yaml")


def test_load_config_max_attempts_default(tmp_path: Path):
    cfg_file = tmp_path / "config.yaml"
    cfg_file.write_text("", encoding="utf-8")
    cfg = load_config(cfg_file)
    assert cfg["retry"]["max_attempts"] == 2


def test_load_config_max_attempts_overridable(tmp_path: Path):
    cfg_file = tmp_path / "config.yaml"
    cfg_file.write_text("retry:\n  max_attempts: 5\n", encoding="utf-8")
    cfg = load_config(cfg_file)
    assert cfg["retry"]["max_attempts"] == 5


def test_load_config_applies_dashboard_override(tmp_path: Path):
    (tmp_path / "config.yaml").write_text("retry:\n  max_attempts: 2\n", encoding="utf-8")
    (tmp_path / "dashboard").mkdir()
    (tmp_path / "dashboard" / "dashboard.yaml").write_text("dashboard:\n  port: 9999\n", encoding="utf-8")
    cfg = load_config(tmp_path / "config.yaml")
    assert cfg["dashboard"]["port"] == 9999


def test_load_config_override_priority_later_file_wins(tmp_path: Path):
    (tmp_path / "config.yaml").write_text("retry:\n  max_attempts: 2\n", encoding="utf-8")
    (tmp_path / "budgets.yaml").write_text("budget:\n  per_dispatch_usd: 1.0\n", encoding="utf-8")
    (tmp_path / "model_router.yaml").write_text("budget:\n  per_dispatch_usd: 2.0\n", encoding="utf-8")
    cfg = load_config(tmp_path / "config.yaml")
    assert cfg["budget"]["per_dispatch_usd"] == 2.0


def test_load_config_project_root_defaults_to_config_parent(tmp_path: Path):
    config_path = tmp_path / "config.yaml"
    config_path.write_text("retry:\n  max_attempts: 2\n", encoding="utf-8")
    (tmp_path / "budgets.yaml").write_text("budget:\n  per_dispatch_usd: 7.0\n", encoding="utf-8")
    # Call WITHOUT explicit project_root — it should still find budgets.yaml
    cfg = load_config(config_path)
    assert cfg["budget"]["per_dispatch_usd"] == 7.0


def test_load_config_budget_partial_override_keeps_default(tmp_path: Path):
    # config.yaml has budget.max_usd but NOT per_dispatch_usd
    (tmp_path / "config.yaml").write_text("budget:\n  max_usd: 100\n", encoding="utf-8")
    cfg = load_config(tmp_path / "config.yaml")
    # per_dispatch_usd default must still be applied
    assert cfg["budget"]["per_dispatch_usd"] == 5.0
    assert cfg["budget"]["max_usd"] == 100


# ---- github: section defaults (Sprint G-1) ----------------------------------


def test_load_config_github_defaults(tmp_path: Path):
    cfg_file = tmp_path / "config.yaml"
    cfg_file.write_text("", encoding="utf-8")
    cfg = load_config(cfg_file)
    assert cfg["github"]["test_command"] == "pytest"
    assert cfg["github"]["auto_merge"] is False


def test_load_config_github_auto_merge_overridable(tmp_path: Path):
    cfg_file = tmp_path / "config.yaml"
    cfg_file.write_text(
        "github:\n  auto_merge: true\n  test_command: \"make test\"\n",
        encoding="utf-8",
    )
    cfg = load_config(cfg_file)
    assert cfg["github"]["auto_merge"] is True
    assert cfg["github"]["test_command"] == "make test"


# ---- notifications: section defaults (Sprint G-6) ---------------------------


def test_load_config_notifications_defaults(tmp_path: Path):
    cfg_file = tmp_path / "config.yaml"
    cfg_file.write_text("", encoding="utf-8")
    cfg = load_config(cfg_file)
    assert cfg["notifications"]["slack_webhook"] == ""
    assert cfg["notifications"]["discord_webhook"] == ""
    assert cfg["notifications"]["timeout_s"] == 5


def test_load_config_notifications_overridable(tmp_path: Path):
    cfg_file = tmp_path / "config.yaml"
    cfg_file.write_text(
        "notifications:\n"
        "  slack_webhook: https://hooks.slack.com/services/AAA\n"
        "  timeout_s: 10\n",
        encoding="utf-8",
    )
    cfg = load_config(cfg_file)
    assert cfg["notifications"]["slack_webhook"] == "https://hooks.slack.com/services/AAA"
    assert cfg["notifications"]["timeout_s"] == 10
    # discord default preserved when only slack overridden
    assert cfg["notifications"]["discord_webhook"] == ""
