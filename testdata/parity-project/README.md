# testdata/parity-project

A real project scaffolded by the Python `orch`, with real state committed
(`orch.db` — SQLite, ~136 KB), used by `scripts/parity.sh` to diff the
Python and Go CLIs' `--json` output over the exact same data, and by
`internal/cli`'s testscript (`.txtar`) tests as golden output.

## How it was generated

```
orch init /tmp/x/parity-project --template python-api
```

then, since `python-api` only ships 2 tasks and the status distribution
below needs 5, three more tasks were appended by hand to `tasks.json`
under a new phase 2 ("F2 — Items CRUD": `F2.T1`/`F2.T2`/`F2.T3`, same
camelCase shape as the template, `F2.T1` depending on `F1.T1`) — a
plausible continuation of the FastAPI stack the template scaffolds, not
copied from any real spec.

Then, to make every command touch real `tasks_runtime` rows:

```
orch status --project-root /tmp/x/parity-project --project-id parity-project
orch task set --id F0.T1 --status done         --project-root /tmp/x/parity-project --project-id parity-project
orch task set --id F1.T1 --status done         --project-root /tmp/x/parity-project --project-id parity-project
orch task set --id F2.T1 --status done         --project-root /tmp/x/parity-project --project-id parity-project
orch task set --id F2.T2 --status in-progress  --project-root /tmp/x/parity-project --project-id parity-project
orch task set --id F2.T3 --status blocked      --project-root /tmp/x/parity-project --project-id parity-project
```

(the first `orch status` call exists only to trigger `Backend.bootstrap`,
which creates the `tasks_runtime` rows `task set` requires — `task set`
itself does not bootstrap.)

`--project-id parity-project` is passed explicitly and matters: project_id
otherwise defaults to the project root's directory basename, and
`scripts/parity.sh` copies this fixture into a randomly-named temp
directory before running both binaries against it. Every row in `orch.db`
was written with `project_id = 'parity-project'` baked in as data, not
derived from a path — pin the flag or every command that hits SQLite
returns nothing for the (differently-named) temp copy.

Result: 3 `done`, 1 `in-progress`, 1 `blocked` — `F2.T3`'s `blocked`
transition carries orch's standard `task set` note ("manual set via orch
task set"), stored in that task's `comments_json`, not as an event (no
dispatch ever ran against this fixture, so `orch events` is legitimately
empty — see `goldens/events-F2.T3.json`).

## goldens/

Captured output from the commands above, pretty-printed
(`json.dumps(..., indent=2)`) for readability — the real CLI emits compact
JSON (`separators=(",", ":")`); compare parsed values, not raw bytes. Every
occurrence of the fixture's real (temp) absolute path was replaced with
the literal token `<PROJECT_ROOT>` — `scripts/parity.sh` must apply the
same substitution to each live binary's output (using its own copy's real
path) before diffing against these, or against each other.

- `status.json` — `orch status --json`
- `tasks.json` — `orch tasks --json`
- `tasks-filtered.json` — `orch tasks --status todo,in-progress --json` (only `F2.T2` matches; every other task is `done` or `blocked`)
- `events-F2.T3.json` — `orch events F2.T3 --json` (`[]` — see above)
- `logs-F2.T3.txt` — `orch logs F2.T3` has **no `--json` flag** (checked in `orchestrator/orch.py`'s `_run_logs_subcommand` — only `--tail`/`--all`). Captured its plain human output instead: task never dispatched, so no log file exists (`no log file for task 'F2.T3' at <PROJECT_ROOT>/.orchestrator/state/parity-project/logs/F2.T3.log`, exit 2). A future golden with a real log file needs a fixture task that was actually dispatched (out of scope here — this fixture never runs a real CLI subprocess).

## Regenerating

Only regenerate if `tasks.json`'s shape, `_STATUS_TRANSITIONS`, or
`build_status_snapshot`'s output shape changes upstream in Python. Re-run
the commands above from a Python venv with orch installed
(`pip install -e ".[dev]"` from the repo root), against a fresh temp
directory, then re-copy `tasks.json`, `.orchestrator/config.yaml`,
`.orchestrator/model_router.yaml` and
`.orchestrator/state/parity-project/orch.db` here, and regenerate
`goldens/` the same way (capture, then substitute the real absolute path
for `<PROJECT_ROOT>`).
