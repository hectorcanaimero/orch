# Sprint F-4: PR Automático + CI Polling — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** After every successful worktree push, automatically create a PR, poll CI status, and re-dispatch the agent with CI failure logs so it can self-correct — closing the quality loop end-to-end.

**Architecture:** Three layers add to the existing F-2 worktree infrastructure: (1) a `VcsProvider` Protocol abstracted over `gh`/`glab` CLIs; (2) four new `SqliteBackend` methods + a `005_pr_ci_tracking.sql` migration for `pr_url`, `ci_status`, and `ci_attempts`; (3) two new functions in `orch.py` (`_create_pr_after_push` hooked into the existing reap loop, `_check_ci_once` called in the main tick) plus `wm.recreate()` for CI re-dispatch. Both flags `dispatch.worktree_mode: true` AND `vcs.auto_pr: true` must be set — existing projects are completely unaffected.

**Tech Stack:** Python 3.11+, SQLite (via existing `sqlite_backend.py` migration infrastructure), `subprocess.run` for `gh`/`glab` CLI calls, TypeScript + React for the dashboard card updates.

**Spec:** `docs/superpowers/specs/2026-08-26-sprint-f4-pr-ci-automation-design.md`

**Branch:** `sprint-f4/pr-ci-automation`

**Test baseline:** 1145 passed (post Sprint F-3). Do not regress.

---

## File Map

| File | Action | Purpose |
|------|--------|---------|
| `orchestrator/state/sqlite_migrations/005_pr_ci_tracking.sql` | **Create** | Add `pr_url`, `ci_status`, `ci_attempts` to `tasks_runtime` |
| `orchestrator/state/sqlite_backend.py` | Modify | 4 new methods: `set_task_pr`, `get_tasks_with_pending_ci`, `set_task_ci_status`, `increment_ci_attempts` |
| `orchestrator/vcs/__init__.py` | **Create** | `get_vcs_provider(cfg)` factory |
| `orchestrator/vcs/protocol.py` | **Create** | `VcsProvider` Protocol |
| `orchestrator/vcs/github.py` | **Create** | `GitHubProvider` — `gh` CLI |
| `orchestrator/vcs/gitlab.py` | **Create** | `GitLabProvider` — `glab` CLI |
| `orchestrator/config_loader.py` | Modify | `vcs` defaults in `_apply_defaults`; ci_* status labels |
| `orchestrator/config.yaml` | Modify | `vcs:` section |
| `orchestrator/worktree.py` | Modify | `recreate(task_id)` method |
| `orchestrator/orch.py` | Modify | `_reap_once` PR hook; `_check_ci_once`; `_redispatch_with_ci_feedback`; main loop wiring; `_spawn_one` uses existing worktree |
| `frontend/src/lib/types.ts` | Modify | `pr_url`, `ci_status`, `ci_attempts` on `Task` |
| `frontend/src/components/TaskCard.tsx` | Modify | PR badge + CI indicator |
| `orchestrator/tests/test_vcs_github.py` | **Create** | GitHubProvider unit tests |
| `orchestrator/tests/test_vcs_gitlab.py` | **Create** | GitLabProvider unit tests |
| `orchestrator/tests/test_ci_dispatch.py` | **Create** | SqliteBackend CI methods + dispatch integration tests |

---

## Task 1: Migration 005 + SqliteBackend CI Methods

**Files:**
- Create: `orchestrator/state/sqlite_migrations/005_pr_ci_tracking.sql`
- Modify: `orchestrator/state/sqlite_backend.py`
- Test: `orchestrator/tests/test_ci_dispatch.py`

- [ ] **Step 1: Write the failing tests for the 4 new backend methods**

```python
# orchestrator/tests/test_ci_dispatch.py
import pytest
from pathlib import Path
from orchestrator.state.sqlite_backend import SqliteBackend


@pytest.fixture
def backend(tmp_path):
    db_path = tmp_path / "orch.db"
    b = SqliteBackend(project_id="p1", db_path=db_path, project_root=tmp_path)
    b.bootstrap([])
    # Seed a minimal tasks_runtime row directly so we don't need full task wiring.
    import sqlite3
    conn = sqlite3.connect(str(db_path))
    conn.execute("PRAGMA foreign_keys = ON")
    conn.execute(
        "INSERT OR IGNORE INTO tasks_runtime "
        "(project_id, task_id, status, updated_at) VALUES (?, ?, 'done', '2026-01-01T00:00:00Z')",
        ("p1", "task-001"),
    )
    conn.commit()
    conn.close()
    return b


def test_set_task_pr_stores_url_and_sets_pending(backend):
    backend.set_task_pr("task-001", "https://github.com/org/repo/pull/42")
    rows = backend.get_tasks_with_pending_ci()
    assert len(rows) == 1
    assert rows[0]["task_id"] == "task-001"
    assert rows[0]["pr_url"] == "https://github.com/org/repo/pull/42"
    assert rows[0]["ci_status"] == "pending"
    assert rows[0]["ci_attempts"] == 0


def test_get_tasks_with_pending_ci_only_returns_pending(backend):
    backend.set_task_pr("task-001", "https://github.com/org/repo/pull/42")
    backend.set_task_ci_status("task-001", "success")
    assert backend.get_tasks_with_pending_ci() == []


def test_set_task_ci_status_updates_status(backend):
    backend.set_task_pr("task-001", "https://github.com/org/repo/pull/42")
    backend.set_task_ci_status("task-001", "failure")
    rows = backend.get_tasks_with_pending_ci()  # no longer pending
    assert rows == []


def test_increment_ci_attempts(backend):
    backend.set_task_pr("task-001", "https://github.com/org/repo/pull/42")
    backend.increment_ci_attempts("task-001")
    backend.increment_ci_attempts("task-001")
    rows = backend.get_tasks_with_pending_ci()
    assert rows[0]["ci_attempts"] == 2


def test_get_tasks_with_pending_ci_null_pr_url_excluded(backend):
    # task without pr_url should never appear
    rows = backend.get_tasks_with_pending_ci()
    assert rows == []
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
cd /Volumes/PortableSSD/orch
pytest orchestrator/tests/test_ci_dispatch.py -v 2>&1 | head -30
```
Expected: FAIL — `set_task_pr` doesn't exist yet.

- [ ] **Step 3: Create the SQL migration**

```sql
-- orchestrator/state/sqlite_migrations/005_pr_ci_tracking.sql
-- Sprint F-4: PR automation + CI polling columns.
-- Tracks the GitHub/GitLab PR created after each worktree push and
-- the resulting CI check result. NULL pr_url = worktree mode off or auto_pr disabled.

PRAGMA user_version = 5;

ALTER TABLE tasks_runtime ADD COLUMN pr_url      TEXT;
ALTER TABLE tasks_runtime ADD COLUMN ci_status   TEXT
    CHECK (ci_status IN ('pending', 'success', 'failure', 'skipped'));
ALTER TABLE tasks_runtime ADD COLUMN ci_attempts INTEGER NOT NULL DEFAULT 0;
```

- [ ] **Step 4: Add the 4 new methods to `SqliteBackend`**

Open `orchestrator/state/sqlite_backend.py`. After the existing milestone methods (near the end of the class, before the closing of the class body), add:

```python
# ---- VCS / CI tracking (Sprint F-4) ------------------------------------

def set_task_pr(self, task_id: str, pr_url: str) -> None:
    """Store PR URL and initialise ci_status to 'pending'."""
    with self._write() as conn:
        conn.execute(
            "UPDATE tasks_runtime SET pr_url = ?, ci_status = 'pending', "
            "updated_at = ? WHERE project_id = ? AND task_id = ?",
            (pr_url, _utc_now_iso(), self.project_id, task_id),
        )

def get_tasks_with_pending_ci(self) -> list[dict]:
    """Return tasks_runtime rows where pr_url IS NOT NULL AND ci_status = 'pending'."""
    conn = self._conn()
    try:
        cur = conn.execute(
            "SELECT task_id, pr_url, ci_status, ci_attempts "
            "FROM tasks_runtime "
            "WHERE project_id = ? AND pr_url IS NOT NULL AND ci_status = 'pending'",
            (self.project_id,),
        )
        return [dict(row) for row in cur.fetchall()]
    finally:
        conn.close()

def set_task_ci_status(self, task_id: str, status: str) -> None:
    """Update ci_status. Valid values: pending | success | failure | skipped."""
    with self._write() as conn:
        conn.execute(
            "UPDATE tasks_runtime SET ci_status = ?, updated_at = ? "
            "WHERE project_id = ? AND task_id = ?",
            (status, _utc_now_iso(), self.project_id, task_id),
        )

def increment_ci_attempts(self, task_id: str) -> None:
    """Increment ci_attempts by 1."""
    with self._write() as conn:
        conn.execute(
            "UPDATE tasks_runtime SET ci_attempts = ci_attempts + 1, "
            "updated_at = ? WHERE project_id = ? AND task_id = ?",
            (_utc_now_iso(), self.project_id, task_id),
        )
```

- [ ] **Step 5: Run the tests and verify they pass**

```bash
pytest orchestrator/tests/test_ci_dispatch.py -v
```
Expected: 5 tests PASS.

- [ ] **Step 6: Run the full suite to check for regressions**

```bash
pytest --tb=short -q 2>&1 | tail -5
```
Expected: 1145+ passed, 0 new failures.

- [ ] **Step 7: Commit**

```bash
git add orchestrator/state/sqlite_migrations/005_pr_ci_tracking.sql \
        orchestrator/state/sqlite_backend.py \
        orchestrator/tests/test_ci_dispatch.py
git commit -m "feat(db): migration 005 — pr_url, ci_status, ci_attempts on tasks_runtime"
```

---

## Task 2: VcsProvider Protocol + Factory

**Files:**
- Create: `orchestrator/vcs/__init__.py`
- Create: `orchestrator/vcs/protocol.py`
- Test: `orchestrator/tests/test_vcs_github.py` (structural check only, implemented in Task 3)

- [ ] **Step 1: Write the failing structural test**

```python
# orchestrator/tests/test_vcs_github.py  (start with just the import test)
from orchestrator.vcs import get_vcs_provider
from orchestrator.vcs.protocol import VcsProvider


def test_get_vcs_provider_returns_github_by_default():
    cfg = {"vcs": {"provider": "github"}}
    provider = get_vcs_provider(cfg)
    # Must satisfy the VcsProvider Protocol (structural check)
    assert hasattr(provider, "create_pr")
    assert hasattr(provider, "get_ci_status")
    assert hasattr(provider, "get_ci_logs")


def test_get_vcs_provider_returns_gitlab_when_configured():
    cfg = {"vcs": {"provider": "gitlab", "host": "gitlab.example.com"}}
    provider = get_vcs_provider(cfg)
    assert hasattr(provider, "create_pr")
```

- [ ] **Step 2: Run test to verify it fails**

```bash
pytest orchestrator/tests/test_vcs_github.py -v 2>&1 | head -10
```
Expected: FAIL — `orchestrator.vcs` doesn't exist.

- [ ] **Step 3: Create `orchestrator/vcs/protocol.py`**

```python
# orchestrator/vcs/protocol.py
"""VcsProvider Protocol — implemented by GitHubProvider and GitLabProvider."""
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
        ...

    def get_ci_status(self, pr_url: str) -> str:
        """Return 'success' | 'failure' | 'pending'.

        'pending' covers in_progress, queued, and waiting states.
        """
        ...

    def get_ci_logs(self, pr_url: str) -> str:
        """Return failed CI job logs as plain text (truncated to ~8000 chars)."""
        ...
```

- [ ] **Step 4: Create `orchestrator/vcs/__init__.py`**

```python
# orchestrator/vcs/__init__.py
"""VCS integration — factory for the configured provider."""
from __future__ import annotations

from .protocol import VcsProvider


def get_vcs_provider(cfg: dict) -> VcsProvider:
    """Return the appropriate VcsProvider for the project config."""
    vcs_cfg = cfg.get("vcs", {})
    provider = vcs_cfg.get("provider", "github")
    host = vcs_cfg.get("host", "github.com")
    if provider == "gitlab":
        from .gitlab import GitLabProvider
        return GitLabProvider(host=host)
    from .github import GitHubProvider
    return GitHubProvider()


__all__ = ["VcsProvider", "get_vcs_provider"]
```

- [ ] **Step 5: Create `orchestrator/vcs/github.py` (stub — full impl in Task 3)**

```python
# orchestrator/vcs/github.py
"""GitHubProvider — delegates to the `gh` CLI."""
from __future__ import annotations

import logging
import subprocess

log = logging.getLogger(__name__)


class GitHubProvider:
    def create_pr(self, task_id: str, title: str, body: str, head: str, base: str) -> str | None:
        raise NotImplementedError

    def get_ci_status(self, pr_url: str) -> str:
        raise NotImplementedError

    def get_ci_logs(self, pr_url: str) -> str:
        raise NotImplementedError
```

- [ ] **Step 6: Create `orchestrator/vcs/gitlab.py` (stub — full impl in Task 4)**

```python
# orchestrator/vcs/gitlab.py
"""GitLabProvider — delegates to the `glab` CLI."""
from __future__ import annotations

import logging
import os

log = logging.getLogger(__name__)


class GitLabProvider:
    def __init__(self, host: str = "gitlab.com") -> None:
        self._host = host

    def create_pr(self, task_id: str, title: str, body: str, head: str, base: str) -> str | None:
        raise NotImplementedError

    def get_ci_status(self, pr_url: str) -> str:
        raise NotImplementedError

    def get_ci_logs(self, pr_url: str) -> str:
        raise NotImplementedError
```

- [ ] **Step 7: Run tests and verify they pass**

```bash
pytest orchestrator/tests/test_vcs_github.py -v
```
Expected: 2 PASS.

- [ ] **Step 8: Commit**

```bash
git add orchestrator/vcs/
git add orchestrator/tests/test_vcs_github.py
git commit -m "feat(vcs): VcsProvider protocol + factory skeleton"
```

---

## Task 3: GitHubProvider Implementation

**Files:**
- Modify: `orchestrator/vcs/github.py`
- Modify: `orchestrator/tests/test_vcs_github.py`

- [ ] **Step 1: Write the failing tests for GitHubProvider**

Add to `orchestrator/tests/test_vcs_github.py`:

```python
import json
from unittest.mock import patch, MagicMock
from orchestrator.vcs.github import GitHubProvider


def _mock_run(stdout="", returncode=0):
    m = MagicMock()
    m.stdout = stdout
    m.returncode = returncode
    m.stderr = ""
    return m


def test_create_pr_calls_gh_with_correct_args():
    pr_url = "https://github.com/org/repo/pull/42"
    with patch("subprocess.run", return_value=_mock_run(stdout=pr_url)) as mock_run:
        provider = GitHubProvider()
        result = provider.create_pr(
            task_id="task-001",
            title="feat: implement auth",
            body="Task: `task-001`",
            head="orch/task-001",
            base="main",
        )
    assert result == pr_url
    call_args = mock_run.call_args[0][0]
    assert call_args[0] == "gh"
    assert "pr" in call_args
    assert "create" in call_args
    assert "--head" in call_args
    assert "orch/task-001" in call_args
    assert "--base" in call_args
    assert "main" in call_args


def test_create_pr_returns_none_on_failure():
    with patch("subprocess.run", return_value=_mock_run(returncode=1)):
        provider = GitHubProvider()
        result = provider.create_pr("t", "title", "body", "head", "main")
    assert result is None


def test_get_ci_status_maps_success():
    payload = json.dumps([{"state": "SUCCESS", "conclusion": "success"}])
    with patch("subprocess.run", return_value=_mock_run(stdout=payload)):
        provider = GitHubProvider()
        status = provider.get_ci_status("https://github.com/org/repo/pull/42")
    assert status == "success"


def test_get_ci_status_maps_failure():
    payload = json.dumps([{"state": "FAILURE", "conclusion": "failure"}])
    with patch("subprocess.run", return_value=_mock_run(stdout=payload)):
        provider = GitHubProvider()
        status = provider.get_ci_status("https://github.com/org/repo/pull/42")
    assert status == "failure"


def test_get_ci_status_maps_pending_for_in_progress():
    payload = json.dumps([{"state": "IN_PROGRESS", "conclusion": None}])
    with patch("subprocess.run", return_value=_mock_run(stdout=payload)):
        provider = GitHubProvider()
        status = provider.get_ci_status("https://github.com/org/repo/pull/42")
    assert status == "pending"


def test_get_ci_status_returns_pending_on_empty_checks():
    with patch("subprocess.run", return_value=_mock_run(stdout="[]")):
        provider = GitHubProvider()
        assert provider.get_ci_status("https://github.com/org/repo/pull/42") == "pending"


def test_get_ci_status_returns_pending_on_subprocess_error():
    with patch("subprocess.run", return_value=_mock_run(returncode=1)):
        provider = GitHubProvider()
        assert provider.get_ci_status("https://github.com/org/repo/pull/42") == "pending"


def test_get_ci_logs_returns_truncated_output():
    long_log = "x" * 10_000
    with patch("subprocess.run", return_value=_mock_run(stdout=long_log)):
        provider = GitHubProvider()
        result = provider.get_ci_logs("https://github.com/org/repo/pull/42")
    assert len(result) <= 8000


def test_get_ci_logs_returns_empty_on_failure():
    with patch("subprocess.run", return_value=_mock_run(returncode=1)):
        provider = GitHubProvider()
        assert provider.get_ci_logs("https://github.com/org/repo/pull/42") == ""
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
pytest orchestrator/tests/test_vcs_github.py -v 2>&1 | tail -15
```
Expected: FAIL — `create_pr` raises `NotImplementedError`.

- [ ] **Step 3: Implement GitHubProvider**

Replace `orchestrator/vcs/github.py` with:

```python
# orchestrator/vcs/github.py
"""GitHubProvider — delegates to the `gh` CLI (must be authenticated)."""
from __future__ import annotations

import json
import logging
import subprocess

log = logging.getLogger(__name__)

_CI_LOG_MAX = 8_000


def _run_gh(*args: str) -> tuple[str, int]:
    """Run a gh CLI command. Returns (stdout, returncode)."""
    result = subprocess.run(
        ["gh", *args],
        capture_output=True,
        text=True,
    )
    return result.stdout.strip(), result.returncode


def _map_gh_state(checks: list[dict]) -> str:
    """Map gh check states to orch's pending/success/failure."""
    if not checks:
        return "pending"
    states = {c.get("state", "").upper() for c in checks}
    conclusions = {c.get("conclusion", "") for c in checks}
    if "FAILURE" in states or "failure" in conclusions or "cancelled" in conclusions:
        return "failure"
    if states - {"SUCCESS", "SKIPPED", "NEUTRAL"}:
        return "pending"
    return "success"


class GitHubProvider:
    def create_pr(
        self,
        task_id: str,
        title: str,
        body: str,
        head: str,
        base: str,
    ) -> str | None:
        stdout, code = _run_gh(
            "pr", "create",
            "--title", title,
            "--body", body,
            "--head", head,
            "--base", base,
        )
        if code != 0:
            log.warning("gh pr create failed for %s (exit %d)", task_id, code)
            return None
        return stdout or None

    def get_ci_status(self, pr_url: str) -> str:
        stdout, code = _run_gh("pr", "checks", pr_url, "--json", "state,conclusion")
        if code != 0:
            return "pending"
        try:
            checks = json.loads(stdout) if stdout else []
        except json.JSONDecodeError:
            return "pending"
        return _map_gh_state(checks)

    def get_ci_logs(self, pr_url: str) -> str:
        # Get the run ID associated with the PR's last check
        run_stdout, code = _run_gh("pr", "checks", pr_url, "--json", "databaseId")
        if code != 0:
            return ""
        try:
            checks = json.loads(run_stdout) if run_stdout else []
            run_id = next(
                (str(c["databaseId"]) for c in checks if c.get("databaseId")),
                None,
            )
        except (json.JSONDecodeError, KeyError, StopIteration):
            return ""
        if not run_id:
            return ""
        log_stdout, code = _run_gh("run", "view", run_id, "--log-failed")
        if code != 0:
            return ""
        return log_stdout[:_CI_LOG_MAX]
```

- [ ] **Step 4: Run tests and verify they pass**

```bash
pytest orchestrator/tests/test_vcs_github.py -v
```
Expected: 11 PASS (2 structural + 9 implementation tests).

- [ ] **Step 5: Full suite check**

```bash
pytest --tb=short -q 2>&1 | tail -5
```
Expected: 1145+ passed, 0 new failures.

- [ ] **Step 6: Commit**

```bash
git add orchestrator/vcs/github.py orchestrator/tests/test_vcs_github.py
git commit -m "feat(vcs): GitHubProvider — gh CLI integration (create_pr, get_ci_status, get_ci_logs)"
```

---

## Task 4: GitLabProvider Implementation

**Files:**
- Modify: `orchestrator/vcs/gitlab.py`
- Create: `orchestrator/tests/test_vcs_gitlab.py`

- [ ] **Step 1: Write the failing tests**

```python
# orchestrator/tests/test_vcs_gitlab.py
import os
import json
from unittest.mock import patch, MagicMock
from orchestrator.vcs.gitlab import GitLabProvider


def _mock_run(stdout="", returncode=0):
    m = MagicMock()
    m.stdout = stdout
    m.returncode = returncode
    m.stderr = ""
    return m


def test_create_mr_calls_glab_with_correct_args():
    mr_url = "https://gitlab.example.com/org/repo/-/merge_requests/7"
    with patch("subprocess.run", return_value=_mock_run(stdout=mr_url)) as mock_run:
        provider = GitLabProvider(host="gitlab.example.com")
        result = provider.create_pr(
            task_id="task-002",
            title="feat: implement auth",
            body="Task: `task-002`",
            head="orch/task-002",
            base="main",
        )
    assert result == mr_url
    call_args = mock_run.call_args[0][0]
    assert call_args[0] == "glab"
    assert "mr" in call_args
    assert "create" in call_args
    assert "--source-branch" in call_args
    assert "orch/task-002" in call_args
    assert "--target-branch" in call_args
    assert "main" in call_args


def test_create_mr_sets_gitlab_host_env():
    with patch("subprocess.run", return_value=_mock_run(stdout="https://x/-/mr/1")) as mock_run:
        provider = GitLabProvider(host="gitlab.mycompany.com")
        provider.create_pr("t", "title", "body", "head", "main")
    env = mock_run.call_args[1].get("env", {})
    assert env.get("GITLAB_HOST") == "gitlab.mycompany.com"


def test_create_mr_returns_none_on_failure():
    with patch("subprocess.run", return_value=_mock_run(returncode=1)):
        provider = GitLabProvider()
        result = provider.create_pr("t", "title", "body", "head", "main")
    assert result is None


def test_get_ci_status_maps_success():
    payload = json.dumps({"status": "success"})
    with patch("subprocess.run", return_value=_mock_run(stdout=payload)):
        provider = GitLabProvider()
        assert provider.get_ci_status("https://gitlab.com/org/repo/-/merge_requests/7") == "success"


def test_get_ci_status_maps_failure():
    payload = json.dumps({"status": "failed"})
    with patch("subprocess.run", return_value=_mock_run(stdout=payload)):
        provider = GitLabProvider()
        assert provider.get_ci_status("https://gitlab.com/org/repo/-/merge_requests/7") == "failure"


def test_get_ci_status_returns_pending_on_running():
    payload = json.dumps({"status": "running"})
    with patch("subprocess.run", return_value=_mock_run(stdout=payload)):
        provider = GitLabProvider()
        assert provider.get_ci_status("https://gitlab.com/org/repo/-/merge_requests/7") == "pending"


def test_get_ci_status_returns_pending_on_subprocess_error():
    with patch("subprocess.run", return_value=_mock_run(returncode=1)):
        provider = GitLabProvider()
        assert provider.get_ci_status("https://gitlab.com/org/repo/-/merge_requests/7") == "pending"


def test_get_ci_logs_returns_empty_on_failure():
    with patch("subprocess.run", return_value=_mock_run(returncode=1)):
        provider = GitLabProvider()
        assert provider.get_ci_logs("https://gitlab.com/org/repo/-/merge_requests/7") == ""
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
pytest orchestrator/tests/test_vcs_gitlab.py -v 2>&1 | tail -10
```
Expected: FAIL — `create_pr` raises `NotImplementedError`.

- [ ] **Step 3: Implement GitLabProvider**

Replace `orchestrator/vcs/gitlab.py` with:

```python
# orchestrator/vcs/gitlab.py
"""GitLabProvider — delegates to the `glab` CLI (must be authenticated).

Self-hosted GitLab instances are supported via the `host` constructor arg,
which sets the GITLAB_HOST environment variable on every subprocess call.
"""
from __future__ import annotations

import json
import logging
import os
import subprocess

log = logging.getLogger(__name__)

_CI_LOG_MAX = 8_000

_GL_SUCCESS = {"success", "passed"}
_GL_FAILURE = {"failed", "cancelled", "skipped"}  # skipped here = job explicitly skipped → treat as failure signal


def _run_glab(*args: str, host: str | None = None) -> tuple[str, int]:
    """Run a glab CLI command. Returns (stdout, returncode)."""
    env = os.environ.copy()
    if host:
        env["GITLAB_HOST"] = host
    result = subprocess.run(
        ["glab", *args],
        capture_output=True,
        text=True,
        env=env,
    )
    return result.stdout.strip(), result.returncode


def _map_gl_status(raw: str) -> str:
    if raw in _GL_SUCCESS:
        return "success"
    if raw in _GL_FAILURE:
        return "failure"
    return "pending"


class GitLabProvider:
    def __init__(self, host: str = "gitlab.com") -> None:
        self._host = host if host != "gitlab.com" else None

    def create_pr(
        self,
        task_id: str,
        title: str,
        body: str,
        head: str,
        base: str,
    ) -> str | None:
        stdout, code = _run_glab(
            "mr", "create",
            "--title", title,
            "--description", body,
            "--source-branch", head,
            "--target-branch", base,
            "--yes",  # skip interactive confirmation
            host=self._host,
        )
        if code != 0:
            log.warning("glab mr create failed for %s (exit %d)", task_id, code)
            return None
        # glab prints the MR URL as the last line of stdout
        return stdout.splitlines()[-1].strip() if stdout else None

    def get_ci_status(self, pr_url: str) -> str:
        # Extract MR IID from URL: .../merge_requests/7 → 7
        try:
            mr_iid = pr_url.rstrip("/").split("/")[-1]
        except (IndexError, AttributeError):
            return "pending"
        stdout, code = _run_glab("mr", "view", mr_iid, "--output", "json", host=self._host)
        if code != 0:
            return "pending"
        try:
            data = json.loads(stdout) if stdout else {}
            pipeline = data.get("head_pipeline") or {}
            status = pipeline.get("status", "")
        except (json.JSONDecodeError, KeyError):
            return "pending"
        return _map_gl_status(status)

    def get_ci_logs(self, pr_url: str) -> str:
        try:
            mr_iid = pr_url.rstrip("/").split("/")[-1]
        except (IndexError, AttributeError):
            return ""
        # Get pipeline ID from MR
        stdout, code = _run_glab("mr", "view", mr_iid, "--output", "json", host=self._host)
        if code != 0:
            return ""
        try:
            data = json.loads(stdout) if stdout else {}
            pipeline_id = str((data.get("head_pipeline") or {}).get("id", ""))
        except (json.JSONDecodeError, KeyError):
            return ""
        if not pipeline_id:
            return ""
        log_stdout, code = _run_glab("pipeline", "jobs", pipeline_id, host=self._host)
        if code != 0:
            return ""
        return log_stdout[:_CI_LOG_MAX]
```

- [ ] **Step 4: Run tests and verify they pass**

```bash
pytest orchestrator/tests/test_vcs_gitlab.py -v
```
Expected: 8 PASS.

- [ ] **Step 5: Full suite check**

```bash
pytest --tb=short -q 2>&1 | tail -5
```
Expected: 1145+ passed, 0 new failures.

- [ ] **Step 6: Commit**

```bash
git add orchestrator/vcs/gitlab.py orchestrator/tests/test_vcs_gitlab.py
git commit -m "feat(vcs): GitLabProvider — glab CLI integration with self-hosted support"
```

---

## Task 5: Config Defaults + config.yaml

**Files:**
- Modify: `orchestrator/config_loader.py`
- Modify: `orchestrator/config.yaml`
- Test: `orchestrator/tests/test_config_loader.py` (existing file — add 2 tests)

- [ ] **Step 1: Write the failing tests**

Open `orchestrator/tests/test_config_loader.py` and add at the end:

```python
def test_vcs_defaults_applied(tmp_path):
    cfg_path = tmp_path / "config.yaml"
    cfg_path.write_text("project_id: p1\n")
    cfg = load_config(cfg_path)
    vcs = cfg["vcs"]
    assert vcs["provider"] == "github"
    assert vcs["host"] == "github.com"
    assert vcs["auto_pr"] is False
    assert vcs["ci_max_retries"] == 1
    assert vcs["ci_poll_interval_s"] == 30


def test_vcs_config_override_merges_correctly(tmp_path):
    cfg_path = tmp_path / "config.yaml"
    cfg_path.write_text("project_id: p1\nvcs:\n  auto_pr: true\n  ci_max_retries: 3\n")
    cfg = load_config(cfg_path)
    assert cfg["vcs"]["auto_pr"] is True
    assert cfg["vcs"]["ci_max_retries"] == 3
    # Other defaults still present
    assert cfg["vcs"]["provider"] == "github"
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
pytest orchestrator/tests/test_config_loader.py::test_vcs_defaults_applied \
       orchestrator/tests/test_config_loader.py::test_vcs_config_override_merges_correctly -v
```
Expected: FAIL — no `vcs` key in defaults.

- [ ] **Step 3: Add `vcs` defaults to `_apply_defaults` in `config_loader.py`**

In `orchestrator/config_loader.py`, inside `_apply_defaults`, after the `presentation` block, add:

```python
cfg.setdefault("vcs", {})
cfg["vcs"].setdefault("provider", "github")
cfg["vcs"].setdefault("host", "github.com")
cfg["vcs"].setdefault("auto_pr", False)
cfg["vcs"].setdefault("ci_max_retries", 1)
cfg["vcs"].setdefault("ci_poll_interval_s", 30)
```

Also add the ci_* status label defaults inside the `status_labels` dict in `_apply_defaults`. The existing block is:

```python
cfg["presentation"].setdefault("status_labels", {
    "backlog":     "Planificado",
    "in_progress": "En progreso",
    "in-progress": "En progreso",
    "done":        "Entregado",
    "blocked":     "Bloqueado",
    "todo":        "Por hacer",
    "skipped":     "Omitido",
})
```

Replace it with:

```python
cfg["presentation"].setdefault("status_labels", {
    "backlog":      "Planificado",
    "in_progress":  "En progreso",
    "in-progress":  "En progreso",
    "done":         "Entregado",
    "blocked":      "Bloqueado",
    "todo":         "Por hacer",
    "skipped":      "Omitido",
    "ci_pending":   "Validando...",
    "ci_success":   "Tests aprobados",
    "ci_failure":   "Corrección en progreso",
})
```

- [ ] **Step 4: Add `vcs:` section to `orchestrator/config.yaml`**

Open `orchestrator/config.yaml`. After the `dispatch:` section, add:

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

- [ ] **Step 5: Run the new tests**

```bash
pytest orchestrator/tests/test_config_loader.py -v 2>&1 | tail -10
```
Expected: All config_loader tests PASS (14 original + 2 new = 16).

- [ ] **Step 6: Full suite check**

```bash
pytest --tb=short -q 2>&1 | tail -5
```
Expected: 1145+ passed, 0 new failures.

- [ ] **Step 7: Commit**

```bash
git add orchestrator/config_loader.py orchestrator/config.yaml \
        orchestrator/tests/test_config_loader.py
git commit -m "feat(config): vcs defaults (provider, auto_pr, ci_max_retries, ci_poll_interval_s)"
```

---

## Task 6: WorktreeManager.recreate() + _spawn_one Reuse

**Files:**
- Modify: `orchestrator/worktree.py`
- Modify: `orchestrator/orch.py` (lines ~1446-1449, the worktree create block in `_spawn_one`)
- Test: `orchestrator/tests/test_worktree.py` (existing file — add `test_recreate_*` tests)

- [ ] **Step 1: Write the failing tests for `recreate()`**

Open `orchestrator/tests/test_worktree.py` and add:

```python
def test_recreate_fetches_and_checks_out_existing_branch(tmp_path, monkeypatch):
    """recreate() calls git fetch + git worktree add (not -b, reuses existing branch)."""
    calls = []

    def fake_run(args, capture_output, text, cwd):
        calls.append(args)
        m = MagicMock()
        m.returncode = 0
        m.stdout = ""
        m.stderr = ""
        return m

    monkeypatch.setattr("subprocess.run", fake_run)
    wm = WorktreeManager(tmp_path)
    result = wm.recreate("task-ci")

    fetch_call = next((c for c in calls if "fetch" in c), None)
    add_call = next((c for c in calls if "worktree" in c and "add" in c), None)

    assert fetch_call is not None, "expected git fetch call"
    assert "orch/task-ci" in fetch_call

    assert add_call is not None, "expected git worktree add call"
    # Must NOT have -b flag (checking out existing branch, not creating new)
    assert "-b" not in add_call
    assert "orch/task-ci" in add_call

    assert wm.exists("task-ci")
    assert result == wm.worktree_path("task-ci")


def test_recreate_removes_stale_worktree_first(tmp_path, monkeypatch):
    """recreate() removes any existing worktree dir before adding."""
    calls = []

    def fake_run(args, capture_output, text, cwd):
        calls.append(args)
        m = MagicMock()
        m.returncode = 0
        m.stdout = ""
        m.stderr = ""
        return m

    monkeypatch.setattr("subprocess.run", fake_run)
    wm = WorktreeManager(tmp_path)
    # Simulate existing worktree
    wt = wm.worktree_path("task-ci")
    wt.mkdir(parents=True)

    wm.recreate("task-ci")

    remove_call = next((c for c in calls if "worktree" in c and "remove" in c), None)
    assert remove_call is not None, "expected git worktree remove for stale dir"
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
pytest orchestrator/tests/test_worktree.py::test_recreate_fetches_and_checks_out_existing_branch \
       orchestrator/tests/test_worktree.py::test_recreate_removes_stale_worktree_first -v
```
Expected: FAIL — `WorktreeManager` has no `recreate` method.

- [ ] **Step 3: Add `recreate()` to `WorktreeManager`**

In `orchestrator/worktree.py`, after the `remove_all()` method, add:

```python
def recreate(self, task_id: str) -> Path:
    """Check out existing remote branch ``orch/<task_id>`` into a fresh worktree.

    Used by CI re-dispatch: preserves the commit history on the branch
    so the agent can amend and push CI fixes. Unlike ``create()``, this
    does NOT create a new branch — it checks out the existing remote one.

    Raises:
        WorktreeError: if ``git fetch`` or ``git worktree add`` fails.
    """
    wt_path = self.worktree_path(task_id)
    if wt_path.exists():
        self.remove(task_id)
    (self._root / ".worktrees").mkdir(parents=True, exist_ok=True)
    self._run(
        ["git", "fetch", "origin", self.branch_name(task_id)],
        task_id,
    )
    self._run(
        ["git", "worktree", "add", str(wt_path), self.branch_name(task_id)],
        task_id,
    )
    self._active[task_id] = wt_path
    return wt_path
```

- [ ] **Step 4: Modify `_spawn_one` in `orch.py` to reuse existing worktree**

Find the worktree creation block in `orch.py` at approximately line 1446:

```python
    effective_cwd = cwd
    if wm is not None:
        try:
            effective_cwd = wm.create(task.id, base_branch)
```

Replace it with:

```python
    effective_cwd = cwd
    if wm is not None:
        try:
            # CI re-dispatch: recreate() sets up the worktree and registers it
            # in wm._active before the task is re-queued. If it already exists,
            # reuse it instead of creating a fresh branch from base_branch.
            if wm.exists(task.id):
                effective_cwd = wm.worktree_path(task.id)
            else:
                effective_cwd = wm.create(task.id, base_branch)
```

- [ ] **Step 5: Run tests and verify they pass**

```bash
pytest orchestrator/tests/test_worktree.py -v 2>&1 | tail -15
```
Expected: All worktree tests PASS (including 2 new).

- [ ] **Step 6: Full suite check**

```bash
pytest --tb=short -q 2>&1 | tail -5
```
Expected: 1145+ passed, 0 new failures.

- [ ] **Step 7: Commit**

```bash
git add orchestrator/worktree.py orchestrator/orch.py orchestrator/tests/test_worktree.py
git commit -m "feat(worktree): recreate() — checkout existing remote branch for CI re-dispatch"
```

---

## Task 7: PR Creation in `_reap_once`

**Files:**
- Modify: `orchestrator/orch.py`
- Test: `orchestrator/tests/test_ci_dispatch.py` (add integration test)

This task adds `_create_pr_after_push()` and wires it into the existing `_reap_once` success path, immediately after `wm.push()` succeeds.

- [ ] **Step 1: Write the failing test**

Add to `orchestrator/tests/test_ci_dispatch.py`:

```python
from unittest.mock import MagicMock, patch


def test_create_pr_after_push_stores_pr_url(tmp_path):
    """_create_pr_after_push stores pr_url when auto_pr=True."""
    from orchestrator.orch import _create_pr_after_push

    db_path = tmp_path / "orch.db"
    b = SqliteBackend(project_id="p1", db_path=db_path, project_root=tmp_path)
    b.bootstrap([])
    import sqlite3
    conn = sqlite3.connect(str(db_path))
    conn.execute("INSERT OR IGNORE INTO tasks_runtime (project_id, task_id, status, updated_at) VALUES (?, ?, 'done', '2026-01-01T00:00:00Z')", ("p1", "task-001"))
    conn.commit()
    conn.close()

    mock_vcs = MagicMock()
    mock_vcs.create_pr.return_value = "https://github.com/org/repo/pull/1"

    cfg = {
        "vcs": {"auto_pr": True, "provider": "github"},
        "dispatch": {"base_branch": "main"},
    }

    class FakeTask:
        id = "task-001"
        title = "feat: implement auth"
        spec_ref = None
        reason = None

    _create_pr_after_push(
        task=FakeTask(),
        cfg=cfg,
        backend=b,
        vcs_provider=mock_vcs,
    )

    rows = b.get_tasks_with_pending_ci()
    assert len(rows) == 1
    assert rows[0]["pr_url"] == "https://github.com/org/repo/pull/1"
    assert rows[0]["ci_status"] == "pending"


def test_create_pr_after_push_skips_when_auto_pr_false(tmp_path):
    """_create_pr_after_push does nothing when auto_pr=False."""
    from orchestrator.orch import _create_pr_after_push

    mock_vcs = MagicMock()
    cfg = {"vcs": {"auto_pr": False}, "dispatch": {"base_branch": "main"}}

    class FakeTask:
        id = "task-noop"
        title = "test"
        spec_ref = None
        reason = None

    _create_pr_after_push(task=FakeTask(), cfg=cfg, backend=None, vcs_provider=mock_vcs)
    mock_vcs.create_pr.assert_not_called()
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
pytest orchestrator/tests/test_ci_dispatch.py::test_create_pr_after_push_stores_pr_url \
       orchestrator/tests/test_ci_dispatch.py::test_create_pr_after_push_skips_when_auto_pr_false -v
```
Expected: FAIL — `_create_pr_after_push` not found.

- [ ] **Step 3: Add `_create_pr_after_push()` to `orch.py`**

In `orchestrator/orch.py`, after the `_reap_once` function definition but before the next function, add (approximately near line 1240):

```python
def _create_pr_after_push(
    task: "Task",
    cfg: dict,
    backend: "SqliteBackend | None",
    vcs_provider: "VcsProvider | None",
) -> None:
    """Create a PR after a successful worktree push.

    Guards: only runs when vcs.auto_pr=True AND both backend and vcs_provider
    are provided. Silent failure on PR creation error (task still proceeds).
    """
    from orchestrator.state.sqlite_backend import SqliteBackend as _Sb

    vcs_cfg = cfg.get("vcs", {})
    if not vcs_cfg.get("auto_pr", False):
        return
    if backend is None or vcs_provider is None or not isinstance(backend, _Sb):
        return

    base = cfg.get("dispatch", {}).get("base_branch", "main")
    body_parts = [f"Task: `{task.id}`"]
    if getattr(task, "spec_ref", None):
        body_parts.append(f"Spec: {task.spec_ref}")
    if getattr(task, "reason", None):
        body_parts.append(f"\n{task.reason}")
    body = "\n".join(body_parts).strip()

    try:
        pr_url = vcs_provider.create_pr(
            task_id=task.id,
            title=task.title,
            body=body,
            head=f"orch/{task.id}",
            base=base,
        )
    except Exception as exc:  # noqa: BLE001
        log.warning("PR creation raised for %s: %s — continuing as done", task.id, exc)
        return

    if pr_url:
        try:
            backend.set_task_pr(task.id, pr_url)
        except Exception as exc:  # noqa: BLE001
            log.warning("set_task_pr failed for %s: %s", task.id, exc)
    else:
        log.info("PR creation returned no URL for %s — proceeding normally", task.id)
```

- [ ] **Step 4: Wire `_create_pr_after_push` into `_reap_once`**

In `_reap_once`, the worktree push block (approximately line 986-995) currently reads:

```python
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

Replace with:

```python
        if entry.worktree_path is not None and wm is not None:
            if result.success:
                push_ok = False
                try:
                    wm.push(entry.task.id)
                    push_ok = True
                except Exception as exc:  # noqa: BLE001
                    log.warning(
                        "worktree push failed for %s (best-effort, not failing task): %s",
                        entry.task.id, exc,
                    )
                if push_ok:
                    _create_pr_after_push(
                        task=entry.task,
                        cfg=cfg,
                        backend=getattr(wm, "_backend", None),
                        vcs_provider=getattr(wm, "_vcs_provider", None),
                    )
            wm.remove(entry.task.id)
```

**Note:** `_backend` and `_vcs_provider` are attached to `wm` in the main loop (Task 8, Step 4). This pattern avoids threading the two objects through `_reap_once`'s already-wide signature.

- [ ] **Step 5: Run the new tests**

```bash
pytest orchestrator/tests/test_ci_dispatch.py -v 2>&1 | tail -15
```
Expected: All 7 tests PASS.

- [ ] **Step 6: Full suite check**

```bash
pytest --tb=short -q 2>&1 | tail -5
```

- [ ] **Step 7: Commit**

```bash
git add orchestrator/orch.py orchestrator/tests/test_ci_dispatch.py
git commit -m "feat(dispatch): _create_pr_after_push — PR creation after successful worktree push"
```

---

## Task 8: `_check_ci_once()` + Main Loop Wiring

**Files:**
- Modify: `orchestrator/orch.py`
- Test: `orchestrator/tests/test_ci_dispatch.py` (add CI polling tests)

- [ ] **Step 1: Write the failing tests for `_check_ci_once`**

Add to `orchestrator/tests/test_ci_dispatch.py`:

```python
import time


def _make_backend_with_pending(tmp_path, task_id="task-ci"):
    db_path = tmp_path / "orch.db"
    b = SqliteBackend(project_id="p1", db_path=db_path, project_root=tmp_path)
    b.bootstrap([])
    import sqlite3
    conn = sqlite3.connect(str(db_path))
    conn.execute(
        "INSERT OR IGNORE INTO tasks_runtime (project_id, task_id, status, updated_at) "
        "VALUES (?, ?, 'done', '2026-01-01T00:00:00Z')",
        ("p1", task_id),
    )
    conn.commit()
    conn.close()
    b.set_task_pr(task_id, "https://github.com/org/repo/pull/1")
    return b


def test_check_ci_once_marks_success(tmp_path):
    from orchestrator.orch import _check_ci_once

    b = _make_backend_with_pending(tmp_path)
    mock_vcs = MagicMock()
    mock_vcs.get_ci_status.return_value = "success"

    cfg = {"vcs": {"ci_poll_interval_s": 0, "ci_max_retries": 1}}
    mock_queue = MagicMock()

    _check_ci_once(cfg=cfg, backend=b, vcs_provider=mock_vcs,
                   queue=mock_queue, wm=None, last_ci_check=0)

    assert b.get_tasks_with_pending_ci() == []
    rows = b.get_tasks_with_pending_ci()
    assert rows == []


def test_check_ci_once_blocks_on_cap_exceeded(tmp_path):
    from orchestrator.orch import _check_ci_once

    b = _make_backend_with_pending(tmp_path)
    b.increment_ci_attempts("task-ci")  # already at 1 attempt
    mock_vcs = MagicMock()
    mock_vcs.get_ci_status.return_value = "failure"

    cfg = {"vcs": {"ci_poll_interval_s": 0, "ci_max_retries": 1}}
    mock_queue = MagicMock()

    _check_ci_once(cfg=cfg, backend=b, vcs_provider=mock_vcs,
                   queue=mock_queue, wm=None, last_ci_check=0)

    # ci_status should be "failure" and task should not be pending anymore
    assert b.get_tasks_with_pending_ci() == []


def test_check_ci_once_throttled_by_interval(tmp_path):
    from orchestrator.orch import _check_ci_once

    b = _make_backend_with_pending(tmp_path)
    mock_vcs = MagicMock()
    mock_vcs.get_ci_status.return_value = "success"

    cfg = {"vcs": {"ci_poll_interval_s": 9999, "ci_max_retries": 1}}
    mock_queue = MagicMock()

    # last_ci_check = now — should be throttled
    _check_ci_once(cfg=cfg, backend=b, vcs_provider=mock_vcs,
                   queue=mock_queue, wm=None, last_ci_check=time.monotonic())

    mock_vcs.get_ci_status.assert_not_called()
    # Task still pending
    assert len(b.get_tasks_with_pending_ci()) == 1
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
pytest orchestrator/tests/test_ci_dispatch.py::test_check_ci_once_marks_success \
       orchestrator/tests/test_ci_dispatch.py::test_check_ci_once_blocks_on_cap_exceeded \
       orchestrator/tests/test_ci_dispatch.py::test_check_ci_once_throttled_by_interval -v
```
Expected: FAIL — `_check_ci_once` not found.

- [ ] **Step 3: Add `_check_ci_once()` and `_redispatch_with_ci_feedback()` to `orch.py`**

Add near the `_create_pr_after_push` function (same area in the file):

```python
def _redispatch_with_ci_feedback(
    task_row: dict,
    ci_logs: str,
    backend: "SqliteBackend",
    wm: "WorktreeManager",
    queue: "TaskQueue",
) -> None:
    """Recreate the worktree from the existing branch, write CI feedback, re-queue.

    The task's tasks_runtime.status is reset to 'todo' and queue._status is
    patched directly (same pattern as the retry loop in _reap_once) so the
    next _refill tick picks it up.  _spawn_one detects the existing worktree
    via wm.exists() and reuses it (Task 6).
    """
    task_id = task_row["task_id"]
    try:
        wt_path = wm.recreate(task_id)
    except Exception as exc:  # noqa: BLE001
        log.warning("wm.recreate failed for %s during CI re-dispatch: %s", task_id, exc)
        return
    # Write the CI failure log as context for the agent
    try:
        feedback_file = wt_path / ".orch-ci-feedback.md"
        feedback_file.write_text(
            f"# CI Failure — Please fix\n\n```\n{ci_logs}\n```\n",
            encoding="utf-8",
        )
    except OSError as exc:
        log.warning("could not write CI feedback file for %s: %s", task_id, exc)
    # Reset tasks_runtime status to todo so the task shows as re-dispatchable
    try:
        backend.set_task_status(
            task_id, "todo", "orch-ci",
            "CI failure — re-queuing for fix",
            _utc_now_iso(),
        )
    except Exception as exc:  # noqa: BLE001
        log.warning("set_task_status(todo) failed for %s: %s", task_id, exc)
    # Patch the in-memory queue so _refill picks it up (same pattern as _reap_once retry)
    queue._status[task_id] = "todo"  # noqa: SLF001


def _check_ci_once(
    cfg: dict,
    backend: "SqliteBackend",
    vcs_provider: "VcsProvider",
    queue: "TaskQueue",
    wm: "WorktreeManager | None",
    last_ci_check: float,
) -> float:
    """Poll tasks with pending CI status. Returns updated last_ci_check timestamp.

    Throttled by cfg['vcs']['ci_poll_interval_s']. Called on every main loop tick.

    Success path: updates ci_status only — the task was already marked done in
    the queue when the agent finished. No queue mutation needed.

    Failure+cap path: sets ci_status='failure' only. Cannot transition
    tasks_runtime.status to 'blocked' from 'done' — the _STATUS_TRANSITIONS
    table only allows done→todo and done→done. The dashboard reads ci_status
    to show the failed state independently of the queue status.
    """
    import time as _time

    now = _time.monotonic()
    interval = float(cfg.get("vcs", {}).get("ci_poll_interval_s", 30))
    if now - last_ci_check < interval:
        return last_ci_check

    from orchestrator.state.sqlite_backend import SqliteBackend as _Sb

    if not isinstance(backend, _Sb) or vcs_provider is None:
        return now

    pending = backend.get_tasks_with_pending_ci()
    max_retries = int(cfg.get("vcs", {}).get("ci_max_retries", 1))

    for task in pending:
        task_id = task["task_id"]
        try:
            status = vcs_provider.get_ci_status(task["pr_url"])
        except Exception as exc:  # noqa: BLE001
            log.warning("get_ci_status failed for %s: %s", task_id, exc)
            continue

        if status == "success":
            # Task is already done in the queue (marked done when the agent
            # finished). Just record the CI result for the dashboard.
            backend.set_task_ci_status(task_id, "success")
            log.info("CI passed for %s", task_id)

        elif status == "failure":
            if task["ci_attempts"] < max_retries and wm is not None:
                try:
                    ci_logs = vcs_provider.get_ci_logs(task["pr_url"])
                except Exception as exc:  # noqa: BLE001
                    log.warning("get_ci_logs failed for %s: %s", task_id, exc)
                    ci_logs = ""
                _redispatch_with_ci_feedback(task, ci_logs, backend, wm, queue)
                backend.increment_ci_attempts(task_id)
                backend.set_task_ci_status(task_id, "pending")
                log.info("CI failed for %s — re-dispatching (attempt %d)", task_id, task["ci_attempts"] + 1)
            else:
                # Cap exceeded: record failure in ci_status column. We do NOT
                # call set_task_status('blocked') because done→blocked is an
                # illegal transition; the dashboard uses ci_status='failure' to
                # show this state.
                backend.set_task_ci_status(task_id, "failure")
                log.warning("CI failed for %s — ci_max_retries=%d reached", task_id, max_retries)
        # status == "pending" → do nothing, check next tick

    return now
```

- [ ] **Step 4: Wire `_check_ci_once` into the main dispatch loop AND attach backend+vcs_provider to `wm`**

In the main dispatch loop setup (approximately line 4235 where worktree mode is enabled), find:

```python
        if _worktree_mode:
            from orchestrator.worktree import WorktreeManager
            wm: "WorktreeManager | None" = WorktreeManager(paths.project_root)
            log.info("worktree mode enabled; base_branch=%s", _base_branch)
        else:
            wm = None
```

Replace with:

```python
        _vcs_cfg = cfg.get("vcs", {})
        _auto_pr = bool(_vcs_cfg.get("auto_pr", False))
        if _worktree_mode:
            from orchestrator.worktree import WorktreeManager
            wm: "WorktreeManager | None" = WorktreeManager(paths.project_root)
            log.info("worktree mode enabled; base_branch=%s", _base_branch)
        else:
            wm = None
            _auto_pr = False  # auto_pr has no effect without worktree mode

        # Attach backend + vcs_provider to wm so _reap_once can access them
        # without threading them through the already-wide signature.
        _vcs_provider = None
        if _worktree_mode and _auto_pr:
            from orchestrator.vcs import get_vcs_provider
            _vcs_provider = get_vcs_provider(cfg)
            log.info("VCS auto-PR enabled (provider=%s)", _vcs_cfg.get("provider", "github"))
        if wm is not None:
            wm._backend = state_backend  # noqa: SLF001
            wm._vcs_provider = _vcs_provider  # noqa: SLF001
```

Then, inside the main `while True:` loop (approximately line 4256), after `_timeout_sweep(...)`, add the CI poll call:

```python
            _last_ci_check = _check_ci_once(
                cfg=cfg,
                backend=state_backend,
                vcs_provider=_vcs_provider,
                queue=queue,
                wm=wm,
                last_ci_check=_last_ci_check,
            )
```

And before the `while True:` loop, initialize `_last_ci_check`:

```python
        _last_ci_check: float = 0.0
```

Note: `_check_ci_once` is a no-op when `_vcs_provider is None` (guard inside).

- [ ] **Step 5: Run the new tests**

```bash
pytest orchestrator/tests/test_ci_dispatch.py -v
```
Expected: All 10 tests PASS.

- [ ] **Step 6: Full suite check**

```bash
pytest --tb=short -q 2>&1 | tail -5
```

- [ ] **Step 7: Commit**

```bash
git add orchestrator/orch.py orchestrator/tests/test_ci_dispatch.py
git commit -m "feat(dispatch): _check_ci_once + _redispatch_with_ci_feedback — CI polling loop"
```

---

## Task 9: Frontend — Task Type + TaskCard PR Badge + CI Indicator

**Files:**
- Modify: `frontend/src/lib/types.ts`
- Modify: `frontend/src/components/TaskCard.tsx`
- Test: TypeScript compile check only (no new unit tests — visual component)

- [ ] **Step 1: Add `pr_url`, `ci_status`, `ci_attempts` to the `Task` interface**

Open `frontend/src/lib/types.ts`. Find the `Task` interface (approximately line 61) and add the new optional fields at the end of the interface body:

```typescript
export interface Task {
  id: string
  phase: number
  title: string
  description: string
  model: string
  reason: string
  status: string
  dependencies: string[]
  dep_count: number
  estimate_hours: number
  files: string[]
  spec_ref: string
  comments: unknown[]
  human_hours: number
  last_updated: string
  downstream_impact: number
  on_critical_path: boolean
  parallelizable?: boolean
  // F-4: VCS / CI tracking
  pr_url?: string | null
  ci_status?: "pending" | "success" | "failure" | "skipped" | null
  ci_attempts?: number
}
```

- [ ] **Step 2: Run TypeScript check**

```bash
cd /Volumes/PortableSSD/orch/frontend
pnpm tsc -b --noEmit 2>&1 | head -20
```
Expected: 0 errors.

- [ ] **Step 3: Add PR badge + CI indicator to TaskCard**

Open `frontend/src/components/TaskCard.tsx`. The current file imports `{ GitBranch, Lock, Timer, Zap }` from lucide-react.

Replace the entire file content with:

```tsx
import { CheckCircle2, ExternalLink, GitBranch, Loader2, Lock, Timer, XCircle, Zap } from "lucide-react"
import { cn } from "@/lib/utils"
import type { Task } from "@/lib/types"

export interface TaskCardProps {
  task: Task
  taskStatusMap?: Record<string, string>
  onClick?: (taskId: string) => void
}

const STATUS_ACCENT: Record<string, string> = {
  backlog:       "border-l-zinc-400",
  todo:          "border-l-sky-400",
  "in-progress": "border-l-violet-500",
  blocked:       "border-l-rose-500",
  done:          "border-l-emerald-500",
}

function CIBadge({ status }: { status: string }) {
  if (status === "success") {
    return (
      <span className="inline-flex items-center gap-0.5 text-emerald-600" title="CI passed">
        <CheckCircle2 className="h-3 w-3" />
        <span className="text-[10px]">CI ✓</span>
      </span>
    )
  }
  if (status === "failure") {
    return (
      <span className="inline-flex items-center gap-0.5 text-rose-500" title="CI failed">
        <XCircle className="h-3 w-3" />
        <span className="text-[10px]">CI ✗</span>
      </span>
    )
  }
  if (status === "pending") {
    return (
      <span className="inline-flex items-center gap-0.5 text-amber-500" title="CI running">
        <Loader2 className="h-3 w-3 animate-spin" />
        <span className="text-[10px]">CI…</span>
      </span>
    )
  }
  return null
}

export function TaskCard({ task, taskStatusMap, onClick }: TaskCardProps) {
  const blockingDeps =
    taskStatusMap && task.status !== "done"
      ? task.dependencies.filter((id) => {
          const s = taskStatusMap[id]
          return s !== undefined && s !== "done"
        })
      : []

  const accentClass = STATUS_ACCENT[task.status] ?? "border-l-zinc-400"

  return (
    <div
      role={onClick ? "button" : undefined}
      tabIndex={onClick ? 0 : undefined}
      onClick={onClick ? () => onClick(task.id) : undefined}
      onKeyDown={
        onClick
          ? (e) => {
              if (e.key === "Enter" || e.key === " ") {
                e.preventDefault()
                onClick(task.id)
              }
            }
          : undefined
      }
      className={cn(
        "group flex flex-col gap-2 rounded-md border border-l-4 border-zinc-200 bg-white p-3",
        "transition-all hover:border-zinc-300 hover:shadow-md",
        onClick
          ? "cursor-pointer focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          : "cursor-default",
        accentClass,
      )}
    >
      {/* ID row */}
      <div className="flex items-center justify-between gap-2">
        <span className="font-mono text-[10px] leading-none text-zinc-400">
          {task.id}
        </span>
        <div className="flex items-center gap-1">
          {task.parallelizable && (
            <GitBranch className="h-3 w-3 text-blue-400" aria-label="Parallelizable" />
          )}
          {task.on_critical_path && (
            <Zap className="h-3 w-3 text-red-400" aria-label="Critical path" />
          )}
        </div>
      </div>

      {/* Title */}
      <p className="line-clamp-3 text-sm font-medium leading-snug text-zinc-800">
        {task.title}
      </p>

      {/* PR badge + CI indicator (F-4) */}
      {(task.pr_url || task.ci_status) && (
        <div className="flex items-center gap-2">
          {task.pr_url && (
            <a
              href={task.pr_url}
              target="_blank"
              rel="noopener noreferrer"
              onClick={(e) => e.stopPropagation()}
              className="inline-flex items-center gap-0.5 text-[10px] text-sky-500 hover:underline"
            >
              <ExternalLink className="h-3 w-3" />
              PR
            </a>
          )}
          {task.ci_status && <CIBadge status={task.ci_status} />}
        </div>
      )}

      {/* Footer */}
      <div className="flex items-center justify-between gap-2 text-xs text-zinc-400">
        <div className="flex items-center gap-2">
          {task.estimate_hours != null && (
            <span className="inline-flex items-center gap-0.5">
              <Timer className="h-3 w-3" />
              {task.estimate_hours}h
            </span>
          )}
          {blockingDeps.length > 0 ? (
            <span className="inline-flex items-center gap-0.5 font-medium text-amber-500">
              <Lock className="h-3 w-3" />
              {blockingDeps.length} blocking
            </span>
          ) : task.dep_count > 0 ? (
            <span className="inline-flex items-center gap-0.5">
              <GitBranch className="h-3 w-3" />
              {task.dep_count}
            </span>
          ) : null}
        </div>
        {task.model && (
          <span className="max-w-[90px] truncate font-mono text-[10px] text-zinc-300">
            {task.model.includes("/") ? task.model.split("/").pop() : task.model}
          </span>
        )}
      </div>
    </div>
  )
}
```

- [ ] **Step 4: Run TypeScript compile check**

```bash
cd /Volumes/PortableSSD/orch/frontend
pnpm tsc -b --noEmit 2>&1 | head -20
```
Expected: 0 errors.

- [ ] **Step 5: Run the full Python suite (no TS test suite on this project)**

```bash
cd /Volumes/PortableSSD/orch
pytest --tb=short -q 2>&1 | tail -5
```
Expected: 1145+ passed, 0 new failures.

- [ ] **Step 6: Commit**

```bash
git add frontend/src/lib/types.ts frontend/src/components/TaskCard.tsx
git commit -m "feat(dashboard): Task type + TaskCard PR badge + CI indicator (F-4)"
```

---

## Task 10: Final Integration Test — auto_pr: false Regression

**Files:**
- Modify: `orchestrator/tests/test_ci_dispatch.py`

This task adds the regression test that confirms existing projects with `auto_pr: false` are completely unaffected.

- [ ] **Step 1: Write the regression test**

Add to `orchestrator/tests/test_ci_dispatch.py`:

```python
def test_auto_pr_false_task_completes_normally(tmp_path):
    """When auto_pr=False, _create_pr_after_push is a no-op and get_tasks_with_pending_ci stays empty."""
    from orchestrator.orch import _create_pr_after_push

    db_path = tmp_path / "orch.db"
    b = SqliteBackend(project_id="p1", db_path=db_path, project_root=tmp_path)
    b.bootstrap([])
    import sqlite3
    conn = sqlite3.connect(str(db_path))
    conn.execute(
        "INSERT OR IGNORE INTO tasks_runtime (project_id, task_id, status, updated_at) "
        "VALUES (?, ?, 'done', '2026-01-01T00:00:00Z')",
        ("p1", "task-noreg"),
    )
    conn.commit()
    conn.close()

    mock_vcs = MagicMock()
    cfg = {"vcs": {"auto_pr": False}, "dispatch": {"base_branch": "main"}}

    class FakeTask:
        id = "task-noreg"
        title = "some task"
        spec_ref = None
        reason = None

    _create_pr_after_push(task=FakeTask(), cfg=cfg, backend=b, vcs_provider=mock_vcs)

    mock_vcs.create_pr.assert_not_called()
    assert b.get_tasks_with_pending_ci() == []
```

- [ ] **Step 2: Run the test**

```bash
pytest orchestrator/tests/test_ci_dispatch.py::test_auto_pr_false_task_completes_normally -v
```
Expected: PASS.

- [ ] **Step 3: Run the full test suite**

```bash
pytest --tb=short -q 2>&1 | tail -5
```
Expected: 1145+ passed. Count the exact number and note it in the commit.

- [ ] **Step 4: Commit**

```bash
git add orchestrator/tests/test_ci_dispatch.py
git commit -m "test(ci): regression — auto_pr=false leaves no pending CI rows"
```

---

## Self-Review Checklist

After all 10 tasks, run this final verification:

```bash
# 1. Full test suite
pytest --tb=short -q 2>&1 | tail -10

# 2. TypeScript compile (no errors)
cd /Volumes/PortableSSD/orch/frontend && pnpm tsc -b --noEmit

# 3. Confirm migration 005 is discovered by the backend
python3 -c "
from orchestrator.state.sqlite_backend import _read_migrations
m = _read_migrations()
print([name for _, name, _ in m])
"
# Expected: [..., '005_pr_ci_tracking.sql'] in the list

# 4. Confirm get_vcs_provider factory works
python3 -c "
from orchestrator.vcs import get_vcs_provider
p = get_vcs_provider({'vcs': {'provider': 'github'}})
print(type(p).__name__)  # GitHubProvider
pg = get_vcs_provider({'vcs': {'provider': 'gitlab', 'host': 'gl.example.com'}})
print(type(pg).__name__)  # GitLabProvider
"
```

---

## Spec Coverage Map

| Spec section | Task(s) |
|---|---|
| Migration 005 — 3 columns | Task 1 |
| SqliteBackend — 4 methods | Task 1 |
| VcsProvider Protocol | Task 2 |
| GitHubProvider | Tasks 2 + 3 |
| GitLabProvider (incl. self-hosted) | Tasks 2 + 4 |
| get_vcs_provider factory | Task 2 |
| vcs: config section + defaults | Task 5 |
| wm.recreate() | Task 6 |
| _spawn_one reuses existing worktree | Task 6 |
| _create_pr_after_push in _reap_once | Task 7 |
| _check_ci_once — success path | Task 8 |
| _check_ci_once — failure+retry path | Task 8 |
| _check_ci_once — failure+cap path | Task 8 |
| _redispatch_with_ci_feedback | Task 8 |
| Main loop wiring (vcs_provider, _last_ci_check) | Task 8 |
| Dashboard — Task type fields | Task 9 |
| Dashboard — PR badge | Task 9 |
| Dashboard — CI indicator | Task 9 |
| ci_* status labels in defaults | Task 5 |
| auto_pr: false regression | Task 10 |
