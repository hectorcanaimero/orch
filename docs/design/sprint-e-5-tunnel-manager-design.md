# Design: Sprint E-5 — Tunnel Manager

> Status: **draft** · Backend: docs (engram unavailable) · Depends on: proposal `sprint-e-5-tunnel-manager.md`, precedent `orch arch generate` (Sprint E-4)

## Technical Approach

Mirror the Sprint E-4 arch pattern (`server.py:821-963`, `orchestrator/arch.py`) with one hard divergence: the tunnel is a **long-lived** supervised subprocess bound to the dashboard process, not a fire-and-forget one-shot. Encapsulate the supervisor in a new `orchestrator/dashboard/tunnel/` package with the same shape as `arch.py` (module-level pure functions for lock/state/discovery, plus one class for the running child). `server.py` registers `/api/tunnel/*` routes that delegate to the manager and evaluate two FastAPI `Depends` gates before any state mutation.

## Architecture Decisions

| # | Decision | Alternatives | Rationale |
|---|----------|--------------|-----------|
| 1 | Package `orchestrator/dashboard/tunnel/` with `manager.py`, `providers.py`, `deps.py` | Single file next to `arch.py`; class hierarchy | Shape matches E-4 without collapsing into it. Providers are pure regex; deps are pure FastAPI callables; manager owns Popen. |
| 2 | Provider is `subprocess.Popen(..., start_new_session=True, stdout=PIPE, stderr=STDOUT)` | `asyncio.create_subprocess_exec`; detached daemon | Same idiom as `arch.py:949` and `dispatcher.py`. `start_new_session` isolates the process group so SIGTERM to dashboard doesn't nuke tunnel — we send the signal explicitly via `os.killpg`. |
| 3 | Stdout consumed on a **daemon `threading.Thread`** | asyncio task in event loop; polling | FastAPI+uvicorn runs the loop; blocking a coroutine on `readline()` would stall. Thread reads line-by-line, appends to a `collections.deque(maxlen=500)` under `threading.Lock`, applies provider regex, updates `state.json` atomically. Dies when child exits. |
| 4 | URL regex lives in `providers.py` per-provider (`pinggy` v1); URL cached in in-memory state + `state.json` | Parse stdout on every request | Extract once, invalidate on subprocess restart. Aligns with proposal "URL parsed from stdout." |
| 5 | Lock = `state/tunnel/lock` JSON `{pid, started_at, provider}`; same PID-alive sweep as `arch.py:142` | Reuse `arch-generate.lock` | Separate concerns — arch is one-shot, tunnel is long-lived. Startup reconciler sweeps stale lock AND kills orphan PID before accepting `/start`. |
| 6 | Gates as **FastAPI `Depends`**, not middleware | Extend `ProfileGuardMiddleware` | Middleware is stakeholder-mode-only; tunnel gate must run even in operator mode. Dependency-injection also composes naturally with route names and returns typed 403/404. |
| 7 | Gate order: `require_loopback_host` → `require_operator_profile` → `require_tunnel_enabled` (route body) | Reverse | Loopback rejection is 403 (not a hint), profile rejection is 403, disabled feature is 404 (proposal §Success Criteria). Evaluate cheapest+most-secret-hiding first. |
| 8 | `auto_start` runs from FastAPI **`@app.on_event("startup")` background task**, not sync in startup | Sync in startup; first-request lazy | Uvicorn's `serve()` binds the socket before firing startup events. A background task inside startup lets us self-probe `GET http://127.0.0.1:{port}/` with a 3s timeout; skip+log on failure. |
| 9 | SPA panel **not mounted at all** when `/api/tunnel/capabilities` returns `can_control:false` | CSS `hidden`; disabled buttons | Proposal is explicit — no CSS-hidden UI. `TunnelPanel` mounted from `BoardPage.tsx` behind `useTunnelCapabilities().data?.can_control`. |
| 10 | `restart_count` resets on **manual `/stop` and on dashboard restart**; increments only on autossh-driven reconnect | Persist forever | It's a diagnostic, not billing. Manual intent = clean slate. |

## Data Flow

```
Browser ── POST /api/tunnel/start ──► FastAPI
                                        │
                                        ├─ Depends: require_loopback_host  → 403 if not 127.0.0.1|localhost|::1
                                        ├─ Depends: require_operator_profile → 403 if stakeholder
                                        ├─ Depends: require_tunnel_enabled   → 404 if disabled
                                        ▼
                                  TunnelManager.start()
                                        │ acquire_lock() → Popen(autossh) → spawn reader thread
                                        ▼
                            stdout ── reader thread ── regex ── state.json + in-mem
                                        │
GET /api/tunnel/status ─────────────────┴──► JSON snapshot
GET /api/tunnel/logs (SSE) ─────────────────► tail deque
```

## File Changes

| File | Action | Description |
|------|--------|-------------|
| `orchestrator/dashboard/tunnel/__init__.py` | Create | Public re-exports (`TunnelManager`, `read_state`, gate deps). |
| `orchestrator/dashboard/tunnel/manager.py` | Create | `TunnelManager` class; `start/stop/status/logs_iter`; lock/state helpers (mirror `arch.py`). |
| `orchestrator/dashboard/tunnel/providers.py` | Create | `PROVIDERS = {"autossh": ProviderSpec(argv_template, url_regex)}`; `resolve_provider(cfg)`. |
| `orchestrator/dashboard/tunnel/deps.py` | Create | `require_loopback_host(request)`, `require_operator_profile(request)`, `require_tunnel_enabled(request)`. |
| `orchestrator/dashboard/server.py` | Modify | Register `/api/tunnel/{status,capabilities,start,stop,logs}`; instantiate manager in `AppState`; wire `on_event("startup")` for `auto_start`. |
| `orchestrator/dashboard/dashboard_config.py` | Modify | Parse `tunnel:` block from `dashboard.yaml` into `TunnelConfig` dataclass. |
| `orchestrator/dashboard/dashboard.yaml` | Modify | Add commented `tunnel:` defaults (`enabled: false`, `provider: autossh`, `auto_start: false`). |
| `orchestrator/doctor.py` | Modify | Add `autossh` binary check + tunnel config validation. |
| `frontend/src/pages/TunnelPanel.tsx` | Create | Panel component; mounted from `BoardPage.tsx`. |
| `frontend/src/hooks/useTunnelStatus.ts` | Create | React Query hook, 3s poll idle / 1s poll while `state=starting|running`. |
| `frontend/src/hooks/useTunnelCapabilities.ts` | Create | One-shot capability probe, drives conditional mount. |
| `frontend/src/hooks/useTunnelLogs.ts` | Create | SSE consumer (mirror `useLiveLogs.ts`). |
| `frontend/src/lib/api.ts` | Modify | `getTunnelStatus/Capabilities`, `startTunnel`, `stopTunnel`. |
| `frontend/src/lib/types.ts` | Modify | `TunnelState`, `TunnelStatus`, `TunnelCapabilities` DTOs. |
| `orchestrator/tests/test_tunnel_manager.py` | Create | Manager unit tests with monkeypatched `Popen`. |
| `orchestrator/tests/test_tunnel_providers.py` | Create | Regex extraction per provider. |
| `orchestrator/tests/test_tunnel_deps.py` | Create | Gate matrix. |
| `orchestrator/tests/test_tunnel_endpoints.py` | Create | FastAPI TestClient — status transitions + gate integration. |

## Interfaces / Contracts

### `dashboard.yaml → tunnel:`
```yaml
tunnel:
  enabled: false          # feature flag; false → all routes 404
  provider: autossh       # allowlist: {"autossh"} in v1
  auto_start: false       # spawn on dashboard boot after self-probe
  args:                   # provider-specific; validated against provider schema
    remote: "a.pinggy.io"
    port: 443
    local_port: 7420
```

### `state/tunnel/state.json`
```json
{
  "state": "idle|starting|running|stopping|failed",
  "provider": "autossh",
  "pid": 12345,
  "started_at": "2026-08-22T14:00:00Z",
  "url": "https://xxx.a.pinggy.link",
  "url_at": "2026-08-22T14:00:04Z",
  "restart_count": 0,
  "last_error": null,
  "last_exit_code": null
}
```
**Invariants**: written atomically via `tmp + os.replace`; `state` transitions are one-way except `running→failed` and `*→stopping→idle`; `pid` present iff `state ∈ {starting, running, stopping}`.

### `state/tunnel/lock`
Same shape as `state/arch-generate.lock` (`{pid, started_at, phase}`) so the PID-alive helper in `arch.py` is reusable — factor `_pid_alive` up if needed, else duplicate (~10 LOC).

### `state/tunnel/stdout.log`
Full log file, append-only, no rotation in v1 (autossh is quiet; ~KB/hour). Redaction pass strips `Authorization:` header echoes and any token that matches `[A-Za-z0-9_-]{32,}` before writing. In-memory `deque(maxlen=500)` mirrors the tail for SSE.

### Gate signatures (deps.py)
```python
def require_loopback_host(request: Request) -> None:
    host = (request.headers.get("host") or "").split(":")[0].lower()
    if host not in {"127.0.0.1", "localhost", "::1", "[::1]"}:
        raise HTTPException(403, "loopback only")

def require_operator_profile(request: Request) -> None:
    cfg = request.app.state.app_state.config
    if cfg.profile != PROFILE_OPERATOR:
        raise HTTPException(403, "operator only")

def require_tunnel_enabled(request: Request) -> None:
    tcfg = request.app.state.app_state.config.tunnel
    if not tcfg.enabled:
        raise HTTPException(404, "not found")
```

### `/api/tunnel/capabilities` (no side effects)
```json
{"can_control": false, "reasons": ["not_loopback"|"not_operator"|"disabled"|"autossh_missing"]}
```

## Subprocess Lifecycle

```
                          ┌─────────┐
     ┌───────────────────►│  idle   │◄────────────┐
     │                    └────┬────┘             │
     │                    /start (POST 202)       │
     │                         ▼                  │
     │                    ┌─────────┐             │
     │                    │starting │             │
     │                    └────┬────┘             │
     │                     Popen ok                │
     │                         ▼                  │
     │              ┌──────────────────┐          │
     │              │      running     │          │
     │              │ (reader tailing) │          │
     │              └────┬───────┬─────┘          │
     │        child exit │       │ /stop          │
     │       (unexpected)│       ▼                │
     │                   ▼   ┌─────────┐          │
     │             ┌────────┐│stopping │──killpg──┤
     │             │ failed ││         │          │
     │             └───┬────┘└────┬────┘          │
     │                 │          ▼               │
     └── manual /stop ─┴──────────┴───────────────┘
```

## Startup Sequence for `auto_start`

```
uvicorn.run() ─► socket.bind(:7420)
              ─► asgi startup event ─► asyncio.create_task(_auto_start_tunnel())
              ─► serve loop begins
                                       │
                                       │ (background, in loop)
                                       ▼
                                  await asyncio.sleep(0.5)  # let serve accept
                                  probe = httpx.get("http://127.0.0.1:7420/", timeout=3)
                                  if probe.ok: manager.start()
                                  else: log.warning("auto_start skipped: self-probe failed")
```
The startup **event** kicks the task, but the task itself sleeps briefly so the serve loop is unquestionably accepting before we probe. If probe fails we log to stderr and skip — dashboard still boots.

## Testing Strategy

| Layer | What to Test | Approach |
|-------|--------------|----------|
| Unit — providers | pinggy URL extraction from real captured stdout | Fixture strings → assert regex hit |
| Unit — manager | start/stop transitions; stale lock sweep; restart counter; atomic state write | Monkeypatch `subprocess.Popen` with a fake yielding controlled stdout; assert `state.json` progression |
| Unit — deps | Gate matrix (loopback×profile×enabled = 8 cases) | Bare `Request` fixture; call dependency directly |
| Integration | Gate composition on real routes; 202/403/404/409 status codes; capability endpoint reflects gate state | `TestClient` + monkeypatched manager |
| Integration | SSE `/logs` streams the same deque and closes on stop | `TestClient.stream()` |
| **NOT tested** | Real autossh spawn to real Pinggy | Requires network + credentials; document manual smoke in `docs/DASHBOARD-PROFILES.md` |

## Migration / Rollout

No migration. Feature is off by default (`tunnel.enabled: false`). Runtime `state/tunnel/` is disposable. Rollback = revert commit or set `enabled: false`.

## Open Questions

- [ ] Should `_pid_alive` in `arch.py` be extracted to a shared helper (`orchestrator/procutil.py`) before both callers duplicate the ProcessLookupError/PermissionError handling? Recommend YES — cheap refactor, cleaner tests.
- [ ] `restart_count` semantics under autossh's own reconnect loop: autossh's monitoring port fires SIGTERM to child ssh and respawns without our supervisor noticing. Do we count those? Recommend: expose `autossh_reconnects` as a separate stdout-parsed counter to keep the two axes distinct.
- [ ] SSE `/logs` timeout — some clients (curl, EventSource) hold the connection open forever. Add a 30-min idle cutoff to avoid resource leaks on abandoned tabs. Defer to `sdd-tasks`.
