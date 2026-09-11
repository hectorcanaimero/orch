"""Local dashboard for the orchestrator — FastAPI serving the embedded React SPA.

Read-only surface over `tasks.json`, `events-*.jsonl` and `spend-*.jsonl`
inside a project's `orchestrator/state/` directory. This module never edits
tasks.json and never writes state files (hard rule from the MVP contract).

The heavy import (fastapi) lives inside `server.create_app()` so importing
`dashboard.metrics` / `dashboard.pricing` / `dashboard.log_stream` for unit
tests does NOT force the web stack to be installed.
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
