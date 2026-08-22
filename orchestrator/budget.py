"""Budget guardrails for orch (Sprint 7).

Prevents burning through provider subscription quotas during long unattended
runs. Reads token usage from `state/spend-*.jsonl` in a rolling window per
provider, blocks dispatches when usage crosses the configured threshold.

Design:
    - Config lives in `budgets.yaml` (presets: aggressive/conservative/shared).
    - `BudgetGate.can_dispatch(provider)` is the hot-path check the main loop
      calls before acquiring the per-provider semaphore. Cheap: reads at most
      today's and yesterday's spend files, filters by ts and backend, sums.
    - `all_capped()` + `earliest_reset()` let the main loop sleep-until-reset
      when every provider is throttled instead of busy-looping.
    - `snapshot()` produces the dashboard payload.
    - Passing `config=None` disables the gate entirely (backwards-compat: any
      run without a `budgets.yaml` or with the gate turned off keeps working
      exactly like pre-Sprint 7).

Not in scope:
    - Per-task or per-model budgets (already covered by `budget.per_dispatch_usd`
      in config.yaml).
    - Rewriting spend history when the wall clock jumps.
    - Cross-machine coordination.
"""

from __future__ import annotations

import json
import logging
from dataclasses import dataclass, field
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any


log = logging.getLogger(__name__)


# ---- Config -------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class ProviderBudget:
    """One provider's window budget."""

    window_hours: float
    token_budget: int
    threshold_pct: float


@dataclass(frozen=True, slots=True)
class BudgetConfig:
    """Full budget config — one entry per configured provider."""

    providers: dict[str, ProviderBudget] = field(default_factory=dict)


def load_budget_config(path: Path, preset: str) -> BudgetConfig | None:
    """Load a `budgets.yaml` preset.

    Returns `None` when the file doesn't exist (gate stays disabled).
    Raises `ValueError` when the file exists but the preset is missing —
    that's a config typo, not a graceful degradation case.
    """
    if not path.exists():
        return None
    import yaml  # local import: yaml is a heavy import for a hot-path module

    with path.open("r", encoding="utf-8") as fh:
        data = yaml.safe_load(fh) or {}
    presets = data.get("presets") or {}
    if preset not in presets:
        available = ", ".join(sorted(presets)) or "<none>"
        raise ValueError(
            f"budgets preset {preset!r} not found in {path}. Available: {available}"
        )
    raw_providers = presets[preset] or {}
    providers: dict[str, ProviderBudget] = {}
    for name, spec in raw_providers.items():
        providers[name] = ProviderBudget(
            window_hours=float(spec["window_hours"]),
            token_budget=int(spec["token_budget"]),
            threshold_pct=float(spec["threshold_pct"]),
        )
    return BudgetConfig(providers=providers)


# ---- Formatting helpers -------------------------------------------------


def _format_tokens_short(n: int | float) -> str:
    """Format an integer token count as `k` / `m` / `b` for log lines.

    Examples:
        400        -> "400"
        1_500      -> "1.5k"
        400_000    -> "400k"
        2_000_000  -> "2.0m"
    """
    try:
        n = float(n)
    except (TypeError, ValueError):
        return "0"
    if n < 1_000:
        return f"{int(n)}"
    if n < 1_000_000:
        val = n / 1_000
        # No decimal when it's a clean whole number of k (e.g. 400k not 400.0k).
        if val == int(val):
            return f"{int(val)}k"
        return f"{val:.1f}k"
    if n < 1_000_000_000:
        val = n / 1_000_000
        if val == int(val):
            return f"{int(val)}m"
        return f"{val:.1f}m"
    val = n / 1_000_000_000
    if val == int(val):
        return f"{int(val)}b"
    return f"{val:.1f}b"


def _format_reset_eta(reset_at: datetime | None, now: datetime | None = None) -> str:
    """Human ETA string for a reset timestamp: `2h 14m` / `45m` / `now`.

    Never raises; returns `"?"` on obviously bad input so the log line still
    prints something useful.
    """
    if reset_at is None:
        return "?"
    if now is None:
        now = datetime.now(timezone.utc)
    delta_s = int((reset_at - now).total_seconds())
    if delta_s <= 0:
        return "now"
    hours, rem = divmod(delta_s, 3600)
    minutes = rem // 60
    if hours > 0:
        return f"{hours}h {minutes}m"
    if minutes > 0:
        return f"{minutes}m"
    return f"{delta_s}s"


# ---- Preset sanity ------------------------------------------------------


def warn_undersized_presets(
    config: BudgetConfig | None,
    preset_name: str,
    typical_dispatch_tokens: int,
) -> list[str]:
    """Emit one WARN per provider whose window can barely fit two dispatches.

    Called once at startup from `orch.py`. Returns the list of warnings
    emitted (for tests + so the caller can also print to stderr if desired).

    A provider whose `token_budget < 2 * typical_dispatch_tokens` will
    serialize dispatches: the second attempt sits in the budget queue until
    the first one falls out of the rolling window. Almost always a
    misconfiguration — either the preset numbers are stale or the
    `typical_dispatch_tokens` estimate needs raising in `config.yaml`.
    """
    warnings_emitted: list[str] = []
    if config is None or not config.providers:
        return warnings_emitted
    if typical_dispatch_tokens <= 0:
        return warnings_emitted
    two_x = 2 * typical_dispatch_tokens
    for provider, pb in config.providers.items():
        if pb.token_budget < two_x:
            msg = (
                f"WARN: budget preset {preset_name!r} provider {provider!r} "
                f"window ({_format_tokens_short(pb.token_budget)}) is smaller "
                f"than 2x typical dispatch ({_format_tokens_short(typical_dispatch_tokens)}) "
                f"— dispatches will serialize"
            )
            log.warning(msg)
            warnings_emitted.append(msg)
    return warnings_emitted


# ---- Gate ---------------------------------------------------------------


class BudgetGate:
    """Rolling-window budget check per provider.

    Instantiated once at run start and shared across the main loop. Reads
    spend files each `can_dispatch` call — cheap because the tail of the day
    is small (a few KB) and we open at most 2 files (today + yesterday) to
    cover windows that straddle UTC midnight.

    Thread-safety: the read is stateless (no cached counters), so concurrent
    calls from the reap loop and refill loop are safe. The write side
    (`SpendLog.record`) already flushes after every append.
    """

    def __init__(self, state_dir: Path, config: BudgetConfig | None):
        self.state_dir = state_dir
        self.config = config
        self.disabled = config is None or not config.providers

    # ---- Hot path -------------------------------------------------------

    def can_dispatch(
        self, provider: str
    ) -> tuple[bool, str | None, datetime | None]:
        """Return `(ok, reason, reset_at)` for a provider.

        `ok=True` → free to dispatch.
        `ok=False` → the caller should skip this provider this tick;
        `reason` is a human string for events, `reset_at` is the UTC datetime
        when the oldest in-window entry falls out (i.e. when we'll be under
        threshold again — best effort).
        """
        if self.disabled or self.config is None:
            return True, None, None
        pb = self.config.providers.get(provider)
        if pb is None:
            # Unknown provider (nothing configured) → don't gate it.
            return True, None, None

        now = datetime.now(timezone.utc)
        cutoff = now - timedelta(hours=pb.window_hours)
        entries = list(self._entries_since(provider, cutoff))
        tokens_used = sum(
            int(e.get("tokens_in", 0) or 0) + int(e.get("tokens_out", 0) or 0)
            for e in entries
        )
        cap = pb.token_budget * (pb.threshold_pct / 100.0)
        if tokens_used < cap:
            return True, None, None

        # Over threshold — estimate reset as (oldest ts in window) + window_hours.
        # That's when the oldest entry falls out and (assuming no new writes)
        # usage drops. Not exact when many entries are near-simultaneous, but
        # good enough for the sleep hint.
        oldest_ts = min(
            (self._parse_ts(e["ts"]) for e in entries if "ts" in e),
            default=now,
        )
        reset_at = oldest_ts + timedelta(hours=pb.window_hours)
        reason = (
            f"{provider} over threshold: {tokens_used:,} tokens used, "
            f"cap {int(cap):,} ({pb.threshold_pct:.0f}% of {pb.token_budget:,})"
        )
        return False, reason, reset_at

    def all_capped(self) -> bool:
        """True when every configured provider is over threshold right now."""
        if self.disabled or self.config is None:
            return False
        if not self.config.providers:
            return False
        for provider in self.config.providers:
            ok, _, _ = self.can_dispatch(provider)
            if ok:
                return False
        return True

    def earliest_reset(self) -> datetime | None:
        """Soonest reset across all capped providers, or None if none capped."""
        if self.disabled or self.config is None:
            return None
        soonest: datetime | None = None
        for provider in self.config.providers:
            ok, _, reset_at = self.can_dispatch(provider)
            if not ok and reset_at is not None:
                if soonest is None or reset_at < soonest:
                    soonest = reset_at
        return soonest

    # ---- Dashboard payload ---------------------------------------------

    def snapshot(self) -> dict[str, dict[str, Any]]:
        """Per-provider dict for the `/budgets` endpoint.

        Empty dict when disabled — the dashboard hides the section.
        """
        if self.disabled or self.config is None:
            return {}
        now = datetime.now(timezone.utc)
        out: dict[str, dict[str, Any]] = {}
        for provider, pb in self.config.providers.items():
            cutoff = now - timedelta(hours=pb.window_hours)
            entries = list(self._entries_since(provider, cutoff))
            tokens_used = sum(
                int(e.get("tokens_in", 0) or 0) + int(e.get("tokens_out", 0) or 0)
                for e in entries
            )
            cap = pb.token_budget * (pb.threshold_pct / 100.0)
            capped = tokens_used >= cap
            reset_at: str | None = None
            if capped and entries:
                oldest_ts = min(
                    (self._parse_ts(e["ts"]) for e in entries if "ts" in e),
                    default=now,
                )
                reset_at = (
                    oldest_ts + timedelta(hours=pb.window_hours)
                ).strftime("%Y-%m-%dT%H:%M:%SZ")
            usage_pct = (
                (tokens_used / pb.token_budget * 100.0)
                if pb.token_budget > 0
                else 0.0
            )
            out[provider] = {
                "tokens_used": tokens_used,
                "token_budget": pb.token_budget,
                "usage_pct": usage_pct,
                "threshold_pct": pb.threshold_pct,
                "window_hours": pb.window_hours,
                "capped": capped,
                "reset_at": reset_at,
            }
        return out

    # ---- Internals ------------------------------------------------------

    def _entries_since(self, provider: str, cutoff: datetime):
        """Yield spend rows for a provider newer than `cutoff`.

        Reads today's file + yesterday's (if the window crosses midnight).
        Two-file bound is enough for the largest sensible window we support
        (~24h — anything bigger belongs in a different tool).
        """
        today = datetime.now(timezone.utc).date()
        days_to_check = [today]
        if cutoff.date() < today:
            days_to_check.insert(0, cutoff.date())
        for day in days_to_check:
            path = self.state_dir / f"spend-{day.isoformat()}.jsonl"
            if not path.exists():
                continue
            with path.open("r", encoding="utf-8") as fh:
                for line in fh:
                    line = line.strip()
                    if not line:
                        continue
                    try:
                        obj = json.loads(line)
                    except (json.JSONDecodeError, ValueError):
                        continue
                    if not isinstance(obj, dict):
                        continue
                    if obj.get("backend") != provider:
                        continue
                    ts_str = obj.get("ts")
                    if not ts_str:
                        continue
                    try:
                        ts = self._parse_ts(ts_str)
                    except ValueError:
                        continue
                    if ts >= cutoff:
                        yield obj

    @staticmethod
    def _parse_ts(ts: str) -> datetime:
        """Parse `2026-08-21T14:30:00Z` (spec.md canonical) into aware UTC."""
        # Accept both `Z` suffix and `+00:00` for robustness with older rows.
        if ts.endswith("Z"):
            ts = ts[:-1] + "+00:00"
        return datetime.fromisoformat(ts)
