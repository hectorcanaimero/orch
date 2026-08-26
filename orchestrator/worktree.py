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
        if wt_path.exists():  # remove() swallows errors — verify cleanup succeeded
            raise WorktreeError(task_id, [], f"stale worktree at {wt_path} could not be removed")
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
