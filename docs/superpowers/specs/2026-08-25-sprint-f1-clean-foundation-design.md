# Sprint F-1: Clean Foundation Design

**Date:** 2026-08-25  
**Status:** Approved  
**Scope:** Three independent workstreams that together eliminate split-brain state, establish proper tooling conventions, and reduce agent token waste.

---

## Context

orch is a single-user, local task orchestrator that dispatches AI agents across a DAG of tasks. As of v0.6.6, there are three structural pain points:

1. **Two sources of truth**: `tasks.json` owns DAG/model assignments; SQLite owns runtime status. Agents must touch two backends to do anything, and the `tasks_json_precedence` config exists solely to manage this friction.
2. **Wrong directory convention**: `orch init` creates `orchestrator/` in the target project — a visible, non-hidden directory that pollutes the project root and requires explicit gitignoring. Unix tooling convention is to use dotfiles/dotdirs (`.git`, `.github`, `.claude`).
3. **Token waste**: `prompt_builder.py` duplicates the `{files}` list and includes non-actionable metadata (phase, estimate, reason) in every dispatch prompt.

---

## Workstream 1 — Rename Runtime Dir to `.orchestrator/`

### Decision

`orch init` creates `.orchestrator/` in the target project root instead of `orchestrator/`. The Python source package (`orchestrator/` in the orch repo itself) is unchanged — it is a Python package, not a runtime directory, and cannot start with a dot.

### Rationale

Follows the established Unix convention for tooling directories: `.git`, `.github`, `.claude`, `.vscode`, `.idea`. Hidden by default in file explorers, clearly signals "infrastructure, not project code", and eliminates the need for an explicit `.gitignore` entry (though we still add one for clarity).

### Files Changed

| File | Change |
|------|--------|
| `orchestrator/paths.py` | `RUNTIME_DIR = ".orchestrator"` (or equivalent constant) |
| `orchestrator/init_cmd.py` | Directory creation uses `.orchestrator/` |
| `orchestrator/templates/gitignore.tmpl` | `.orchestrator/` replaces `orchestrator/state/*` |
| `orchestrator/migrate.py` | Path refs updated |
| `docs/MANUAL.{en,es,pt}.md` | Upgrade note + new path |

### Backward Compatibility

Existing projects with `orchestrator/` continue to work. `orch init` only affects new projects. Upgrade path: manually rename `orchestrator/` → `.orchestrator/` and update `.gitignore`. Document this as a breaking change in the changelog.

### gitignore Template (new)

```gitignore
# orch runtime — local tooling, never checked in
.orchestrator/

# orch agent context — commit this
# !AGENTS.md  (uncomment if you added AGENTS.md to a broader ignore)

# Editor / OS
.DS_Store
*.swp
.idea/
.vscode/

# Python
__pycache__/
*.pyc
*.pyo
.venv/
venv/
.pytest_cache/
*.egg-info/
```

---

## Workstream 2 — SQLite as Single Runtime Owner

### Decision

Introduce `tasks_definition` table (migration `003`). `tasks.json` becomes the atomize input format only — never read at runtime. Remove `tasks_json_precedence` from config schema. Add `orch task set` CLI command.

### Problem Statement

`tasks_json_precedence: deps-only` exists to paper over the fact that SQLite owns status but `tasks.json` owns model assignments and DAG structure. This means:
- Agents must edit `tasks.json` directly to change a model assignment (bypasses orch tracking)
- No CLI command to mark a task done without a full dispatch
- Dashboard reconciles two backends on every request
- `tasks_json_precedence` is a config knob that only makes sense if you know the history

### New Schema — Migration `003_task_definition.sql`

```sql
PRAGMA user_version = 3;

CREATE TABLE IF NOT EXISTS tasks_definition (
  project_id   TEXT NOT NULL REFERENCES projects(project_id) ON DELETE CASCADE,
  task_id      TEXT NOT NULL,
  title        TEXT NOT NULL DEFAULT '',
  model        TEXT,
  backend      TEXT,
  deps_json    TEXT NOT NULL DEFAULT '[]',
  spec_ref     TEXT,
  phase        INTEGER,
  estimate_h   REAL,
  reason       TEXT,
  files_json   TEXT NOT NULL DEFAULT '[]',
  updated_at   TEXT NOT NULL,
  PRIMARY KEY (project_id, task_id)
);
CREATE INDEX IF NOT EXISTS idx_tasks_def_project ON tasks_definition(project_id);
```

### Atomize Behavior (updated)

`orch atomize --apply` performs an UPSERT into `tasks_definition` for all parsed tasks:
- **New tasks**: INSERT into `tasks_definition` + INSERT OR IGNORE into `tasks_runtime` (status=`backlog`)
- **Existing tasks**: UPDATE `tasks_definition` (title, model, deps, spec_ref, etc.) — never touches `tasks_runtime`
- **Removed tasks** (in tasks.json, not in specs): listed in diff, left in DB as-is (same behavior as today)

`tasks.json` continues to be written by `orch atomize --apply` for human readability, but is never read at runtime after this change.

### Runtime Read Path (updated)

All code that currently reads `tasks.json` at dispatch time is updated to query:

```sql
SELECT d.*, r.status, r.started_at, r.finished_at, r.attempts,
       r.last_backend, r.last_model, r.comments_json, r.updated_at
FROM tasks_definition d
JOIN tasks_runtime r USING (project_id, task_id)
WHERE d.project_id = ?
```

### Config Schema (updated)

`tasks_json_precedence` is removed from `config.yaml` and the config schema. SQLite is always authoritative. Projects that still have the key in their `config.yaml` have it silently ignored (no error, backward compatible).

### New Command: `orch task set`

```
orch task set --id <TASK_ID> [--model <MODEL>] [--status <STATUS>] [--backend <BACKEND>]

Options:
  --id        Task ID (required), e.g. F1.1.T3
  --model     Assign model → writes to tasks_definition.model
  --status    Set status → writes to tasks_runtime via set_task_status()
  --backend   Assign backend → writes to tasks_definition.backend

Examples:
  orch task set --id F1.1.T3 --status done
  orch task set --id F1.1.T3 --model claude-sonnet-4-6
  orch task set --id F1.1.T3 --model claude-sonnet-4-6 --status done
```

**Validation:**
- `--id` must exist in `tasks_definition` for this project (exit 1 if not found)
- `--status` must be a valid status value; transition rules from `_STATUS_TRANSITIONS` apply
- At least one of `--model`, `--status`, `--backend` is required

**Exit codes:** 0 = success, 1 = task not found, 3 = invalid status transition (matches existing convention in sqlite_backend.py)

---

## Workstream 3 — AGENTS.md + prompt_builder fix

### AGENTS.md Generation

`orch init` generates `AGENTS.md` at the project root. This file IS committed to git (not in `.orchestrator/`, not gitignored) because Claude Code and other agent runtimes read it automatically at session start, providing zero-cost context to every agent session.

`orch atomize --apply` updates `AGENTS.md` (refreshes paths and caps from current config).

**Template** (generated dynamically from `config.yaml` values at init/atomize time):

```markdown
# Orch Project Context

> Auto-generated by `orch init`. Refresh with `orch atomize --apply`.
> This file is read automatically by Claude Code at session start.

## State backend

- Runtime state: SQLite at `.orchestrator/state/<project-id>/orch.db`
- `tasks_definition` (static): model, backend, deps, spec_ref, files, phase, estimate
- `tasks_runtime` (dynamic): status, attempts, last_model, last_backend
- `tasks.json`: atomize input only — **never edit runtime status here**

## Check real task status

```bash
sqlite3 .orchestrator/state/<project-id>/orch.db \
  "SELECT task_id, status, last_model FROM tasks_runtime
   WHERE status != 'done' ORDER BY task_id;"
```

## Key commands

| Action | Command |
|--------|---------|
| View status | `orch status` |
| Set model/status | `orch task set --id TASK --model MODEL --status done` |
| Capture finding | `orch findings capture --type bug\|feature\|fix --about orch\|project --summary "..." --evidence "..."` |
| Publish finding | `orch findings publish <id> --repo <repo> --yes` |
| Re-atomize | `orch atomize --apply` |

## Provider concurrency caps

| Provider | Max concurrent |
|----------|---------------|
| claude | 3 |
| opencode | 6 |
| codex | 2 |

## Model router

See `.orchestrator/model_router.yaml` for backend→model mappings.

## Findings minimum confidence to auto-publish

`medium` (set in `config.yaml → findings.min_confidence`)
```

### prompt_builder.py Fix (Issue #46)

Remove non-actionable metadata and deduplicate `{files}`.

**Before (lines 169–184):**
```
Phase: {phase}  Estimate: {estimate_hours}h
Model reason: {reason}
Files you may write: {files}
...
- Do NOT touch files outside {files} unless the spec explicitly requires it.
```

**After:**
```
Files you may write: {files}
...
- Do NOT touch files outside the list above unless the spec explicitly requires it.
```

**Token savings:** ~350–400 chars (~120–150 tokens) per dispatch. Across a 150-task project cycle: ~18–22K tokens saved at zero cost.

---

## Execution Order

```
WS1 (rename .orchestrator/)  ──────────────────────────────► commit
WS2 (SQLite single owner)     ──────────────────────────────► commit
WS3 (AGENTS.md + prompt fix)  depends on WS1 path, runs after ► commit
```

WS1 and WS2 are independent and can run in parallel (different files). WS3 depends on WS1 for the `.orchestrator/` path in the AGENTS.md template.

---

## Testing Requirements

| Workstream | Tests Required |
|-----------|---------------|
| WS1 | `test_init.py`: assert `.orchestrator/` created, `orchestrator/` not created; gitignore template contains `.orchestrator/` |
| WS2 | `test_sqlite_backend.py`: migration 003 applies; atomize seeds `tasks_definition`; `orch task set` writes correct table; `tasks_json_precedence` key ignored without error |
| WS3 | `test_prompt_builder.py`: phase/estimate/reason absent from rendered prompt; `{files}` appears once; `test_init.py`: AGENTS.md generated at project root |

**Baseline:** 1061 passed + 2 skipped + 1 pre-existing failure. New work must not regress the green count.

---

## Out of Scope

- Dashboard SPA changes (no UI changes in this sprint)
- File backend changes (file backend users unaffected; `.orchestrator/` rename is init-only)
- `orch migrate` for renaming existing `orchestrator/` dirs (documented as manual step)
- Issue #41 (VITE_API_BASE_URL) — separate sprint
