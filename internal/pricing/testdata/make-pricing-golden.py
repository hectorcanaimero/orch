#!/usr/bin/env python3
"""Capture the price table PYTHON actually loads, and what it resolves.

The Go tree embeds its own copy of `pricing.yaml`, and a copied data file is
a file that drifts. This golden is what the Python dashboard would charge, so
the Go test compares against the live table rather than against the file it
was copied from.

Run with a venv holding orch v0.11.0:

    /tmp/orch-py/bin/python internal/pricing/testdata/make-pricing-golden.py
"""
import json
from pathlib import Path

from orchestrator.dashboard.pricing import PricingTable

HERE = Path(__file__).resolve().parent
table = PricingTable.load()

# (recorded_cost, model, tokens_in, tokens_out) → what the dashboard charges.
RESOLUTIONS = [
    [0.0, "claude-sonnet-4-6", 1_000_000, 0],
    [0.5, "claude-sonnet-4-6", 1_000_000, 0],
    [-2.0, "claude-sonnet-4-6", 1_000_000, 0],
    [0.0, "claude-sonnet-4-6", 1_000_000, 1_000_000],
    [0.0, "a-model-nobody-priced", 1_000_000, 1_000_000],
    [0.0, "", 1_000_000, 0],
    [0.0, "claude-sonnet-4-6", -5_000_000, 1_000_000],
    [0.0, "gemini-3.7-flash-medium", 1_000_000, 1_000_000],
]

out = {
    "table": {
        model: {
            "input": table.for_model(model).input,
            "output": table.for_model(model).output,
        }
        for model in table.models()
    },
    "resolutions": [
        {
            "recorded_cost": rc, "model": model,
            "tokens_in": ti, "tokens_out": to,
            "cost_usd": table.resolve_cost(
                recorded_cost=rc, model=model or "default",
                tokens_in=ti, tokens_out=to,
            ),
        }
        for rc, model, ti, to in RESOLUTIONS
    ],
}

(HERE / "pricing.golden.json").write_text(
    json.dumps(out, indent=2, sort_keys=True) + "\n", encoding="utf-8"
)
print(f"{len(out['table'])} models, {len(out['resolutions'])} resolutions written")
