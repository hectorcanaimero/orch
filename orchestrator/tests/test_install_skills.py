"""Tests for `orch install-skills` + doctor `skill.orch` check (Sprint H-6)."""
from __future__ import annotations

from pathlib import Path

import pytest


# ---- shipped SKILL.md ------------------------------------------------------


def test_orch_skill_ships_with_frontmatter_and_rules() -> None:
    """Sanity guard: the shipped SKILL.md must have Claude Code frontmatter
    and the load-bearing rules (never edit tasks.json, install-skills hint,
    pipx install command)."""
    from orchestrator import __file__ as pkg_init
    pkg_dir = Path(pkg_init).parent
    skill_path = pkg_dir / "skills" / "orch" / "SKILL.md"
    assert skill_path.exists(), "packaged skill missing"

    text = skill_path.read_text(encoding="utf-8")
    # Claude Code skill frontmatter contract.
    assert text.startswith("---\n")
    assert "name: orch" in text
    assert "description:" in text
    # Load-bearing content.
    assert "pipx install orch" in text
    assert "Never edit `tasks.json`" in text
    assert "orch task set" in text


# ---- CLI: orch install-skills ---------------------------------------------


def test_install_skills_writes_skill_to_target_dir(tmp_path: Path) -> None:
    from orchestrator.orch import _run_install_skills_subcommand

    dst = tmp_path / "skills"
    rc = _run_install_skills_subcommand(["--path", str(dst)])
    assert rc == 0
    assert (dst / "orch" / "SKILL.md").exists()


def test_install_skills_skips_existing_without_force(
    tmp_path: Path, capsys: pytest.CaptureFixture,
) -> None:
    from orchestrator.orch import _run_install_skills_subcommand

    dst = tmp_path / "skills"
    (dst / "orch").mkdir(parents=True)
    (dst / "orch" / "SKILL.md").write_text("stale content", encoding="utf-8")

    rc = _run_install_skills_subcommand(["--path", str(dst)])
    out = capsys.readouterr().out
    assert rc == 0
    assert "skipped" in out
    # The stale content is preserved when --force is absent.
    assert (dst / "orch" / "SKILL.md").read_text(encoding="utf-8") == "stale content"


def test_install_skills_force_overwrites(tmp_path: Path) -> None:
    from orchestrator.orch import _run_install_skills_subcommand

    dst = tmp_path / "skills"
    (dst / "orch").mkdir(parents=True)
    (dst / "orch" / "SKILL.md").write_text("stale", encoding="utf-8")

    rc = _run_install_skills_subcommand(["--path", str(dst), "--force"])
    assert rc == 0
    body = (dst / "orch" / "SKILL.md").read_text(encoding="utf-8")
    assert "pipx install orch" in body  # real content, not "stale"


def test_install_skills_dry_run_writes_nothing(
    tmp_path: Path, capsys: pytest.CaptureFixture,
) -> None:
    from orchestrator.orch import _run_install_skills_subcommand

    dst = tmp_path / "skills"
    rc = _run_install_skills_subcommand(["--path", str(dst), "--dry-run"])
    out = capsys.readouterr().out
    assert rc == 0
    assert "--dry-run" in out
    assert "orch" in out
    # Nothing on disk.
    assert not dst.exists()


# ---- doctor: skill.orch check ---------------------------------------------


def test_doctor_reports_skip_when_claude_home_absent(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch,
) -> None:
    """Users without Claude Code shouldn't see a `warn` — this isn't for them."""
    from orchestrator.doctor import _check_orch_skill_installed
    monkeypatch.setenv("HOME", str(tmp_path))

    result = _check_orch_skill_installed()
    assert result.status == "skip"


def test_doctor_reports_ok_when_skill_installed(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch,
) -> None:
    from orchestrator.doctor import _check_orch_skill_installed
    monkeypatch.setenv("HOME", str(tmp_path))
    (tmp_path / ".claude" / "skills" / "orch").mkdir(parents=True)
    (tmp_path / ".claude" / "skills" / "orch" / "SKILL.md").write_text(
        "---\nname: orch\n---\nbody", encoding="utf-8"
    )

    result = _check_orch_skill_installed()
    assert result.status == "ok"


def test_doctor_warns_and_hints_install_when_skill_missing(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch,
) -> None:
    from orchestrator.doctor import _check_orch_skill_installed
    monkeypatch.setenv("HOME", str(tmp_path))
    (tmp_path / ".claude").mkdir()  # Claude Code set up, no skill installed

    result = _check_orch_skill_installed()
    assert result.status == "warn"
    assert result.remediation == "orch install-skills"


# ---- AGENTS.md stub (H-6) --------------------------------------------------


def test_orch_init_agents_md_includes_about_orch_block(tmp_path: Path) -> None:
    from orchestrator.init_cmd import orch_init
    dest = tmp_path / "proj"
    assert orch_init(dest) == 0
    body = (dest / "AGENTS.md").read_text(encoding="utf-8")
    # The About orch block must be there so agents opening the project
    # cold learn what orch is + how to install the skill.
    assert "About orch" in body
    assert "pipx install orch" in body
    assert "orch install-skills" in body
    assert "Never edit `tasks.json`" in body


def test_template_agents_md_all_include_about_orch_block(tmp_path: Path) -> None:
    """Every shipped template's AGENTS.md must inherit the About orch stub —
    the whole point of H-6 is portable, template-agnostic discoverability."""
    from orchestrator.init_cmd import list_templates, orch_init

    for tname in list_templates():
        dest = tmp_path / tname
        assert orch_init(dest, template=tname) == 0
        body = (dest / "AGENTS.md").read_text(encoding="utf-8")
        assert "About orch" in body, f"{tname} AGENTS.md missing About orch"
        assert "pipx install orch" in body, f"{tname} missing pipx hint"
        assert "orch install-skills" in body, f"{tname} missing skill hint"
