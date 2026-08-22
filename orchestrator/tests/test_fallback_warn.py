"""Sprint C: dedup the router fallback WARN + verbose / quiet / env log level.

Covers:
    - `_warn_fallback_routes` non-verbose default: one INFO summary line,
      no double-emit to stderr.
    - `_warn_fallback_routes` verbose: one INFO line per route.
    - `_resolve_log_level` precedence: --quiet > --verbose > env > INFO.
"""

from __future__ import annotations

import logging
from typing import Any

import pytest

from orchestrator.models import RouteEntry
from orchestrator.orch import _resolve_log_level, _warn_fallback_routes


def _route(cli: str, fallback: str | None) -> RouteEntry:
    return RouteEntry(
        backend="claude",
        cli_model=cli,
        tier="standard",
        is_premium=False,
        fallback_cli_model=fallback,
    )


def _router(entries: dict[str, RouteEntry]) -> dict[str, RouteEntry]:
    return entries


def test_fallback_warn_emits_once_by_default(
    caplog: pytest.LogCaptureFixture,
) -> None:
    router = _router({
        "opencode/x": _route("x-model", "x-model-fallback"),
        "opencode/y": _route("y-model", "y-model-fallback"),
        "opencode/z": _route("z-model", None),  # no fallback
    })
    with caplog.at_level(logging.INFO, logger="orchestrator.orch"):
        _warn_fallback_routes(router)
    # In the non-verbose path we emit ONE summary line, no per-route detail.
    summary_rows = [
        rec for rec in caplog.records
        if "route(s) with fallback configured" in rec.getMessage()
    ]
    assert len(summary_rows) == 1
    assert "2 route(s)" in summary_rows[0].getMessage()
    # And no per-route detail line leaked into the non-verbose path.
    detail_rows = [
        rec for rec in caplog.records
        if "fallback_cli_model=" in rec.getMessage()
    ]
    assert detail_rows == []


def test_fallback_warn_verbose_expands_per_route(
    caplog: pytest.LogCaptureFixture,
) -> None:
    router = _router({
        "opencode/x": _route("x-model", "x-model-fallback"),
        "opencode/y": _route("y-model", "y-model-fallback"),
    })
    with caplog.at_level(logging.INFO, logger="orchestrator.orch"):
        _warn_fallback_routes(router, verbose=True)
    per_route = [
        rec for rec in caplog.records
        if "fallback_cli_model" in rec.getMessage()
    ]
    assert len(per_route) == 2
    # Non-verbose summary line MUST NOT appear when we're in verbose mode.
    summary_rows = [
        rec for rec in caplog.records
        if "route(s) with fallback configured" in rec.getMessage()
    ]
    assert summary_rows == []


def test_fallback_warn_zero_routes_is_silent(
    caplog: pytest.LogCaptureFixture
) -> None:
    with caplog.at_level(logging.INFO, logger="orchestrator.orch"):
        _warn_fallback_routes({})
    assert not any(
        "fallback" in rec.getMessage() for rec in caplog.records
    )


# ---- _resolve_log_level -------------------------------------------------


def test_resolve_log_level_quiet_wins_over_verbose_and_env() -> None:
    assert _resolve_log_level(verbose=2, quiet=True, env_var="DEBUG") == "ERROR"


def test_resolve_log_level_verbose_beats_env() -> None:
    assert _resolve_log_level(verbose=1, env_var="WARNING") == "DEBUG"


def test_resolve_log_level_env_wins_when_no_flags() -> None:
    assert _resolve_log_level(env_var="WARNING") == "WARNING"


def test_resolve_log_level_default_is_info() -> None:
    assert _resolve_log_level() == "INFO"
