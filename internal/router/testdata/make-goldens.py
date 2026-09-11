#!/usr/bin/env python3
"""Dump what the PYTHON router loads, as the golden for internal/router.

Two inputs: the router orch ships, and the parity project's. Run with the
v0.11.0 venv.
"""
import json
from dataclasses import asdict
from pathlib import Path

from orchestrator.router import load_router

HERE = Path(__file__).resolve().parent
REPO = HERE.parents[2]

CASES = {
    "packaged": REPO / "orchestrator" / "model_router.yaml",
    "parity": REPO / "internal" / "graph" / "testdata" / "parity-project" / "model_router.yaml",
}

for name, path in CASES.items():
    router = load_router(path)
    dumped = {k: asdict(v) for k, v in sorted(router.items())}
    (HERE / f"{name}.golden.json").write_text(
        json.dumps(dumped, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(f"{name}: {len(router)} routes")
