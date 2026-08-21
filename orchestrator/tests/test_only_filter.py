"""Regression test: `--only` must not break dep validation for out-of-glob deps.

Repro: `python3 -m orchestrator --only P0-013 ...` used to fail with
    tasks.json has 1 unresolved dependency reference(s):
      - P0-013 depends on unknown 'P0-012'
because `_filter_by_only` ran BEFORE `TaskQueue(...)` was constructed, so
P0-013's dep on P0-012 (which was filtered out) tripped `_validate_deps`.

Correct behavior: `TaskQueue` validates the FULL DAG; `--only` is a
dispatcher-scope filter, not a graph-scope filter. This test asserts the
orchestrator returns a clean exit (not the dep-validation error) when
`--only` targets a task whose deps exist elsewhere in tasks.json.
"""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from orchestrator import orch as orch_mod


FIXTURES = Path(__file__).parent / "fixtures"


def _write_only_filter_tasks(dst: Path) -> None:
    """Two tasks: P0-012 already done, P0-013 depends on P0-012 (todo)."""
    data = {
        "meta": {"note": "regression fixture for --only dep validation"},
        "phases": [{"id": 1, "name": "test"}],
        "tasks": [
            {
                "id": "P0-012",
                "phase": 1,
                "title": "Already done",
                "description": "",
                "model": "opencode-go/glm-5.1",
                "reason": "",
                "status": "done",
                "dependencies": [],
                "estimateHours": 0.1,
                "files": [],
                "specRef": "",
                "comments": [],
            },
            {
                "id": "P0-013",
                "phase": 1,
                "title": "Depends on P0-012",
                "description": "",
                "model": "opencode-go/glm-5.1",
                "reason": "",
                "status": "todo",
                "dependencies": ["P0-012"],
                "estimateHours": 0.1,
                "files": [],
                "specRef": "",
                "comments": [],
            },
        ],
    }
    dst.write_text(json.dumps(data), encoding="utf-8")


@pytest.fixture()
def only_filter_v2(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Path:
    """Stage a v2/ dir where P0-013's dep (P0-012) is done but out of --only glob."""
    from orchestrator.tests.test_orch import _stage_v2, _mk_no_op_script  # noqa: PLC0415

    tasks_path = tmp_path / "only_filter_tasks.json"
    _write_only_filter_tasks(tasks_path)

    v2 = _stage_v2(
        tmp_path,
        tasks_path,
        FIXTURES / "main_loop_router.yaml",
        FIXTURES / "main_loop_config.yaml",
    )
    monkeypatch.chdir(v2)
    return v2


def test_only_filter_does_not_break_dep_validation(
    only_filter_v2: Path,
    capsys: pytest.CaptureFixture[str],
) -> None:
    """`--only P0-013` must NOT raise MissingDependencyError just because
    the out-of-glob dep (P0-012) was filtered from the tasks list.

    We use --dry-run to keep the test hermetic (no subprocess spawn), so the
    only failure surface is the pre-dispatch validation path.
    """
    rc = orch_mod.main(["--dry-run", "--only", "P0-013"])
    err = capsys.readouterr().err

    # The specific bug: MissingDependencyError message must NOT appear.
    assert "unresolved dependency reference" not in err, (
        f"regression: --only broke dep validation. stderr:\n{err}"
    )
    assert "depends on unknown" not in err, (
        f"regression: --only broke dep validation. stderr:\n{err}"
    )
    # And the run must exit cleanly.
    assert rc == 0, f"expected exit 0, got {rc}. stderr:\n{err}"
