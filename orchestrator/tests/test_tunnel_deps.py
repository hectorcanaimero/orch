"""Tests for `orchestrator.dashboard.tunnel.deps` — Sprint E-5 (TUN-3, TUN-4).

Full 2x2x2 gate matrix (enabled × operator × loopback) plus the
non-raising `evaluate_capabilities` variant used by /capabilities.
"""

from __future__ import annotations

from types import SimpleNamespace
from typing import Any

import pytest
from fastapi import HTTPException

from orchestrator.dashboard.dashboard_config import (
    PROFILE_OPERATOR,
    PROFILE_STAKEHOLDER,
    DashboardConfig,
    KanbanDefaults,
    TunnelConfig,
)
from orchestrator.dashboard.tunnel.deps import (
    REASON_CONFIG_DISABLED,
    REASON_HOST_GATE,
    REASON_OK,
    REASON_PROFILE_GATE,
    evaluate_capabilities,
    require_loopback_host,
    require_operator_profile,
    require_tunnel_enabled,
)


# ---- Fixtures -----------------------------------------------------------


def _cfg(*, enabled: bool = True, profile: str = PROFILE_OPERATOR) -> DashboardConfig:
    return DashboardConfig(
        kanban=KanbanDefaults(),
        profile=profile,
        tunnel=TunnelConfig(enabled=enabled, provider="autossh", command="autossh"),
    )


def _fake_request(
    *, host: str = "127.0.0.1", cfg: DashboardConfig | None = None
) -> Any:
    """Return a Request-shaped stub whose `.headers.get` and app state work.

    We avoid Starlette's real `Request` because it requires an ASGI scope
    dance — the deps only touch `.headers` and `.app.state.app_state.config`
    so a SimpleNamespace suffices and makes the matrix testable in bulk.
    """
    cfg = cfg or _cfg()
    app_state = SimpleNamespace(config=cfg)
    app = SimpleNamespace(state=SimpleNamespace(app_state=app_state))
    headers = {"host": host} if host else {}

    class _Headers:
        def __init__(self, data: dict[str, str]) -> None:
            self._data = {k.lower(): v for k, v in data.items()}

        def get(self, key: str, default: Any = None) -> Any:
            return self._data.get(key.lower(), default)

    return SimpleNamespace(app=app, headers=_Headers(headers))


# ---- Gate 1: config (404) -----------------------------------------------


def test_require_tunnel_enabled_passes_when_enabled() -> None:
    req = _fake_request(cfg=_cfg(enabled=True))
    require_tunnel_enabled(req)  # no raise


def test_require_tunnel_enabled_raises_404_when_disabled() -> None:
    req = _fake_request(cfg=_cfg(enabled=False))
    with pytest.raises(HTTPException) as exc:
        require_tunnel_enabled(req)
    assert exc.value.status_code == 404


# ---- Gate 2: operator profile (403) --------------------------------------


def test_require_operator_profile_passes_for_operator() -> None:
    req = _fake_request(cfg=_cfg(profile=PROFILE_OPERATOR))
    require_operator_profile(req)


def test_require_operator_profile_rejects_stakeholder() -> None:
    req = _fake_request(cfg=_cfg(profile=PROFILE_STAKEHOLDER))
    with pytest.raises(HTTPException) as exc:
        require_operator_profile(req)
    assert exc.value.status_code == 403


# ---- Gate 3: loopback host (403) -----------------------------------------


@pytest.mark.parametrize(
    "host",
    ["127.0.0.1", "localhost", "::1", "[::1]", "127.0.0.1:7420", "localhost:7420"],
)
def test_require_loopback_host_accepts_loopback_variants(host: str) -> None:
    req = _fake_request(host=host)
    require_loopback_host(req)


@pytest.mark.parametrize(
    "host",
    [
        "example.com",
        "abc123.a.pinggy.link",
        "192.168.1.5",
        "10.0.0.1:80",
        "evil.tld",
        "",
    ],
)
def test_require_loopback_host_rejects_non_loopback(host: str) -> None:
    req = _fake_request(host=host)
    with pytest.raises(HTTPException) as exc:
        require_loopback_host(req)
    assert exc.value.status_code == 403


def test_loopback_gate_ignores_forwarded_host_header() -> None:
    """TUN-3: proxy headers MUST NOT be trusted."""
    req = _fake_request(host="evil.tld")

    # Layer an X-Forwarded-Host header on top; the dep still uses `host`.
    req.headers._data["x-forwarded-host"] = "127.0.0.1"
    with pytest.raises(HTTPException) as exc:
        require_loopback_host(req)
    assert exc.value.status_code == 403


# ---- Combined matrix (TUN-3 gate order) ---------------------------------


@pytest.mark.parametrize("enabled", [True, False])
@pytest.mark.parametrize("profile", [PROFILE_OPERATOR, PROFILE_STAKEHOLDER])
@pytest.mark.parametrize("host", ["127.0.0.1", "abc123.a.pinggy.link"])
def test_gate_matrix_2x2x2(enabled: bool, profile: str, host: str) -> None:
    cfg = _cfg(enabled=enabled, profile=profile)
    req = _fake_request(cfg=cfg, host=host)
    all_pass = enabled and profile == PROFILE_OPERATOR and host == "127.0.0.1"
    # Config gate runs first, then profile, then host.
    if not enabled:
        with pytest.raises(HTTPException) as exc:
            require_tunnel_enabled(req)
        assert exc.value.status_code == 404
        return
    require_tunnel_enabled(req)
    if profile != PROFILE_OPERATOR:
        with pytest.raises(HTTPException) as exc:
            require_operator_profile(req)
        assert exc.value.status_code == 403
        return
    require_operator_profile(req)
    if host != "127.0.0.1":
        with pytest.raises(HTTPException) as exc:
            require_loopback_host(req)
        assert exc.value.status_code == 403
        return
    require_loopback_host(req)
    assert all_pass


# ---- evaluate_capabilities (TUN-4, non-raising) --------------------------


def test_evaluate_capabilities_returns_ok_when_all_gates_pass() -> None:
    cfg = _cfg(enabled=True, profile=PROFILE_OPERATOR)
    req = _fake_request(cfg=cfg, host="127.0.0.1")
    can, reason = evaluate_capabilities(req, cfg)
    assert can is True
    assert reason == REASON_OK


def test_evaluate_capabilities_reports_config_first() -> None:
    """Gate order: config → profile → host. Disabled wins over the others."""
    cfg = _cfg(enabled=False, profile=PROFILE_STAKEHOLDER)
    req = _fake_request(cfg=cfg, host="evil.tld")
    can, reason = evaluate_capabilities(req, cfg)
    assert can is False
    assert reason == REASON_CONFIG_DISABLED


def test_evaluate_capabilities_reports_profile_over_host() -> None:
    cfg = _cfg(enabled=True, profile=PROFILE_STAKEHOLDER)
    req = _fake_request(cfg=cfg, host="evil.tld")
    can, reason = evaluate_capabilities(req, cfg)
    assert can is False
    assert reason == REASON_PROFILE_GATE


def test_evaluate_capabilities_reports_host_when_only_host_fails() -> None:
    cfg = _cfg(enabled=True, profile=PROFILE_OPERATOR)
    req = _fake_request(cfg=cfg, host="abc123.a.pinggy.link")
    can, reason = evaluate_capabilities(req, cfg)
    assert can is False
    assert reason == REASON_HOST_GATE
