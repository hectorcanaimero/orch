# Sprint E-5 — Tunnel Manager (proposal)

> Status: **draft** · Backend: docs (engram unavailable in current session) · Depends on: Sprint E-2 (profiles), Sprint E-3 (SPA), Sprint E-4 (arch regenerate pattern)

## Intent

Operators expose the orch dashboard publicly for stakeholder review. Cloudflare Tunnel works but requires a stable domain, out-of-process daemon, and DNS setup. Users want a lighter-weight ephemeral tunnel (Pinggy via `autossh`) that can be started/stopped from the dashboard itself, with the ephemeral URL surfaced in the UI. Currently they juggle a terminal window and copy the URL by hand.

## Scope

### In Scope
- Config-driven tunnel section in `dashboard.yaml` (`provider`, `command`, `args`, `enabled`, `auto_start`).
- Backend endpoints under `/api/tunnel/`: `status`, `capabilities`, `start` (202), `stop` (202), `logs` (SSE).
- Supervised subprocess spawn (autossh) with lock/PID file, restart count, last-error, and URL regex parsed from stdout.
- SPA panel that shows state + current URL + start/stop controls; hidden when disabled or when caller lacks capability.
- Profile gate (operator-only) + host gate (loopback-only) enforced on every control endpoint.
- Provider allowlist (v1: `autossh`) validated at load time — no arbitrary shell commands.

### Out of Scope
- Multiple concurrent tunnels (one per orch instance).
- Providers beyond pinggy in v1 (schema is provider-agnostic; only pinggy documented + tested).
- Tunnel persistence across `orch dashboard` restarts (lifecycle bound to dashboard process).
- Editing tunnel config from the UI (edit YAML directly).
- Managing Cloudflare Tunnel (has its own daemon, remains documented separately).

## Approach

Mirror the Sprint E-4 `/api/architecture/regenerate` pattern: fire-and-forget subprocess spawn, lock file for single-instance, PID + state persisted under `state/tunnel/`. A background reader tails stdout, applies a per-provider regex to extract the public URL, and updates in-memory state consumed by `GET /api/tunnel/status`. `/logs` is SSE tailing the same buffer. Every control endpoint runs through two dependencies in order: `require_operator_profile` (existing profile-token check) and a new `require_loopback_host` guard that inspects the request Host header against `127.0.0.1|localhost|::1`. `/capabilities` returns `{ can_control: false }` when either gate fails so the SPA hides the panel.

## Affected Areas

| Area | Impact | Description |
|------|--------|-------------|
| `orchestrator/dashboard/server.py` | Modified | Register `/api/tunnel/*` routes, wire dependencies |
| `orchestrator/dashboard/tunnel/` | New | `manager.py` (subprocess + state), `providers.py` (allowlist + regex), `deps.py` (host gate) |
| `orchestrator/templates/dashboard.yaml` | Modified | New `tunnel:` section with defaults (disabled) |
| `state/tunnel/` | New (runtime) | `lock`, `pid`, `state.json`, `stdout.log` |
| `frontend/src/pages/` | Modified | New `TunnelPanel` component + hook `useTunnelStatus` |
| `frontend/src/lib/types.ts` | Modified | Tunnel status DTOs |
| `docs/DASHBOARD-PROFILES.md` | Modified | Section on ephemeral-tunnel workflow + host-gate rationale |
| `orchestrator/tests/` | New | `test_tunnel_endpoints.py`, `test_tunnel_manager.py`, `test_tunnel_host_gate.py` |

## Risks

| Risk | Likelihood | Mitigation |
|------|------------|------------|
| Operator token leaks and attacker starts/stops tunnel remotely | Med | Host gate (loopback-only) — even valid token from tunnel domain gets 403 |
| Config allows arbitrary command → RCE | Low | Allowlist `command` field at YAML load; reject unknown providers |
| autossh not installed on host | Med | `doctor` check + `GET /capabilities` returns `can_control:false` with `reason:"autossh_missing"` |
| Zombie subprocess after dashboard crash | Med | Lock file with PID; startup reconciler kills stale PID before accepting new start |
| URL regex fails on provider output change | Low | Regex configurable per-provider; `last_error` surfaced in status |
| SSE log stream leaks secrets | Low | Redact known token patterns; scope `/logs` behind same gates as `/start` |

## Rollback Plan

Feature is gated by `tunnel.enabled: false` (default). To roll back: set `enabled: false` in `dashboard.yaml` — endpoints return 404, panel hidden, no code path exercised. If a shipped version misbehaves, revert the merge commit; no schema migrations, no persisted state migrations. Runtime `state/tunnel/` is disposable — delete the directory to reset.

## Dependencies

- `autossh` binary on host (documented prerequisite; `orch doctor` check).
- Existing profile-token middleware from Sprint E-2.
- Existing SPA plumbing from Sprint E-3.

## Success Criteria

- [ ] Operator on `http://127.0.0.1:7420` starts pinggy tunnel, sees ephemeral URL in panel within 5s of `POST /start`.
- [ ] Same operator hitting dashboard through tunnel URL sees NO tunnel panel and gets 403 on `POST /start`.
- [ ] Stakeholder-token requests get 403 on all `/api/tunnel/*` control endpoints and `can_control:false` from `/capabilities`.
- [ ] With `tunnel.enabled:false`, every `/api/tunnel/*` route returns 404 and the SPA renders no panel.
- [ ] `orch doctor` reports autossh presence and current tunnel config validity.
- [ ] Full pytest suite (~680 tests) stays green; new tests cover host gate, profile gate, allowlist rejection, URL parsing.

## Open questions (must resolve before spec/design)

1. **autossh vs. plain `ssh` with an internal retry loop.** autossh is the pragmatic choice (single binary, purpose-built) but adds a host dep. Alternative: manage retries in orch itself. Recommendation: autossh.
2. **Host gate behind a reverse proxy.** If the operator fronts orch with nginx on 127.0.0.1 that rewrites the `Host` header, the loopback check may misfire. Options: (a) also honor `X-Forwarded-For` from a trusted set, or (b) document "no reverse proxy in front of the dashboard for tunnel control." Recommendation: (b) — simpler, matches the local-tool ethos.
3. **SSE `/logs` gating.** Should it require operator + loopback (same as `/start`), or operator only (allowing remote log tail for debugging)? Recommendation: identical gating in v1; relax later if a real use case appears.
4. **`auto_start: true` race.** When the dashboard boots with `auto_start: true` and `enabled: true`, does the tunnel spawn before FastAPI is listening on 7420? Design must sequence: bind → serve → then start tunnel. Flag for `sdd-design`.
