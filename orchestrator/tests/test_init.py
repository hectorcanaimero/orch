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
    TemplateNotFoundError,
    detect_sdd,
    list_templates,
    orch_init,
    run_init_cli,
)
from orchestrator.models import Task


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
    # And it must be APPENDABLE. It used to end with an explicit `{}`, which is
    # a complete YAML document — appending a block mapping after it produced a
    # file PyYAML refuses to parse, so `orch router add-missing` broke the very
    # file whose own comment tells you to run it.
    assert "{}" not in text, "the stub must not close the document"
    appended = text + "\nsome/model:\n  backend: claude\n  cli_model: m\n  tier: standard\n"
    assert yaml.safe_load(appended) == {
        "some/model": {"backend": "claude", "cli_model": "m", "tier": "standard"}
    }


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


# ---- Project templates (Sprint H-1a) ---------------------------------------


def test_list_templates_includes_python_api() -> None:
    """python-api must ship in the packaged templates."""
    rows = list_templates()
    assert "python-api" in rows
    assert rows["python-api"].strip()  # description is not empty


def test_orch_init_with_python_api_template_writes_seeded_tasks(
    tmp_path: Path,
) -> None:
    dest = tmp_path / "proj"
    assert orch_init(dest, template="python-api") == 0

    payload = json.loads((dest / "tasks.json").read_text(encoding="utf-8"))
    assert payload["meta"]["template"] == "python-api"
    task_ids = [t["id"] for t in payload["tasks"]]
    assert "F0.T1" in task_ids
    assert "F1.T1" in task_ids
    # DAG dependency preserved end-to-end.
    f1 = next(t for t in payload["tasks"] if t["id"] == "F1.T1")
    assert "F0.T1" in f1["dependencies"]


def test_orch_init_with_python_api_template_overrides_config_and_agents(
    tmp_path: Path,
) -> None:
    dest = tmp_path / "proj"
    assert orch_init(dest, template="python-api") == 0

    cfg_text = (dest / ".orchestrator" / "config.yaml").read_text(encoding="utf-8")
    # Template config keeps python-friendly defaults (pytest CI command).
    assert "test_command: pytest" in cfg_text

    agents_text = (dest / "AGENTS.md").read_text(encoding="utf-8")
    # Template AGENTS.md carries the stack blurb, not the generic one.
    assert "FastAPI" in agents_text
    # Placeholders are rendered, not raw.
    assert "PROJECT_NAME" not in agents_text


def test_orch_init_unknown_template_raises_template_not_found(
    tmp_path: Path,
) -> None:
    with pytest.raises(TemplateNotFoundError) as exc_info:
        orch_init(tmp_path / "proj", template="does-not-exist")
    # Error message must name the available options so the operator can fix
    # the typo without re-running --list-templates.
    assert "python-api" in str(exc_info.value)


def test_orch_init_without_template_matches_pre_H1a_behavior(
    tmp_path: Path,
) -> None:
    """Regression guard: template=None → the pre-existing empty tasks.json
    skeleton is written verbatim, no template metadata leaks in."""
    dest = tmp_path / "proj"
    assert orch_init(dest) == 0
    payload = json.loads((dest / "tasks.json").read_text(encoding="utf-8"))
    assert payload["tasks"] == []
    assert "template" not in payload["meta"]


# ---- CLI: --list-templates / --template ------------------------------------


def test_cli_list_templates_prints_available_and_exits_zero(
    capsys: pytest.CaptureFixture,
) -> None:
    rc = run_init_cli(["--list-templates"])
    out = capsys.readouterr().out
    assert rc == 0
    assert "python-api" in out


def test_cli_template_scaffolds_project(tmp_path: Path) -> None:
    dest = tmp_path / "proj"
    rc = run_init_cli([str(dest), "--template", "python-api"])
    assert rc == 0
    assert (dest / "tasks.json").exists()
    payload = json.loads((dest / "tasks.json").read_text(encoding="utf-8"))
    assert payload["meta"]["template"] == "python-api"


def test_cli_template_unknown_exits_2(
    tmp_path: Path, capsys: pytest.CaptureFixture,
) -> None:
    with pytest.raises(SystemExit) as exc_info:
        run_init_cli([str(tmp_path / "proj"), "--template", "bogus-template"])
    # argparse.error → exit 2.
    assert exc_info.value.code == 2
    err = capsys.readouterr().err
    assert "bogus-template" in err
    assert "python-api" in err  # available list surfaced


# ---- chatbot-whatsapp template (Sprint H-1b) -------------------------------


def test_list_templates_includes_chatbot_whatsapp() -> None:
    rows = list_templates()
    assert "chatbot-whatsapp" in rows
    assert "waha" in rows["chatbot-whatsapp"].lower() or "whatsapp" in rows["chatbot-whatsapp"].lower()


def test_orch_init_chatbot_whatsapp_writes_dag_across_three_phases(
    tmp_path: Path,
) -> None:
    dest = tmp_path / "proj"
    assert orch_init(dest, template="chatbot-whatsapp") == 0

    payload = json.loads((dest / "tasks.json").read_text(encoding="utf-8"))
    assert payload["meta"]["template"] == "chatbot-whatsapp"
    # Phases F0/F1/F2 present.
    phase_ids = {p["id"] for p in payload["phases"]}
    assert phase_ids == {0, 1, 2}
    # DAG: F2.T1 depends on F1.T1 (which depends on F0.T2).
    tasks_by_id = {t["id"]: t for t in payload["tasks"]}
    assert "F2.T1" in tasks_by_id
    assert "F1.T1" in tasks_by_id["F2.T1"]["dependencies"]
    assert "F0.T2" in tasks_by_id["F1.T1"]["dependencies"]


def test_orch_init_chatbot_whatsapp_enables_tunnel_by_default(
    tmp_path: Path,
) -> None:
    """The whole ICP is 'agencies delivering a bot to a client' — the tunnel
    default is on so the shareable URL works out of the box."""
    dest = tmp_path / "proj"
    assert orch_init(dest, template="chatbot-whatsapp") == 0

    cfg_text = (dest / ".orchestrator" / "config.yaml").read_text(encoding="utf-8")
    # tunnel.enabled: true AND auto_start: true both present.
    assert "enabled: true" in cfg_text
    assert "auto_start: true" in cfg_text


def test_orch_init_chatbot_whatsapp_agents_md_documents_stack(
    tmp_path: Path,
) -> None:
    dest = tmp_path / "proj"
    assert orch_init(dest, template="chatbot-whatsapp") == 0

    agents_text = (dest / "AGENTS.md").read_text(encoding="utf-8")
    # Stack keywords the agent needs at session start.
    assert "Waha" in agents_text
    assert "anthropic" in agents_text
    # Security note (webhook auth).
    assert "X-Api-Key" in agents_text
    # Placeholders rendered.
    assert "PROJECT_NAME" not in agents_text


# ---- data-pipeline template (Sprint H-1c) ----------------------------------


def test_list_templates_includes_data_pipeline() -> None:
    rows = list_templates()
    assert "data-pipeline" in rows
    desc = rows["data-pipeline"].lower()
    assert "duckdb" in desc or "etl" in desc


def test_orch_init_data_pipeline_writes_four_phase_dag(tmp_path: Path) -> None:
    dest = tmp_path / "proj"
    assert orch_init(dest, template="data-pipeline") == 0

    payload = json.loads((dest / "tasks.json").read_text(encoding="utf-8"))
    assert payload["meta"]["template"] == "data-pipeline"
    phase_ids = {p["id"] for p in payload["phases"]}
    assert phase_ids == {0, 1, 2, 3}
    tasks_by_id = {t["id"]: t for t in payload["tasks"]}
    # DAG chain: F3 → F2 → F1 → F0.
    assert "F2.T1" in tasks_by_id["F3.T1"]["dependencies"]
    assert "F1.T1" in tasks_by_id["F2.T1"]["dependencies"]
    assert "F0.T1" in tasks_by_id["F1.T1"]["dependencies"]


def test_orch_init_data_pipeline_agents_md_documents_duckdb_and_apscheduler(
    tmp_path: Path,
) -> None:
    dest = tmp_path / "proj"
    assert orch_init(dest, template="data-pipeline") == 0

    agents_text = (dest / "AGENTS.md").read_text(encoding="utf-8")
    assert "DuckDB" in agents_text
    assert "apscheduler" in agents_text.lower()
    # Layered-schema convention (raw / marts).
    assert "raw." in agents_text and "marts." in agents_text
    # Placeholders rendered.
    assert "PROJECT_NAME" not in agents_text


# ---- nextjs-saas template (Sprint H-1d) ------------------------------------


def test_list_templates_includes_nextjs_saas() -> None:
    rows = list_templates()
    assert "nextjs-saas" in rows
    desc = rows["nextjs-saas"].lower()
    assert "next.js" in desc


def test_orch_init_nextjs_saas_writes_four_phase_dag(tmp_path: Path) -> None:
    dest = tmp_path / "proj"
    assert orch_init(dest, template="nextjs-saas") == 0

    payload = json.loads((dest / "tasks.json").read_text(encoding="utf-8"))
    assert payload["meta"]["template"] == "nextjs-saas"
    phase_ids = {p["id"] for p in payload["phases"]}
    assert phase_ids == {0, 1, 2, 3}
    tasks_by_id = {t["id"]: t for t in payload["tasks"]}
    # DAG: F2.T2 depends on both F1.T1 (Clerk) and F2.T1 (Drizzle schema).
    assert "F1.T1" in tasks_by_id["F2.T2"]["dependencies"]
    assert "F2.T1" in tasks_by_id["F2.T2"]["dependencies"]
    # F3 depends on both foundation and schema.
    assert "F0.T1" in tasks_by_id["F3.T1"]["dependencies"]
    assert "F2.T1" in tasks_by_id["F3.T1"]["dependencies"]


def test_orch_init_nextjs_saas_config_uses_pnpm_test(tmp_path: Path) -> None:
    dest = tmp_path / "proj"
    assert orch_init(dest, template="nextjs-saas") == 0
    cfg_text = (dest / ".orchestrator" / "config.yaml").read_text(encoding="utf-8")
    # pnpm is the ICP-standard package manager for this stack.
    assert "test_command: pnpm test" in cfg_text
    # Same "share the URL" default as chatbot-whatsapp.
    assert "enabled: true" in cfg_text
    assert "auto_start: true" in cfg_text


def test_orch_init_nextjs_saas_agents_md_documents_full_stack(
    tmp_path: Path,
) -> None:
    dest = tmp_path / "proj"
    assert orch_init(dest, template="nextjs-saas") == 0
    agents_text = (dest / "AGENTS.md").read_text(encoding="utf-8")
    # Stack keywords the agent needs at session start.
    assert "Next.js" in agents_text
    assert "Clerk" in agents_text
    assert "Supabase" in agents_text
    assert "Drizzle" in agents_text
    # Security rule the agent must respect.
    assert "CLERK_SECRET_KEY" in agents_text
    # Placeholders rendered.
    assert "PROJECT_NAME" not in agents_text


# ---- Regression: templates must use Task's camelCase JSON keys ------------


@pytest.mark.parametrize(
    "template", ["python-api", "data-pipeline", "nextjs-saas", "chatbot-whatsapp"]
)
def test_template_tasks_are_readable_by_task_from_json(
    template: str, tmp_path: Path
) -> None:
    """Regression guard: `tasks.json.tmpl` used snake_case `estimate_hours` /
    `spec_ref` keys, which `Task.from_json` never reads (it looks for
    `estimateHours` / `specRef`) — every task scaffolded from a template
    silently got `estimate_hours=0.0` and `spec_ref=""`, so the dashboard ETA
    showed "—" and the dispatch prompt dropped the "Spec ref (READ FIRST)"
    line for every templated project."""
    dest = tmp_path / "proj"
    assert orch_init(dest, template=template) == 0
    payload = json.loads((dest / "tasks.json").read_text(encoding="utf-8"))
    assert payload["tasks"], f"{template} template produced zero tasks"
    for raw in payload["tasks"]:
        task = Task.from_json(raw)
        assert task.estimate_hours > 0, f"{template}/{task.id}: estimate_hours <= 0"
        assert task.spec_ref, f"{template}/{task.id}: spec_ref is empty"


# ---- G0.2: sqlite + worktrees + auto PR are the shipped defaults -----------


@pytest.mark.parametrize(
    "template", [None, "python-api", "data-pipeline", "nextjs-saas", "chatbot-whatsapp"]
)
def test_init_ships_sqlite_worktrees_and_auto_pr_on(
    tmp_path: Path, template: str | None
) -> None:
    """Every scaffold path — blank or templated — must produce a config with
    state.backend=sqlite, dispatch.worktree_mode=true and vcs.auto_pr=true.
    github.auto_merge stays off: it needs branch protection the user owns."""
    import yaml

    dest = tmp_path / (template or "blank")
    assert orch_init(dest, template=template) == 0
    cfg = yaml.safe_load(
        (dest / ".orchestrator" / "config.yaml").read_text(encoding="utf-8")
    )
    assert cfg["state"]["backend"] == "sqlite"
    assert cfg["dispatch"]["worktree_mode"] is True
    assert cfg["vcs"]["auto_pr"] is True
    assert cfg["github"]["auto_merge"] is False


# ---- spec_root: a templated project must point at its own specs ----------


@pytest.mark.parametrize(
    "template", ["python-api", "data-pipeline", "nextjs-saas", "chatbot-whatsapp"]
)
def test_template_config_pins_spec_root_to_specs(tmp_path: Path, template: str) -> None:
    """Without this, `_apply_defaults` supplied `docs/rewrite-plan` — a path
    from orch's own repo — to every project scaffolded from a template."""
    import yaml

    from orchestrator.config_loader import load_config

    dest = tmp_path / template
    assert orch_init(dest, template=template) == 0
    cfg_path = dest / ".orchestrator" / "config.yaml"

    raw = yaml.safe_load(cfg_path.read_text(encoding="utf-8"))
    assert raw["spec_root"] == "specs", "the written config must say so out loud"

    effective = load_config(cfg_path, project_root=dest)
    assert effective["spec_root"] == "specs"


def _spec_ref_line(text: str) -> str:
    """The one line of a prompt this section is about."""
    for line in text.splitlines():
        if line.startswith("Spec ref (READ FIRST): "):
            return line[len("Spec ref (READ FIRST): "):]
    raise AssertionError("the prompt has no spec-ref line")


@pytest.mark.parametrize(
    "template", ["python-api", "data-pipeline", "nextjs-saas", "chatbot-whatsapp"]
)
def test_templated_task_prompt_points_at_the_projects_own_specs(
    tmp_path: Path, template: str
) -> None:
    """Every task of every template, resolved the way a real dispatch does.

    Two bugs have lived on this line. `docs/rewrite-plan/...` (fixed in #102)
    was orch's own repo layout leaking into scaffolded projects. `specs/specs/
    ...` came next, because the templates' `specRef` values carried the
    `specs/` prefix that `spec_root` already supplies — so the prompt told the
    agent to READ FIRST a file one directory deeper than the one `orch init`
    creates.

    The assertion is the whole resolved path, not a substring of it. The
    previous version of this test checked `"Spec ref (READ FIRST): specs/" in
    text`, which `specs/specs/f0-foundation.md#T1` satisfies — a test shaped to
    pass rather than to check.
    """
    from orchestrator.config_loader import load_config
    from orchestrator.prompt_builder import render_prompt
    from orchestrator.state import load_tasks

    dest = tmp_path / template
    assert orch_init(dest, template=template) == 0

    cfg = load_config(dest / ".orchestrator" / "config.yaml", project_root=dest)
    tasks = load_tasks(dest / "tasks.json")
    assert tasks, "the template seeds tasks"

    for task in tasks:
        assert task.spec_ref, f"{task.id} has no specRef — see PR #96"
        out = render_prompt(
            task=task,
            completed_deps=[],
            spec_ref=task.spec_ref,
            run_id="r-spec-root",
            state_dir=tmp_path / "state" / template,
            project_root=dest,
            spec_root=cfg["spec_root"],
        )
        rendered = _spec_ref_line(out.read_text(encoding="utf-8"))

        assert rendered == f"specs/{task.spec_ref}", (
            f"{template}/{task.id}: the prompt resolves to {rendered!r}"
        )
        assert "docs/rewrite-plan" not in rendered
        assert "specs/specs" not in rendered

        # The path has to land in the directory `orch init` actually creates.
        # The spec file itself is the user's to write — `_print_next_steps`
        # tells them to — so what is checked is the directory, and that the
        # prompt and the wizard name the same file.
        path_part = rendered.split("#", 1)[0]
        assert (dest / path_part).parent == dest / "specs", (
            f"{template}/{task.id}: {path_part!r} is not inside the specs/ "
            f"directory orch init creates"
        )

    # And the file the wizard tells the user to write is the one the first
    # task's prompt asks the agent to read. These two drifted apart silently
    # once already.
    first = tasks[0]
    wizard_path = dest / "specs" / "f0-foundation.md"
    prompt_path = dest / f"specs/{first.spec_ref}".split("#", 1)[0]
    assert prompt_path == wizard_path, (
        f"{template}: the wizard says to write {wizard_path}, the prompt asks "
        f"the agent to read {prompt_path}"
    )


# ---- bug 14: a scaffolded project has to actually run --------------------


def _orch(*args: str) -> "subprocess.CompletedProcess[str]":
    import subprocess
    import sys

    return subprocess.run(
        [sys.executable, "-m", "orchestrator.orch", *args],
        capture_output=True, text=True, timeout=180,
    )


@pytest.mark.parametrize(
    "template",
    ["python-api", "data-pipeline", "nextjs-saas", "chatbot-whatsapp", None],
)
def test_scaffolded_project_dry_runs_clean(tmp_path: Path, template: str | None) -> None:
    """The test that was missing: not that `orch init` writes files, but that
    the project it writes WORKS.

    Bug 14. A template ships tasks, every task names a model, and the router
    stub was empty — so `orch init --template python-api` exited 0 and the
    first command the banner suggests exited 1 with "model_router.yaml is
    missing entries for 2 task(s)". Running `orch router add-missing --yes`,
    which both the error and the stub's own comment recommend, then wrote a
    file PyYAML could not parse, so the documented escape hatch was broken
    too.

    This runs the real binary against a real scaffold. Every existing init
    test asserts on files; none of them would have caught either half.
    """
    dest = tmp_path / (template or "blank")
    assert orch_init(dest, template=template) == 0

    result = _orch("--project-root", str(dest), "--dry-run")
    assert result.returncode == 0, (
        f"a freshly scaffolded {template or 'blank'} project failed its first "
        f"command:\n{result.stderr[-2000:]}"
    )


@pytest.mark.parametrize(
    "template", ["python-api", "data-pipeline", "nextjs-saas", "chatbot-whatsapp"]
)
def test_init_with_template_routes_every_task_model(
    tmp_path: Path, template: str
) -> None:
    """Every model the template's tasks reference has a route after init."""
    from orchestrator.router import load_router, missing_models
    from orchestrator.state import load_tasks

    dest = tmp_path / template
    assert orch_init(dest, template=template) == 0

    router = load_router(dest / ".orchestrator" / "model_router.yaml")
    tasks = load_tasks(dest / "tasks.json")
    assert tasks, "a template scaffolds tasks"
    assert missing_models(tasks, router) == [], (
        f"{template}: models with no route after init"
    )
    # Inferred, not invented: the key is `backend/cli_model`.
    for model in {t.model for t in tasks}:
        entry = router[model]
        assert entry.backend == model.split("/", 1)[0]
        assert entry.cli_model == model.split("/", 1)[1]
        # `tier` cannot be inferred from a model name and drives the budget
        # gate, so it defaults to standard and the banner says to review it.
        assert entry.tier == "standard"


def test_init_without_a_template_adds_no_routes(tmp_path: Path) -> None:
    """A blank project has no tasks, so there is nothing to route and the stub
    stays empty. Populating it with guesses would be worse than leaving it."""
    from orchestrator.router import load_router

    dest = tmp_path / "blank"
    assert orch_init(dest) == 0
    assert load_router(dest / ".orchestrator" / "model_router.yaml") == {}


def test_next_steps_name_the_router_command(tmp_path: Path, capsys) -> None:
    """The banner names `orch router add-missing`, for the model a user adds by
    hand later. Init has already routed the template's own models; the next one
    is the one with no route, and the error it produces does not say where it
    came from."""
    dest = tmp_path / "proj"
    assert orch_init(dest, template="python-api") == 0
    out = capsys.readouterr().out
    assert "orch router add-missing" in out
    # And it reports what it routed, so the tier default is visible.
    assert "added 1 route(s)" in out
    assert "tier defaults to `standard`" in out


# ---- _populate_router_from_tasks: the paths a happy scaffold never takes ----


def _scaffolded(tmp_path: Path, template: str | None = "python-api") -> Path:
    dest = tmp_path / "proj"
    assert orch_init(dest, template=template) == 0
    return dest


def test_populate_router_survives_unreadable_inputs(tmp_path: Path, capsys) -> None:
    """A scaffold that got this far has produced a usable project. Raising here
    would leave one behind with no explanation, so the helper reports and
    returns nothing — and the message names the command that finishes the job.
    """
    from orchestrator.init_cmd import _populate_router_from_tasks

    dest = _scaffolded(tmp_path)
    capsys.readouterr()
    (dest / "tasks.json").unlink()

    added = _populate_router_from_tasks(dest, dest / ".orchestrator")
    assert added == []
    out = capsys.readouterr().out
    assert "could not read tasks or router" in out
    assert "orch router add-missing --yes" in out


def test_populate_router_reports_a_model_it_cannot_infer(tmp_path: Path, capsys) -> None:
    """`infer_route_entry` returns None for a key with no `backend/` prefix —
    a bare model string, or a backend orch does not know. That needs a human,
    not a guess, and the line says which model and why."""
    from orchestrator.init_cmd import _populate_router_from_tasks

    dest = _scaffolded(tmp_path)
    capsys.readouterr()
    tasks = json.loads((dest / "tasks.json").read_text(encoding="utf-8"))
    tasks["tasks"][0]["model"] = "bare-model-with-no-slash"
    (dest / "tasks.json").write_text(json.dumps(tasks), encoding="utf-8")
    # Start from a stub again so the run has something to do.
    (dest / ".orchestrator" / "model_router.yaml").write_text("# stub\n", encoding="utf-8")

    added = _populate_router_from_tasks(dest, dest / ".orchestrator")
    out = capsys.readouterr().out
    assert "bare-model-with-no-slash" in out
    assert "cannot infer a backend" in out
    # The other model in the template is still routed — one uninferable entry
    # must not cost the rest.
    assert "claude/claude-sonnet-4-6" in added


def test_populate_router_survives_a_failed_write(tmp_path: Path, capsys, monkeypatch) -> None:
    """Same contract as the read failure: report, do not raise.

    The write is made to fail by patching `append_router_entries` rather than
    by breaking the file, because every way of breaking the file makes the READ
    fail first — and a test that cannot tell the two branches apart is not
    testing either. The assertion is on the write branch's own wording.
    """
    from orchestrator.init_cmd import _populate_router_from_tasks

    dest = _scaffolded(tmp_path)
    capsys.readouterr()
    # Back to a stub, so there is something left to add.
    (dest / ".orchestrator" / "model_router.yaml").write_text("# stub\n", encoding="utf-8")

    import orchestrator.router as router_mod

    def boom(*_args, **_kwargs):
        raise OSError("no space left on device")

    monkeypatch.setattr(router_mod, "append_router_entries", boom)

    added = _populate_router_from_tasks(dest, dest / ".orchestrator")
    assert added == []
    out = capsys.readouterr().out
    assert "could not write routes" in out, out
    assert "no space left on device" in out
    assert "orch router add-missing --yes" in out
