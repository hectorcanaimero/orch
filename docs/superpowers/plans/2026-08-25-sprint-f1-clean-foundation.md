# Sprint F-1: Clean Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rename the runtime dir to `.orchestrator/`, make SQLite the single runtime owner (add `tasks_definition` table + `orch task set` command), and generate `AGENTS.md` on init while cleaning up prompt_builder token waste.

**Architecture:** Three independent workstreams executed in order: WS1 renames the runtime directory in paths.py and init_cmd.py; WS2 adds a `tasks_definition` migration and updates atomize + SQLite backend; WS3 generates AGENTS.md at init time and strips non-actionable metadata from the dispatch prompt.

**Tech Stack:** Python 3.11+, SQLite (sqlite3 stdlib), pytest, argparse. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-08-25-sprint-f1-clean-foundation-design.md`

**Baseline:** 1061 passed + 2 skipped + 1 pre-existing failure. Never regress the green count.

---

## Step 0: Create sprint branch

```bash
git checkout -b sprint-f1/clean-foundation
```

All commits go on this branch. Merge to `main` via PR at Task 12.

---

## File Structure

| File | Action | Purpose |
|------|--------|---------|
| `orchestrator/paths.py` | Modify | Change `"orchestrator"` → `".orchestrator"` in path properties |
| `orchestrator/init_cmd.py` | Modify | Use `.orchestrator/` for runtime dir; generate AGENTS.md |
| `orchestrator/templates/gitignore.tmpl` | Modify | Gitignore `.orchestrator/` |
| `orchestrator/state/sqlite_migrations/003_task_definition.sql` | Create | New table `tasks_definition` |
| `orchestrator/state/sqlite_backend.py` | Modify | Seed + update `tasks_definition`; add `set_task_model`, `set_task_backend` |
| `orchestrator/atomize.py` | Modify | UPSERT `tasks_definition` after merge |
| `orchestrator/orch.py` | Modify | Register `task` subcommand; add `_run_task_set_subcommand` |
| `orchestrator/prompt_builder.py` | Modify | Remove phase/estimate/reason; deduplicate `{files}` |
| `orchestrator/tests/test_orch_init.py` | Modify | Assert `.orchestrator/` created; AGENTS.md generated |
| `orchestrator/tests/test_sqlite_backend.py` | Modify | Migration 003; tasks_definition seeding; set_task_model |
| `orchestrator/tests/test_prompt_builder.py` | Modify | Assert removed fields; single `{files}` reference |

---

## Phase 1 — WS1: Rename to `.orchestrator/`

---

### Task 1: Update `paths.py` — change hardcoded `"orchestrator"` to `".orchestrator"`

**Files:**
- Modify: `orchestrator/paths.py:109,123,206`

- [ ] **Step 1: Find all `"orchestrator"` string literals in paths.py**

```bash
rg --line-number '"orchestrator"' orchestrator/paths.py
```

Expected: lines ~109 (router_yaml), ~123 (state_dir base), ~206 (namespaced auto-detect).

- [ ] **Step 2: Update `router_yaml` property (line ~109)**

Change:
```python
    @property
    def router_yaml(self) -> Path:
        return self.project_root / "orchestrator" / "model_router.yaml"
```

To:
```python
    @property
    def router_yaml(self) -> Path:
        return self.project_root / ".orchestrator" / "model_router.yaml"
```

- [ ] **Step 3: Update `state_dir` property (line ~123)**

Change:
```python
        base = self.project_root / "orchestrator" / "state"
```

To:
```python
        base = self.project_root / ".orchestrator" / "state"
```

- [ ] **Step 4: Update namespaced auto-detect (line ~206)**

Change:
```python
        namespaced_state = root / "orchestrator" / "state" / pid
```

To:
```python
        namespaced_state = root / ".orchestrator" / "state" / pid
```

- [ ] **Step 5: Find any remaining `"orchestrator"` references in orch.py that build the config path default**

```bash
rg --line-number '"orchestrator"' orchestrator/orch.py | head -20
```

Update any default config path from `"orchestrator/config.yaml"` to `".orchestrator/config.yaml"`.

- [ ] **Step 6: Run the full test suite to catch regressions**

```bash
pytest orchestrator/tests/ -x -q 2>&1 | tail -20
```

Expected: same green count as baseline (1061 passed). Fix any failures before continuing.

- [ ] **Step 7: Commit**

```bash
git add orchestrator/paths.py orchestrator/orch.py
git commit -m "refactor: rename runtime dir orchestrator/ → .orchestrator/ in paths"
```

---

### Task 2: Update `init_cmd.py` — create `.orchestrator/` instead of `orchestrator/`

**Files:**
- Modify: `orchestrator/init_cmd.py:174-176` (orch_dir creation)
- Modify: `orchestrator/init_cmd.py` (`_YAML_DEFAULTS`, `_CONFLICT_MARKERS`)

- [ ] **Step 1: Find `_YAML_DEFAULTS` and `_CONFLICT_MARKERS` in init_cmd.py**

```bash
rg --line-number '_YAML_DEFAULTS\|_CONFLICT_MARKERS\|"orchestrator"' orchestrator/init_cmd.py | head -30
```

Note every occurrence.

- [ ] **Step 2: Update `_YAML_DEFAULTS` entries**

Find the `_YAML_DEFAULTS` list (maps src file → dst path in project). All entries that have `"orchestrator/..."` as their dst_rel must become `".orchestrator/..."`. Example:

```python
# Before
_YAML_DEFAULTS = [
    ("config.yaml",        "orchestrator/config.yaml"),
    ("model_router.yaml",  "orchestrator/model_router.yaml"),
    ("budgets.yaml",       "orchestrator/budgets.yaml"),
    ("dashboard.yaml",     "orchestrator/dashboard.yaml"),  # if present
]

# After
_YAML_DEFAULTS = [
    ("config.yaml",        ".orchestrator/config.yaml"),
    ("model_router.yaml",  ".orchestrator/model_router.yaml"),
    ("budgets.yaml",       ".orchestrator/budgets.yaml"),
    ("dashboard.yaml",     ".orchestrator/dashboard.yaml"),  # if present
]
```

- [ ] **Step 3: Update `_CONFLICT_MARKERS` if it includes `"orchestrator"`**

If `_CONFLICT_MARKERS` contains `"orchestrator"`, change to `".orchestrator"`.

- [ ] **Step 4: Update `orch_dir` creation (line ~174)**

Change:
```python
    # ---- orchestrator/ ---------------------------------------------
    orch_dir = project_path / "orchestrator"
    (orch_dir / "state").mkdir(parents=True, exist_ok=True)
    (orch_dir / "state" / ".gitkeep").touch()
```

To:
```python
    # ---- .orchestrator/ --------------------------------------------
    orch_dir = project_path / ".orchestrator"
    (orch_dir / "state").mkdir(parents=True, exist_ok=True)
    (orch_dir / "state" / ".gitkeep").touch()
```

- [ ] **Step 5: Find and update `_post_process_config` call — config path reference**

`_post_process_config` is called with the config yaml path. Ensure the path passed is `project_path / ".orchestrator" / "config.yaml"` not `"orchestrator/config.yaml"`.

```bash
rg --line-number '_post_process_config' orchestrator/init_cmd.py
```

Update the call site's config_yaml argument if needed.

- [ ] **Step 6: Run tests**

```bash
pytest orchestrator/tests/ -x -q 2>&1 | tail -20
```

- [ ] **Step 7: Commit**

```bash
git add orchestrator/init_cmd.py
git commit -m "refactor: orch init creates .orchestrator/ instead of orchestrator/"
```

---

### Task 3: Update `gitignore.tmpl`

**Files:**
- Modify: `orchestrator/templates/gitignore.tmpl`

- [ ] **Step 1: Replace the entire template**

Write `orchestrator/templates/gitignore.tmpl` with this content:

```gitignore
# orch runtime — local tooling, never checked in
.orchestrator/

# Editor / OS
.DS_Store
*.swp
.idea/
.vscode/

# Python (in case you also have Python here)
__pycache__/
*.pyc
*.pyo
.venv/
venv/
.pytest_cache/
*.egg-info/
```

- [ ] **Step 2: Run tests**

```bash
pytest orchestrator/tests/ -x -q 2>&1 | tail -5
```

- [ ] **Step 3: Commit**

```bash
git add orchestrator/templates/gitignore.tmpl
git commit -m "fix: gitignore template covers .orchestrator/ instead of orchestrator/state/*"
```

---

### Task 4: Tests for WS1

**Files:**
- Modify: `orchestrator/tests/test_orch_init.py` (or equivalent init test file)

- [ ] **Step 1: Find the existing init test file**

```bash
fd "test.*init" orchestrator/tests/ --type f
```

- [ ] **Step 2: Add test — `.orchestrator/` created, `orchestrator/` not created**

Add to the test file:

```python
def test_init_creates_dotorchestrator_dir(tmp_path):
    """orch init must create .orchestrator/, never plain orchestrator/."""
    from orchestrator.init_cmd import orch_init

    exit_code = orch_init(tmp_path)

    assert exit_code == 0
    assert (tmp_path / ".orchestrator").is_dir(), ".orchestrator/ must exist"
    assert not (tmp_path / "orchestrator").exists(), "plain orchestrator/ must NOT be created"


def test_init_gitignore_covers_dotorchestrator(tmp_path):
    """Generated .gitignore must ignore .orchestrator/, not orchestrator/."""
    from orchestrator.init_cmd import orch_init

    orch_init(tmp_path)

    gitignore = (tmp_path / ".gitignore").read_text()
    assert ".orchestrator/" in gitignore
    assert "orchestrator/state" not in gitignore
```

- [ ] **Step 3: Run new tests to verify they pass**

```bash
pytest orchestrator/tests/test_orch_init.py -v -k "dotorchestrator" 2>&1 | tail -15
```

Expected: 2 PASSED.

- [ ] **Step 4: Commit**

```bash
git add orchestrator/tests/test_orch_init.py
git commit -m "test: assert .orchestrator/ rename in orch init"
```

---

## Phase 2 — WS2: SQLite as Single Runtime Owner

---

### Task 5: Write migration `003_task_definition.sql`

**Files:**
- Create: `orchestrator/state/sqlite_migrations/003_task_definition.sql`

- [ ] **Step 1: Verify the migrations directory and existing versions**

```bash
fd "*.sql" orchestrator/state/sqlite_migrations/ --type f | sort
```

Expected: `001_init.sql`, `002_findings.sql`. We add `003`.

- [ ] **Step 2: Create the migration file**

Write `orchestrator/state/sqlite_migrations/003_task_definition.sql`:

```sql
-- 003_task_definition.sql — Sprint F-1.
--
-- Adds `tasks_definition` table to hold the declarative (static) fields
-- of each task: model assignment, dependencies, spec_ref, files, etc.
-- Previously these lived in tasks.json only; now SQLite is the single
-- runtime owner. tasks.json remains as atomize input format only.
--
-- Compatibility: SQLite >= 3.32. No RETURNING, STRICT, or GENERATED.

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

- [ ] **Step 3: Verify migration applies cleanly on a fresh DB**

```bash
python3 -c "
import tempfile, sqlite3
from pathlib import Path
from orchestrator.state.sqlite_backend import SqliteBackend

with tempfile.NamedTemporaryFile(suffix='.db', delete=False) as f:
    db = Path(f.name)

sb = SqliteBackend(db, 'test-proj')
conn = sqlite3.connect(db)
cur = conn.execute(\"SELECT name FROM sqlite_master WHERE type='table' ORDER BY name\")
print([r[0] for r in cur.fetchall()])
conn.close()
"
```

Expected output includes `tasks_definition`.

- [ ] **Step 4: Run test suite**

```bash
pytest orchestrator/tests/test_migrate.py orchestrator/tests/test_sqlite_backend.py -x -q 2>&1 | tail -10
```

- [ ] **Step 5: Commit**

```bash
git add orchestrator/state/sqlite_migrations/003_task_definition.sql
git commit -m "feat(db): migration 003 — tasks_definition table"
```

---

### Task 6: Update `SqliteBackend` — seed `tasks_definition` + mutation methods

**Files:**
- Modify: `orchestrator/state/sqlite_backend.py`

- [ ] **Step 1: Write failing tests first**

Add to `orchestrator/tests/test_sqlite_backend.py`:

```python
def test_bootstrap_seeds_tasks_definition(tmp_path):
    """bootstrap() must INSERT tasks_definition rows alongside tasks_runtime."""
    from orchestrator.state.sqlite_backend import SqliteBackend
    from orchestrator.models import Task
    import sqlite3

    db = tmp_path / "orch.db"
    sb = SqliteBackend(db, "proj")
    tasks = [
        Task(id="T1", title="First", model="claude", status="todo",
             dependencies=[], files=["a.py"], spec_ref="specs/f.md",
             phase=1, estimate_hours=2.0, reason="fast", description="", comments=[]),
    ]
    sb.bootstrap(tasks)

    conn = sqlite3.connect(db)
    row = conn.execute(
        "SELECT title, model, files_json FROM tasks_definition WHERE task_id = 'T1'"
    ).fetchone()
    conn.close()

    assert row is not None
    assert row[0] == "First"
    assert row[1] == "claude"
    assert row[2] == '["a.py"]'


def test_upsert_task_definition_updates_model(tmp_path):
    """upsert_task_definition() must update model without touching tasks_runtime."""
    from orchestrator.state.sqlite_backend import SqliteBackend
    from orchestrator.models import Task
    import sqlite3, json

    db = tmp_path / "orch.db"
    sb = SqliteBackend(db, "proj")
    task = Task(id="T1", title="First", model="claude", status="todo",
                dependencies=[], files=[], spec_ref=None, phase=1,
                estimate_hours=1.0, reason="", description="", comments=[])
    sb.bootstrap([task])
    sb.set_task_status("T1", "in-progress")

    # Now update model via upsert
    sb.upsert_task_definition(
        task_id="T1",
        title="First",
        model="gemini",
        backend=None,
        deps=[],
        spec_ref=None,
        phase=1,
        estimate_h=1.0,
        reason="",
        files=[],
    )

    conn = sqlite3.connect(db)
    model = conn.execute(
        "SELECT model FROM tasks_definition WHERE task_id = 'T1'"
    ).fetchone()[0]
    status = conn.execute(
        "SELECT status FROM tasks_runtime WHERE task_id = 'T1'"
    ).fetchone()[0]
    conn.close()

    assert model == "gemini", "model must be updated in tasks_definition"
    assert status == "in-progress", "status in tasks_runtime must be untouched"


def test_set_task_model(tmp_path):
    """set_task_model() must update only tasks_definition.model."""
    from orchestrator.state.sqlite_backend import SqliteBackend
    from orchestrator.models import Task
    import sqlite3

    db = tmp_path / "orch.db"
    sb = SqliteBackend(db, "proj")
    task = Task(id="T2", title="T", model="claude", status="todo",
                dependencies=[], files=[], spec_ref=None, phase=1,
                estimate_hours=1.0, reason="", description="", comments=[])
    sb.bootstrap([task])

    sb.set_task_model("T2", "codex")

    conn = sqlite3.connect(db)
    row = conn.execute(
        "SELECT model FROM tasks_definition WHERE task_id = 'T2'"
    ).fetchone()
    conn.close()
    assert row[0] == "codex"
```

- [ ] **Step 2: Run tests to confirm they fail**

```bash
pytest orchestrator/tests/test_sqlite_backend.py -x -q -k "definition or set_task_model" 2>&1 | tail -10
```

Expected: FAILED (methods don't exist yet).

- [ ] **Step 3: Update `bootstrap()` to also seed `tasks_definition`**

In `orchestrator/state/sqlite_backend.py`, inside the `bootstrap()` method's `for t in tasks:` loop, add an INSERT OR IGNORE into `tasks_definition` after the existing `tasks_runtime` insert:

```python
            for t in tasks:
                conn.execute(
                    "INSERT OR IGNORE INTO tasks_runtime "
                    "(project_id, task_id, status, comments_json, updated_at) "
                    "VALUES (?, ?, ?, ?, ?)",
                    (
                        self.project_id,
                        t.id,
                        t.status if t.status in _ALLOWED_STATUSES else "todo",
                        json.dumps(list(t.comments or [])),
                        now,
                    ),
                )
                # Seed tasks_definition (INSERT OR IGNORE — never overwrite on re-bootstrap)
                conn.execute(
                    "INSERT OR IGNORE INTO tasks_definition "
                    "(project_id, task_id, title, model, backend, deps_json, "
                    " spec_ref, phase, estimate_h, reason, files_json, updated_at) "
                    "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
                    (
                        self.project_id,
                        t.id,
                        t.title or "",
                        t.model,
                        None,  # backend not in Task dataclass yet
                        json.dumps(list(t.dependencies or [])),
                        t.spec_ref,
                        t.phase,
                        t.estimate_hours,
                        t.reason,
                        json.dumps(list(t.files or [])),
                        now,
                    ),
                )
```

- [ ] **Step 4: Add `upsert_task_definition()` method**

Add after `bootstrap()`:

```python
    def upsert_task_definition(
        self,
        task_id: str,
        *,
        title: str,
        model: str | None,
        backend: str | None,
        deps: list[str],
        spec_ref: str | None,
        phase: int | None,
        estimate_h: float | None,
        reason: str | None,
        files: list[str],
    ) -> None:
        """INSERT OR REPLACE into tasks_definition (called by atomize --apply)."""
        now = _utc_now_iso()
        with self._write() as conn:
            conn.execute(
                "INSERT OR REPLACE INTO tasks_definition "
                "(project_id, task_id, title, model, backend, deps_json, "
                " spec_ref, phase, estimate_h, reason, files_json, updated_at) "
                "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
                (
                    self.project_id,
                    task_id,
                    title or "",
                    model,
                    backend,
                    json.dumps(list(deps or [])),
                    spec_ref,
                    phase,
                    estimate_h,
                    reason,
                    json.dumps(list(files or [])),
                    now,
                ),
            )
```

- [ ] **Step 5: Add `set_task_model()` and `set_task_backend()` methods**

Add after `upsert_task_definition()`:

```python
    def set_task_model(self, task_id: str, model: str) -> None:
        """Update tasks_definition.model for a task (used by orch task set)."""
        now = _utc_now_iso()
        with self._write() as conn:
            rowcount = conn.execute(
                "UPDATE tasks_definition SET model = ?, updated_at = ? "
                "WHERE project_id = ? AND task_id = ?",
                (model, now, self.project_id, task_id),
            ).rowcount
        if rowcount == 0:
            raise KeyError(
                f"task '{task_id}' not found in tasks_definition for project "
                f"'{self.project_id}'. Run 'orch atomize --apply' first."
            )

    def set_task_backend(self, task_id: str, backend: str) -> None:
        """Update tasks_definition.backend for a task (used by orch task set)."""
        now = _utc_now_iso()
        with self._write() as conn:
            rowcount = conn.execute(
                "UPDATE tasks_definition SET backend = ?, updated_at = ? "
                "WHERE project_id = ? AND task_id = ?",
                (backend, now, self.project_id, task_id),
            ).rowcount
        if rowcount == 0:
            raise KeyError(
                f"task '{task_id}' not found in tasks_definition for project "
                f"'{self.project_id}'. Run 'orch atomize --apply' first."
            )
```

- [ ] **Step 6: Run tests to confirm they pass**

```bash
pytest orchestrator/tests/test_sqlite_backend.py -x -q -k "definition or set_task_model" 2>&1 | tail -10
```

Expected: all PASSED.

- [ ] **Step 7: Run full suite to catch regressions**

```bash
pytest orchestrator/tests/ -x -q 2>&1 | tail -5
```

- [ ] **Step 8: Commit**

```bash
git add orchestrator/state/sqlite_backend.py orchestrator/tests/test_sqlite_backend.py
git commit -m "feat(db): seed tasks_definition in bootstrap; add upsert + set_task_model/backend"
```

---

### Task 7: Update `atomize.py` — UPSERT `tasks_definition` after merge

**Files:**
- Modify: `orchestrator/atomize.py` (the `--apply` write path in `_run_atomize_subcommand` in `orch.py`, or wherever `tasks.json` is written)

- [ ] **Step 1: Find where atomize writes tasks.json with --apply**

```bash
rg --line-number "write_text\|tasks_json\|\.apply" orchestrator/orch.py | grep -i atomize | head -20
```

Find the exact function `_run_atomize_subcommand` (or similar) in `orch.py`.

- [ ] **Step 2: Find where the SQLite backend is bootstrapped in the atomize path**

```bash
rg --line-number "bootstrap\|SqliteBackend" orchestrator/orch.py | head -20
```

Note the location where the backend is created and `bootstrap()` is called.

- [ ] **Step 3: After writing tasks.json (in `--apply` path), add UPSERT loop**

In `_run_atomize_subcommand`, after the `tasks.json` write, add:

```python
        # Sync tasks_definition to SQLite (single source of truth for runtime)
        if args.apply and isinstance(backend, SqliteBackend):
            for pt in parsed_tasks:
                backend.upsert_task_definition(
                    task_id=pt.id,
                    title=pt.title or "",
                    model=pt.model,
                    backend=None,
                    deps=list(pt.dependencies or []),
                    spec_ref=pt.spec_ref,
                    phase=pt.phase,
                    estimate_h=pt.estimate_hours,
                    reason=pt.reason,
                    files=list(pt.files or []),
                )
```

Note: `parsed_tasks` is whatever variable holds the `list[ParsedTask]` returned by the parser. Check the actual variable name in the function.

- [ ] **Step 4: Ensure `SqliteBackend` import is present at the top of orch.py (or wherever the edit lands)**

```bash
rg --line-number "SqliteBackend" orchestrator/orch.py | head -5
```

Add `from orchestrator.state.sqlite_backend import SqliteBackend` if missing.

- [ ] **Step 5: Write a test for the atomize → tasks_definition path**

Add to `orchestrator/tests/test_sqlite_backend.py` or a new `test_atomize_sqlite.py`:

```python
def test_atomize_apply_upserts_tasks_definition(tmp_path):
    """atomize --apply must write tasks_definition rows in SQLite."""
    import sqlite3
    from orchestrator.state.sqlite_backend import SqliteBackend

    # Set up a minimal project
    (tmp_path / "tasks.json").write_text(
        '{"meta": {"project": "test"}, "tasks": []}', encoding="utf-8"
    )
    (tmp_path / "specs").mkdir()
    spec = tmp_path / "specs" / "f1.md"
    spec.write_text(
        "# F1\n## F1.1 Pkg\n### F1.1.T1 Do thing\n- **Model**: claude-sonnet-4-6\n- **Estimación**: 2h\n",
        encoding="utf-8",
    )
    db = tmp_path / ".orchestrator" / "state" / "test" / "orch.db"
    db.parent.mkdir(parents=True)
    backend = SqliteBackend(db, "test", project_root=tmp_path)
    backend.bootstrap([])

    # Run atomize programmatically
    from orchestrator.atomize import parse_spec_files, merge_tasks
    parsed = parse_spec_files([spec])
    existing = {"meta": {"project": "test"}, "tasks": []}
    new_json, _diff = merge_tasks(existing, parsed)

    for pt in parsed:
        backend.upsert_task_definition(
            task_id=pt.id,
            title=pt.title or "",
            model=pt.model,
            backend=None,
            deps=list(pt.dependencies or []),
            spec_ref=pt.spec_ref,
            phase=pt.phase,
            estimate_h=pt.estimate_hours,
            reason=pt.reason,
            files=list(pt.files or []),
        )

    conn = sqlite3.connect(db)
    row = conn.execute(
        "SELECT task_id, model FROM tasks_definition WHERE project_id = 'test'"
    ).fetchone()
    conn.close()

    assert row is not None
    assert row[0] == "F1.1.T1"
    assert row[1] == "claude-sonnet-4-6"
```

- [ ] **Step 6: Run tests**

```bash
pytest orchestrator/tests/ -x -q 2>&1 | tail -10
```

- [ ] **Step 7: Commit**

```bash
git add orchestrator/orch.py orchestrator/tests/
git commit -m "feat(atomize): UPSERT tasks_definition in SQLite on --apply"
```

---

### Task 8: Add `orch task set` subcommand

**Files:**
- Modify: `orchestrator/orch.py` (routing + implementation of `_run_task_set_subcommand`)

- [ ] **Step 1: Register the `task` subcommand in the router (around line 3757)**

In the subcommand dispatch block, add before the final `else` or after `findings`:

```python
    if incoming and incoming[0] == "task":
        return _run_task_subcommand(incoming[1:])
```

- [ ] **Step 2: Add `_run_task_subcommand` dispatcher**

Add the function (near the other `_run_*_subcommand` functions):

```python
def _run_task_subcommand(args: list[str]) -> int:
    """Dispatch `orch task <subcommand>`."""
    if not args:
        print("usage: orch task <subcommand>")
        print("subcommands: set")
        return 1
    sub = args[0]
    if sub == "set":
        return _run_task_set_subcommand(args[1:])
    print(f"error: unknown task subcommand '{sub}'. Available: set")
    return 1
```

- [ ] **Step 3: Find the CLI test pattern and write the failing test**

First, see how existing CLI tests invoke subcommands:

```bash
rg --line-number "task.status\|_run_task_status\|task-status" orchestrator/tests/ --type py | head -10
```

Then add a test that follows the same pattern. Most orch CLI tests call `_run_task_set_subcommand` directly rather than going through sys.argv. Add to `orchestrator/tests/test_sqlite_backend.py`:

```python
def test_task_set_model_updates_definition(tmp_path):
    """_run_task_set_subcommand --model must update tasks_definition, not tasks_runtime."""
    import sqlite3
    from orchestrator.state.sqlite_backend import SqliteBackend
    from orchestrator.models import Task

    # Arrange: project with SQLite backend + one seeded task
    db = tmp_path / ".orchestrator" / "state" / "proj" / "orch.db"
    db.parent.mkdir(parents=True)
    (tmp_path / "tasks.json").write_text('{"tasks":[]}')
    (tmp_path / "scripts").mkdir()
    (tmp_path / "scripts" / "task-start.sh").touch()
    config = tmp_path / ".orchestrator" / "config.yaml"
    config.parent.mkdir(parents=True, exist_ok=True)
    config.write_text("state:\n  backend: sqlite\n")

    sb = SqliteBackend(db, "proj", project_root=tmp_path)
    task = Task(id="T1", title="T", model="claude", status="todo",
                dependencies=[], files=[], spec_ref=None, phase=1,
                estimate_hours=1.0, reason="", description="", comments=[])
    sb.bootstrap([task])

    # Act: call the subcommand function directly
    from orchestrator.orch import _run_task_set_subcommand
    exit_code = _run_task_set_subcommand([
        "--id", "T1", "--model", "gemini",
        "--project-root", str(tmp_path),
        "--project-id", "proj",
        "--config", str(config),
    ])

    # Assert
    assert exit_code == 0
    conn = sqlite3.connect(db)
    model = conn.execute(
        "SELECT model FROM tasks_definition WHERE task_id='T1'"
    ).fetchone()[0]
    conn.close()
    assert model == "gemini"
```

- [ ] **Step 4: Implement `_run_task_set_subcommand`**

```python
def _run_task_set_subcommand(args: list[str]) -> int:
    """orch task set --id TASK_ID [--model MODEL] [--status STATUS] [--backend BACKEND]"""
    import argparse
    from orchestrator.state.sqlite_backend import SqliteBackend

    parser = argparse.ArgumentParser(prog="orch task set")
    parser.add_argument("--id", required=True, dest="task_id", help="Task ID, e.g. F1.1.T3")
    parser.add_argument("--model", default=None, help="Assign model to task")
    parser.add_argument("--status", default=None, help="Set task status")
    parser.add_argument("--backend", default=None, help="Assign backend to task")
    parser.add_argument("--project-root", default=None)
    parser.add_argument("--project-id", default=None)
    parser.add_argument("--config", default=".orchestrator/config.yaml")
    parsed = parser.parse_args(args)

    if not any([parsed.model, parsed.status, parsed.backend]):
        print("error: at least one of --model, --status, --backend is required")
        return 1

    paths = resolve_project_paths(parsed.project_root, parsed.project_id, parsed.config)
    cfg = _load_config(paths.config_yaml)
    backend = _build_backend(cfg, paths)

    if not isinstance(backend, SqliteBackend):
        print("error: orch task set requires state.backend = sqlite")
        return 1

    try:
        if parsed.model:
            backend.set_task_model(parsed.task_id, parsed.model)
            print(f"✓ {parsed.task_id}: model → {parsed.model}")

        if parsed.backend:
            backend.set_task_backend(parsed.task_id, parsed.backend)
            print(f"✓ {parsed.task_id}: backend → {parsed.backend}")

        if parsed.status:
            current = backend.get_task_status(parsed.task_id)
            backend.set_task_status(parsed.task_id, parsed.status)
            print(f"✓ {parsed.task_id}: status {current} → {parsed.status}")

    except KeyError as e:
        print(f"error: {e}")
        return 1
    except ValueError as e:
        print(f"error: {e}")
        return 3

    return 0
```

Note: `_build_backend`, `_load_config`, `resolve_project_paths` are existing helpers in `orch.py`. Use the exact names from the file — run `rg "_build_backend\|_load_config" orchestrator/orch.py | head -5` to confirm.

- [ ] **Step 5: Allow `todo → done` transition in `_STATUS_TRANSITIONS`**

In `sqlite_backend.py`, update `_STATUS_TRANSITIONS`:

```python
_STATUS_TRANSITIONS: dict[str, frozenset[str]] = {
    "backlog": frozenset({"todo", "blocked", "backlog"}),
    "todo": frozenset({"in-progress", "blocked", "todo", "done"}),  # done = manual completion
    "in-progress": frozenset({"done", "blocked", "todo", "in-progress"}),
    "blocked": frozenset({"todo", "in-progress", "blocked"}),
    "done": frozenset({"todo", "done"}),
}
```

- [ ] **Step 6: Run full test suite**

```bash
pytest orchestrator/tests/ -x -q 2>&1 | tail -10
```

- [ ] **Step 7: Commit**

```bash
git add orchestrator/orch.py orchestrator/state/sqlite_backend.py orchestrator/tests/
git commit -m "feat: orch task set command — model/status/backend via SQLite"
```

---

### Task 9: Remove `tasks_json_precedence` from config + tests

**Files:**
- Modify: `orchestrator/config.yaml` (shipped template)
- Modify: `orchestrator/orch.py` (any code that reads `tasks_json_precedence`)
- Modify: `orchestrator/dashboard/server.py` (API endpoint that exposes it)

- [ ] **Step 1: Find all usages of `tasks_json_precedence`**

```bash
rg --line-number "tasks_json_precedence" orchestrator/ --type py
rg --line-number "tasks_json_precedence" orchestrator/config.yaml
```

- [ ] **Step 2: Remove the key from the shipped `config.yaml`**

Delete or comment the `tasks_json_precedence: deps-only` line from `orchestrator/config.yaml`.

- [ ] **Step 3: Remove all code that reads `tasks_json_precedence`**

For each occurrence found in Step 1:
- If the code used `tasks_json_precedence` to decide whether to read from DB or file, that logic is now always "use DB". Simplify or remove the branch.
- The dashboard `/api/config` endpoint that returns this key: remove the `tasks_json_precedence` field from the response dict.

- [ ] **Step 4: Run tests**

```bash
pytest orchestrator/tests/ -x -q 2>&1 | tail -10
```

Fix any failures caused by tests that assert on `tasks_json_precedence` in config output.

- [ ] **Step 5: Commit**

```bash
git add orchestrator/config.yaml orchestrator/orch.py orchestrator/dashboard/server.py orchestrator/tests/
git commit -m "refactor: remove tasks_json_precedence — SQLite is always authoritative"
```

---

## Phase 3 — WS3: AGENTS.md + prompt_builder fix

---

### Task 10: Fix `prompt_builder.py` (Issue #46)

**Files:**
- Modify: `orchestrator/prompt_builder.py:163-184`

- [ ] **Step 1: Write the failing test**

In `orchestrator/tests/test_prompt_builder.py` (find exact file with `fd "test.*prompt" orchestrator/tests/`), add:

```python
def test_prompt_excludes_phase_estimate_reason():
    """Rendered prompt must not include Phase, Estimate, or Model reason lines."""
    from orchestrator.prompt_builder import build_prompt
    from orchestrator.models import Task

    task = Task(
        id="F1.1.T1", title="Do thing", description="desc",
        model="claude", phase=2, estimate_hours=4.0,
        reason="fast model", status="todo",
        dependencies=[], files=["src/a.py"], spec_ref="specs/f.md",
        comments=[],
    )
    prompt = build_prompt(task, working_dir="/proj", deps_info=[])

    assert "Phase:" not in prompt
    assert "Estimate:" not in prompt
    assert "Model reason:" not in prompt


def test_prompt_files_appears_once():
    """The files list must appear exactly once in the rendered prompt (not duplicated)."""
    from orchestrator.prompt_builder import build_prompt
    from orchestrator.models import Task

    task = Task(
        id="F1.1.T1", title="Do thing", description="desc",
        model="claude", phase=1, estimate_hours=2.0, reason="",
        status="todo", dependencies=[], files=["src/a.py", "src/b.py"],
        spec_ref="specs/f.md", comments=[],
    )
    prompt = build_prompt(task, working_dir="/proj", deps_info=[])

    # The literal file list should appear once, not twice
    assert prompt.count("src/a.py") == 1, "files list must not be duplicated"
```

Note: `build_prompt` may be named differently. Run `rg "def.*prompt\|def build" orchestrator/prompt_builder.py` to find the actual function name and signature.

- [ ] **Step 2: Run test to confirm it fails**

```bash
pytest orchestrator/tests/test_prompt_builder.py -x -q -k "phase_estimate or files_appears" 2>&1 | tail -10
```

Expected: FAILED.

- [ ] **Step 3: Update `_TEMPLATE` in prompt_builder.py**

Change lines 163-184 from:

```python
_TEMPLATE = """\
TASK_ID={id}
You are executing task {id}.

Working dir: {working_dir}
Title: {title}
Description: {description}
Phase: {phase}  Estimate: {estimate_hours}h
Model reason: {reason}
Files you may write: {files}
Spec ref (READ FIRST): {spec_ref_line}
{deps_block}
Coordination protocol:
1. Read the spec ref for the exact acceptance criteria for {id}.
2. Do the work. If blocked, run: scripts/task-block.sh {id} "<reason>" "{model}" and STOP.
3. On success, run: scripts/task-finish.sh {id} "<what you did>" "{model}"
   Note: the orchestrator will call scripts/task-start.sh {id} BEFORE launching you.

Constraints:
- Do NOT edit tasks.json directly.
- Do NOT touch files outside {files} unless the spec explicitly requires it.
- Report progress via the scripts above only.
"""
```

To:

```python
_TEMPLATE = """\
TASK_ID={id}
You are executing task {id}.

Working dir: {working_dir}
Title: {title}
Description: {description}
Files you may write: {files}
Spec ref (READ FIRST): {spec_ref_line}
{deps_block}
Coordination protocol:
1. Read the spec ref for the exact acceptance criteria for {id}.
2. Do the work. If blocked, run: scripts/task-block.sh {id} "<reason>" "{model}" and STOP.
3. On success, run: scripts/task-finish.sh {id} "<what you did>" "{model}"
   Note: the orchestrator will call scripts/task-start.sh {id} BEFORE launching you.

Constraints:
- Do NOT edit tasks.json directly.
- Do NOT touch files outside the list above unless the spec explicitly requires it.
- Report progress via the scripts above only.
"""
```

Key changes:
- Removed lines: `Phase: {phase}  Estimate: {estimate_hours}h` and `Model reason: {reason}`
- Second `{files}` reference replaced with `the list above`

- [ ] **Step 4: Run tests to confirm they pass**

```bash
pytest orchestrator/tests/test_prompt_builder.py -x -q 2>&1 | tail -10
```

Expected: all PASSED.

- [ ] **Step 5: Run full suite**

```bash
pytest orchestrator/tests/ -x -q 2>&1 | tail -5
```

- [ ] **Step 6: Commit**

```bash
git add orchestrator/prompt_builder.py orchestrator/tests/test_prompt_builder.py
git commit -m "fix(prompt): remove non-actionable metadata and deduplicate files reference (#46)"
```

---

### Task 11: Generate `AGENTS.md` in `orch init`

**Files:**
- Modify: `orchestrator/init_cmd.py` (add `_generate_agents_md` helper + call in `orch_init`)
- Modify: `orchestrator/tests/test_orch_init.py`

- [ ] **Step 1: Write the failing test**

Add to `orchestrator/tests/test_orch_init.py`:

```python
def test_init_generates_agents_md(tmp_path):
    """orch init must generate AGENTS.md at the project root."""
    from orchestrator.init_cmd import orch_init

    orch_init(tmp_path, project_name="my-project")

    agents_md = tmp_path / "AGENTS.md"
    assert agents_md.exists(), "AGENTS.md must be generated by orch init"

    content = agents_md.read_text()
    assert "Orch Project Context" in content
    assert ".orchestrator/state/" in content
    assert "tasks_definition" in content
    assert "tasks_runtime" in content
    assert "orch task set" in content


def test_init_agents_md_not_gitignored(tmp_path):
    """AGENTS.md must NOT be listed in the generated .gitignore."""
    from orchestrator.init_cmd import orch_init

    orch_init(tmp_path)

    gitignore = (tmp_path / ".gitignore").read_text()
    assert "AGENTS.md" not in gitignore
```

- [ ] **Step 2: Run tests to confirm they fail**

```bash
pytest orchestrator/tests/test_orch_init.py -x -q -k "agents_md" 2>&1 | tail -10
```

Expected: FAILED (AGENTS.md not generated yet).

- [ ] **Step 3: Add `_generate_agents_md` helper to `init_cmd.py`**

Add this function (before `orch_init`):

```python
def _generate_agents_md(project_name: str) -> str:
    """Generate AGENTS.md content for a new project."""
    db_path = f".orchestrator/state/{project_name}/orch.db"
    return (
        "# Orch Project Context\n"
        "\n"
        "> Auto-generated by `orch init`. Refresh with `orch atomize --apply`.\n"
        "> This file is read automatically by Claude Code and compatible agents.\n"
        "\n"
        "## State backend\n"
        "\n"
        f"- SQLite: `.orchestrator/state/{project_name}/orch.db`\n"
        "- `tasks_definition` (static): model, backend, deps, spec_ref, files, phase, estimate\n"
        "- `tasks_runtime` (dynamic): status, attempts, last_model, last_backend\n"
        "- `tasks.json`: atomize input only — **never edit runtime status here**\n"
        "\n"
        "## Check real task status\n"
        "\n"
        "```bash\n"
        f'sqlite3 {db_path} \\\n'
        '  "SELECT task_id, status, last_model FROM tasks_runtime \\\n'
        '   WHERE status != \'done\' ORDER BY task_id;"\n'
        "```\n"
        "\n"
        "## Key commands\n"
        "\n"
        "| Action | Command |\n"
        "|--------|---------|\n"
        "| View status | `orch status` |\n"
        "| Set model/status | `orch task set --id TASK --model MODEL --status done` |\n"
        "| Capture finding | `orch findings capture --type bug\\|feature\\|fix --about orch\\|project --summary \"...\" --evidence \"...\"` |\n"
        "| Publish finding | `orch findings publish <id> --repo <repo> --yes` |\n"
        "| Re-atomize | `orch atomize --apply` |\n"
        "\n"
        "## Provider concurrency caps\n"
        "\n"
        "| Provider | Max concurrent |\n"
        "|----------|---------------|\n"
        "| claude | 3 |\n"
        "| opencode | 6 |\n"
        "| codex | 2 |\n"
        "\n"
        "## Model router\n"
        "\n"
        "See `.orchestrator/model_router.yaml` for backend→model mappings.\n"
    )
```

- [ ] **Step 4: Call `_generate_agents_md` in `orch_init()` after `.gitignore` generation**

In `orch_init()`, after the `.gitignore` block (around line 187), add:

```python
    # ---- AGENTS.md (always generated, always committed) ----------------
    agents_md_path = project_path / "AGENTS.md"
    if not agents_md_path.exists() or force:
        agents_md_path.write_text(
            _generate_agents_md(name), encoding="utf-8"
        )
```

- [ ] **Step 5: Run tests to confirm they pass**

```bash
pytest orchestrator/tests/test_orch_init.py -x -q -k "agents_md" 2>&1 | tail -10
```

Expected: 2 PASSED.

- [ ] **Step 6: Run full suite**

```bash
pytest orchestrator/tests/ -x -q 2>&1 | tail -5
```

- [ ] **Step 7: Commit**

```bash
git add orchestrator/init_cmd.py orchestrator/tests/test_orch_init.py
git commit -m "feat(init): generate AGENTS.md with orch project context at init time (#45)"
```

---

### Task 12: Final — run full suite, close issues, push

- [ ] **Step 1: Run the complete test suite**

```bash
pytest orchestrator/tests/ -q 2>&1 | tail -10
```

Expected: ≥ 1061 passed (baseline), 2 skipped, 1 pre-existing failure.

- [ ] **Step 2: Close GitHub issues**

```bash
gh issue close 46 --comment "Fixed in prompt_builder.py: removed Phase/Estimate/Model reason lines and deduplicated {files} reference."
gh issue close 45 --comment "AGENTS.md generated by orch init with SQLite paths, key commands, and provider caps."
gh issue close 44 --comment "orch task set --id TASK [--model MODEL] [--status STATUS] [--backend BACKEND] implemented. SQLite is now the single runtime owner via tasks_definition table."
```

- [ ] **Step 3: Bump version**

```bash
rg --line-number "version" pyproject.toml | head -5
```

Update `version` in `pyproject.toml` to `0.7.0` (breaking change: `.orchestrator/` rename).

```bash
git add pyproject.toml
git commit -m "chore: bump version to 0.7.0"
```

- [ ] **Step 4: Create PR**

```bash
gh pr create \
  --title "feat: Sprint F-1 — .orchestrator rename + SQLite single owner + AGENTS.md" \
  --body "$(cat <<'EOF'
## Summary

- **WS1**: `orch init` now creates `.orchestrator/` (hidden dir) instead of `orchestrator/`. Follows Unix tooling convention (`.git`, `.github`, `.claude`). Backward compatible — existing projects keep working.
- **WS2**: Introduces `tasks_definition` table (migration 003). SQLite is now the single runtime owner. Adds `orch task set --id TASK [--model MODEL] [--status STATUS] [--backend BACKEND]`. Removes `tasks_json_precedence` config knob.
- **WS3**: `orch init` generates `AGENTS.md` at project root (auto-read by Claude Code). Fixes #46 — removes non-actionable metadata and deduplicates `{files}` in dispatch prompt.

## Closes

Fixes #44, #45, #46

## Test plan

- [ ] `pytest orchestrator/tests/ -q` passes with ≥ baseline green count
- [ ] `orch init` creates `.orchestrator/` not `orchestrator/`
- [ ] `orch init` generates `AGENTS.md` at project root
- [ ] `orch atomize --apply` UPSERTs `tasks_definition` in SQLite
- [ ] `orch task set --id T1 --model gemini` updates `tasks_definition.model`
- [ ] `orch task set --id T1 --status done` transitions status via `tasks_runtime`
- [ ] Prompt no longer contains `Phase:`, `Estimate:`, `Model reason:` lines

🤖 Sprint F-1 — Clean Foundation
EOF
)"
```
