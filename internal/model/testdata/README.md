# internal/model/testdata

## tasks.json fixtures

`base-tasks.json.tmpl` and `<template>-tasks.json.tmpl` are verbatim copies of
the five templates orch ships (Python source, checked at commit time this
package was added):

- `orchestrator/templates/tasks.json.tmpl` (the empty `orch init` skeleton)
- `orchestrator/templates/projects/python-api/tasks.json.tmpl`
- `orchestrator/templates/projects/chatbot-whatsapp/tasks.json.tmpl`
- `orchestrator/templates/projects/data-pipeline/tasks.json.tmpl`
- `orchestrator/templates/projects/nextjs-saas/tasks.json.tmpl`

Used by the round-trip test (`LoadTasksFile` → `SaveTasksFile` → re-decode
both as generic JSON → compare) to prove the Go reader/writer loses no
information relative to what a real `orch init` writes.

## transitions.json

The exact legal/illegal status-transition matrix, read directly off the
Python source of truth (`_STATUS_TRANSITIONS` in
`orchestrator/state/sqlite_backend.py`), not re-derived by hand. Generated
once with:

```
python3 -c "
import json
from orchestrator.state.sqlite_backend import _STATUS_TRANSITIONS
statuses = ['backlog', 'todo', 'in-progress', 'done', 'blocked']
rows = []
for frm in statuses:
    for to in statuses:
        legal = to in _STATUS_TRANSITIONS.get(frm, frozenset())
        rows.append({'from': frm, 'to': to, 'legal': legal})
payload = {
    'generatedBy': 'see internal/model/testdata/README.md',
    'statuses': statuses,
    'transitions': rows,
}
with open('internal/model/testdata/transitions.json', 'w') as fh:
    json.dump(payload, fh, indent=2)
    fh.write('\n')
"
```

Run from the repo root — `orchestrator.state.sqlite_backend` imports only
the standard library plus `orchestrator.models`, so no venv/install is
needed, just `orchestrator/` importable from the cwd (which it is at repo
root). Re-run this if `_STATUS_TRANSITIONS` ever changes in Python; the Go
test (`TestCanTransitionMatchesPython` in `status_test.go`) fails loudly if
`CanTransition` and this fixture disagree.
