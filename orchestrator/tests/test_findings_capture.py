"""Tests for `orch findings capture` / `list` + core dedup helpers.

Sprint E-1 / #17 — commit 2 scope.

These exercise the pure-Python business logic (`orchestrator.findings`) plus
the two smallest CLI verbs. Publish + review flows have their own files.
"""

from __future__ import annotations

import json
import subprocess
from pathlib import Path

import pytest

from orchestrator import findings as f_mod
from orchestrator.findings import (
    DuplicateFindingError,
    FindingValidationError,
    _dedup_hash,
    _normalize_summary,
    _word_overlap_ratio,
    capture,
    list_findings,
)
from orchestrator.models import Finding
from orchestrator.state import FileBackend
from orchestrator.state.interface import StateBackend


# ---- pure helpers -------------------------------------------------------


def test_normalize_summary_lowers_and_collapses() -> None:
    assert _normalize_summary("Hello,  WORLD! ") == "hello world"
    assert _normalize_summary("A\tB   C") == "a b c"
    assert _normalize_summary("") == ""


def test_normalize_summary_strips_punctuation() -> None:
    assert _normalize_summary("path/to/file:123 — error!!!") == \
        "path to file 123 error"


def test_dedup_hash_is_stable_and_sensitive() -> None:
    a = _dedup_hash("bug", "orch", "Broken thing on init")
    b = _dedup_hash("bug", "orch", "broken thing on init!!!")
    c = _dedup_hash("bug", "orch", "different summary altogether")
    d = _dedup_hash("feature", "orch", "Broken thing on init")
    e = _dedup_hash("bug", "project", "Broken thing on init")
    assert a == b, "punctuation/case must not affect hash"
    assert a != c
    assert a != d, "type change must produce different hash"
    assert a != e, "about change must produce different hash"


def test_word_overlap_ratio_basic() -> None:
    assert _word_overlap_ratio("foo bar baz", "foo bar baz") == 1.0
    assert _word_overlap_ratio("", "foo bar") == 0.0
    # Stopwords are dropped: only "foo" overlaps.
    r = _word_overlap_ratio("the foo on init", "the foo when starting")
    assert 0.0 < r < 1.0
    assert _word_overlap_ratio("alpha", "beta") == 0.0


def test_word_overlap_ratio_symmetric() -> None:
    a = "orch prints stacktrace when starting"
    b = "stacktrace on startup with orch"
    assert _word_overlap_ratio(a, b) == _word_overlap_ratio(b, a)


# ---- capture ------------------------------------------------------------


@pytest.fixture
def backend(tmp_path: Path) -> StateBackend:
    state_dir = tmp_path / "state"
    state_dir.mkdir()
    return FileBackend(state_dir=state_dir, project_id="pt", project_root=tmp_path)


def test_capture_persists_and_computes_hash(backend: StateBackend) -> None:
    f = capture(
        backend,
        finding_type="bug",
        about="orch",
        summary="Race condition in dispatcher",
    )
    assert isinstance(f, Finding)
    assert f.status == "pending"
    assert f.dedup_hash == _dedup_hash("bug", "orch", "Race condition in dispatcher")
    stored = backend.get_finding(f.id)
    assert stored is not None
    assert stored.summary == "Race condition in dispatcher"


def test_capture_rejects_duplicate_hash(backend: StateBackend) -> None:
    capture(
        backend,
        finding_type="bug",
        about="orch",
        summary="the same problem exactly",
    )
    with pytest.raises(DuplicateFindingError) as exc:
        capture(
            backend,
            finding_type="bug",
            about="orch",
            # Different punctuation/case — normalization makes it a dup.
            summary="The SAME problem, exactly!",
        )
    assert exc.value.existing is not None
    assert exc.value.existing.summary == "the same problem exactly"


def test_capture_validates_type() -> None:
    be = FileBackend(state_dir=Path("/tmp/unused-x"), project_id="pt")
    with pytest.raises(FindingValidationError):
        capture(be, finding_type="glitch", about="orch", summary="ok")  # type: ignore[arg-type]


def test_capture_rejects_empty_summary(backend: StateBackend) -> None:
    with pytest.raises(FindingValidationError):
        capture(backend, finding_type="bug", about="orch", summary="   ")


def test_capture_rejects_newline_summary(backend: StateBackend) -> None:
    with pytest.raises(FindingValidationError):
        capture(
            backend,
            finding_type="bug",
            about="orch",
            summary="first line\nsecond line",
        )


def test_capture_defaults_project_id_from_backend(backend: StateBackend) -> None:
    f = capture(
        backend,
        finding_type="feature",
        about="orch",
        summary="Add color theme picker",
    )
    assert f.project_id == "pt"


def test_capture_strips_summary_whitespace(backend: StateBackend) -> None:
    f = capture(
        backend,
        finding_type="bug",
        about="orch",
        summary="   trimmed summary   ",
    )
    assert f.summary == "trimmed summary"


# ---- list ---------------------------------------------------------------


def test_list_findings_filters(backend: StateBackend) -> None:
    capture(backend, finding_type="bug", about="orch", summary="A one")
    capture(backend, finding_type="fix", about="project", summary="B two")
    capture(backend, finding_type="feature", about="orch", summary="C three")

    orch_only = list_findings(backend, about="orch")
    project_only = list_findings(backend, about="project")
    assert {f.summary for f in orch_only} == {"A one", "C three"}
    assert {f.summary for f in project_only} == {"B two"}

    all_pending = list_findings(backend, status="pending")
    assert len(all_pending) == 3


# ---- CLI --------------------------------------------------------------


def _run_cli(argv: list[str], tmp_path: Path) -> tuple[int, str, str]:
    """Invoke `orch findings ...` in-process, capturing stdout/stderr."""
    import io
    import sys

    from orchestrator.orch import main

    # Fresh state dir + minimal config so main() works via cwd-less flags.
    (tmp_path / "state").mkdir(exist_ok=True)
    stdout, stderr = sys.stdout, sys.stderr
    sio_out, sio_err = io.StringIO(), io.StringIO()
    sys.stdout, sys.stderr = sio_out, sio_err
    try:
        rc = main(["findings", *argv, "--project-root", str(tmp_path)])
    except SystemExit as exc:
        rc = int(exc.code) if isinstance(exc.code, int) else 2
    finally:
        sys.stdout, sys.stderr = stdout, stderr
    return rc, sio_out.getvalue(), sio_err.getvalue()


def _bootstrap_project(tmp_path: Path) -> None:
    """Minimal on-disk layout so _load_config + project resolution work."""
    (tmp_path / "orchestrator").mkdir(exist_ok=True)
    (tmp_path / "orchestrator" / "config.yaml").write_text(
        "concurrency: {global_max: 1}\n"
        "state: {backend: file}\n",
        encoding="utf-8",
    )
    (tmp_path / "tasks.json").write_text('{"tasks": []}', encoding="utf-8")
    (tmp_path / "scripts").mkdir(exist_ok=True)
    for name in ("task-start.sh", "task-finish.sh", "task-block.sh"):
        (tmp_path / "scripts" / name).write_text("#!/bin/sh\nexit 0\n")
        (tmp_path / "scripts" / name).chmod(0o755)


def test_cli_capture_happy_path(tmp_path: Path) -> None:
    _bootstrap_project(tmp_path)
    rc, out, err = _run_cli(
        [
            "capture",
            "--type", "bug",
            "--about", "orch",
            "--summary", "Dashboard crashes on refresh",
        ],
        tmp_path,
    )
    assert rc == 0, err
    assert "captured" in out


def test_cli_capture_duplicate_exit_2(tmp_path: Path) -> None:
    _bootstrap_project(tmp_path)
    _run_cli(
        ["capture", "--type", "bug", "--about", "orch",
         "--summary", "recurrent oopsie"],
        tmp_path,
    )
    rc, _out, err = _run_cli(
        ["capture", "--type", "bug", "--about", "orch",
         "--summary", "Recurrent OOPSIE!"],
        tmp_path,
    )
    assert rc == 2
    assert "duplicate" in err.lower()


def test_cli_capture_json_output(tmp_path: Path) -> None:
    _bootstrap_project(tmp_path)
    rc, out, err = _run_cli(
        [
            "capture",
            "--type", "feature",
            "--about", "orch",
            "--summary", "Add graphviz export",
            "--json",
        ],
        tmp_path,
    )
    assert rc == 0, err
    row = json.loads(out.strip())
    assert row["type"] == "feature"
    assert row["about"] == "orch"
    assert row["status"] == "pending"
    assert row["summary"] == "Add graphviz export"


def test_cli_list_empty(tmp_path: Path) -> None:
    _bootstrap_project(tmp_path)
    rc, out, _err = _run_cli(["list"], tmp_path)
    assert rc == 0
    assert "no findings" in out


def test_cli_list_filters(tmp_path: Path) -> None:
    _bootstrap_project(tmp_path)
    _run_cli(["capture", "--type", "bug", "--about", "orch",
              "--summary", "One"], tmp_path)
    _run_cli(["capture", "--type", "fix", "--about", "project",
              "--summary", "Two"], tmp_path)
    rc, out, _err = _run_cli(["list", "--about", "orch"], tmp_path)
    assert rc == 0
    assert "One" in out
    assert "Two" not in out


def test_cli_list_json(tmp_path: Path) -> None:
    _bootstrap_project(tmp_path)
    _run_cli(["capture", "--type", "bug", "--about", "orch",
              "--summary", "some bug"], tmp_path)
    rc, out, _err = _run_cli(["list", "--json"], tmp_path)
    assert rc == 0
    rows = json.loads(out.strip())
    assert len(rows) == 1
    assert rows[0]["summary"] == "some bug"


def test_cli_unknown_verb_exit_2(tmp_path: Path) -> None:
    _bootstrap_project(tmp_path)
    rc, _out, err = _run_cli(["frobulate"], tmp_path)
    assert rc == 2
    assert "unknown" in err.lower()
