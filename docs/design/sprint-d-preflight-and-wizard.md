# Sprint D — Preflight + Interactive Wizard

Branch: `sprint-d/preflight-and-wizard` · Closes: #9 (`orch doctor`), #10 (`orch validate`), #16 (`orch init` interactive wizard)

## Guiding principles
- Every new probe/validator flows through a shared, pure-function module (`orchestrator/preflight.py`). No CLI code in there; no I/O beyond what each check strictly needs. Trivially unit-testable.
- CLI wrappers (`orch doctor`, `orch validate`) mirror the existing subcommand pattern in `orchestrator/orch.py` (`_run_<name>_subcommand`, string-match dispatch in `main()`, entry in `_SUBCOMMANDS`).
- Human output uses `rich` when available; degrades to plain print.
- JSON output is compact single-object `json.dumps(default=str)` to stdout.
- **No new runtime deps.** The wizard uses stdlib `input()` with a `prompt()` helper (validation loops + choices). No `questionary`, no `prompt_toolkit`.
- Existing batch `orch init` behavior is preserved byte-identically. Every existing `test_init.py` case still passes.

## Contract summary

| Command | Purpose | Exit codes |
|---|---|---|
| `orch doctor` | Runtime probes: backends, scripts, jq, state | 0 ok · 1 warn · 2 error |
| `orch validate` | Static graph: schema, deps, cycles, routes, presets | 0 ok · 1 warn · 2 error |
| `orch init` (no flags) | Interactive wizard (opt-in, requires TTY) | 0 ok · 1 hard error |
| `orch init PATH` | Batch scaffolder (unchanged from Sprint 9) | 0 ok · 1 conflict |

## Module layout

### `orchestrator/preflight.py` (~800 LOC, new)
Two families of pure functions:

**Runtime probes** (used by `orch doctor`):
- `check_backends(tasks, router, max_workers=4)` — `shutil.which(cli)` + `<cli> --version` + cheap auth probe per referenced backend, wrapped in `ThreadPoolExecutor` with per-probe 5s timeout.
- `check_config_files(config_yaml, budgets_yaml, router_yaml, tasks_json, budgets_preset)` — parseability + active-preset presence.
- `check_scripts(scripts_dir)` — `task-*.sh` present + executable + `jq` on PATH.
- `check_state_backend(state_dir, backend, sqlite_path, expected_schema_version)` — dir writable + (sqlite only) DB opens + schema_version match. Skipped with explanatory row when `backend == "file"`.

**Static validators** (used by `orch validate`):
- `validate_schema(tasks)` — required fields + sane types.
- `validate_dependencies(tasks)` — `id` in `dependencies[]` must exist; self-loop reported as `dep.cycle`.
- `validate_cycles(tasks)` / `find_cycles(tasks)` — DFS 3-coloring reports the ACTUAL cycle path (`A -> B -> C -> A`), not just "cycle exists".
- `validate_routes(tasks, router_keys)` — every `task.model` resolves.
- `validate_files_writable(tasks, project_root)` — opt-in (`--files`).
- `validate_preset_sanity(budgets_yaml, preset, typical)` — reuses Sprint A `warn_undersized_presets`, wrapped as warn severity.
- `validate_config_shape(config_yaml)` — enough to catch dangerous typos (state.backend enum, concurrency mapping).
- `validate_graph(tasks, router_keys)` — aggregate of the schema/deps/cycles/routes validators.

**Result types**:
- `CheckResult(name, status, detail, remediation)` — status ∈ {ok, warn, error, skip}.
- `ValidationError(task_id, field, kind, message, remediation, severity)` — severity ∈ {error, warn}.

**Aggregation**:
- `summarize_checks(results) -> dict[str, int]`
- `exit_code_for_checks(results)` / `exit_code_for_errors(errors)` — encode the 0/1/2 convention.

## `orch doctor`

### CLI
```
orch doctor [--project-root PATH] [--project-id ID] [--config PATH] [--json] [--only CHECK_NAME]
```

`--only` is a substring filter (e.g. `--only backend` narrows to backend probes).

### Checks (in emitted order after grouping)
1. `config.parse`, `budgets.parse`, `budgets.preset`, `router.parse`, `tasks.parse`
2. `scripts.task-*.sh` (3 checks) + `jq.present`
3. `backend.<name>` + `backend.<name>.auth` (only for backends referenced by `tasks.json` when the router resolves; otherwise defaults to `{claude, codex, opencode}`)
4. `models.resolve`
5. `state.dir.writable`, `state.db.accessible`

### Concurrency
`ThreadPoolExecutor(max_workers=4)` per product decision #6. Version + auth probes for each backend run in parallel so a hung `opencode` doesn't block `claude`. Per-probe timeout = 5s.

### Auth probes (product decision #9)
- **opencode**: `opencode auth list` (read-only, no cost). Returns `error` when exit != 0 with a "run `opencode auth login`" remediation.
- **claude**: no cheap non-interactive auth probe exists. Emit `skip` with explanation.
- **codex**: same as claude.

### Human render
Rich table with colored `✓ ⚠ ✗ ○` glyphs, summary line, grouped remediation section at bottom. Plain-print fallback when rich isn't available.

### JSON schema
```json
{
  "project": {"id": "...", "root": "..."},
  "backend": "file|sqlite",
  "checks": [
    {"name": "backend.opencode", "status": "ok", "detail": "opencode 1.18.19 at /usr/local/bin/opencode", "remediation": null}
  ],
  "summary": {"ok": N, "warn": N, "error": N, "skip": N},
  "exit_code": 0
}
```

## `orch validate`

### CLI
```
orch validate [--project-root PATH] [--project-id ID] [--config PATH] [--json] [--files]
```

### Validators (see preflight module list above)
`--files` toggles the parent-dir-writable check for every `task.files[]` entry. Off by default because file trees on developer laptops move around a lot (would false-positive on every branch).

### Human render
Errors grouped by `kind` (one small table per kind), colored by severity, summary line + optional remediation section.

### JSON schema
```json
{
  "project": {"id": "...", "root": "..."},
  "errors": [
    {"task_id": "T-A", "field": "model", "kind": "route.unresolved", "message": "...", "remediation": null, "severity": "error"}
  ],
  "summary": {"total": N, "by_kind": {"dep.cycle": 1}, "errors": N, "warnings": N},
  "exit_code": 0
}
```

### Cycle path reporting
`find_cycles()` runs iterative DFS 3-coloring, tracking `parent[]`. When a back-edge to a GRAY node is discovered, it walks the parent chain back to the target and canonicalizes the rotation (smallest id first) so the same cycle isn't reported twice from different entry points. Message form: `dependency cycle: A -> B -> C -> A`.

## `orch init` — interactive wizard

### Detection matrix (product decision #2)
```
if --interactive:
    wizard  (require TTY)
elif --non-interactive:
    batch   (require PATH)
elif any scaffolder flag (PATH / --force / --sdd / --project-name):
    batch
elif stdin.isatty():
    wizard
else:
    argparse error — "interactive mode requires a TTY on stdin"
```

Product decision #3: on non-TTY (CI, piped stdin) with no scaffolder flags, we error out immediately with a helpful message. This prevents the wizard from hanging in unattended CI runs.

### Wizard flow (stdlib input only)
1. `project_id` (regex `^[a-z0-9][a-z0-9_-]*$`)
2. `project_root` (default: `cwd / project_id`; parent must exist + be writable)
3. Backend detection via `shutil.which` — informational, doesn't gate anything.
4. `state backend` (choice: file/sqlite; default file)
5. `budget preset` (loaded from the shipped `budgets.yaml` preset keys)
6. `spec_root` (default: `specs`)
7. Model tier picker — for each of `premium/standard/cheap`, show routes from shipped `model_router.yaml` grouped by tier and prompt for the default.
8. `sdd` (y/n) — optional openspec/ layout.
9. Per-file overwrite prompts when existing files clash (default: keep, product decision #4).
10. Scaffold via `orch_init(force=True)`.
11. Restore protected files (the ones user chose to keep) and skip their post-processing.
12. Post-process `config.yaml` (regex-edit `backend`/`budgets_preset`/`spec_root` in place, preserving the shipped comments so operators see the guidance in `$EDITOR`).
13. Post-process `tasks.json` `meta` (project id + `default_<tier>_model` hints).
14. Run `preflight.validate_graph()` inline and print green/red summary.
15. Print next-steps hint (`orch dry-run`, `orch atomize`, `orch doctor`).

### `prompt()` helper
```python
def prompt(message, *, default=None, validate=None, choices=None, input_fn=None) -> str
```
- `validate(value)` returns None on success or an error string to show.
- `choices` is enforced case-sensitively.
- Empty input returns `default` when set; otherwise re-prompts.
- `input_fn` defaults to a call-time lookup of `builtins.input` so `monkeypatch.setattr("builtins.input", ...)` in tests works.

### Backwards-compat guarantees
- `test_init.py` (13 batch tests) passes unchanged.
- `orch init PATH` behaves identically to Sprint 9.
- `orch.py:_run_init_subcommand` is now a 3-line delegator to `run_init_cli()` in `init_cmd.py`; the argparser + wizard live together in one place.

## Testing summary

| Suite | Count | Focus |
|---|---:|---|
| `test_preflight.py` | 39 | Unit coverage of every check/validator, cycle detection edge cases, mocked `shutil.which`. |
| `test_doctor_cmd.py` | 14 | JSON shape, exit codes 0/1/2, referenced-backends-only shortcut, `--only` filter, both state backends. |
| `test_validate_cmd.py` | 12 | Clean/self-loop/3-cycle path form/missing dep/unresolved route/bad config/undersized preset/`--files` on-off. |
| `test_init_wizard.py` | 22 | `prompt()` helper, wizard happy path, state/budget/spec_root post-processing, overwrite keep/replace, TTY fallback, batch regression. |
| `test_preflight_integration.py` | 12 | Real batch + wizard scaffolds feed into doctor + validate; edge cases: missing jq, non-executable scripts, malformed router, empty/malformed tasks, both backends, missing router agreement between doctor + validate. |

**Total new:** 99 tests. Baseline 489 → 588 with zero regressions. Both `ORCH_TEST_BACKEND=file` and `ORCH_TEST_BACKEND=sqlite` gates pass at 566 tests each.

## Non-goals for Sprint D
- Auto-fix ("orch doctor --fix") — remediation strings only.
- Interactive tasks.json editor — the wizard scaffolds an empty task list; operators use `orch atomize` to populate.
- Network probes (reach the provider API) — everything stays local + read-only.
- Full YAML round-trip for `config.yaml` post-processing — the regex edit preserves comments; a full round-trip would drop them.
- `orch validate --fix` — same rationale as doctor.

## Files touched

New:
- `orchestrator/preflight.py`
- `orchestrator/tests/test_preflight.py`
- `orchestrator/tests/test_doctor_cmd.py`
- `orchestrator/tests/test_validate_cmd.py`
- `orchestrator/tests/test_init_wizard.py`
- `orchestrator/tests/test_preflight_integration.py`
- `docs/design/sprint-d-preflight-and-wizard.md` (this doc)
- `docs/PREFLIGHT.md`

Modified:
- `orchestrator/orch.py` — added `_run_doctor_subcommand`, `_run_validate_subcommand`, helpers `_resolve_budgets_path`, `_resolve_sqlite_path`, `_render_doctor_report`, `_render_validate_report`; wired into `_SUBCOMMANDS`, `_print_subcommand_list`, `main()`. `_run_init_subcommand` is now a delegator to `init_cmd.run_init_cli`.
- `orchestrator/init_cmd.py` — added wizard, `prompt()` helper, `run_init_cli()`, post-processing helpers. Existing `orch_init()` unchanged.
- `README.md` — mention new subcommands.
