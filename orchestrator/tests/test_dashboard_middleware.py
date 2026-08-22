"""Unit tests for the dashboard profile middleware layer.

Covers:
    - `dashboard_config.DashboardConfig.load` — profile/token/routes
      resolution + precedence (CLI > env > config > default).
    - `middleware.TokenAuthMiddleware` — Bearer header + `?token=` param,
      constant-time compare, misconfig behavior.
    - `middleware.ProfileGuardMiddleware` — 403 for non-allow-listed paths,
      pass-through for allow-listed routes.
    - `websocket_stakeholder_guard` — closes stakeholder ws handshakes.

The middlewares are exercised against a bare Starlette app so we don't
need the full dashboard scaffolding here — cheap + fast + easy to reason
about failure modes.
"""

from __future__ import annotations

from pathlib import Path

import pytest

from orchestrator.dashboard.dashboard_config import (
    DEFAULT_STAKEHOLDER_ROUTES,
    PROFILE_BOTH,
    PROFILE_OPERATOR,
    PROFILE_STAKEHOLDER,
    DashboardConfig,
    stakeholder_route_matches,
)


# ---- Config resolution -----------------------------------------------------


def test_config_defaults_to_operator_when_nothing_set() -> None:
    cfg = DashboardConfig.load()
    assert cfg.profile == PROFILE_OPERATOR
    assert cfg.token is None
    assert cfg.stakeholder_routes == DEFAULT_STAKEHOLDER_ROUTES


def test_config_reads_profile_from_config_yaml(tmp_path: Path) -> None:
    yaml_path = tmp_path / "config.yaml"
    yaml_path.write_text(
        "dashboard:\n  profile: stakeholder\n  token: hunter2\n",
        encoding="utf-8",
    )
    cfg = DashboardConfig.load(config_yaml=yaml_path)
    assert cfg.profile == PROFILE_STAKEHOLDER
    assert cfg.token == "hunter2"


def test_config_env_var_overrides_yaml_token(tmp_path: Path, monkeypatch) -> None:
    yaml_path = tmp_path / "config.yaml"
    yaml_path.write_text(
        "dashboard:\n  profile: stakeholder\n  token: from-yaml\n",
        encoding="utf-8",
    )
    monkeypatch.setenv("ORCH_DASHBOARD_TOKEN", "from-env")
    cfg = DashboardConfig.load(config_yaml=yaml_path)
    assert cfg.token == "from-env"


def test_config_cli_overrides_env_and_yaml(tmp_path: Path, monkeypatch) -> None:
    yaml_path = tmp_path / "config.yaml"
    yaml_path.write_text(
        "dashboard:\n  profile: operator\n  token: from-yaml\n", encoding="utf-8",
    )
    monkeypatch.setenv("ORCH_DASHBOARD_TOKEN", "from-env")
    cfg = DashboardConfig.load(
        config_yaml=yaml_path,
        profile_override="stakeholder",
        token_override="from-cli",
    )
    assert cfg.profile == PROFILE_STAKEHOLDER
    assert cfg.token == "from-cli"


def test_config_invalid_profile_falls_back_to_operator(
    tmp_path: Path, capsys
) -> None:
    yaml_path = tmp_path / "config.yaml"
    yaml_path.write_text("dashboard:\n  profile: bogus\n", encoding="utf-8")
    cfg = DashboardConfig.load(config_yaml=yaml_path)
    assert cfg.profile == PROFILE_OPERATOR
    err = capsys.readouterr().err
    assert "unknown profile" in err


def test_config_stakeholder_routes_extends_but_never_shrinks(tmp_path: Path) -> None:
    yaml_path = tmp_path / "config.yaml"
    yaml_path.write_text(
        "dashboard:\n"
        "  profile: stakeholder\n"
        "  token: t\n"
        "  stakeholder_routes:\n"
        "    - /public-extra/\n"
        "    - some_custom_route_name\n",
        encoding="utf-8",
    )
    cfg = DashboardConfig.load(config_yaml=yaml_path)
    # Defaults preserved.
    for r in DEFAULT_STAKEHOLDER_ROUTES:
        assert r in cfg.stakeholder_routes
    # Extras appended.
    assert "/public-extra/" in cfg.stakeholder_routes
    assert "some_custom_route_name" in cfg.stakeholder_routes


def test_stakeholder_route_matches_by_name_and_prefix() -> None:
    allow = ("stakeholder_index", "/static/")
    assert stakeholder_route_matches("/stakeholder", "stakeholder_index", allow) is True
    assert stakeholder_route_matches("/static/main.css", None, allow) is True
    assert stakeholder_route_matches("/api/tasks", "api_tasks", allow) is False
    # Named allow-list entry with no route.name attached → no match.
    assert stakeholder_route_matches("/x", None, allow) is False


# ---- Middleware — TokenAuth ------------------------------------------------


def _bare_app_with(middleware_layers):
    """Build a tiny Starlette app so middleware runs in isolation."""
    from starlette.applications import Starlette
    from starlette.responses import PlainTextResponse
    from starlette.routing import Route

    async def _root(request):
        return PlainTextResponse("root-ok")

    async def _snapshot(request):
        return PlainTextResponse("snapshot-ok")

    async def _stakeholder(request):
        return PlainTextResponse("stakeholder-ok")

    routes = [
        Route("/", _root, name="index"),
        Route("/snapshot", _snapshot, name="snapshot"),
        Route("/stakeholder", _stakeholder, name="stakeholder_index"),
        Route("/stakeholder/summary", _stakeholder, name="stakeholder_summary_json"),
    ]
    app = Starlette(routes=routes, middleware=middleware_layers)
    return app


def _client(app):
    from fastapi.testclient import TestClient  # ships with starlette httpx client
    return TestClient(app)


def test_token_auth_operator_profile_is_noop() -> None:
    pytest.importorskip("fastapi")
    from starlette.middleware import Middleware
    from orchestrator.dashboard.middleware import TokenAuthMiddleware

    cfg = DashboardConfig.load()  # operator by default
    app = _bare_app_with([Middleware(TokenAuthMiddleware, config=cfg)])
    client = _client(app)
    r = client.get("/snapshot")
    assert r.status_code == 200
    assert r.text == "snapshot-ok"


def test_token_auth_stakeholder_rejects_without_token() -> None:
    pytest.importorskip("fastapi")
    from starlette.middleware import Middleware
    from orchestrator.dashboard.middleware import TokenAuthMiddleware

    cfg = DashboardConfig.load(profile_override="stakeholder", token_override="secret")
    app = _bare_app_with([Middleware(TokenAuthMiddleware, config=cfg)])
    client = _client(app)
    r = client.get("/")
    assert r.status_code == 401
    assert r.text == "unauthorized"
    # No cache leak, no CT sniffing.
    assert r.headers.get("Cache-Control") == "no-store"


def test_token_auth_accepts_bearer_header() -> None:
    pytest.importorskip("fastapi")
    from starlette.middleware import Middleware
    from orchestrator.dashboard.middleware import TokenAuthMiddleware

    cfg = DashboardConfig.load(profile_override="stakeholder", token_override="s3cr3t")
    app = _bare_app_with([Middleware(TokenAuthMiddleware, config=cfg)])
    client = _client(app)
    r = client.get("/", headers={"Authorization": "Bearer s3cr3t"})
    assert r.status_code == 200


def test_token_auth_accepts_query_param() -> None:
    pytest.importorskip("fastapi")
    from starlette.middleware import Middleware
    from orchestrator.dashboard.middleware import TokenAuthMiddleware

    cfg = DashboardConfig.load(profile_override="stakeholder", token_override="s3cr3t")
    app = _bare_app_with([Middleware(TokenAuthMiddleware, config=cfg)])
    client = _client(app)
    r = client.get("/?token=s3cr3t")
    assert r.status_code == 200


def test_token_auth_rejects_wrong_token() -> None:
    pytest.importorskip("fastapi")
    from starlette.middleware import Middleware
    from orchestrator.dashboard.middleware import TokenAuthMiddleware

    cfg = DashboardConfig.load(profile_override="stakeholder", token_override="s3cr3t")
    app = _bare_app_with([Middleware(TokenAuthMiddleware, config=cfg)])
    client = _client(app)
    r = client.get("/", headers={"Authorization": "Bearer wrong"})
    assert r.status_code == 401


def test_token_auth_stakeholder_misconfig_returns_401() -> None:
    """Profile=stakeholder but no token on the server side → 401, not 500."""
    pytest.importorskip("fastapi")
    from starlette.middleware import Middleware
    from orchestrator.dashboard.middleware import TokenAuthMiddleware

    cfg = DashboardConfig.load(profile_override="stakeholder")  # no token
    app = _bare_app_with([Middleware(TokenAuthMiddleware, config=cfg)])
    client = _client(app)
    r = client.get("/", headers={"Authorization": "Bearer anything"})
    assert r.status_code == 401


def test_token_auth_both_mode_only_guards_stakeholder_prefix() -> None:
    """`profile=both` means operator paths bypass auth, stakeholder paths need it."""
    pytest.importorskip("fastapi")
    from starlette.middleware import Middleware
    from orchestrator.dashboard.middleware import TokenAuthMiddleware

    cfg = DashboardConfig.load(profile_override="both", token_override="tok")
    app = _bare_app_with([Middleware(TokenAuthMiddleware, config=cfg)])
    client = _client(app)
    # Operator path — no token, still 200.
    assert client.get("/snapshot").status_code == 200
    # Stakeholder path — no token, 401.
    assert client.get("/stakeholder").status_code == 401
    # Stakeholder path with token — 200.
    assert client.get("/stakeholder?token=tok").status_code == 200


# ---- Middleware — ProfileGuard --------------------------------------------


def test_profile_guard_operator_is_noop() -> None:
    pytest.importorskip("fastapi")
    from starlette.middleware import Middleware
    from orchestrator.dashboard.middleware import ProfileGuardMiddleware

    cfg = DashboardConfig.load()
    app = _bare_app_with([Middleware(ProfileGuardMiddleware, config=cfg)])
    client = _client(app)
    assert client.get("/snapshot").status_code == 200


def test_profile_guard_blocks_non_allow_listed_in_stakeholder_mode() -> None:
    pytest.importorskip("fastapi")
    from starlette.middleware import Middleware
    from orchestrator.dashboard.middleware import ProfileGuardMiddleware

    cfg = DashboardConfig.load(profile_override="stakeholder", token_override="t")
    app = _bare_app_with([Middleware(ProfileGuardMiddleware, config=cfg)])
    client = _client(app)
    # /snapshot is NOT in the default allow-list → 403.
    r = client.get("/snapshot")
    assert r.status_code == 403
    assert r.text == "forbidden"


def test_profile_guard_allows_listed_names_in_stakeholder_mode() -> None:
    pytest.importorskip("fastapi")
    from starlette.middleware import Middleware
    from orchestrator.dashboard.middleware import ProfileGuardMiddleware

    cfg = DashboardConfig.load(profile_override="stakeholder", token_override="t")
    app = _bare_app_with([Middleware(ProfileGuardMiddleware, config=cfg)])
    client = _client(app)
    # `stakeholder_index` is in DEFAULT_STAKEHOLDER_ROUTES.
    assert client.get("/stakeholder").status_code == 200
    assert client.get("/stakeholder/summary").status_code == 200


def test_profile_guard_both_mode_only_gates_stakeholder_prefix() -> None:
    pytest.importorskip("fastapi")
    from starlette.middleware import Middleware
    from orchestrator.dashboard.middleware import ProfileGuardMiddleware

    cfg = DashboardConfig.load(profile_override="both")
    app = _bare_app_with([Middleware(ProfileGuardMiddleware, config=cfg)])
    client = _client(app)
    # Operator paths unblocked.
    assert client.get("/snapshot").status_code == 200
    # Stakeholder allow-listed → still 200 (guard passes it through).
    assert client.get("/stakeholder").status_code == 200


def test_profile_guard_403_body_hides_path_details() -> None:
    """The 403 body must not echo the requested path — no route-existence hints."""
    pytest.importorskip("fastapi")
    from starlette.middleware import Middleware
    from orchestrator.dashboard.middleware import ProfileGuardMiddleware

    cfg = DashboardConfig.load(profile_override="stakeholder", token_override="t")
    app = _bare_app_with([Middleware(ProfileGuardMiddleware, config=cfg)])
    client = _client(app)
    r = client.get("/snapshot")
    body = r.text
    assert "snapshot" not in body.lower()
    assert "route" not in body.lower()


# ---- Middleware layering (auth + guard together) --------------------------


def test_layered_middleware_auth_runs_before_guard() -> None:
    """Missing token yields 401 even for an allow-listed route (no bypass)."""
    pytest.importorskip("fastapi")
    from starlette.middleware import Middleware
    from orchestrator.dashboard.middleware import (
        ProfileGuardMiddleware,
        TokenAuthMiddleware,
    )

    cfg = DashboardConfig.load(profile_override="stakeholder", token_override="t")
    app = _bare_app_with([
        Middleware(ProfileGuardMiddleware, config=cfg),
        Middleware(TokenAuthMiddleware, config=cfg),
    ])
    client = _client(app)
    r = client.get("/stakeholder")
    assert r.status_code == 401


# ---- Websocket guard -------------------------------------------------------


def test_websocket_stakeholder_guard_closes_when_stakeholder_mode() -> None:
    import asyncio

    from orchestrator.dashboard.middleware import websocket_stakeholder_guard

    cfg = DashboardConfig.load(profile_override="stakeholder", token_override="t")
    sent: list[dict] = []

    async def _send(msg):
        sent.append(msg)

    async def _receive():  # not used
        return {"type": "websocket.connect"}

    scope = {"type": "websocket", "path": "/logs/stream"}
    result = asyncio.run(
        websocket_stakeholder_guard(scope, _receive, _send, config=cfg)
    )
    assert result is True
    assert sent and sent[0]["type"] == "websocket.close"


def test_websocket_stakeholder_guard_passthrough_in_operator_mode() -> None:
    import asyncio

    from orchestrator.dashboard.middleware import websocket_stakeholder_guard

    cfg = DashboardConfig.load()  # operator by default
    sent: list[dict] = []

    async def _send(msg):
        sent.append(msg)

    async def _receive():
        return {"type": "websocket.connect"}

    scope = {"type": "websocket", "path": "/logs/stream"}
    result = asyncio.run(
        websocket_stakeholder_guard(scope, _receive, _send, config=cfg)
    )
    assert result is False
    assert sent == []
