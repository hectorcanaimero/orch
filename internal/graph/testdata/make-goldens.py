#!/usr/bin/env python3
"""Capture what the PYTHON validators say about parity-project.

The Go package has to produce the same kinds, the same messages and the same
cycle set. Run with a venv holding orch v0.11.0:

    /tmp/orch-py/bin/python internal/graph/testdata/make-goldens.py
"""
import json
from pathlib import Path

import yaml

from orchestrator.preflight import find_cycles, validate_graph
from orchestrator.state import load_tasks

HERE = Path(__file__).resolve().parent
PROJECT = HERE / "parity-project"

tasks = load_tasks(PROJECT / "tasks.json")
router = yaml.safe_load((PROJECT / "model_router.yaml").read_text(encoding="utf-8"))

errors = validate_graph(tasks, router_keys=list(router.keys()))
(HERE / "validate.golden.json").write_text(
    json.dumps([e.as_json() for e in errors], indent=2, sort_keys=True) + "\n",
    encoding="utf-8",
)

(HERE / "cycles.golden.json").write_text(
    json.dumps(find_cycles(tasks), indent=2) + "\n", encoding="utf-8"
)

# The display order `orch tasks` uses: stable sort by (phase, id). Not a
# topological order — Python has none.
ordered = sorted(tasks, key=lambda t: (t.phase, t.id))
(HERE / "display-order.golden.json").write_text(
    json.dumps([t.id for t in ordered], indent=2) + "\n", encoding="utf-8"
)

print(f"{len(errors)} validation errors, {len(find_cycles(tasks))} cycles, "
      f"{len(ordered)} tasks ordered")
