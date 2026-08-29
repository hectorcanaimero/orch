"""Unit tests for the shared preflight primitives (Sprint D — commit 1).

Only pure-function coverage lives here; end-to-end CLI coverage is in
`test_doctor_cmd.py` and `test_validate_cmd.py` (commits 2 + 3).
"""

from __future__ import annotations

import json
import os
import stat
from pathlib import Path
from typing import Any
from unittest.mock import patch

import pytest

from orchestrator.models import Task
from orchestrator.preflight import (
    CheckResult,
    ValidationError,
    _probe_agy_auth,
    _probe_gemini_auth,
    _probe_version,
    check_backends,
    check_config_files,
    check_scripts,
    check_state_backend,
    exit_code_for_checks,
    exit_code_for_errors,
    find_cycles,
    summarize_checks,
    validate_config_shape,
    validate_cycles,
    validate_dependencies,
    validate_files_writable,
    validate_graph,
    validate_preset_sanity,
    validate_routes,
    validate_schema,
)


# ---- helpers ------------------------------------------------------------


def _task(
    tid: str,
    model: str = "opencode/claude-sonnet-4-6",
    deps: list[str] | None = None,
    files: list[str] | None = None,
    phase: int = 1,
) -> Task:
    return Task(
        id=tid,
        phase=phase,
        title=f"Task {tid}",
        description="",
        model=model,
        reason="",
        status="todo",
        dependencies=list(deps or []),
        estimate_hours=0.5,
        files=list(files or []),
        spec_ref="",
        comments=[],
    )


# ---- CheckResult / ValidationError as_json --------------------------------


def test_check_result_as_json_shape() -> None:
    r = CheckResult(name="x", status="ok", detail="hi", remediation=None)
    assert r.as_json() == {
        "name": "x",
        "status": "ok",
        "detail": "hi",
        "remediation": None,
    }


def test_validation_error_as_json_shape() -> None:
    e = ValidationError(task_id="T-A", field="model", kind="route.unresolved", message="oops")
    payload = e.as_json()
    assert payload["task_id"] == "T-A"
    assert payload["kind"] == "route.unresolved"
    assert payload["severity"] == "error"


# ---- summarize + exit codes ----------------------------------------------


def test_summarize_and_exit_codes_all_ok() -> None:
    results = [CheckResult(name="a", status="ok"), CheckResult(name="b", status="ok")]
    assert summarize_checks(results) == {"ok": 2, "warn": 0, "error": 0, "skip": 0}
    assert exit_code_for_checks(results) == 0


def test_exit_code_warn_beats_ok() -> None:
    results = [
        CheckResult(name="a", status="ok"),
        CheckResult(name="b", status="warn"),
    ]
    assert exit_code_for_checks(results) == 1


def test_exit_code_error_beats_warn() -> None:
    results = [
        CheckResult(name="a", status="warn"),
        CheckResult(name="b", status="error"),
    ]
    assert exit_code_for_checks(results) == 2


def test_exit_code_errors_helper() -> None:
    errs = [
        ValidationError(task_id=None, field="x", kind="k", message="m", severity="warn"),
    ]
    assert exit_code_for_errors(errs) == 1
    errs.append(ValidationError(task_id=None, field="y", kind="k", message="m"))
    assert exit_code_for_errors(errs) == 2
    assert exit_code_for_errors([]) == 0


# ---- schema / deps / routes ----------------------------------------------


def test_validate_schema_flags_missing_model() -> None:
    t = _task("T-A", model="")
    errs = validate_schema([t])
    kinds = {e.kind for e in errs}
    assert "schema.tasks" in kinds
    assert any("model" == e.field for e in errs)


def test_validate_dependencies_missing_dep() -> None:
    tasks = [_task("T-A", deps=["missing"])]
    errs = validate_dependencies(tasks)
    assert len(errs) == 1
    assert errs[0].kind == "dep.missing"


def test_validate_dependencies_self_loop() -> None:
    tasks = [_task("T-A", deps=["T-A"])]
    errs = validate_dependencies(tasks)
    assert len(errs) == 1
    assert errs[0].kind == "dep.cycle"


def test_validate_routes_unresolved() -> None:
    tasks = [_task("T-A", model="mystery/model")]
    errs = validate_routes(tasks, router_keys={"opencode/claude-sonnet-4-6"})
    assert len(errs) == 1
    assert errs[0].kind == "route.unresolved"


# ---- cycle detection ------------------------------------------------------


def test_find_cycles_none_on_dag() -> None:
    tasks = [
        _task("A"),
        _task("B", deps=["A"]),
        _task("C", deps=["A", "B"]),
    ]
    assert find_cycles(tasks) == []


def test_find_cycles_reports_specific_path_3cycle() -> None:
    # A -> B -> C -> A
    tasks = [
        _task("A", deps=["C"]),
        _task("B", deps=["A"]),
        _task("C", deps=["B"]),
    ]
    cycles = find_cycles(tasks)
    assert len(cycles) == 1
    cycle = cycles[0]
    # Canonical form starts with the smallest id and closes back to it.
    assert cycle[0] == cycle[-1] == "A"
    assert set(cycle[:-1]) == {"A", "B", "C"}


def test_find_cycles_ignores_self_loop() -> None:
    tasks = [_task("A", deps=["A"])]
    # Self-loops are reported by validate_dependencies as `dep.cycle`, not here.
    assert find_cycles(tasks) == []


def test_find_cycles_multiple_disjoint() -> None:
    tasks = [
        _task("A", deps=["B"]),
        _task("B", deps=["A"]),
        _task("C", deps=["D"]),
        _task("D", deps=["C"]),
    ]
    cycles = find_cycles(tasks)
    assert len(cycles) == 2
    starts = {c[0] for c in cycles}
    assert starts == {"A", "C"}


def test_validate_cycles_produces_error_row() -> None:
    tasks = [
        _task("A", deps=["B"]),
        _task("B", deps=["A"]),
    ]
    errs = validate_cycles(tasks)
    assert len(errs) == 1
    assert errs[0].kind == "dep.cycle"
    assert "->" in errs[0].message


# ---- validate_graph aggregate --------------------------------------------


def test_validate_graph_bundles_everything(tmp_path: Path) -> None:
    tasks = [
        _task("A", deps=["missing"]),
        _task("B", model="unknown", deps=["A"]),
    ]
    errs = validate_graph(tasks, router_keys=["opencode/claude-sonnet-4-6"])
    kinds = {e.kind for e in errs}
    assert "dep.missing" in kinds
    assert "route.unresolved" in kinds


# ---- files writable ------------------------------------------------------


def test_validate_files_writable_missing_parent(tmp_path: Path) -> None:
    tasks = [_task("A", files=["deep/nested/file.txt"])]
    errs = validate_files_writable(tasks, tmp_path)
    assert len(errs) == 1
    assert errs[0].kind == "files.writable"
    assert errs[0].severity == "warn"


def test_validate_files_writable_ok(tmp_path: Path) -> None:
    (tmp_path / "src").mkdir()
    (tmp_path / "src" / "x.txt").write_text("")
    tasks = [_task("A", files=["src/x.txt"])]
    assert validate_files_writable(tasks, tmp_path) == []


# ---- config shape --------------------------------------------------------


def test_validate_config_shape_ok(tmp_path: Path) -> None:
    cfg = tmp_path / "config.yaml"
    cfg.write_text("state:\n  backend: file\n")
    assert validate_config_shape(cfg) == []


def test_validate_config_shape_bad_backend(tmp_path: Path) -> None:
    cfg = tmp_path / "config.yaml"
    cfg.write_text("state:\n  backend: mysql\n")
    errs = validate_config_shape(cfg)
    assert len(errs) == 1
    assert errs[0].field == "state.backend"


def test_validate_config_shape_missing_file(tmp_path: Path) -> None:
    errs = validate_config_shape(tmp_path / "nope.yaml")
    assert len(errs) == 1
    assert errs[0].kind == "schema.config"


# ---- config files check ---------------------------------------------------


def _write_defaults(root: Path, *, tasks: list[dict[str, Any]] | None = None) -> dict[str, Path]:
    cfg = root / "config.yaml"
    cfg.write_text("state:\n  backend: file\n")
    budgets = root / "budgets.yaml"
    budgets.write_text(
        "presets:\n"
        "  conservative:\n"
        "    claude:\n"
        "      window_hours: 5\n"
        "      token_budget: 800000\n"
        "      threshold_pct: 60\n"
    )
    router = root / "model_router.yaml"
    router.write_text(
        "opencode/claude-sonnet-4-6:\n"
        "  backend: claude\n"
        "  cli_model: claude-sonnet-4-6\n"
        "  tier: standard\n"
    )
    tasks_path = root / "tasks.json"
    tasks_path.write_text(
        json.dumps({"meta": {}, "phases": [], "tasks": tasks or []})
    )
    return {
        "config": cfg,
        "budgets": budgets,
        "router": router,
        "tasks": tasks_path,
    }


def test_check_config_files_ok(tmp_path: Path) -> None:
    paths = _write_defaults(tmp_path)
    results = check_config_files(
        config_yaml=paths["config"],
        budgets_yaml=paths["budgets"],
        router_yaml=paths["router"],
        tasks_json=paths["tasks"],
        budgets_preset="conservative",
    )
    names = {r.name: r for r in results}
    assert names["config.parse"].status == "ok"
    assert names["budgets.parse"].status == "ok"
    assert names["budgets.preset"].status == "ok"
    assert names["router.parse"].status == "ok"
    assert names["tasks.parse"].status == "ok"


def test_check_config_files_missing_preset(tmp_path: Path) -> None:
    paths = _write_defaults(tmp_path)
    results = check_config_files(
        config_yaml=paths["config"],
        budgets_yaml=paths["budgets"],
        router_yaml=paths["router"],
        tasks_json=paths["tasks"],
        budgets_preset="aggressive",
    )
    names = {r.name: r for r in results}
    assert names["budgets.preset"].status == "error"
    assert "aggressive" in names["budgets.preset"].detail


def test_check_config_files_no_budgets(tmp_path: Path) -> None:
    paths = _write_defaults(tmp_path)
    paths["budgets"].unlink()
    results = check_config_files(
        config_yaml=paths["config"],
        budgets_yaml=paths["budgets"],
        router_yaml=paths["router"],
        tasks_json=paths["tasks"],
        budgets_preset="conservative",
    )
    names = {r.name: r for r in results}
    assert names["budgets.parse"].status == "skip"


def test_check_config_files_malformed_yaml(tmp_path: Path) -> None:
    paths = _write_defaults(tmp_path)
    paths["config"].write_text(": : : not yaml : :\n\n\t- broken")
    results = check_config_files(
        config_yaml=paths["config"],
        budgets_yaml=paths["budgets"],
        router_yaml=paths["router"],
        tasks_json=paths["tasks"],
    )
    names = {r.name: r for r in results}
    assert names["config.parse"].status == "error"


def test_check_config_files_malformed_json(tmp_path: Path) -> None:
    paths = _write_defaults(tmp_path)
    paths["tasks"].write_text("{not json")
    results = check_config_files(
        config_yaml=paths["config"],
        budgets_yaml=paths["budgets"],
        router_yaml=paths["router"],
        tasks_json=paths["tasks"],
    )
    names = {r.name: r for r in results}
    assert names["tasks.parse"].status == "error"


# ---- scripts + jq ---------------------------------------------------------


def test_check_scripts_all_ok(tmp_path: Path, monkeypatch) -> None:
    scripts = tmp_path / "scripts"
    scripts.mkdir()
    for name in ("task-start.sh", "task-finish.sh", "task-block.sh"):
        p = scripts / name
        p.write_text("#!/bin/bash\nexit 0\n")
        p.chmod(0o755)
    monkeypatch.setattr("shutil.which", lambda x: "/usr/bin/jq" if x == "jq" else None)
    results = check_scripts(scripts)
    names = {r.name: r for r in results}
    assert names["scripts.task-start.sh"].status == "ok"
    assert names["jq.present"].status == "ok"


def test_check_scripts_missing_script(tmp_path: Path, monkeypatch) -> None:
    scripts = tmp_path / "scripts"
    scripts.mkdir()
    (scripts / "task-start.sh").write_text("#!/bin/bash\n")
    (scripts / "task-start.sh").chmod(0o755)
    monkeypatch.setattr("shutil.which", lambda x: "/usr/bin/jq")
    results = check_scripts(scripts)
    names = {r.name: r for r in results}
    assert names["scripts.task-finish.sh"].status == "error"


def test_check_scripts_missing_jq(tmp_path: Path, monkeypatch) -> None:
    scripts = tmp_path / "scripts"
    scripts.mkdir()
    for name in ("task-start.sh", "task-finish.sh", "task-block.sh"):
        p = scripts / name
        p.write_text("#!/bin/bash\n")
        p.chmod(0o755)
    monkeypatch.setattr("shutil.which", lambda x: None)
    results = check_scripts(scripts)
    names = {r.name: r for r in results}
    assert names["jq.present"].status == "error"


def test_check_scripts_non_executable_is_warn(tmp_path: Path, monkeypatch) -> None:
    scripts = tmp_path / "scripts"
    scripts.mkdir()
    for name in ("task-start.sh", "task-finish.sh", "task-block.sh"):
        (scripts / name).write_text("#!/bin/bash\n")
    monkeypatch.setattr("shutil.which", lambda x: "/usr/bin/jq")
    results = check_scripts(scripts)
    for r in results:
        if r.name.startswith("scripts."):
            assert r.status == "warn"


# ---- state backend --------------------------------------------------------


def test_check_state_backend_file_skips_db(tmp_path: Path) -> None:
    state = tmp_path / "state"
    results = check_state_backend(state_dir=state, backend="file")
    names = {r.name: r for r in results}
    assert names["state.dir.writable"].status == "ok"
    assert names["state.db.accessible"].status == "skip"


def test_check_state_backend_sqlite_missing_db_ok(tmp_path: Path) -> None:
    # A fresh sqlite3 file is created on connect — no error even when the DB
    # file doesn't pre-exist. Schema version is 0, we don't force an expected.
    state = tmp_path / "state"
    results = check_state_backend(state_dir=state, backend="sqlite")
    names = {r.name: r for r in results}
    assert names["state.db.accessible"].status == "ok"


def test_check_state_backend_sqlite_wrong_schema(tmp_path: Path) -> None:
    import sqlite3 as _sq
    state = tmp_path / "state"
    state.mkdir()
    conn = _sq.connect(str(state / "orch.db"))
    try:
        conn.execute("PRAGMA user_version = 42")
        conn.commit()
    finally:
        conn.close()
    results = check_state_backend(
        state_dir=state,
        backend="sqlite",
        expected_schema_version=2,
    )
    names = {r.name: r for r in results}
    assert names["state.db.accessible"].status == "warn"
    assert "42" in names["state.db.accessible"].detail


# ---- backend probes -------------------------------------------------------


def test_probe_version_returns_false_when_binary_missing() -> None:
    ok, detail = _probe_version("this-binary-should-not-exist-abcdef123")
    assert ok is False
    assert "not on PATH" in detail or "No such file" in detail


def test_check_backends_marks_missing_as_error(monkeypatch) -> None:
    monkeypatch.setattr("shutil.which", lambda x: None)
    results = check_backends(tasks=[], router={})
    names = {r.name: r for r in results}
    for backend in ("claude", "codex", "opencode"):
        assert names[f"backend.{backend}"].status == "error"
        assert names[f"backend.{backend}.auth"].status == "skip"


def test_probe_agy_auth_skips_when_missing(monkeypatch) -> None:
    monkeypatch.setattr("shutil.which", lambda x: None)
    result = _probe_agy_auth()
    assert result.name == "backend.agy.auth"
    assert result.status == "skip"
    assert "not installed" in result.detail


def test_probe_agy_auth_ok_when_present(monkeypatch) -> None:
    monkeypatch.setattr("shutil.which", lambda x: "/usr/local/bin/agy" if x == "agy" else None)
    result = _probe_agy_auth()
    assert result.name == "backend.agy.auth"
    assert result.status == "ok"
    assert "agy" in result.detail.lower()


def test_probe_gemini_auth_skips_when_missing(monkeypatch) -> None:
    monkeypatch.setattr("shutil.which", lambda x: None)
    result = _probe_gemini_auth()
    assert result.name == "backend.gemini.auth"
    assert result.status == "skip"


def test_probe_gemini_auth_ok_when_present(monkeypatch) -> None:
    monkeypatch.setattr("shutil.which", lambda x: "/usr/local/bin/gemini" if x == "gemini" else None)
    result = _probe_gemini_auth()
    assert result.name == "backend.gemini.auth"
    assert result.status == "ok"


def test_check_backends_respects_referenced_only(monkeypatch) -> None:
    class FakeRoute:
        def __init__(self, backend: str) -> None:
            self.backend = backend
    router = {"only/opencode-model": FakeRoute("opencode")}
    tasks = [_task("A", model="only/opencode-model")]
    monkeypatch.setattr("shutil.which", lambda x: None)
    results = check_backends(tasks=tasks, router=router)
    names = {r.name for r in results}
    assert "backend.opencode" in names
    assert "backend.claude" not in names
    assert "backend.codex" not in names


# ---- preset sanity --------------------------------------------------------


def test_validate_preset_sanity_flags_undersized(tmp_path: Path) -> None:
    budgets = tmp_path / "budgets.yaml"
    budgets.write_text(
        "presets:\n"
        "  tiny:\n"
        "    claude:\n"
        "      window_hours: 5\n"
        "      token_budget: 1000\n"
        "      threshold_pct: 60\n"
    )
    errs = validate_preset_sanity(budgets, "tiny", typical_dispatch_tokens=200_000)
    assert len(errs) == 1
    assert errs[0].kind == "preset.sanity"
    assert errs[0].severity == "warn"


def test_validate_preset_sanity_missing_preset(tmp_path: Path) -> None:
    budgets = tmp_path / "budgets.yaml"
    budgets.write_text(
        "presets:\n"
        "  conservative:\n"
        "    claude:\n"
        "      window_hours: 5\n"
        "      token_budget: 800000\n"
        "      threshold_pct: 60\n"
    )
    errs = validate_preset_sanity(budgets, "does-not-exist", typical_dispatch_tokens=200_000)
    assert len(errs) == 1
    assert "does-not-exist" in errs[0].message


def test_validate_preset_sanity_no_budgets_file(tmp_path: Path) -> None:
    assert validate_preset_sanity(tmp_path / "nope.yaml", "any", 200_000) == []
