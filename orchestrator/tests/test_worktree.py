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
