"""HTTP integration tests for the tunnel endpoints — Sprint E-5 Batch 3.

Covers TUN-3 (three-gate order), TUN-4 (`/capabilities`), TUN-5 (`/status`),
TUN-6 (`/start`, `/stop`, 202/409 semantics), and TUN-10 (SSE `/logs`
including the D3 idle cutoff).

Every test monkeypatches `TunnelManager.start`/`.stop`/`.status`/`.logs_iter`
so no real subprocess spawns — we only exercise the HTTP shape and gate
composition.
"""

from __future__ import annotations

import json
import os
from pathlib import Path
from typing import Any, Iterator

import pytest

from orchestrator.dashboard.dashboard_config import (
    PROFILE_OPERATOR,
    PROFILE_STAKEHOLDER,
)


pytest.importorskip("fastapi")
pytest.importorskip("jinja2")
from fastapi.testclient import TestClient  # noqa: E402


# ---- Fixture project + client wiring -------------------------------------


def _write_dashboard_yaml(root: Path, *, enabled: bool, extra: dict | None = None) -> None:
    lines = ["tunnel:", f"  enabled: {'true' if enabled else 'false'}"]
    if enabled:
        lines += [
            "  provider: autossh",
            "  command: autossh",
            "  auto_start: false",
        ]
    for k, v in (extra or {}).items():
        lines.append(f"  {k}: {v}")
    (root / "dashboard.yaml").write_text("\n".join(lines) + "\n", encoding="utf-8")


def _scaffold(tmp_path: Path, *, enabled: bool = True, extra: dict | None = None) -> Path:
    root = tmp_path / "proj"
    (root / "orchestrator" / "state").mkdir(parents=True)
    (root / "scripts").mkdir(parents=True)
    (root / "scripts" / "task-start.sh").write_text("#!/bin/sh\nexit 0\n")
    (root / "tasks.json").write_text(
        json.dumps({"meta": {}, "phases": [], "tasks": []}),
        encoding="utf-8",
    )
    # Empty config.yaml — token/profile come via overrides on create_app.
    (root / "orchestrator" / "config.yaml").write_text("", encoding="utf-8")
    _write_dashboard_yaml(root, enabled=enabled, extra=extra)
    return root


def _client(
    tmp_path: Path,
    *,
    enabled: bool = True,
    profile: str = PROFILE_OPERATOR,
    token: str | None = None,
    extra: dict | None = None,
):
    from orchestrator.dashboard.server import create_app
    from orchestrator.paths import ProjectPaths

    root = _scaffold(tmp_path, enabled=enabled, extra=extra)
    paths = ProjectPaths(
        project_root=root,
        project_id="proj",
        config_yaml=root / "orchestrator" / "config.yaml",
        explicit_root=True,
        state_layout="legacy",
    )
    app = create_app(paths=paths, profile_override=profile, token_override=token)
    return TestClient(app), app


class FakeTunnelManager:
    """In-memory stand-in — mirrors the public surface used by the routes."""

    def __init__(self) -> None:
        self._state: dict[str, Any] = {
            "state": "idle",
            "provider": "autossh",
            "pid": None,
            "url": None,
            "started_at": None,
            "url_at": None,
            "restart_count": 0,
            "autossh_reconnects": 0,
            "last_error": None,
            "last_exit_code": None,
            "phase": "idle",
        }
        self._logs: list[str] = []
        self.start_calls = 0
        self.stop_calls = 0
        self.raise_locked_next_start = False
        self.raise_start_error: Exception | None = None
        self.raise_stop_error: Exception | None = None

    def status(self) -> dict[str, Any]:
        return dict(self._state)

    def logs_iter(self) -> Iterator[str]:
        return iter(list(self._logs))

    def start(self, cfg) -> dict[str, Any]:  # noqa: ANN001 — mirrors real cfg
        self.start_calls += 1
        if self.raise_start_error is not None:
            raise self.raise_start_error
        if self.raise_locked_next_start:
            self.raise_locked_next_start = False
            raise RuntimeError("locked")
        if self._state["state"] != "idle":
            raise RuntimeError("already_running")
        self._state.update(
            state="running",
            phase="running",
            pid=os.getpid(),
            url="https://abc123.a.pinggy.link",
            started_at="2026-08-22T00:00:00Z",
            url_at="2026-08-22T00:00:01Z",
        )
        self._logs.append("listening on 7420")
        self._logs.append("https://abc123.a.pinggy.link")
        return dict(self._state)

    def stop(self) -> dict[str, Any]:
        self.stop_calls += 1
        if self.raise_stop_error is not None:
            raise self.raise_stop_error
        if self._state["state"] == "idle":
            raise RuntimeError("not_running")
        self._state.update(
            state="idle",
            phase="idle",
            pid=None,
            url=None,
            started_at=None,
            url_at=None,
            restart_count=self._state.get("restart_count", 0) + 1,
        )
        return dict(self._state)

    def sweep_stale_lock(self, cfg=None) -> None:  # noqa: ANN001
        return None

    def push_log(self, line: str) -> None:
        self._logs.append(line)

    def force_state(self, **kw) -> None:
        self._state.update(**kw)


def _install_fake(app) -> FakeTunnelManager:
    fake = FakeTunnelManager()
    app.state.app_state.tunnel_manager = fake
    return fake


LOOPBACK = {"Host": "127.0.0.1"}
TUNNEL_HOST = {"Host": "abc123.a.pinggy.link"}


# ---- /capabilities is auth-free and truthful (TUN-4) ---------------------


def test_capabilities_returns_disabled_when_config_off(tmp_path: Path) -> None:
    client, _ = _client(tmp_path, enabled=False)
    r = client.get("/api/tunnel/capabilities", headers=LOOPBACK)
    assert r.status_code == 200
    body = r.json()
    assert body["enabled"] is False
    assert body["can_control"] is False
    assert body["reasons"] == ["disabled"]


def test_capabilities_returns_not_operator_for_stakeholder(tmp_path: Path) -> None:
    client, _ = _client(
        tmp_path, enabled=True, profile=PROFILE_STAKEHOLDER, token="t"
    )
    r = client.get(
        "/api/tunnel/capabilities",
        headers={**LOOPBACK, "Authorization": "Bearer t"},
    )
    assert r.status_code == 200
    body = r.json()
    assert body["enabled"] is True
    assert body["can_control"] is False
    assert body["reasons"] == ["not_operator"]


def test_capabilities_returns_not_loopback_from_tunnel_origin(tmp_path: Path) -> None:
    client, _ = _client(tmp_path, enabled=True)
    r = client.get("/api/tunnel/capabilities", headers=TUNNEL_HOST)
    assert r.status_code == 200
    body = r.json()
    assert body["can_control"] is False
    assert body["reasons"] == ["not_loopback"]


def test_capabilities_ok_when_all_gates_pass_and_binary_present(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.setattr("shutil.which", lambda name: "/usr/bin/" + name)
    client, _ = _client(tmp_path, enabled=True)
    r = client.get("/api/tunnel/capabilities", headers=LOOPBACK)
    assert r.status_code == 200
    body = r.json()
    assert body["enabled"] is True
    assert body["provider"] == "autossh"
    assert body["can_control"] is True
    assert body["reasons"] == []


def test_capabilities_flags_autossh_missing_when_binary_absent(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.setattr("shutil.which", lambda name: None)
    client, _ = _client(tmp_path, enabled=True)
    r = client.get("/api/tunnel/capabilities", headers=LOOPBACK)
    body = r.json()
    assert r.status_code == 200
    assert body["can_control"] is False
    assert "autossh_missing" in body["reasons"]


def test_capabilities_requires_no_token(tmp_path: Path) -> None:
    # Stakeholder profile with a token set — deliberately DON'T send it.
    client, _ = _client(
        tmp_path, enabled=False, profile=PROFILE_STAKEHOLDER, token="t"
    )
    r = client.get("/api/tunnel/capabilities", headers=LOOPBACK)
    assert r.status_code == 200
    assert r.json()["can_control"] is False


# ---- Config gate (404) short-circuits everything else --------------------


def test_status_404_when_disabled(tmp_path: Path) -> None:
    client, _ = _client(tmp_path, enabled=False)
    r = client.get("/api/tunnel/status", headers=LOOPBACK)
    assert r.status_code == 404


def test_start_404_when_disabled(tmp_path: Path) -> None:
    client, _ = _client(tmp_path, enabled=False)
    r = client.post("/api/tunnel/start", headers=LOOPBACK)
    assert r.status_code == 404


def test_stop_404_when_disabled(tmp_path: Path) -> None:
    client, _ = _client(tmp_path, enabled=False)
    r = client.post("/api/tunnel/stop", headers=LOOPBACK)
    assert r.status_code == 404


def test_logs_404_when_disabled(tmp_path: Path) -> None:
    client, _ = _client(tmp_path, enabled=False)
    r = client.get("/api/tunnel/logs", headers=LOOPBACK)
    assert r.status_code == 404


def test_gate_order_config_wins_over_host(tmp_path: Path) -> None:
    # enabled:false + non-loopback host → still 404, the tunnel's own
    # config gate short-circuits before the host gate runs (TUN-3 order).
    # Profile stays operator so the request reaches the tunnel gates at all;
    # middleware-layer profile enforcement is a separate axis tested above.
    client, _ = _client(tmp_path, enabled=False)
    r = client.post("/api/tunnel/start", headers=TUNNEL_HOST)
    assert r.status_code == 404


# ---- Profile gate (403) ---------------------------------------------------


def test_status_403_for_stakeholder(tmp_path: Path) -> None:
    client, app = _client(
        tmp_path, enabled=True, profile=PROFILE_STAKEHOLDER, token="t"
    )
    _install_fake(app)
    r = client.get(
        "/api/tunnel/status",
        headers={**LOOPBACK, "Authorization": "Bearer t"},
    )
    assert r.status_code == 403


def test_start_403_for_stakeholder(tmp_path: Path) -> None:
    client, app = _client(
        tmp_path, enabled=True, profile=PROFILE_STAKEHOLDER, token="t"
    )
    fake = _install_fake(app)
    r = client.post(
        "/api/tunnel/start",
        headers={**LOOPBACK, "Authorization": "Bearer t"},
    )
    assert r.status_code == 403
    assert fake.start_calls == 0


def test_stop_403_for_stakeholder(tmp_path: Path) -> None:
    client, app = _client(
        tmp_path, enabled=True, profile=PROFILE_STAKEHOLDER, token="t"
    )
    _install_fake(app)
    r = client.post(
        "/api/tunnel/stop",
        headers={**LOOPBACK, "Authorization": "Bearer t"},
    )
    assert r.status_code == 403


def test_logs_403_for_stakeholder(tmp_path: Path) -> None:
    client, app = _client(
        tmp_path, enabled=True, profile=PROFILE_STAKEHOLDER, token="t"
    )
    _install_fake(app)
    r = client.get(
        "/api/tunnel/logs",
        headers={**LOOPBACK, "Authorization": "Bearer t"},
    )
    assert r.status_code == 403


# ---- Host gate (403) — the load-bearing safety layer ---------------------


def test_status_403_from_tunnel_host(tmp_path: Path) -> None:
    client, app = _client(tmp_path, enabled=True)
    _install_fake(app)
    r = client.get("/api/tunnel/status", headers=TUNNEL_HOST)
    assert r.status_code == 403


def test_start_403_from_tunnel_host(tmp_path: Path) -> None:
    client, app = _client(tmp_path, enabled=True)
    fake = _install_fake(app)
    r = client.post("/api/tunnel/start", headers=TUNNEL_HOST)
    assert r.status_code == 403
    assert fake.start_calls == 0


def test_stop_403_from_tunnel_host(tmp_path: Path) -> None:
    client, app = _client(tmp_path, enabled=True)
    _install_fake(app)
    r = client.post("/api/tunnel/stop", headers=TUNNEL_HOST)
    assert r.status_code == 403


def test_logs_403_from_tunnel_host(tmp_path: Path) -> None:
    # TUN-10 scenario: SSE MUST refuse from the tunnel origin, no frames
    # emitted. The load-bearing safety pin.
    client, app = _client(tmp_path, enabled=True)
    _install_fake(app)
    r = client.get("/api/tunnel/logs", headers=TUNNEL_HOST)
    assert r.status_code == 403


def test_host_gate_ignores_x_forwarded_for(tmp_path: Path) -> None:
    # Non-negotiable: XFF must NEVER unlock the loopback gate.
    client, app = _client(tmp_path, enabled=True)
    _install_fake(app)
    r = client.post(
        "/api/tunnel/start",
        headers={"Host": "abc123.a.pinggy.link", "X-Forwarded-For": "127.0.0.1"},
    )
    assert r.status_code == 403


# ---- Happy path -----------------------------------------------------------


def test_status_returns_idle_snapshot_on_fresh_boot(tmp_path: Path) -> None:
    client, app = _client(tmp_path, enabled=True)
    _install_fake(app)
    r = client.get("/api/tunnel/status", headers=LOOPBACK)
    assert r.status_code == 200
    body = r.json()
    assert body["state"] == "idle"
    assert body["url"] is None
    assert body["pid"] is None


def test_start_returns_202_and_status_reflects_running(tmp_path: Path) -> None:
    client, app = _client(tmp_path, enabled=True)
    fake = _install_fake(app)
    r = client.post("/api/tunnel/start", headers=LOOPBACK)
    assert r.status_code == 202
    body = r.json()
    assert body["state"] == "running"
    assert body["url"] == "https://abc123.a.pinggy.link"
    assert fake.start_calls == 1

    r2 = client.get("/api/tunnel/status", headers=LOOPBACK)
    assert r2.status_code == 200
    assert r2.json()["state"] == "running"


def test_stop_returns_202_and_status_returns_to_idle(tmp_path: Path) -> None:
    client, app = _client(tmp_path, enabled=True)
    fake = _install_fake(app)
    client.post("/api/tunnel/start", headers=LOOPBACK)
    r = client.post("/api/tunnel/stop", headers=LOOPBACK)
    assert r.status_code == 202
    assert r.json()["state"] == "idle"
    assert fake.stop_calls == 1


# ---- Conflict semantics (TUN-6, 409) -------------------------------------


def test_double_start_returns_409_already_running(tmp_path: Path) -> None:
    client, app = _client(tmp_path, enabled=True)
    _install_fake(app)
    client.post("/api/tunnel/start", headers=LOOPBACK)
    r = client.post("/api/tunnel/start", headers=LOOPBACK)
    assert r.status_code == 409
    body = r.json()
    assert body["error"] == "already_running"
    assert body["state"] == "running"


def test_start_returns_409_locked_when_manager_signals_lock(tmp_path: Path) -> None:
    client, app = _client(tmp_path, enabled=True)
    fake = _install_fake(app)
    fake.raise_locked_next_start = True
    r = client.post("/api/tunnel/start", headers=LOOPBACK)
    assert r.status_code == 409
    assert r.json()["error"] == "locked"


def test_stop_while_idle_returns_409_not_running(tmp_path: Path) -> None:
    client, app = _client(tmp_path, enabled=True)
    _install_fake(app)
    r = client.post("/api/tunnel/stop", headers=LOOPBACK)
    assert r.status_code == 409
    assert r.json()["error"] == "not_running"


# ---- SSE /logs stream ----------------------------------------------------


def test_logs_streams_redacted_buffer(tmp_path: Path) -> None:
    client, app = _client(tmp_path, enabled=True)
    fake = _install_fake(app)
    fake.push_log("listening on 7420")
    fake.push_log("https://abc123.a.pinggy.link")
    # Force state to idle so the stream closes after replay instead of
    # blocking on the live-tail deadline for 30 min.
    fake.force_state(state="idle", phase="idle")
    with client.stream("GET", "/api/tunnel/logs", headers=LOOPBACK) as resp:
        assert resp.status_code == 200
        assert resp.headers["content-type"].startswith("text/event-stream")
        chunks = list(resp.iter_text())
    body = "".join(chunks)
    assert "data: listening on 7420" in body
    assert "data: https://abc123.a.pinggy.link" in body
    assert "event: idle" in body


def test_logs_closes_cleanly_after_idle_cutoff(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    # Shrink the D3 cutoff to a tiny value so we don't wait 30 minutes. We
    # patch the module-level constant AND the running state so the stream
    # enters the live-tail phase (state != idle) and then trips the deadline.
    from orchestrator.dashboard import server as srv

    monkeypatch.setattr(srv, "TUNNEL_SSE_IDLE_CUTOFF_S", 0.05)
    client, app = _client(tmp_path, enabled=True)
    fake = _install_fake(app)
    fake.force_state(state="running", phase="running")
    with client.stream("GET", "/api/tunnel/logs", headers=LOOPBACK) as resp:
        assert resp.status_code == 200
        body = "".join(list(resp.iter_text()))
    assert "retry: 5000" in body


# ---- Config × host matrix on the operator profile ------------------------
# Profile is held constant at operator so the tunnel's own gates run in
# their fixed order. Middleware-layer profile enforcement is exercised in
# the dedicated stakeholder tests above.


@pytest.mark.parametrize("enabled", [True, False])
@pytest.mark.parametrize("host_ok", [True, False])
def test_start_config_host_matrix(
    tmp_path: Path, enabled: bool, host_ok: bool
) -> None:
    client, app = _client(tmp_path, enabled=enabled, profile=PROFILE_OPERATOR)
    if app.state.app_state.tunnel_manager is not None:
        _install_fake(app)
    headers = {"Host": "127.0.0.1" if host_ok else "abc123.a.pinggy.link"}
    r = client.post("/api/tunnel/start", headers=headers)

    if not enabled:
        assert r.status_code == 404
        return
    if not host_ok:
        assert r.status_code == 403
        return
    assert r.status_code == 202
