"""Read-only helpers over `state/spend-<YYYY-MM-DD>.jsonl` (R-019).

The dashboard fetches the same JSONL file directly via HTTP — this module
mirrors the read contract on the Python side so tests can verify the data the
dashboard will consume without spinning up a browser.

Design notes:
    - Iterates lazily so files that grow during a run can be re-read cheaply.
    - Silently drops malformed lines. `SpendLog.record()` flushes after every
      write, but a reader tailing the file concurrently may catch a partial
      last line during an append; better to skip than crash the dashboard.
    - Missing file → empty iterator (dashboard degrades to "no dispatches
      today yet" — NFR-OBS-2, C-5).
    - No dependency on any other orchestrator module — this is a leaf helper
      the tests and any external tooling can use.
"""

from __future__ import annotations

import json
from collections.abc import Iterable, Iterator
from datetime import date, datetime, timezone
from pathlib import Path


def _today_utc() -> str:
    """UTC calendar date, ISO format — matches `SpendLog._path_for` naming."""
    return datetime.now(timezone.utc).date().isoformat()


def spend_path(state_dir: Path, on_date: date | str | None = None) -> Path:
    """Return the JSONL path for a given UTC date (defaults to today)."""
    if on_date is None:
        stamp = _today_utc()
    elif isinstance(on_date, date):
        stamp = on_date.isoformat()
    else:
        stamp = str(on_date)
    return state_dir / f"spend-{stamp}.jsonl"


def iter_today_entries(state_dir: Path) -> Iterator[dict]:
    """Yield parsed dicts from today's spend log.

    Missing file yields nothing. Malformed JSON lines are silently skipped
    (partial appends, truncated lines, hand-edited noise).
    """
    path = spend_path(state_dir)
    if not path.exists():
        return
    with path.open("r", encoding="utf-8") as fh:
        for line in fh:
            line = line.strip()
            if not line:
                continue
            try:
                obj = json.loads(line)
            except (json.JSONDecodeError, ValueError):
                continue
            if isinstance(obj, dict):
                yield obj


def aggregate_by_provider(entries: Iterable[dict]) -> dict[str, float]:
    """Sum `cost_usd` by `backend` string.

    Missing/non-numeric costs are treated as 0.0; entries missing `backend` are
    grouped under the empty string so callers can spot data-quality problems
    without crashing.
    """
    totals: dict[str, float] = {}
    for entry in entries:
        backend = str(entry.get("backend", "") or "")
        cost = entry.get("cost_usd", 0.0)
        try:
            cost_f = float(cost)
        except (TypeError, ValueError):
            cost_f = 0.0
        totals[backend] = totals.get(backend, 0.0) + cost_f
    return totals
