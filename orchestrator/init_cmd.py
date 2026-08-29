"""`orch init` — scaffolder for a new orch project (Sprint 9 + Sprint D).

Two modes, one entry point:

    * BATCH mode (unchanged from Sprint 9). All flags come from argv;
      the function `orch_init(path, force, sdd, project_name)` is a pure
      write-to-disk operation. Every existing test in `test_init.py`
      still exercises this path unchanged.

    * INTERACTIVE mode (Sprint D / Issue #16). Opt-in: triggered by
      `orch init` with no positional path and no scaffolder flags, OR
      explicit `--interactive`. Uses stdlib `input()` — no new deps.
      Falls back to a helpful error when stdin isn't a TTY (CI safety).

Contract (both modes):
    - Creates the minimum layout orch needs (H-2 consolidation): tasks.json,
      scripts/task-*.sh, .orchestrator/{state, config.yaml, model_router.yaml
      (stub)}, specs/README.md, .gitignore (when absent),
      .github/workflows/orch-ci.yml (when absent — Sprint G-1).
    - `dashboard.yaml` and `budgets.yaml` are NOT scaffolded — their defaults
      live in the packaged config.yaml (`dashboard:`) and in
      config_loader._apply_defaults (`budget:`) respectively. Backwards
      compat: if a project already has either file, load_config still applies
      it as a deep-merge override.
    - `model_router.yaml` is written as an empty-mapping stub. `orch router
      add-missing` (issue #55) populates it from tasks.json when the dev
      adds tasks referencing new models.
    - Never writes AI. Never touches the network. Never installs anything.
    - Batch mode refuses to overwrite existing files without --force.
    - Interactive mode prompts per-file before overwriting (default: keep).
    - `.gitignore` is a soft target: if the user already has one, we leave
      it alone even without --force (respect existing).
    - --sdd adds openspec/ scaffolding for teams using Spec-Driven Development.
    - The YAML defaults copied into the project are byte-identical to the
      ones shipped with the installed package — single source of truth.
"""

from __future__ import annotations

import argparse
import os
import re
import shutil
import stat
import sys
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import Callable

# ---- Package-relative paths --------------------------------------------

_PKG_DIR = Path(__file__).resolve().parent
_TEMPLATES_DIR = _PKG_DIR / "templates"
_PROJECTS_DIR = _TEMPLATES_DIR / "projects"  # H-1a: named project templates

# Files copied verbatim from the package (shipped YAML defaults).
# Each tuple is (source path relative to _PKG_DIR, destination path
# relative to project_path).
#
# H-2 (opt B, consolidation): dashboard.yaml is NO LONGER scaffolded —
# its defaults now live inline in the packaged config.yaml under a
# `dashboard:` block. Backwards compat: a project-root `dashboard.yaml`
# still overrides via deep-merge in config_loader. Same for budgets.yaml
# (load_budget_config already treats it as optional). Result: a fresh
# `orch init` writes 1 config file instead of 4.
_YAML_DEFAULTS: tuple[tuple[str, str], ...] = (
    ("config.yaml", ".orchestrator/config.yaml"),
)

# Files that would be clobbered — presence of ANY of these triggers the
# --force check. `.gitignore` deliberately not in this list (soft target).
_CONFLICT_MARKERS: tuple[str, ...] = (
    "tasks.json",
    "scripts/task-start.sh",
    "scripts/task-finish.sh",
    "scripts/task-block.sh",
    ".orchestrator/config.yaml",
    ".orchestrator/model_router.yaml",
)


# ---- SDD detection -----------------------------------------------------


@dataclass(frozen=True, slots=True)
class SDDStatus:
    """Result of scanning `~/.claude/skills/` for SDD skills."""

    installed: bool
    skills: list[str] = field(default_factory=list)


# ---- H-1a: project templates -------------------------------------------


class TemplateNotFoundError(ValueError):
    """Raised by orch_init when template=<name> doesn't match any shipped
    template directory."""


def list_templates() -> dict[str, str]:
    """Enumerate shipped project templates.

    Returns ``{name: one_line_description}`` sorted by name. A template is
    any subdir under ``templates/projects/`` that carries at least a
    ``tasks.json.tmpl`` file (the only required file — every other file
    in the template is optional and only overlaid when present).

    ``description.txt`` (first non-empty line) supplies the human blurb;
    falls back to the template name when absent.
    """
    if not _PROJECTS_DIR.exists():
        return {}
    out: dict[str, str] = {}
    for entry in sorted(_PROJECTS_DIR.iterdir()):
        if not entry.is_dir():
            continue
        if not (entry / "tasks.json.tmpl").exists():
            continue
        desc_path = entry / "description.txt"
        desc = entry.name
        if desc_path.exists():
            for line in desc_path.read_text(encoding="utf-8").splitlines():
                stripped = line.strip()
                if stripped:
                    desc = stripped
                    break
        out[entry.name] = desc
    return out


def _resolve_template(name: str) -> Path:
    """Return the template dir for *name* or raise TemplateNotFoundError."""
    candidate = _PROJECTS_DIR / name
    if not candidate.is_dir() or not (candidate / "tasks.json.tmpl").exists():
        available = ", ".join(sorted(list_templates())) or "<none>"
        raise TemplateNotFoundError(
            f"unknown template {name!r}. Available: {available}"
        )
    return candidate


def _render_template_file(src: Path, project_name: str) -> str:
    """Read a template .tmpl / .md.tmpl / .yaml.tmpl and substitute the two
    canonical placeholders — matches the pattern already used for the
    shipped tasks.json.tmpl (single source of truth for how tokens map)."""
    body = src.read_text(encoding="utf-8")
    return body.replace("PROJECT_NAME", project_name).replace(
        "GENERATED_AT", datetime.now(timezone.utc).date().isoformat()
    )


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


# ---- AGENTS.md generator -----------------------------------------------


_ABOUT_ORCH_BLOCK = (
    "## About orch (this project is orch-managed)\n"
    "\n"
    "- `orch` walks `tasks.json` as a DAG and dispatches each task to\n"
    "  Claude / Codex / OpenCode / Gemini in parallel.\n"
    "- **CLI missing?** `pipx install orch`\n"
    "  (repo: <https://github.com/hectorcanaimero/orch>).\n"
    "- **Never edit `tasks.json` status directly** — runtime state lives\n"
    "  in SQLite. Use `orch task set --id ID --status STATUS` or one of\n"
    "  the `scripts/task-*.sh` helpers.\n"
    "- **Full agent context on demand**: if you use Claude Code, run\n"
    "  `orch install-skills` once — the `/orch` skill loads only when the\n"
    "  agent needs it (token-cheap). Otherwise `orch --help` is the source\n"
    "  of truth.\n"
    "- **Read-only inspection**: `orch status`, `orch tasks`,\n"
    "  `orch events ID`, `orch logs ID`, `orch graph`.\n"
    "\n"
)


def _generate_agents_md(project_name: str) -> str:
    """Generate AGENTS.md content for a new orch-managed project."""
    db_path = f".orchestrator/state/{project_name}/orch.db"
    return (
        "# Orch Project Context\n"
        "\n"
        "> Auto-generated by `orch init`. Refresh with `orch atomize --apply`.\n"
        "> Read automatically by Claude Code and compatible agents at session start.\n"
        "\n"
        + _ABOUT_ORCH_BLOCK
        + "## State backend\n"
        "\n"
        f"- SQLite: `.orchestrator/state/{project_name}/orch.db`\n"
        "- `tasks_definition` (static): model, backend, deps, spec_ref, files, phase, estimate\n"
        "- `tasks_runtime` (dynamic): status, attempts, last_model, last_backend\n"
        "- `tasks.json`: atomize input only — **never edit runtime status here**\n"
        "\n"
        "## Check real task status\n"
        "\n"
        "```bash\n"
        f'sqlite3 {db_path} \\\n'
        '  "SELECT task_id, status, last_model FROM tasks_runtime \\\n'
        "   WHERE status != 'done' ORDER BY task_id;\"\n"
        "```\n"
        "\n"
        "## Key commands\n"
        "\n"
        "| Action | Command |\n"
        "|--------|---------|\n"
        "| View status | `orch status` |\n"
        "| Set model/status | `orch task set --id TASK --model MODEL --status done` |\n"
        "| Capture finding | `orch findings capture --type bug\\|feature\\|fix --about orch\\|project --summary \"...\" --evidence \"...\"` |\n"
        "| Publish finding | `orch findings publish <id> --repo <repo> --yes` |\n"
        "| Re-atomize | `orch atomize --apply` |\n"
        "\n"
        "## Provider concurrency caps\n"
        "\n"
        "| Provider | Max concurrent |\n"
        "|----------|---------|\n"
        "| claude | 3 |\n"
        "| opencode | 6 |\n"
        "| codex | 2 |\n"
        "\n"
        "## Model router\n"
        "\n"
        "See `.orchestrator/model_router.yaml` for backend→model mappings.\n"
    )


# ---- init --------------------------------------------------------------


def orch_init(
    project_path: Path,
    *,
    force: bool = False,
    sdd: bool = False,
    project_name: str | None = None,
    template: str | None = None,
) -> int:
    """Scaffold an orch project at `project_path`.

    Args:
        template: optional named template (H-1a) — one of the keys returned
            by ``list_templates()``. When set, the baseline `tasks.json`,
            `.orchestrator/config.yaml`, and `AGENTS.md` are overlaid with
            the template's ``.tmpl`` files (each file is optional in the
            template dir; only what's present overrides). Unknown names
            raise ``TemplateNotFoundError``.

    Returns exit code:
        0 — success
        1 — destination already has conflicting files and --force wasn't set
    """
    template_dir = _resolve_template(template) if template else None
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
    name = project_name or project_path.name
    tasks_src = (
        template_dir / "tasks.json.tmpl"
        if template_dir and (template_dir / "tasks.json.tmpl").exists()
        else _TEMPLATES_DIR / "tasks.json.tmpl"
    )
    (project_path / "tasks.json").write_text(
        _render_template_file(tasks_src, name), encoding="utf-8"
    )

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

    # ---- .orchestrator/ --------------------------------------------
    orch_dir = project_path / ".orchestrator"
    (orch_dir / "state").mkdir(parents=True, exist_ok=True)
    (orch_dir / "state" / ".gitkeep").touch()
    for src_rel, dst_rel in _YAML_DEFAULTS:
        src = _PKG_DIR / src_rel
        dst = project_path / dst_rel
        dst.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(src, dst)

    # ---- Template overlay: config.yaml (H-1a) -----------------------
    # If the chosen template ships a config.yaml.tmpl, it replaces the
    # packaged default. The default already got applied above so any keys
    # the template omits are still covered by _apply_defaults at load time.
    if template_dir and (template_dir / "config.yaml.tmpl").exists():
        (project_path / ".orchestrator" / "config.yaml").write_text(
            _render_template_file(
                template_dir / "config.yaml.tmpl", name
            ),
            encoding="utf-8",
        )

    # ---- .orchestrator/model_router.yaml (H-2: stub, not the whole
    # shipped table). load_router() requires this file to exist; a fresh
    # project has no tasks yet, so an empty mapping is valid. When the
    # dev adds tasks referencing new models, `orch router add-missing`
    # (issue #55) appends the routing entries.
    router_dst = orch_dir / "model_router.yaml"
    if not router_dst.exists() or force:
        router_dst.write_text(
            "# model_router.yaml — maps tasks.json model strings to CLI\n"
            "# invocations. This stub was generated by `orch init`.\n"
            "#\n"
            "# When you add tasks that reference new models, run:\n"
            "#     orch router add-missing --yes\n"
            "# to append inferred entries. See the shipped default at\n"
            "# `orchestrator/model_router.yaml` in the package for a\n"
            "# fully-populated reference.\n"
            "{}\n",
            encoding="utf-8",
        )

    # ---- .gitignore (soft — only when absent) -----------------------
    gitignore_path = project_path / ".gitignore"
    if not gitignore_path.exists():
        shutil.copyfile(
            _TEMPLATES_DIR / "gitignore.tmpl", gitignore_path
        )

    # ---- AGENTS.md (committed, not gitignored) -------------------------
    # H-1a: a template can ship its own AGENTS.md.tmpl with stack-specific
    # context. Falls back to the generic generator when the template omits
    # it (or when no template is in use).
    agents_md_path = project_path / "AGENTS.md"
    if not agents_md_path.exists() or force:
        template_agents = (
            template_dir / "AGENTS.md.tmpl"
            if template_dir and (template_dir / "AGENTS.md.tmpl").exists()
            else None
        )
        agents_body = (
            _render_template_file(template_agents, name)
            if template_agents
            else _generate_agents_md(name)
        )
        agents_md_path.write_text(agents_body, encoding="utf-8")

    # ---- .github/workflows/orch-ci.yml (soft — only when absent) -------
    # Gives the vcs.auto_pr / CI-polling loop (Sprint F-4) a check to watch
    # out of the box. Never overwritten once it exists (user-editable).
    workflow_path = project_path / ".github" / "workflows" / "orch-ci.yml"
    if not workflow_path.exists() or force:
        workflow_path.parent.mkdir(parents=True, exist_ok=True)
        workflow_template = (
            _TEMPLATES_DIR / "github" / "orch-ci.yml.tmpl"
        ).read_text(encoding="utf-8")
        workflow_path.write_text(
            workflow_template.replace("TEST_COMMAND", "pytest"),
            encoding="utf-8",
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
    print("  2. Preview atomize (dry, shows diff):")
    print(
        f"       orch atomize --project-root {project_path} "
        f"--file {project_path / 'specs' / 'f0-foundation.md'}"
    )
    print("     Then apply:")
    print(
        f"       orch atomize --project-root {project_path} "
        f"--file {project_path / 'specs' / 'f0-foundation.md'} --apply"
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
    print(f"✓ CI workflow: {project_path / '.github' / 'workflows' / 'orch-ci.yml'}")
    print("  (edit `github.test_command` / `github.auto_merge` in config.yaml)")
    print()
    print("Config:")
    print(f"  Single source of truth: {project_path / '.orchestrator' / 'config.yaml'}")
    print("  Edit that one file. `budgets.yaml` and `dashboard.yaml` are optional")
    print("  overrides — if you drop them in the project root they deep-merge on top.")
    print()


# ---- Interactive wizard (Sprint D / Issue #16) -------------------------

_PROJECT_ID_RE = re.compile(r"^[a-z0-9][a-z0-9_-]*$")
# Scaffolder flags whose presence disables auto-interactive mode. Kept as a
# set so the check is trivially extensible. If any of these are set, orch
# init behaves as a batch scaffolder (backwards-compat with Sprint 9).
_SCAFFOLDER_FLAGS: frozenset[str] = frozenset({
    "path",
    "force",
    "sdd",
    "project_name",
    "template",  # H-1a: --template signals batch intent
})


def _is_scaffolder_flag_provided(args: argparse.Namespace) -> bool:
    """True when the operator passed at least one batch-mode flag/argument.

    We treat `path` (positional) as a scaffolder flag — passing a path
    signals "I know what I want, don't ask me". Explicit --force/--sdd/
    --project-name likewise mean batch intent.
    """
    if getattr(args, "path", None):
        return True
    for name in ("force", "sdd", "project_name", "template"):
        val = getattr(args, name, None)
        if name == "project_name":
            if val:
                return True
        elif val:  # boolean flags
            return True
    return False


def prompt(
    message: str,
    *,
    default: str | None = None,
    validate: Callable[[str], str | None] | None = None,
    choices: list[str] | None = None,
    input_fn: Callable[[str], str] | None = None,
) -> str:
    """Loop `input()` until we get an accepted answer.

    `validate(value)` returns None on success or an error string to show.
    `choices` is a shorthand: shown in the prompt, enforced case-sensitively.
    Blank input returns `default` when set; otherwise re-prompts.

    We take `input_fn` so tests can inject a scripted sequence without
    monkeypatching the stdlib `input`. When `input_fn` is None (default) we
    dereference `builtins.input` at CALL time — so monkeypatched
    `builtins.input` in tests takes effect for the wizard path too.
    """
    if input_fn is None:
        import builtins as _builtins
        input_fn = _builtins.input
    prompt_bits = [message]
    if choices:
        prompt_bits.append(f" [{'/'.join(choices)}]")
    if default is not None:
        prompt_bits.append(f" ({default})")
    prompt_bits.append(": ")
    full = "".join(prompt_bits)

    while True:
        raw = input_fn(full)
        value = raw.strip()
        if not value and default is not None:
            value = default
        if not value:
            print("  → value required, please try again.")
            continue
        if choices and value not in choices:
            print(f"  → must be one of: {', '.join(choices)}")
            continue
        if validate is not None:
            err = validate(value)
            if err:
                print(f"  → {err}")
                continue
        return value


def _validate_project_id(value: str) -> str | None:
    if not _PROJECT_ID_RE.match(value):
        return (
            "project id must start with [a-z0-9] and contain only "
            "lowercase letters, digits, _ and -"
        )
    return None


def _validate_project_root(value: str) -> str | None:
    p = Path(value).expanduser()
    parent = p if p.exists() else p.parent
    if not parent.exists():
        return f"parent dir {parent} does not exist"
    if not os.access(parent, os.W_OK):
        return f"parent dir {parent} is not writable"
    return None


def _load_budget_presets(pkg_dir: Path) -> list[str]:
    """Read the shipped budgets.yaml and return the preset keys.

    Wizard uses this to offer the user a picklist rather than free-text.
    Falls back to a sensible default when yaml/parsing fails so we never
    hard-crash the wizard on a broken package copy.
    """
    try:
        import yaml

        data = yaml.safe_load((pkg_dir / "budgets.yaml").read_text(encoding="utf-8")) or {}
        presets = data.get("presets") or {}
        keys = sorted(presets.keys())
        return keys or ["conservative"]
    except Exception:  # noqa: BLE001
        return ["conservative", "aggressive", "shared"]


def _load_router_tier_map(pkg_dir: Path) -> dict[str, list[str]]:
    """Group router keys by tier for the wizard's model tier picker."""
    try:
        import yaml

        data = yaml.safe_load((pkg_dir / "model_router.yaml").read_text(encoding="utf-8")) or {}
        out: dict[str, list[str]] = {"premium": [], "standard": [], "cheap": []}
        for key, entry in data.items():
            if not isinstance(entry, dict):
                continue
            tier = entry.get("tier")
            if tier in out:
                out[tier].append(key)
        for tier in out:
            out[tier].sort()
        return out
    except Exception:  # noqa: BLE001
        return {"premium": [], "standard": [], "cheap": []}


def _wizard_should_overwrite(
    path: Path,
    *,
    input_fn: Callable[[str], str] | None = None,
) -> bool:
    """Prompt (default: keep existing) before overwriting one file."""
    answer = prompt(
        f"  file {path} exists — overwrite?",
        default="n",
        choices=["y", "n"],
        input_fn=input_fn,
    )
    return answer == "y"


def _detect_installed_backends() -> dict[str, str | None]:
    """Return `{backend: absolute-path-or-None}` for each known backend."""
    return {name: shutil.which(name) for name in ("claude", "codex", "opencode")}


def run_wizard(
    args: argparse.Namespace,
    *,
    input_fn: Callable[[str], str] | None = None,
    output_stream=None,
) -> int:
    """Interactive `orch init` (Sprint D / Issue #16).

    Returns the same exit-code contract as `orch_init`:
        0 — success (scaffold written + validation summary printed)
        1 — user aborted / hard write error / validation failure
    """
    if output_stream is None:
        output_stream = sys.stdout

    def _say(msg: str = "") -> None:
        print(msg, file=output_stream)

    _say("orch init — interactive setup")
    _say("Press Enter to accept defaults. Ctrl-C to abort.")
    _say("")

    # ---- Prompts --------------------------------------------------------

    project_id = prompt(
        "project id",
        validate=_validate_project_id,
        input_fn=input_fn,
    )
    project_root_str = prompt(
        "project root",
        default=str(Path.cwd() / project_id),
        validate=_validate_project_root,
        input_fn=input_fn,
    )
    project_root = Path(project_root_str).expanduser().resolve()

    # ---- template picker (H-1a) ---------------------------------------
    # An explicit --template already picked one → don't ask again. Otherwise
    # offer "blank" + every shipped template as choices, keyed by name, with
    # the description line printed inline.
    template_choice: str | None = getattr(args, "template", None) or None
    if template_choice is None:
        shipped = list_templates()
        if shipped:
            _say("")
            _say("Project templates:")
            _say("  blank          Empty tasks.json — you fill it in yourself.")
            for tname, tdesc in shipped.items():
                _say(f"  {tname.ljust(14)} {tdesc}")
            picked = prompt(
                "template",
                default="blank",
                choices=["blank", *shipped.keys()],
                input_fn=input_fn,
            )
            template_choice = None if picked == "blank" else picked
            _say("")

    # Backends probe — informational, doesn't gate anything.
    _say("")
    _say("Detected backends on PATH:")
    for name, path in _detect_installed_backends().items():
        marker = f"✓ {path}" if path else "✗ not on PATH"
        _say(f"  {name:<10} {marker}")
    _say("(missing backends can be installed later — `orch doctor` verifies)")
    _say("")

    state_backend = prompt(
        "state backend",
        default="file",
        choices=["file", "sqlite"],
        input_fn=input_fn,
    )
    _say(
        "  → file backend: JSONL + JSON in state/. Simple, easy to eyeball.\n"
        "  → sqlite backend: single orch.db, better for multi-project setups.\n"
        if state_backend == "file"
        else "  → sqlite backend selected — single orch.db file with WAL journaling.\n"
    )

    presets = _load_budget_presets(_PKG_DIR)
    budget_preset = prompt(
        "budget preset",
        default=presets[0] if presets else "conservative",
        choices=presets,
        input_fn=input_fn,
    )

    spec_root = prompt(
        "spec root (relative to project root)",
        default="specs",
        input_fn=input_fn,
    )

    # Model tier picker — optional but presented for completeness.
    tier_map = _load_router_tier_map(_PKG_DIR)
    tier_choices: dict[str, str | None] = {}
    for tier in ("premium", "standard", "cheap"):
        options = tier_map.get(tier) or []
        if not options:
            tier_choices[tier] = None
            continue
        default_choice = options[0]
        _say(f"\n{tier} tier options (pick one for meta.default_{tier}_model):")
        for opt in options:
            _say(f"  - {opt}")
        picked = prompt(
            f"  default {tier} model",
            default=default_choice,
            choices=options,
            input_fn=input_fn,
        )
        tier_choices[tier] = picked
    _say("")

    sdd_flag = prompt(
        "scaffold openspec/ layout for SDD?",
        default="n",
        choices=["y", "n"],
        input_fn=input_fn,
    ) == "y"

    # ---- Confirm + scaffold ---------------------------------------------

    project_root.mkdir(parents=True, exist_ok=True)

    # Per-file overwrite prompts.
    overwrites: dict[str, bool] = {}
    for marker in _CONFLICT_MARKERS:
        target = project_root / marker
        if target.exists():
            overwrites[marker] = _wizard_should_overwrite(target, input_fn=input_fn)

    # Scaffold everything with force=True — we've already handled overwrites
    # by (a) prompting the user and (b) restoring untouched files below.
    protected: dict[str, bytes] = {}
    for marker, do_overwrite in overwrites.items():
        if not do_overwrite:
            target = project_root / marker
            try:
                protected[marker] = target.read_bytes()
            except OSError:
                protected.pop(marker, None)

    rc = orch_init(
        project_root,
        force=True,
        sdd=sdd_flag,
        project_name=project_id,
        template=template_choice,
    )
    if rc != 0:
        _say(f"scaffold failed (exit {rc}).")
        return rc

    # Restore any protected files that the user asked to keep. Restored
    # files are also skipped from post-processing so operator-authored
    # content is never rewritten behind their back.
    for marker, blob in protected.items():
        (project_root / marker).write_bytes(blob)

    # Post-process config.yaml with the wizard's answers (state.backend,
    # spec_root, budgets_preset) so operators don't need to edit YAML by
    # hand for common choices.
    if ".orchestrator/config.yaml" not in protected:
        _post_process_config(
            project_root / ".orchestrator" / "config.yaml",
            state_backend=state_backend,
            budget_preset=budget_preset,
            spec_root=spec_root,
        )

    # Post-process tasks.json meta with tier picks (informational; the
    # atomizer/dispatcher read tasks[*].model — this is a hint for humans).
    if "tasks.json" not in protected:
        _post_process_tasks_meta(
            project_root / "tasks.json",
            project_id=project_id,
            tier_defaults=tier_choices,
        )

    # ---- Post-scaffold: inline validate --------------------------------
    _say("")
    _say("Running orch validate on the fresh scaffold...")
    from orchestrator import preflight
    from orchestrator.router import load_router
    from orchestrator.state import load_tasks

    try:
        tasks_list = load_tasks(project_root / "tasks.json")
        router = load_router(project_root / ".orchestrator" / "model_router.yaml")
        errors = preflight.validate_graph(tasks_list, router_keys=router.keys())
    except Exception as exc:  # noqa: BLE001
        _say(f"  validation could not run: {exc}")
        errors = []

    if not errors:
        _say("  ✓ validate: no issues found.")
    else:
        _say(f"  ⚠ validate found {len(errors)} issue(s):")
        for e in errors[:10]:
            _say(f"    - {e.kind}: {e.message}")
        if len(errors) > 10:
            _say(f"    ... and {len(errors) - 10} more.")

    _say("")
    _say("Setup complete! 🎉")
    _say("")
    _say("Next steps:")
    _say(f"  orch dry-run --project-root {project_root}")
    _say(f"  orch atomize --project-root {project_root} --file specs/YOUR-SPEC.md")
    _say(f"  orch doctor  --project-root {project_root}")
    _say("")

    # Offer to launch dashboard
    open_dashboard = prompt(
        "Open dashboard in browser?",
        default="y",
        choices=["y", "n"],
        input_fn=input_fn,
    ) == "y"

    if open_dashboard:
        _say("")
        start_dashboard_background(project_root)

    _say("")
    return 0


def _post_process_config(
    config_yaml: Path,
    *,
    state_backend: str,
    budget_preset: str,
    spec_root: str,
) -> None:
    """Rewrite `state.backend`, `budgets_preset`, `spec_root` in a config file.

    Minimal (regex-based) edit rather than full YAML round-trip: preserves
    the comments in the shipped config.yaml so operators see the guidance
    when they open it in $EDITOR later.
    """
    if not config_yaml.exists():
        return
    text = config_yaml.read_text(encoding="utf-8")
    text = re.sub(
        r"(?m)^(\s*backend:\s*)\S+",
        rf"\g<1>{state_backend}",
        text,
        count=1,
    )
    text = re.sub(
        r"(?m)^budgets_preset:\s*\S+",
        f"budgets_preset: {budget_preset}",
        text,
        count=1,
    )
    text = re.sub(
        r"(?m)^spec_root:\s*\S+",
        f"spec_root: {spec_root}",
        text,
        count=1,
    )
    config_yaml.write_text(text, encoding="utf-8")


def _post_process_tasks_meta(
    tasks_json: Path,
    *,
    project_id: str,
    tier_defaults: dict[str, str | None],
) -> None:
    """Stamp the wizard's picks into `meta` for downstream tools to inspect."""
    if not tasks_json.exists():
        return
    import json

    try:
        data = json.loads(tasks_json.read_text(encoding="utf-8"))
    except json.JSONDecodeError:
        return
    if not isinstance(data, dict):
        return
    meta = data.setdefault("meta", {})
    meta["project"] = project_id
    for tier, model in tier_defaults.items():
        if model:
            meta[f"default_{tier}_model"] = model
    tasks_json.write_text(json.dumps(data, indent=2) + "\n", encoding="utf-8")


# ---- CLI entry (called from orch.py _run_init_subcommand) --------------


def run_init_cli(argv: list[str]) -> int:
    """Handle `orch init [...]` — routes to batch or interactive mode.

    Kept here (rather than orch.py) so the argparser + wizard logic live
    together. `orch.py` calls this via `_run_init_subcommand`.
    """
    p = argparse.ArgumentParser(
        prog="orch init",
        description=(
            "Scaffold a new orch project. Batch mode (with flags) or "
            "interactive wizard (no flags / --interactive)."
        ),
    )
    p.add_argument(
        "path", nargs="?", metavar="PATH", default=None,
        help="Destination directory (created if missing). Optional in interactive mode.",
    )
    p.add_argument("--force", action="store_true",
                   help="Overwrite existing files at PATH (batch mode only).")
    p.add_argument("--sdd", action="store_true",
                   help="Also scaffold openspec/ layout for Spec-Driven Development.")
    p.add_argument("--project-name", default=None, metavar="NAME",
                   help="Sets `meta.project` in tasks.json. Default: basename of PATH.")
    p.add_argument("--interactive", action="store_true",
                   help="Force interactive wizard (auto-detect otherwise).")
    p.add_argument("--non-interactive", action="store_true",
                   help="Force batch mode even without scaffolder flags (fail if flags missing).")
    p.add_argument("--template", default=None, metavar="NAME",
                   help="Named project template (see --list-templates). Skips the wizard.")
    p.add_argument("--list-templates", action="store_true",
                   help="Print available project templates and exit.")
    args = p.parse_args(argv)

    if args.list_templates:
        rows = list_templates()
        if not rows:
            print("no templates shipped with this build", file=sys.stderr)
            return 1
        width = max(len(name) for name in rows)
        print("Available templates:")
        for name, desc in rows.items():
            print(f"  {name.ljust(width)}   {desc}")
        return 0

    if args.template is not None:
        try:
            _resolve_template(args.template)
        except TemplateNotFoundError as exc:
            p.error(str(exc))

    if args.interactive and args.non_interactive:
        p.error("cannot combine --interactive and --non-interactive")

    scaffolder_flags_seen = _is_scaffolder_flag_provided(args)

    # Decision matrix:
    #   explicit --interactive → wizard (or error if no TTY).
    #   explicit --non-interactive → batch (requires PATH).
    #   flags seen → batch.
    #   no flags → wizard when TTY, batch-error when not (CI safety).
    want_wizard = args.interactive or (
        not args.non_interactive and not scaffolder_flags_seen
    )

    if want_wizard:
        if not sys.stdin.isatty():
            p.error(
                "interactive mode requires a TTY on stdin; "
                "pass PATH explicitly or use --non-interactive"
            )
        return run_wizard(args)

    if not args.path:
        p.error("PATH is required in batch mode (or pass --interactive)")
    return orch_init(
        Path(args.path),
        force=args.force,
        sdd=args.sdd,
        project_name=args.project_name,
        template=args.template,
    )


# ---- Dashboard launcher (background) -----

def _open_browser(url: str) -> None:
    """Open a URL in the default browser (best-effort, non-blocking)."""
    import subprocess
    import platform

    try:
        if platform.system() == "Darwin":
            subprocess.Popen(["open", url])
        elif platform.system() == "Windows":
            subprocess.Popen(["start", url], shell=True)
        else:
            subprocess.Popen(["xdg-open", url])
    except Exception:
        pass


def start_dashboard_background(
    project_root: Path,
    port: int = 7420,
    host: str = "127.0.0.1",
) -> int | None:
    """Start the dashboard in a background process.

    Returns the process PID if successful, None otherwise.
    Prints the dashboard URL to stdout so the user knows where to go.
    """
    import subprocess
    import time

    try:
        proc = subprocess.Popen(
            [
                sys.executable,
                "-m",
                "orchestrator.orch",
                "dashboard",
                "--project-root",
                str(project_root),
                "--port",
                str(port),
                "--host",
                host,
            ],
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            start_new_session=True,  # Detach from current session
        )

        # Give the server a moment to start
        time.sleep(1)

        # Check if process is still running
        if proc.poll() is None:
            url = f"http://{host}:{port}/setup"
            print(f"\n✓ Dashboard started in background")
            print(f"  → {url}")
            print()
            _open_browser(url)
            return proc.pid

        return None
    except Exception as e:
        print(f"⚠ Could not start dashboard: {e}", file=sys.stderr)
        return None
