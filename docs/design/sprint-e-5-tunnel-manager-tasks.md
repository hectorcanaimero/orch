# Tasks: Sprint E-5 — Tunnel Manager

> Status: **ready-for-apply** · Mode: file (engram unavailable) · Sources: `sprint-e-5-tunnel-manager.md` (proposal), `-spec.md` (TUN-1..11 + NFR-1..3), `-design.md` (module layout, decisions D1–D3).

Convention: each task cites the artifact section that pins it. "Done" is a checkable condition (file exists, test green, endpoint returns X on Y). Batches are atomic — after each batch, `pytest` from repo root MUST be green.

Legend for citations: `P§X` = proposal section, `S:TUN-N` = spec requirement, `D§X` / `D#N` = design section / decision row, `Dx` = resolved-decision addendum (D1 procutil, D2 autossh_reconnects, D3 SSE idle cutoff).

---

## Batch 1 — Shared prerequisites (procutil extraction, D1)

Rationale: both `arch.py` and the new tunnel manager rely on a PID-alive helper. Extract before either consumer changes so the refactor is small and green.

- [ ] 1.1 Create `orchestrator/procutil.py` exposing `pid_alive(pid: int) -> bool` (behavior copied verbatim from `arch.py:127-135`: `os.kill(pid, 0)` returning `False` on `ProcessLookupError`, `True` on `PermissionError`, `True` on success, `False` on `pid <= 0`). Module docstring names it as the shared PID-liveness helper for `arch` and `dashboard.tunnel`. **Cite**: D1, D§Open Questions #1. **Done**: file exists, importable as `from orchestrator.procutil import pid_alive`.
- [ ] 1.2 Create `orchestrator/tests/test_procutil.py`: (a) returns `False` for a PID we just killed (spawn `sleep 30`, `os.kill(pid, 9)`, `waitpid`), (b) returns `True` for `os.getpid()`, (c) returns `False` for `pid <= 0`, (d) returns `True` when `os.kill` raises `PermissionError` (monkeypatch). **Cite**: D1. **Done**: `pytest orchestrator/tests/test_procutil.py` green (4 tests).
- [ ] 1.3 Refactor `orchestrator/arch.py`: replace the inline `_pid_alive` def (lines ~127-135) with `from orchestrator.procutil import pid_alive as _pid_alive` (keep the underscore alias so callers at line 154 don't move). **Cite**: D1. **Done**: `arch.py` no longer defines `_pid_alive`; existing `orchestrator/tests/test_arch_command.py` still green.
- [ ] 1.4 Run full pytest suite; MUST stay green (~680 + 4 new = ~684). **Cite**: S:TUN-NFR-1. **Done**: `pytest` exit 0.

---

## Batch 2 — Backend config, providers, deps, manager (no HTTP yet)

Rationale: models and pure logic land first — the manager class is fully testable with a monkeypatched `Popen` before routes are registered.

- [ ] 2.1 Extend `orchestrator/dashboard/dashboard_config.py`: add `TunnelConfig` dataclass with fields per S:TUN-1 (`enabled`, `provider`, `command`, `args`, `auto_start`, `url_regex`, `startup_probe_timeout_s`, `url_parse_timeout_s`) and a `parse_tunnel_section(raw: dict | None) -> TunnelConfig` helper. Absent section → `enabled=False` default instance. Validation: range checks on the two timeouts, allowlist-crosscheck for `provider`/`command` (delegates to `providers.resolve`). Raises `ConfigError` naming the offending key on failure. Attach `tunnel: TunnelConfig` to the existing `DashboardConfig`. **Cite**: S:TUN-1, S:TUN-2. **Done**: parser accepts valid block, rejects `provider:ngrok` and `command:rm`.
- [ ] 2.2 Create `orchestrator/dashboard/tunnel/__init__.py` (public re-exports: `TunnelManager`, `read_state`, `TunnelState`, gate deps). **Cite**: D§File Changes. **Done**: package importable.
- [ ] 2.3 Create `orchestrator/dashboard/tunnel/providers.py`: frozen `PROVIDERS = {"autossh": ProviderSpec(command="autossh", default_args=[...], url_regex=r"https://[a-z0-9-]+\.a\.pinggy\.link", autossh_reconnect_regex=r"...reconnect...")}`. Public API: `resolve(provider_name: str) -> ProviderSpec` (raises `KeyError` on unknown), `compile_url_regex(cfg) -> re.Pattern`, `compile_reconnect_regex(cfg) -> re.Pattern`. **Cite**: S:TUN-2, S:TUN-7, D2 (autossh_reconnects regex lives here), D§Interfaces. **Done**: `resolve("autossh")` returns spec; `resolve("ngrok")` raises.
- [ ] 2.4 Create `orchestrator/tests/test_tunnel_providers.py`: (a) `resolve("autossh")` returns pinned command, (b) `resolve("ngrok")` raises, (c) URL regex captures pinggy URL from a fixture stdout line, (d) reconnect regex fires on a fixture autossh reconnect line (see D2). **Cite**: S:TUN-2, S:TUN-7, D2. **Done**: 4+ tests green.
- [ ] 2.5 Create `orchestrator/dashboard/tunnel/deps.py` with three FastAPI-callable dependencies per D§Interfaces: `require_tunnel_enabled(request)` (404), `require_operator_profile(request)` (403), `require_loopback_host(request)` (403). Host check: split `host` header on `:`, lowercase, membership test against `{"127.0.0.1", "localhost", "::1", "[::1]"}`. MUST NOT read `X-Forwarded-*`. **Cite**: S:TUN-3, D#6, D#7. **Done**: module importable.
- [ ] 2.6 Create `orchestrator/tests/test_tunnel_deps.py`: full 2×2×2 gate matrix (enabled × operator × loopback) via bare `Request` fixtures — 8 cases. Assert 404 when disabled regardless of others; 403 (operator) when enabled+non-operator; 403 (host) when enabled+operator+non-loopback; passes silently in the all-green case. Also assert deps ignore `X-Forwarded-Host`. **Cite**: S:TUN-3, S:TUN-NFR-1 bullet 1. **Done**: 9+ tests green.
- [ ] 2.7 Create `orchestrator/dashboard/tunnel/manager.py`: `TunnelManager` class holding a `threading.RLock`-guarded in-mem state dict, `state/tunnel/{lock,pid,state.json,stdout.log}` paths. Methods: `start(cfg) -> None` (acquire lock, `subprocess.Popen(..., start_new_session=True, stdout=PIPE, stderr=STDOUT)`, spawn daemon reader thread), `stop() -> None` (SIGTERM via `os.killpg`, wait 5s, SIGKILL escalate — mirror Sprint A killpg pattern), `status() -> dict` (S:TUN-5 snapshot including `autossh_reconnects` per D2), `logs_iter() -> Iterator[str]` (deque tail then live). Reader thread: line-by-line stdout consumption, redact `Bearer <hex>` and `token=<hex>` and 32+ hex tokens before append to `deque(maxlen=500)` and append-only `stdout.log`; on first URL regex hit set `status.url`; on reconnect regex hit increment `autossh_reconnects`. Atomic state write via `tmp + os.replace`. Reconciler `sweep_stale_lock()` uses `orchestrator.procutil.pid_alive` and portable exe-basename check (`ps -p <pid> -o comm=` — cross-platform per S:Open-questions #1). **Cite**: S:TUN-5, S:TUN-6, S:TUN-7, S:TUN-8, S:TUN-10 redaction, D§Data Flow, D#2, D#3, D#5, D2. **Done**: file exists, class importable.
- [ ] 2.8 Create `orchestrator/tests/test_tunnel_manager.py` with monkeypatched `subprocess.Popen` yielding controlled stdout: (a) `start` transitions `idle→starting→running` and writes atomic `state.json`, (b) URL regex hit populates `status.url` while `state` stays `running`, (c) no-URL within `url_parse_timeout_s` sets `last_error="url_parse_timeout"`, url stays `null`, state stays `running` (S:TUN-7), (d) `stop()` sends SIGTERM then escalates SIGKILL after 5s if child ignores (S:TUN-6), (e) stale-PID reconciler on boot deletes `lock`+`pid` and stays idle (S:TUN-8 zombie scenario), (f) reader increments `autossh_reconnects` on regex hit while `restart_count` stays 0 (D2), (g) `restart_count` increments on manual `/stop` then `/start` cycle (D#10), (h) redaction: `Bearer deadbeef...` never appears in the deque or `stdout.log`. **Cite**: S:TUN-5..8, S:TUN-NFR-1 bullets 2-4, D2. **Done**: 8+ tests green.
- [ ] 2.9 Run full pytest; must stay green. **Cite**: S:TUN-NFR-1. **Done**: `pytest` exit 0.

---

## Batch 3 — HTTP endpoints (server.py wiring)

Rationale: manager + deps + providers exist and are tested — now expose them over HTTP behind the three gates. Every route uses `Depends(...)` composition in the order fixed by S:TUN-3.

- [ ] 3.1 Modify `orchestrator/dashboard/server.py`: instantiate a singleton `TunnelManager` on `app.state.app_state.tunnel_manager` inside the factory alongside existing state. Guard: only instantiate when `dash_cfg.tunnel.enabled` (S:TUN-NFR-3 — zero code path when disabled beyond the config gate). Register a startup handler that calls `manager.sweep_stale_lock()` (S:TUN-8 reconciler). **Cite**: S:TUN-8, S:TUN-NFR-3, D#5. **Done**: dashboard boots with `enabled:true` and manager attribute present; with `enabled:false` attribute absent and no reconciler call.
- [ ] 3.2 Register `GET /api/tunnel/capabilities` in `server.py`. NO `Depends` (per S:TUN-4 the endpoint MUST always return 200 and report which gate failed). Body per S:TUN-4: `{enabled, provider, can_control, reason}`. `reason` priority: config → profile → host → `autossh_missing` (via `shutil.which(spec.command)`). **Cite**: S:TUN-4. **Done**: endpoint returns 200 in all 4 gate-failure permutations with correct `reason`; returns `can_control:true, reason:"ok"` when everything passes.
- [ ] 3.3 Register `GET /api/tunnel/status` with `Depends(require_tunnel_enabled), Depends(require_operator_profile), Depends(require_loopback_host)` (fixed order per S:TUN-3). Body per S:TUN-5 including `autossh_reconnects` (D2). **Cite**: S:TUN-3, S:TUN-5, D2. **Done**: 404 when disabled, 403 when non-operator, 403 when non-loopback, 200 with schema otherwise.
- [ ] 3.4 Register `POST /api/tunnel/start` (`status_code=202`) with the same three `Depends`. Body-less. Delegates to `manager.start(cfg)`. Returns 202 on `idle→starting`; 409 `{"error":"already_running","state":<...>}` when state is `starting|running`; 409 `{"error":"locked"}` when lock acquisition fails. **Cite**: S:TUN-3, S:TUN-6, S:TUN-8. **Done**: happy path 202; double-start 409; scenarios green.
- [ ] 3.5 Register `POST /api/tunnel/stop` (202) with the same three `Depends`. Delegates to `manager.stop()`. 202 on `running|starting → stopping`; 409 `{"error":"not_running"}` when `idle`. **Cite**: S:TUN-3, S:TUN-6. **Done**: happy path 202; stop-while-idle 409.
- [ ] 3.6 Register `GET /api/tunnel/logs` (SSE) with the same three `Depends`. StreamingResponse yielding `data: <line>\n\n` frames; replay last 200 lines from deque then live-tail. If subprocess not running, emit replay then `event: idle\ndata: {}\n\n` and close. Implement 30-min idle cutoff (D3): server tracks last event timestamp per connection; after 1800s with no keepalive send `retry: 5000\n\n` and close. **Cite**: S:TUN-3, S:TUN-10, D3. **Done**: SSE opens, streams frames, honors gate matrix (403 from tunnel origin per S:TUN-10 scenario), closes cleanly after 30-min idle in a fake-clock test.
- [ ] 3.7 Create `orchestrator/tests/test_tunnel_endpoints.py` using FastAPI `TestClient`: (a) gate matrix per endpoint (spot-check 2 endpoints × 8 combos = 16 assertions), (b) happy-path start returns 202 and `status` reflects `running` with parsed URL within timeout (monkeypatched Popen writes URL line), (c) double `/start` → 409 `already_running`, (d) `/stop` while idle → 409 `not_running`, (e) `/capabilities` reports each failure `reason` correctly, (f) `/logs` from tunnel-origin Host → 403 (S:TUN-10 scenario), (g) `/logs` 30-min idle cutoff closes stream with `retry: 5000` (fake clock, D3). **Cite**: S:TUN-3..7, S:TUN-10, S:TUN-NFR-1, D3. **Done**: 12+ tests green.
- [ ] 3.8 Create `orchestrator/tests/test_tunnel_host_gate.py` — dedicated matrix for the loopback gate: `127.0.0.1`, `localhost`, `::1`, `[::1]`, with and without port suffix, all → pass; `example.com`, `abc123.a.pinggy.link`, empty header → fail; `X-Forwarded-Host: 127.0.0.1` with `Host: evil.tld` → fail (proxy header ignored per S:TUN-3). **Cite**: S:TUN-3, S:TUN-NFR-1. **Done**: 10+ parametrized cases green.
- [ ] 3.9 Run full pytest; green. **Cite**: S:TUN-NFR-1. **Done**: `pytest` exit 0.

---

## Batch 4 — `orch doctor` extension + `auto_start` boot sequence

Rationale: preflight/doctor is independent of HTTP but depends on config parsing. `auto_start` needs the endpoints registered because it invokes the same code path.

- [ ] 4.1 Extend `orchestrator/preflight.py` (or the module that hosts current doctor checks — verify wiring in `orch.py` CLI): add `check_tunnel_config(dashboard_yaml_path) -> CheckResult` (PASS if `tunnel` absent or valid per S:TUN-1/2; FAIL naming offending key) and `check_tunnel_binary(cfg) -> CheckResult` (PASS if `shutil.which(spec.command)` or `enabled:false` or absent; WARN when `enabled:false` and binary missing; FAIL when `enabled:true` and binary missing with remediation string mentioning `autossh`). Wire both into the doctor result list. **Cite**: S:TUN-11. **Done**: `orch doctor` output includes both checks; both scenarios in S:TUN-11 green.
- [ ] 4.2 Extend `orchestrator/tests/test_doctor_cmd.py` (or add `test_doctor_tunnel.py` if the existing file is large): (a) valid tunnel config → PASS, (b) invalid `provider:ngrok` → FAIL naming `tunnel.provider`, (c) `enabled:true` + autossh missing (monkeypatch `shutil.which`) → FAIL with non-zero exit, (d) `enabled:false` + autossh missing → WARN, (e) tunnel section absent → PASS. **Cite**: S:TUN-11, S:TUN-NFR-1 bullet 7. **Done**: 5 tests green.
- [ ] 4.3 In `server.py`, register `@app.on_event("startup")` handler `_auto_start_tunnel`: only runs when `cfg.tunnel.enabled and cfg.tunnel.auto_start`. Sequence per S:TUN-9: schedule `asyncio.create_task(_probe_and_start(...))` (task sleeps briefly so serve loop is accepting), then `httpx.AsyncClient` GET to `http://127.0.0.1:{port}/` with timeout `startup_probe_timeout_s`. On 2xx → invoke same code path as `POST /api/tunnel/start` (call `manager.start(cfg)` directly). On failure → `logger.error("auto_start_skipped: self_probe_failed")` and leave state idle; startup itself MUST NOT fail. **Cite**: S:TUN-9, D#8. **Done**: startup handler registered, gated on both booleans.
- [ ] 4.4 Add `orchestrator/tests/test_tunnel_auto_start.py` (integration): (a) `auto_start:true` + successful probe → `manager.start` called once; monkeypatch httpx to return 200 (S:TUN-9 happy scenario), (b) probe returns 500 → `manager.start` not called AND stderr contains `auto_start_skipped: self_probe_failed` AND status stays `idle` (S:TUN-9 skip scenario), (c) `auto_start:false` → handler no-ops even when enabled. **Cite**: S:TUN-9, S:TUN-NFR-1 bullet 5. **Done**: 3 tests green.
- [ ] 4.5 Run full pytest; green. **Cite**: S:TUN-NFR-1. **Done**: `pytest` exit 0.

---

## Batch 5 — Frontend (types, hooks, capability probe, panel)

Rationale: backend contracts are stable and tested. Frontend can now consume them. Types before hooks; capability probe before panel (drives conditional mount per D#9).

- [ ] 5.1 Modify `frontend/src/lib/types.ts`: add `TunnelState = "idle"|"starting"|"running"|"stopping"|"error"`; `TunnelStatus` (fields per S:TUN-5 plus `autossh_reconnects: number` per D2); `TunnelCapabilities` (fields per S:TUN-4 including `reason` union `"ok"|"config_disabled"|"profile_gate"|"host_gate"|"autossh_missing"`). **Cite**: S:TUN-4, S:TUN-5, D2. **Done**: types compile (`pnpm tsc --noEmit` in `frontend/`).
- [ ] 5.2 Modify `frontend/src/lib/api.ts`: add `getTunnelCapabilities()`, `getTunnelStatus()`, `startTunnel()` (POST, expect 202/403/404/409), `stopTunnel()` (POST, same expectations). Return typed responses; surface 409 body distinctly so the panel can render `already_running` vs `locked` vs `not_running`. **Cite**: S:TUN-4, S:TUN-5, S:TUN-6. **Done**: functions exported and typed.
- [ ] 5.3 Create `frontend/src/hooks/useTunnelCapabilities.ts`: React Query one-shot (`staleTime: Infinity`, `retry: false`) — drives the conditional mount from `TunnelPage` (the dedicated `/tunnel` route wrapper). **Cite**: S:TUN-4, D#9. **Done**: hook exports `useTunnelCapabilities()`.
- [ ] 5.4 Create `frontend/src/hooks/useTunnelStatus.ts`: React Query polling — `refetchInterval: 3000` when `state === "idle"`, `1000` when `starting|running|stopping`, disabled when caps `can_control:false`. **Cite**: S:TUN-5. **Done**: hook exports `useTunnelStatus()` with adaptive polling.
- [ ] 5.5 Create `frontend/src/hooks/useTunnelLogs.ts`: SSE consumer modeled on `useLiveLogs.ts` (200-line ring buffer client-side; auto-reconnect on `retry:` frame per D3). **Cite**: S:TUN-10, D3. **Done**: hook exports `useTunnelLogs()`.
- [ ] 5.6 Create `frontend/src/pages/TunnelPanel.tsx`: shadcn Card with header (`state` badge, `provider`), body (current `url` with copy-to-clipboard button, `restart_count`, `autossh_reconnects` per D2, `last_error`), footer (Start/Stop buttons — disabled when transition in flight; distinct toasts for 409 `already_running`/`locked`/`not_running`), collapsible log tail (via `useTunnelLogs`). Component MUST NOT be exported at module top-level in a way that mounts it unconditionally; consumer conditions on capabilities. **Cite**: S:TUN-4, S:TUN-5, S:TUN-6, S:TUN-10, D#9, D2. **Done**: component compiles, no oxlint errors.
- [ ] 5.7 Create `frontend/src/pages/TunnelPage.tsx` and wire it at the `/tunnel` route with a nav entry in `frontend/src/components/layout/AppLayout.tsx`: `TunnelPage` calls `useTunnelCapabilities()` and renders `<TunnelPanel/>` ONLY when `caps.can_control === true` — no CSS-hidden fallback (D#9). When `enabled:false` or gates fail, do NOT mount the panel at all (render an `<Alert/>` with the capability reason, or `null` while loading). Note: `BoardPage.tsx` cannot host the panel because it is a full-height ExcaliDash iframe with no sibling slot — a dedicated route preserves the D#9 invariant and gives the feature a clean URL. **Cite**: S:TUN-NFR-3, D#9. **Done**: panel visible for operator on loopback at `/tunnel`; absent for stakeholder or tunnel-origin caller.
- [ ] 5.8 Frontend type-check + oxlint: `pnpm tsc --noEmit && pnpm lint` in `frontend/`. Zero errors. (Convention: never build, only type-check.) **Cite**: repo convention. **Done**: both commands exit 0.

---

## Batch 6 — Docs + final integration pass

- [ ] 6.1 Extend `docs/DASHBOARD-PROFILES.md` with a new section "Ephemeral tunnel (Pinggy via autossh)": (a) config example (S:TUN-1 fields), (b) three-gate model diagram (config→profile→host) and rationale (D#7), (c) reverse-proxy exclusion warning — no XFF (S:TUN-3, P§Open-questions #2), (d) `auto_start` semantics and self-probe (S:TUN-9), (e) rollback = `enabled:false` (S:TUN-NFR-3), (f) manual smoke test recipe (real pinggy — not covered by pytest per D§Testing). **Cite**: S:TUN-3, S:TUN-9, S:TUN-NFR-3, D§Testing. **Done**: section merged into the doc.
- [ ] 6.2 Modify `orchestrator/dashboard/dashboard.yaml` (bundled template): add commented `tunnel:` block with safe defaults (`enabled:false`, `provider:autossh`, `auto_start:false`, sample `args`). Ensure `pyproject.toml [tool.setuptools.package-data]` still picks the file up (it already includes `dashboard.yaml`; verify no path change needed). **Cite**: S:TUN-1, P§Rollback, D§File-Changes. **Done**: bundled template renders and `orch dashboard --dry-run` (or existing config test) loads it without error.
- [ ] 6.3 Final full pytest run + orch doctor smoke on a fixture project (both with tunnel disabled and enabled without autossh). **Cite**: S:TUN-NFR-1, S:TUN-11. **Done**: `pytest` green; `orch doctor` reports the new checks with expected PASS/WARN/FAIL per config.

---

## Traceability

| Requirement | Covered by tasks |
|---|---|
| TUN-1 (config schema) | 2.1, 4.1, 6.2 |
| TUN-2 (provider allowlist) | 2.1, 2.3, 2.4, 4.1 |
| TUN-3 (three-gate order) | 2.5, 2.6, 3.3, 3.4, 3.5, 3.6, 3.7, 3.8, 6.1 |
| TUN-4 (`/capabilities`) | 3.2, 3.7, 5.1, 5.2, 5.3, 5.6 |
| TUN-5 (`/status`) | 2.7, 2.8, 3.3, 3.7, 5.1, 5.2, 5.4, 5.6 |
| TUN-6 (`/start`, `/stop`) | 2.7, 2.8, 3.4, 3.5, 3.7, 5.2, 5.6 |
| TUN-7 (URL extraction) | 2.3, 2.4, 2.7, 2.8 |
| TUN-8 (lock/PID lifecycle) | 2.7, 2.8, 3.1, 3.4 |
| TUN-9 (`auto_start` sequence) | 4.3, 4.4, 6.1 |
| TUN-10 (SSE `/logs`) | 2.7 (redaction), 3.6, 3.7, 5.5, 5.6 |
| TUN-11 (`orch doctor`) | 4.1, 4.2, 6.3 |
| TUN-NFR-1 (tests green) | 1.4, 2.9, 3.9, 4.5, 6.3 |
| TUN-NFR-2 (no new runtime deps) | verified by inspection in 2.7 (stdlib only), 4.3 (`httpx` already in dev/dashboard) — flag if httpx not present at runtime (see risk below) |
| TUN-NFR-3 (rollback via config) | 3.1, 5.7, 6.1 |
| D1 (procutil extraction) | 1.1, 1.2, 1.3 |
| D2 (autossh_reconnects) | 2.3, 2.4, 2.7, 2.8, 5.1, 5.6 |
| D3 (SSE 30-min idle cutoff) | 3.6, 3.7, 5.5 |

**Every TUN-1..11 and every NFR is covered.** Zero unmatched requirements.

---

## Critical path

Longest dependency chain from first task to last:

`1.1 (procutil) → 1.3 (arch refactor) → 2.7 (manager uses procutil) → 2.8 (manager tests) → 3.1 (server wires manager) → 3.4 (/start route) → 3.7 (endpoint tests) → 4.3 (auto_start uses /start path) → 4.4 (auto_start tests) → 5.6 (TunnelPanel consumes /start) → 5.7 (TunnelPage at /tunnel mounts panel via AppLayout nav) → 6.3 (final smoke).`

12 tasks on the critical path. Everything else (providers, deps, doctor, docs, types/hooks) can parallelize within its batch.

---

## Risks / human-review flags

- **3.6 SSE 30-min idle cutoff (D3)** — Uvicorn/Starlette does not expose per-connection idle timers naturally. Implementation likely needs an `anyio.move_on_after` wrapper or an `asyncio.wait_for` around each `yield`. Human review recommended before `sdd-apply` picks this up.
- **2.7 killpg semantics on macOS vs Linux** — `os.killpg(os.getpgid(pid), SIGTERM)` behaves consistently, but autossh spawning the child ssh in the SAME process group is worth verifying. If autossh detaches, we may need to walk children via `ps`. Flag for the applier.
- **4.3 `httpx` availability at runtime** — S:TUN-NFR-2 forbids new runtime deps. `httpx` is in `pyproject.toml` under dev (for `FastAPI TestClient`). Task 4.3 assumes it is import-available at runtime. If not, swap to `urllib.request` (stdlib). **Verify before implementing 4.3.**
- **6.2 packaged `dashboard.yaml`** — modifying the bundled template affects fresh installs. Confirm no existing test snapshots the file byte-for-byte.
- **2.7 exe-basename portability check** — spec open-question #1 recommends `ps -p <pid> -o comm=`. Confirm on macOS the flag works identically (it does on BSD ps, but verify no `-o` gotchas).

---

## Envelope

- **status**: `ready-for-apply`
- **artifact**: `docs/design/sprint-e-5-tunnel-manager-tasks.md`
- **batches**: 6 · **total tasks**: 41 (batch 1: 4, batch 2: 9, batch 3: 9, batch 4: 5, batch 5: 8, batch 6: 3, plus internal type-check step counted within batch 5)
- **next_recommended**: `sdd-apply` starting with Batch 1
- **risks**: SSE idle cutoff plumbing (3.6), killpg on macOS (2.7), runtime `httpx` (4.3), packaged yaml snapshotting (6.2)
