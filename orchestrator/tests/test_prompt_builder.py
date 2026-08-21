"""Unit tests for `orchestrator.prompt_builder` (R-012).

Covers:
    - Golden-file test locks the rendered template (FR-P-1, FR-P-2, FR-P-5).
    - `completed_deps == []` omits the "Completed dependencies" block
      (design §6).
    - Missing `spec_ref` renders the placeholder + WARNs (FR-P-2 fallback).
    - `Task.files == []` renders literal `[]`.
    - Dep-comment reader pulls the LAST `comments[].text` and truncates to
      500 chars (FR-P-3).
    - Output path shape: `state/prompts/<run-id>/<task-id>.txt` (FR-P-4).
    - Every prompt embeds `TASK_ID=<id>` on line 1 (FR-P-5, AS-10).
"""

from __future__ import annotations

from pathlib import Path

from orchestrator.models import Task
from orchestrator.prompt_builder import read_dep_last_comment, render_prompt


GOLDEN = (
    Path(__file__).parent / "fixtures" / "prompts" / "golden_prompt.txt"
)


def _mk_task(**overrides) -> Task:
    """Minimal task; override individual fields per test."""
    base = dict(
        id="T-A",
        phase=1,
        title="Root A",
        description="First root",
        model="opencode-go/glm-5.1",
        reason="Cheap sanity task",
        status="todo",
        dependencies=[],
        estimate_hours=0.5,
        files=[],
        spec_ref="",
        comments=[],
    )
    base.update(overrides)
    return Task(**base)


def _mk_dep(tid: str, text: str) -> Task:
    return _mk_task(
        id=tid,
        title=f"Dep {tid}",
        comments=[{"author": "agent", "ts": "2026-01-01", "text": text}],
    )


# ---- Golden file (locks the format) ------------------------------------


def test_render_prompt_matches_golden(tmp_path: Path) -> None:
    """Any accidental template drift will fail this test — that's the point.

    `project_root` pinned to a stable value so the golden test is deterministic
    regardless of where pytest runs from (was breaking when repo lived at a
    path other than /Volumes/PortableSSD/rupies/v2).
    """
    t = _mk_task()
    dep = _mk_dep("T-DEP", "dep did the thing")
    path = render_prompt(
        task=t,
        completed_deps=[dep],
        spec_ref="specs/foo.md",
        run_id="r1",
        state_dir=tmp_path,
        project_root=Path("/tmp/orch-golden-project"),
    )
    got = path.read_text(encoding="utf-8")
    want = GOLDEN.read_text(encoding="utf-8")
    assert got == want


# ---- Path shape (FR-P-4) ------------------------------------------------


def test_output_path_shape(tmp_path: Path) -> None:
    t = _mk_task()
    path = render_prompt(t, [], "specs/foo.md", "run-42", tmp_path)
    assert path == tmp_path / "prompts" / "run-42" / "T-A.txt"
    assert path.exists()


# ---- Empty deps (design §6) --------------------------------------------


def test_empty_deps_omits_block(tmp_path: Path) -> None:
    t = _mk_task()
    path = render_prompt(t, [], "specs/foo.md", "r", tmp_path)
    text = path.read_text(encoding="utf-8")
    assert "Completed dependencies" not in text
    # Sanity: the sections that MUST stay are still there.
    assert "Coordination protocol:" in text
    assert "TASK_ID=T-A" in text


# ---- Missing spec_ref (FR-P-2 fallback) --------------------------------


def test_missing_spec_ref_renders_placeholder(tmp_path: Path) -> None:
    t = _mk_task()
    path = render_prompt(t, [], None, "r", tmp_path)
    text = path.read_text(encoding="utf-8")
    assert "no spec ref provided" in text
    # No path fragment leaked when there's no ref.
    assert "docs/rewrite-plan/" not in text


def test_empty_string_spec_ref_treated_as_missing(tmp_path: Path) -> None:
    t = _mk_task()
    path = render_prompt(t, [], "", "r", tmp_path)
    text = path.read_text(encoding="utf-8")
    assert "no spec ref provided" in text


# ---- files == [] renders literal `[]` ----------------------------------


def test_empty_files_renders_literal_brackets(tmp_path: Path) -> None:
    t = _mk_task(files=[])
    path = render_prompt(t, [], "specs/foo.md", "r", tmp_path)
    text = path.read_text(encoding="utf-8")
    assert "Files you may write: []" in text
    assert "Do NOT touch files outside []" in text


def test_files_populated_renders_list_repr(tmp_path: Path) -> None:
    t = _mk_task(files=["src/foo.ts", "src/bar.ts"])
    path = render_prompt(t, [], "specs/foo.md", "r", tmp_path)
    text = path.read_text(encoding="utf-8")
    assert "['src/foo.ts', 'src/bar.ts']" in text


# ---- TASK_ID marker (FR-P-5 / AS-10) -----------------------------------


def test_prompt_starts_with_task_id_marker(tmp_path: Path) -> None:
    t = _mk_task(id="B-020")
    path = render_prompt(t, [], "specs/foo.md", "r", tmp_path)
    first_line = path.read_text(encoding="utf-8").splitlines()[0]
    assert first_line == "TASK_ID=B-020"


# ---- Dep-comment reader (FR-P-3) ---------------------------------------


def test_read_dep_last_comment_returns_latest_text() -> None:
    dep = _mk_task(
        comments=[
            {"author": "a", "text": "first"},
            {"author": "b", "text": "second"},
            {"author": "c", "text": "third"},
        ]
    )
    assert read_dep_last_comment(dep) == "third"


def test_read_dep_last_comment_empty_when_no_comments() -> None:
    dep = _mk_task(comments=[])
    assert read_dep_last_comment(dep) == ""


def test_read_dep_last_comment_truncates_at_500_chars() -> None:
    dep = _mk_task(comments=[{"author": "a", "text": "x" * 800}])
    got = read_dep_last_comment(dep)
    # 500 chars + ellipsis marker (single char).
    assert len(got) == 501
    assert got.endswith("…")


def test_missing_comment_renders_no_comment_placeholder(tmp_path: Path) -> None:
    dep = _mk_task(id="T-DEP", comments=[])  # empty comments → placeholder
    t = _mk_task()
    path = render_prompt(t, [dep], "specs/foo.md", "r", tmp_path)
    text = path.read_text(encoding="utf-8")
    assert "- T-DEP: (no comment)" in text
