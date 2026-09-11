"""What Python actually stores in started_at / finished_at across a reopen.

Run with the v0.11.0 venv. Output is the golden vector for the Go test.
"""
import json, sys, tempfile
from pathlib import Path
from orchestrator.models import Task
from orchestrator.state.sqlite_backend import SqliteBackend

db = Path(tempfile.mkdtemp()) / "orch.db"
b = SqliteBackend(project_id="p", db_path=db, project_root=Path("/tmp/p"))
b.bootstrap([Task(id="T1", phase=0, title="t", description="", model="m",
                  reason="", status="todo", dependencies=[], estimate_hours=0.5,
                  files=[], spec_ref="", comments=[])])

steps = [
    ("in-progress", "2026-09-01T09:00:00Z"),
    ("done",        "2026-09-01T11:00:00Z"),
    ("todo",        "2026-09-05T09:00:00Z"),
    ("in-progress", "2026-09-05T10:00:00Z"),
    ("done",        "2026-09-05T12:00:00Z"),
]
import sqlite3
out = []
for status, ts in steps:
    b.set_task_status("T1", status, author="orch", note="", ts=ts)
    c = sqlite3.connect(str(db)); c.row_factory = sqlite3.Row
    r = c.execute("SELECT status, started_at, finished_at, updated_at "
                  "FROM tasks_runtime WHERE task_id='T1'").fetchone()
    c.close()
    out.append({"after": f"{status} @ {ts}", "status": r["status"],
                "started_at": r["started_at"], "finished_at": r["finished_at"],
                "updated_at": r["updated_at"]})
print(json.dumps(out, indent=2))
