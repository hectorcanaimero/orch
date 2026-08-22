"""Dashboard UX + profile config loader.

Loads `dashboard.yaml` — first from the project root (operator override),
then falls back to the packaged default shipped alongside `server.py`.

Also merges the top-level `dashboard:` block from `config.yaml` (profile,
token, host, port, stakeholder route allow-list). CLI flags override the
config; the config overrides the packaged defaults.

Sprint E-2 introduces the `profile` knob:
    - "operator"    (default) — existing behavior, all routes, no auth.
    - "stakeholder" — read-only curated view, token required.
    - "both"        — routes under `/stakeholder/*` follow the stakeholder
                      profile (auth + curated); everything else stays
                      operator-mode (no auth, full data).

Design mirrors `PricingTable.load()` — no side effects, tolerant of a
missing project file, tolerant of a malformed YAML (falls back to defaults
with a warning printed to stderr).
"""

from __future__ import annotations

import os
import sys
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

import yaml


# ---- Profile constants -----------------------------------------------------

PROFILE_OPERATOR = "operator"
PROFILE_STAKEHOLDER = "stakeholder"
PROFILE_BOTH = "both"
VALID_PROFILES = frozenset({PROFILE_OPERATOR, PROFILE_STAKEHOLDER, PROFILE_BOTH})

# Route path prefix in `both` mode. Requests under this prefix are gated as
# stakeholder; everything else stays operator (no auth). We use a prefix
# match to keep the middleware trivially fast and safe (no regex surprises).
STAKEHOLDER_PATH_PREFIX = "/stakeholder"

# The stakeholder allow-list is an INCLUSIVE list of route names / path
# prefixes that the curated view is allowed to render. Every other route
# (operator-only mutations, raw logs, per-model spend, etc.) returns 403.
#
# Names are matched against the route's `name` attribute when we register
# it (in `server.py`). Paths listed with a `/` prefix use path-prefix match
# for static files, snapshots and stakeholder-only JSON endpoints. Both
# forms are permitted so we can express "any /static/* file" and "the
# named `stakeholder_index` route" in one list.
DEFAULT_STAKEHOLDER_ROUTES: tuple[str, ...] = (
    "stakeholder_index",
    "stakeholder_summary_json",
    "/static/",
)


# ---- Dataclasses -----------------------------------------------------------


@dataclass(frozen=True, slots=True)
class KanbanDefaults:
    wip_default: int | None = None
    sort_default: str | None = None
    group_default: str | None = None
    refresh_interval_s: int = 10


@dataclass(frozen=True, slots=True)
class DashboardConfig:
    """Full dashboard config surface — UX defaults + profile/auth.

    Every field has a safe default so existing operator-only setups don't
    need to touch config.yaml. Backwards-compatible: `profile` defaults to
    `operator`, `token` defaults to `None` (auth off), routes default to
    the built-in allow-list.
    """

    kanban: KanbanDefaults
    profile: str = PROFILE_OPERATOR
    token: str | None = None
    stakeholder_routes: tuple[str, ...] = DEFAULT_STAKEHOLDER_ROUTES

    @classmethod
    def load(
        cls,
        project_root: Path | None = None,
        *,
        config_yaml: Path | None = None,
        profile_override: str | None = None,
        token_override: str | None = None,
    ) -> "DashboardConfig":
        """Merge packaged defaults with the optional project override.

        Precedence for UX knobs (kanban): project `dashboard.yaml` >
        packaged `dashboard.yaml` > hard-coded dataclass defaults.

        Precedence for profile/auth:
            1. explicit `profile_override` / `token_override` (CLI flags).
            2. `ORCH_DASHBOARD_TOKEN` env var (token only).
            3. top-level `dashboard:` block in `config.yaml`.
            4. dataclass defaults (operator profile, no token).

        Missing files are silently OK — the dashboard boots with defaults.
        """
        packaged = Path(__file__).parent / "dashboard.yaml"
        merged: dict[str, Any] = {}
        for src in (packaged, (project_root / "dashboard.yaml") if project_root else None):
            if src is None or not src.exists():
                continue
            try:
                data = yaml.safe_load(src.read_text(encoding="utf-8")) or {}
            except (yaml.YAMLError, OSError) as exc:
                print(f"[dashboard] ignoring {src}: {exc}", file=sys.stderr)
                continue
            if not isinstance(data, dict):
                continue
            _deep_merge(merged, data)

        kanban_raw = merged.get("kanban") or {}
        kanban = KanbanDefaults(
            wip_default=_coerce_int_or_none(kanban_raw.get("wip_default")),
            sort_default=_coerce_str_or_none(kanban_raw.get("sort_default")),
            group_default=_coerce_str_or_none(kanban_raw.get("group_default")),
            refresh_interval_s=int(kanban_raw.get("refresh_interval_s") or 10),
        )

        # Read the `dashboard:` block from config.yaml when present. We
        # look it up separately from dashboard.yaml because config.yaml
        # is the orchestrator-wide config and typically already resolved
        # by the CLI; loading it here keeps the dashboard boot standalone.
        cfg_block: dict[str, Any] = {}
        if config_yaml is not None and config_yaml.exists():
            try:
                cfg_data = yaml.safe_load(config_yaml.read_text(encoding="utf-8")) or {}
                if isinstance(cfg_data, dict):
                    block = cfg_data.get("dashboard") or {}
                    if isinstance(block, dict):
                        cfg_block = block
            except (yaml.YAMLError, OSError) as exc:
                print(f"[dashboard] ignoring dashboard block in {config_yaml}: {exc}",
                      file=sys.stderr)

        profile = _resolve_profile(
            explicit=profile_override,
            from_config=cfg_block.get("profile"),
        )
        token = _resolve_token(
            explicit=token_override,
            from_env=os.environ.get("ORCH_DASHBOARD_TOKEN"),
            from_config=cfg_block.get("token"),
        )
        routes = _resolve_routes(cfg_block.get("stakeholder_routes"))

        return cls(
            kanban=kanban,
            profile=profile,
            token=token,
            stakeholder_routes=routes,
        )


# ---- Helpers ---------------------------------------------------------------


def _deep_merge(base: dict, override: dict) -> None:
    """In-place recursive merge — later wins, dicts merge, scalars replace."""
    for k, v in override.items():
        if isinstance(v, dict) and isinstance(base.get(k), dict):
            _deep_merge(base[k], v)
        else:
            base[k] = v


def _coerce_int_or_none(v: Any) -> int | None:
    if v is None or v == "":
        return None
    try:
        return int(v)
    except (TypeError, ValueError):
        return None


def _coerce_str_or_none(v: Any) -> str | None:
    if v is None or v == "":
        return None
    return str(v)


def _resolve_profile(*, explicit: str | None, from_config: Any) -> str:
    """Pick the effective profile, warning + falling back on invalid values."""
    for candidate in (explicit, from_config):
        if not candidate:
            continue
        name = str(candidate).strip().lower()
        if name in VALID_PROFILES:
            return name
        print(
            f"[dashboard] ignoring unknown profile {candidate!r}; "
            f"valid = {sorted(VALID_PROFILES)}",
            file=sys.stderr,
        )
    return PROFILE_OPERATOR


def _resolve_token(
    *,
    explicit: str | None,
    from_env: str | None,
    from_config: Any,
) -> str | None:
    """Token precedence: CLI flag > env var > config. Empty strings drop."""
    for candidate in (explicit, from_env, from_config):
        if candidate is None:
            continue
        token = str(candidate).strip()
        if token:
            return token
    return None


def _resolve_routes(from_config: Any) -> tuple[str, ...]:
    """Merge the config-provided allow-list with the built-in defaults.

    Operator can EXTEND the stakeholder allow-list via config.yaml. They
    cannot shrink it below the built-in defaults (removing `/static/` for
    instance would nuke the CSS on the curated page — pointless footgun).
    """
    if not isinstance(from_config, (list, tuple)):
        return DEFAULT_STAKEHOLDER_ROUTES
    extra: list[str] = []
    for item in from_config:
        if not isinstance(item, str):
            continue
        s = item.strip()
        if s and s not in DEFAULT_STAKEHOLDER_ROUTES and s not in extra:
            extra.append(s)
    return DEFAULT_STAKEHOLDER_ROUTES + tuple(extra)


def stakeholder_route_matches(
    path: str,
    route_name: str | None,
    allow_list: tuple[str, ...],
) -> bool:
    """True when the incoming request may be served under the stakeholder profile.

    Rules:
        - Any entry starting with `/` is a path-prefix match against `path`.
        - Every other entry is a case-sensitive route-name match against
          the FastAPI route's `.name` attribute.

    Both matches short-circuit — order in the allow-list doesn't matter.
    """
    for entry in allow_list:
        if not entry:
            continue
        if entry.startswith("/"):
            if path == entry.rstrip("/") or path.startswith(entry):
                return True
        elif route_name and entry == route_name:
            return True
    return False
