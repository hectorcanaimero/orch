# Orchestrator — Archive Report

**Change**: orchestrator
**Started**: 2026-08-19
**Archived**: 2026-08-19
**Final status**: GREEN
**Tests**: 119/119 passing

## Timeline

- Explore → proposal → spec + design (parallel) → tasks → apply batches A-F → verify (YELLOW) → R-021 closeout → archive.

## Delivered artifacts

### Code (all under orchestrator/)
- orch.py — CLI + main loop (~1170 LOC)
- state.py — RunFile, EventLog, SpendLog, flock, resume reconciliation
- models.py — dataclasses (Task, RouteEntry, Dispatch, RunState, SpendEntry, EventEntry)
- router.py — model → backend routing + fail-fast validation
- task_queue.py — DAG resolver (Kahn's variant)
- prompt_builder.py — template renderer
- dispatcher.py — Backend Protocol + Claude/Codex/Opencode adapters + cost extraction + version-drift detection
- checkpoints.py — semi-mode gate (predicates + stdin prompt)
- spend_reader.py — dashboard read helper
- __main__.py — python -m orchestrator entry
- model_router.yaml — 16 model strings → backend routing
- config.yaml — concurrency, timeouts, strict_files_phases
- tests/ — 119 tests, all passing

### Docs (all under orchestrator/)
- proposal.md, spec.md, design.md, tasks.md, verify-report.md, archive-report.md

### Dashboard integration
- dashboard/index.html — budget-panel section, loadSpend(), scheduleSpendPoll(), UTC date handling

## Deliverables vs. proposal scope

| Proposal item | Delivered |
|---|---|
| DAG walk | ✓ |
| Per-provider concurrency | ✓ |
| Auto + semi modes | ✓ |
| Resumability | ✓ (--resume + os.kill + git-diff heuristic) |
| Dashboard budget panel | ✓ |
| Retry-once (FR-D-4) | ✓ (added in R-021) |
| Version-drift fallback (FR-D-7) | ✓ (added in R-021) |

## Metrics

- Tasks: 20 planned (R-001..R-020) + 1 follow-up (R-021) = 21 landed
- Batches: 6 (A/B/C/D/E/F) + 1 closeout
- Tests: 0 → 119
- Files: ~15 Python + 2 YAML + 6 markdown + 1 HTML section
- Real gaps found in verify: 3 (all closed pre-archive)
- Spec patches applied: 3 (FR-STATE-7 event names, FR-D-2 argv drifts, AS-08 dry-run event clarification)

## What was not delivered (explicit non-goals)

- Multi-machine / distributed dispatch
- Remote queue backend
- Retry-with-different-model (only retry-once with same or fallback model)
- TUI or web UI for semi-mode (stdin only)
- Auto-detection of model registry via CLI probe (uses config-driven fallback instead)

## Handoff notes for future maintainers

- Real `claude`/`codex`/`opencode` CLIs must be installed and authenticated. Router assumes `opencode` cannot serve Anthropic models — do not add Anthropic routes to opencode without confirming auth.
- `rich` is an optional dependency; plain-text fallback works.
- `pyproject.toml` declares `orch = "orchestrator.orch:main"` script; `pip install -e orchestrator/` enables `orch` on PATH.
- Dashboard fetches `../orchestrator/state/spend-<UTC-today>.jsonl` — serve from repo root, not from `dashboard/`.
- The 334-task project backlog (`v2/tasks.json`) is what the orchestrator EXECUTES — do not confuse with `orchestrator/tasks.md` (the SDD task breakdown for the orchestrator itself).

## Next steps (not in scope of this archive)

- First real end-to-end run: `python3 -m orchestrator --mode semi --max-tasks 3` against a couple of easy tasks to smoke-test with real CLIs.
- Optional: install `rich` for pretty tables.
- Optional: monitor `orchestrator/state/spend-<date>.jsonl` and dashboard budget panel during real runs.

## Sign-off

Change closed GREEN. Ready to execute real dispatches.
