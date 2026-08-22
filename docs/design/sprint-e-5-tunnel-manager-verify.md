# Sprint E-5 — Tunnel Manager (verify report)

> **Verdict**: **GO with follow-ups**
> Phase: `sdd-verify` · Change: `sprint-e-5-tunnel-manager` · Branch: `sprint-e3/spa-spike` · Date: 2026-08-22
> Backend: docs (engram unavailable) · Report: `docs/design/sprint-e-5-tunnel-manager-verify.md`

---

## Executive Summary

All 11 functional requirements (TUN-1..TUN-11), all 3 NFRs, and all 3 design decisions (D1/D2/D3) are implemented, wired into HTTP routes, tested at the behavioral level, and documented. The full pytest suite runs at the expected baseline (**993 passed, 2 skipped, 1 pre-existing failure** in `test_router.py::test_validate_all_models_passes_on_shipping_router` unrelated to this sprint). Frontend `tsc --noEmit` is clean. Zero new runtime dependencies. The load-bearing three-gate security check (config → operator profile → loopback host, XFF/XFH ignored) is verified end-to-end via dedicated matrix tests. The one meaningful deviation from the spec is UI layout (dedicated `/tunnel` route + `TunnelPage` wrapper instead of mounting `TunnelPanel` inside `BoardPage.tsx`) — this preserves the D#9 invariant ("panel not in the React tree when `can_control === false`") and is an improvement, not a regression.

**Recommendation**: merge. File the two low-severity follow-ups below at the reviewer's discretion.

---

## Requirement Coverage Matrix

| Req | Intent | Implemented in | Tested in | Verdict | Note |
|-----|--------|----------------|-----------|---------|------|
| **TUN-1** | `tunnel:` config schema in `dashboard.yaml` with typed defaults, timeouts, validation | `orchestrator/dashboard/dashboard_config.py` (`TunnelConfig`, `parse_tunnel_section`, `_coerce_bounded_int`) | `test_tunnel_config.py` (all 16 fns — absent/valid/invalid/timeouts/args-list/args-dict) | pass | Bounds enforced (`startup_probe_timeout_s ∈ [1,30]`, `url_parse_timeout_s ∈ [5,300]`). Invalid sections raise `ConfigError` naming the key. |
| **TUN-2** | Hard-coded provider allowlist with pinned binary; reject unknown provider or command override | `orchestrator/dashboard/tunnel/providers.py` (`PROVIDERS`, `resolve`), `dashboard_config.py:313` (command-mismatch check) | `test_tunnel_providers.py::test_resolve_unknown_provider_raises_keyerror`, `test_known_providers_contains_only_autossh_in_v1`, `test_tunnel_config.py::test_parse_rejects_command_override_for_autossh`, `test_parse_rejects_unknown_provider` | pass | Registry frozen (`ProviderSpec(slots=True, frozen=True)`); command override rejected with exact message from spec. |
| **TUN-3** | Three-layer gate order (config 404 → operator 403 → host 403), XFF ignored | `orchestrator/dashboard/tunnel/deps.py` (`require_tunnel_enabled`, `require_operator_profile`, `require_loopback_host`, `_extract_host`), `server.py:1184-1246` (route deps composed in fixed order) | `test_tunnel_deps.py` (all 9 fns incl. 2×2×2 matrix + XFH ignored), `test_tunnel_endpoints.py` (16 gate-composition fns), `test_gate_order_config_wins_over_host` | pass | `_extract_host` only reads `Host` header; XFF/XFH never consulted. See gate matrix section below. |
| **TUN-4** | `GET /api/tunnel/capabilities` — always 200, reports `reason` for first failing gate, includes `autossh_missing` after all gates pass | `server.py:1149-1178` (`api_tunnel_capabilities`), `deps.py:102-118` (`evaluate_capabilities`), `middleware.py:177` (bypass), `dashboard_config.py:60` (allowlist) | `test_tunnel_endpoints.py::test_capabilities_*` (6 fns covering all 5 reasons + no-token), `test_tunnel_deps.py::test_evaluate_capabilities_*` (4 fns), `test_dashboard_security_integration.py` (bypass parity) | pass | `reasons` list also emitted (design-doc vocab) alongside spec-required `reason` singular — additive, backward-compatible. |
| **TUN-5** | `GET /api/tunnel/status` — full snapshot with `state`/`url`/`pid`/`started_at`/`restart_count`/`last_error` | `manager.py:_blank_state` + `status()`, `server.py:1180-1191` | `test_tunnel_manager.py::test_start_transitions_*`, `test_url_regex_hit_populates_status_url`, `test_tunnel_endpoints.py::test_status_returns_idle_snapshot_on_fresh_boot` | pass | Snapshot includes `autossh_reconnects` per D2, and `phase` mirror for compatibility. |
| **TUN-6** | `/start` 202 idle→starting, 409 `already_running` on double-start, 409 `locked` on lock held; `/stop` 202 running→stopping→idle, 409 `not_running` when idle; SIGTERM then SIGKILL after 5s | `manager.py::start`/`stop`/`_signal_pgroup`, `server.py:1193-1237` | `test_tunnel_manager.py::test_double_start_raises_already_running`, `test_stop_signals_process_group_and_transitions_to_idle`, `test_stop_escalates_to_sigkill_when_sigterm_ignored`, `test_stop_while_idle_raises_not_running`, `test_start_refuses_when_live_lock_exists`, `test_tunnel_endpoints.py::test_double_start_returns_409_already_running`, `test_start_returns_409_locked_when_manager_signals_lock`, `test_stop_while_idle_returns_409_not_running` | pass | Both 409 payload shapes present: `{"error":"already_running","state":<current>}` and `{"error":"locked"}` / `{"error":"not_running"}`. Signal escalation uses `os.killpg` first with `os.kill` fallback. |
| **TUN-7** | URL regex extraction from stdout, `last_error="url_parse_timeout"` on no-match within `url_parse_timeout_s` with `url:null` `state:running` | `manager.py:_start_reader_thread` (lines 411-470), `providers.py::compile_url_regex` | `test_tunnel_manager.py::test_url_regex_hit_populates_status_url`, `test_url_parse_timeout_sets_last_error_url_stays_null`, `test_tunnel_providers.py::test_url_regex_captures_valid_pinggy_urls` (parametrized), `test_url_regex_rejects_malformed` | pass | Reader thread flags timeout via `timeout_flagged` sentinel so a late URL still populates and `last_error` isn't rewritten on every subsequent line. |
| **TUN-8** | Lock at `state/tunnel/lock`, PID at `state/tunnel/pid`, boot reconciler adopts live PID whose `comm` matches allowlisted binary, else sweeps both files | `manager.py::sweep_stale_lock`, `_acquire_lock_or_raise`, `_pid_comm` (uses `ps -p <pid> -o comm=`), `procutil.py::pid_alive` | `test_tunnel_manager.py::test_sweep_stale_lock_removes_dead_pid_files`, `test_sweep_stale_lock_is_noop_when_no_lock_present`, `test_start_refuses_when_live_lock_exists`, `test_procutil.py` (5 fns) | pass | Cross-platform PID→exe check via `ps` (macOS + Linux both support `-o comm=`). Reconciler wired at `server.py:488` behind the `enabled:true` guard. |
| **TUN-9** | `auto_start:true` bind → serve → self-probe `GET /` → spawn; skip+log on probe failure; dashboard never fails to boot | `server.py::_tunnel_self_probe` (stdlib `urllib.request`, retries), `_run_auto_start`, `_tunnel_auto_start` startup handler at line 1299 | `test_tunnel_auto_start.py` (8 fns — happy path, probe fail, probe-port unknown, retry count, first-success short-circuit, disabled-no-op, auto_start-false no-op, swallows manager errors) | pass | Stdlib-only probe (no `httpx` runtime dep — resolves task-4.3 risk in tasks doc). Retries × `TUNNEL_AUTO_START_PROBE_RETRIES` (default 3) with gap. |
| **TUN-10** | `GET /api/tunnel/logs` SSE, same three-gate composition, 200-line replay + live-tail, redact `Bearer <hex>`/`token=<hex>`/32+ hex, 30-min idle cutoff | `server.py:1239-1290` (`api_tunnel_logs` with `anyio` idle deadline), `manager.py::_redact` + `_start_reader_thread` (redaction before deque append and `stdout.log` write) | `test_tunnel_endpoints.py::test_logs_streams_redacted_buffer`, `test_logs_closes_cleanly_after_idle_cutoff` (patches `TUNNEL_SSE_IDLE_CUTOFF_S=0.05`), `test_logs_403_from_tunnel_host`, `test_tunnel_manager.py::test_reader_redacts_bearer_tokens_before_appending_to_deque` | pass | 30-min cutoff emits `retry: 5000\n\n` before closing (D3). Emits `event: idle\ndata: {}\n\n` when state==idle so idle-replay closes cleanly. |
| **TUN-11** | `orch doctor` reports `tunnel.config` + `tunnel.binary`; missing binary + `enabled:true` → FAIL non-zero exit; missing binary + `enabled:false` → WARN; section absent → PASS | `preflight.py::check_tunnel` (lines 658-801), wired in `doctor.py:190` | `test_doctor_tunnel_checks.py` (11 fns covering all matrix cells + malformed YAML + `build_doctor_report` integration) | pass | Live-invoked `build_doctor_report` against a fixture project (`/tmp/orch-doctor-verify`) confirms both rows in the payload with correct WARN status when `enabled:false` and binary absent. |
| **TUN-NFR-1** | Existing ~680 pytest suite stays green; new tests cover the mandated axes | Whole sprint | See "Test suite health" section | pass | 993 passed / 2 skipped / 1 pre-existing failure. New tests: ~90 across 8 tunnel-only files. |
| **TUN-NFR-2** | No new runtime deps; stdlib subprocess/signal/os/asyncio only | `pyproject.toml` unchanged in the sprint scope | `git diff f0e5840..HEAD -- pyproject.toml frontend/package.json` → **zero bytes** | pass | Notable resolution: `_tunnel_self_probe` uses `urllib.request` (stdlib) rather than `httpx`, defusing the risk flagged in tasks.md §Risks. |
| **TUN-NFR-3** | `tunnel.enabled:false` fully disables — routes 404, no subprocess, no reconciler, no panel | `server.py:484-494` (manager only instantiated when enabled), `dashboard_config.py:117` (default `TunnelConfig()`), `TunnelPage.tsx:59` (conditional mount), tests below | `test_tunnel_endpoints.py::test_*_404_when_disabled` (4 fns), `test_dashboard_config_load_absent_tunnel_stays_disabled`, `test_tunnel_binary_warns_when_missing_and_disabled` | pass | Rollback = flip `enabled:false`; nothing spawns/sweeps/mounts. Verified by manager `is None` check on `AppState`. |
| **D1** | Extract `pid_alive` into shared `orchestrator/procutil.py` | `orchestrator/procutil.py`, `arch.py` refactored to import | `test_procutil.py` (5 fns) | pass | Handles `ProcessLookupError` / `PermissionError` / `pid<=0` / generic `OSError`. |
| **D2** | Track `autossh_reconnects` separately from `restart_count` | `providers.py::_AUTOSSH_RECONNECT_PATTERN`, `manager.py::_start_reader_thread` (reconnect regex increments counter), status snapshot exposes both | `test_tunnel_providers.py::test_reconnect_regex_matches_autossh_lifecycle_events` (parametrized), `test_reconnect_regex_does_not_match_normal_stdout`, `test_tunnel_manager.py::test_reconnect_regex_increments_autossh_reconnects`, `test_restart_count_increments_on_manual_stop_start_cycle`, frontend `types.ts::TunnelStatus.autossh_reconnects` + `TunnelPanel.tsx` Stat | pass | Two axes cleanly separated. |
| **D3** | SSE `/logs` 30-min idle cutoff emits `retry: 5000` and closes | `server.py:1257-1290` (`anyio.current_time()` deadline), `useTunnelLogs.ts:29` (client honors `retry:` frame with `RETRY_MS=5000`) | `test_tunnel_endpoints.py::test_logs_closes_cleanly_after_idle_cutoff` (fake-clock via `monkeypatch.setattr(TUNNEL_SSE_IDLE_CUTOFF_S, 0.05)`) | pass | Cutoff overridable in tests via module-level constant + `_get_sse_idle_cutoff_s()` accessor. |

**Coverage summary**: **17 / 17 pass** (11 TUN + 3 NFR + 3 Dn). **0 partial / 0 missing / 0 deviation** in requirement conformance (see Coherence section for the one UI-layout deviation, which is additive and preserves the D#9 invariant).

---

## Gate Matrix (load-bearing security check)

Empirically verified via `test_tunnel_endpoints.py` + `test_tunnel_deps.py`:

| # | Scenario | Test(s) | Expected | Observed |
|---|----------|---------|----------|----------|
| 1 | `enabled:false` + operator + loopback → all control routes 404 | `test_status_404_when_disabled`, `test_start_404_when_disabled`, `test_stop_404_when_disabled`, `test_logs_404_when_disabled` | 404 | 404 |
| 2 | `enabled:true` + stakeholder + loopback → 403 | `test_status_403_for_stakeholder`, `test_start_403_for_stakeholder` (also asserts `start_calls == 0`), `test_stop_403_for_stakeholder`, `test_logs_403_for_stakeholder` | 403 | 403 |
| 3 | `enabled:true` + operator + tunnel-domain Host → 403 | `test_status_403_from_tunnel_host`, `test_start_403_from_tunnel_host` (also asserts `start_calls == 0`), `test_stop_403_from_tunnel_host`, `test_logs_403_from_tunnel_host` (TUN-10 scenario) | 403 | 403 |
| 4 | `enabled:true` + operator + LAN-IP Host → 403 | `test_require_loopback_host_rejects_non_loopback` (parametrized: `example.com`, `192.168.1.10`, `10.0.0.1`, `[fe80::1]`) | 403 | 403 |
| 5 | `enabled:true` + operator + `X-Forwarded-For: 127.0.0.1` from LAN Host → still 403 | `test_host_gate_ignores_x_forwarded_for` (endpoints), `test_loopback_gate_ignores_forwarded_host_header` (deps) | 403 | 403 |
| 6 | `/api/tunnel/capabilities` returns 200 without auth + reports right `reason` for each rejection scenario | `test_capabilities_returns_disabled_when_config_off`, `test_capabilities_returns_not_operator_for_stakeholder`, `test_capabilities_returns_not_loopback_from_tunnel_origin`, `test_capabilities_ok_when_all_gates_pass_and_binary_present`, `test_capabilities_flags_autossh_missing_when_binary_absent`, `test_capabilities_requires_no_token` | 200 with correct `reason`/`reasons` | 200, all reasons match spec vocab |
| 7 | Panel NOT in the React tree when `can_control === false` | Static inspection: `TunnelPage.tsx:59` renders `{caps && caps.can_control ? <TunnelPanel …/> : caps ? <Alert …/> : null}`; no `hidden` prop, no CSS `display:none` on the panel (all `aria-hidden` occurrences are on decorative icons); `BoardPage.tsx` no longer references `TunnelPanel` | conditional mount, no CSS hiding | Confirmed — see Coherence deviation note about the panel location |

**All 7 gate assertions hold.** The XFF ignore is the load-bearing safety net and has TWO dedicated tests (one at the dep layer, one at the endpoint layer).

---

## Test Suite Health

Command: `python3 -m pytest --tb=short -q` from `/Volumes/PortableSSD/orch`.

Result (last 20 lines):

```
orchestrator/tests/test_dashboard_stakeholder_view.py: 5 warnings
  /opt/homebrew/lib/python3.13/site-packages/starlette/templating.py:161: DeprecationWarning: The `name` is not the first parameter anymore. The first parameter should be the `Request` instance.
  Replace `TemplateResponse(name, {"request": request})` by `TemplateResponse(request, name)`.
    warnings.warn(

-- Docs: https://docs.pytest.org/en/stable/how-to/capture-warnings.html
=========================== short test summary info ============================
FAILED orchestrator/tests/test_router.py::test_validate_all_models_passes_on_shipping_router
1 failed, 993 passed, 2 skipped, 358 warnings in 24.55s
```

Verdict: **matches expected baseline exactly** (993 passed / 2 skipped / 1 failed, the failure being the pre-existing `test_router.py::test_validate_all_models_passes_on_shipping_router` explicitly called out in the launch instructions as unrelated to E-5).

- Duration: 24.55 s (reasonable for the suite size + subprocess-heavy tunnel tests).
- Warnings: 358 total, ALL of them pre-existing `on_event` deprecation notices from FastAPI/Starlette 0.115.x and the Starlette 1.0 `TemplateResponse` migration warning. None emitted from E-5-authored code specifically.

---

## NFR Sanity

- **NFR-1** (no regression) — met: baseline `993 passed, 2 skipped, 1 failed` preserved; the failure is pre-existing.
- **NFR-2** (no new runtime deps) — met: `git diff f0e5840..HEAD -- pyproject.toml frontend/package.json` returns zero bytes. `_tunnel_self_probe` deliberately uses `urllib.request` (stdlib) rather than `httpx`, resolving the risk called out in `tasks.md §Risks` bullet 3. `frontend/package.json` was added in the earlier E-3 SPA spike (commit `f0e5840`) — E-5 added no npm deps on top of it.
- **NFR-3** (rollback via config) — met: bundled `orchestrator/dashboard/dashboard.yaml` defaults to `tunnel: { enabled: false, provider: autossh, command: autossh, auto_start: false, … }`. Grep for `if dash_cfg.tunnel.enabled` at `server.py:485` confirms manager and reconciler run only when enabled. `TunnelPage.tsx:59` gates the panel behind `caps && caps.can_control` (which is `false` when disabled). `_tunnel_auto_start` at `server.py:1299` guards on `tcfg.enabled and tcfg.auto_start` and returns early otherwise.

---

## Docs Conformance

- `docs/DASHBOARD-PROFILES.md` — pinggy section (line 128 onward) covers: config example (line 165 with the full `tunnel:` block), three-gate table (line 242) with rationale, XFF/XFH ignore explicit call-out (line 259), reverse-proxy exclusion (line 280), `auto_start` semantics + self-probe (line 225), `orch doctor` integration table (line 292), rollback recipe (line 301). All 6 spec bullet points from task 6.1 are present.
- Bundled `orchestrator/dashboard/dashboard.yaml` — added `tunnel:` block (line 12) with `enabled:false` default, provider, matching command, sample `args`, `auto_start:false`, both timeouts. Comment references `docs/DASHBOARD-PROFILES.md`. Format valid (loads via `yaml.safe_load` inside `parse_tunnel_section` without raising).
- `CLAUDE.md` — line 17 test-count line reads `Full suite is 993 passed + 2 skipped + 1 pre-existing failure`. Accurate against today's run.

---

## Cross-cutting Sanity

- **Dead code**: none. Every symbol in `orchestrator/dashboard/tunnel/__init__.py` `__all__` is imported by `server.py`, `preflight.py`, or the test suite. `_get_sse_idle_cutoff_s()`, `TUNNEL_SSE_IDLE_CUTOFF_S`, `TUNNEL_AUTO_START_PROBE_RETRIES`, `TUNNEL_AUTO_START_PROBE_GAP_S` are all referenced by their tests.
- **TODO/FIXME/XXX in E-5 paths**: `rg -w 'TODO|FIXME|XXX' orchestrator/dashboard/tunnel/ orchestrator/preflight.py frontend/src/hooks/useTunnel*.ts frontend/src/pages/Tunnel*.tsx` → **empty**.
- **Frontend type-check**: `cd frontend && npx tsc --noEmit` → exit 0, no output. Clean.
- **`orch doctor` payload**: live-invoked `build_doctor_report` against a scratch fixture project (`/tmp/orch-doctor-verify`) with `tunnel.enabled:false` + `autossh` absent from PATH → payload contains both `tunnel.config` (status `ok`, detail `tunnel section valid (provider=autossh, enabled=False)`) and `tunnel.binary` (status `warn`, detail names the binary + install hint). Wiring confirmed.
- **Working tree**: `git status --short` before writing this report → empty (clean). This report is the only change introduced by the verify pass.

---

## Coherence (Design)

| Decision | Followed? | Note |
|----------|-----------|------|
| D#1 package layout (`manager.py`/`providers.py`/`deps.py`) | yes | Exactly the shape described in design. |
| D#2 `Popen(start_new_session=True, stdout=PIPE, stderr=STDOUT)` | yes | `manager.py:_spawn` line 382-390. |
| D#3 daemon-thread stdout reader with `deque(maxlen=500)` under `Lock` | yes | `_start_reader_thread` + `_logs_lock`. |
| D#4 URL regex in `providers.py`, cached on hit | yes | `providers.py::_AUTOSSH_URL_PATTERN`, reader sets `state["url"]` once (`url_seen` sentinel). |
| D#5 lock JSON at `state/tunnel/lock`, PID sweep | yes | `_acquire_lock_or_raise` writes `{pid, started_at, provider, phase}`; `sweep_stale_lock` uses `pid_alive` + `_pid_comm`. |
| D#6 gates as FastAPI `Depends` (not middleware) | yes | Routes compose `Depends(require_tunnel_enabled), Depends(require_operator_profile), Depends(require_loopback_host)` in fixed order. |
| D#7 gate order (config 404 → operator 403 → host 403) | yes | Verified in gate matrix table. |
| D#8 `auto_start` via `@app.on_event("startup")` + background task | yes | `_tunnel_auto_start` schedules `asyncio.create_task(_run_auto_start(...))`. |
| D#9 SPA panel NOT mounted when `can_control:false` | **yes, with UI layout deviation** | Panel is now behind a dedicated `/tunnel` route (`TunnelPage.tsx`) instead of being mounted inside `BoardPage.tsx` as `tasks.md` §5.7 described. The invariant is preserved: `TunnelPage.tsx:59` renders `{caps && caps.can_control ? <TunnelPanel/> : caps ? <Alert/> : null}`. No CSS-hidden fallback. This deviation is an improvement (dedicated URL, cleaner IA) and does not violate D#9. |
| D#10 `restart_count` resets on manual stop + dashboard restart | yes | `_blank_state()` initializes 0; `stop()` increments on transition; `_on_child_exit` resets volatile fields but preserves `restart_count`. Tested in `test_restart_count_increments_on_manual_stop_start_cycle`. |

---

## Follow-up Tickets

Both are low-severity — do not block merge.

- **FU-1 (docs)** — `tasks.md §5.7` still references `BoardPage.tsx` as the mount point for `TunnelPanel`; shipped code lives at `TunnelPage.tsx` behind the `/tunnel` route + a nav entry in `AppLayout.tsx`. Non-blocking (the D#9 invariant is preserved and the code is cleaner) but the task doc drift should be reconciled — either amend `tasks.md` on archive, or add a note in the sprint's archive report.
- **FU-2 (test hygiene)** — 358 pytest deprecation warnings are all pre-existing (`on_event` from FastAPI 0.115 lifespan-events migration, plus one Starlette `TemplateResponse` position-arg warning). Not introduced by E-5. Consider a dedicated cleanup sprint (`orchestrator/dashboard/server.py:1299 @app.on_event("startup") → lifespan handler`) so we can pin FastAPI ≥0.116 later without re-triggering the Starlette 1.0 template signature break noted in CLAUDE.md.

---

## Verdict

**GO with follow-ups.** Merge to `main` is recommended. All spec requirements are behaviorally verified by passing tests; the security gates are exhaustively covered including the XFF-ignore invariant; NFRs are all met; docs are current and accurate; the working tree is clean. The two follow-ups are documentation / hygiene items that can be filed as tickets and picked up outside the sprint's critical path.

---

## Return Envelope

```yaml
status: verified
verdict: GO_with_followups
change: sprint-e-5-tunnel-manager
report_path: /Volumes/PortableSSD/orch/docs/design/sprint-e-5-tunnel-manager-verify.md
requirement_coverage:
  pass: 17
  partial: 0
  missing: 0
  deviation: 0            # UI layout deviation is additive (see D#9 row); does not violate the requirement
suite:
  passed: 993
  skipped: 2
  failed: 1               # pre-existing: test_router.py::test_validate_all_models_passes_on_shipping_router
  duration_s: 24.55
followups:
  - id: FU-1
    kind: docs
    summary: reconcile tasks.md §5.7 with the shipped TunnelPage route (docs drift only)
  - id: FU-2
    kind: hygiene
    summary: migrate @app.on_event("startup") to lifespan handlers to unblock FastAPI >=0.116 upgrade
next_recommended: sdd-archive
```
