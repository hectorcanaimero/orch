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
