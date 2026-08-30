# Sprint F-4: PR Automático + CI Polling — Design Spec

**Date:** 2026-08-26
**Status:** Draft — pending implementation
**Scope:** VcsProvider abstraction, PR creation on task success, CI polling in dispatch loop, re-dispatch with CI feedback, dashboard badges

---

## Goal

Close the quality loop: today orch marks a task `done` without knowing if the generated code passes CI. F-4 adds automatic PR creation after every successful worktree push, polls CI status, and re-dispatches the agent with CI failure logs so it can self-correct — up to a configurable cap.

---

## Constraints

- Only active when `dispatch.worktree_mode: true` AND `vcs.auto_pr: true`. Both flags must be set. Existing projects with neither flag are completely unaffected.
- Auth via CLI tool (`gh` for GitHub, `glab` for GitLab). No token config — users must have the CLI authenticated. Silent degradation if CLI is not available.
- GitLab support includes self-hosted instances via `vcs.host`.

---

## Layer 1 — Data model

### Migration `005_pr_ci_tracking.sql`

```sql
PRAGMA user_version = 5;

ALTER TABLE tasks_runtime ADD COLUMN pr_url     TEXT;
ALTER TABLE tasks_runtime ADD COLUMN ci_status  TEXT
    CHECK (ci_status IN ('pending', 'success', 'failure', 'skipped'));
ALTER TABLE tasks_runtime ADD COLUMN ci_attempts INTEGER NOT NULL DEFAULT 0;
```

`pr_url NULL` → no PR created (worktree mode off, or auto_pr disabled).
`ci_status NULL` → no CI tracking active for this task.
`ci_attempts` → count of CI-triggered re-dispatches so far.

### `SqliteBackend` additions

```python
def set_task_pr(self, task_id: str, pr_url: str) -> None:
    """Store PR URL and set ci_status to 'pending'."""

def get_tasks_with_pending_ci(self) -> list[dict]:
    """Return tasks where pr_url IS NOT NULL AND ci_status = 'pending'."""

def set_task_ci_status(self, task_id: str, status: str) -> None:
    """Update ci_status. Valid values: pending | success | failure | skipped."""

def increment_ci_attempts(self, task_id: str) -> None:
    """Increment ci_attempts by 1."""
```

---

## Layer 2 — VcsProvider abstraction

### Package layout

```
orchestrator/vcs/
    __init__.py          # exports get_vcs_provider()
    protocol.py          # VcsProvider Protocol
    github.py            # GitHubProvider (gh CLI)
    gitlab.py            # GitLabProvider (glab CLI)
```

### `VcsProvider` Protocol (`protocol.py`)

```python
from typing import Protocol

class VcsProvider(Protocol):
    def create_pr(
        self,
        task_id: str,
        title: str,
        body: str,
        head: str,
        base: str,
    ) -> str | None:
        """Open a PR/MR. Returns the PR URL or None on failure."""

    def get_ci_status(self, pr_url: str) -> str:
        """
        Returns one of: 'success' | 'failure' | 'pending'.
        'pending' covers in_progress, queued, waiting states.
        """

    def get_ci_logs(self, pr_url: str) -> str:
        """Return failed CI job logs as plain text (truncated to ~8000 chars)."""
```

### `GitHubProvider` (`github.py`)

Uses `gh` CLI. All calls via `subprocess.run(["gh", ...], capture_output=True, text=True)`.

- `create_pr` → `gh pr create --title ... --body ... --base ... --head ...` → returns stdout (PR URL)
- `get_ci_status` → `gh pr checks <pr_url> --json state,conclusion` → maps to `pending/success/failure`
- `get_ci_logs` → `gh run view <run_id> --log-failed` → first 8000 chars

### `GitLabProvider` (`gitlab.py`)

Uses `glab` CLI. Self-hosted via `GITLAB_HOST` env var set from `vcs.host`.

- `create_pr` → `glab mr create --title ... --description ... --target-branch ... --source-branch ...`
- `get_ci_status` → `glab pipeline status --pipeline-id <id>` → maps to `pending/success/failure`
- `get_ci_logs` → `glab pipeline jobs --pipeline-id <id>` + `glab pipeline trace <job_id>`

### Factory (`__init__.py`)

```python
def get_vcs_provider(cfg: dict) -> VcsProvider:
    vcs_cfg = cfg.get("vcs", {})
    provider = vcs_cfg.get("provider", "github")
    host = vcs_cfg.get("host", "github.com")
    if provider == "gitlab":
        return GitLabProvider(host=host)
    return GitHubProvider()
```

---

## Layer 3 — Config

### New `vcs` section in `config.yaml`

```yaml
# VCS integration — PR automation and CI polling.
# Requires dispatch.worktree_mode: true to have any effect.
vcs:
  provider: github           # github | gitlab
  host: github.com           # self-hosted: gitlab.mycompany.com
  auto_pr: false             # opt-in — false = no behaviour change
  ci_max_retries: 1          # CI-triggered re-dispatches before blocking
  ci_poll_interval_s: 30     # seconds between CI status checks
```

### `_apply_defaults` addition in `config_loader.py`

```python
cfg.setdefault("vcs", {})
cfg["vcs"].setdefault("provider", "github")
cfg["vcs"].setdefault("host", "github.com")
cfg["vcs"].setdefault("auto_pr", False)
cfg["vcs"].setdefault("ci_max_retries", 1)
cfg["vcs"].setdefault("ci_poll_interval_s", 30)
```

---

## Layer 4 — Dispatch loop integration

### PR creation in `_reap_once()` (`orch.py`)

After `wm.push(task_id)` succeeds, if `vcs.auto_pr` is true:

```python
pr_url = vcs_provider.create_pr(
    task_id=task_id,
    title=task.title,
    body=f"Task: `{task_id}`\nSpec: {task.spec_ref or 'n/a'}\n\n{task.reason or ''}".strip(),
    head=f"orch/{task_id}",
    base=cfg["dispatch"]["base_branch"],
)
if pr_url:
    backend.set_task_pr(task_id, pr_url)
    # Do NOT call mark_done() yet — wait for CI
else:
    # PR creation failed — degrade gracefully, mark done normally
    mark_done(task_id)
```

### CI watcher `_check_ci_once()` (`orch.py`)

New function called in the dispatch loop on every tick, throttled by `ci_poll_interval_s`:

```python
def _check_ci_once(cfg, backend, vcs_provider, queue, wm, last_ci_check):
    now = time.monotonic()
    interval = cfg["vcs"]["ci_poll_interval_s"]
    if now - last_ci_check < interval:
        return last_ci_check  # not time yet

    pending = backend.get_tasks_with_pending_ci()
    for task in pending:
        status = vcs_provider.get_ci_status(task["pr_url"])
        if status == "success":
            backend.set_task_ci_status(task["task_id"], "success")
            queue.mark_done(task["task_id"])
        elif status == "failure":
            max_retries = cfg["vcs"]["ci_max_retries"]
            if task["ci_attempts"] < max_retries:
                logs = vcs_provider.get_ci_logs(task["pr_url"])
                _redispatch_with_ci_feedback(task, logs, cfg, backend, wm)
                backend.increment_ci_attempts(task["task_id"])
                backend.set_task_ci_status(task["task_id"], "pending")
            else:
                backend.set_task_ci_status(task["task_id"], "failure")
                backend.set_task_status(task["task_id"], "blocked")
        # status == "pending" → do nothing, check next tick

    return now
```

### Re-dispatch with CI feedback `_redispatch_with_ci_feedback()`

```python
def _redispatch_with_ci_feedback(task, ci_logs, cfg, backend, wm):
    # Reconstruct worktree from existing branch (not fresh from base)
    wm.recreate(task["task_id"])   # checks out orch/<task_id> into new worktree dir
    # Write CI logs to a context file in the worktree
    context_file = wm.worktree_path(task["task_id"]) / ".orch-ci-feedback.md"
    context_file.write_text(
        f"# CI Failure — Please fix\n\n```\n{ci_logs}\n```\n"
    )
    # Re-dispatch with extra context flag (backend-specific, e.g. --context for claude)
    _dispatch_task(task, cfg, backend, wm, extra_context=context_file)
```

`wm.recreate(task_id)` is a new method on `WorktreeManager`: checks out the existing remote branch `orch/<task_id>` into a fresh local worktree path (does not start from `base_branch`).

---

## Layer 5 — Dashboard

### API

`GET /api/tasks` already returns `tasks_runtime` rows. The new `pr_url` and `ci_status` columns are returned automatically — no server.py changes needed.

### SPA — `Task` type (`frontend/src/lib/types.ts`)

```typescript
export interface Task {
  // existing fields ...
  pr_url?: string | null
  ci_status?: "pending" | "success" | "failure" | "skipped" | null
  ci_attempts?: number
}
```

### SPA — Task card component

In the existing task card (used by `KanbanPage`, `MilestonesPage`):

- If `pr_url` is set: show a small `[↗ PR]` link badge
- If `ci_status` is set: show CI indicator using `labelForStatus` with new ci-specific labels:

```yaml
# config.yaml presentation.status_labels additions
ci_pending:  "Validando..."
ci_success:  "Tests aprobados"
ci_failure:  "Corrección en progreso"
```

CI indicator is shown below the task status badge, not replacing it.

---

## Files touched

| File | Action |
|------|--------|
| `orchestrator/state/sqlite_migrations/005_pr_ci_tracking.sql` | **Create** |
| `orchestrator/state/sqlite_backend.py` | Modify — 4 new methods |
| `orchestrator/vcs/__init__.py` | **Create** |
| `orchestrator/vcs/protocol.py` | **Create** |
| `orchestrator/vcs/github.py` | **Create** |
| `orchestrator/vcs/gitlab.py` | **Create** |
| `orchestrator/config.yaml` | Modify — add `vcs:` section |
| `orchestrator/config_loader.py` | Modify — `vcs` defaults in `_apply_defaults` |
| `orchestrator/orch.py` | Modify — `_create_pr`, `_check_ci_once`, `_redispatch_with_ci_feedback` |
| `orchestrator/state/sqlite_backend.py` | Modify — `wm.recreate()` |
| `orchestrator/worktree.py` | Modify — `recreate()` method |
| `frontend/src/lib/types.ts` | Modify — `pr_url`, `ci_status`, `ci_attempts` on Task |
| `frontend/src/components/` | Modify — task card PR badge + CI indicator |
| `orchestrator/tests/test_vcs_github.py` | **Create** |
| `orchestrator/tests/test_vcs_gitlab.py` | **Create** |
| `orchestrator/tests/test_ci_dispatch.py` | **Create** |

---

## Tests

- `VcsProvider` protocol satisfied by both `GitHubProvider` and `GitLabProvider` (structural check)
- `GitHubProvider.create_pr` calls correct `gh` CLI args (subprocess mock)
- `GitHubProvider.get_ci_status` maps gh JSON output to `pending/success/failure`
- `GitLabProvider.create_pr` sets `GITLAB_HOST` env and calls `glab` CLI
- `get_vcs_provider(cfg)` returns correct provider class per `vcs.provider`
- `_check_ci_once`: success path marks task done; failure+cap → blocked; failure+retry → re-dispatch + ci_attempts incremented
- `set_task_pr` stores URL and sets `ci_status='pending'`
- `get_tasks_with_pending_ci` returns only tasks with pending CI
- `auto_pr: false` → no PR created, task marked done immediately (regression test)

---

## Branch

`sprint-f4/pr-ci-automation`

---

## Out of scope

- `auto_merge` (F-4b)
- `orch init` generating `.github/workflows/orch-ci.yml` (F-4b)
- Bitbucket / Azure DevOps support
- Email/Slack notifications on CI result (F-5)

*Spec written: 2026-08-26*
