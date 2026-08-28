"""Unit tests for `orchestrator.init_cmd` (Sprint 9 — batch scaffolder).

Covers:
    - Fresh dir → all expected files created + executable bits on scripts
    - Existing dir with conflicts + no --force → refuse, exit 1, no writes
    - Existing dir + --force → overwrite
    - --sdd → also creates openspec/ layout
    - tasks.json skeleton is valid JSON that orch.load_tasks can parse
    - YAML defaults are byte-identical to the packaged ones (single source of truth)
    - .gitignore only written when absent (respect existing)
    - SDD detection: returns (installed, hints) tuple
"""

from __future__ import annotations

import json
import os
import stat
from pathlib import Path

import pytest

from orchestrator.init_cmd import (
    SDDStatus,
    detect_sdd,
    orch_init,
)


# ---- Fresh init --------------------------------------------------------


def test_init_creates_expected_layout(tmp_path: Path) -> None:
    dest = tmp_path / "proj"
    exit_code = orch_init(dest)
    assert exit_code == 0
    # Files at the root
    assert (dest / "tasks.json").exists()
    assert (dest / ".gitignore").exists()
    assert (dest / "specs" / "README.md").exists()
    # Scripts
    for name in ("task-start.sh", "task-finish.sh", "task-block.sh"):
        p = dest / "scripts" / name
        assert p.exists(), f"missing script: {p}"
        mode = p.stat().st_mode
        assert mode & stat.S_IXUSR, f"script not executable: {p}"
    # orchestrator dir — H-2: only config.yaml + router stub live here.
    # dashboard.yaml and budgets.yaml are NO LONGER scaffolded; their
    # defaults ship inline in config.yaml (`dashboard:`) and in
    # config_loader._apply_defaults (`budget:`) respectively.
    assert (dest / ".orchestrator" / "state" / ".gitkeep").exists()
    assert (dest / ".orchestrator" / "config.yaml").exists()
    assert (dest / ".orchestrator" / "model_router.yaml").exists()
    assert not (dest / ".orchestrator" / "budgets.yaml").exists()
    assert not (dest / "dashboard.yaml").exists()


def test_init_creates_dest_dir_if_missing(tmp_path: Path) -> None:
    dest = tmp_path / "deep" / "nested" / "proj"
    assert not dest.exists()
    assert orch_init(dest) == 0
    assert dest.is_dir()
    assert (dest / "tasks.json").exists()


def test_init_tasks_json_is_valid_and_empty(tmp_path: Path) -> None:
    dest = tmp_path / "proj"
    orch_init(dest)
    payload = json.loads((dest / "tasks.json").read_text(encoding="utf-8"))
    assert "meta" in payload
    assert "phases" in payload
    assert "tasks" in payload
    assert payload["tasks"] == []


def test_init_tasks_json_loadable_by_orch(tmp_path: Path) -> None:
    """The skeleton must survive orch.load_tasks without error (contract)."""
    from orchestrator.state import load_tasks

    dest = tmp_path / "proj"
    orch_init(dest)
    tasks = load_tasks(dest / "tasks.json")
    assert tasks == []


# ---- Config copies match the packaged defaults -------------------------


def test_init_config_yaml_is_byte_identical_to_packaged_default(tmp_path: Path) -> None:
    """H-2: only config.yaml is copied verbatim; the other YAMLs are gone."""
    from orchestrator import (
        __file__ as pkg_init,
    )

    pkg_dir = Path(pkg_init).parent
    dest = tmp_path / "proj"
    orch_init(dest)

    packaged = pkg_dir / "config.yaml"
    copied = dest / ".orchestrator" / "config.yaml"
    assert copied.read_bytes() == packaged.read_bytes(), (
        "config.yaml drift from packaged default"
    )


def test_init_config_yaml_ships_dashboard_defaults_inline(tmp_path: Path) -> None:
    """H-2: `dashboard:` section is inline in config.yaml (no dashboard.yaml)."""
    dest = tmp_path / "proj"
    assert orch_init(dest) == 0
    cfg_text = (dest / ".orchestrator" / "config.yaml").read_text(encoding="utf-8")
    assert "dashboard:" in cfg_text
    assert "kanban:" in cfg_text
    assert "tunnel:" in cfg_text
    # And the standalone override file is NOT scaffolded.
    assert not (dest / "dashboard.yaml").exists()


def test_init_writes_router_stub_not_shipped_table(tmp_path: Path) -> None:
    """H-2: fresh project gets an empty router mapping — not the 286-line
    shipped table full of models the dev may not use.

    `orch router add-missing` populates entries when tasks add new models.
    """
    dest = tmp_path / "proj"
    assert orch_init(dest) == 0
    router = dest / ".orchestrator" / "model_router.yaml"
    text = router.read_text(encoding="utf-8")
    assert "orch router add-missing" in text  # points the dev at the tool
    # Stub must still parse as an empty mapping (load_router requires a dict).
    import yaml
    parsed = yaml.safe_load(text) or {}
    assert parsed == {}


def test_init_pre_existing_dashboard_yaml_is_left_alone(tmp_path: Path) -> None:
    """H-2 backwards compat: a project that already has dashboard.yaml as an
    override keeps it untouched — orch init never scaffolds one, and its
    presence must NOT trigger the conflict gate (init writes only the
    packaged config.yaml now).
    """
    dest = tmp_path / "proj"
    dest.mkdir()
    original = "# operator-authored overrides\nkanban:\n  wip_default: 5\n"
    (dest / "dashboard.yaml").write_text(original, encoding="utf-8")

    exit_code = orch_init(dest, force=False)
    assert exit_code == 0
    # Existing override preserved verbatim.
    assert (dest / "dashboard.yaml").read_text(encoding="utf-8") == original


# ---- Conflict handling -------------------------------------------------


def test_init_refuses_when_dest_has_tasks_json(tmp_path: Path) -> None:
    dest = tmp_path / "proj"
    dest.mkdir()
    (dest / "tasks.json").write_text('{"old":"stuff"}', encoding="utf-8")

    exit_code = orch_init(dest, force=False)
    assert exit_code == 1
    # Existing content is intact.
    assert (dest / "tasks.json").read_text(encoding="utf-8") == '{"old":"stuff"}'
    # And no partial write happened.
    assert not (dest / "scripts").exists()


def test_init_force_overwrites_existing(tmp_path: Path) -> None:
    dest = tmp_path / "proj"
    dest.mkdir()
    (dest / "tasks.json").write_text('{"old":"stuff"}', encoding="utf-8")

    assert orch_init(dest, force=True) == 0
    payload = json.loads((dest / "tasks.json").read_text(encoding="utf-8"))
    assert payload["tasks"] == []


def test_init_gitignore_preserves_existing(tmp_path: Path) -> None:
    dest = tmp_path / "proj"
    dest.mkdir()
    original = "# my custom .gitignore\nnode_modules/\n"
    (dest / ".gitignore").write_text(original, encoding="utf-8")
    # No conflict since tasks.json etc don't exist yet.
    assert orch_init(dest) == 0
    # .gitignore left alone (respect the user's).
    assert (dest / ".gitignore").read_text(encoding="utf-8") == original


# ---- .github/workflows/orch-ci.yml (Sprint G-1) ------------------------


def test_init_generates_ci_workflow_when_absent(tmp_path: Path) -> None:
    dest = tmp_path / "proj"
    assert orch_init(dest) == 0
    workflow = dest / ".github" / "workflows" / "orch-ci.yml"
    assert workflow.exists()
    text = workflow.read_text(encoding="utf-8")
    assert "pytest" in text
    assert "TEST_COMMAND" not in text  # placeholder must be substituted


def test_init_preserves_existing_ci_workflow(tmp_path: Path) -> None:
    dest = tmp_path / "proj"
    workflow_dir = dest / ".github" / "workflows"
    workflow_dir.mkdir(parents=True)
    original = "# hand-authored workflow\nname: custom-ci\n"
    (workflow_dir / "orch-ci.yml").write_text(original, encoding="utf-8")

    assert orch_init(dest) == 0
    assert (workflow_dir / "orch-ci.yml").read_text(encoding="utf-8") == original


def test_init_force_overwrites_existing_ci_workflow(tmp_path: Path) -> None:
    dest = tmp_path / "proj"
    workflow_dir = dest / ".github" / "workflows"
    workflow_dir.mkdir(parents=True)
    (workflow_dir / "orch-ci.yml").write_text("stale", encoding="utf-8")

    assert orch_init(dest, force=True) == 0
    assert "pytest" in (workflow_dir / "orch-ci.yml").read_text(encoding="utf-8")


# ---- SDD detection -----------------------------------------------------


def test_detect_sdd_finds_installed_skills(tmp_path: Path, monkeypatch) -> None:
    fake_home = tmp_path / "home"
    (fake_home / ".claude" / "skills" / "sdd-explore").mkdir(parents=True)
    monkeypatch.setenv("HOME", str(fake_home))
    monkeypatch.setattr("pathlib.Path.home", lambda: fake_home)

    status = detect_sdd()
    assert isinstance(status, SDDStatus)
    assert status.installed is True
    assert status.skills  # non-empty list


def test_detect_sdd_returns_not_installed_when_missing(
    tmp_path: Path, monkeypatch
) -> None:
    fake_home = tmp_path / "empty_home"
    fake_home.mkdir()
    monkeypatch.setenv("HOME", str(fake_home))
    monkeypatch.setattr("pathlib.Path.home", lambda: fake_home)

    status = detect_sdd()
    assert status.installed is False
    assert status.skills == []


# ---- --sdd flag adds openspec layout ------------------------------------


def test_init_sdd_flag_creates_openspec_layout(tmp_path: Path) -> None:
    dest = tmp_path / "proj"
    assert orch_init(dest, sdd=True) == 0
    assert (dest / "openspec" / "README.md").exists()
    assert (dest / "openspec" / "changes").is_dir()
    assert (dest / "openspec" / "specs").is_dir()


def test_init_without_sdd_flag_skips_openspec(tmp_path: Path) -> None:
    dest = tmp_path / "proj"
    assert orch_init(dest) == 0
    assert not (dest / "openspec").exists()


# ---- Idempotency after --force ------------------------------------------


def test_init_twice_with_force_is_idempotent(tmp_path: Path) -> None:
    dest = tmp_path / "proj"
    orch_init(dest)
    # Snapshot the byte content of every file we care about.
    snapshot = {}
    for p in dest.rglob("*"):
        if p.is_file():
            snapshot[p.relative_to(dest)] = p.read_bytes()
    # Re-run with force.
    assert orch_init(dest, force=True) == 0
    for rel, expected in snapshot.items():
        assert (dest / rel).read_bytes() == expected, f"drift: {rel}"


# ---- AGENTS.md generation -----------------------------------------------


def test_init_generates_agents_md(tmp_path: Path) -> None:
    """orch init must generate AGENTS.md at the project root."""
    orch_init(tmp_path, project_name="my-project")

    agents_md = tmp_path / "AGENTS.md"
    assert agents_md.exists(), "AGENTS.md must be generated by orch init"

    content = agents_md.read_text()
    assert "Orch Project Context" in content
    assert ".orchestrator/state/" in content
    assert "tasks_definition" in content
    assert "tasks_runtime" in content
    assert "orch task set" in content


def test_init_agents_md_not_gitignored(tmp_path: Path) -> None:
    """AGENTS.md must NOT appear in the generated .gitignore."""
    orch_init(tmp_path)

    gitignore = (tmp_path / ".gitignore").read_text()
    assert "AGENTS.md" not in gitignore


def test_init_agents_md_not_overwritten_without_force(tmp_path: Path) -> None:
    """A pre-existing AGENTS.md must not be overwritten unless --force."""
    (tmp_path / "AGENTS.md").write_text("custom content", encoding="utf-8")

    orch_init(tmp_path, force=False)

    content = (tmp_path / "AGENTS.md").read_text()
    assert content == "custom content", "AGENTS.md must not be overwritten without --force"


def test_init_gitignore_includes_worktrees(tmp_path: Path) -> None:
    """orch init must gitignore .worktrees/ so git worktree dirs aren't committed."""
    orch_init(tmp_path)
    gitignore = (tmp_path / ".gitignore").read_text()
    assert ".worktrees/" in gitignore
