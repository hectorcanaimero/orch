"""CLI tests for `orch config consolidate` (Sprint H-2)."""
from __future__ import annotations

from pathlib import Path

import pytest
import yaml

from orchestrator.orch import _run_config_subcommand
from orchestrator.tests.test_validate_cmd import _common, _task, _write_project


def _dashboard_yaml(root: Path) -> Path:
    return root / "dashboard.yaml"


def _config_yaml(root: Path) -> Path:
    return root / ".orchestrator" / "config.yaml"


# ---- consolidate: nothing to do --------------------------------------------


def test_consolidate_noop_when_no_dashboard_yaml_present(
    tmp_path: Path, capsys: pytest.CaptureFixture,
) -> None:
    root = tmp_path / "proj"
    _write_project(root, tasks=[_task("A")])
    # A fresh H-2 project has no dashboard.yaml.
    assert not _dashboard_yaml(root).exists()

    rc = _run_config_subcommand(["consolidate", *_common(root)])
    out = capsys.readouterr().out
    assert rc == 0
    assert "nothing to consolidate" in out


def test_consolidate_noop_when_dashboard_already_subset_of_config(
    tmp_path: Path, capsys: pytest.CaptureFixture,
) -> None:
    root = tmp_path / "proj"
    _write_project(
        root,
        tasks=[_task("A")],
        config_extra="\ndashboard:\n  kanban:\n    wip_default: 5\n",
    )
    # dashboard.yaml carries the same value already in config.yaml.
    _dashboard_yaml(root).write_text(
        "kanban:\n  wip_default: 5\n", encoding="utf-8"
    )
    rc = _run_config_subcommand(["consolidate", *_common(root)])
    assert rc == 0
    assert "already a subset" in capsys.readouterr().out
    # Nothing renamed since it was a no-op.
    assert _dashboard_yaml(root).exists()


# ---- consolidate: happy path -----------------------------------------------


def test_consolidate_merges_dashboard_yaml_into_config_and_backs_up(
    tmp_path: Path,
) -> None:
    root = tmp_path / "proj"
    _write_project(root, tasks=[_task("A")])
    _dashboard_yaml(root).write_text(
        "kanban:\n  wip_default: 5\ntunnel:\n  enabled: true\n",
        encoding="utf-8",
    )

    rc = _run_config_subcommand(["consolidate", *_common(root)])
    assert rc == 0

    # Original renamed to .bak-<ts>, not deleted.
    assert not _dashboard_yaml(root).exists()
    backups = list(root.glob("dashboard.yaml.bak-*"))
    assert len(backups) == 1
    assert "kanban:" in backups[0].read_text(encoding="utf-8")

    # config.yaml carries the merged block.
    cfg = yaml.safe_load(_config_yaml(root).read_text(encoding="utf-8")) or {}
    assert cfg["dashboard"]["kanban"]["wip_default"] == 5
    assert cfg["dashboard"]["tunnel"]["enabled"] is True


def test_consolidate_dashboard_yaml_keys_win_over_existing_config_section(
    tmp_path: Path,
) -> None:
    """When both files set the same key, the operator-authored dashboard.yaml
    wins — that's the file they explicitly edited."""
    root = tmp_path / "proj"
    _write_project(
        root,
        tasks=[_task("A")],
        config_extra="\ndashboard:\n  kanban:\n    wip_default: 3\n",
    )
    _dashboard_yaml(root).write_text(
        "kanban:\n  wip_default: 9\n", encoding="utf-8"
    )

    rc = _run_config_subcommand(["consolidate", *_common(root)])
    assert rc == 0
    cfg = yaml.safe_load(_config_yaml(root).read_text(encoding="utf-8")) or {}
    assert cfg["dashboard"]["kanban"]["wip_default"] == 9


# ---- consolidate: --dry-run doesn't touch files ----------------------------


def test_consolidate_dry_run_writes_nothing(
    tmp_path: Path, capsys: pytest.CaptureFixture,
) -> None:
    root = tmp_path / "proj"
    _write_project(root, tasks=[_task("A")])
    _dashboard_yaml(root).write_text(
        "kanban:\n  wip_default: 7\n", encoding="utf-8"
    )
    original_cfg = _config_yaml(root).read_text(encoding="utf-8")

    rc = _run_config_subcommand(["consolidate", "--dry-run", *_common(root)])
    out = capsys.readouterr().out
    assert rc == 0
    assert "--dry-run" in out
    # Neither file touched.
    assert _dashboard_yaml(root).exists()
    assert not any(root.glob("dashboard.yaml.bak-*"))
    assert _config_yaml(root).read_text(encoding="utf-8") == original_cfg
