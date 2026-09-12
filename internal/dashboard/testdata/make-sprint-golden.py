#!/usr/bin/env python3
"""Capture what PYTHON computes for sprint health and milestone ETAs.

The clock is frozen: `sprint_health` takes a `now` and `milestone_eta` takes a
`today`, both injected, so every date in this golden is reproducible.

Run with a venv holding orch v0.11.0:

    /tmp/orch-py/bin/python internal/dashboard/testdata/make-sprint-golden.py
"""
import json
from datetime import datetime, timezone
from pathlib import Path

from orchestrator.dashboard.metrics import milestone_eta, sprint_health
from orchestrator.models import Task

HERE = Path(__file__).resolve().parent
NOW = datetime(2026, 9, 1, 12, 0, 0, tzinfo=timezone.utc)
TODAY = "2026-09-01"


def t(tid, *, phase=0, status="todo", hours=0.0, title=""):
    return Task(
        id=tid, phase=phase, title=title, description="", model="m", reason="",
        status=status, dependencies=[], estimate_hours=hours, files=[],
        spec_ref="", comments=[],
    )


def ev(event_type, ts, **extra):
    return {"event_type": event_type, "ts": ts, "extra": extra}


SPRINT_SCENARIOS: dict[str, dict] = {
    "empty": {"tasks": [], "done_7d": 0, "last_events": {}},

    # Nothing finished yet: velocity is zero, so there is NO projection —
    # null, not "today".
    "no-velocity": {
        "tasks": [t("A", hours=2.0), t("B", hours=3.0)],
        "done_7d": 0,
        "last_events": {},
    },

    # Everything done: nothing remaining, so again no projection.
    "nothing-remaining": {
        "tasks": [t("A", status="done"), t("B", status="done")],
        "done_7d": 2,
        "last_events": {},
    },

    # 7 done in 7 days is 1/day; 4 remaining is 4 days out.
    "steady-pace": {
        "tasks": [
            t("A", status="done"), t("B", status="done"),
            t("C", hours=1.0), t("D", hours=2.0), t("E", hours=3.0), t("F", hours=4.0),
        ],
        "done_7d": 7,
        "last_events": {},
    },

    # One done in 7 days, 40 remaining: 280 days out, which is beyond the
    # 30-day horizon, so confidence drops to "low".
    "slow-pace-low-confidence": {
        "tasks": [t("D", status="done")] + [t(f"T{i}", hours=1.0) for i in range(40)],
        "done_7d": 1,
        "last_events": {},
    },

    # Blocked tasks are NOT counted as remaining work — they are their own
    # figure — and each one explains itself from its last event.
    "blockers": {
        "tasks": [
            t("A", status="done"),
            t("B", status="blocked", phase=2, hours=5.0, title="Payments"),
            t("C", status="blocked", phase=1, hours=1.0),
            t("D", hours=2.0),
        ],
        "done_7d": 7,
        "last_events": {
            "B": ev("block", "2026-08-30T09:00:00Z", reason="waiting on the bank"),
            # No `reason` in extra: the event TYPE becomes the reason.
            "C": ev("fail", "2026-08-31T09:00:00Z"),
        },
    },

    # A blocked task with no event at all still appears, with "unknown".
    "blocker-with-no-event": {
        "tasks": [t("A", status="blocked", hours=1.0)],
        "done_7d": 0,
        "last_events": {},
    },

    # A reason longer than 300 characters is cut. The panel is a list, not a
    # log, and a pasted stack trace would take the page over.
    "long-reason": {
        "tasks": [t("A", status="blocked")],
        "done_7d": 0,
        "last_events": {"A": ev("block", "2026-08-30T09:00:00Z", reason="x" * 500)},
    },
}

MILESTONE_CASES = [
    # remaining, velocity, target_date
    [0, 1.0, None],
    [5, 0.0, None],
    [5, 1.0, None],
    # 1.2 days of work is not finishing today: the ceiling makes it 2.
    [6, 5.0, None],
    [5, 1.0, "2026-09-10"],
    [5, 1.0, "2026-09-02"],
    [100, 1.0, None],
    [100, 1.0, "2027-01-01"],
    [5, 1.0, "not-a-date"],
]

out = {
    "sprint": {
        name: sprint_health(
            s["tasks"], s["done_7d"], s["last_events"], now=NOW,
        )
        for name, s in SPRINT_SCENARIOS.items()
    },
    "milestone_eta": [
        {
            "remaining": remaining, "velocity_per_day": velocity,
            "target_date": target, "today": TODAY,
            "eta": milestone_eta(remaining, velocity, TODAY, target),
        }
        for remaining, velocity, target in MILESTONE_CASES
    ],
}

(HERE / "sprint.golden.json").write_text(
    json.dumps(out, indent=2, sort_keys=True) + "\n", encoding="utf-8"
)
print(f"{len(SPRINT_SCENARIOS)} sprint scenarios, {len(MILESTONE_CASES)} ETAs written")
