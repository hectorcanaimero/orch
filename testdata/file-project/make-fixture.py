#!/usr/bin/env python3
"""Generate golden.json for internal/cli's `orch migrate` test.

The fixture files themselves (tasks.json, .orchestrator/config.yaml,
.orchestrator/state/{events,spend}-run1.jsonl, scripts/task-start.sh) are
committed directly, hand-authored to a fixed, small shape — this script's
job is producing the GOLDEN by running the REAL Python
`orchestrator.migrate.run_migrate` against a disposable copy of the
fixture, then dumping what it wrote to orch.db. Run with a venv holding
the orch v0.11.0 Python package:

    <venv>/bin/python testdata/file-project/make-fixture.py

Never run against the committed fixture directory itself — migrate writes
a backup dir and an orch.db next to the state it reads, which don't belong
in git. This script always operates on a temp copy.
"""
import json
import shutil
import sqlite3
import sys
import tempfile
from pathlib import Path

HERE = Path(__file__).resolve().parent
FIXTURE_FILES = ["tasks.json", ".orchestrator", "scripts"]


def main() -> int:
    with tempfile.TemporaryDirectory() as tmp:
        project = Path(tmp) / "proj"
        project.mkdir()
        for name in FIXTURE_FILES:
            src = HERE / name
            dst = project / name
            if src.is_dir():
                shutil.copytree(src, dst)
            else:
                shutil.copy2(src, dst)

        sys.path.insert(0, str(HERE.parent.parent))  # repo root, for `orchestrator`
        from orchestrator.migrate import run_migrate

        rc = run_migrate(["--project-root", str(project), "--project-id", "file-project"])
        if rc != 0:
            print(f"run_migrate exited {rc}", file=sys.stderr)
            return 1

        db_path = project / ".orchestrator" / "state" / "orch.db"
        conn = sqlite3.connect(str(db_path))
        conn.row_factory = sqlite3.Row

        def rows(sql: str) -> list[dict]:
            return [dict(r) for r in conn.execute(sql).fetchall()]

        golden = {
            "tasks_runtime": rows(
                "SELECT task_id, status, comments_json FROM tasks_runtime "
                "WHERE project_id = 'file-project' ORDER BY task_id"
            ),
            "events": rows(
                "SELECT run_id, event_type, task_id, backend, ts, extra_json FROM events "
                "WHERE project_id = 'file-project' ORDER BY id"
            ),
            "spend": rows(
                "SELECT ts, task_id, backend, model, tokens_in, tokens_out, "
                "cost_usd, duration_s, estimated FROM spend "
                "WHERE project_id = 'file-project' ORDER BY ts, task_id"
            ),
        }
        conn.close()

    out = HERE / "golden.json"
    out.write_text(json.dumps(golden, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(
        f"wrote {out}: {len(golden['tasks_runtime'])} tasks, "
        f"{len(golden['events'])} events, {len(golden['spend'])} spend rows"
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
