"""Integration + edge-case coverage for the dogfooding loop (Sprint E-1).

- Cross-backend end-to-end: capture -> review -> publish -> list.
- Edge cases: unicode summaries, over-long summaries (title truncation),
  whitespace-only evidence, rate-limit boundary (exactly at limit vs one over).
"""

from __future__ import annotations

import io
import json
import subprocess
import sys
from datetime import datetime, timedelta, timezone
from pathlib import Path
from unittest.mock import patch

import pytest

from orchestrator import findings as f_mod
from orchestrator.findings import (
    _truncate_title,
    capture,
    dismiss,
    publish,
)
from orchestrator.state import FileBackend
from orchestrator.state.interface import StateBackend
from orchestrator.state.sqlite_backend import (
    SqliteBackend,
    _reset_schema_cache_for_tests,
)


# ---- fixtures / helpers ------------------------------------------------


def _mk_completed(stdout: str = "", stderr: str = "", rc: int = 0) -> subprocess.CompletedProcess:
    return subprocess.CompletedProcess(
        args=["gh"], returncode=rc, stdout=stdout, stderr=stderr
    )


def _fake_runner(create_url: str = "https://github.com/o/r/issues/999"):
    def runner(cmd, **_kwargs):
        if cmd[:3] == ["gh", "label", "create"]:
            return _mk_completed()
        if cmd[:2] == ["gh", "api"]:
            path = cmd[2] if len(cmd) > 2 else ""
            if path.startswith("search/issues"):
                return _mk_completed(stdout=json.dumps({"items": []}))
            return _mk_completed(stdout=json.dumps({
                "html_url": create_url, "number": 999
            }))
        return _mk_completed()
    return runner


@pytest.fixture(params=["file", "sqlite"], ids=lambda k: f"backend={k}")
def any_backend(request, tmp_path: Path) -> StateBackend:
    _reset_schema_cache_for_tests()
    kind = request.param
    state_dir = tmp_path / "state"
    state_dir.mkdir()
    if kind == "file":
        return FileBackend(state_dir=state_dir, project_id="pt",
                           project_root=tmp_path)
    return SqliteBackend(
        db_path=state_dir / "orch.db",
        project_id="pt",
        project_root=tmp_path,
    )


# ---- cross-backend integration -----------------------------------------


def test_end_to_end_flow(any_backend: StateBackend) -> None:
    """capture -> review-shaped search -> publish -> stored+published."""
    f = capture(
        any_backend,
        finding_type="bug",
        about="orch",
        summary="Race condition when two dispatchers race on state save",
        evidence="orchestrator/state/file_backend.py:200\nsymptom: half-written run file",
        confidence="high",
    )

    # Simulate an operator running review: search returns nothing new.
    matches = f_mod.search_github_issues_for_duplicate(
        f.summary,
        "o/r",
        runner=lambda cmd, **_: _mk_completed(stdout=json.dumps({"items": []})),
    )
    assert matches == []

    # Publish flow with fully mocked gh.
    result = publish(
        any_backend, f.id, repo="o/r",
        runner=_fake_runner(create_url="https://github.com/o/r/issues/777"),
        confirm=lambda _f: True,
    )
    assert result["status"] == "published"
    assert result["published_url"].endswith("/777")

    # Backend now reflects the published state.
    stored = any_backend.get_finding(f.id)
    assert stored is not None
    assert stored.status == "published"
    assert stored.published_url == "https://github.com/o/r/issues/777"

    # Re-publish is idempotent (no gh calls).
    calls: list[list[str]] = []

    def blocking_runner(cmd, **_kwargs):
        calls.append(cmd)
        raise AssertionError(
            "gh must not be invoked on idempotent republish"
        )

    result2 = publish(
        any_backend, f.id, repo="o/r",
        runner=blocking_runner,
        confirm=lambda _f: True,
    )
    assert result2["status"] == "already_published"
    assert calls == []

    # Dismiss changes state (not that you'd normally dismiss a published one,
    # but the flow shouldn't error).
    dismiss(any_backend, f.id, "resolved via other means")
    reloaded = any_backend.get_finding(f.id)
    assert reloaded is not None
    assert reloaded.status == "dismissed"
    assert reloaded.dismissed_reason == "resolved via other means"


# ---- edge cases: summary content ---------------------------------------


def test_unicode_summary_normalizes(any_backend: StateBackend) -> None:
    f1 = capture(
        any_backend,
        finding_type="bug",
        about="orch",
        summary="Emoji causa un fallo raro cuando 🚀 aparece en logs",
    )
    # Dedup normalization keeps the unicode word, so a rephrase changes hash.
    f2 = capture(
        any_backend,
        finding_type="bug",
        about="orch",
        summary="Otro caso distinto de un problema con acentos: canción rota",
    )
    assert f1.dedup_hash != f2.dedup_hash


def test_very_long_summary_title_gets_truncated() -> None:
    long = "x" * 500
    title = _truncate_title(long)
    assert len(title) <= 120
    assert title.endswith("…")


def test_short_summary_title_unchanged() -> None:
    assert _truncate_title("short line") == "short line"


def test_whitespace_only_evidence_stored_verbatim(any_backend: StateBackend) -> None:
    f = capture(
        any_backend,
        finding_type="fix",
        about="orch",
        summary="Whitespace evidence acceptable normally",
        evidence="   \n\t\n",
    )
    stored = any_backend.get_finding(f.id)
    assert stored is not None
    assert stored.evidence == "   \n\t\n"


def test_capture_over_long_summary_persists(any_backend: StateBackend) -> None:
    """Long summaries are allowed for capture; only publish TRUNCATES the title."""
    long = "S" * 400 + " end"
    f = capture(
        any_backend,
        finding_type="feature",
        about="orch",
        summary=long,
    )
    stored = any_backend.get_finding(f.id)
    assert stored is not None
    assert stored.summary == long


def test_capture_missing_evidence_becomes_empty_string(any_backend: StateBackend) -> None:
    f = capture(
        any_backend,
        finding_type="bug",
        about="orch",
        summary="No evidence supplied here at all yes",
    )
    stored = any_backend.get_finding(f.id)
    assert stored is not None
    assert stored.evidence == ""


# ---- rate-limit boundary -----------------------------------------------


def _shift_created_at(backend: StateBackend, finding_id: str, when: datetime) -> None:
    """Test-only: reach into the backend to reset a finding's created_at."""
    ts = when.strftime("%Y-%m-%dT%H:%M:%SZ")
    if isinstance(backend, FileBackend):
        p = backend.state_dir / "findings.jsonl"
        rows = [json.loads(ln) for ln in p.read_text().splitlines() if ln.strip()]
        for r in rows:
            if r["id"] == finding_id:
                r["created_at"] = ts
        p.write_text("\n".join(json.dumps(r) for r in rows) + "\n")
    else:
        import sqlite3

        conn = sqlite3.connect(str(backend.db_path))  # type: ignore[attr-defined]
        try:
            conn.execute(
                "UPDATE findings SET created_at = ? WHERE id = ?",
                (ts, finding_id),
            )
            conn.commit()
        finally:
            conn.close()


def test_rate_limit_boundary_exactly_at_limit_trips(any_backend: StateBackend) -> None:
    """3 published in-window with limit=3 → next publish must trip."""
    now = datetime.now(timezone.utc)
    for i in range(3):
        f = capture(any_backend, finding_type="bug", about="orch",
                    summary=f"in-window seed number {i}")
        any_backend.update_finding(f.id, status="published",
                                   published_url=f"https://x/{i}")
        _shift_created_at(any_backend, f.id, now - timedelta(minutes=10))
    fresh = capture(
        any_backend, finding_type="bug", about="orch",
        summary="fresh candidate at the boundary",
    )
    with pytest.raises(f_mod.RateLimitExceeded):
        publish(
            any_backend, fresh.id, repo="o/r",
            rate_limit_per_hour=3,
            runner=_fake_runner(),
            confirm=lambda _f: True,
        )


def test_rate_limit_one_publish_below_limit_ok(any_backend: StateBackend) -> None:
    """2 published in-window with limit=3 → next publish must succeed."""
    now = datetime.now(timezone.utc)
    for i in range(2):
        f = capture(any_backend, finding_type="bug", about="orch",
                    summary=f"under-limit seed number {i}")
        any_backend.update_finding(f.id, status="published",
                                   published_url=f"https://x/{i}")
        _shift_created_at(any_backend, f.id, now - timedelta(minutes=10))
    fresh = capture(
        any_backend, finding_type="bug", about="orch",
        summary="publish should succeed within the window",
    )
    result = publish(
        any_backend, fresh.id, repo="o/r",
        rate_limit_per_hour=3,
        runner=_fake_runner(),
        confirm=lambda _f: True,
    )
    assert result["status"] == "published"


def test_rate_limit_stale_publishes_do_not_count(any_backend: StateBackend) -> None:
    """Publishes older than 1h must drop out of the counting window."""
    old = datetime.now(timezone.utc) - timedelta(hours=3)
    for i in range(5):
        f = capture(any_backend, finding_type="bug", about="orch",
                    summary=f"ancient seed for eviction {i}")
        any_backend.update_finding(f.id, status="published",
                                   published_url=f"https://x/{i}")
        _shift_created_at(any_backend, f.id, old)
    fresh = capture(
        any_backend, finding_type="bug", about="orch",
        summary="new work after the window rolled over",
    )
    result = publish(
        any_backend, fresh.id, repo="o/r",
        rate_limit_per_hour=1,
        runner=_fake_runner(),
        confirm=lambda _f: True,
    )
    assert result["status"] == "published"


# ---- dedup threshold boundary -----------------------------------------


def test_dedup_below_threshold_allows_publish(any_backend: StateBackend) -> None:
    f = capture(any_backend, finding_type="bug", about="orch",
                summary="Distinct summary about a rare dispatcher deadlock")
    search = _mk_completed(stdout=json.dumps({"items": [{
        "number": 12,
        "title": "Something completely unrelated to the query",
        "html_url": "https://github.com/o/r/issues/12",
    }]}))

    def runner(cmd, **_kwargs):
        if cmd[:3] == ["gh", "label", "create"]:
            return _mk_completed()
        if cmd[:2] == ["gh", "api"] and "search/issues" in cmd[2]:
            return search
        return _mk_completed(stdout=json.dumps({
            "html_url": "https://github.com/o/r/issues/900", "number": 900
        }))

    result = publish(
        any_backend, f.id, repo="o/r",
        runner=runner,
        confirm=lambda _f: True,
    )
    assert result["status"] == "published"


# ---- CLI-level integration --------------------------------------------


def _bootstrap_project(tmp_path: Path, backend_kind: str = "file") -> None:
    (tmp_path / ".orchestrator").mkdir(exist_ok=True)
    (tmp_path / ".orchestrator" / "config.yaml").write_text(
        f"concurrency: {{global_max: 1}}\n"
        f"state: {{backend: {backend_kind}}}\n"
        f"findings: {{publish_repo: 'o/r', publish_rate_limit_per_hour: 3}}\n",
        encoding="utf-8",
    )
    (tmp_path / "tasks.json").write_text('{"tasks": []}', encoding="utf-8")
    (tmp_path / "scripts").mkdir(exist_ok=True)
    for name in ("task-start.sh", "task-finish.sh", "task-block.sh"):
        (tmp_path / "scripts" / name).write_text("#!/bin/sh\nexit 0\n")
        (tmp_path / "scripts" / name).chmod(0o755)


def _run_cli(argv: list[str], tmp_path: Path) -> tuple[int, str, str]:
    from orchestrator.orch import main
    from orchestrator.state import _reset_backend_cache

    _reset_backend_cache()
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


@pytest.mark.parametrize("backend_kind", ["file", "sqlite"])
def test_cli_end_to_end_across_backends(tmp_path: Path, backend_kind: str) -> None:
    _reset_schema_cache_for_tests()
    _bootstrap_project(tmp_path, backend_kind=backend_kind)
    rc, _out, err = _run_cli(
        [
            "capture", "--type", "bug", "--about", "orch",
            "--summary", "CLI end to end publishable subject",
            "--confidence", "high",
        ],
        tmp_path,
    )
    assert rc == 0, err
    _, out, _ = _run_cli(["list", "--json"], tmp_path)
    fid = json.loads(out.strip())[0]["id"]

    with patch("orchestrator.findings.subprocess.run",
               side_effect=_fake_runner("https://github.com/o/r/issues/2020")):
        rc, out, err = _run_cli(["publish", fid, "--yes"], tmp_path)
    assert rc == 0, err
    assert "published" in out
    _, out, _ = _run_cli(["list", "--json"], tmp_path)
    row = json.loads(out.strip())[0]
    assert row["status"] == "published"
    assert row["published_url"].endswith("/2020")
