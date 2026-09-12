#!/usr/bin/env python3
"""Capture what PYTHON derives from an event log, per task.

`human_hours_by_task` has more edge cases than its five lines suggest: a
recorded duration beats the wall clock, a retry stacks, an unpaired terminal
event contributes nothing, and a task that was never dispatched is ABSENT
rather than zero. Each scenario below is one of them.

Run with a venv holding orch v0.11.0:

    /tmp/orch-py/bin/python internal/project/testdata/make-events-golden.py
"""
import json
from pathlib import Path

from orchestrator.dashboard.metrics import human_hours_by_task, last_updated_by_task

HERE = Path(__file__).resolve().parent


def ev(task_id, event_type, ts, **extra):
    return {
        "task_id": task_id,
        "event_type": event_type,
        "ts": ts,
        "backend": "claude",
        "extra": extra,
    }


SCENARIOS: dict[str, list[dict]] = {
    "empty": [],

    # One clean interval: 90 minutes of wall clock, no recorded duration.
    "wall-clock": [
        ev("A", "dispatch", "2026-09-01T10:00:00Z"),
        ev("A", "success", "2026-09-01T11:30:00Z"),
    ],

    # duration_s wins over the wall clock, even when the wall clock is far
    # larger — the gap is queue time, which is not work.
    "duration-beats-wall-clock": [
        ev("A", "dispatch", "2026-09-01T10:00:00Z"),
        ev("A", "success", "2026-09-01T12:00:00Z", duration_s=600),
    ],

    # Three attempts. They add up.
    "retries-stack": [
        ev("A", "dispatch", "2026-09-01T10:00:00Z"),
        ev("A", "fail", "2026-09-01T10:30:00Z"),
        ev("A", "dispatch", "2026-09-01T11:00:00Z"),
        ev("A", "timeout", "2026-09-01T11:15:00Z"),
        ev("A", "dispatch", "2026-09-01T12:00:00Z"),
        ev("A", "success", "2026-09-01T12:45:00Z"),
    ],

    # A terminal event with no dispatch before it contributes nothing, and a
    # dispatch with no terminal after it never closes.
    "unpaired": [
        ev("A", "success", "2026-09-01T10:00:00Z"),
        ev("B", "dispatch", "2026-09-01T10:00:00Z"),
    ],

    # Events arriving out of order are sorted by ts first, so the pairing is
    # the same as if they had arrived in order.
    "out-of-order": [
        ev("A", "success", "2026-09-01T11:00:00Z"),
        ev("A", "dispatch", "2026-09-01T10:00:00Z"),
    ],

    # duration_s of 0 falls back to the wall clock; a non-numeric one does too.
    "bad-durations": [
        ev("A", "dispatch", "2026-09-01T10:00:00Z"),
        ev("A", "success", "2026-09-01T10:30:00Z", duration_s=0),
        ev("B", "dispatch", "2026-09-01T10:00:00Z"),
        ev("B", "success", "2026-09-01T10:30:00Z", duration_s="not a number"),
        ev("C", "dispatch", "2026-09-01T10:00:00Z"),
        ev("C", "success", "2026-09-01T10:30:00Z", duration_s="1800"),
    ],

    # A terminal event BEFORE its dispatch (clock skew) produces a negative
    # delta, which is dropped rather than subtracted.
    "negative-delta": [
        ev("A", "dispatch", "2026-09-01T11:00:00Z"),
        ev("A", "fail", "2026-09-01T11:00:00Z"),
    ],

    # Project-wide events carry "-" as the task id and must not become a task.
    "project-wide-events": [
        ev("-", "run_start", "2026-09-01T09:00:00Z"),
        ev("-", "run_end", "2026-09-01T13:00:00Z"),
        ev("A", "dispatch", "2026-09-01T10:00:00Z"),
        ev("A", "success", "2026-09-01T10:06:00Z"),
    ],

    # Several tasks at once, so last_updated has to keep them apart.
    "several-tasks": [
        ev("A", "dispatch", "2026-09-01T10:00:00Z"),
        ev("B", "dispatch", "2026-09-01T10:05:00Z"),
        ev("A", "success", "2026-09-01T10:10:00Z", duration_s=600),
        ev("B", "fail", "2026-09-01T10:20:00Z", duration_s=900),
        ev("C", "block", "2026-09-01T10:30:00Z"),
    ],

    # A duration that does not divide into whole milliseconds, to pin the
    # rounding: 1234.5678s / 3600 = 0.342935... → round(…, 3).
    "rounding": [
        ev("A", "dispatch", "2026-09-01T10:00:00Z"),
        ev("A", "success", "2026-09-01T10:30:00Z", duration_s=1234.5678),
    ],
}

out = {
    name: {
        "human_hours": human_hours_by_task(evs),
        "last_updated": last_updated_by_task(evs),
    }
    for name, evs in SCENARIOS.items()
}
(HERE / "events.golden.json").write_text(
    json.dumps(out, indent=2, sort_keys=True) + "\n", encoding="utf-8"
)
print(f"{len(out)} scenarios written")
