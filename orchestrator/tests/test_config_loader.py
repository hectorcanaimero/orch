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
