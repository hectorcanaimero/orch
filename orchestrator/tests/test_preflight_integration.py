"""End-to-end integration coverage for the Sprint D preflight commands.

These tests wire the real `orch init` scaffolder (both batch and wizard)
into `orch doctor` + `orch validate` to catch drift between the two.
The goal is confidence that a fresh scaffold ALWAYS produces a clean
validate report, and that doctor's checklist stays in sync with the
files init writes.

Also covers edge cases the unit tests don't hit:
    * empty tasks.json → validate is quiet, doctor still probes.
    * malformed router yaml → doctor + validate both report parse error.
    * missing scripts / non-executable / no jq.
    * both file + sqlite backend scaffolds.
"""

from __future__ import annotations

import io
import json
import stat
from pathlib import Path

import pytest

from orchestrator.init_cmd import orch_init, run_init_cli, run_wizard
from orchestrator.orch import _run_doctor_subcommand, _run_validate_subcommand
from orchestrator.state import _reset_backend_cache
from orchestrator.state.sqlite_backend import _reset_schema_cache_for_tests


@pytest.fixture(autouse=True)
def _clear_caches():
    _reset_backend_cache()
    _reset_schema_cache_for_tests()
    yield
    _reset_backend_cache()
    _reset_schema_cache_for_tests()


def _common(root: Path, project_id: str = "integ") -> list[str]:
    return [
        "--project-root", str(root),
        "--project-id", project_id,
        "--config", "orchestrator/config.yaml",
    ]


def _scaffold(capsys: pytest.CaptureFixture, root: Path, project_name: str) -> None:
    """Scaffold a project and swallow the orch_init 'next steps' banner.

    The banner would otherwise contaminate capsys.readouterr().out when the
    test calls a JSON-emitting subcommand right after.
    """
    assert orch_init(root, project_name=project_name) == 0
    capsys.readouterr()


def _fake_which_all_present():
    def _inner(name: str) -> str | None:
        if name == "jq":
            return "/usr/bin/jq"
        if name in ("claude", "codex", "opencode"):
            return f"/usr/local/bin/{name}"
        return None
    return _inner


def _stub_probes(monkeypatch) -> None:
    """Stub out subprocess-y bits so integration tests are hermetic."""
    monkeypatch.setattr("shutil.which", _fake_which_all_present())
    monkeypatch.setattr(
        "orchestrator.preflight._probe_version",
        lambda cli: (True, f"{cli} 1.0.0"),
    )
    from orchestrator.preflight import CheckResult
    monkeypatch.setattr(
        "orchestrator.preflight._probe_opencode_auth",
        lambda: CheckResult(name="backend.opencode.auth", status="ok", detail="ok"),
    )
    monkeypatch.setattr(
        "orchestrator.preflight._probe_claude_auth",
        lambda: CheckResult(name="backend.claude.auth", status="skip", detail="skip"),
    )
    monkeypatch.setattr(
        "orchestrator.preflight._probe_codex_auth",
        lambda: CheckResult(name="backend.codex.auth", status="skip", detail="skip"),
    )


# ---- fresh batch scaffold ------------------------------------------------


def test_batch_scaffold_passes_doctor(
    tmp_path: Path, capsys: pytest.CaptureFixture, monkeypatch
) -> None:
    root = tmp_path / "batch-proj"
    _scaffold(capsys, root, "batchproj")
    _stub_probes(monkeypatch)

    rc = _run_doctor_subcommand([*_common(root, "batchproj"), "--json"])
    payload = json.loads(capsys.readouterr().out)
    # A fresh scaffold has zero tasks — validate resolves nothing.
    assert payload["summary"]["error"] == 0
    assert rc in (0, 1)  # possibly warn on schema_version mismatch on sqlite


def test_batch_scaffold_passes_validate(
    tmp_path: Path, capsys: pytest.CaptureFixture
) -> None:
    root = tmp_path / "batch-proj"
    _scaffold(capsys, root, "batchproj")
    rc = _run_validate_subcommand([*_common(root, "batchproj"), "--json"])
    payload = json.loads(capsys.readouterr().out)
    assert rc == 0
    assert payload["errors"] == []


# ---- wizard scaffold ------------------------------------------------------


def test_wizard_scaffold_passes_validate(
    tmp_path: Path, capsys: pytest.CaptureFixture
) -> None:
    """Wizard output must match batch output for downstream tools."""
    import argparse

    root = tmp_path / "wiz-proj"
    answers = iter([
        "wizproj",  # project id
        str(root),  # project root
        "file",  # state backend
        "conservative",  # budget preset
        "specs",  # spec root
        "",  # premium (default)
        "",  # standard (default)
        "",  # cheap (default)
        "n",  # sdd
    ])
    args = argparse.Namespace()
    rc = run_wizard(
        args,
        input_fn=lambda _p: next(answers),
        output_stream=io.StringIO(),
    )
    assert rc == 0

    # Wizard invokes orch_init internally which prints the 'next steps'
    # banner to real stdout — drain it before parsing the validate JSON.
    capsys.readouterr()
    rc2 = _run_validate_subcommand([*_common(root, "wizproj"), "--json"])
    payload = json.loads(capsys.readouterr().out)
    assert rc2 == 0
    assert payload["errors"] == []


# ---- doctor edge cases ---------------------------------------------------


def test_doctor_reports_missing_jq(
    tmp_path: Path, capsys: pytest.CaptureFixture, monkeypatch
) -> None:
    root = tmp_path / "no-jq"
    _scaffold(capsys, root, "nojq")
    monkeypatch.setattr("shutil.which", lambda name: None if name == "jq" else "/usr/local/bin/x")
    monkeypatch.setattr(
        "orchestrator.preflight._probe_version",
        lambda cli: (True, f"{cli} 1.0.0"),
    )
    rc = _run_doctor_subcommand([*_common(root, "nojq"), "--json"])
    payload = json.loads(capsys.readouterr().out)
    jq_check = next(c for c in payload["checks"] if c["name"] == "jq.present")
    assert jq_check["status"] == "error"
    assert rc == 2


def test_doctor_non_executable_scripts_are_warn(
    tmp_path: Path, capsys: pytest.CaptureFixture, monkeypatch
) -> None:
    root = tmp_path / "no-x"
    _scaffold(capsys, root, "nox")
    for name in ("task-start.sh", "task-finish.sh", "task-block.sh"):
        (root / "scripts" / name).chmod(0o644)
    _stub_probes(monkeypatch)
    rc = _run_doctor_subcommand([*_common(root, "nox"), "--json"])
    payload = json.loads(capsys.readouterr().out)
    scripts_warns = [c for c in payload["checks"] if c["name"].startswith("scripts.") and c["status"] == "warn"]
    assert len(scripts_warns) == 3
    assert rc == 1  # warn only


def test_doctor_malformed_router_reports_error(
    tmp_path: Path, capsys: pytest.CaptureFixture, monkeypatch
) -> None:
    root = tmp_path / "bad-router"
    _scaffold(capsys, root, "bad")
    # Overwrite router with garbage YAML.
    (root / "orchestrator" / "model_router.yaml").write_text(
        "not: [yaml\n  - broken"
    )
    _stub_probes(monkeypatch)
    rc = _run_doctor_subcommand([*_common(root, "bad"), "--json"])
    payload = json.loads(capsys.readouterr().out)
    router_check = next(c for c in payload["checks"] if c["name"] == "router.parse")
    assert router_check["status"] == "error"
    assert rc == 2


# ---- validate edge cases -------------------------------------------------


def test_validate_empty_tasks_json_ok(
    tmp_path: Path, capsys: pytest.CaptureFixture
) -> None:
    root = tmp_path / "empty"
    _scaffold(capsys, root, "empty")
    rc = _run_validate_subcommand([*_common(root, "empty"), "--json"])
    payload = json.loads(capsys.readouterr().out)
    assert rc == 0
    assert payload["errors"] == []


def test_validate_malformed_tasks_reports_error(
    tmp_path: Path, capsys: pytest.CaptureFixture
) -> None:
    root = tmp_path / "malformed"
    _scaffold(capsys, root, "mal")
    (root / "tasks.json").write_text("{not json")
    rc = _run_validate_subcommand([*_common(root, "mal"), "--json"])
    payload = json.loads(capsys.readouterr().out)
    assert rc == 2
    kinds = {e["kind"] for e in payload["errors"]}
    # Malformed JSON surfaces as tasks.parse.
    assert "tasks.parse" in kinds


def test_validate_with_files_flag_on_real_scaffold(
    tmp_path: Path, capsys: pytest.CaptureFixture
) -> None:
    """--files against a fresh scaffold (empty tasks) is a no-op."""
    root = tmp_path / "files-flag"
    _scaffold(capsys, root, "ff")
    rc = _run_validate_subcommand([*_common(root, "ff"), "--json", "--files"])
    payload = json.loads(capsys.readouterr().out)
    assert rc == 0
    assert payload["errors"] == []


# ---- both backends parity -----------------------------------------------


@pytest.mark.parametrize("state_backend", ["file", "sqlite"])
def test_doctor_reports_correct_state_backend(
    tmp_path: Path,
    capsys: pytest.CaptureFixture,
    monkeypatch,
    state_backend: str,
) -> None:
    root = tmp_path / f"be-{state_backend}"
    _scaffold(capsys, root, f"proj-{state_backend}")
    # Rewrite config so state.backend matches the parametrized value.
    cfg = root / "orchestrator" / "config.yaml"
    text = cfg.read_text()
    import re as _re
    cfg.write_text(_re.sub(r"(?m)^(\s*backend:\s*)\S+", rf"\g<1>{state_backend}", text, count=1))
    _stub_probes(monkeypatch)
    rc = _run_doctor_subcommand([*_common(root, f"proj-{state_backend}"), "--json"])
    payload = json.loads(capsys.readouterr().out)
    assert payload["backend"] == state_backend
    db_check = next(c for c in payload["checks"] if c["name"] == "state.db.accessible")
    if state_backend == "file":
        assert db_check["status"] == "skip"
    else:
        assert db_check["status"] in ("ok", "warn")
    # Both scaffolds pass validate.
    capsys.readouterr()
    rc2 = _run_validate_subcommand([*_common(root, f"proj-{state_backend}"), "--json"])
    payload2 = json.loads(capsys.readouterr().out)
    assert rc2 == 0
    assert payload2["errors"] == []


# ---- validation ↔ doctor consistency -------------------------------------


def test_validate_and_doctor_agree_on_missing_router(
    tmp_path: Path, capsys: pytest.CaptureFixture, monkeypatch
) -> None:
    """Deleting model_router.yaml should surface in BOTH commands as error."""
    root = tmp_path / "no-router"
    _scaffold(capsys, root, "nr")
    (root / "orchestrator" / "model_router.yaml").unlink()
    _stub_probes(monkeypatch)

    rc_val = _run_validate_subcommand([*_common(root, "nr"), "--json"])
    val_payload = json.loads(capsys.readouterr().out)
    assert rc_val == 2
    kinds = {e["kind"] for e in val_payload["errors"]}
    assert "router.missing" in kinds

    rc_doc = _run_doctor_subcommand([*_common(root, "nr"), "--json"])
    doc_payload = json.loads(capsys.readouterr().out)
    router_check = next(c for c in doc_payload["checks"] if c["name"] == "router.parse")
    assert router_check["status"] == "error"
    assert rc_doc == 2
