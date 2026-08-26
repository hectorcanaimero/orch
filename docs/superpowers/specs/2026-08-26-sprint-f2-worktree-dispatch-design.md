# Sprint F-2: Worktree Dispatch Design

**Date:** 2026-08-26
**Status:** Approved
**Scope:** Git worktree isolation per task — each agent works in a dedicated branch/worktree, branch is pushed to remote on success.

---

## Context

orch v0.7.0 dispatches agents that all work in the same project directory. Concurrency is controlled by a threading semaphore, but two agents running in parallel can write conflicting changes to the same files. There is no file-level isolation between tasks.

Sprint F-2 introduces `dispatch.worktree_mode`, an opt-in mode where orch creates a dedicated `git worktree` for each task before dispatch, runs the agent in that isolated directory, pushes the resulting branch to origin on success, and cleans up the worktree. This is the foundational primitive for Sprint F-3 (PR creation + CI polling).

---

## Design Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Opt-in vs default | Opt-in (`worktree_mode: false`) | Existing projects unaffected |
| Branch naming | `orch/<task-id>` (e.g. `orch/F2.1.T3`) | Clear namespace, no collision with human branches |
| Base branch | Configurable `dispatch.base_branch: main` | Developer controls sprint branch if needed |
| Worktrees location | `.worktrees/<task-id>/` gitignored | Hidden from project tree, easy to glob-clean |
| State (SQLite) location | Always main project root `.orchestrator/` | State is shared across all tasks, not per-worktree |
| Push on failure | No — branch not published on agent failure | Don't pollute remote with incomplete work |
| Retry / branch exists | `git push --force-with-lease` | Same task ID = same branch, overwrite previous attempt |
| PR creation | Out of scope (Sprint F-3) | Clean boundary; developer creates PR manually in F-2 |

---

## Behavior

When `dispatch.worktree_mode: true`, the lifecycle of each dispatched task is:

```
orch run
  └─ WorktreeManager.create(task_id, base_branch)
       git worktree add .worktrees/<task-id> -b orch/<task-id> <base_branch>
  └─ agent spawned with cwd = .worktrees/<task-id>/
  └─ agent finishes
       success → WorktreeManager.push(task_id)
                   git push --force-with-lease -u origin orch/<task-id>
       always  → WorktreeManager.remove(task_id)
                   git worktree remove .worktrees/<task-id>/ --force
```

When `dispatch.worktree_mode: false` (default), behavior is unchanged from v0.7.0.

---

## Architecture

### New File: `orchestrator/worktree.py`

Single responsibility: worktree lifecycle management. Calls `git` via subprocess. All git commands are run from the main project root (not the worktree path).

```python
class WorktreeManager:
    def __init__(self, project_root: Path): ...

    def worktree_path(self, task_id: str) -> Path:
        return self.project_root / ".worktrees" / task_id

    def branch_name(self, task_id: str) -> str:
        return f"orch/{task_id}"

    def create(self, task_id: str, base_branch: str) -> Path:
        """Creates worktree. If stale path exists, removes it first."""

    def push(self, task_id: str) -> None:
        """git push --force-with-lease -u origin orch/<task-id>"""

    def remove(self, task_id: str) -> None:
        """git worktree remove --force. No-op if worktree doesn't exist."""

    def remove_all(self) -> None:
        """Remove all worktrees under .worktrees/. Called on SIGTERM."""

    def exists(self, task_id: str) -> bool:
        """True if .worktrees/<task-id>/ exists on disk."""
```

**Error handling**: all git failures raise `WorktreeError(task_id, cmd, stderr)`. The dispatcher catches this and marks the task as failed — same as a subprocess spawn failure.

### Modified: `orchestrator/dispatcher.py`

Three insertion points:

1. **Pre-spawn** (when `worktree_mode`):
   ```python
   worktree_path = wm.create(task_id, base_branch)
   effective_root = worktree_path
   ```

2. **Post-success** (when `worktree_mode`):
   ```python
   wm.push(task_id)
   ```

3. **Cleanup** (always, when `worktree_mode`):
   ```python
   wm.remove(task_id)
   ```

`effective_root` is passed as the `cwd` for the agent subprocess and as `override_root` to `ProjectPaths`.

### Modified: `orchestrator/paths.py`

`ProjectPaths.__init__` gains `override_root: Path | None = None`.

When set, all project-file properties (`tasks_json`, `router_yaml`, `scripts_dir`, etc.) resolve relative to `override_root`. **Exception**: `state_dir` always resolves from the original `project_root` — SQLite state is shared and must not move into the worktree.

```python
@property
def state_dir(self) -> Path:
    # Always uses self._project_root, never override_root
    return self._project_root / ".orchestrator" / "state" / ...
```

### Modified: `orchestrator/orch.py`

- Reads `dispatch.worktree_mode` (bool, default `False`) and `dispatch.base_branch` (str, default `"main"`) from config
- Passes both to the dispatcher
- In SIGTERM handler: calls `WorktreeManager(project_root).remove_all()` before existing cleanup

### Modified: `orchestrator/init_cmd.py`

Adds `.worktrees/` to the generated `.gitignore` template.

### Modified: `orchestrator/config.yaml` (schema)

```yaml
dispatch:
  worktree_mode: false   # opt-in; when true, each task runs in an isolated git worktree
  base_branch: main      # base branch for new worktrees
```

---

## State Isolation

The worktree contains only the project's source files — a clean checkout of `base_branch`. The following are **not** in the worktree:

| Resource | Location | Shared? |
|----------|----------|---------|
| SQLite (`orch.db`) | `<main_root>/.orchestrator/state/` | ✅ shared |
| Task logs | `<main_root>/.orchestrator/state/<pid>/logs/` | ✅ per-task, main root |
| `tasks.json` | `<main_root>/tasks.json` | ✅ read-only at dispatch |
| Source files | `<worktree_root>/` | ❌ isolated per task |
| `AGENTS.md` | `<worktree_root>/AGENTS.md` | part of git tree, present in worktree |

The `AGENTS.md` committed to the repo is available in the worktree (it's part of the git tree). Its SQLite paths use paths relative to the project root — from inside the worktree these paths don't resolve correctly. This is a known limitation of F-2: the agent's primary job is to write code, not to query SQLite directly. Orch manages all state writes; the agent only needs the file system. A proper fix (injecting the absolute SQLite path into the dispatch prompt via `prompt_builder.py`) is deferred to F-3 when the full worktree + PR flow is wired up.

---

## Error & Cleanup Cases

| Scenario | Behavior |
|----------|----------|
| `git worktree add` fails | `WorktreeError` raised → task marked failed, no subprocess spawned |
| Agent fails (non-zero exit) | Worktree removed, branch NOT pushed |
| Agent killed by timeout | Existing SIGTERM→grace→SIGKILL flow runs first, then worktree removed |
| orch crashes before cleanup | `.worktrees/<task-id>/` left on disk. Next `create()` for same task_id detects stale path, runs `git worktree remove --force` before creating fresh one |
| orch SIGTERM | `remove_all()` called — removes all `.worktrees/*/` directories |
| Branch already exists on remote | `--force-with-lease` overwrites. Safe: same task_id = same logical branch, retry is intentional |
| Stale worktree path on `create()` | `exists()` check → `remove()` → then `add` |

---

## Testing Requirements

**Baseline**: 1088 passed + 2 skipped. New work must not regress the green count.

### `orchestrator/tests/test_worktree.py` (new)

All git calls mocked via `unittest.mock.patch("subprocess.run")`.

- `test_create_runs_correct_git_command` — verifies `git worktree add .worktrees/<id> -b orch/<id> main`
- `test_create_cleans_stale_path_first` — if `.worktrees/<id>/` exists, removes before creating
- `test_push_uses_force_with_lease` — verifies `git push --force-with-lease -u origin orch/<id>`
- `test_remove_uses_force_flag`
- `test_remove_is_noop_when_worktree_missing`
- `test_remove_all_removes_all_active_worktrees`
- `test_exists_returns_true_when_path_present`
- `test_create_raises_worktree_error_on_git_failure`

### `orchestrator/tests/test_dispatcher.py` (additions)

- `test_dispatch_worktree_mode_creates_worktree_before_spawn`
- `test_dispatch_worktree_mode_pushes_branch_on_success`
- `test_dispatch_worktree_mode_does_not_push_on_failure`
- `test_dispatch_worktree_mode_removes_worktree_on_success`
- `test_dispatch_worktree_mode_removes_worktree_on_failure`

### `orchestrator/tests/test_init.py` (addition)

- `test_init_gitignore_includes_worktrees_dir`

### `orchestrator/tests/test_paths.py` (addition)

- `test_override_root_changes_project_files_not_state_dir` — state_dir still resolves from original project_root when override_root is set

---

## Out of Scope

- PR creation (Sprint F-3)
- CI polling / auto-merge (Sprint F-3)
- Conflict resolution between worktrees (human responsibility in F-2)
- Support for non-GitHub remotes (assumption: origin is GitHub)
- `orch worktree` subcommand (no CLI surface added in F-2 — it's a dispatch-time behavior)
