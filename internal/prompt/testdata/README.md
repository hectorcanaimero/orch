# `internal/prompt/testdata`

Two sets of goldens for one renderer, and the split is the point.

| Path | Rendered by | What it holds us to |
|---|---|---|
| `*.txt` | Python's `prompt_builder` (`make-goldens.py`) | the prompt's **head** — every line from `TASK_ID=` down to `Spec ref (READ FIRST):` — must still match byte for byte |
| `go/*.txt` | this package | the **whole** body, divergent blocks included |
| `cases.json` | `make-goldens.py` | the inputs both sets are rendered from, so neither side retypes a task |

Until G6.6 the Python files were the whole target. They are not any more,
because two blocks changed on purpose.

## What changed, and why the Python files are still here

**The protocol block offers MCP first** (G6.6). `orch mcp` serves the same
state transitions as tools, so the prompt names `orch_set_status` /
`orch_block` and falls back to `scripts/task-{finish,block}.sh` for an agent
that has no tools. Python has no MCP server, so there is nothing to be
faithful to.

**The dependency block finally has content** (bug 24). This is the part worth
keeping the Python files for. `prompt_builder._read_dep_last_comment` reads
the comment entry's `text` key:

```python
text = str(last.get("text", "")) if isinstance(last, dict) else str(last)
```

Nothing writes `text`. All three comment writers in
`orchestrator/state/sqlite_backend.py` marshal `{"author", "body", "at"}`, and
so does Go's `state.appendComment`:

```
$ sqlite3 internal/state/testdata/orch-py-0.11.0.db \
    "select comments_json from tasks_runtime where task_id='F0.T1';"
[{"author": "orch", "body": "in-progress", "at": "2026-09-01T09:00:00+00:00"},
 {"author": "orch", "body": "green",       "at": "2026-09-01T10:30:00+00:00"}]
```

So `Completed dependencies (context):` has rendered `(no comment)` on every
dispatch since it was written — and `testdata/data-pipeline-f2t1.txt` shows
exactly that, for a dependency whose note is sitting in `cases.json` three
lines away. `TestPythonRendersEveryDependencyAsNoComment` pins it. The bug is
recorded in `docs/brainstorm/go-migration-notes/opus-2.md`.

**The fixtures were complicit and are not any more.** The old `dep()` helper
in `make-goldens.py` wrote `{"author", "ts", "text"}` — copied from the
reader's own docstring rather than captured from a writer — so Python found
its key and every golden looked correct. The shape above is what a database
Python wrote actually contains, and the trail now has three entries, because
that is what a normally-finished task has: orch at dispatch, the agent's
report, orch again when the reaper closes it. The agent's note is therefore
**not** the last entry, which is the second half of the bug and why
`prompt.AgentComment` scans back for the first non-`orch` author instead of
taking `comments[-1]`.

## Regenerating

```bash
# the Python goldens and cases.json
PYTHONPATH=. .venv/bin/python internal/prompt/testdata/make-goldens.py

# the Go goldens, from the current renderer
go test ./internal/prompt -run TestRenderMatchesTheGoGoldens -update
```

Read the diff both times. A golden nobody looked at pins whatever was there,
typo included — `TestGoldensCoverWhatTheyAreFor` exists because regenerating
can silently drop the boundary case (the multi-byte truncation, the task with
no files, the dependency no agent ever reported on) while every comparison
keeps passing.

Three of the five cases are tasks out of the shipped project templates, so a
change to a `tasks.json.tmpl` moves these files. That is intended: those are
the prompts a real `orch init` + `orch run` sends.
