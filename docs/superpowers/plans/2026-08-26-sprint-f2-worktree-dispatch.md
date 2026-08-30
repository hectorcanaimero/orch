# Sprint F-2: Worktree Dispatch Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add opt-in `dispatch.worktree_mode` that isolates each agent in its own `git worktree`, runs the agent there, pushes the branch to origin on success, and cleans up — zero changes to existing behavior when the flag is off.

**Architecture:** A new `WorktreeManager` class owns the full worktree lifecycle (create/push/remove). It is instantiated once in `main()` and passed to `_spawn_one` (creates worktree before spawn), `_reap_once` (pushes on success, removes always), and the SIGTERM handler (remove_all). `InFlight` gains a `worktree_path` field so the reap loop knows which worktree belongs to which PID. `ProjectPaths` gains an `override_root` field so project-file paths (tasks_json, scripts_dir) resolve inside the worktree while state_dir always stays at the main project root.

**Tech Stack:** Python 3.11+, `subprocess.run` (git commands), `dataclasses`, `pytest`, `unittest.mock`.

---

## File Map

| File | Action | Responsibility |
|------|--------|----------------|
| `orchestrator/worktree.py` | **Create** | WorktreeError + WorktreeManager — the only file that calls git |
| `orchestrator/paths.py` | **Modify** | Add `override_root: Path \| None = None` to ProjectPaths |
| `orchestrator/orch.py` | **Modify** | InFlight.worktree_path, _spawn_one wm args, _reap_once wm args, main() wiring |
| `orchestrator/templates/gitignore.tmpl` | **Modify** | Add `.worktrees/` entry |
| `orchestrator/tests/test_worktree.py` | **Create** | Unit tests for WorktreeManager (all git calls mocked) |
| `orchestrator/tests/test_paths.py` | **Modify** | Test override_root changes project files but not state_dir |
| `orchestrator/tests/test_init.py` | **Modify** | Test gitignore template contains `.worktrees/` |

---

## Baseline

Run before starting. Must stay green throughout.

```bash
pytest orchestrator/tests/ -q 2>&1 | tail -5
# Expected: 1088 passed, 2 skipped
```

---

## Task 1: gitignore template + test

**Files:**
- Modify: `orchestrator/templates/gitignore.tmpl`
- Modify: `orchestrator/tests/test_init.py`

- [ ] **Step 1: Write the failing test**

Open `orchestrator/tests/test_init.py`. Find the existing `test_init_generates_agents_md` test (or any init test) to understand the pattern. Add this test:

```python
def test_init_gitignore_includes_worktrees(tmp_path: Path) -> None:
    """orch init must gitignore .worktrees/ so git worktree dirs aren't committed."""
    from orchestrator.init_cmd import orch_init

    orch_init(str(tmp_path), name="proj", force=False)
    gitignore = (tmp_path / ".gitignore").read_text()
    assert ".worktrees/" in gitignore
```

- [ ] **Step 2: Run it — expect FAIL**

```bash
pytest orchestrator/tests/test_init.py::test_init_gitignore_includes_worktrees -v
# Expected: FAIL — .worktrees/ not in gitignore yet
```

- [ ] **Step 3: Add `.worktrees/` to the template**

Open `orchestrator/templates/gitignore.tmpl`. After the `.orchestrator/` line, add:

```
# orch runtime — local tooling, never checked in
.orchestrator/
.worktrees/
```

Full file after edit:

```gitignore
# orch runtime — local tooling, never checked in
.orchestrator/
.worktrees/

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

- [ ] **Step 4: Run test — expect PASS**

```bash
pytest orchestrator/tests/test_init.py::test_init_gitignore_includes_worktrees -v
# Expected: PASS
```

- [ ] **Step 5: Run full suite to confirm no regression**

```bash
pytest orchestrator/tests/ -q 2>&1 | tail -5
# Expected: 1089 passed, 2 skipped
```

- [ ] **Step 6: Commit**

```bash
git add orchestrator/templates/gitignore.tmpl orchestrator/tests/test_init.py
git commit -m "feat(init): add .worktrees/ to generated gitignore"
```

---

## Task 2: ProjectPaths override_root + test

**Files:**
- Modify: `orchestrator/paths.py`
- Modify: `orchestrator/tests/test_paths.py` (create if it doesn't exist)

- [ ] **Step 1: Write the failing test**

Check if `orchestrator/tests/test_paths.py` exists:

```bash
ls orchestrator/tests/test_paths.py 2>/dev/null || echo "not found"
```

If it doesn't exist, create it. If it does, append. Add:

```python
from pathlib import Path
from orchestrator.paths import ProjectPaths


def test_override_root_changes_project_files_not_state_dir(tmp_path: Path) -> None:
    """When override_root is set, project files (tasks_json, scripts_dir, router_yaml)
    resolve relative to override_root, but state_dir always uses the original project_root.
    This is the worktree isolation contract: code is isolated, state is shared."""
    main_root = tmp_path / "main"
    worktree_root = tmp_path / ".worktrees" / "F2.1.T1"

    paths = ProjectPaths(
        project_root=main_root,
        project_id="myproject",
        config_yaml=main_root / ".orchestrator" / "config.yaml",
        explicit_root=True,
        state_layout="namespaced",
        override_root=worktree_root,
    )

    # Project files resolve inside the worktree
    assert paths.tasks_json == worktree_root / "tasks.json"
    assert paths.scripts_dir == worktree_root / "scripts"
    assert paths.router_yaml == worktree_root / ".orchestrator" / "model_router.yaml"

    # State ALWAYS uses the main root (shared across all worktrees)
    assert paths.state_dir == main_root / ".orchestrator" / "state" / "myproject"
    assert str(main_root) in str(paths.state_dir)
    assert ".worktrees" not in str(paths.state_dir)


def test_override_root_none_is_default_behavior(tmp_path: Path) -> None:
    """When override_root is None (default), all paths resolve from project_root as before."""
    root = tmp_path / "proj"
    paths = ProjectPaths(
        project_root=root,
        project_id="proj",
        config_yaml=root / ".orchestrator" / "config.yaml",
    )

    assert paths.tasks_json == root / "tasks.json"
    assert paths.scripts_dir == root / "scripts"
    assert paths.state_dir == root / ".orchestrator" / "state"
```

- [ ] **Step 2: Run — expect FAIL**

```bash
pytest orchestrator/tests/test_paths.py -v
# Expected: FAIL — ProjectPaths has no override_root field yet
```

- [ ] **Step 3: Add override_root to ProjectPaths**

In `orchestrator/paths.py`, update the `ProjectPaths` dataclass. The field goes after `state_layout`:

```python
@dataclass(frozen=True)
class ProjectPaths:
    """Snapshot inmutable de los paths que el orquestador necesita resolver.

    Fase 2:
        - `explicit_root`: True si el usuario indicó `--project-root` (flag)
          o `ORCH_PROJECT_ROOT` (env). False si el root vino del fallback a
          `Path.cwd()` (modo rupies clásico).
        - `state_layout`: "legacy" | "namespaced" — derivado de `explicit_root`.

    Sprint F-2:
        - `override_root`: when set (worktree mode), project-file paths resolve
          relative to this root instead of `project_root`. `state_dir` is exempt
          — it always uses `project_root` so all worktrees share one SQLite DB.
    """

    project_root: Path
    project_id: str
    config_yaml: Path
    explicit_root: bool = False
    state_layout: StateLayout = "legacy"
    override_root: Path | None = None

    @property
    def tasks_json(self) -> Path:
        return (self.override_root or self.project_root) / "tasks.json"

    @property
    def router_yaml(self) -> Path:
        return (self.override_root or self.project_root) / ".orchestrator" / "model_router.yaml"

    @property
    def state_dir(self) -> Path:
        """State dir según layout (Fase 2). Never uses override_root — state is shared."""
        base = self.project_root / ".orchestrator" / "state"
        if self.state_layout == "namespaced":
            return base / self.project_id
        return base

    @property
    def scripts_dir(self) -> Path:
        return (self.override_root or self.project_root) / "scripts"
```

The `ensure_valid()` method checks `self.tasks_json` and `self.scripts_dir` — since those now use `override_root or project_root`, the check runs against the worktree path. This is intentional: the worktree must be a valid project layout.

- [ ] **Step 4: Run tests — expect PASS**

```bash
pytest orchestrator/tests/test_paths.py -v
# Expected: PASS
```

- [ ] **Step 5: Full suite**

```bash
pytest orchestrator/tests/ -q 2>&1 | tail -5
# Expected: 1091 passed, 2 skipped (2 new tests)
```

- [ ] **Step 6: Commit**

```bash
git add orchestrator/paths.py orchestrator/tests/test_paths.py
git commit -m "feat(paths): add override_root for worktree file isolation"
```

---

## Task 3: Create orchestrator/worktree.py + unit tests

**Files:**
- Create: `orchestrator/worktree.py`
- Create: `orchestrator/tests/test_worktree.py`

- [ ] **Step 1: Write the failing tests**

Create `orchestrator/tests/test_worktree.py`:

```python
"""Unit tests for WorktreeManager.

All git subprocess calls are mocked — no real git repo needed.
"""
from __future__ import annotations

from pathlib import Path
from unittest.mock import MagicMock, call, patch

import pytest

from orchestrator.worktree import WorktreeError, WorktreeManager


# ---- helpers ----------------------------------------------------------------


def _mk_manager(tmp_path: Path) -> WorktreeManager:
    return WorktreeManager(tmp_path)


def _ok_run(stdout: str = "") -> MagicMock:
    """A subprocess.run result that signals success (returncode=0)."""
    r = MagicMock()
    r.returncode = 0
    r.stdout = stdout
    r.stderr = ""
    return r


def _fail_run(stderr: str = "fatal: something went wrong") -> MagicMock:
    """A subprocess.run result that signals failure (returncode=1)."""
    r = MagicMock()
    r.returncode = 1
    r.stdout = ""
    r.stderr = stderr
    return r


# ---- WorktreeManager.create ------------------------------------------------


def test_create_runs_correct_git_command(tmp_path: Path) -> None:
    wm = _mk_manager(tmp_path)
    with patch("subprocess.run", return_value=_ok_run()) as mock_run:
        result = wm.create("F2.1.T3", "main")

    expected_wt_path = tmp_path / ".worktrees" / "F2.1.T3"
    mock_run.assert_called_once_with(
        ["git", "worktree", "add", str(expected_wt_path), "-b", "orch/F2.1.T3", "main"],
        capture_output=True,
        text=True,
        cwd=str(tmp_path),
    )
    assert result == expected_wt_path


def test_create_returns_worktree_path(tmp_path: Path) -> None:
    wm = _mk_manager(tmp_path)
    with patch("subprocess.run", return_value=_ok_run()):
        path = wm.create("F2.1.T5", "sprint-f2")
    assert path == tmp_path / ".worktrees" / "F2.1.T5"


def test_create_registers_in_active(tmp_path: Path) -> None:
    wm = _mk_manager(tmp_path)
    with patch("subprocess.run", return_value=_ok_run()):
        wm.create("F2.1.T1", "main")
    assert "F2.1.T1" in wm._active


def test_create_cleans_stale_path_first(tmp_path: Path) -> None:
    """If .worktrees/<task_id>/ already exists, remove it before creating fresh."""
    wm = _mk_manager(tmp_path)
    stale_path = tmp_path / ".worktrees" / "F2.1.T1"
    stale_path.mkdir(parents=True)

    calls_made = []
    def fake_run(args, **kwargs):
        calls_made.append(args)
        return _ok_run()

    with patch("subprocess.run", side_effect=fake_run):
        wm.create("F2.1.T1", "main")

    # First call must be the remove (for the stale path), then the add.
    assert calls_made[0] == ["git", "worktree", "remove", "--force", str(stale_path)]
    assert calls_made[1][1] == "worktree"
    assert calls_made[1][2] == "add"


def test_create_raises_worktree_error_on_git_failure(tmp_path: Path) -> None:
    wm = _mk_manager(tmp_path)
    with patch("subprocess.run", return_value=_fail_run("fatal: branch already exists")):
        with pytest.raises(WorktreeError) as exc_info:
            wm.create("F2.1.T1", "main")
    assert "F2.1.T1" in str(exc_info.value)


# ---- WorktreeManager.push --------------------------------------------------


def test_push_uses_force_with_lease(tmp_path: Path) -> None:
    wm = _mk_manager(tmp_path)
    with patch("subprocess.run", return_value=_ok_run()) as mock_run:
        wm.push("F2.1.T3")
    mock_run.assert_called_once_with(
        ["git", "push", "--force-with-lease", "-u", "origin", "orch/F2.1.T3"],
        capture_output=True,
        text=True,
        cwd=str(tmp_path),
    )


def test_push_raises_worktree_error_on_failure(tmp_path: Path) -> None:
    wm = _mk_manager(tmp_path)
    with patch("subprocess.run", return_value=_fail_run("error: failed to push")):
        with pytest.raises(WorktreeError):
            wm.push("F2.1.T3")


# ---- WorktreeManager.remove ------------------------------------------------


def test_remove_calls_git_worktree_remove_force(tmp_path: Path) -> None:
    wm = _mk_manager(tmp_path)
    wt_path = tmp_path / ".worktrees" / "F2.1.T1"
    wt_path.mkdir(parents=True)

    with patch("subprocess.run", return_value=_ok_run()) as mock_run:
        wm.remove("F2.1.T1")

    mock_run.assert_called_once_with(
        ["git", "worktree", "remove", "--force", str(wt_path)],
        capture_output=True,
        text=True,
        cwd=str(tmp_path),
    )


def test_remove_is_noop_when_directory_absent(tmp_path: Path) -> None:
    """If the worktree dir doesn't exist, remove() must not call git."""
    wm = _mk_manager(tmp_path)
    with patch("subprocess.run") as mock_run:
        wm.remove("nonexistent-task")
    mock_run.assert_not_called()


def test_remove_swallows_git_error(tmp_path: Path) -> None:
    """remove() is best-effort cleanup — git failures must not propagate."""
    wm = _mk_manager(tmp_path)
    wt_path = tmp_path / ".worktrees" / "F2.1.T1"
    wt_path.mkdir(parents=True)

    with patch("subprocess.run", return_value=_fail_run("error")):
        wm.remove("F2.1.T1")  # must NOT raise


def test_remove_clears_active_entry(tmp_path: Path) -> None:
    wm = _mk_manager(tmp_path)
    wt_path = tmp_path / ".worktrees" / "F2.1.T1"
    wt_path.mkdir(parents=True)
    wm._active["F2.1.T1"] = wt_path

    with patch("subprocess.run", return_value=_ok_run()):
        wm.remove("F2.1.T1")

    assert "F2.1.T1" not in wm._active


# ---- WorktreeManager.remove_all -------------------------------------------


def test_remove_all_removes_every_active_worktree(tmp_path: Path) -> None:
    wm = _mk_manager(tmp_path)
    for tid in ["F2.1.T1", "F2.1.T2", "F2.1.T3"]:
        wt = tmp_path / ".worktrees" / tid
        wt.mkdir(parents=True)
        wm._active[tid] = wt

    removed = []
    def fake_run(args, **kwargs):
        if args[2] == "remove":
            removed.append(Path(args[4]).name)
        return _ok_run()

    with patch("subprocess.run", side_effect=fake_run):
        wm.remove_all()

    assert set(removed) == {"F2.1.T1", "F2.1.T2", "F2.1.T3"}
    assert wm._active == {}


def test_remove_all_is_noop_when_no_active_worktrees(tmp_path: Path) -> None:
    wm = _mk_manager(tmp_path)
    with patch("subprocess.run") as mock_run:
        wm.remove_all()
    mock_run.assert_not_called()


# ---- WorktreeManager.exists ------------------------------------------------


def test_exists_returns_true_when_directory_present(tmp_path: Path) -> None:
    wm = _mk_manager(tmp_path)
    (tmp_path / ".worktrees" / "F2.1.T1").mkdir(parents=True)
    assert wm.exists("F2.1.T1") is True


def test_exists_returns_false_when_directory_absent(tmp_path: Path) -> None:
    wm = _mk_manager(tmp_path)
    assert wm.exists("nope") is False


# ---- branch_name / worktree_path -------------------------------------------


def test_branch_name_format(tmp_path: Path) -> None:
    wm = _mk_manager(tmp_path)
    assert wm.branch_name("F2.1.T3") == "orch/F2.1.T3"


def test_worktree_path_format(tmp_path: Path) -> None:
    wm = _mk_manager(tmp_path)
    assert wm.worktree_path("F2.1.T3") == tmp_path / ".worktrees" / "F2.1.T3"
```

- [ ] **Step 2: Run — expect FAIL (module doesn't exist yet)**

```bash
pytest orchestrator/tests/test_worktree.py -v 2>&1 | head -20
# Expected: ModuleNotFoundError or ImportError
```

- [ ] **Step 3: Create orchestrator/worktree.py**

```python
"""Git worktree lifecycle management for Sprint F-2 dispatch isolation.

Each dispatched task runs in its own ``git worktree`` so concurrent agents
cannot overwrite each other's file changes. The manager is instantiated once
per ``orch run`` invocation and lives until the main loop exits.

Public surface:
    WorktreeError  — raised when any git command fails
    WorktreeManager.create(task_id, base_branch) -> Path
    WorktreeManager.push(task_id)
    WorktreeManager.remove(task_id)
    WorktreeManager.remove_all()
    WorktreeManager.exists(task_id) -> bool
"""
from __future__ import annotations

import subprocess
from pathlib import Path


class WorktreeError(RuntimeError):
    """Raised when a git worktree command fails."""

    def __init__(self, task_id: str, cmd: list[str], stderr: str) -> None:
        super().__init__(
            f"worktree git command failed for {task_id!r}: "
            f"{' '.join(cmd)!r} → {stderr[:200]!r}"
        )
        self.task_id = task_id
        self.cmd = cmd
        self.stderr = stderr


class WorktreeManager:
    """Create, push, and clean up per-task git worktrees.

    All git commands run from ``project_root`` (the main repo), never from
    inside a worktree. Active worktrees are tracked in ``_active``
    (task_id → path) so ``remove_all`` can clean up on SIGTERM.
    """

    def __init__(self, project_root: Path) -> None:
        self._root = project_root.resolve()
        self._active: dict[str, Path] = {}

    # ---- path helpers -------------------------------------------------------

    def worktree_path(self, task_id: str) -> Path:
        return self._root / ".worktrees" / task_id

    def branch_name(self, task_id: str) -> str:
        return f"orch/{task_id}"

    # ---- internal -----------------------------------------------------------

    def _run(self, args: list[str], task_id: str) -> str:
        result = subprocess.run(
            args,
            capture_output=True,
            text=True,
            cwd=str(self._root),
        )
        if result.returncode != 0:
            raise WorktreeError(task_id, args, result.stderr)
        return result.stdout

    # ---- public lifecycle ---------------------------------------------------

    def exists(self, task_id: str) -> bool:
        """True if the worktree directory is present on disk."""
        return self.worktree_path(task_id).exists()

    def create(self, task_id: str, base_branch: str) -> Path:
        """Create an isolated worktree for *task_id* branched off *base_branch*.

        If a stale worktree directory already exists (e.g. from a crashed prior
        run), it is removed first. Returns the path to the new worktree.

        Raises:
            WorktreeError: if ``git worktree add`` fails.
        """
        wt_path = self.worktree_path(task_id)
        if wt_path.exists():
            self.remove(task_id)
        (self._root / ".worktrees").mkdir(parents=True, exist_ok=True)
        self._run(
            ["git", "worktree", "add", str(wt_path), "-b", self.branch_name(task_id), base_branch],
            task_id,
        )
        self._active[task_id] = wt_path
        return wt_path

    def push(self, task_id: str) -> None:
        """Push the task branch to origin using force-with-lease.

        ``--force-with-lease`` makes retried tasks overwrite the previous
        attempt's branch without clobbering unrelated remote changes.

        Raises:
            WorktreeError: if ``git push`` fails.
        """
        self._run(
            ["git", "push", "--force-with-lease", "-u", "origin", self.branch_name(task_id)],
            task_id,
        )

    def remove(self, task_id: str) -> None:
        """Remove the worktree directory. No-op if the directory is absent.

        Uses ``--force`` so dirty trees (untracked files from a failed agent)
        are removed without complaint. Git errors are swallowed — this is
        best-effort cleanup.
        """
        wt_path = self.worktree_path(task_id)
        if wt_path.exists():
            try:
                self._run(["git", "worktree", "remove", "--force", str(wt_path)], task_id)
            except WorktreeError:
                pass
        self._active.pop(task_id, None)

    def remove_all(self) -> None:
        """Remove every tracked worktree. Called from the SIGTERM handler."""
        for task_id in list(self._active):
            self.remove(task_id)
```

- [ ] **Step 4: Run tests — expect PASS**

```bash
pytest orchestrator/tests/test_worktree.py -v
# Expected: all 16 tests PASS
```

- [ ] **Step 5: Full suite**

```bash
pytest orchestrator/tests/ -q 2>&1 | tail -5
# Expected: 1107 passed, 2 skipped (16 new tests)
```

- [ ] **Step 6: Commit**

```bash
git add orchestrator/worktree.py orchestrator/tests/test_worktree.py
git commit -m "feat(worktree): WorktreeManager — per-task git worktree lifecycle"
```

---

## Task 4: Wire worktree mode into orch.py

**Files:**
- Modify: `orchestrator/orch.py`

This task has five surgical changes to `orch.py`. Make them in order. Run the tests between each sub-step.

### Sub-step 4a: Add `worktree_path` to `InFlight`

- [ ] **Step 1: Find `InFlight` dataclass (around line 600)**

```bash
grep -n "^class InFlight\|^    timed_out\|^    task_lock_fd" orchestrator/orch.py | head -5
```

- [ ] **Step 2: Add `worktree_path` field**

In `orchestrator/orch.py`, find the `InFlight` dataclass. It looks like this:

```python
@dataclass
class InFlight:
    task: Task
    route: RouteEntry
    backend: Backend
    dispatch: Dispatch
    started_at_mono: float
    timeout_s: float
    timed_out: bool = False
    task_lock_fd: Any = None
```

Add one field after `task_lock_fd`:

```python
@dataclass
class InFlight:
    task: Task
    route: RouteEntry
    backend: Backend
    dispatch: Dispatch
    started_at_mono: float
    timeout_s: float
    timed_out: bool = False
    task_lock_fd: Any = None
    worktree_path: Path | None = None
```

- [ ] **Step 3: Run suite — no regressions**

```bash
pytest orchestrator/tests/ -q 2>&1 | tail -3
# Expected: same pass count as before this task
```

### Sub-step 4b: Update `_install_sigint` to accept `wm`

- [ ] **Step 4: Find `_install_sigint` (around line 880)**

```bash
grep -n "def _install_sigint" orchestrator/orch.py
```

- [ ] **Step 5: Add `wm` parameter and call `remove_all()` on hard kill**

Current signature:
```python
def _install_sigint(drain: _DrainFlag, in_flight: dict[int, InFlight]) -> None:
```

New:
```python
def _install_sigint(
    drain: _DrainFlag,
    in_flight: dict[int, InFlight],
    wm: "WorktreeManager | None" = None,
) -> None:
    """SIGINT/SIGTERM → drain; second signal → SIGKILL every child group.

    Sprint A / Issue #12: SIGTERM is treated identically to SIGINT so that
    `kill <orch-pid>` and `orch stop` behave the same as Ctrl-C. Both hit
    the same in-memory `_DrainFlag`. On the second signal we escalate:
    SIGKILL the whole process group of every in-flight child (catches
    subprocess-of-subprocess trees, not just the direct CLI).

    Sprint F-2: on hard kill (second signal), also call wm.remove_all() to
    clean up any orphaned worktrees.
    """

    def handler(signum, frame):  # noqa: ARG001
        if drain.set:
            for pid, entry in list(in_flight.items()):
                _killpg_or_pid(pid, signal.SIGKILL)
                entry.timed_out = True
            drain.hard_kill_next = True
            if wm is not None:
                wm.remove_all()
        else:
            drain.set = True
            log.warning(
                "%s received — draining in-flight; hit again to SIGKILL",
                signal.Signals(signum).name if signum else "signal",
            )

    signal.signal(signal.SIGINT, handler)
    signal.signal(signal.SIGTERM, handler)
```

Note: the `WorktreeManager` type annotation uses a string literal `"WorktreeManager | None"` to avoid a circular import (the import happens lazily inside `main()`).

- [ ] **Step 6: Run suite**

```bash
pytest orchestrator/tests/ -q 2>&1 | tail -3
# Expected: same pass count
```

### Sub-step 4c: Update `_spawn_one` to create worktree before spawn

- [ ] **Step 7: Find `_spawn_one` (around line 1311)**

```bash
grep -n "def _spawn_one" orchestrator/orch.py
```

- [ ] **Step 8: Add `wm` and `base_branch` parameters to signature**

Current end of signature (around line 1326):
```python
    use_task_locks: bool = False,
    budget_gate: BudgetGate | None = None,
    defer_reasons: dict[str, str] | None = None,
) -> bool:
```

New:
```python
    use_task_locks: bool = False,
    budget_gate: BudgetGate | None = None,
    defer_reasons: dict[str, str] | None = None,
    wm: "WorktreeManager | None" = None,
    base_branch: str = "main",
) -> bool:
```

- [ ] **Step 9: Add worktree creation block before `backend.spawn()`**

Find the line `log_path = state_dir / "logs" / f"{task.id}.log"` (around line 1457). Insert the worktree block BEFORE it:

```python
    # ---- worktree creation (Sprint F-2) -----------------------------------
    # When worktree_mode is on, each task gets its own isolated git branch.
    # effective_cwd is the path passed to backend.spawn(); all other uses of
    # cwd (call_task_start, render_prompt, error paths) keep the main root.
    effective_cwd = cwd
    if wm is not None:
        try:
            effective_cwd = wm.create(task.id, base_branch)
        except Exception as exc:  # noqa: BLE001
            log.error("worktree create failed for %s: %s", task.id, exc)
            gsem.release()
            psem[route.backend].release()
            release_task_lock(task_lock_fd)
            try:
                call_task_block(
                    task.id, f"worktree create failed: {exc}", route.cli_model,
                    project_root=cwd,
                )
            except Exception:  # noqa: BLE001
                pass
            queue.mark_blocked(task.id)
            run_file.mark_blocked(task.id)
            event_log.emit(
                "block",
                task.id,
                backend=route.backend,
                reason=f"worktree create failed: {exc}",
            )
            return False

    log_path = state_dir / "logs" / f"{task.id}.log"
```

- [ ] **Step 10: Change `cwd=cwd` → `cwd=effective_cwd` in `backend.spawn()` call**

Find:
```python
        dispatch = backend.spawn(
            task=task,
            route=route,
            prompt_path=prompt_path,
            log_path=log_path,
            cwd=cwd,
        )
```

Replace with:
```python
        dispatch = backend.spawn(
            task=task,
            route=route,
            prompt_path=prompt_path,
            log_path=log_path,
            cwd=effective_cwd,
        )
```

- [ ] **Step 11: Add worktree cleanup on spawn failure**

In the `except (FileNotFoundError, OSError)` block after `backend.spawn()`, add a `wm.remove()` call before the existing cleanup. Find the block (around line 1469):

```python
    except (FileNotFoundError, OSError) as exc:
        log.exception("spawn failed for %s: %s", task.id, exc)
        gsem.release()
        psem[route.backend].release()
        release_task_lock(task_lock_fd)
        if wm is not None:
            wm.remove(task.id)
        try:
            call_task_block(
```

- [ ] **Step 12: Add `worktree_path` to `InFlight` construction**

Find the `entry = InFlight(...)` block (around line 1495):

```python
    entry = InFlight(
        task=task,
        route=route,
        backend=backend,
        dispatch=dispatch,
        started_at_mono=_monotonic(),
        timeout_s=timeout_s,
        task_lock_fd=task_lock_fd,
        worktree_path=effective_cwd if wm is not None else None,
    )
```

- [ ] **Step 13: Run suite**

```bash
pytest orchestrator/tests/ -q 2>&1 | tail -3
# Expected: same pass count
```

### Sub-step 4d: Update `_reap_once` to push and remove worktrees

- [ ] **Step 14: Find `_reap_once` (around line 942)**

```bash
grep -n "def _reap_once" orchestrator/orch.py
```

- [ ] **Step 15: Add `wm` parameter to signature**

Current end:
```python
    task_costs: dict[str, float] | None = None,
) -> int:
```

New:
```python
    task_costs: dict[str, float] | None = None,
    wm: "WorktreeManager | None" = None,
) -> int:
```

- [ ] **Step 16: Add push + remove block after `_post_run_checks`**

Find the line `result, spoof_id = _post_run_checks(entry.task, result, cfg, cwd, log_text)` (around line 1014). Insert the worktree block immediately after it:

```python
        result, spoof_id = _post_run_checks(entry.task, result, cfg, cwd, log_text)

        # Worktree push + cleanup (Sprint F-2)
        # push() only on success — don't publish incomplete work.
        # remove() always — best-effort, errors are logged and swallowed.
        if entry.worktree_path is not None and wm is not None:
            if result.success:
                try:
                    wm.push(entry.task.id)
                except Exception as exc:  # noqa: BLE001
                    log.warning(
                        "worktree push failed for %s (best-effort, not failing task): %s",
                        entry.task.id, exc,
                    )
            wm.remove(entry.task.id)
```

- [ ] **Step 17: Run suite**

```bash
pytest orchestrator/tests/ -q 2>&1 | tail -3
# Expected: same pass count
```

### Sub-step 4e: Update `_drain_wait`, `_refill`, and `main()`

- [ ] **Step 18: Update `_drain_wait` to accept and forward `wm`**

Find `def _drain_wait` (around line 1670). Add `wm: "WorktreeManager | None" = None` to its signature and pass it to `_reap_once`:

```python
def _drain_wait(
    in_flight: dict[int, InFlight],
    queue: TaskQueue,
    run_file: RunFile,
    event_log: EventLog,
    spend_log: SpendLog,
    cfg: dict[str, Any],
    cwd: Path,
    gsem: _Sem,
    psem: dict[str, _Sem],
    timeout_s: float = 300.0,
    router: dict[str, RouteEntry] | None = None,
    task_costs: dict[str, float] | None = None,
    wm: "WorktreeManager | None" = None,
) -> None:
    """Poll `_reap_once` until `in_flight` empty or overall timeout hits."""
    deadline = _monotonic() + timeout_s
    while in_flight and _monotonic() < deadline:
        _reap_once(
            in_flight, queue, run_file, event_log, spend_log, cfg, cwd, gsem, psem,
            router=router, task_costs=task_costs,
            wm=wm,
        )
        _timeout_sweep(in_flight, event_log)
        time.sleep(0.2)
```

- [ ] **Step 19: Update `_refill` to accept and forward `wm` and `base_branch`**

Find `def _refill` (around line 1518). Add two optional params at the end of its signature:

```python
    wm: "WorktreeManager | None" = None,
    base_branch: str = "main",
) -> int:
```

Find ALL calls to `_spawn_one` inside `_refill` and add the two new kwargs. There are typically 2 calls (first attempt and retry). Example:

```python
        ok = _spawn_one(
            task, route, attempt, cfg, gsem, psem, in_flight, run_file, event_log,
            run_id, state_dir, cwd, queue,
            use_task_locks=use_task_locks,
            budget_gate=budget_gate,
            defer_reasons=defer_reasons,
            wm=wm,
            base_branch=base_branch,
        )
```

Do a targeted search:

```bash
grep -n "_spawn_one(" orchestrator/orch.py
```

Update every `_spawn_one(` call inside `_refill` to include `wm=wm, base_branch=base_branch`.

- [ ] **Step 20: Wire everything in `main()`**

Find the area after `cfg = _load_config(paths.config_yaml)` and the router/tasks loading block. After the router is loaded (around line 3960), add:

```python
    # Sprint F-2: worktree mode — opt-in via dispatch.worktree_mode in config.yaml
    _dispatch_cfg = cfg.get("dispatch") or {}
    _worktree_mode = bool(_dispatch_cfg.get("worktree_mode", False))
    _base_branch = str(_dispatch_cfg.get("base_branch", "main"))
    if _worktree_mode:
        from orchestrator.worktree import WorktreeManager
        wm: "WorktreeManager | None" = WorktreeManager(paths.project_root)
        log.info("worktree mode enabled; base_branch=%s", _base_branch)
    else:
        wm = None
```

Find `_install_sigint(drain, in_flight)` (around line 4199) and update it:

```python
    _install_sigint(drain, in_flight, wm=wm)
```

Find the `_reap_once(...)` call in the main loop (around line 4209) and add `wm=wm`:

```python
            _reap_once(
                in_flight, queue, run_file, event_log, spend_log, cfg, cwd, gsem, psem,
                retry_queue=retry_queue,
                router=router,
                task_costs=task_costs,
                wm=wm,
            )
```

Find the `_refill(...)` call and add `wm=wm, base_branch=_base_branch`:

```python
                dispatched_count = _refill(
                    queue, router, cfg, args.mode, gate, gsem, psem, in_flight,
                    run_file, event_log, run_id, state_dir, cwd,
                    dispatched_count, args.max_tasks, deferred, drain,
                    retry_queue=retry_queue,
                    use_task_locks=args.task_locks,
                    only=args.only,
                    budget_gate=budget_gate,
                    defer_reasons=defer_reasons,
                    wm=wm,
                    base_branch=_base_branch,
                )
```

Find the `_drain_wait(...)` call (around line 4303) and add `wm=wm`:

```python
            _drain_wait(
                in_flight, queue, run_file, event_log, spend_log, cfg, cwd, gsem, psem,
                router=router, task_costs=task_costs,
                wm=wm,
            )
```

Finally, add cleanup AFTER the `_drain_wait` return (before the end-of-run summary). Find:

```python
        # ---- final drain on SIGINT ---------------------------------------
        if drain.set and in_flight:
            _drain_wait(...)
            return 130
```

Add cleanup right before `return 130` AND after the `if` block (for normal exit):

```python
        # ---- final drain on SIGINT ---------------------------------------
        if drain.set and in_flight:
            _drain_wait(
                in_flight, queue, run_file, event_log, spend_log, cfg, cwd, gsem, psem,
                router=router, task_costs=task_costs,
                wm=wm,
            )
            if wm is not None:
                wm.remove_all()
            return 130

        # Sprint F-2: clean up worktrees on normal exit
        if wm is not None:
            wm.remove_all()
```

- [ ] **Step 21: Run full suite — confirm no regressions**

```bash
pytest orchestrator/tests/ -q 2>&1 | tail -5
# Expected: 1107 passed, 2 skipped (same count as after Task 3)
```

- [ ] **Step 22: Commit**

```bash
git add orchestrator/orch.py
git commit -m "feat(dispatch): worktree mode — isolate each agent in its own git branch"
```

---

## Task 5: Integration tests for worktree mode in orch.py

**Files:**
- Create: `orchestrator/tests/test_orch_worktree.py`

- [ ] **Step 1: Write tests**

Create `orchestrator/tests/test_orch_worktree.py`:

```python
"""Integration tests for worktree mode wiring in orch.py.

These tests mock WorktreeManager and verify that _spawn_one and _reap_once
call create/push/remove at the right times. No real git commands are run.
"""
from __future__ import annotations

from dataclasses import dataclass, field
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from orchestrator.worktree import WorktreeManager, WorktreeError


# ---- helpers ----------------------------------------------------------------


def _mk_wm(tmp_path: Path, worktree_path: Path | None = None) -> MagicMock:
    """Return a mock WorktreeManager whose create() returns a fixed path."""
    wm = MagicMock(spec=WorktreeManager)
    wm.create.return_value = worktree_path or (tmp_path / ".worktrees" / "F2.1.T1")
    return wm


# ---- WorktreeManager.remove_all called on hard SIGKILL ----------------------


def test_install_sigint_calls_remove_all_on_second_signal() -> None:
    """On the second SIGINT (hard kill), remove_all() must be called."""
    import signal
    from orchestrator.orch import _DrainFlag, _install_sigint

    drain = _DrainFlag()
    drain.set = True  # simulate first signal already received
    in_flight: dict = {}
    wm = MagicMock(spec=WorktreeManager)

    _install_sigint(drain, in_flight, wm=wm)

    # Trigger the handler as if a second SIGINT arrived
    handler = signal.getsignal(signal.SIGINT)
    handler(signal.SIGINT, None)

    wm.remove_all.assert_called_once()


def test_install_sigint_no_wm_does_not_raise_on_second_signal() -> None:
    """Without a WorktreeManager, second signal must still work (wm=None)."""
    import signal
    from orchestrator.orch import _DrainFlag, _install_sigint

    drain = _DrainFlag()
    drain.set = True
    in_flight: dict = {}

    _install_sigint(drain, in_flight, wm=None)

    handler = signal.getsignal(signal.SIGINT)
    handler(signal.SIGINT, None)  # must not raise


# ---- Reap loop: push on success, skip on failure, always remove -------------


def test_reap_calls_push_and_remove_on_success(tmp_path: Path) -> None:
    """When a task succeeds and worktree_path is set, reap must push then remove."""
    from orchestrator.orch import _reap_once, InFlight, _DrainFlag
    from orchestrator.dispatcher import DispatchResult, ClaudeBackend
    from orchestrator.models import Task, RouteEntry, Dispatch

    # Build a minimal InFlight with worktree_path set
    task = Task(id="F2.1.T1", title="test", model="claude", status="in_progress")
    route = RouteEntry(backend="claude", cli_model="claude-sonnet-4-6")
    backend = MagicMock(spec=ClaudeBackend)
    backend.parse_result.return_value = DispatchResult(exit_code=0, success=True)

    dispatch_obj = MagicMock(spec=Dispatch)
    dispatch_obj.log_path = str(tmp_path / "test.log")
    dispatch_obj.attempt = 1

    wt_path = tmp_path / ".worktrees" / "F2.1.T1"
    entry = InFlight(
        task=task,
        route=route,
        backend=backend,
        dispatch=dispatch_obj,
        started_at_mono=0.0,
        timeout_s=3600.0,
        worktree_path=wt_path,
    )

    wm = _mk_wm(tmp_path, wt_path)

    # Patch os.waitpid to return our fake PID
    fake_pid = 99999
    in_flight = {fake_pid: entry}
    queue = MagicMock()
    run_file = MagicMock()
    event_log = MagicMock()
    spend_log = MagicMock()
    cfg: dict = {}

    gsem = MagicMock()
    psem = {"claude": MagicMock()}

    import os
    with patch("os.waitpid", side_effect=[(fake_pid, 0), ChildProcessError]):
        with patch("orchestrator.orch._read_log_safely", return_value=""):
            with patch("orchestrator.orch._post_run_checks", return_value=(
                DispatchResult(exit_code=0, success=True), None,
            )):
                with patch("orchestrator.orch._record_spend"):
                    _reap_once(
                        in_flight, queue, run_file, event_log, spend_log,
                        cfg, tmp_path, gsem, psem,
                        wm=wm,
                    )

    wm.push.assert_called_once_with("F2.1.T1")
    wm.remove.assert_called_once_with("F2.1.T1")


def test_reap_skips_push_on_failure(tmp_path: Path) -> None:
    """When a task fails, reap must NOT push the branch but must still remove."""
    from orchestrator.orch import _reap_once, InFlight
    from orchestrator.dispatcher import DispatchResult, ClaudeBackend
    from orchestrator.models import Task, RouteEntry, Dispatch

    task = Task(id="F2.1.T2", title="test", model="claude", status="in_progress")
    route = RouteEntry(backend="claude", cli_model="claude-sonnet-4-6")
    backend = MagicMock(spec=ClaudeBackend)
    backend.parse_result.return_value = DispatchResult(exit_code=1, success=False)

    dispatch_obj = MagicMock(spec=Dispatch)
    dispatch_obj.log_path = str(tmp_path / "test.log")
    dispatch_obj.attempt = 1

    wt_path = tmp_path / ".worktrees" / "F2.1.T2"
    entry = InFlight(
        task=task,
        route=route,
        backend=backend,
        dispatch=dispatch_obj,
        started_at_mono=0.0,
        timeout_s=3600.0,
        worktree_path=wt_path,
    )

    wm = _mk_wm(tmp_path, wt_path)
    fake_pid = 88888
    in_flight = {fake_pid: entry}
    queue = MagicMock()
    run_file = MagicMock()
    event_log = MagicMock()
    spend_log = MagicMock()
    cfg: dict = {}
    gsem = MagicMock()
    psem = {"claude": MagicMock()}

    with patch("os.waitpid", side_effect=[(fake_pid, 256), ChildProcessError]):
        with patch("orchestrator.orch._read_log_safely", return_value=""):
            with patch("orchestrator.orch._post_run_checks", return_value=(
                DispatchResult(exit_code=1, success=False), None,
            )):
                with patch("orchestrator.orch._record_spend"):
                    _reap_once(
                        in_flight, queue, run_file, event_log, spend_log,
                        cfg, tmp_path, gsem, psem,
                        wm=wm,
                    )

    wm.push.assert_not_called()
    wm.remove.assert_called_once_with("F2.1.T2")


def test_reap_logs_warning_when_push_fails(tmp_path: Path) -> None:
    """Push failure must log a warning but not downgrade the task result."""
    from orchestrator.orch import _reap_once, InFlight
    from orchestrator.dispatcher import DispatchResult, ClaudeBackend
    from orchestrator.models import Task, RouteEntry, Dispatch

    task = Task(id="F2.1.T3", title="test", model="claude", status="in_progress")
    route = RouteEntry(backend="claude", cli_model="claude-sonnet-4-6")
    backend = MagicMock(spec=ClaudeBackend)

    dispatch_obj = MagicMock(spec=Dispatch)
    dispatch_obj.log_path = str(tmp_path / "test.log")
    dispatch_obj.attempt = 1

    wt_path = tmp_path / ".worktrees" / "F2.1.T3"
    entry = InFlight(
        task=task,
        route=route,
        backend=backend,
        dispatch=dispatch_obj,
        started_at_mono=0.0,
        timeout_s=3600.0,
        worktree_path=wt_path,
    )

    wm = _mk_wm(tmp_path, wt_path)
    wm.push.side_effect = WorktreeError("F2.1.T3", ["git", "push"], "network error")

    fake_pid = 77777
    in_flight = {fake_pid: entry}
    queue = MagicMock()
    run_file = MagicMock()
    event_log = MagicMock()
    spend_log = MagicMock()
    cfg: dict = {}
    gsem = MagicMock()
    psem = {"claude": MagicMock()}

    with patch("os.waitpid", side_effect=[(fake_pid, 0), ChildProcessError]):
        with patch("orchestrator.orch._read_log_safely", return_value=""):
            with patch("orchestrator.orch._post_run_checks", return_value=(
                DispatchResult(exit_code=0, success=True), None,
            )):
                with patch("orchestrator.orch._record_spend"):
                    # Must not raise even though push failed
                    _reap_once(
                        in_flight, queue, run_file, event_log, spend_log,
                        cfg, tmp_path, gsem, psem,
                        wm=wm,
                    )

    # remove still called even when push failed
    wm.remove.assert_called_once_with("F2.1.T3")
```

- [ ] **Step 2: Run — expect PASS**

```bash
pytest orchestrator/tests/test_orch_worktree.py -v
# Expected: all 5 tests PASS
```

- [ ] **Step 3: Full suite**

```bash
pytest orchestrator/tests/ -q 2>&1 | tail -5
# Expected: 1112 passed, 2 skipped (+5 from this task)
```

- [ ] **Step 4: Commit**

```bash
git add orchestrator/tests/test_orch_worktree.py
git commit -m "test(worktree): integration tests for _reap_once and _install_sigint worktree wiring"
```

---

## Task 6: Final — full suite + PR

- [ ] **Step 1: Run full test suite**

```bash
pytest orchestrator/tests/ -q 2>&1 | tail -10
# Expected: ≥ 1112 passed, 2 skipped, 0 new failures
```

- [ ] **Step 2: Verify the feature is documented in config.yaml**

Open `orchestrator/config.yaml`. Add the `dispatch:` section if missing (or update it if it exists):

```yaml
# Sprint F-2: worktree isolation — each task runs in a dedicated git branch.
# When worktree_mode is true, orch creates git worktrees under .worktrees/,
# runs each agent there, and pushes the branch to origin on success.
dispatch:
  worktree_mode: false   # opt-in — set to true to enable per-task isolation
  base_branch: main      # base branch for new worktrees
```

- [ ] **Step 3: Run suite once more after config.yaml edit**

```bash
pytest orchestrator/tests/ -q 2>&1 | tail -5
# Expected: same pass count — no regression from config.yaml edit
```

- [ ] **Step 4: Commit config update**

```bash
git add orchestrator/config.yaml
git commit -m "docs(config): document dispatch.worktree_mode and dispatch.base_branch"
```

- [ ] **Step 5: Push branch and open PR**

```bash
git push -u origin sprint-f1/clean-foundation
gh pr create \
  --title "feat: Sprint F-2 — per-task git worktree isolation" \
  --body "$(cat <<'EOF'
## Summary

- New `WorktreeManager` (`orchestrator/worktree.py`) owns the git worktree lifecycle: create, push, remove, remove_all.
- `dispatch.worktree_mode: false` (opt-in). When true, each task runs in `.worktrees/<task-id>/` on branch `orch/<task-id>`.
- Branch is pushed to origin with `--force-with-lease` on agent success; not pushed on failure.
- Worktrees are removed after every task (success or failure) and on SIGTERM (hard kill path).
- `ProjectPaths.override_root` lets project-file paths resolve inside the worktree while `state_dir` (SQLite) always stays at the main project root — all worktrees share one DB.
- `.worktrees/` added to `orch init`-generated `.gitignore`.

## Baseline stats
- Before: 1088 passed, 2 skipped
- After: ≥ 1112 passed, 2 skipped (+24 new tests)

## Test plan
- [x] `pytest orchestrator/tests/ -q` → ≥ 1112 passed
- [x] `test_worktree.py` — 16 tests, all git calls mocked
- [x] `test_orch_worktree.py` — 5 integration tests for lifecycle wiring
- [x] `test_paths.py` — override_root isolation verified
- [x] `test_init.py` — .worktrees/ in generated gitignore
EOF
)"
```
