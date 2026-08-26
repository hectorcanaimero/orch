"""Fase 1 + Fase 2 multi-proyecto — tests focales para `paths.py` y wiring.

Alcance:
    - `_default_project_id` salta basenames genéricos (`v2`, `app`, `src`).
    - `resolve_project_paths` respeta precedencia: flag > env > cwd.
    - `render_prompt` embebe el `project_root` en el template en lugar del
      hardcode viejo (y sigue funcionando cuando `project_root is None`).
    - Fase 2: `explicit_root` + `state_layout` — modo namespaced vs legacy.
"""

from __future__ import annotations

import os
from pathlib import Path
from unittest.mock import patch

import pytest

from orchestrator.models import Task
from orchestrator.paths import (
    ProjectPaths,
    _default_project_id,
    resolve_project_paths,
)
from orchestrator.prompt_builder import render_prompt


# ---- _default_project_id ------------------------------------------------


def test_default_project_id_uses_basename() -> None:
    assert _default_project_id(Path("/tmp/myproject")) == "myproject"


def test_default_project_id_skips_generic_v2(tmp_path: Path) -> None:
    """Layout tipo `<proyecto>/v2/` → devolvé `<proyecto>`."""
    root = tmp_path / "rupies" / "v2"
    root.mkdir(parents=True)
    assert _default_project_id(root) == "rupies"


def test_default_project_id_skips_generic_app(tmp_path: Path) -> None:
    root = tmp_path / "myapp" / "app"
    root.mkdir(parents=True)
    assert _default_project_id(root) == "myapp"


def test_default_project_id_falls_back_to_unknown_at_fs_root() -> None:
    # `/` no tiene padre útil ni basename — fallback debe ser algo loggeable.
    assert _default_project_id(Path("/")) in {"unknown", ""}  # tolerante


# ---- resolve_project_paths precedence -----------------------------------


def test_resolve_flag_beats_env_and_cwd(tmp_path: Path, monkeypatch) -> None:
    monkeypatch.setenv("ORCH_PROJECT_ROOT", str(tmp_path / "from-env"))
    monkeypatch.chdir(tmp_path)
    flagged = tmp_path / "from-flag"
    flagged.mkdir()
    paths = resolve_project_paths(
        project_root_arg=str(flagged),
        project_id_arg=None,
        config_arg="config.yaml",
    )
    assert paths.project_root == flagged.resolve()


def test_resolve_env_beats_cwd(tmp_path: Path, monkeypatch) -> None:
    env_root = tmp_path / "envroot"
    env_root.mkdir()
    monkeypatch.setenv("ORCH_PROJECT_ROOT", str(env_root))
    monkeypatch.chdir(tmp_path)
    paths = resolve_project_paths(
        project_root_arg=None,
        project_id_arg=None,
        config_arg="config.yaml",
    )
    assert paths.project_root == env_root.resolve()


def test_resolve_defaults_to_cwd(tmp_path: Path, monkeypatch) -> None:
    monkeypatch.delenv("ORCH_PROJECT_ROOT", raising=False)
    monkeypatch.chdir(tmp_path)
    paths = resolve_project_paths(
        project_root_arg=None,
        project_id_arg=None,
        config_arg="config.yaml",
    )
    assert paths.project_root == tmp_path.resolve()


def test_resolve_project_id_flag_wins(tmp_path: Path, monkeypatch) -> None:
    monkeypatch.setenv("ORCH_PROJECT_ID", "from-env")
    paths = resolve_project_paths(
        project_root_arg=str(tmp_path),
        project_id_arg="from-flag",
        config_arg="config.yaml",
    )
    assert paths.project_id == "from-flag"


def test_resolve_relative_config_is_anchored_to_root(tmp_path: Path) -> None:
    paths = resolve_project_paths(
        project_root_arg=str(tmp_path),
        project_id_arg=None,
        config_arg="config.yaml",
    )
    assert paths.config_yaml == (tmp_path / "config.yaml").resolve()


def test_resolve_absolute_config_kept_as_is(tmp_path: Path) -> None:
    abs_cfg = tmp_path / "elsewhere" / "cfg.yaml"
    paths = resolve_project_paths(
        project_root_arg=str(tmp_path),
        project_id_arg=None,
        config_arg=str(abs_cfg),
    )
    assert paths.config_yaml == abs_cfg


# ---- ProjectPaths derived paths -----------------------------------------


def test_project_paths_derived_paths_are_absolute(tmp_path: Path) -> None:
    paths = resolve_project_paths(
        project_root_arg=str(tmp_path),
        project_id_arg="test-proj",
        config_arg="config.yaml",
    )
    assert paths.tasks_json == tmp_path / "tasks.json"
    assert paths.router_yaml == tmp_path / ".orchestrator" / "model_router.yaml"
    # Fase 2: --project-root explícito → layout `namespaced`,
    # state_dir = <root>/orchestrator/state/<project_id>.
    assert paths.state_dir == tmp_path / ".orchestrator" / "state" / "test-proj"
    assert paths.state_layout == "namespaced"
    assert paths.explicit_root is True
    assert paths.scripts_dir == tmp_path / "scripts"


def test_ensure_valid_passes_when_layout_present(tmp_path: Path) -> None:
    (tmp_path / "tasks.json").write_text("[]")
    (tmp_path / "scripts").mkdir()
    (tmp_path / "scripts" / "task-start.sh").write_text("#!/bin/sh\n")
    paths = resolve_project_paths(
        project_root_arg=str(tmp_path),
        project_id_arg=None,
        config_arg="config.yaml",
    )
    paths.ensure_valid()  # no exception


def test_ensure_valid_raises_when_layout_missing(tmp_path: Path) -> None:
    from orchestrator.state import CwdViolationError

    paths = resolve_project_paths(
        project_root_arg=str(tmp_path),
        project_id_arg=None,
        config_arg="config.yaml",
    )
    with pytest.raises(CwdViolationError):
        paths.ensure_valid()


# ---- render_prompt honors project_root ---------------------------------


def _mk_task(**overrides) -> Task:
    base = dict(
        id="T-X",
        phase=1,
        title="Multi-project task",
        description="ensures project_root reaches the template",
        model="opencode-go/glm-5.1",
        reason="fase-1 smoke",
        status="todo",
        dependencies=[],
        estimate_hours=0.1,
        files=[],
        spec_ref="",
        comments=[],
    )
    base.update(overrides)
    return Task(**base)


def test_render_prompt_uses_project_root(tmp_path: Path) -> None:
    """Con `project_root` explícito, `Working dir:` refleja ese path."""
    fake_root = tmp_path / "someotherproject"
    fake_root.mkdir()
    out = render_prompt(
        task=_mk_task(),
        completed_deps=[],
        spec_ref="specs/foo.md",
        run_id="r-multi",
        state_dir=tmp_path,
        project_root=fake_root,
    )
    text = out.read_text(encoding="utf-8")
    assert f"Working dir: {fake_root}" in text
    # No debe quedar rastro del hardcode viejo bajo ningún concepto.
    assert "/Volumes/PortableSSD/rupies/v2" not in text


def test_render_prompt_falls_back_to_cwd_when_no_root(
    tmp_path: Path, monkeypatch
) -> None:
    monkeypatch.chdir(tmp_path)
    out = render_prompt(
        task=_mk_task(),
        completed_deps=[],
        spec_ref="specs/foo.md",
        run_id="r-cwd",
        state_dir=tmp_path,
    )
    text = out.read_text(encoding="utf-8")
    assert f"Working dir: {tmp_path}" in text


# ---- state._ensure_v2_cwd accepts project_root -------------------------


def test_ensure_v2_cwd_with_project_root_validates_that_path(tmp_path: Path) -> None:
    from orchestrator.state import _ensure_v2_cwd, CwdViolationError

    good = tmp_path / "good"
    good.mkdir()
    (good / "tasks.json").write_text("[]")
    (good / "scripts").mkdir()
    (good / "scripts" / "task-start.sh").write_text("#!/bin/sh\n")

    # Con el root válido, no explota.
    _ensure_v2_cwd(project_root=good)

    # Con un root sin layout, sí.
    bad = tmp_path / "bad"
    bad.mkdir()
    with pytest.raises(CwdViolationError):
        _ensure_v2_cwd(project_root=bad)


def test_ensure_v2_cwd_without_arg_uses_cwd(tmp_path: Path, monkeypatch) -> None:
    from orchestrator.state import _ensure_v2_cwd, CwdViolationError

    monkeypatch.chdir(tmp_path)
    with pytest.raises(CwdViolationError):
        _ensure_v2_cwd()


# ---- Auto-detect namespaced layout when state already exists -------------


def test_autodetect_namespaced_when_orch_db_in_subdir(
    tmp_path: Path, monkeypatch
) -> None:
    """No --project-root but namespaced orch.db exists → auto-upgrade to namespaced."""
    monkeypatch.delenv("ORCH_PROJECT_ROOT", raising=False)
    monkeypatch.chdir(tmp_path)

    project_id = tmp_path.name
    namespaced_state = tmp_path / ".orchestrator" / "state" / project_id
    namespaced_state.mkdir(parents=True)
    (namespaced_state / "orch.db").touch()

    paths = resolve_project_paths(
        project_root_arg=None,
        project_id_arg=None,
        config_arg="config.yaml",
    )
    assert paths.state_layout == "namespaced"
    assert paths.state_dir == namespaced_state
    assert paths.explicit_root is False


def test_autodetect_namespaced_when_spend_jsonl_in_subdir(
    tmp_path: Path, monkeypatch
) -> None:
    """No --project-root but namespaced spend-*.jsonl exists → auto-upgrade."""
    monkeypatch.delenv("ORCH_PROJECT_ROOT", raising=False)
    monkeypatch.chdir(tmp_path)

    project_id = tmp_path.name
    namespaced_state = tmp_path / ".orchestrator" / "state" / project_id
    namespaced_state.mkdir(parents=True)
    (namespaced_state / "spend-2026-08-23.jsonl").touch()

    paths = resolve_project_paths(
        project_root_arg=None,
        project_id_arg=None,
        config_arg="config.yaml",
    )
    assert paths.state_layout == "namespaced"


def test_no_autodetect_when_namespaced_subdir_absent(
    tmp_path: Path, monkeypatch
) -> None:
    """No --project-root, no namespaced state dir → stays legacy."""
    monkeypatch.delenv("ORCH_PROJECT_ROOT", raising=False)
    monkeypatch.chdir(tmp_path)

    paths = resolve_project_paths(
        project_root_arg=None,
        project_id_arg=None,
        config_arg="config.yaml",
    )
    assert paths.state_layout == "legacy"
    assert paths.explicit_root is False


# ---- Fase 2: state_layout / explicit_root ------------------------------


def test_cwd_fallback_uses_legacy_layout(tmp_path: Path, monkeypatch) -> None:
    """Sin flag ni env → explicit_root=False, layout=legacy, state_dir sin sufijo."""
    monkeypatch.delenv("ORCH_PROJECT_ROOT", raising=False)
    monkeypatch.delenv("ORCH_PROJECT_ID", raising=False)
    monkeypatch.chdir(tmp_path)
    paths = resolve_project_paths(
        project_root_arg=None,
        project_id_arg=None,
        config_arg="config.yaml",
    )
    assert paths.explicit_root is False
    assert paths.state_layout == "legacy"
    assert paths.state_dir == tmp_path.resolve() / ".orchestrator" / "state"
    # Y NO debe tener el project_id colgado al final (retrocompat rupies).
    assert paths.project_id not in paths.state_dir.parts[-1:]


def test_flag_activates_namespaced_layout(tmp_path: Path) -> None:
    root = tmp_path / "someproj"
    root.mkdir()
    paths = resolve_project_paths(
        project_root_arg=str(root),
        project_id_arg=None,
        config_arg="config.yaml",
    )
    assert paths.explicit_root is True
    assert paths.state_layout == "namespaced"
    # project_id = basename del root (no hay generic name que saltar).
    assert paths.project_id == "someproj"
    assert paths.state_dir == root / ".orchestrator" / "state" / "someproj"


def test_env_activates_namespaced_layout(tmp_path: Path, monkeypatch) -> None:
    root = tmp_path / "envproj"
    root.mkdir()
    monkeypatch.setenv("ORCH_PROJECT_ROOT", str(root))
    paths = resolve_project_paths(
        project_root_arg=None,
        project_id_arg=None,
        config_arg="config.yaml",
    )
    assert paths.explicit_root is True
    assert paths.state_layout == "namespaced"
    assert paths.state_dir == root / ".orchestrator" / "state" / "envproj"


def test_ensure_valid_creates_state_dir_when_namespaced(tmp_path: Path) -> None:
    """Namespaced layout: `ensure_valid()` crea `state_dir` si falta."""
    root = tmp_path / "freshproj"
    root.mkdir()
    (root / "tasks.json").write_text("[]")
    (root / "scripts").mkdir()
    (root / "scripts" / "task-start.sh").write_text("#!/bin/sh\n")
    paths = resolve_project_paths(
        project_root_arg=str(root),
        project_id_arg=None,
        config_arg="config.yaml",
    )
    # antes de ensure_valid, el dir no existe
    assert not paths.state_dir.exists()
    paths.ensure_valid()
    assert paths.state_dir.exists()
    assert paths.state_dir.is_dir()


def test_ensure_valid_does_not_create_state_dir_when_legacy(
    tmp_path: Path, monkeypatch
) -> None:
    """Legacy layout: NO tocamos disco desde ensure_valid — mantiene rupies clean."""
    monkeypatch.delenv("ORCH_PROJECT_ROOT", raising=False)
    monkeypatch.delenv("ORCH_PROJECT_ID", raising=False)
    (tmp_path / "tasks.json").write_text("[]")
    (tmp_path / "scripts").mkdir()
    (tmp_path / "scripts" / "task-start.sh").write_text("#!/bin/sh\n")
    monkeypatch.chdir(tmp_path)
    paths = resolve_project_paths(
        project_root_arg=None,
        project_id_arg=None,
        config_arg="config.yaml",
    )
    assert paths.state_layout == "legacy"
    # Legacy: state_dir NO se crea desde ensure_valid.
    # (RunFile.create/EventLog/SpendLog lo crean si hace falta en su primer write).
    paths.ensure_valid()
    assert not paths.state_dir.exists()


# ---- Fase 3.1: spec_root configurable via render_prompt -----------------


def test_render_prompt_uses_default_spec_root_for_backcompat(tmp_path: Path) -> None:
    """Sin `spec_root` explícito → default rupies (`docs/rewrite-plan/...`)."""
    from orchestrator.prompt_builder import DEFAULT_SPEC_ROOT

    out = render_prompt(
        task=_mk_task(),
        completed_deps=[],
        spec_ref="specs/foo.md",
        run_id="r-default",
        state_dir=tmp_path,
    )
    text = out.read_text(encoding="utf-8")
    assert f"{DEFAULT_SPEC_ROOT}/specs/foo.md" in text


def test_render_prompt_honors_custom_spec_root(tmp_path: Path) -> None:
    """`spec_root` explícito reemplaza el default en la línea `Spec ref:`."""
    out = render_prompt(
        task=_mk_task(),
        completed_deps=[],
        spec_ref="p1/foo.md",
        run_id="r-custom",
        state_dir=tmp_path,
        spec_root="sdd/specs",
    )
    text = out.read_text(encoding="utf-8")
    assert "sdd/specs/p1/foo.md" in text
    # Y NO debe aparecer el default rupies.
    assert "docs/rewrite-plan/p1/foo.md" not in text


def test_render_prompt_missing_spec_ref_ignores_spec_root(tmp_path: Path) -> None:
    """Cuando `spec_ref` es None, el placeholder es literal — sin usar spec_root."""
    out = render_prompt(
        task=_mk_task(),
        completed_deps=[],
        spec_ref=None,
        run_id="r-none",
        state_dir=tmp_path,
        spec_root="whatever/",
    )
    text = out.read_text(encoding="utf-8")
    assert "no spec ref provided" in text
    assert "whatever/" not in text
