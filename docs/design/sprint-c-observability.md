# Sprint C — Observability

Branch: `sprint-c/observability` · Closes: #6 (all six sub-ideas) + phase 1 of #13

## Guiding principles
- Every new command flows through `StateBackend`. No file-shape assumptions.
- New CLI commands mirror existing pattern in `orchestrator/orch.py` (`_run_<name>_subcommand`, string-match dispatch in `main()`, entry in `_SUBCOMMANDS`).
- Human output uses `rich` when available; degrades to plain print.
- JSON output is compact single-object dumps to stdout, `json.dumps(default=str)`.
- No new runtime deps. `orch graph` is inline SVG in a single HTML file, no CDN.

## Commands

### `orch status [--json] [--only GLOB] [--status STATUSES]`
Flagship. Table (human) or structured JSON. Columns: ID · STATUS · BACKEND/MODEL · LAST EVENT · COST · DURATION. Header: `Project · backend · totals · run summary`. See design section 1.1 for full mock output and JSON schema.

Calls: `backend.get_all_task_status()`, `backend.list_runs()`, `backend.load_run()`, `backend.iter_all_spend()`, NEW `backend.get_task_last_events()`. Also `load_tasks(paths.tasks_json)` for phase/title/model, `router.load_router(paths.model_router_yaml)` for backend/cli_model.

### `orch tasks [--status STATUSES] [--only GLOB] [--json]`
Thin listing. Columns: ID · STATUS · BACKEND/MODEL · DEPS · PHASE. `--status` accepts comma-separated set.

### `orch events <task-id> [--tail N] [--json] [--run RUN_ID]`
Event stream for one task. Default tail=20. Iterates `backend.iter_events(task_id=…, limit=…)` (new extended signature).

### `orch logs <task-id> [--tail N] [--all]`
Raw log tail. Default tail=200. Reads `paths.state_dir / "logs" / f"{task_id}.log"`. Exits 2 if file missing. NO `--follow`.

### `orch dry-run --json`
Add `--json` combined-flag to existing `--dry-run`. Refactor `_print_plan` → `_build_plan_rows` (pure) + `_print_plan_table` (rich renderer) + `_print_plan_json` (dumps). Validator: `--json` requires `--dry-run`.

### `orch graph [--out plan.html] [--project-root PATH] [--open] [--only GLOB]`
Self-contained HTML file with inline SVG. Phase swimlanes (columns), status colors, dependency edges (cubic Bézier), backend/model badges. Zero external deps.

## Router WARN dedup (`orch.py:_warn_fallback_routes`)
Delete `print(msg, file=sys.stderr)` at line 376. Change per-route log.warning to log.info (severity fix). Non-verbose default: single summary "N route(s) with fallback configured". Add `-v/--verbose` and `-q/--quiet` flags to argparser.

Grep audit: `rg "log\.(warn|warning|error|info).*\\n.*print" orchestrator/` — check no other double-emit. Also audit `warn_undersized_presets` in `budget.py`.

## End-of-run summary
Hook at `orch.py:2536` (before final `return 0`/`return 1`). Print unconditionally after clean drain. Skip on: `--dry-run`, `return 130` (SIGINT), config-error early exit. Uses in-memory `task_costs` dict + local attempt counter maintained in the reap loop. Rich table if available; plain fixed-width fallback.

## `orch graph` implementation
New module `orchestrator/graph.py` (~250 lines):
- `build_html(snapshot, tasks) -> str`
- `_layout(tasks) -> dict[task_id, (x, y)]` — phase columns × id-sorted rows within phase
- `_svg_nodes(...)`, `_svg_edges(...)`, `_svg_legend()` — pure string builders

Node: 140×44px rounded rect, 20px horizontal gap between phases, 12px vertical gap between rows. Status→color map centralized:
```python
STATUS_COLORS = {
    "done": "#22c55e", "in-progress": "#3b82f6", "todo": "#475569",
    "backlog": "#334155", "blocked": "#ef4444", "blocked-by-budget": "#f59e0b",
}
```

`blocked-by-budget` shown only when in-memory `defer_reasons` populated (i.e., graph rendered mid-run from same process). Documented limitation.

Edges: cubic Bézier with `dx = (x2-x1)/2` control offset. No overlap avoidance in Sprint C. Arrowhead via SVG `<marker>`.

Layout for 200 tasks: ~100 KB HTML file. Above 500 → recommend `--only`.

## New `StateBackend` methods

### REQUIRED: `get_task_last_events(task_ids=None) -> dict[task_id, event_row]`
Sqlite: single indexed self-join `WHERE e.id = latest.max_id`. File: reverse-scan events-*.jsonl newest-first, dict-fill.

### REQUIRED: extend `iter_events` signature
```python
def iter_events(self, run_id=None, since_id=None, task_id=None, limit=None)
```
Backwards-compat kwargs. Sqlite adds `AND task_id=? LIMIT ?`. File streams JSONL with inline filter + yield-counter.

### NOT ADDING: `get_active_run()`
Use `list_runs()[0] if list_runs() else None` in helper. Keep out of Protocol.

## Shared aggregation module
New `orchestrator/observability.py`:
```python
def build_status_snapshot(paths, cfg) -> dict:
    """Pure aggregation used by orch status and orch graph."""
```
Returns the JSON shape documented in status spec. `orch graph` calls this in-process (do NOT shell out to `orch status --json`).

## Product decisions frozen during design

1. Aggregate task statuses come from `backend.get_all_task_status()` (current state). "Latest run" info comes from most recent live run only. Historical runs → out of scope.
2. `last_event` for a task never dispatched → `None` in JSON, `—` in human output.
3. `orch logs --follow` → NOT in Sprint C (deferred).
4. `orch graph` ceiling ~500 tasks (documented). Use `--only` for larger projects.
5. `SpendEntry` gets NO `run_id` column in Sprint C. End-of-run summary uses in-memory `task_costs` dict from main loop. Follow-up issue for schema bump.
6. `defer_reasons` remains in-memory-only. `orch status` from disk shows `defer_reason: null` for reads outside the running process. Persistence deferred.
7. `--json` on `orch dry-run` validated: `--json` requires `--dry-run` (`parser.error`).
8. Router WARN "WARN" severity → change to INFO in non-verbose path (informational, not a warning).

## Commit breakdown (≤6 atomic)

1. `refactor(observability): extract build_status_snapshot + get_task_last_events + extended iter_events`
2. `feat(cli): orch status + orch tasks (human + JSON)`
3. `feat(cli): orch events + orch logs subcommands`
4. `feat(cli): --json on orch dry-run + end-of-run summary table`
5. `fix(router): dedup fallback WARN + --verbose / --quiet / ORCH_LOG_LEVEL`
6. `feat(cli): orch graph HTML/SVG snapshot + docs`
