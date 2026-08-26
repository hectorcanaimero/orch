"""Tests for the `orch validate` subcommand (Sprint D — commit 3).

Coverage:
    * Clean scaffold → exit 0.
    * Self-loop cycle detection.
    * 3-cycle detection reports specific path.
    * Missing dependency.
    * Unresolved model route.
    * Bad config.state.backend.
    * Undersized budget preset → warn only.
    * --files flag toggles the writable check.
"""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from orchestrator.orch import _run_validate_subcommand


def _write_project(
    root: Path,
    *,
    tasks: list[dict],
    router: dict[str, dict] | None = None,
    config_extra: str = "",
    budgets_yaml: str | None = None,
) -> None:
    root.mkdir(parents=True, exist_ok=True)
    (root / "tasks.json").write_text(
        json.dumps({"meta": {}, "phases": [{"id": 1, "name": "test"}], "tasks": tasks})
    )
    scripts = root / "scripts"
    scripts.mkdir(exist_ok=True)
    for name in ("task-start.sh", "task-finish.sh", "task-block.sh"):
        (scripts / name).write_text("#!/usr/bin/env bash\nexit 0\n")
        (scripts / name).chmod(0o755)
    orch_dir = root / ".orchestrator"
    orch_dir.mkdir(exist_ok=True)
    cfg_text = "state:\n  backend: file\n"
    if budgets_yaml is not None:
        cfg_text += (
            "budgets_config: budgets.yaml\n"
            "budgets_preset: conservative\n"
            "typical_dispatch_tokens: 200000\n"
        )
    if config_extra:
        cfg_text += config_extra
    (orch_dir / "config.yaml").write_text(cfg_text)
    router = router or {
        "opencode/claude-sonnet-4-6": {
            "backend": "claude",
            "cli_model": "claude-sonnet-4-6",
            "tier": "standard",
        },
    }
    lines = []
    for key, entry in router.items():
        lines.append(f"{key}:")
        for k, v in entry.items():
            lines.append(f"  {k}: {v}")
    (orch_dir / "model_router.yaml").write_text("\n".join(lines) + "\n")
    if budgets_yaml is not None:
        (root / "budgets.yaml").write_text(budgets_yaml)


def _task(
    tid: str,
    model: str = "opencode/claude-sonnet-4-6",
    deps: list[str] | None = None,
    files: list[str] | None = None,
) -> dict:
    return {
        "id": tid,
        "phase": 1,
        "title": tid,
        "description": "",
        "model": model,
        "reason": "",
        "status": "todo",
        "dependencies": list(deps or []),
        "estimateHours": 0.5,
        "files": list(files or []),
        "specRef": "",
        "comments": [],
    }


def _common(root: Path) -> list[str]:
    return [
        "--project-root", str(root),
        "--project-id", "proj-validate",
        "--config", ".orchestrator/config.yaml",
    ]


def test_validate_clean_project_exits_0(tmp_path: Path, capsys: pytest.CaptureFixture) -> None:
    root = tmp_path / "proj"
    _write_project(root, tasks=[_task("A"), _task("B", deps=["A"])])
    rc = _run_validate_subcommand([*_common(root), "--json"])
    payload = json.loads(capsys.readouterr().out)
    assert rc == 0
    assert payload["errors"] == []


def test_validate_self_loop_flags_dep_cycle(tmp_path: Path, capsys: pytest.CaptureFixture) -> None:
    root = tmp_path / "proj"
    _write_project(root, tasks=[_task("A", deps=["A"])])
    rc = _run_validate_subcommand([*_common(root), "--json"])
    payload = json.loads(capsys.readouterr().out)
    assert rc == 2
    kinds = {e["kind"] for e in payload["errors"]}
    assert "dep.cycle" in kinds


def test_validate_3_cycle_reports_path(tmp_path: Path, capsys: pytest.CaptureFixture) -> None:
    root = tmp_path / "proj"
    _write_project(
        root,
        tasks=[
            _task("A", deps=["C"]),
            _task("B", deps=["A"]),
            _task("C", deps=["B"]),
        ],
    )
    rc = _run_validate_subcommand([*_common(root), "--json"])
    payload = json.loads(capsys.readouterr().out)
    assert rc == 2
    cycle_errs = [e for e in payload["errors"] if e["kind"] == "dep.cycle"]
    assert len(cycle_errs) == 1
    # Path form: "A -> B -> C -> A" (or a rotation), so contains all ids.
    msg = cycle_errs[0]["message"]
    for tid in ("A", "B", "C"):
        assert tid in msg
    assert "->" in msg


def test_validate_missing_dep(tmp_path: Path, capsys: pytest.CaptureFixture) -> None:
    root = tmp_path / "proj"
    _write_project(root, tasks=[_task("A", deps=["ghost"])])
    rc = _run_validate_subcommand([*_common(root), "--json"])
    payload = json.loads(capsys.readouterr().out)
    assert rc == 2
    kinds = {e["kind"] for e in payload["errors"]}
    assert "dep.missing" in kinds


def test_validate_unresolved_model(tmp_path: Path, capsys: pytest.CaptureFixture) -> None:
    root = tmp_path / "proj"
    _write_project(root, tasks=[_task("A", model="unknown/model")])
    rc = _run_validate_subcommand([*_common(root), "--json"])
    payload = json.loads(capsys.readouterr().out)
    assert rc == 2
    kinds = {e["kind"] for e in payload["errors"]}
    assert "route.unresolved" in kinds


def test_validate_bad_state_backend(tmp_path: Path, capsys: pytest.CaptureFixture) -> None:
    root = tmp_path / "proj"
    _write_project(
        root,
        tasks=[_task("A")],
        config_extra="",  # placeholder — override below
    )
    # Overwrite config with a bad backend.
    (root / ".orchestrator" / "config.yaml").write_text(
        "state:\n  backend: mysql\n"
    )
    rc = _run_validate_subcommand([*_common(root), "--json"])
    payload = json.loads(capsys.readouterr().out)
    assert rc == 2
    kinds = {e["kind"] for e in payload["errors"]}
    assert "schema.config" in kinds


def test_validate_undersized_preset_is_warn(tmp_path: Path, capsys: pytest.CaptureFixture) -> None:
    root = tmp_path / "proj"
    _write_project(
        root,
        tasks=[_task("A")],
        budgets_yaml=(
            "presets:\n"
            "  conservative:\n"
            "    claude:\n"
            "      window_hours: 5\n"
            "      token_budget: 1000\n"
            "      threshold_pct: 60\n"
        ),
    )
    rc = _run_validate_subcommand([*_common(root), "--json"])
    payload = json.loads(capsys.readouterr().out)
    # Warn only → exit code 1.
    assert rc == 1
    kinds = {e["kind"] for e in payload["errors"]}
    assert "preset.sanity" in kinds


def test_validate_files_flag_off_by_default(tmp_path: Path, capsys: pytest.CaptureFixture) -> None:
    root = tmp_path / "proj"
    _write_project(root, tasks=[_task("A", files=["deep/missing/x.txt"])])
    # No --files → the writable check is skipped, project passes.
    rc = _run_validate_subcommand([*_common(root), "--json"])
    payload = json.loads(capsys.readouterr().out)
    assert rc == 0
    assert not any(e["kind"] == "files.writable" for e in payload["errors"])


def test_validate_files_flag_flags_missing_parent(
    tmp_path: Path, capsys: pytest.CaptureFixture
) -> None:
    root = tmp_path / "proj"
    _write_project(root, tasks=[_task("A", files=["deep/missing/x.txt"])])
    rc = _run_validate_subcommand([*_common(root), "--json", "--files"])
    payload = json.loads(capsys.readouterr().out)
    kinds = {e["kind"] for e in payload["errors"]}
    assert "files.writable" in kinds
    # Missing parent is severity=warn → exit 1.
    assert rc == 1


def test_validate_human_output_when_clean(
    tmp_path: Path, capsys: pytest.CaptureFixture
) -> None:
    root = tmp_path / "proj"
    _write_project(root, tasks=[_task("A")])
    rc = _run_validate_subcommand(_common(root))
    out = capsys.readouterr().out
    assert rc == 0
    assert "no issues" in out.lower()


def test_validate_missing_router_reports_error(
    tmp_path: Path, capsys: pytest.CaptureFixture
) -> None:
    root = tmp_path / "proj"
    _write_project(root, tasks=[_task("A")])
    (root / ".orchestrator" / "model_router.yaml").unlink()
    rc = _run_validate_subcommand([*_common(root), "--json"])
    payload = json.loads(capsys.readouterr().out)
    assert rc == 2
    kinds = {e["kind"] for e in payload["errors"]}
    assert "router.missing" in kinds


def test_validate_missing_tasks_reports_error(
    tmp_path: Path, capsys: pytest.CaptureFixture
) -> None:
    root = tmp_path / "proj"
    _write_project(root, tasks=[])
    (root / "tasks.json").unlink()
    rc = _run_validate_subcommand([*_common(root), "--json"])
    payload = json.loads(capsys.readouterr().out)
    assert rc == 2
    kinds = {e["kind"] for e in payload["errors"]}
    assert "tasks.missing" in kinds
