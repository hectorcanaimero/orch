# python-frozen/

Byte-for-byte copies of the Python package's packaged defaults —
`orchestrator/config.yaml`, `orchestrator/budgets.yaml` and
`orchestrator/model_router.yaml` — taken from `main` at `5db7091`
(2026-09-13), the Python tree the last Python release is cut from.

`TestPackagedDefaultsMatchPython` compares `internal/scaffold/defaults/`
against these, so a project scaffolded by this binary and one scaffolded by the
last Python release cannot differ by accident after the Python tree is gone.

**Never edit these files.** To change a default on purpose, edit
`internal/scaffold/defaults/` and add the file to `defaultsDivergedFromPython`
in `scaffold_test.go` with the reason.
