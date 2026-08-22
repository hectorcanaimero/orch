# Sprint E-5 — Tunnel Manager (archive)

> Status: **closed** · Verdict: **GO with follow-ups** · Archived: 2026-08-22
> Backend: docs (engram unavailable — `docs/design/*.md` is the source of truth)
> Change name: `tunnel-manager` · Sprint id: `E-5`
> Branch at archive: `sprint-e3/spa-spike`

## Convention note

Sprints B, C, D, E-1, E-2 in this repo shipped a single consolidated `sprint-*.md`
per sprint (no separate proposal/spec/design/tasks/verify artifacts). Sprint E-5 is
the first sprint in the repo to use the full multi-artifact SDD chain, so there is
no prior archive precedent. Naming for this archive follows the sibling convention
already established by the other Sprint E-5 artifacts (`sprint-e-5-tunnel-manager-*.md`),
producing `sprint-e-5-tunnel-manager-archive.md` in the same folder. **New pattern
established:** future multi-artifact sprints archive alongside their siblings under
`docs/design/` rather than being moved or renamed.

## Artifacts (frozen, referenced only — not moved)

| Kind | Path |
|---|---|
| Proposal | `docs/design/sprint-e-5-tunnel-manager.md` |
| Spec | `docs/design/sprint-e-5-tunnel-manager-spec.md` |
| Design | `docs/design/sprint-e-5-tunnel-manager-design.md` |
| Tasks | `docs/design/sprint-e-5-tunnel-manager-tasks.md` (amended for FU-1 on archive) |
| Verify report | `docs/design/sprint-e-5-tunnel-manager-verify.md` |
| Archive report | `docs/design/sprint-e-5-tunnel-manager-archive.md` (this file) |

## Timeline

| Date | Commit | Scope |
|---|---|---|
| 2026-08-22 | `97e2ed0` | docs(sprint-e-5): proposal + design + spec |
| 2026-08-22 | `8cdaa49` | docs(sprint-e-5): task breakdown |
| 2026-08-22 | `cd35757` | refactor(procutil): extract shared `pid_alive` (D1) |
| 2026-08-22 | `645d012` | feat(dashboard/tunnel): backend core (manager, providers, deps, procutil use) |
| 2026-08-22 | `cc38312` | feat(dashboard/tunnel): endpoints + gate integration (`/capabilities`, `/status`, `/start`, `/stop`, `/logs`) |
| 2026-08-22 | `add0ea6` | feat(dashboard/tunnel): `orch doctor` checks + `auto_start` self-probe |
| 2026-08-22 | `51cb831` | feat(frontend/tunnel): `TunnelPanel` with capability-driven mounting |
| 2026-08-22 | `51838c7` | docs(sprint-e-5): pinggy workflow section + bundled `dashboard.yaml` tunnel block + CLAUDE.md refresh |
| 2026-08-22 | `69d0197` | docs(sprint-e-5): verification report (GO with follow-ups) |

Total: 9 commits, single-day sprint. All merged onto `sprint-e3/spa-spike`; ready to
be squashed / rebased into `main` per repo PR convention.

## Delivered scope (against proposal success criteria)

| Success criterion | Status | Evidence |
|---|---|---|
| Operator on `http://127.0.0.1:7420` starts pinggy tunnel, sees URL within 5s of `POST /start` | met | `manager.py::start()` + `_start_reader_thread()` extracts URL via provider regex; status is polled by `useTunnelStatus` at 1s cadence while `starting|running`. |
| Same operator hitting dashboard through tunnel URL sees NO panel and 403 on `POST /start` | met | `require_loopback_host` at `deps.py`; verified by `test_gate_order_config_wins_over_host` and `_403_from_tunnel_host` fixtures. |
| Stakeholder-token requests get 403 on all control endpoints + `can_control:false` from `/capabilities` | met | `require_operator_profile` composed before host gate; `test_capabilities_returns_not_operator_for_stakeholder`. |
| With `tunnel.enabled:false`, every `/api/tunnel/*` route returns 404 and SPA renders no panel | met | `server.py:484-494` (manager only instantiated when enabled); `TunnelPage.tsx:59` conditional mount; `test_*_404_when_disabled` (4 fns). |
| `orch doctor` reports autossh presence + current tunnel config validity | met | `preflight.py` new checks (Batch 4.1, 4.2); verified in Batch 6.3 smoke. |
| Full pytest suite stays green; new tests cover host/profile gates, allowlist rejection, URL parsing | met | 993 passed, 2 skipped, 1 pre-existing failure (unrelated `test_router.py`). Suite grew from ~680 to 993 tests over the sprint. |

Every proposal-level success criterion is met. Verify report gives 17/17 requirement
coverage (11 TUN + 3 NFR + 3 D-decisions) with 0 missing, 0 partial.

## Deviations from spec / design / tasks

Synthesized from the verify report (§Coherence and §Follow-ups) and from FU-1 reconciled
on this archive.

### Code deviations from tasks.md (both non-blocking, additive)

1. **UI mount location changed from `BoardPage.tsx` to a dedicated `/tunnel` route** (verify §D#9 row + FU-1).
   - `tasks.md §5.7` originally described mounting `TunnelPanel` conditionally inside `BoardPage.tsx`.
   - Shipped code introduces `frontend/src/pages/TunnelPage.tsx` at the `/tunnel` route with a
     nav entry in `frontend/src/components/layout/AppLayout.tsx`.
   - Reason: `BoardPage.tsx` is now a full-height ExcaliDash iframe with no sibling slot to host a card.
   - Impact: **improvement** — dedicated URL + cleaner IA. The D#9 invariant
     ("panel not in the React tree when `can_control === false`") is preserved verbatim
     at `TunnelPage.tsx:59` (`{caps && caps.can_control ? <TunnelPanel/> : caps ? <Alert/> : null}`).
     No CSS-hidden fallback anywhere in the tree.
   - Reconciled on archive: `tasks.md §5.3`, §5.7, and the critical path line updated to
     reference `TunnelPage` / `/tunnel` / `AppLayout.tsx`. Historical context for the
     BoardPage-cannot-host decision retained inside §5.7 for future readers.

### Docs deviations (reconciled during sprint)

2. **`_pid_alive` helper duplication consolidated into `procutil.py`** (verify §D1 row, commit `cd35757`).
   - Design D1 called for extraction to a shared helper before manager code was written; done
     in Batch 1.5 as prescribed. Not a deviation from D1 — a deviation from the pre-D1 world.
   - Impact: one canonical `pid_alive` implementation across `manager.py` and the reconciler
     path. Reduces future drift risk.

3. **Test count in `CLAUDE.md` refreshed** (commit `51838c7`).
   - `CLAUDE.md` previously stated "~680 tests." Sprint E-5 added ~313 tests (tunnel manager,
     endpoints, providers, deps, procutil, auto-start, doctor). New baseline noted in docs.

### Deviations from spec: NONE.

Every TUN-1..11 and every NFR-1..3 is behaviorally verified by tests. The XFF-ignore
invariant (S:TUN-3) is exhaustively covered (2×2×2 matrix in `test_tunnel_deps.py` plus
`test_gate_order_config_wins_over_host`).

## Follow-ups

| ID | Kind | Summary | Status at archive |
|---|---|---|---|
| **FU-1** | docs | Reconcile `tasks.md §5.7` with shipped `TunnelPage` route + `AppLayout` nav entry. | **DONE** — amended on this archive (see §Deviations #1). |
| **FU-2** | hygiene | Migrate `@app.on_event("startup")` → FastAPI `lifespan` handlers to unblock the `fastapi>=0.116` upgrade (CLAUDE.md gotcha). 358 deprecation warnings are all pre-existing. | **DEFERRED** — non-blocking. Suggest a dedicated cleanup sprint. |
| Pre-existing router failure | test hygiene | `orchestrator/tests/test_router.py::test_validate_all_models_passes_on_shipping_router` fails on baseline `main`, independent of Sprint E-5. | **DEFERRED** — pre-existing, out of scope. Track separately. |

FU-1 was resolved as part of the archive itself (Step 1 of the archive workflow).
FU-2 and the router failure are flagged for future sprints; neither blocks merge.

## Non-blockers surfaced during the sprint

- **`_pid_alive` duplicate consolidated in Batch 1.5** — noted above under Deviation #2. No lingering copies.
- **`CLAUDE.md` test-count refresh (~680 → 993)** — noted above under Deviation #3.
- **`autossh` availability on the host is a runtime prerequisite** — surfaced by `orch doctor`
  (`preflight.py`) and by `GET /api/tunnel/capabilities` returning `reason:"autossh_missing"`
  after all gates pass. Documented in `docs/DASHBOARD-PROFILES.md` (Batch 6.1).
- **`httpx` at runtime for `auto_start` self-probe** — verified available (already pulled in
  transitively via `fastapi`/`uvicorn`); no new runtime dep introduced (S:TUN-NFR-2 satisfied).

## Merge readiness

**Yes — safe to merge to `main`.** All acceptance criteria met, 17/17 requirements verified,
993 tests pass on `sprint-e3/spa-spike` (single pre-existing failure is unrelated to E-5),
frontend `tsc --noEmit` clean, zero new runtime dependencies, feature default-off via
`tunnel.enabled:false` in the bundled `dashboard.yaml` (S:TUN-NFR-3 rollback path).

## SDD cycle status

Planned → specced → designed → tasked → applied → verified → **archived**.
Ready for the next change.
