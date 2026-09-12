#!/usr/bin/env python3
"""Capture what PYTHON's dashboard metrics say about a set of task graphs.

The Go port in internal/graph/analytics.go has to agree with
`orchestrator/dashboard/metrics.py` on every one of these — including the
parts nobody would guess: which task wins a tie on the critical path, whether
a dependency on a task that does not exist counts as satisfied, and what a
cycle does to the whole computation.

Scenarios are defined here rather than as fixture files because each one is a
single interesting shape and reading it next to its answer is the point.
The parity project on disk is included as the eleventh, so the goldens also
cover a graph nobody tuned for this.

Run with a venv holding orch v0.11.0:

    /tmp/orch-py/bin/python internal/graph/testdata/make-analytics-golden.py
"""
import json
from pathlib import Path

from orchestrator.dashboard.metrics import (
    critical_path,
    downstream_impact,
    orphan_dependencies,
    parallelizable_tasks,
    phase_counts,
    project_summary,
)
from orchestrator.models import Task
from orchestrator.state import load_tasks

HERE = Path(__file__).resolve().parent
PROJECT = HERE / "parity-project"


def t(tid, *, phase=0, status="todo", deps=(), hours=0.0, title=""):
    return Task(
        id=tid,
        phase=phase,
        title=title or tid,
        description="",
        model="claude/claude-sonnet-4-6",
        reason="",
        status=status,
        dependencies=list(deps),
        estimate_hours=hours,
        files=[],
        spec_ref="",
        comments=[],
    )


SCENARIOS: dict[str, list] = {
    # Nothing at all. Every function has to survive it.
    "empty": [],

    # One task, no dependencies, no estimate: the fallback weight of 1.0 is
    # the only thing keeping the critical path non-empty.
    "single-unestimated": [t("A")],

    # A straight chain. The critical path is the whole thing.
    "chain": [
        t("A", hours=1.0, status="done"),
        t("B", deps=["A"], hours=2.0),
        t("C", deps=["B"], hours=3.0),
    ],

    # Two branches of EQUAL weight. Which one wins is decided by the order
    # ids enter `dist`, which is decided by the pop order of the ready
    # stack. This is the scenario that catches a port that iterates a map.
    "tie": [
        t("root", hours=1.0),
        t("left", deps=["root"], hours=2.0),
        t("right", deps=["root"], hours=2.0),
    ],

    # Same, with the branches declared in the other order.
    "tie-reversed": [
        t("root", hours=1.0),
        t("right", deps=["root"], hours=2.0),
        t("left", deps=["root"], hours=2.0),
    ],

    # A done task in the middle: downstream impact must traverse THROUGH it
    # and still not count it.
    "done-in-the-middle": [
        t("A", status="done"),
        t("B", deps=["A"], status="done"),
        t("C", deps=["B"]),
        t("D", deps=["C"]),
    ],

    # A dependency nobody defined. Not parallelizable, an orphan, and it
    # stalls the critical path's queue because indeg never reaches 0.
    "orphan-dep": [
        t("A", deps=["GHOST"]),
        t("B", hours=5.0),
    ],

    # A cycle. `critical_path` is documented as silent here — it returns
    # whatever it computed before the queue stalled.
    "cycle": [
        t("A", deps=["B"]),
        t("B", deps=["A"]),
        t("C", hours=9.0),
    ],

    # Statuses across the board, several phases, so summary and phase counts
    # have something to disagree about.
    "mixed-statuses": [
        t("p0-done", phase=0, status="done", hours=1.5),
        t("p0-blocked", phase=0, status="blocked", hours=2.0),
        t("p1-todo", phase=1, status="todo", deps=["p0-done"], hours=0.5),
        t("p1-wip", phase=1, status="in-progress", hours=3.0),
        t("p1-backlog", phase=1, status="backlog", deps=["p0-blocked"]),
        t("p5-todo", phase=5, status="todo"),
    ],

    # Ready to launch: deps done, status backlog or todo. Everything else is
    # a near miss on one of those two conditions.
    "parallelizable": [
        t("dep", status="done"),
        t("ready-todo", status="todo", deps=["dep"], phase=2),
        t("ready-backlog", status="backlog", deps=["dep"], phase=1),
        t("not-ready-dep-open", status="todo", deps=["ready-todo"]),
        t("not-ready-wip", status="in-progress", deps=["dep"]),
        t("not-ready-blocked", status="blocked", deps=["dep"]),
        t("not-ready-done", status="done", deps=["dep"]),
    ],
}

SCENARIOS["parity-project"] = load_tasks(PROJECT / "tasks.json")


def shape(tasks) -> dict:
    return {
        "summary": project_summary(tasks).as_dict(),
        "phase_counts": phase_counts(tasks),
        "parallelizable": [x.id for x in parallelizable_tasks(tasks)],
        "downstream_impact": downstream_impact(tasks),
        "critical_path": sorted(critical_path(tasks)),
        "orphans": orphan_dependencies(tasks),
    }


out = {name: shape(tasks) for name, tasks in SCENARIOS.items()}
(HERE / "analytics.golden.json").write_text(
    json.dumps(out, indent=2, sort_keys=True) + "\n", encoding="utf-8"
)
print(f"{len(out)} scenarios written")
