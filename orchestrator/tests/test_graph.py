"""Tests for the `orch graph` renderer (Sprint C — commit 6).

We assert:
    - `build_html` returns a valid, self-contained HTML string.
    - The number of `<g class="task">` elements matches the number of
      task rows in the input snapshot.
    - Phase columns lay out left → right (x increases monotonically with
      phase index).
    - Status colors are picked from `STATUS_COLORS`.
    - The subcommand writes the file and exits 0.
"""

from __future__ import annotations

from pathlib import Path

import pytest

from orchestrator.graph import STATUS_COLORS, _layout, build_html
from orchestrator.orch import _run_graph_subcommand


def _snapshot(tasks: list[dict]) -> dict:
    return {
        "project": {"project_id": "gtest", "backend": "file"},
        "totals": {"_total": len(tasks)},
        "tasks": tasks,
    }


def _task(
    tid: str, phase: int, status: str = "todo",
    deps: list[str] | None = None,
    backend: str = "opencode", model: str = "glm-5.1",
) -> dict:
    return {
        "id": tid, "phase": phase, "status": status, "backend": backend,
        "cli_model": model, "dependencies": deps or [],
        "cost_usd": 0.0, "last_event_human": None, "last_event": None,
    }


def test_layout_places_phase_columns_left_to_right() -> None:
    tasks = [
        _task("T-A", 1), _task("T-B", 1),
        _task("T-C", 2, deps=["T-A"]),
        _task("T-D", 3, deps=["T-C"]),
    ]
    positions, _canvas_w, _canvas_h = _layout(tasks)
    x_phase1 = positions["T-A"][0]
    x_phase2 = positions["T-C"][0]
    x_phase3 = positions["T-D"][0]
    assert x_phase1 < x_phase2 < x_phase3


def test_layout_stacks_same_phase_ids_sorted() -> None:
    tasks = [_task("T-B", 1), _task("T-A", 1), _task("T-C", 1)]
    positions, _cw, _ch = _layout(tasks)
    # X should be identical for the same phase.
    xs = {positions[tid][0] for tid in ("T-A", "T-B", "T-C")}
    assert len(xs) == 1
    # Y ordering follows id sort (T-A above T-B above T-C).
    assert positions["T-A"][1] < positions["T-B"][1] < positions["T-C"][1]


def test_build_html_task_group_count_matches_input() -> None:
    tasks = [_task(f"T-{i}", 1) for i in range(5)]
    html = build_html(_snapshot(tasks))
    assert html.count('<g class="task"') == 5


def test_build_html_is_self_contained() -> None:
    tasks = [_task("T-A", 1)]
    html = build_html(_snapshot(tasks))
    # HTML skeleton present.
    assert html.startswith("<!doctype html>")
    # Everything inline: no <script src=…>, no <link rel="stylesheet" href=…>.
    assert "<script src=" not in html
    assert 'link rel="stylesheet"' not in html
    # Inline SVG is present.
    assert "<svg" in html


def test_build_html_uses_status_color() -> None:
    tasks = [
        _task("T-A", 1, status="done"),
        _task("T-B", 1, status="blocked"),
    ]
    html = build_html(_snapshot(tasks))
    assert STATUS_COLORS["done"] in html
    assert STATUS_COLORS["blocked"] in html


def test_build_html_edges_drawn_for_dependencies() -> None:
    tasks = [
        _task("T-A", 1),
        _task("T-B", 2, deps=["T-A"]),
    ]
    html = build_html(_snapshot(tasks))
    # At least one <path with a cubic Bézier command.
    assert '<path d="M' in html and " C" in html


def test_build_html_gracefully_handles_missing_deps() -> None:
    """A dep pointing to a task not in the snapshot must not crash."""
    tasks = [_task("T-B", 2, deps=["T-A"])]  # T-A not present
    html = build_html(_snapshot(tasks))
    assert html.count('<g class="task"') == 1


# ---- CLI wiring ---------------------------------------------------------


FIXTURE_TASKS = Path(__file__).parent / "fixtures" / "tiny_tasks.json"


def _make_project(root: Path) -> None:
    root.mkdir(parents=True, exist_ok=True)
    (root / "tasks.json").write_bytes(FIXTURE_TASKS.read_bytes())
    scripts = root / "scripts"
    scripts.mkdir()
    for name in ("task-start.sh", "task-finish.sh", "task-block.sh"):
        (scripts / name).write_text("#!/usr/bin/env bash\nexit 0\n")
        (scripts / name).chmod(0o755)
    orch_dir = root / ".orchestrator"
    orch_dir.mkdir()
    (orch_dir / "config.yaml").write_text("state:\n  backend: file\n")
    (orch_dir / "model_router.yaml").write_text(
        "opencode-go/glm-5.1:\n"
        "  backend: opencode\n"
        "  cli_model: glm-5.1\n"
        "  tier: cheap\n"
        "opencode/claude-sonnet-4-6:\n"
        "  backend: claude\n"
        "  cli_model: claude-sonnet-4-6\n"
        "  tier: standard\n"
    )


def test_graph_subcommand_writes_html(
    tmp_path: Path, capsys: pytest.CaptureFixture
) -> None:
    root = tmp_path / "proj"
    _make_project(root)
    out = tmp_path / "plan.html"
    rc = _run_graph_subcommand([
        "--project-root", str(root),
        "--project-id", "proj-graph",
        "--config", ".orchestrator/config.yaml",
        "--out", str(out),
    ])
    assert rc == 0
    assert out.exists()
    content = out.read_text()
    assert content.startswith("<!doctype html>")
    # Fixture has 5 tasks (T-A..T-E).
    assert content.count('<g class="task"') == 5
    stdout = capsys.readouterr().out
    assert "wrote" in stdout and "5 nodes" in stdout


def test_graph_subcommand_only_filter(tmp_path: Path) -> None:
    root = tmp_path / "proj"
    _make_project(root)
    out = tmp_path / "plan.html"
    rc = _run_graph_subcommand([
        "--project-root", str(root),
        "--project-id", "proj-graph",
        "--config", ".orchestrator/config.yaml",
        "--out", str(out),
        "--only", "T-A",
    ])
    assert rc == 0
    content = out.read_text()
    assert content.count('<g class="task"') == 1
