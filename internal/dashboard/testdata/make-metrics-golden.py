#!/usr/bin/env python3
"""Capture what PYTHON makes of a set of spend rows.

The interesting part is not the arithmetic, it is the fallbacks: a recorded
cost of zero is an ESTIMATE from the price table rather than "free", a model
nobody priced falls through to `default`, and `tasks_total` counts distinct
task ids rather than rows.

Run with a venv holding orch v0.11.0:

    /tmp/orch-py/bin/python internal/dashboard/testdata/make-metrics-golden.py
"""
import json
from pathlib import Path

from orchestrator.dashboard.metrics import metrics_by_day, metrics_by_model, total_cost
from orchestrator.dashboard.pricing import PricingTable

HERE = Path(__file__).resolve().parent


def sp(model, ti, to, cost, ts="2026-09-01T10:00:00Z", task="T-1"):
    return {
        "model": model, "tokens_in": ti, "tokens_out": to,
        "cost_usd": cost, "ts": ts, "task_id": task, "backend": "claude",
    }


SCENARIOS: dict[str, list[dict]] = {
    "empty": [],

    # A real billed row: the recorded cost wins and the table is not consulted.
    "recorded-cost-wins": [sp("claude-sonnet-4-6", 1000, 500, 0.99)],

    # cost_usd == 0 is "nobody billed", not "free": estimated from the table.
    # claude-sonnet-4-6 is 3.00 / 15.00 per 1M.
    "zero-cost-is-estimated": [sp("claude-sonnet-4-6", 1_000_000, 1_000_000, 0.0)],

    # A model with no row falls through to `default` (1.00 / 4.00).
    "unpriced-model": [sp("a-model-nobody-priced", 1_000_000, 1_000_000, 0.0)],

    # An empty model name. Note metrics_by_model groups it under "unknown"
    # while total_cost prices it as "default" — both land on the same row
    # unless a project defines a model called "unknown".
    "empty-model-name": [sp("", 1_000_000, 0, 0.0)],

    # Three rows, one task, one model: tasks_total counts the TASK once.
    "retries-count-one-task": [
        sp("gpt-5", 100, 100, 0.01, task="T-1"),
        sp("gpt-5", 100, 100, 0.01, task="T-1"),
        sp("gpt-5", 100, 100, 0.01, task="T-2"),
    ],

    # Several models, so the sort (cost descending, then name) has work to do.
    "sorted-by-cost": [
        sp("claude-haiku-4-5", 1_000_000, 0, 0.0, task="T-1"),
        sp("claude-opus-4-7", 1_000_000, 0, 0.0, task="T-2"),
        sp("gpt-5", 1_000_000, 0, 0.0, task="T-3"),
    ],

    # Two models that cost exactly the same: the tie breaks on name.
    "tie-breaks-on-name": [
        sp("zeta", 0, 0, 0.0, task="T-1"),
        sp("alpha", 0, 0, 0.0, task="T-2"),
    ],

    # Days: newest first, no zero-fill for the gap.
    "days-newest-first": [
        sp("gpt-5", 10, 10, 0.05, ts="2026-09-01T10:00:00Z", task="T-1"),
        sp("gpt-5", 10, 10, 0.05, ts="2026-09-03T10:00:00Z", task="T-2"),
        sp("gpt-5", 10, 10, 0.05, ts="2026-09-03T23:59:59Z", task="T-3"),
    ],

    # A timestamp too short to hold a date is skipped by the day table and
    # still counted by the model table and the total.
    "undated-row": [
        sp("gpt-5", 10, 10, 0.25, ts="2026-09"),
        sp("gpt-5", 10, 10, 0.25, ts="2026-09-01T10:00:00Z"),
    ],

    # Negative tokens are read as zero rather than as a refund.
    "negative-tokens": [sp("claude-sonnet-4-6", -5_000_000, 1_000_000, 0.0)],
}

pricing = PricingTable.load()
out = {
    name: {
        "total_cost_usd": total_cost(rows, pricing),
        "by_model": [m.as_dict() for m in metrics_by_model(rows, pricing)],
        "by_day": [d.as_dict() for d in metrics_by_day(rows, pricing, days=14)],
    }
    for name, rows in SCENARIOS.items()
}

# The price table itself is NOT captured here — it has its own golden next to
# the package that embeds it (internal/pricing/testdata).

(HERE / "metrics.golden.json").write_text(
    json.dumps(out, indent=2, sort_keys=True) + "\n", encoding="utf-8"
)
print(f"{len(SCENARIOS)} scenarios written")
