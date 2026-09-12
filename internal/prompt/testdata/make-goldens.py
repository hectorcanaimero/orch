#!/usr/bin/env python3
"""Render prompts with the PYTHON prompt_builder, for Go to be held to.

    PYTHONPATH=. python3 internal/prompt/testdata/make-goldens.py

The cases come from the shipped project templates
(`orchestrator/templates/projects/*/tasks.json.tmpl`) rather than from
invented tasks: those are the prompts a real `orch init` + `orch run` sends to
an agent, so they are the ones that have to survive the port.

Everything a prompt depends on is pinned — project root, run id, spec root,
dependency comments — because the point of a golden is that a diff means the
renderer changed, not that the machine did.

WHAT THESE FILES ARE FOR, SINCE G6.6
------------------------------------
They are no longer the whole target. Go's prompt diverges below the
`Spec ref (READ FIRST):` line on purpose — the protocol block offers MCP
first, and the dependency block finally has content (bug 24). So:

  * the HEAD of each file, down to and including the spec-ref line, is still
    compared byte for byte against Go's render;
  * the rest is kept as EVIDENCE: it is what Python renders for a dependency
    whose real note is right there in the fixture, and what it renders is
    `(no comment)`, every time.

`testdata/go/*.txt` are the Go goldens for the whole body. See
`testdata/README.md`.

The spec-ref line in the template cases reads `specs/f0-foundation.md#T1`.
It read `specs/specs/f0-foundation.md#T1` when these goldens were first
generated — the templates carried the `specs/` prefix that `spec_root` already
supplies, so every agent was sent one directory deeper than the file exists
(bug 12, fixed in #132). Regenerating after that fix is what produced the
current files. Worth knowing before regenerating again: a change to a
`tasks.json.tmpl` moves these.
"""

from __future__ import annotations

import json
import shutil
import sys
import tempfile
from pathlib import Path

REPO = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(REPO))

from orchestrator.models import Task  # noqa: E402
from orchestrator.prompt_builder import render_prompt  # noqa: E402

OUT = Path(__file__).resolve().parent
PROJECT_ROOT = Path("/tmp/orch-golden-project")
RUN_ID = "r1"


def task(**kw) -> Task:
    base = dict(
        id="T-A", phase=0, title="", description="", model="claude/claude-sonnet-4-6",
        reason="", status="todo", dependencies=[], estimate_hours=0.0,
        files=[], spec_ref="", comments=[],
    )
    base.update(kw)
    return Task(**base)


def from_template(name: str, index: int) -> Task:
    """One task out of a shipped template, by position."""
    raw = (REPO / "orchestrator" / "templates" / "projects" / name /
           "tasks.json.tmpl").read_text(encoding="utf-8")
    # The template has {{placeholders}} outside the JSON string values; the
    # task fields we use are literal, so a parse failure here means the file
    # shape changed and the golden should be regenerated deliberately.
    data = json.loads(raw.replace("{{PROJECT_NAME}}", "demo")
                         .replace("{{project_name}}", "demo"))
    return Task.from_json(data["tasks"][index])


def dep(tid: str, body: str) -> Task:
    """A finished dependency carrying its REAL comment trail.

    Shape and interleaving captured from a database Python wrote — not
    invented, which is how bug 24 survived three passing tests:

        $ sqlite3 internal/state/testdata/orch-py-0.11.0.db \
            "select comments_json from tasks_runtime where task_id='F0.T1';"
        [{"author": "orch", "body": "in-progress", "at": "2026-09-01T09:00:00+00:00"},
         {"author": "orch", "body": "green",       "at": "2026-09-01T10:30:00+00:00"}]

    Keys are `author`/`body`/`at`. `prompt_builder` reads `text`, which nothing
    writes; the previous version of this helper wrote `{"author","ts","text"}`,
    copied from that reader's docstring, so Python's renderer found its own key
    and every golden looked right.

    Three entries, because that is what a normally-finished task has: orch at
    dispatch, the agent's own report, orch again when the reaper transitions it
    to done. The agent's note is therefore NOT the last entry, which is the
    other half of the bug.
    """
    return task(id=tid, title=f"Dep {tid}", comments=[
        {"author": "orch", "body": "in-progress",
         "at": "2026-01-01T09:00:00+00:00"},
        {"author": "claude/claude-sonnet-4-6", "body": body,
         "at": "2026-01-01T10:00:00+00:00"},
        {"author": "orch", "body": "dispatch succeeded",
         "at": "2026-01-01T10:30:00+00:00"},
    ])


def dep_unreported(tid: str) -> Task:
    """A dependency orch closed and no agent ever reported on.

    `orch task set --id X --status done` with no note, or a reap whose agent
    never called task-finish. Every entry is orch's own, so there is no
    sentence to show and the block must say so rather than echoing the
    bookkeeping.
    """
    return task(id=tid, title=f"Dep {tid}", comments=[
        {"author": "orch", "body": "in-progress",
         "at": "2026-01-01T09:00:00+00:00"},
        {"author": "orch", "body": "dispatch succeeded",
         "at": "2026-01-01T10:30:00+00:00"},
    ])


CASES = [
    # (golden filename, task, completed deps, spec_root)
    ("python-api-f0t1.txt", from_template("python-api", 0), [], "specs"),
    ("nextjs-saas-f1t1.txt", from_template("nextjs-saas", 1),
     [dep("F0.T1", "scaffolded the app router and tailwind")], "specs"),
    ("data-pipeline-f2t1.txt", from_template("data-pipeline", 2),
     [dep("F0.T1", "created the warehouse schema"),
      dep_unreported("F1.T1")], "specs"),
    # No spec ref at all: the placeholder path.
    ("no-spec-ref.txt", task(id="T-A", title="Root A", description="First root",
                             model="opencode-go/glm-5.1"), [], "specs"),
    # A spec_root the operator moved, and a dep comment over the 500-character
    # cap with multi-byte characters straddling it — truncation is by
    # character in Python, and a byte-wise port would cut a rune in half.
    ("custom-spec-root.txt",
     task(id="B-020", title="Café ☕", description="ñandú",
          files=["src/a.ts", "src/b.ts"], spec_ref="deep/nested.md#Anchor"),
     [dep("T-LONG", "ñ" * 600)], "docs/specs"),
]


def main() -> None:
    for name, t, deps, spec_root in CASES:
        with tempfile.TemporaryDirectory() as td:
            path = render_prompt(
                task=t, completed_deps=deps,
                spec_ref=t.spec_ref or None,
                run_id=RUN_ID, state_dir=Path(td),
                project_root=PROJECT_ROOT, spec_root=spec_root,
            )
            shutil.copy(path, OUT / name)
            print(f"wrote {name} ({(OUT / name).stat().st_size} bytes)")

    # The inputs, so the Go test drives the same tasks rather than retyping
    # them. `comments` is the raw JSON the Task carried.
    inputs = []
    for name, t, deps, spec_root in CASES:
        inputs.append({
            "golden": name,
            "spec_root": spec_root,
            "task": {
                "id": t.id, "title": t.title, "description": t.description,
                "model": t.model, "files": list(t.files),
                "specRef": t.spec_ref, "phase": t.phase,
            },
            "deps": [
                {"id": d.id, "comments": list(d.comments)} for d in deps
            ],
        })
    (OUT / "cases.json").write_text(
        json.dumps({
            "_comment": "Generated by make-goldens.py. Do not hand-edit: regenerate.",
            "project_root": str(PROJECT_ROOT),
            "run_id": RUN_ID,
            "cases": inputs,
        }, indent=2) + "\n", encoding="utf-8")
    print("wrote cases.json")


if __name__ == "__main__":
    main()
