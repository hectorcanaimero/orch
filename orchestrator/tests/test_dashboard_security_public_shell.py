"""The SPA shell is public; every data route stays behind the token.

G0.3 follow-up. Before this, `TokenAuthMiddleware` 401'd `index.html` and
`/assets/*` too, which made the stakeholder URL unusable in a browser:

    - bare URL            → plain-text 401, no UI at all
    - URL with `?token=X` → shell 200 (the query param is accepted), but the
                            browser does not carry the query over to
                            `<script src="/assets/...">` → 401 → blank page

so the SPA's own token form could never paint. The fix serves the static
shell without a token. The point of this file is to prove that nothing else
came along for the ride.

The sweep at the bottom walks the LIVE route table rather than a hand-written
list: a new `/api/*` endpoint added later is covered the day it is registered.
"""

from __future__ import annotations

import json
from pathlib import Path

import pytest


TOKEN = "s3cr3t"

# Routes that exist to serve the static shell. Everything else in the route
# table is data and must 401 without a token.
_PUBLIC_ROUTE_NAMES = frozenset({"spa"})


def _make_fixture_project(tmp_path: Path):
    """A project WITH a built SPA, so the StaticFiles mount is registered.

    The other dashboard security suites deliberately run without a build (the
    mount is absent there). This one needs it: the whole behaviour under test
    is "the request resolved to the SPA mount".
    """
    from orchestrator.paths import ProjectPaths

    root = tmp_path / "proj"
    (root / ".orchestrator" / "state").mkdir(parents=True)
    (root / "scripts").mkdir(parents=True)
    (root / "scripts" / "task-start.sh").write_text("#!/bin/sh\nexit 0\n")

    (root / "tasks.json").write_text(
        json.dumps({
            "phases": [{"id": 1, "name": "smoke"}],
            "tasks": [
                {"id": "T-A", "phase": 1, "title": "A", "description": "",
                 "model": "opencode-go/glm-5.1", "reason": "", "status": "done",
                 "dependencies": [], "estimateHours": 0.5, "files": [],
                 "specRef": "", "comments": []},
            ],
        }),
        encoding="utf-8",
    )

    dist = root / "frontend" / "dist"
    (dist / "assets").mkdir(parents=True)
    (dist / "index.html").write_text(
        "<!doctype html><html><head>"
        '<script type="module" src="/assets/index.js"></script>'
        "</head><body><div id=root></div></body></html>",
        encoding="utf-8",
    )
    (dist / "assets" / "index.js").write_text("console.log('orch')\n", encoding="utf-8")
    (dist / "favicon.svg").write_text("<svg/>", encoding="utf-8")

    return ProjectPaths(
        project_root=root,
        project_id="proj",
        config_yaml=root / ".orchestrator" / "config.yaml",
        explicit_root=True,
        state_layout="legacy",
    )


def _client(tmp_path: Path, **override):
    pytest.importorskip("fastapi")
    from fastapi.testclient import TestClient
    from orchestrator.dashboard.server import create_app

    app = create_app(
        paths=_make_fixture_project(tmp_path),
        profile_override=override.get("profile"),
        token_override=override.get("token"),
    )
    return TestClient(app)


def _stakeholder(tmp_path: Path):
    return _client(tmp_path, profile="stakeholder", token=TOKEN)


# ---- The shell loads without a token --------------------------------------


@pytest.mark.parametrize("path", ["/", "/index.html", "/kanban", "/stakeholder"])
def test_spa_shell_is_served_without_a_token(tmp_path: Path, path: str) -> None:
    """Both the root and every React Router client route return the shell.

    `/stakeholder` is the URL the operator actually shares; it must render,
    not 401.
    """
    r = _stakeholder(tmp_path).get(path)
    assert r.status_code == 200, f"{path} → {r.status_code} ({r.text[:120]})"
    assert "text/html" in r.headers.get("content-type", "").lower()
    assert "<div id=root>" in r.text


@pytest.mark.parametrize("path", ["/assets/index.js", "/favicon.svg"])
def test_spa_bundle_is_served_without_a_token(tmp_path: Path, path: str) -> None:
    """Without this the shell loads and then paints nothing."""
    r = _stakeholder(tmp_path).get(path)
    assert r.status_code == 200, f"{path} → {r.status_code}"


def test_shared_url_with_query_token_serves_shell_and_bundle(tmp_path: Path) -> None:
    """The end-to-end shape of the flow the README sells: send one URL.

    The shell comes back for `/?token=…`, and the bundle it references comes
    back for a request that carries NO token at all — which is exactly what
    the browser issues for `<script src>`.
    """
    client = _stakeholder(tmp_path)
    shell = client.get(f"/?token={TOKEN}")
    assert shell.status_code == 200
    assert "<div id=root>" in shell.text
    assert client.get("/assets/index.js").status_code == 200


def test_missing_asset_is_404_not_401(tmp_path: Path) -> None:
    """A probing client gets an honest 404, not a confusing auth error."""
    r = _stakeholder(tmp_path).get("/assets/does-not-exist.js")
    assert r.status_code == 404


# ---- …and the data surface did not come along -----------------------------


@pytest.mark.parametrize(
    "path",
    [
        "/api/tasks",
        "/api/summary",
        "/api/whoami",
        "/api/config",
        "/api/metrics",
        "/api/doctor",
        "/api/events",
        "/stakeholder/summary",
        "/snapshot",
        "/logs/stream",
        "/api/events/stream",
    ],
)
def test_data_routes_still_require_a_token(tmp_path: Path, path: str) -> None:
    r = _stakeholder(tmp_path).get(path)
    assert r.status_code == 401, f"{path} → {r.status_code} without a token"
    assert r.text == "unauthorized"


def test_method_fuzz_on_an_api_route_is_not_treated_as_the_shell(
    tmp_path: Path,
) -> None:
    """`POST /api/tasks` must not fall through to the SPA mount and go public.

    The SPA mount FULL-matches every path, so this is the trap the exemption
    could have opened. `_route_name` prefers the PARTIAL match on the real
    route, and `/api/` is in the never-public prefix list on top of that.
    """
    r = _stakeholder(tmp_path).post("/api/tasks")
    assert r.status_code == 401


def test_unknown_api_path_is_not_public(tmp_path: Path) -> None:
    """No named route matches `/api/invented`, so route resolution alone would
    hand it to the SPA mount. The `/api/` prefix backstop catches it."""
    r = _stakeholder(tmp_path).get("/api/invented")
    assert r.status_code == 401


# ---- Sweep: every registered data route, from the live route table --------


def _concrete_path(path: str) -> str:
    """Turn `/api/task/{task_id}` into `/api/task/x` so it can be requested."""
    out: list[str] = []
    for segment in path.split("/"):
        if segment.startswith("{") and segment.endswith("}"):
            out.append("x")
        else:
            out.append(segment)
    return "/".join(out)


def test_every_registered_data_route_401s_without_a_token(tmp_path: Path) -> None:
    """Walk the app's own route table — no hand-maintained list to drift.

    Any route that is not the SPA mount serves data or server metadata and
    must be gated. If someone adds an endpoint that slips past the token
    check, this fails on the next run.
    """
    pytest.importorskip("fastapi")
    from fastapi.testclient import TestClient
    from orchestrator.dashboard.server import create_app

    app = create_app(
        paths=_make_fixture_project(tmp_path),
        profile_override="stakeholder",
        token_override=TOKEN,
    )
    client = TestClient(app)

    checked: list[str] = []
    for route in app.routes:
        name = getattr(route, "name", None)
        raw_path = getattr(route, "path", None)
        if not name or not raw_path or name in _PUBLIC_ROUTE_NAMES:
            continue
        # Sprint E-5: capabilities is auth-free by design so the SPA can
        # decide whether to render the tunnel panel before asking for a token.
        if name == "api_tunnel_capabilities":
            continue
        path = _concrete_path(raw_path)
        methods = getattr(route, "methods", None) or {"GET"}
        method = "GET" if "GET" in methods else sorted(methods)[0]
        r = client.request(method, path)
        assert r.status_code == 401, (
            f"{method} {path} (route {name!r}) returned {r.status_code} "
            "without a token — it escaped the gate"
        )
        checked.append(f"{method} {path}")

    # Guard against the sweep silently checking nothing (e.g. if the route
    # table shape changes and every route gets skipped).
    assert len(checked) >= 20, f"sweep only covered {len(checked)} routes: {checked}"


# ---- Operator profile is untouched ----------------------------------------


@pytest.mark.parametrize("path", ["/", "/assets/index.js", "/api/tasks", "/snapshot"])
def test_operator_profile_needs_no_token_for_anything(
    tmp_path: Path, path: str
) -> None:
    r = _client(tmp_path).get(path)
    assert r.status_code not in (401, 403), f"{path} rejected in operator mode"


def test_both_mode_keeps_the_stakeholder_json_gated(tmp_path: Path) -> None:
    """`both` mode only gates `/stakeholder/*`; the exemption must not open
    the one JSON route living under that prefix."""
    client = _client(tmp_path, profile="both", token=TOKEN)
    assert client.get("/stakeholder/summary").status_code == 401
    assert client.get(f"/stakeholder/summary?token={TOKEN}").status_code == 200
    # The shell at that prefix is still reachable — it is what renders the UI.
    assert client.get("/stakeholder").status_code == 200
