"""`orch init` — batch scaffolder for a new orch project (Sprint 9).

Contract:
    - Creates the minimum layout orch needs: tasks.json, scripts/task-*.sh,
      orchestrator/{state, config.yaml, model_router.yaml, budgets.yaml},
      specs/README.md, .gitignore (when absent).
    - Never writes AI. Never touches the network. Never installs anything.
    - Refuses to overwrite existing files without --force (safety).
    - `.gitignore` is a soft target: if the user already has one, we leave it
      alone even without --force (respect existing).
    - --sdd adds openspec/ scaffolding for teams using Spec-Driven Development.
    - The YAML defaults copied into the project are byte-identical to the ones
      shipped with the installed package — single source of truth.

Not in scope (would be a future --interactive mode):
    - Detecting installed CLIs.
    - Probing model availability.
    - Editing the copied YAMLs based on prompts.
"""

from __future__ import annotations

import shutil
import stat
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path

# ---- Package-relative paths --------------------------------------------

_PKG_DIR = Path(__file__).resolve().parent
_TEMPLATES_DIR = _PKG_DIR / "templates"

# Files copied verbatim from the package root (shipped YAML defaults).
_YAML_DEFAULTS: tuple[str, ...] = (
    "config.yaml",
    "model_router.yaml",
    "budgets.yaml",
)

# Files that would be clobbered — presence of ANY of these triggers the
# --force check. `.gitignore` deliberately not in this list (soft target).
_CONFLICT_MARKERS: tuple[str, ...] = (
    "tasks.json",
    "scripts/task-start.sh",
    "scripts/task-finish.sh",
    "scripts/task-block.sh",
    "orchestrator/config.yaml",
    "orchestrator/model_router.yaml",
    "orchestrator/budgets.yaml",
)


# ---- SDD detection -----------------------------------------------------


@dataclass(frozen=True, slots=True)
class SDDStatus:
    """Result of scanning `~/.claude/skills/` for SDD skills."""

    installed: bool
    skills: list[str] = field(default_factory=list)


def detect_sdd() -> SDDStatus:
    """Look for SDD skills in the user's `~/.claude/skills/` dir.

    We consider SDD "installed" when at least one of the known skill
    directories exists. That's a best-effort heuristic — it doesn't verify
    the skills actually work, just that they're present.
    """
    known = (
        "sdd-explore",
        "sdd-propose",
        "sdd-spec",
        "sdd-design",
        "sdd-tasks",
        "sdd-apply",
        "sdd-verify",
        "sdd-archive",
        "orch-plan",  # composite skill in some setups
    )
    skills_dir = Path.home() / ".claude" / "skills"
    if not skills_dir.exists():
        return SDDStatus(installed=False, skills=[])
    found = [name for name in known if (skills_dir / name).exists()]
    return SDDStatus(installed=bool(found), skills=found)


# ---- init --------------------------------------------------------------


def orch_init(
    project_path: Path,
    *,
    force: bool = False,
    sdd: bool = False,
    project_name: str | None = None,
) -> int:
    """Scaffold an orch project at `project_path`.

    Returns exit code:
        0 — success
        1 — destination already has conflicting files and --force wasn't set
    """
    project_path = Path(project_path).expanduser().resolve()

    # ---- conflict check (before ANY write) --------------------------
    if not force:
        clashes = [
            m for m in _CONFLICT_MARKERS if (project_path / m).exists()
        ]
        if clashes:
            print(
                f"error: destination has conflicting files (use --force to overwrite):",
                flush=True,
            )
            for c in clashes:
                print(f"  - {project_path / c}")
            return 1

    project_path.mkdir(parents=True, exist_ok=True)

    # ---- tasks.json -------------------------------------------------
    tasks_template = (_TEMPLATES_DIR / "tasks.json.tmpl").read_text(
        encoding="utf-8"
    )
    name = project_name or project_path.name
    tasks_rendered = tasks_template.replace("PROJECT_NAME", name).replace(
        "GENERATED_AT", datetime.now(timezone.utc).date().isoformat()
    )
    (project_path / "tasks.json").write_text(tasks_rendered, encoding="utf-8")

    # ---- scripts ----------------------------------------------------
    scripts_src = _TEMPLATES_DIR / "scripts"
    scripts_dst = project_path / "scripts"
    scripts_dst.mkdir(exist_ok=True)
    for script in scripts_src.glob("*.sh"):
        target = scripts_dst / script.name
        shutil.copyfile(script, target)
        # chmod +x for owner (and preserve group/other read from copy).
        mode = target.stat().st_mode
        target.chmod(mode | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)

    # ---- specs/ -----------------------------------------------------
    specs_dst = project_path / "specs"
    specs_dst.mkdir(exist_ok=True)
    shutil.copyfile(
        _TEMPLATES_DIR / "specs" / "README.md",
        specs_dst / "README.md",
    )

    # ---- orchestrator/ ---------------------------------------------
    orch_dir = project_path / "orchestrator"
    (orch_dir / "state").mkdir(parents=True, exist_ok=True)
    (orch_dir / "state" / ".gitkeep").touch()
    for name in _YAML_DEFAULTS:
        src = _PKG_DIR / name
        shutil.copyfile(src, orch_dir / name)

    # ---- .gitignore (soft — only when absent) -----------------------
    gitignore_path = project_path / ".gitignore"
    if not gitignore_path.exists():
        shutil.copyfile(
            _TEMPLATES_DIR / "gitignore.tmpl", gitignore_path
        )

    # ---- --sdd: openspec/ layout ------------------------------------
    if sdd:
        openspec_dst = project_path / "openspec"
        (openspec_dst / "changes").mkdir(parents=True, exist_ok=True)
        (openspec_dst / "specs").mkdir(parents=True, exist_ok=True)
        shutil.copyfile(
            _TEMPLATES_DIR / "openspec" / "README.md",
            openspec_dst / "README.md",
        )

    # ---- success banner --------------------------------------------
    _print_next_steps(project_path, sdd=sdd)
    return 0


def _print_next_steps(project_path: Path, *, sdd: bool) -> None:
    """Human-friendly summary of what was created + what to do next."""
    sdd_status = detect_sdd()
    print()
    print(f"✓ orch project initialized at {project_path}")
    print()
    print("Next steps:")
    print("  1. Write your first spec:")
    print(f"       $EDITOR {project_path / 'specs' / 'f0-foundation.md'}")
    print("     (format reference: specs/README.md)")
    print()
    print("  2. Generate tasks.json from the spec:")
    print(
        f"       orch atomize --spec {project_path / 'specs' / 'f0-foundation.md'} "
        f"--tasks {project_path / 'tasks.json'}"
    )
    print()
    print("  3. Dry-run to review the plan:")
    print(f"       orch --project-root {project_path} --dry-run")
    print()
    print("  4. Full run:")
    print(f"       orch --project-root {project_path} --mode semi")
    print()
    print("Spec-Driven Development:")
    if sdd_status.installed:
        print(
            f"  ✓ SDD skills detected: {', '.join(sorted(sdd_status.skills))}"
        )
        print("    Use `/sdd-explore <topic>` in Claude Code to design specs.")
    else:
        print("  ✗ SDD skills not detected under ~/.claude/skills/")
        print(
            "    You can write specs by hand (see specs/README.md) OR install "
            "SDD skills first."
        )
    if sdd:
        print()
        print(
            f"  openspec/ layout scaffolded at {project_path / 'openspec'}"
        )
    print()
    print("Dashboard:")
    print(f"  orch dashboard --project-root {project_path}")
    print("  → http://127.0.0.1:7420")
    print()
