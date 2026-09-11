#!/usr/bin/env python3
"""Build internal/state/testdata/orch-py-0.11.0.db with the PYTHON backend.

The point of the fixture is that Go never touched it: it is a real orch.db
written by orch v0.11.0, so the Go tests prove "a DB Python made opens in Go"
rather than "Go agrees with itself".

Run it with a venv that has orch v0.11.0 installed (see testdata/README.md).
Every timestamp the tests assert on is fixed. The one exception is
`runs.started_at`, which `create_run` stamps from the clock — the tests do
not depend on it.
"""
import shutil
import sys
from pathlib import Path

from orchestrator.models import Dispatch, EventEntry, SpendEntry, Task
from orchestrator.state.sqlite_backend import SqliteBackend

OUT = Path(sys.argv[1]).resolve()
PROJECT_ID = "billing-api"
RUN_ID = "fixture-run-0001"

# Fixed clock. Nothing here may read datetime.now().
T0 = "2026-09-01T09:00:00+00:00"
T1 = "2026-09-01T10:30:00+00:00"
T2 = "2026-09-02T11:15:00+00:00"
T3 = "2026-09-03T14:45:00+00:00"

# The python-api template's shape, extended so the fixture exercises deps,
# phases, estimates and spec refs.
TASKS = [
    Task(id="F0.T1", phase=0, title="Scaffold FastAPI project layout",
         description="", model="claude/claude-sonnet-4-6", reason="Deterministic scaffolding.",
         status="todo", dependencies=[], estimate_hours=0.3,
         files=["pyproject.toml", "app/main.py"], spec_ref="specs/f0-foundation.md#T1",
         comments=[]),
    Task(id="F0.T2", phase=0, title="Postgres connection pool + settings",
         description="", model="claude/claude-sonnet-4-6", reason="",
         status="todo", dependencies=["F0.T1"], estimate_hours=0.8,
         files=["app/db.py"], spec_ref="specs/f0-foundation.md#T2", comments=[]),
    Task(id="F1.T1", phase=1, title="Health endpoint + integration test",
         description="", model="claude/claude-sonnet-4-6", reason="",
         status="todo", dependencies=["F0.T2"], estimate_hours=0.4,
         files=["app/routes/health.py"], spec_ref="specs/f1-health.md#T1", comments=[]),
    Task(id="F1.T2", phase=1, title="POST /customers with validation",
         description="", model="claude/claude-opus-4-6",
         reason="Validation boundaries are where bugs hide.",
         status="todo", dependencies=["F1.T1"], estimate_hours=0.9,
         files=["app/routes/customers.py"], spec_ref="specs/f1-health.md#T2", comments=[]),
    Task(id="F2.T1", phase=2, title="Invoice state machine",
         description="", model="claude/claude-opus-4-6", reason="",
         status="todo", dependencies=["F1.T2"], estimate_hours=1.8,
         files=["app/domain/invoice_state.py"], spec_ref="specs/f2-invoices.md#T1",
         comments=[]),
]

if OUT.exists():
    OUT.unlink()
for suffix in ("-wal", "-shm"):
    p = Path(str(OUT) + suffix)
    if p.exists():
        p.unlink()

backend = SqliteBackend(project_id=PROJECT_ID, db_path=OUT, project_root=Path("/tmp/billing-api"))
backend.bootstrap(TASKS)

# Transitions: one full happy path, one blocked with a note, one in-progress.
backend.set_task_status("F0.T1", "in-progress", author="orch", note="", ts=T0)
backend.set_task_status("F0.T1", "done", author="orch", note="green", ts=T1)
backend.set_task_status("F0.T2", "in-progress", author="orch", note="", ts=T1)
backend.set_task_status("F0.T2", "done", author="orch", note="", ts=T2)
backend.set_task_status("F1.T1", "in-progress", author="orch", note="", ts=T2)
backend.set_task_status("F1.T2", "blocked", author="orch",
                        note="Stripe sandbox key still pending from the client.", ts=T3)

# One run + one in-flight dispatch row (dispatches FK onto runs).
backend.create_run(RUN_ID, mode="auto", parent_pid=4200)
backend.add_dispatch(RUN_ID, Dispatch(
    task_id="F1.T1", backend="claude", pid=4242, session_id="sess-abc123",
    started_at=T2, prompt_path="state/prompts/F1.T1.md",
    log_path="state/logs/F1.T1.log", output_path="state/out/F1.T1.json", attempt=1,
))

# Events — including one exact duplicate so the fixture proves dedup by hash.
for ev in (
    EventEntry(event_type="dispatch", task_id="F0.T1", backend="claude", ts=T0,
               extra={"pid": 4240}, project_id=PROJECT_ID),
    EventEntry(event_type="exit_ok", task_id="F0.T1", backend="claude", ts=T1,
               extra={"duration_s": 5400.0}, project_id=PROJECT_ID),
    EventEntry(event_type="dispatch", task_id="F1.T1", backend="claude", ts=T2,
               extra={"pid": 4242}, project_id=PROJECT_ID),
    EventEntry(event_type="block", task_id="F1.T2", backend="claude", ts=T3,
               extra={"reason": "waiting on the client"}, project_id=PROJECT_ID),
    # Byte-identical to the first event: INSERT OR IGNORE must drop it.
    EventEntry(event_type="dispatch", task_id="F0.T1", backend="claude", ts=T0,
               extra={"pid": 4240}, project_id=PROJECT_ID),
):
    backend.append_event(RUN_ID, ev)

# Two spend rows. cost_usd/duration_s are chosen so the dedup hash exercises
# Python's float repr: 0.42 stays "0.42", 1.0 renders as "1.0" (Go's default
# formatter would say "1" — see pyFloat in dedup.go).
backend.append_spend(SpendEntry(
    ts=T1, task_id="F0.T1", backend="claude", model="claude/claude-sonnet-4-6",
    tokens_in=18000, tokens_out=5200, cost_usd=0.42, duration_s=5400.0,
    project_id=PROJECT_ID, estimated=False,
))
backend.append_spend(SpendEntry(
    ts=T2, task_id="F0.T2", backend="claude", model="claude/claude-opus-4-6",
    tokens_in=41000, tokens_out=9100, cost_usd=1.0, duration_s=1.5,
    project_id=PROJECT_ID, estimated=False,
))

# One milestone with two tasks on it.
backend.upsert_milestone("M1", "Customer API live",
                         description="First slice the client can click on.",
                         target_date="2026-09-20")
backend.set_task_milestone("F1.T1", "M1")
backend.set_task_milestone("F1.T2", "M1")

print(f"schema_version={backend.schema_version()}")
print(f"wrote {OUT} ({OUT.stat().st_size} bytes)")
