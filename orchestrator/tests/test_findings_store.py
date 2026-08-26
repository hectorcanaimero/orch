"""Backend-specific tests for the findings store (Sprint E-1 / #17).

Parity tests live in `test_backend_parity.py`. This file focuses on:

- Migration versioning (sqlite bumps to `PRAGMA user_version = 3`).
- File-backend on-disk layout (`findings.jsonl`).
- Multitenant isolation (two `project_id`s share one DB but never see each
  other's rows).
"""

from __future__ import annotations

import json
import sqlite3
from pathlib import Path

import pytest

from orchestrator.models import Finding
from orchestrator.state import FileBackend, _reset_backend_cache
from orchestrator.state.sqlite_backend import (
    SqliteBackend,
    _reset_schema_cache_for_tests,
)


def _finding(fid: str, *, dedup: str, project_id: str = "") -> Finding:
    return Finding(
        id=fid,
        created_at="2026-08-21T00:00:00Z",
        type="bug",
        about="orch",
        summary=f"Finding {fid}",
        evidence="",
        confidence="medium",
        dedup_hash=dedup,
        project_id=project_id,
    )


# ---- sqlite migration ---------------------------------------------------


def test_sqlite_migration_bumps_user_version_to_3(tmp_path: Path) -> None:
    _reset_schema_cache_for_tests()
    _reset_backend_cache()
    db = tmp_path / "state" / "orch.db"
    backend = SqliteBackend(db_path=db, project_id="pytest")
    assert backend.schema_version() >= 3
    # findings table is queryable — smoke-check the schema is applied.
    conn = sqlite3.connect(str(db))
    try:
        cur = conn.execute(
            "SELECT name FROM sqlite_master WHERE type = 'table' "
            "AND name = 'findings'"
        )
        assert cur.fetchone() is not None
    finally:
        conn.close()


def test_sqlite_migration_is_reentrant(tmp_path: Path) -> None:
    """Opening the same DB twice must not re-apply migrations."""
    _reset_schema_cache_for_tests()
    _reset_backend_cache()
    db = tmp_path / "state" / "orch.db"
    SqliteBackend(db_path=db, project_id="pytest")
    _reset_schema_cache_for_tests()  # force re-check without altering DB
    b2 = SqliteBackend(db_path=db, project_id="pytest")
    assert b2.schema_version() >= 3


# ---- file backend on-disk layout ---------------------------------------


def test_file_backend_creates_findings_jsonl(tmp_path: Path) -> None:
    state_dir = tmp_path / "state"
    state_dir.mkdir()
    be = FileBackend(state_dir=state_dir, project_id="p1", project_root=tmp_path)
    be.append_finding(_finding("f-1", dedup="h1", project_id="p1"))
    findings_file = state_dir / "findings.jsonl"
    assert findings_file.exists()
    line = findings_file.read_text(encoding="utf-8").strip().splitlines()[0]
    row = json.loads(line)
    assert row["id"] == "f-1"
    assert row["dedup_hash"] == "h1"
    assert row["status"] == "pending"


def test_file_backend_update_rewrites_file(tmp_path: Path) -> None:
    state_dir = tmp_path / "state"
    state_dir.mkdir()
    be = FileBackend(state_dir=state_dir, project_id="p1", project_root=tmp_path)
    be.append_finding(_finding("f-1", dedup="h1", project_id="p1"))
    be.append_finding(_finding("f-2", dedup="h2", project_id="p1"))
    be.update_finding("f-1", status="published", published_url="https://x/1")
    # Both rows are present; f-1 is mutated in place.
    rows = [
        json.loads(ln)
        for ln in (state_dir / "findings.jsonl")
        .read_text(encoding="utf-8")
        .splitlines()
        if ln.strip()
    ]
    assert len(rows) == 2
    by_id = {r["id"]: r for r in rows}
    assert by_id["f-1"]["status"] == "published"
    assert by_id["f-1"]["published_url"] == "https://x/1"
    assert by_id["f-2"]["status"] == "pending"


def test_file_backend_iter_skips_blank_and_malformed_lines(tmp_path: Path) -> None:
    state_dir = tmp_path / "state"
    state_dir.mkdir()
    be = FileBackend(state_dir=state_dir, project_id="p1", project_root=tmp_path)
    be.append_finding(_finding("f-1", dedup="h1", project_id="p1"))
    with (state_dir / "findings.jsonl").open("a", encoding="utf-8") as fh:
        fh.write("\n")
        fh.write("{not valid json}\n")
        fh.write("42\n")  # not a dict
    rows = list(be.iter_findings())
    assert [r.id for r in rows] == ["f-1"]


# ---- multitenant isolation ---------------------------------------------


def test_sqlite_multitenant_scoping(tmp_path: Path) -> None:
    _reset_schema_cache_for_tests()
    _reset_backend_cache()
    db = tmp_path / "state" / "orch.db"
    a = SqliteBackend(db_path=db, project_id="proj-a")
    b = SqliteBackend(db_path=db, project_id="proj-b")
    a.append_finding(_finding("f-A", dedup="ha", project_id="proj-a"))
    b.append_finding(_finding("f-B", dedup="hb", project_id="proj-b"))
    a_ids = {f.id for f in a.iter_findings()}
    b_ids = {f.id for f in b.iter_findings()}
    assert a_ids == {"f-A"}
    assert b_ids == {"f-B"}
    # Cross-project dedup lookup must not leak.
    assert a.find_finding_by_dedup_hash("hb") is None
    assert b.find_finding_by_dedup_hash("ha") is None
    assert b.get_finding("f-A") is None
