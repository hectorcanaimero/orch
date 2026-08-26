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
    # orchestrator dir
    assert (dest / ".orchestrator" / "state" / ".gitkeep").exists()
    assert (dest / ".orchestrator" / "config.yaml").exists()
    assert (dest / ".orchestrator" / "model_router.yaml").exists()
    assert (dest / ".orchestrator" / "budgets.yaml").exists()


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


def test_init_config_yamls_are_copies_of_packaged_defaults(tmp_path: Path) -> None:
    """Single source of truth: the copied YAMLs must equal the ones shipped."""
    from orchestrator import (
        __file__ as pkg_init,
    )

    pkg_dir = Path(pkg_init).parent
    dest = tmp_path / "proj"
    orch_init(dest)

    pairs = (
        ("config.yaml", ".orchestrator/config.yaml"),
        ("model_router.yaml", ".orchestrator/model_router.yaml"),
        ("budgets.yaml", ".orchestrator/budgets.yaml"),
        # Dashboard override lives at the project root — package source is
        # one level deeper. Verified explicitly to lock the layout.
        ("dashboard/dashboard.yaml", "dashboard.yaml"),
    )
    for src_rel, dst_rel in pairs:
        packaged = pkg_dir / src_rel
        copied = dest / dst_rel
        assert copied.read_bytes() == packaged.read_bytes(), (
            f"{dst_rel} drift from packaged default"
        )


def test_init_creates_dashboard_yaml_at_project_root(tmp_path: Path) -> None:
    """dashboard.yaml MUST land at project root (not under orchestrator/).

    The dashboard config loader looks for `<project_root>/dashboard.yaml`,
    so if we put it anywhere else the operator can't actually override
    settings (like flipping tunnel.enabled = true).
    """
    dest = tmp_path / "proj"
    assert orch_init(dest) == 0
    dashboard_yaml = dest / "dashboard.yaml"
    assert dashboard_yaml.exists(), "dashboard.yaml must be scaffolded at project root"
    # Sanity-check the schema: tunnel block must be present (shipped default).
    text = dashboard_yaml.read_text(encoding="utf-8")
    assert "tunnel:" in text, "shipped dashboard.yaml should include tunnel block"
    # And it MUST NOT sit under orchestrator/ — that would silently break
    # the loader while looking correct in a file listing.
    assert not (dest / ".orchestrator" / "dashboard.yaml").exists()


def test_init_refuses_when_dest_has_dashboard_yaml(tmp_path: Path) -> None:
    """Existing dashboard.yaml at project root must trigger the conflict gate."""
    dest = tmp_path / "proj"
    dest.mkdir()
    original = "# operator-authored overrides\nkanban:\n  wip_default: 5\n"
    (dest / "dashboard.yaml").write_text(original, encoding="utf-8")

    exit_code = orch_init(dest, force=False)
    assert exit_code == 1
    # Existing file preserved and no partial write happened.
    assert (dest / "dashboard.yaml").read_text(encoding="utf-8") == original
    assert not (dest / "scripts").exists()


def test_init_force_overwrites_existing_dashboard_yaml(tmp_path: Path) -> None:
    dest = tmp_path / "proj"
    dest.mkdir()
    (dest / "dashboard.yaml").write_text(
        "# stale operator overrides\n", encoding="utf-8"
    )

    assert orch_init(dest, force=True) == 0
    # After --force, the file equals the shipped default (tunnel block back).
    assert "tunnel:" in (dest / "dashboard.yaml").read_text(encoding="utf-8")


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
