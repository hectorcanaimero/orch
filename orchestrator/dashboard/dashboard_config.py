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
    "stakeholder_summary_json",
    # Sprint E-5: capabilities is intentionally auth-free so the SPA can
    # decide whether to render the tunnel panel BEFORE requesting a token.
    "api_tunnel_capabilities",
    # Sprint E-6 UX: the SPA reads its own profile from /api/whoami to hide
    # operator-only nav items in stakeholder mode. Profile is not a secret
    # (the token is); leaking the mode indicator is safe. Token auth still
    # applies via TokenAuthMiddleware — this only bypasses the profile guard.
    "api_whoami",
    # Documents view: PRD / SPEC / arch markdown files are project artefacts
    # stakeholders should be able to read. Listing and fetching content are
    # both read-only and never expose runtime secrets.
    "api_docs_list",
    "api_docs_content",
    # SPA-at-root migration: the React SPA now lives at `/` (moved from
    # `/spa/`) with its bundled assets under `/assets/*`. Two allow-list
    # forms cover it:
    #   - "spa" (the SPA StaticFiles mount name registered LAST in
    #     create_app()) — matches any path that fell through the explicit
    #     routes, so `/kanban`, `/metrics`, `/logs`, `/stakeholder`, and
    #     every other React Router client route resolve to the SPA
    #     fallback HTML.
    #   - "/assets/" (path prefix) — belt-and-suspenders for the bundled
    #     JS/CSS chunks: even if the SPA mount is absent (bare-source
    #     checkout, no `pnpm build`) the middleware still lets the
    #     request through so a probing client sees a 404, not a
    #     mysterious 403.
    "spa",
    "/assets/",
)


# ---- Errors ----------------------------------------------------------------


class ConfigError(ValueError):
    """Raised when `dashboard.yaml` contains an invalid value that MUST
    prevent boot (per TUN-1 — bad tunnel config is a hard failure so
    operators notice at startup rather than at first request)."""


# ---- Dataclasses -----------------------------------------------------------


@dataclass(frozen=True, slots=True)
class KanbanDefaults:
    wip_default: int | None = None
    sort_default: str | None = None
    group_default: str | None = None
    refresh_interval_s: int = 10


@dataclass(frozen=True, slots=True)
class TunnelConfig:
    """Tunnel supervisor config — Sprint E-5 TUN-1.

    Defaults keep the feature OFF so existing dashboards boot unchanged.
    `provider` and `command` are cross-checked against the provider
    allowlist (TUN-2) at load time; a mismatch raises `ConfigError`.
    """

    enabled: bool = False
    provider: str = "autossh"
    command: str = "autossh"
    args: tuple[str, ...] = ()
    auto_start: bool = False
    url_regex: str | None = None
    startup_probe_timeout_s: int = 3
    url_parse_timeout_s: int = 30


@dataclass(frozen=True, slots=True)
class DashboardConfig:
    """Full dashboard config surface — UX defaults + profile/auth.

    Every field has a safe default so existing operator-only setups don't
    need to touch config.yaml. Backwards-compatible: `profile` defaults to
    `operator`, `token` defaults to `None` (auth off), routes default to
    the built-in allow-list.

    `server_port` / `server_host` come from an optional top-level
    `server:` block in `dashboard.yaml`. Both default to `None` — the CLI
    resolves the actual bind after applying the flag > env > config >
    hardcoded-default precedence. Keeping them nullable here means an
    absent block is trivially distinguishable from an explicit value.
    """

    kanban: KanbanDefaults
    profile: str = PROFILE_OPERATOR
    token: str | None = None
    stakeholder_routes: tuple[str, ...] = DEFAULT_STAKEHOLDER_ROUTES
    tunnel: TunnelConfig = field(default_factory=TunnelConfig)
    server_port: int | None = None
    server_host: str | None = None

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
        if cfg_block.get("show_spend_to_stakeholder"):
            routes = routes + ("api_budget_summary",)
        tunnel = parse_tunnel_section(merged.get("tunnel"))
        server_port, server_host = parse_server_section(merged.get("server"))

        return cls(
            kanban=kanban,
            profile=profile,
            token=token,
            stakeholder_routes=routes,
            tunnel=tunnel,
            server_port=server_port,
            server_host=server_host,
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


def parse_tunnel_section(raw: Any) -> TunnelConfig:
    """Parse the `tunnel:` block from `dashboard.yaml` into `TunnelConfig`.

    Absent or empty → disabled default. Any validation error raises
    `ConfigError` naming the offending key (TUN-1 requires dashboard boot
    to fail loudly rather than silently disable the feature).
    """
    if raw is None:
        return TunnelConfig()
    if not isinstance(raw, dict):
        raise ConfigError(
            f"tunnel: expected mapping, got {type(raw).__name__}"
        )

    # Lazy import so a config load doesn't force the tunnel subpackage on
    # environments that never touch the feature.
    from orchestrator.dashboard.tunnel.providers import PROVIDERS, resolve

    enabled = bool(raw.get("enabled", False))
    provider_raw = raw.get("provider", "autossh")
    if not isinstance(provider_raw, str) or not provider_raw.strip():
        raise ConfigError("tunnel.provider must be a non-empty string")
    provider = provider_raw.strip()
    try:
        spec = resolve(provider)
    except KeyError:
        raise ConfigError(
            f"tunnel.provider {provider!r} is not on the allowlist "
            f"(known providers: {sorted(PROVIDERS.keys())})"
        )

    command_raw = raw.get("command", spec.command)
    if not isinstance(command_raw, str) or not command_raw.strip():
        raise ConfigError("tunnel.command must be a non-empty string")
    command = command_raw.strip()
    if command != spec.command:
        raise ConfigError(
            f'tunnel.command must equal "{spec.command}" for provider '
            f'"{provider}" (got {command!r})'
        )

    args_raw = raw.get("args", None)
    if args_raw is None:
        args = spec.default_args
    elif isinstance(args_raw, list):
        if not all(isinstance(a, str) for a in args_raw):
            raise ConfigError("tunnel.args must be a list of strings")
        args = tuple(args_raw)
    elif isinstance(args_raw, dict):
        # Dict form is a shorthand — flatten into `--key value` argv fragments.
        flat: list[str] = []
        for k, v in args_raw.items():
            flat.append(f"--{k}")
            flat.append(str(v))
        args = tuple(flat)
    else:
        raise ConfigError(
            f"tunnel.args must be a list or mapping, got {type(args_raw).__name__}"
        )

    auto_start = bool(raw.get("auto_start", False))

    url_regex_raw = raw.get("url_regex", None)
    if url_regex_raw is not None and not isinstance(url_regex_raw, str):
        raise ConfigError("tunnel.url_regex must be a string when provided")
    url_regex = url_regex_raw if isinstance(url_regex_raw, str) else None

    startup_probe_timeout_s = _coerce_bounded_int(
        raw.get("startup_probe_timeout_s", 3),
        key="tunnel.startup_probe_timeout_s",
        low=1,
        high=30,
    )
    url_parse_timeout_s = _coerce_bounded_int(
        raw.get("url_parse_timeout_s", 30),
        key="tunnel.url_parse_timeout_s",
        low=5,
        high=300,
    )

    return TunnelConfig(
        enabled=enabled,
        provider=provider,
        command=command,
        args=args,
        auto_start=auto_start,
        url_regex=url_regex,
        startup_probe_timeout_s=startup_probe_timeout_s,
        url_parse_timeout_s=url_parse_timeout_s,
    )


def parse_server_section(raw: Any) -> tuple[int | None, str | None]:
    """Parse the optional `server:` block from `dashboard.yaml`.

    Returns `(port, host)` — either or both may be `None` if the block is
    absent or only sets one field. On any invalid value we raise
    `ConfigError`, matching the tunnel section's fail-loud stance (better
    to surface a bad config at boot than silently ignore it and confuse
    the operator later).

    Validation:
        - `server` must be a mapping.
        - `port`, when present, must be an int in [1, 65535].
        - `host`, when present, must be a non-empty string.
    """
    if raw is None:
        return None, None
    if not isinstance(raw, dict):
        raise ConfigError(
            f"server: expected mapping, got {type(raw).__name__}"
        )

    port: int | None = None
    if "port" in raw and raw["port"] is not None:
        port_raw = raw["port"]
        # Reject bool (bool is a subclass of int but "port: true" is nonsense).
        if isinstance(port_raw, bool):
            raise ConfigError(
                f"server.port must be an integer in [1, 65535], got {port_raw!r}"
            )
        try:
            port = int(port_raw)
        except (TypeError, ValueError):
            raise ConfigError(
                f"server.port must be an integer in [1, 65535], got {port_raw!r}"
            )
        if port < 1 or port > 65535:
            raise ConfigError(
                f"server.port must be in [1, 65535], got {port}"
            )

    host: str | None = None
    if "host" in raw and raw["host"] is not None:
        host_raw = raw["host"]
        if not isinstance(host_raw, str) or not host_raw.strip():
            raise ConfigError(
                "server.host must be a non-empty string"
            )
        host = host_raw.strip()

    return port, host


def _coerce_bounded_int(value: Any, *, key: str, low: int, high: int) -> int:
    try:
        n = int(value)
    except (TypeError, ValueError):
        raise ConfigError(f"{key} must be an integer, got {value!r}")
    if n < low or n > high:
        raise ConfigError(f"{key} must be in [{low}, {high}], got {n}")
    return n


def stakeholder_route_matches(
    path: str,
    route_name: str | None,
    allow_list: tuple[str, ...],
) -> bool:
    """True when the incoming request may be served under the stakeholder profile.

    Rules:
        - Bare `/` is an EXACT match (only the index). Otherwise a bare
          slash would let every path through since they all start with `/`.
        - Any other entry starting with `/` is a path-prefix match against
          `path`.
        - Every other entry is a case-sensitive route-name match against
          the FastAPI route's `.name` attribute — the SPA StaticFiles
          mount is registered as `name="spa"` so allow-listing that name
          covers the SPA shell + every client-side React Router route
          without also exposing the operator JSON APIs.

    Matches short-circuit — order in the allow-list doesn't matter.
    """
    for entry in allow_list:
        if not entry:
            continue
        if entry.startswith("/"):
            # Special-case bare `/` — exact match only (SPA index HTML).
            # Without this the middleware would leak `/api/*` etc.
            if entry == "/":
                if path == "/":
                    return True
                continue
            if path == entry.rstrip("/") or path.startswith(entry):
                return True
        elif route_name and entry == route_name:
            return True
    return False
