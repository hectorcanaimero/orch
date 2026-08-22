"""ASGI middleware for the profile-aware dashboard.

Two independent middlewares:

    - `TokenAuthMiddleware` — validates `Authorization: Bearer <token>` OR
      `?token=<token>` query param on requests that fall inside the
      stakeholder-visible surface. Runs FIRST so an unauthenticated caller
      never sees route-existence hints (401 comes before 403).

    - `ProfileGuardMiddleware` — enforces the stakeholder allow-list. Any
      route not on the allow-list returns 403 with a bland message when
      the request is being handled under the stakeholder profile.

Design notes (Sprint E-2):

    - Middleware stays framework-neutral (pure ASGI callable) so tests can
      exercise it against a bare Starlette app without dragging FastAPI's
      TestClient into every unit test.

    - The order of middleware registration matters: FastAPI runs the
      LAST-added middleware first. `server.py` therefore adds
      `ProfileGuardMiddleware` first and `TokenAuthMiddleware` last so the
      order is `auth → guard → route handler` on the wire.

    - Both middlewares are no-ops when `profile == "operator"`. Existing
      operator-only deployments are unchanged — no auth, no route gating.

    - In `both` mode we bounce the request into stakeholder-mode ONLY when
      the path is under `/stakeholder/*`. Operator paths stay open (no
      auth, full data) because the operator is expected to be on
      localhost when running mixed-mode.

    - 401/403 responses NEVER leak the requested path or a stack trace.
      The body is a fixed one-liner and we set `Cache-Control: no-store`
      so intermediaries don't accidentally cache the rejection.
"""

from __future__ import annotations

import hmac
from typing import Awaitable, Callable

from starlette.middleware.base import BaseHTTPMiddleware
from starlette.requests import Request
from starlette.responses import Response
from starlette.types import ASGIApp

from orchestrator.dashboard.dashboard_config import (
    PROFILE_BOTH,
    PROFILE_OPERATOR,
    PROFILE_STAKEHOLDER,
    STAKEHOLDER_PATH_PREFIX,
    DashboardConfig,
    stakeholder_route_matches,
)


# ---- Helpers ---------------------------------------------------------------


def _plain(status_code: int, body: str) -> Response:
    """Build a minimal text/plain response with no-cache headers."""
    return Response(
        content=body,
        status_code=status_code,
        media_type="text/plain; charset=utf-8",
        headers={"Cache-Control": "no-store", "X-Content-Type-Options": "nosniff"},
    )


def _extract_token(request: Request) -> str | None:
    """Pull the caller-supplied token from `Authorization: Bearer` or `?token=`.

    Both header and query variants are accepted so a stakeholder can
    bookmark the URL. Header wins when both are present. Whitespace is
    trimmed; empty tokens are treated as missing.
    """
    auth = request.headers.get("Authorization") or request.headers.get("authorization")
    if auth:
        stripped = auth.strip()
        # Case-insensitive `Bearer ` prefix.
        if stripped.lower().startswith("bearer "):
            token = stripped[7:].strip()
            if token:
                return token
    q_token = request.query_params.get("token")
    if q_token:
        q_token = q_token.strip()
        if q_token:
            return q_token
    return None


def _is_stakeholder_context(config: DashboardConfig, path: str) -> bool:
    """True when the request should be handled under the stakeholder profile.

    - profile=operator  → never.
    - profile=stakeholder → always.
    - profile=both      → only for paths under `/stakeholder/*`.
    """
    if config.profile == PROFILE_OPERATOR:
        return False
    if config.profile == PROFILE_STAKEHOLDER:
        return True
    if config.profile == PROFILE_BOTH:
        return path == STAKEHOLDER_PATH_PREFIX or path.startswith(
            STAKEHOLDER_PATH_PREFIX + "/"
        )
    return False


def _route_name(request: Request) -> str | None:
    """Best-effort lookup of the current route's `.name` for allow-list matching.

    `BaseHTTPMiddleware` runs BEFORE Starlette's routing populates the
    scope with `route`/`endpoint`, so we walk `app.routes` and match by
    hand. We only need the route name (not its handler) so this stays
    cheap even with a few dozen routes.

    Falls back to `None` (path-prefix match still works) when no route
    matches — typically for 404s.
    """
    app = request.scope.get("app")
    if app is None:
        return None
    router = getattr(app, "router", None)
    if router is None:
        return None
    routes = getattr(router, "routes", None) or []
    from starlette.routing import Match

    for route in routes:
        try:
            match, _ = route.matches(request.scope)
        except (AttributeError, TypeError):
            continue
        if match == Match.FULL:
            name = getattr(route, "name", None)
            return str(name) if name else None
    return None


# ---- Middlewares -----------------------------------------------------------


class TokenAuthMiddleware(BaseHTTPMiddleware):
    """Enforce the shared secret token on stakeholder-context requests.

    Order-of-events on the wire:
        1. Compute if the request falls under the stakeholder profile.
        2. If no → pass through unmodified (operator mode).
        3. If yes → require a token AND compare it constant-time. Fail 401.

    We use `hmac.compare_digest` so a comparison never leaks length via
    timing. When no token is configured on the SERVER side we refuse the
    request outright (500-adjacent misconfiguration, returned as 401 for
    the same info-hiding reason).
    """

    def __init__(self, app: ASGIApp, config: DashboardConfig) -> None:
        super().__init__(app)
        self._config = config

    async def dispatch(
        self,
        request: Request,
        call_next: Callable[[Request], Awaitable[Response]],
    ) -> Response:
        cfg = self._config
        path = request.url.path

        if not _is_stakeholder_context(cfg, path):
            return await call_next(request)

        # Sprint E-5: capabilities is intentionally auth-free so the SPA can
        # decide whether to render the tunnel panel BEFORE requesting a token.
        if path == "/api/tunnel/capabilities":
            return await call_next(request)

        expected = cfg.token
        if not expected:
            # Server misconfigured (profile=stakeholder but no token set).
            # Never reveal that in the body — same 401 as "wrong token".
            return _plain(401, "unauthorized")

        supplied = _extract_token(request)
        if not supplied or not hmac.compare_digest(supplied, expected):
            return _plain(401, "unauthorized")

        return await call_next(request)


class ProfileGuardMiddleware(BaseHTTPMiddleware):
    """Fence off operator-only routes when the request is stakeholder-context.

    Every route not on the stakeholder allow-list returns 403. WebSocket
    upgrade attempts get the same treatment via a bespoke ASGI hook
    (BaseHTTPMiddleware doesn't cover websockets on its own — Starlette
    routes the connect frame through a separate scope type).
    """

    def __init__(self, app: ASGIApp, config: DashboardConfig) -> None:
        super().__init__(app)
        self._config = config

    async def dispatch(
        self,
        request: Request,
        call_next: Callable[[Request], Awaitable[Response]],
    ) -> Response:
        cfg = self._config
        path = request.url.path

        if not _is_stakeholder_context(cfg, path):
            return await call_next(request)

        name = _route_name(request)
        if stakeholder_route_matches(path, name, cfg.stakeholder_routes):
            return await call_next(request)
        return _plain(403, "forbidden")


async def websocket_stakeholder_guard(
    scope, receive, send, *, config: DashboardConfig
) -> bool:
    """Reject stakeholder-context websocket handshakes.

    Returns True when the handshake was rejected (caller should stop
    processing) and False when it should be allowed to continue. Used by
    `log_stream.py` where the SSE stream is HTTP but any future websocket
    endpoint would plug in the same guard.

    We deliberately reject BEFORE the endpoint runs to avoid leaking
    per-endpoint error shapes to the stakeholder side.
    """
    if scope.get("type") != "websocket":
        return False
    path = scope.get("path", "")
    # Reuse the shared HTTP predicate — path prefix rules apply identically.
    class _StubRequest:
        url = type("U", (), {"path": path})()

    if not _is_stakeholder_context(config, path):
        return False
    # Stakeholder websockets are always denied at MVP scope.
    await send({"type": "websocket.close", "code": 1008})  # policy violation
    return True
