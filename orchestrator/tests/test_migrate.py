"""Tests for `orch migrate` (Sprint B — commit 5).

Covers:
    - Round-trip: file state → sqlite backend contains identical rows.
    - Idempotency: running migrate twice produces stable row counts.
    - Backup: `state/backups/<ts>/` created before writes.
    - Dry-run: no rows written; DB unchanged.
    - Rollback: restore backup + drop tenant rows.
    - Malformed JSONL: skipped with a warning, not fatal.
"""

from __future__ import annotations

import json
import sqlite3
from pathlib import Path

import pytest

from orchestrator.migrate import run_migrate
from orchestrator.state import _reset_backend_cache
from orchestrator.state.sqlite_backend import _reset_schema_cache_for_tests


FIXTURE_TASKS = Path(__file__).parent / "fixtures" / "tiny_tasks.json"


@pytest.fixture(autouse=True)
def _clear_caches():
    _reset_backend_cache()
    _reset_schema_cache_for_tests()
    yield
    _reset_backend_cache()
    _reset_schema_cache_for_tests()


@pytest.fixture
def project_with_state(tmp_path: Path) -> Path:
    """Build a project with tasks.json, scripts/, and populated state/.

    state/ contains:
        - run-<uuid>.json with one in-flight dispatch
        - events-<uuid>.jsonl with 3 rows
        - spend-2026-08-19.jsonl with 2 rows
    """
    root = tmp_path / "proj"
    root.mkdir()
    (root / "tasks.json").write_bytes(FIXTURE_TASKS.read_bytes())
    scripts = root / "scripts"
    scripts.mkdir()
    for name in ("task-start.sh", "task-finish.sh", "task-block.sh"):
        (scripts / name).write_text("#!/usr/bin/env bash\nexit 0\n")
        (scripts / name).chmod(0o755)
    cfg_dir = root / "orchestrator"
    cfg_dir.mkdir()
    (cfg_dir / "config.yaml").write_text("state:\n  backend: file\n")

    state = root / "orchestrator" / "state"
    state.mkdir(parents=True)
    run_id = "abc-123"
    run_json = {
        "run_id": run_id,
        "started_at": "2026-08-19T12:00:00Z",
        "mode": "auto",
        "in_flight": {
            "T-A": {
                "task_id": "T-A",
                "backend": "opencode",
                "pid": 1234,
                "session_id": "s-T-A",
                "started_at": "2026-08-19T12:00:00Z",
                "prompt_path": "state/prompts/T-A.txt",
                "log_path": "state/logs/T-A.log",
                "output_path": "state/logs/T-A.out",
                "attempt": 1,
            }
        },
        "completed": ["T-Z"],
        "blocked": [],
        "deferred": [],
    }
    (state / f"run-{run_id}.json").write_text(json.dumps(run_json))
    events = [
        {"event_type": "dispatch", "task_id": "T-A", "backend": "opencode",
         "ts": "2026-08-19T12:00:00Z", "extra": {"pid": 1234}},
        {"event_type": "success", "task_id": "T-Z", "backend": "claude",
         "ts": "2026-08-19T12:00:10Z", "extra": {}},
        {"event_type": "fail", "task_id": "T-C", "backend": "codex",
         "ts": "2026-08-19T12:00:20Z", "extra": {"reason": "boom"}},
    ]
    events_path = state / f"events-{run_id}.jsonl"
    events_path.write_text("\n".join(json.dumps(e) for e in events) + "\n")

    spend = [
        {"ts": "2026-08-19T12:00:05Z", "task_id": "T-A", "backend": "claude",
         "model": "opus", "tokens_in": 100, "tokens_out": 50,
         "cost_usd": 0.03, "duration_s": 1.2},
        {"ts": "2026-08-19T12:00:15Z", "task_id": "T-Z", "backend": "codex",
         "model": "gpt-5", "tokens_in": 200, "tokens_out": 100,
         "cost_usd": 0.05, "duration_s": 2.0},
    ]
    spend_path = state / "spend-2026-08-19.jsonl"
    spend_path.write_text("\n".join(json.dumps(s) for s in spend) + "\n")

    return root


# ---- Round-trip --------------------------------------------------------


def test_migrate_round_trip(project_with_state: Path) -> None:
    """Every row from state/ lands in the sqlite DB."""
    rc = run_migrate([
        "--project-root", str(project_with_state),
        "--project-id", "p-round-trip",
        "--config", "orchestrator/config.yaml",
    ])
    assert rc == 0
    db = project_with_state / "orchestrator" / "state" / "orch.db"
    conn = sqlite3.connect(str(db))
    try:
        n_runs = conn.execute("SELECT COUNT(*) FROM runs").fetchone()[0]
        n_disp = conn.execute("SELECT COUNT(*) FROM dispatches").fetchone()[0]
        n_ev = conn.execute("SELECT COUNT(*) FROM events").fetchone()[0]
        n_sp = conn.execute("SELECT COUNT(*) FROM spend").fetchone()[0]
        n_tr = conn.execute("SELECT COUNT(*) FROM tasks_runtime").fetchone()[0]
        migrated = conn.execute(
            "SELECT migrated_at FROM projects WHERE project_id='p-round-trip'"
        ).fetchone()[0]
    finally:
        conn.close()
    assert n_runs == 1
    assert n_disp == 1  # T-A was in_flight
    assert n_ev == 3
    assert n_sp == 2
    assert n_tr == 5  # tiny_tasks.json has 5 tasks
    assert migrated is not None


def test_migrate_idempotent_second_run_no_new_rows(project_with_state: Path) -> None:
    """Running migrate twice produces stable counts (dedup_hash works)."""
    rc1 = run_migrate([
        "--project-root", str(project_with_state),
        "--project-id", "p-idem",
        "--config", "orchestrator/config.yaml",
    ])
    assert rc1 == 0
    # Second run requires --force because migrated_at guard trips.
    rc2 = run_migrate([
        "--project-root", str(project_with_state),
        "--project-id", "p-idem",
        "--config", "orchestrator/config.yaml",
        "--force",
    ])
    assert rc2 == 0
    db = project_with_state / "orchestrator" / "state" / "orch.db"
    conn = sqlite3.connect(str(db))
    try:
        counts = {
            "runs": conn.execute("SELECT COUNT(*) FROM runs").fetchone()[0],
            "events": conn.execute("SELECT COUNT(*) FROM events").fetchone()[0],
            "spend": conn.execute("SELECT COUNT(*) FROM spend").fetchone()[0],
        }
    finally:
        conn.close()
    assert counts == {"runs": 1, "events": 3, "spend": 2}


def test_migrate_second_run_without_force_refuses(project_with_state: Path) -> None:
    """migrated_at guard: second migrate without --force exits 1."""
    rc1 = run_migrate([
        "--project-root", str(project_with_state),
        "--project-id", "p-guard",
        "--config", "orchestrator/config.yaml",
    ])
    assert rc1 == 0
    rc2 = run_migrate([
        "--project-root", str(project_with_state),
        "--project-id", "p-guard",
        "--config", "orchestrator/config.yaml",
    ])
    assert rc2 == 1


# ---- Backup ------------------------------------------------------------


def test_migrate_creates_backup(project_with_state: Path) -> None:
    rc = run_migrate([
        "--project-root", str(project_with_state),
        "--project-id", "p-backup",
        "--config", "orchestrator/config.yaml",
    ])
    assert rc == 0
    backup_root = project_with_state / "orchestrator" / "state-backups"
    assert backup_root.exists()
    backups = list(backup_root.iterdir())
    assert len(backups) == 1
    # Backup contains the JSONL files but not the DB itself.
    entries = {p.name for p in backups[0].iterdir()}
    assert any(name.startswith("run-") for name in entries)
    assert any(name.startswith("events-") for name in entries)
    assert any(name.startswith("spend-") for name in entries)


# ---- Dry-run -----------------------------------------------------------


def test_migrate_dry_run_writes_nothing(project_with_state: Path) -> None:
    rc = run_migrate([
        "--project-root", str(project_with_state),
        "--project-id", "p-dry",
        "--config", "orchestrator/config.yaml",
        "--dry-run",
    ])
    assert rc == 0
    # Dry-run must not create backups.
    backup_root = project_with_state / "orchestrator" / "state-backups"
    assert not backup_root.exists()
    # The DB DID get created (SqliteBackend ctor runs migrations), but
    # projects table has no row for p-dry.
    db = project_with_state / "orchestrator" / "state" / "orch.db"
    if db.exists():
        conn = sqlite3.connect(str(db))
        try:
            row = conn.execute(
                "SELECT COUNT(*) FROM projects WHERE project_id='p-dry'"
            ).fetchone()
        finally:
            conn.close()
        assert row[0] == 0
        # No events either.
        conn = sqlite3.connect(str(db))
        try:
            n = conn.execute("SELECT COUNT(*) FROM events").fetchone()[0]
        finally:
            conn.close()
        assert n == 0


# ---- Rollback ----------------------------------------------------------


def test_migrate_rollback_drops_tenant_and_restores(project_with_state: Path) -> None:
    rc = run_migrate([
        "--project-root", str(project_with_state),
        "--project-id", "p-rb",
        "--config", "orchestrator/config.yaml",
    ])
    assert rc == 0

    # Delete a JSONL file to prove restore actually happens.
    state_dir = project_with_state / "orchestrator" / "state"
    spend_path = state_dir / "spend-2026-08-19.jsonl"
    assert spend_path.exists()
    spend_path.unlink()
    assert not spend_path.exists()

    rc_rb = run_migrate([
        "--project-root", str(project_with_state),
        "--project-id", "p-rb",
        "--config", "orchestrator/config.yaml",
        "--rollback",
    ])
    assert rc_rb == 0
    # File must be restored.
    assert spend_path.exists()
    # DB rows for the tenant must be gone.
    db = state_dir / "orch.db"
    conn = sqlite3.connect(str(db))
    try:
        n_proj = conn.execute(
            "SELECT COUNT(*) FROM projects WHERE project_id='p-rb'"
        ).fetchone()[0]
        n_ev = conn.execute(
            "SELECT COUNT(*) FROM events WHERE project_id='p-rb'"
        ).fetchone()[0]
    finally:
        conn.close()
    assert n_proj == 0
    assert n_ev == 0


# ---- Malformed input ---------------------------------------------------


def test_migrate_skips_malformed_jsonl_lines(project_with_state: Path) -> None:
    """One garbage line in events.jsonl doesn't kill the migration."""
    state_dir = project_with_state / "orchestrator" / "state"
    events_files = list(state_dir.glob("events-*.jsonl"))
    assert events_files
    with events_files[0].open("a", encoding="utf-8") as fh:
        fh.write("{not valid json\n")
        fh.write("\n")
        fh.write("null\n")

    rc = run_migrate([
        "--project-root", str(project_with_state),
        "--project-id", "p-bad",
        "--config", "orchestrator/config.yaml",
    ])
    assert rc == 0
    db = project_with_state / "orchestrator" / "state" / "orch.db"
    conn = sqlite3.connect(str(db))
    try:
        n = conn.execute("SELECT COUNT(*) FROM events").fetchone()[0]
    finally:
        conn.close()
    # Only the 3 valid rows landed.
    assert n == 3
