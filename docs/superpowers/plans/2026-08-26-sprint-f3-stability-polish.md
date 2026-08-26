# Sprint F-3: Stability & Polish — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix structural debt in four dependency-ordered layers: config consolidation + retry policy → Milestone SQLite model → status labels + dashboard milestone view → README rewrite + Board removal.

**Architecture:** New `orchestrator/config_loader.py` extracts config loading with deep-merge override support. New migration `004_milestones.sql` adds `milestones` table + FK on `tasks_definition`. FastAPI gains `GET /api/milestones`. SPA gains `MilestonesPage` replacing the Board iframe.

**Tech Stack:** Python 3.11+, SQLite 3.32+, pytest, FastAPI, React + TypeScript + shadcn/ui + Tailwind, pnpm.

---

## Baseline

```bash
pytest orchestrator/tests/ -q 2>&1 | tail -3
# Expected: 1113 passed, 2 skipped
```

---

## File Map

| File | Action | Layer |
|------|--------|-------|
| `orchestrator/config_loader.py` | **Create** | 1 |
| `orchestrator/orch.py` | Modify (config loader + max_attempts) | 1 |
| `orchestrator/config.yaml` | Modify (add presentation defaults) | 1+3 |
| `orchestrator/tests/test_config_loader.py` | **Create** | 1 |
| `orchestrator/state/sqlite_migrations/004_milestones.sql` | **Create** | 2 |
| `orchestrator/state/sqlite_backend.py` | Modify (4 new methods) | 2 |
| `orchestrator/orch.py` | Modify (`--milestone` in `orch task set`) | 2 |
| `orchestrator/tests/test_milestones.py` | **Create** | 2 |
| `orchestrator/dashboard/server.py` | Modify (`GET /api/milestones`) | 3 |
| `orchestrator/tests/test_dashboard_milestones.py` | **Create** | 3 |
| `frontend/src/lib/status.ts` | Modify (add `labelForStatus`) | 3 |
| `frontend/src/hooks/useMilestones.ts` | **Create** | 3 |
| `frontend/src/pages/MilestonesPage.tsx` | **Create** | 3 |
| `frontend/src/App.tsx` | Modify (add `/milestones`, remove `/board`) | 3+4 |
| `frontend/src/components/AppLayout.tsx` | Modify (swap Board → Milestones nav) | 3+4 |
| `frontend/src/pages/BoardPage.tsx` | **Delete** | 4 |
| `README.md` | **Rewrite** | 4 |
| `docs/README-dev.md` | **Create** (current README content) | 4 |

---

## Task 1 — Create `orchestrator/config_loader.py`

**Files:**
- Create: `orchestrator/config_loader.py`
- Create: `orchestrator/tests/test_config_loader.py`

- [ ] **Step 1: Write the failing tests**

Create `orchestrator/tests/test_config_loader.py`:

```python
"""Tests for orchestrator.config_loader — deep_merge + override loading."""
from __future__ import annotations

import textwrap
from pathlib import Path

import pytest

from orchestrator.config_loader import deep_merge, load_config


# ---- deep_merge -------------------------------------------------------------


def test_deep_merge_override_wins_on_conflict():
    base = {"a": 1, "nested": {"x": 10, "y": 20}}
    override = {"nested": {"x": 99}}
    result = deep_merge(base, override)
    assert result["nested"]["x"] == 99
    assert result["nested"]["y"] == 20  # untouched key preserved


def test_deep_merge_adds_missing_keys():
    base = {"a": 1}
    override = {"b": 2}
    result = deep_merge(base, override)
    assert result == {"a": 1, "b": 2}


def test_deep_merge_does_not_mutate_base():
    base = {"a": {"x": 1}}
    override = {"a": {"y": 2}}
    deep_merge(base, override)
    assert base == {"a": {"x": 1}}


def test_deep_merge_non_dict_override_replaces():
    base = {"retry": {"backoff_seconds": 5}}
    override = {"retry": 99}
    result = deep_merge(base, override)
    assert result["retry"] == 99


# ---- load_config ------------------------------------------------------------


def test_load_config_returns_defaults_with_no_overrides(tmp_path: Path):
    cfg_file = tmp_path / "config.yaml"
    cfg_file.write_text("concurrency:\n  global_max: 4\n", encoding="utf-8")
    cfg = load_config(cfg_file)
    assert cfg["concurrency"]["global_max"] == 4
    assert cfg["retry"]["backoff_seconds"] == 5.0   # default


def test_load_config_applies_budgets_override(tmp_path: Path):
    cfg_file = tmp_path / "config.yaml"
    cfg_file.write_text("", encoding="utf-8")
    budgets_file = tmp_path / "budgets.yaml"
    budgets_file.write_text("budgets_preset: aggressive\n", encoding="utf-8")
    cfg = load_config(cfg_file, project_root=tmp_path)
    assert cfg["budgets_preset"] == "aggressive"


def test_load_config_missing_override_is_silently_skipped(tmp_path: Path):
    cfg_file = tmp_path / "config.yaml"
    cfg_file.write_text("", encoding="utf-8")
    # No budgets.yaml → load_config must not raise
    cfg = load_config(cfg_file, project_root=tmp_path)
    assert isinstance(cfg, dict)


def test_load_config_raises_on_missing_config_file(tmp_path: Path):
    with pytest.raises(FileNotFoundError):
        load_config(tmp_path / "nonexistent.yaml")


def test_load_config_max_attempts_default(tmp_path: Path):
    cfg_file = tmp_path / "config.yaml"
    cfg_file.write_text("", encoding="utf-8")
    cfg = load_config(cfg_file)
    assert cfg["retry"]["max_attempts"] == 2


def test_load_config_max_attempts_overridable(tmp_path: Path):
    cfg_file = tmp_path / "config.yaml"
    cfg_file.write_text("retry:\n  max_attempts: 5\n", encoding="utf-8")
    cfg = load_config(cfg_file)
    assert cfg["retry"]["max_attempts"] == 5
```

- [ ] **Step 2: Run tests — expect FAIL**

```bash
pytest orchestrator/tests/test_config_loader.py -v 2>&1 | head -20
# Expected: ModuleNotFoundError: No module named 'orchestrator.config_loader'
```

- [ ] **Step 3: Create `orchestrator/config_loader.py`**

```python
"""Config loading with deep-merge override support (Sprint F-3).

Resolution order (last wins):
  1. Defaults set by _apply_defaults()
  2. config.yaml
  3. Override files (budgets.yaml, model_router.yaml, dashboard/dashboard.yaml)
     loaded from project_root if present.
"""
from __future__ import annotations

from pathlib import Path
from typing import Any

import yaml


def deep_merge(base: dict, override: dict) -> dict:
    """Recursively merge *override* into a copy of *base*. Override wins on conflict."""
    result = dict(base)
    for key, val in override.items():
        if key in result and isinstance(result[key], dict) and isinstance(val, dict):
            result[key] = deep_merge(result[key], val)
        else:
            result[key] = val
    return result


def _try_load_override(path: Path) -> dict[str, Any]:
    """Load a YAML override file. Returns {} if the file doesn't exist."""
    if not path.exists():
        return {}
    with open(path, encoding="utf-8") as fh:
        return yaml.safe_load(fh) or {}


def _apply_defaults(cfg: dict[str, Any]) -> dict[str, Any]:
    """Fill in sane defaults so downstream code doesn't need .get() everywhere."""
    from orchestrator.prompt_builder import DEFAULT_SPEC_ROOT

    cfg.setdefault("concurrency", {})
    cfg["concurrency"].setdefault("global_max", 6)
    cfg["concurrency"].setdefault(
        "per_provider", {"claude": 3, "codex": 2, "opencode": 3}
    )
    cfg.setdefault("strict_files_phases", [])
    cfg.setdefault("default_timeout_multiplier", 1.5)
    cfg.setdefault("budget", {"per_dispatch_usd": 5.0})
    cfg.setdefault("retry", {})
    cfg["retry"].setdefault("backoff_seconds", 5.0)
    cfg["retry"].setdefault("rate_limit_backoff_seconds", 60.0)
    cfg["retry"].setdefault("max_attempts", 2)
    cfg.setdefault("spec_root", DEFAULT_SPEC_ROOT)
    cfg.setdefault("budgets_config", "budgets.yaml")
    cfg.setdefault("budgets_preset", "conservative")
    cfg.setdefault("typical_dispatch_tokens", 200_000)
    cfg.setdefault("findings", {})
    cfg["findings"].setdefault("publish_repo", "hectorcanaimero/orch")
    cfg["findings"].setdefault("publish_rate_limit_per_hour", 3)
    cfg["findings"].setdefault("label", "auto-reported")
    cfg["findings"].setdefault("min_publish_confidence", "medium")
    cfg.setdefault("dispatch", {})
    cfg["dispatch"].setdefault("worktree_mode", False)
    cfg["dispatch"].setdefault("base_branch", "main")
    cfg.setdefault("presentation", {})
    cfg["presentation"].setdefault("status_labels", {
        "backlog":     "Planificado",
        "in_progress": "En progreso",
        "in-progress": "En progreso",
        "done":        "Entregado",
        "blocked":     "Bloqueado",
        "todo":        "Por hacer",
        "skipped":     "Omitido",
    })
    return cfg


def load_config(
    path: str | Path,
    project_root: str | Path | None = None,
) -> dict[str, Any]:
    """Load config.yaml and apply optional override files.

    Args:
        path: Path to config.yaml. Raises FileNotFoundError if missing.
        project_root: Directory to look for override files. Defaults to
                      the directory containing config.yaml.
    """
    p = Path(path)
    if not p.exists():
        raise FileNotFoundError(f"config file not found: {p}")

    root = Path(project_root) if project_root else p.parent

    with open(p, encoding="utf-8") as fh:
        cfg: dict[str, Any] = yaml.safe_load(fh) or {}

    cfg = _apply_defaults(cfg)

    # Apply override files in priority order — last file wins within a key.
    for override_path in [
        root / "budgets.yaml",
        root / "model_router.yaml",
        root / "dashboard" / "dashboard.yaml",
    ]:
        override = _try_load_override(override_path)
        if override:
            cfg = deep_merge(cfg, override)

    return cfg
```

- [ ] **Step 4: Run tests — expect PASS**

```bash
pytest orchestrator/tests/test_config_loader.py -v
# Expected: 9 tests PASS
```

- [ ] **Step 5: Full suite — no regressions**

```bash
pytest orchestrator/tests/ -q 2>&1 | tail -3
# Expected: 1122 passed, 2 skipped
```

- [ ] **Step 6: Commit**

```bash
git add orchestrator/config_loader.py orchestrator/tests/test_config_loader.py
git commit -m "feat(config): config_loader with deep_merge override support"
```

---

## Task 2 — Wire `config_loader` into `orch.py` + configurable `max_attempts`

**Files:**
- Modify: `orchestrator/orch.py` (lines ~278–323 and ~1148)

- [ ] **Step 1: Replace `_load_config` in `orch.py` to delegate to `config_loader`**

Find `def _load_config(path: str | Path) -> dict[str, Any]:` at line 278. Replace the entire function body so it delegates to the new module:

```python
def _load_config(path: str | Path, project_root: str | Path | None = None) -> dict[str, Any]:
    """Load config.yaml via config_loader (Sprint F-3: deep-merge override support)."""
    from orchestrator.config_loader import load_config
    return load_config(path, project_root=project_root)
```

- [ ] **Step 2: Update callers of `_load_config` to pass `project_root`**

Find all calls to `_load_config(paths.config_yaml)` in `orch.py` (there are several — in `main()`, `_run_status_subcommand`, `_run_task_set_subcommand`, etc.). Add `project_root=paths.project_root` to each:

```python
cfg = _load_config(paths.config_yaml, project_root=paths.project_root)
```

Run this to find them all:

```bash
grep -n "_load_config(" orchestrator/orch.py
```

Update each occurrence that receives a `paths` object.

- [ ] **Step 3: Make `max_attempts` configurable**

Find line 1148 in `orch.py`:

```python
max_attempts = 3 if escalation_allowed else 2
```

Replace with:

```python
_base_attempts = int(cfg.get("retry", {}).get("max_attempts", 2))
max_attempts = _base_attempts + 1 if escalation_allowed else _base_attempts
```

Note: `cfg` is already in scope at the call site inside `_reap_once`. Verify this with:

```bash
grep -n "def _reap_once\|^def \|retry_cfg\|max_attempts" orchestrator/orch.py | head -20
```

If `cfg` is not directly in scope, look for how `retry_cfg` is already accessed nearby:

```python
retry_cfg = cfg.get("retry", {})
_base_attempts = int(retry_cfg.get("max_attempts", 2))
max_attempts = _base_attempts + 1 if escalation_allowed else _base_attempts
```

- [ ] **Step 4: Full suite — no regressions**

```bash
pytest orchestrator/tests/ -q 2>&1 | tail -3
# Expected: 1122 passed, 2 skipped (same as after Task 1)
```

- [ ] **Step 5: Commit**

```bash
git add orchestrator/orch.py
git commit -m "refactor(config): delegate _load_config to config_loader; make max_attempts configurable"
```

---

## Task 3 — Migration `004_milestones.sql`

**Files:**
- Create: `orchestrator/state/sqlite_migrations/004_milestones.sql`

- [ ] **Step 1: Create the migration file**

```sql
-- 004_milestones.sql — Sprint F-3.
--
-- Adds `milestones` table for grouping tasks by stakeholder-visible deliverable.
-- Adds nullable `milestone_id` FK on `tasks_definition`.
--
-- Compatibility: SQLite >= 3.32. No RETURNING, STRICT, or GENERATED.

PRAGMA user_version = 4;

CREATE TABLE IF NOT EXISTS milestones (
    project_id   TEXT NOT NULL REFERENCES projects(project_id) ON DELETE CASCADE,
    id           TEXT NOT NULL,
    title        TEXT NOT NULL,
    description  TEXT,
    target_date  TEXT,
    status       TEXT NOT NULL DEFAULT 'open'
                 CHECK (status IN ('open', 'completed', 'cancelled')),
    created_at   TEXT NOT NULL
                 DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    PRIMARY KEY (project_id, id)
);

CREATE INDEX IF NOT EXISTS idx_milestones_project ON milestones(project_id);

ALTER TABLE tasks_definition
    ADD COLUMN milestone_id TEXT;
```

Note: SQLite does not support `ADD COLUMN ... REFERENCES` in older versions. The FK is enforced at the application level in `set_task_milestone`.

- [ ] **Step 2: Verify migration is auto-discovered**

The migration runner in `sqlite_backend.py` uses `_read_migrations()` which globs `sqlite_migrations/*.sql` sorted by the leading number. File `004_milestones.sql` will be picked up automatically.

```bash
python3 -c "
from orchestrator.state.sqlite_backend import _read_migrations
for v, name, _ in _read_migrations():
    print(v, name)
"
# Expected: 1 001_init.sql  2 002_findings.sql  3 003_task_definition.sql  4 004_milestones.sql
```

- [ ] **Step 3: Full suite — no regressions**

```bash
pytest orchestrator/tests/ -q 2>&1 | tail -3
# Expected: 1122 passed, 2 skipped
```

- [ ] **Step 4: Commit**

```bash
git add orchestrator/state/sqlite_migrations/004_milestones.sql
git commit -m "feat(db): migration 004 — milestones table + milestone_id on tasks_definition"
```

---

## Task 4 — `SqliteBackend` milestone methods

**Files:**
- Modify: `orchestrator/state/sqlite_backend.py`
- Create: `orchestrator/tests/test_milestones.py`

- [ ] **Step 1: Write failing tests**

Create `orchestrator/tests/test_milestones.py`:

```python
"""Tests for milestone methods in SqliteBackend."""
from __future__ import annotations

from pathlib import Path

import pytest

from orchestrator.state.sqlite_backend import SqliteBackend


def _backend(tmp_path: Path) -> SqliteBackend:
    db = tmp_path / "orch.db"
    b = SqliteBackend(db_path=db, project_id="test", project_root=tmp_path)
    b.bootstrap([])
    return b


def test_upsert_milestone_creates_record(tmp_path: Path):
    b = _backend(tmp_path)
    b.upsert_milestone("M1", title="Login Feature")
    milestones = b.get_milestones()
    assert len(milestones) == 1
    assert milestones[0]["id"] == "M1"
    assert milestones[0]["title"] == "Login Feature"


def test_upsert_milestone_updates_existing(tmp_path: Path):
    b = _backend(tmp_path)
    b.upsert_milestone("M1", title="Old Title")
    b.upsert_milestone("M1", title="New Title")
    milestones = b.get_milestones()
    assert len(milestones) == 1
    assert milestones[0]["title"] == "New Title"


def test_get_milestones_returns_progress(tmp_path: Path):
    from orchestrator.models import Task
    b = _backend(tmp_path)
    b.bootstrap([
        Task(id="T1", title="t1", model="claude", status="done"),
        Task(id="T2", title="t2", model="claude", status="backlog"),
    ])
    b.upsert_milestone("M1", title="Feature A")
    b.upsert_task_definition(
        "T1", title="t1", model="claude", backend=None, deps=[], spec_ref=None,
        phase=None, estimate_h=None, reason=None, files=[],
    )
    b.upsert_task_definition(
        "T2", title="t2", model="claude", backend=None, deps=[], spec_ref=None,
        phase=None, estimate_h=None, reason=None, files=[],
    )
    b.set_task_milestone("T1", "M1")
    b.set_task_milestone("T2", "M1")
    milestones = b.get_milestones()
    m = milestones[0]
    assert m["progress"]["total"] == 2
    assert m["progress"]["done"] == 1
    assert m["progress"]["pct"] == 50


def test_get_milestones_empty_returns_empty_list(tmp_path: Path):
    b = _backend(tmp_path)
    assert b.get_milestones() == []


def test_set_task_milestone_raises_on_unknown_task(tmp_path: Path):
    b = _backend(tmp_path)
    b.upsert_milestone("M1", title="M")
    with pytest.raises(KeyError):
        b.set_task_milestone("NONEXISTENT", "M1")


def test_set_task_milestone_raises_on_unknown_milestone(tmp_path: Path):
    from orchestrator.models import Task
    b = _backend(tmp_path)
    b.bootstrap([Task(id="T1", title="t", model="claude", status="backlog")])
    b.upsert_task_definition(
        "T1", title="t", model="claude", backend=None, deps=[], spec_ref=None,
        phase=None, estimate_h=None, reason=None, files=[],
    )
    with pytest.raises(KeyError):
        b.set_task_milestone("T1", "NONEXISTENT_MILESTONE")


def test_complete_milestone_changes_status(tmp_path: Path):
    b = _backend(tmp_path)
    b.upsert_milestone("M1", title="Done Feature")
    b.complete_milestone("M1")
    milestones = b.get_milestones()
    assert milestones[0]["status"] == "completed"
```

- [ ] **Step 2: Run tests — expect FAIL**

```bash
pytest orchestrator/tests/test_milestones.py -v 2>&1 | head -20
# Expected: AttributeError — upsert_milestone not found
```

- [ ] **Step 3: Add milestone methods to `SqliteBackend`**

In `orchestrator/state/sqlite_backend.py`, add after `set_task_backend` (around line 363):

```python
# ---- milestones (Sprint F-3) -------------------------------------------

def upsert_milestone(
    self,
    id: str,
    title: str,
    description: str | None = None,
    target_date: str | None = None,
) -> None:
    """Create or update a milestone for this project."""
    now = _utc_now_iso()
    with self._write() as conn:
        conn.execute(
            "INSERT INTO milestones "
            "(project_id, id, title, description, target_date, created_at) "
            "VALUES (?, ?, ?, ?, ?, ?) "
            "ON CONFLICT(project_id, id) DO UPDATE SET "
            "title=excluded.title, description=excluded.description, "
            "target_date=excluded.target_date",
            (self.project_id, id, title, description, target_date, now),
        )

def get_milestones(self) -> list[dict]:
    """Return all milestones for this project with computed task progress."""
    conn = self._conn()
    try:
        cur = conn.execute(
            """
            SELECT
                m.id,
                m.title,
                m.description,
                m.target_date,
                m.status,
                m.created_at,
                COUNT(td.task_id) AS total,
                SUM(CASE WHEN tr.status = 'done' THEN 1 ELSE 0 END) AS done
            FROM milestones m
            LEFT JOIN tasks_definition td
                ON td.project_id = m.project_id AND td.milestone_id = m.id
            LEFT JOIN tasks_runtime tr
                ON tr.project_id = td.project_id AND tr.task_id = td.task_id
            WHERE m.project_id = ?
            GROUP BY m.id
            ORDER BY m.created_at
            """,
            (self.project_id,),
        )
        rows = cur.fetchall()
    finally:
        conn.close()

    result = []
    for row in rows:
        total = row["total"] or 0
        done = row["done"] or 0
        pct = int(done / total * 100) if total > 0 else 0
        result.append({
            "id": row["id"],
            "title": row["title"],
            "description": row["description"],
            "target_date": row["target_date"],
            "status": row["status"],
            "created_at": row["created_at"],
            "progress": {"total": total, "done": done, "pct": pct},
        })
    return result

def set_task_milestone(self, task_id: str, milestone_id: str) -> None:
    """Assign a task to a milestone. Raises KeyError if either doesn't exist."""
    now = _utc_now_iso()
    with self._write() as conn:
        # Verify milestone exists
        cur = conn.execute(
            "SELECT id FROM milestones WHERE project_id = ? AND id = ?",
            (self.project_id, milestone_id),
        )
        if cur.fetchone() is None:
            raise KeyError(
                f"milestone '{milestone_id}' not found for project '{self.project_id}'"
            )
        rowcount = conn.execute(
            "UPDATE tasks_definition SET milestone_id = ?, updated_at = ? "
            "WHERE project_id = ? AND task_id = ?",
            (milestone_id, now, self.project_id, task_id),
        ).rowcount
    if rowcount == 0:
        raise KeyError(
            f"task '{task_id}' not found in tasks_definition for project '{self.project_id}'"
        )

def complete_milestone(self, milestone_id: str) -> None:
    """Mark a milestone as completed."""
    with self._write() as conn:
        conn.execute(
            "UPDATE milestones SET status = 'completed' "
            "WHERE project_id = ? AND id = ?",
            (self.project_id, milestone_id),
        )
```

- [ ] **Step 4: Run tests — expect PASS**

```bash
pytest orchestrator/tests/test_milestones.py -v
# Expected: 8 tests PASS
```

- [ ] **Step 5: Full suite — no regressions**

```bash
pytest orchestrator/tests/ -q 2>&1 | tail -3
# Expected: 1130 passed, 2 skipped
```

- [ ] **Step 6: Commit**

```bash
git add orchestrator/state/sqlite_backend.py orchestrator/tests/test_milestones.py
git commit -m "feat(db): SqliteBackend milestone methods — upsert/get/set_task/complete"
```

---

## Task 5 — `orch task set --milestone` CLI flag

**Files:**
- Modify: `orchestrator/orch.py` (`_run_task_set_subcommand`)

- [ ] **Step 1: Add `--milestone` arg and call `set_task_milestone`**

Find `_run_task_set_subcommand` in `orch.py`. Add:

After the `--backend` argument definition:
```python
p.add_argument("--milestone", default=None, dest="task_milestone",
               help="Assign the task to a milestone ID.")
```

Update the "at least one" validation guard:
```python
if not any([parsed.model, parsed.status, parsed.task_backend, parsed.task_milestone]):
    print(
        "error: at least one of --model, --status, --backend, --milestone is required",
        file=sys.stderr,
    )
    return 1
```

Add the milestone branch in the try block (after the `if parsed.status:` block):
```python
        if parsed.task_milestone:
            backend.set_task_milestone(parsed.task_id, parsed.task_milestone)
            print(f"task {parsed.task_id}: milestone -> {parsed.task_milestone}")
```

- [ ] **Step 2: Full suite — no regressions**

```bash
pytest orchestrator/tests/ -q 2>&1 | tail -3
# Expected: 1130 passed, 2 skipped
```

- [ ] **Step 3: Commit**

```bash
git add orchestrator/orch.py
git commit -m "feat(cli): orch task set --milestone flag"
```

---

## Task 6 — `GET /api/milestones` FastAPI endpoint

**Files:**
- Modify: `orchestrator/dashboard/server.py`
- Create: `orchestrator/tests/test_dashboard_milestones.py`

- [ ] **Step 1: Write failing tests**

Create `orchestrator/tests/test_dashboard_milestones.py`:

```python
"""Tests for GET /api/milestones dashboard endpoint."""
from __future__ import annotations

from pathlib import Path

import pytest
from fastapi.testclient import TestClient

from orchestrator.dashboard.server import create_app
from orchestrator.paths import ProjectPaths


def _make_client(tmp_path: Path) -> TestClient:
    cfg_yaml = tmp_path / ".orchestrator" / "config.yaml"
    cfg_yaml.parent.mkdir(parents=True)
    cfg_yaml.write_text(
        "state:\n  backend: sqlite\n  sqlite_path: orch.db\n",
        encoding="utf-8",
    )
    paths = ProjectPaths(
        project_root=tmp_path,
        project_id="test",
        config_yaml=cfg_yaml,
        explicit_root=True,
        state_layout="namespaced",
    )
    app = create_app(paths)
    return TestClient(app, headers={"X-Dashboard-Token": "dev"})


def test_milestones_returns_empty_list(tmp_path: Path):
    client = _make_client(tmp_path)
    resp = client.get("/api/milestones")
    assert resp.status_code == 200
    data = resp.json()
    assert data["milestones"] == []


def test_milestones_returns_progress(tmp_path: Path):
    from orchestrator.state.sqlite_backend import SqliteBackend
    from orchestrator.models import Task

    # Seed the DB directly
    db_path = tmp_path / ".orchestrator" / "state" / "test" / "orch.db"
    db_path.parent.mkdir(parents=True)
    b = SqliteBackend(db_path=db_path, project_id="test", project_root=tmp_path)
    b.bootstrap([Task(id="T1", title="task", model="claude", status="done")])
    b.upsert_task_definition(
        "T1", title="task", model="claude", backend=None, deps=[], spec_ref=None,
        phase=None, estimate_h=None, reason=None, files=[],
    )
    b.upsert_milestone("M1", title="Alpha Release", target_date="2026-09-01")
    b.set_task_milestone("T1", "M1")

    client = _make_client(tmp_path)
    resp = client.get("/api/milestones")
    assert resp.status_code == 200
    milestones = resp.json()["milestones"]
    assert len(milestones) == 1
    assert milestones[0]["title"] == "Alpha Release"
    assert milestones[0]["progress"]["total"] == 1
```

- [ ] **Step 2: Run tests — expect FAIL**

```bash
pytest orchestrator/tests/test_dashboard_milestones.py -v 2>&1 | head -20
# Expected: 404 or AttributeError — endpoint not registered yet
```

- [ ] **Step 3: Add `GET /api/milestones` to `server.py`**

`AppState` has no `raw_cfg` field — routes load raw config from disk. Follow the same pattern as `_load_project_view` (line ~281). In `orchestrator/dashboard/server.py`, inside `create_app()`, add after the `@app.get("/api/task/{task_id}")` endpoint (around line 677):

```python
    @app.get("/api/milestones", name="api_milestones")
    def api_milestones():
        """Return all milestones with task progress. Requires SQLite backend."""
        import yaml
        from orchestrator.state.sqlite_backend import SqliteBackend

        cfg_path = app_state.paths.config_yaml
        try:
            raw_cfg = yaml.safe_load(cfg_path.read_text(encoding="utf-8")) or {}
        except (OSError, ValueError):
            raw_cfg = {}

        backend = _get_state_backend(app_state.paths, raw_cfg)
        if not isinstance(backend, SqliteBackend):
            return JSONResponse({"milestones": [], "backend": "file"})
        return JSONResponse({"milestones": backend.get_milestones()})
```

Also update `_load_project_config` (around line 478) to:
1. Remove the `board_url` key from the `dashboard` dict (Board is being removed)
2. Add `presentation` to the whitelist:

```python
            "dashboard": {
                # board_url removed — Board tab no longer exists (Sprint F-3)
            },
            "presentation": {
                "status_labels": raw.get("presentation", {}).get("status_labels") or {},
            },
```

- [ ] **Step 4: Run tests — expect PASS**

```bash
pytest orchestrator/tests/test_dashboard_milestones.py -v
# Expected: 2 tests PASS
```

- [ ] **Step 5: Full suite — no regressions**

```bash
pytest orchestrator/tests/ -q 2>&1 | tail -3
# Expected: 1132 passed, 2 skipped
```

- [ ] **Step 6: Commit**

```bash
git add orchestrator/dashboard/server.py orchestrator/tests/test_dashboard_milestones.py
git commit -m "feat(dashboard): GET /api/milestones endpoint"
```

---

## Task 7 — Status labels: config defaults + frontend helper

**Files:**
- Modify: `orchestrator/config.yaml`
- Modify: `frontend/src/lib/status.ts`

- [ ] **Step 1: Add `presentation.status_labels` defaults to `config.yaml`**

Open `orchestrator/config.yaml`. Add at the end:

```yaml
# Sprint F-3 — presentation layer. Labels shown in the dashboard UI.
# Override any key to use your own language. Internal values (backlog,
# in_progress, done, blocked) never change — this is view-only.
presentation:
  status_labels:
    backlog:     "Planificado"
    todo:        "Por hacer"
    in_progress: "En progreso"
    in-progress: "En progreso"
    done:        "Entregado"
    blocked:     "Bloqueado"
    skipped:     "Omitido"
```

- [ ] **Step 2: Add `labelForStatus` to `frontend/src/lib/status.ts`**

Current file content:

```typescript
import type { Badge } from "@/components/ui/badge"

export function statusBadgeVariant(
  status: string,
): Parameters<typeof Badge>[0]["variant"] {
  switch (status) {
    case "done":
      return "success"
    case "in-progress":
    case "in_progress":
      return "warning"
    case "blocked":
      return "danger"
    case "todo":
    case "backlog":
      return "muted"
    default:
      return "outline"
  }
}
```

Replace with (add `labelForStatus` below the existing function):

```typescript
import type { Badge } from "@/components/ui/badge"

export function statusBadgeVariant(
  status: string,
): Parameters<typeof Badge>[0]["variant"] {
  switch (status) {
    case "done":
      return "success"
    case "in-progress":
    case "in_progress":
      return "warning"
    case "blocked":
      return "danger"
    case "todo":
    case "backlog":
      return "muted"
    default:
      return "outline"
  }
}

/**
 * Translate a raw status value to its display label using the
 * presentation.status_labels config from /api/config.
 * Falls back to the raw status string if the label is not configured.
 */
export function labelForStatus(
  status: string,
  labels: Record<string, string> | undefined,
): string {
  if (!labels) return status
  return labels[status] ?? status
}
```

- [ ] **Step 3: Full suite — no regressions**

```bash
pytest orchestrator/tests/ -q 2>&1 | tail -3
# Expected: 1132 passed, 2 skipped (no change — frontend is not tested by pytest)
```

- [ ] **Step 4: Commit**

```bash
git add orchestrator/config.yaml frontend/src/lib/status.ts
git commit -m "feat(presentation): status_labels config defaults + labelForStatus helper"
```

---

## Task 8 — `MilestonesPage` SPA

**Files:**
- Create: `frontend/src/hooks/useMilestones.ts`
- Create: `frontend/src/pages/MilestonesPage.tsx`
- Modify: `frontend/src/App.tsx`
- Modify: `frontend/src/components/AppLayout.tsx`

- [ ] **Step 1: Create `useMilestones.ts`**

Create `frontend/src/hooks/useMilestones.ts`:

```typescript
import { useQuery } from "@tanstack/react-query"

export interface MilestoneProgress {
  total: number
  done: number
  pct: number
}

export interface Milestone {
  id: string
  title: string
  description: string | null
  target_date: string | null
  status: "open" | "completed" | "cancelled"
  created_at: string
  progress: MilestoneProgress
}

async function fetchMilestones(): Promise<Milestone[]> {
  const resp = await fetch("/api/milestones")
  if (!resp.ok) throw new Error(`milestones fetch failed: ${resp.status}`)
  const data = await resp.json()
  return data.milestones as Milestone[]
}

export function useMilestones() {
  return useQuery({
    queryKey: ["milestones"],
    queryFn: fetchMilestones,
    refetchInterval: 10_000,
  })
}
```

- [ ] **Step 2: Create `MilestonesPage.tsx`**

Create `frontend/src/pages/MilestonesPage.tsx`:

```typescript
import { AlertTriangle, Calendar, CheckCircle2, Circle } from "lucide-react"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Progress } from "@/components/ui/progress"
import { Skeleton } from "@/components/ui/skeleton"
import { useMilestones } from "@/hooks/useMilestones"
import { useProjectConfig } from "@/hooks/useProjectConfig"
import { labelForStatus } from "@/lib/status"

export function MilestonesPage() {
  const { data: milestones, isLoading, isError, error } = useMilestones()
  const { data: config } = useProjectConfig()
  const statusLabels = config?.presentation?.status_labels as
    | Record<string, string>
    | undefined

  if (isLoading) {
    return (
      <div className="space-y-4">
        <h1 className="text-2xl font-semibold tracking-tight">Milestones</h1>
        {[1, 2, 3].map((i) => (
          <Skeleton key={i} className="h-32 w-full" />
        ))}
      </div>
    )
  }

  if (isError) {
    return (
      <Alert variant="destructive">
        <AlertTriangle className="h-4 w-4" />
        <AlertTitle>Failed to load milestones</AlertTitle>
        <AlertDescription>{(error as Error)?.message}</AlertDescription>
      </Alert>
    )
  }

  if (!milestones || milestones.length === 0) {
    return (
      <div className="space-y-4">
        <h1 className="text-2xl font-semibold tracking-tight">Milestones</h1>
        <Card>
          <CardHeader>
            <CardTitle>No milestones yet</CardTitle>
          </CardHeader>
          <CardContent className="text-sm text-muted-foreground">
            Create milestones with{" "}
            <code className="font-mono">orch task set --milestone M1</code> and
            assign tasks to them.
          </CardContent>
        </Card>
      </div>
    )
  }

  return (
    <div className="space-y-6">
      <h1 className="text-2xl font-semibold tracking-tight">Milestones</h1>
      <div className="grid gap-4 md:grid-cols-2">
        {milestones.map((m) => (
          <Card key={m.id} className="flex flex-col">
            <CardHeader className="flex flex-row items-start justify-between gap-2 pb-2">
              <div>
                <CardTitle className="text-base">{m.title}</CardTitle>
                {m.description && (
                  <p className="mt-1 text-sm text-muted-foreground">
                    {m.description}
                  </p>
                )}
              </div>
              <Badge
                variant={m.status === "completed" ? "success" : "outline"}
                className="shrink-0"
              >
                {m.status === "completed" ? (
                  <CheckCircle2 className="mr-1 h-3 w-3" />
                ) : (
                  <Circle className="mr-1 h-3 w-3" />
                )}
                {labelForStatus(m.status, statusLabels)}
              </Badge>
            </CardHeader>
            <CardContent className="flex flex-col gap-3">
              <div className="space-y-1">
                <div className="flex items-center justify-between text-sm">
                  <span className="text-muted-foreground">
                    {m.progress.done} / {m.progress.total} tasks
                  </span>
                  <span className="font-medium">{m.progress.pct}%</span>
                </div>
                <Progress value={m.progress.pct} className="h-2" />
              </div>
              {m.target_date && (
                <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
                  <Calendar className="h-3.5 w-3.5" />
                  <span>Target: {m.target_date}</span>
                </div>
              )}
            </CardContent>
          </Card>
        ))}
      </div>
    </div>
  )
}
```

- [ ] **Step 3: Add `/milestones` route to `App.tsx`**

In `frontend/src/App.tsx`, add the import:

```typescript
import { MilestonesPage } from "@/pages/MilestonesPage"
```

Add the route inside `<Routes>` (after the `/kanban` route):

```typescript
          <Route
            path="/milestones"
            element={
              <ProtectedRoute>
                <AppLayout>
                  <MilestonesPage />
                </AppLayout>
              </ProtectedRoute>
            }
          />
```

Also remove the `/board` route and its import:
- Remove: `import { BoardPage } from "@/pages/BoardPage"`
- Remove the entire `/board` `<Route>` block

- [ ] **Step 4: Update `AppLayout.tsx` nav**

In `frontend/src/components/AppLayout.tsx`, find the `NAV_ITEMS` array and:

1. Remove the `PenTool` import from lucide-react
2. Add `Milestone` to the imports: `import { ..., Milestone } from "lucide-react"`
3. Replace the Board nav item:

```typescript
  { to: "/board", label: "Board", icon: PenTool },
```

with:

```typescript
  { to: "/milestones", label: "Milestones", icon: Milestone },
```

- [ ] **Step 5: Verify TypeScript builds clean**

```bash
cd frontend && pnpm tsc --noEmit 2>&1 | head -20
# Expected: no errors
```

- [ ] **Step 6: Full pytest suite — no regressions**

```bash
pytest orchestrator/tests/ -q 2>&1 | tail -3
# Expected: 1132 passed, 2 skipped
```

- [ ] **Step 7: Commit**

```bash
git add frontend/src/hooks/useMilestones.ts \
        frontend/src/pages/MilestonesPage.tsx \
        frontend/src/App.tsx \
        frontend/src/components/AppLayout.tsx
git commit -m "feat(spa): MilestonesPage — progress cards with status labels"
```

---

## Task 9 — Remove Board tab

**Files:**
- Delete: `frontend/src/pages/BoardPage.tsx`

- [ ] **Step 1: Verify `BoardPage` is no longer imported anywhere**

```bash
grep -r "BoardPage\|/board" frontend/src/ 2>/dev/null
# Expected: no output (cleaned in Task 8)
```

- [ ] **Step 2: Delete the file**

```bash
rm frontend/src/pages/BoardPage.tsx
```

- [ ] **Step 3: TypeScript build clean**

```bash
cd frontend && pnpm tsc --noEmit 2>&1 | head -20
# Expected: no errors
```

- [ ] **Step 4: Full pytest suite**

```bash
pytest orchestrator/tests/ -q 2>&1 | tail -3
# Expected: 1132 passed, 2 skipped
```

- [ ] **Step 5: Commit**

```bash
git add -A frontend/src/pages/BoardPage.tsx
git commit -m "chore(spa): remove Board/ExcaliDash tab"
```

---

## Task 10 — README rewrite

**Files:**
- Create: `docs/README-dev.md`
- Modify: `README.md`

- [ ] **Step 1: Save current README as `docs/README-dev.md`**

```bash
cp README.md docs/README-dev.md
```

- [ ] **Step 2: Rewrite `README.md`**

Replace the full content of `README.md` with:

```markdown
# orch

**Run AI agents as a team. Show clients a live dashboard — not a Slack thread.**

orch is a local task orchestrator for freelancers and agencies building with AI.
You define the work as a DAG (`tasks.json`), dispatch each task to Claude, Codex,
or Gemini in parallel, and share a read-only dashboard URL with your client so
they see progress in real time.

---

## The problem

Your client is paying for AI tokens they can't see. You're shipping features they
can't track. Status updates live in Slack threads that get lost. orch fixes that.

---

## How it works

```
1. orch atomize --apply   # spec.md → tasks.json → SQLite
2. orch run               # dispatch tasks to AI agents in parallel
3. orch dashboard         # share a URL — client sees live progress
```

---

## Quick start

```bash
pipx install orch
cd my-project
orch init
orch run
orch dashboard --profile stakeholder --tunnel
```

---

## What makes it different

| Feature | LangChain | CrewAI | Devin | **orch** |
|---------|:---------:|:------:|:-----:|:--------:|
| Multi-backend in one DAG | ❌ | ❌ | ❌ | ✅ |
| Budget guardrails | ❌ | ❌ | ❌ | ✅ |
| Client-shareable dashboard | ❌ | ❌ | ❌ | ✅ |
| Spec → tasks pipeline | ❌ | ❌ | ❌ | ✅ |
| Git worktree isolation | ❌ | ❌ | ❌ | ✅ |
| PR per task (auto) | ❌ | ❌ | ✅ | 🔜 |

---

## Configuration

One file to start:

```yaml
# .orchestrator/config.yaml
concurrency:
  global_max: 4
  per_provider:
    claude: 2
    gemini: 2
```

Optional overrides: `budgets.yaml`, `model_router.yaml`, `dashboard/dashboard.yaml`.

---

## Documentation

- [English manual](docs/MANUAL.en.md)
- [Manual en español](docs/MANUAL.es.md)
- [Dashboard guide](docs/DELIVERING-TO-STAKEHOLDERS.md)
- [Developer notes](docs/README-dev.md)
```

- [ ] **Step 3: Full suite — no regressions**

```bash
pytest orchestrator/tests/ -q 2>&1 | tail -3
# Expected: 1132 passed, 2 skipped
```

- [ ] **Step 4: Commit**

```bash
git add README.md docs/README-dev.md
git commit -m "docs: rewrite README for stakeholder/agency audience; archive dev notes"
```

---

## Task 11 — Push branch and open PR

- [ ] **Step 1: Final suite run**

```bash
pytest orchestrator/tests/ -q 2>&1 | tail -5
# Expected: ≥ 1132 passed, 2 skipped, 0 new failures
```

- [ ] **Step 2: Push and open PR**

```bash
git push -u origin sprint-f3/stability-polish
gh pr create \
  --title "feat: Sprint F-3 — config consolidation, milestones, status labels, README" \
  --base main \
  --body "$(cat <<'EOF'
## Summary

- **Config consolidation**: new \`config_loader.py\` with \`deep_merge\` — single mandatory \`config.yaml\`, optional override files. \`max_attempts\` now configurable.
- **Milestone model**: migration 004 adds \`milestones\` table + FK on \`tasks_definition\`. \`orch task set --milestone\` for CLI assignment.
- **Status labels**: \`presentation.status_labels\` in config, \`labelForStatus()\` helper in SPA, \`GET /api/milestones\` endpoint.
- **MilestonesPage**: replaces Board/ExcaliDash tab — progress cards with configurable labels.
- **README rewrite**: stakeholder/agency audience, 3-step quickstart, comparison table.

## Test baseline
- Before: 1113 passed, 2 skipped
- After: ≥ 1132 passed, 2 skipped

## Test plan
- [x] \`pytest orchestrator/tests/ -q\` → ≥ 1132 passed
- [x] \`test_config_loader.py\` — deep_merge + override loading
- [x] \`test_milestones.py\` — upsert/get/set_task/complete
- [x] \`test_dashboard_milestones.py\` — endpoint returns correct progress
- [x] \`pnpm tsc --noEmit\` — TypeScript clean after Board removal + MilestonesPage
EOF
)"
```
