"""Unit tests for `orchestrator.spend_reader` (R-019).

Covers the read contract the dashboard depends on:
    - Normal read yields all parsed rows in order.
    - Missing file → empty iterator (no crash).
    - Malformed / partial-append lines are silently dropped.
    - `aggregate_by_provider` sums `cost_usd` per `backend`.

The writer side (`state.SpendLog`) is covered by `test_state.py`; here we
exercise the reader against fixture files it produces.
"""

from __future__ import annotations

import json
from datetime import date, datetime, timezone
from pathlib import Path

from orchestrator.models import SpendEntry
from orchestrator.spend_reader import (
    aggregate_by_provider,
    iter_today_entries,
    spend_path,
)
from orchestrator.state import SpendLog


def _today_utc_iso() -> str:
    return datetime.now(timezone.utc).date().isoformat()


def _make_entry(
    backend: str = "claude",
    cost: float = 0.01,
    task_id: str = "R-001",
) -> SpendEntry:
    now = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    return SpendEntry(
        ts=now,
        task_id=task_id,
        backend=backend,  # type: ignore[arg-type]
        model="claude-opus-4",
        tokens_in=100,
        tokens_out=50,
        cost_usd=cost,
        duration_s=5.0,
    )


def test_spend_path_defaults_to_today(tmp_path: Path) -> None:
    p = spend_path(tmp_path)
    assert p.parent == tmp_path
    assert p.name == f"spend-{_today_utc_iso()}.jsonl"


def test_spend_path_accepts_explicit_date(tmp_path: Path) -> None:
    p = spend_path(tmp_path, date(2026, 1, 15))
    assert p.name == "spend-2026-01-15.jsonl"


def test_iter_missing_file_yields_empty(tmp_path: Path) -> None:
    # No file exists at all.
    assert list(iter_today_entries(tmp_path)) == []


def test_iter_reads_all_rows_in_order(tmp_path: Path) -> None:
    log = SpendLog(tmp_path)
    for i in range(3):
        log.record(_make_entry(task_id=f"R-{i:03d}", cost=0.01 * (i + 1)))
    rows = list(iter_today_entries(tmp_path))
    assert len(rows) == 3
    assert [r["task_id"] for r in rows] == ["R-000", "R-001", "R-002"]
    assert all("cost_usd" in r for r in rows)


def test_iter_skips_malformed_lines(tmp_path: Path) -> None:
    log = SpendLog(tmp_path)
    log.record(_make_entry(task_id="R-good-1"))
    log.record(_make_entry(task_id="R-good-2"))
    # Simulate a partial/corrupt append.
    path = spend_path(tmp_path)
    with path.open("a", encoding="utf-8") as fh:
        fh.write("{not valid json\n")
        fh.write("\n")  # blank line
        fh.write("null\n")  # valid JSON but not a dict — should be dropped
    log.record(_make_entry(task_id="R-good-3"))

    rows = list(iter_today_entries(tmp_path))
    assert len(rows) == 3
    assert [r["task_id"] for r in rows] == ["R-good-1", "R-good-2", "R-good-3"]


def test_aggregate_by_provider_sums_costs() -> None:
    entries = [
        {"backend": "claude", "cost_usd": 0.10},
        {"backend": "claude", "cost_usd": 0.05},
        {"backend": "codex", "cost_usd": 0.20},
        {"backend": "opencode", "cost_usd": 0.01},
    ]
    agg = aggregate_by_provider(entries)
    assert set(agg.keys()) == {"claude", "codex", "opencode"}
    assert abs(agg["claude"] - 0.15) < 1e-9
    assert abs(agg["codex"] - 0.20) < 1e-9
    assert abs(agg["opencode"] - 0.01) < 1e-9


def test_aggregate_handles_missing_and_bad_cost() -> None:
    entries = [
        {"backend": "claude"},  # missing cost_usd
        {"backend": "claude", "cost_usd": "not-a-number"},
        {"backend": "claude", "cost_usd": 0.02},
        {"cost_usd": 0.03},  # missing backend → empty-string bucket
    ]
    agg = aggregate_by_provider(entries)
    assert agg["claude"] == 0.02
    assert agg[""] == 0.03


def test_end_to_end_reader_and_aggregate(tmp_path: Path) -> None:
    log = SpendLog(tmp_path)
    log.record(_make_entry(backend="claude", cost=0.01))
    log.record(_make_entry(backend="claude", cost=0.02))
    log.record(_make_entry(backend="codex", cost=0.05))
    rows = list(iter_today_entries(tmp_path))
    agg = aggregate_by_provider(rows)
    assert abs(agg["claude"] - 0.03) < 1e-9
    assert abs(agg["codex"] - 0.05) < 1e-9
