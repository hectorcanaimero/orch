"""Local dashboard for the orchestrator — FastAPI + HTMX + Alpine + Tailwind.

Read-only surface over `tasks.json`, `events-*.jsonl` and `spend-*.jsonl`
inside a project's `orchestrator/state/` directory. This module never edits
tasks.json and never writes state files (hard rule from the MVP contract).

The heavy imports (fastapi / jinja2) live inside `server.create_app()` so
importing `dashboard.metrics` / `dashboard.pricing` / `dashboard.log_stream`
for unit tests does NOT force the web stack to be installed. If the tests
that render templates need jinja2 they use `pytest.importorskip`.
"""

from __future__ import annotations

__all__ = ["create_app", "run"]


def create_app(*args, **kwargs):
    """Lazy re-export of `server.create_app` (avoids top-level fastapi import)."""
    from orchestrator.dashboard.server import create_app as _create_app

    return _create_app(*args, **kwargs)


def run(*args, **kwargs):
    """Lazy re-export of `server.run` (uvicorn foreground)."""
    from orchestrator.dashboard.server import run as _run

    return _run(*args, **kwargs)
