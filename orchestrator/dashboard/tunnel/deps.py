"""FastAPI dependency callables enforcing the three-layer tunnel gate.

Order (per TUN-3, fixed): config (404) → operator profile (403) → loopback
host (403). Each dep is independent so FastAPI's `Depends(...)` composes
them in the route signature.

`evaluate_capabilities()` mirrors the three checks WITHOUT raising —
`/api/tunnel/capabilities` uses it to always answer 200 with a reason
string identifying the first failing gate (TUN-4).
"""

from __future__ import annotations

from typing import TYPE_CHECKING

from fastapi import HTTPException, Request

from orchestrator.dashboard.dashboard_config import PROFILE_OPERATOR

if TYPE_CHECKING:
    from orchestrator.dashboard.dashboard_config import DashboardConfig


_LOOPBACK_HOSTS = frozenset({"127.0.0.1", "localhost", "::1", "[::1]"})


# Failure reason tokens returned by the capabilities endpoint. Ordered from
# highest gate to lowest so callers can short-circuit on the first hit.
REASON_OK = "ok"
REASON_CONFIG_DISABLED = "config_disabled"
REASON_PROFILE_GATE = "profile_gate"
REASON_HOST_GATE = "host_gate"
REASON_AUTOSSH_MISSING = "autossh_missing"


def _extract_host(request: Request) -> str:
    """Case-insensitive host from the `Host` header, port stripped.

    IPv6 literals arrive bracket-wrapped (`[::1]:7420`); we preserve the
    brackets because that is the canonical form we compare against.
    We never consult `X-Forwarded-Host` — reverse-proxy fronting is out
    of scope for the tunnel gate (TUN-3, resolved decision 2).
    """
    raw = request.headers.get("host") or request.headers.get("Host") or ""
    raw = raw.strip().lower()
    if not raw:
        return ""
    if raw.startswith("["):
        # `[::1]:7420` → `[::1]`
        end = raw.find("]")
        return raw[: end + 1] if end >= 0 else raw
    # Bare IPv6 literal like `::1` (multiple colons, no brackets) — leave
    # it alone; only strip the trailing port when the shape is host:port.
    if raw.count(":") > 1:
        return raw
    # `localhost:7420` → `localhost`
    return raw.split(":", 1)[0]


def _tunnel_enabled(config: "DashboardConfig") -> bool:
    tunnel = getattr(config, "tunnel", None)
    return bool(tunnel and tunnel.enabled)


def _is_operator(config: "DashboardConfig") -> bool:
    return getattr(config, "profile", None) == PROFILE_OPERATOR


def _get_config(request: Request) -> "DashboardConfig":
    """Pull the resolved `DashboardConfig` off `app.state.app_state`.

    Kept private so tests can supply a bare `Request` with a stub app_state.
    """
    app_state = getattr(request.app.state, "app_state", None)
    if app_state is None:
        raise HTTPException(500, "dashboard app_state unavailable")
    return app_state.config


def require_tunnel_enabled(request: Request) -> None:
    """404 when `dashboard.yaml → tunnel.enabled` is false (TUN-3 gate 1)."""
    if not _tunnel_enabled(_get_config(request)):
        raise HTTPException(404, "not found")


def require_operator_profile(request: Request) -> None:
    """403 when caller's profile is not `operator` (TUN-3 gate 2).

    The tunnel is an operator-only lever regardless of dashboard mode —
    stakeholders cannot spawn, stop, or read tunnel state.
    """
    if not _is_operator(_get_config(request)):
        raise HTTPException(403, "operator only")


def require_loopback_host(request: Request) -> None:
    """403 when the request `Host` header is not loopback (TUN-3 gate 3)."""
    if _extract_host(request) not in _LOOPBACK_HOSTS:
        raise HTTPException(403, "loopback only")


def evaluate_capabilities(
    request: Request, config: "DashboardConfig"
) -> tuple[bool, str]:
    """Non-raising version of the three-gate check for `/capabilities`.

    Returns `(can_control, reason)`. `reason` is the FIRST failing gate in
    the fixed order — never a list, per TUN-4. Callers can layer the
    binary-presence check on top of a passing result (TUN-4: `autossh_missing`
    is evaluated last, so config/profile/host all pass before we consult PATH).
    """
    if not _tunnel_enabled(config):
        return False, REASON_CONFIG_DISABLED
    if not _is_operator(config):
        return False, REASON_PROFILE_GATE
    if _extract_host(request) not in _LOOPBACK_HOSTS:
        return False, REASON_HOST_GATE
    return True, REASON_OK
