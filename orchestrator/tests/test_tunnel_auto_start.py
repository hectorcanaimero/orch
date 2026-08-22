"""Tests for the tunnel `auto_start` startup handler — Sprint E-5 (TUN-9).

Covers:
- No-op when `tunnel.enabled: false`.
- No-op when `enabled: true` but `auto_start: false`.
- Successful self-probe → `TunnelManager.start()` is called.
- Failed self-probe (all retries) → NO start, stderr message present,
  dashboard boots healthy.
- Retry count: probe attempted exactly N times before giving up.

The handler is registered on startup, so tests drive it via
`with TestClient(app):` which triggers `@app.on_event("startup")`. Blocking
work inside the handler runs through `asyncio.to_thread` — the probe and
`manager.start` calls we monkeypatch are the sync functions that end up
inside those threads.
"""

from __future__ import annotations

import json
import sys
import threading
import time
from pathlib import Path
from typing import Any, Iterator

import pytest


pytest.importorskip("fastapi")
from fastapi.testclient import TestClient  # noqa: E402


# ---- Fixture project ------------------------------------------------------


def _scaffold(
    tmp_path: Path,
    *,
    enabled: bool,
    auto_start: bool,
    startup_probe_timeout_s: int = 3,
) -> Path:
    root = tmp_path / "proj"
    (root / "orchestrator" / "state").mkdir(parents=True)
    (root / "scripts").mkdir(parents=True)
    (root / "scripts" / "task-start.sh").write_text("#!/bin/sh\nexit 0\n")
    (root / "tasks.json").write_text(
        json.dumps({"meta": {}, "phases": [], "tasks": []}),
        encoding="utf-8",
    )
    (root / "orchestrator" / "config.yaml").write_text("", encoding="utf-8")
    dash = ["tunnel:", f"  enabled: {'true' if enabled else 'false'}"]
    if enabled:
        dash += [
            "  provider: autossh",
            "  command: autossh",
            f"  auto_start: {'true' if auto_start else 'false'}",
            f"  startup_probe_timeout_s: {startup_probe_timeout_s}",
        ]
    (root / "dashboard.yaml").write_text("\n".join(dash) + "\n", encoding="utf-8")
    return root


class RecordingManager:
    """In-memory stand-in — records `start(cfg)` calls so tests can assert."""

    def __init__(self) -> None:
        self.start_calls: list[Any] = []
        self.raise_error: Exception | None = None
        self._state: dict[str, Any] = {"state": "idle"}
        self._event = threading.Event()

    def start(self, cfg) -> dict[str, Any]:  # noqa: ANN001 — mirrors real signature
        self.start_calls.append(cfg)
        self._event.set()
        if self.raise_error is not None:
            raise self.raise_error
        self._state = {"state": "running", "pid": 1234}
        return dict(self._state)

    def stop(self) -> dict[str, Any]:  # noqa: D401
        self._state = {"state": "idle"}
        return dict(self._state)

    def status(self) -> dict[str, Any]:
        return dict(self._state)

    def logs_iter(self) -> Iterator[str]:
        return iter([])

    def sweep_stale_lock(self, cfg=None) -> None:  # noqa: ANN001
        return None

    def wait_for_start(self, timeout: float = 2.0) -> bool:
        return self._event.wait(timeout=timeout)


def _make_client(
    tmp_path: Path,
    *,
    enabled: bool,
    auto_start: bool,
    probe_port: int | None = 7420,
    startup_probe_timeout_s: int = 3,
):
    from orchestrator.dashboard.server import create_app
    from orchestrator.paths import ProjectPaths

    root = _scaffold(
        tmp_path,
        enabled=enabled,
        auto_start=auto_start,
        startup_probe_timeout_s=startup_probe_timeout_s,
    )
    paths = ProjectPaths(
        project_root=root,
        project_id="proj",
        config_yaml=root / "orchestrator" / "config.yaml",
        explicit_root=True,
        state_layout="legacy",
    )
    app = create_app(paths=paths, probe_port=probe_port)
    return app


def _install_fake_manager(app) -> RecordingManager:
    fake = RecordingManager()
    app.state.app_state.tunnel_manager = fake
    return fake


# ---- Test cases -----------------------------------------------------------


def test_auto_start_noop_when_tunnel_disabled(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    calls: list[int] = []

    def _probe(port: int, timeout_s: float) -> bool:
        calls.append(port)
        return True

    from orchestrator.dashboard import server as _srv

    monkeypatch.setattr(_srv, "_tunnel_self_probe", _probe)

    app = _make_client(tmp_path, enabled=False, auto_start=False)
    # No tunnel_manager attribute-set path — enabled:false leaves it None.
    with TestClient(app) as client:
        client.get("/api/tunnel/capabilities")
        # Give the loop a beat in case a stray task is scheduled.
        time.sleep(0.05)
    assert calls == []


def test_auto_start_noop_when_auto_start_false(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    calls: list[int] = []

    def _probe(port: int, timeout_s: float) -> bool:
        calls.append(port)
        return True

    from orchestrator.dashboard import server as _srv

    monkeypatch.setattr(_srv, "_tunnel_self_probe", _probe)

    app = _make_client(tmp_path, enabled=True, auto_start=False)
    fake = _install_fake_manager(app)
    with TestClient(app):
        time.sleep(0.05)
    assert fake.start_calls == []
    assert calls == []


def test_auto_start_spawns_when_probe_succeeds(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    probe_calls: list[tuple[int, float]] = []

    def _probe(port: int, timeout_s: float) -> bool:
        probe_calls.append((port, timeout_s))
        return True

    from orchestrator.dashboard import server as _srv

    monkeypatch.setattr(_srv, "_tunnel_self_probe", _probe)

    app = _make_client(
        tmp_path, enabled=True, auto_start=True, probe_port=7420,
        startup_probe_timeout_s=5,
    )
    fake = _install_fake_manager(app)
    with TestClient(app):
        assert fake.wait_for_start(timeout=2.0), (
            f"start() was never called; probe_calls={probe_calls}"
        )
    assert probe_calls == [(7420, 5.0)]
    assert len(fake.start_calls) == 1
    cfg = fake.start_calls[0]
    assert cfg.provider == "autossh"
    assert cfg.command == "autossh"


def test_auto_start_skips_when_probe_fails(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture,
) -> None:
    def _probe(port: int, timeout_s: float) -> bool:
        return False

    from orchestrator.dashboard import server as _srv

    monkeypatch.setattr(_srv, "_tunnel_self_probe", _probe)

    app = _make_client(tmp_path, enabled=True, auto_start=True)
    fake = _install_fake_manager(app)
    with TestClient(app) as client:
        time.sleep(0.15)
        # Dashboard MUST still be healthy — capabilities returns 200.
        r = client.get(
            "/api/tunnel/capabilities", headers={"Host": "127.0.0.1"}
        )
        assert r.status_code == 200
    assert fake.start_calls == []
    err = capsys.readouterr().err
    assert "auto_start_skipped" in err
    assert "self_probe_failed" in err


def test_auto_start_skips_when_probe_port_unknown(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture,
) -> None:
    def _probe(port: int, timeout_s: float) -> bool:
        return True

    from orchestrator.dashboard import server as _srv

    monkeypatch.setattr(_srv, "_tunnel_self_probe", _probe)

    app = _make_client(tmp_path, enabled=True, auto_start=True, probe_port=None)
    fake = _install_fake_manager(app)
    with TestClient(app):
        time.sleep(0.05)
    assert fake.start_calls == []
    err = capsys.readouterr().err
    assert "auto_start_skipped" in err
    assert "probe port unknown" in err


def test_probe_retries_exactly_n_times_before_giving_up(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """`_tunnel_self_probe` MUST call urlopen exactly N times."""
    from orchestrator.dashboard import server as _srv
    from urllib.error import URLError

    attempts: list[str] = []

    def _fake_urlopen(url, timeout=None):  # noqa: ANN001
        attempts.append(url)
        raise URLError("boom")

    # Zero the retry gap so the whole loop runs instantly.
    monkeypatch.setattr(_srv, "TUNNEL_AUTO_START_PROBE_GAP_S", 0.0)
    monkeypatch.setattr(_srv, "TUNNEL_AUTO_START_PROBE_RETRIES", 3)
    monkeypatch.setattr("orchestrator.dashboard.server.urlopen", _fake_urlopen, raising=False)
    # `urlopen` is imported inside the helper — patch the imported symbol
    # via urllib.request as well so the local `from urllib.request import
    # urlopen` inside the function resolves to our fake.
    import urllib.request as _urlreq

    monkeypatch.setattr(_urlreq, "urlopen", _fake_urlopen)

    assert _srv._tunnel_self_probe(port=9999, timeout_s=0.1) is False
    assert len(attempts) == 3


def test_probe_returns_true_on_first_success(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    from orchestrator.dashboard import server as _srv
    import urllib.request as _urlreq

    class _FakeResp:
        status = 200

        def __enter__(self):
            return self

        def __exit__(self, *a):
            return False

        def getcode(self):
            return 200

    attempts: list[str] = []

    def _fake_urlopen(url, timeout=None):  # noqa: ANN001
        attempts.append(url)
        return _FakeResp()

    monkeypatch.setattr(_urlreq, "urlopen", _fake_urlopen)
    monkeypatch.setattr(_srv, "TUNNEL_AUTO_START_PROBE_RETRIES", 3)

    assert _srv._tunnel_self_probe(port=7420, timeout_s=1.0) is True
    # Should short-circuit on the first success.
    assert len(attempts) == 1


def test_auto_start_swallows_manager_start_errors(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture,
) -> None:
    """If probe succeeds but manager.start raises, startup MUST NOT fail."""

    def _probe(port: int, timeout_s: float) -> bool:
        return True

    from orchestrator.dashboard import server as _srv

    monkeypatch.setattr(_srv, "_tunnel_self_probe", _probe)

    app = _make_client(tmp_path, enabled=True, auto_start=True)
    fake = _install_fake_manager(app)
    fake.raise_error = RuntimeError("locked")

    with TestClient(app) as client:
        assert fake.wait_for_start(timeout=2.0)
        # Dashboard survived the exception.
        r = client.get(
            "/api/tunnel/capabilities", headers={"Host": "127.0.0.1"}
        )
        assert r.status_code == 200
    err = capsys.readouterr().err
    assert "auto_start_skipped" in err
