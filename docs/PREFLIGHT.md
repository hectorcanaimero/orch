# orch preflight — doctor · validate · init wizard

Three commands that together form the "front door" of orch. Use them before every long unattended run, when onboarding a new machine, and when a scaffold refuses to behave.

## When to use which

| Situation | Command |
|---|---|
| Setting up on a new machine — is my env ready? | `orch doctor` |
| Edited `tasks.json` — did I break the DAG? | `orch validate` |
| Starting a brand-new project | `orch init` (with no flags for the wizard) |
| Onboarding a coworker onto an existing project | `orch doctor && orch validate` |
| Debugging a failed dispatch | `orch doctor --only backend` |

---

## `orch doctor`

Read-only preflight. Never mutates anything on disk. Verifies:

- **Config files** parse cleanly — `config.yaml`, `budgets.yaml` (+ active preset present), `model_router.yaml`, `tasks.json`.
- **Scripts** — `scripts/task-*.sh` present + executable, and `jq` on `PATH` (required by the scripts).
- **Backends** — for every backend referenced by `tasks.json`: `shutil.which(cli)` present + `<cli> --version` succeeds. Plus a cheap read-only auth probe (only `opencode auth list` is fully cheap; `claude` + `codex` flagged as `skip` with an explanation).
- **Model resolution** — every `task.model` resolves to a `model_router.yaml` entry.
- **State backend** — state dir writable; when `backend: sqlite`, DB opens + `PRAGMA user_version` matches. For `backend: file`, the DB check is skipped with an explanatory row.

### Usage

```bash
# Human-readable checklist
orch doctor

# JSON report (for scripts / CI gates)
orch doctor --json | jq '.summary'

# Narrow to one family (substring match on check name)
orch doctor --only backend
orch doctor --only scripts
orch doctor --only state

# Point at a different project
orch doctor --project-root /path/to/other-project
```

### Exit codes

| Code | Meaning |
|---:|---|
| 0 | All checks passed. |
| 1 | At least one warning, no errors. |
| 2 | At least one error. |

### Reading the output

- `✓ ok` — check passed, nothing to do.
- `⚠ warn` — something's off but orch can still run (fallback active, legacy version, chmod bit missing).
- `✗ error` — orch will misbehave until fixed. Remediation string printed in the "Remediation" section at the bottom.
- `○ skip` — check intentionally not run (feature disabled, N/A for the active backend, no cheap probe available).

### Example

```
$ orch doctor
                             orch doctor · project=rupies · backend=file
┌──┬───────────────────────────────┬────────┬─────────────────────────────────────────────────────┐
│  │ CHECK                         │ STATUS │ DETAIL                                              │
├──┼───────────────────────────────┼────────┼─────────────────────────────────────────────────────┤
│ ✓│ backend.claude                │ ok     │ claude 1.4.2 at /usr/local/bin/claude               │
│ ○│ backend.claude.auth           │ skip   │ no cheap auth probe for claude CLI — assumed ok     │
│ ✓│ backend.opencode              │ ok     │ opencode 1.18.19 at /Users/al3jandro/.local/bin/... │
│ ✓│ backend.opencode.auth         │ ok     │ opencode auth list ok                               │
│ ✓│ budgets.parse                 │ ok     │ budgets.yaml parses ok                              │
│ ✓│ budgets.preset                │ ok     │ preset 'conservative' present                       │
│ ✓│ config.parse                  │ ok     │ config.yaml parses ok                               │
│ ✓│ jq.present                    │ ok     │ jq at /opt/homebrew/bin/jq                          │
│ ✓│ models.resolve                │ ok     │ 42 task(s) resolve                                  │
│ ✓│ router.parse                  │ ok     │ model_router.yaml parses ok                         │
│ ✓│ scripts.task-block.sh         │ ok     │ task-block.sh present + executable                  │
│ ✓│ scripts.task-finish.sh        │ ok     │ task-finish.sh present + executable                 │
│ ✓│ scripts.task-start.sh         │ ok     │ task-start.sh present + executable                  │
│ ○│ state.db.accessible           │ skip   │ backend=file — sqlite DB check not applicable       │
│ ✓│ state.dir.writable            │ ok     │ orchestrator/state writable                         │
│ ✓│ tasks.parse                   │ ok     │ tasks.json parses ok (42 task rows)                 │
└──┴───────────────────────────────┴────────┴─────────────────────────────────────────────────────┘
14 ok · 0 warn · 0 error · 2 skip
```

---

## `orch validate`

Static whole-graph analysis. No dispatch, no subprocess probes (that's `orch doctor`). Runs:

- **`schema.tasks`** — every task has the required fields with sane types.
- **`dep.missing`** — every id in `dependencies[]` must exist.
- **`dep.cycle`** — DFS 3-coloring reports the ACTUAL cycle path, not just "cycle exists".
- **`route.unresolved`** — every `task.model` must have a `model_router.yaml` entry.
- **`schema.config`** — `config.yaml` shape (state.backend enum, concurrency mapping).
- **`preset.sanity`** — active budget preset provider windows must fit ≥ 2× typical dispatch tokens (warns otherwise; dispatches would serialize).
- **`files.writable`** (opt-in `--files`) — parent dirs of `task.files[]` exist + writable.

### Usage

```bash
# Human report grouped by kind
orch validate

# JSON report
orch validate --json

# Also verify file-writability for every task.files[] entry
orch validate --files
```

### Exit codes

Same 0/1/2 convention as doctor.

### Cycle reporting

The report always names the actual cycle path:

```
[dep.cycle] (1):
  - A · dependencies: dependency cycle: A -> B -> C -> A
```

so you know exactly which edge to break.

---

## `orch init` — interactive wizard

Scaffolds a new orch project. Two modes, one command:

### Batch mode (unchanged from Sprint 9)

```bash
orch init ./my-project --project-name my-project
orch init ./my-project --project-name my-project --force  # overwrite existing
orch init ./my-project --sdd                              # also scaffold openspec/
```

Every existing flag still works. If you pass a `PATH` or any of `--force / --sdd / --project-name`, orch skips the wizard entirely.

### Interactive mode (Sprint D)

```bash
# All defaults come from wizard prompts
orch init

# Or force the wizard even when a path is present
orch init --interactive
```

The wizard asks for:

1. **project id** — must match `[a-z0-9][a-z0-9_-]*`.
2. **project root** — parent dir must exist and be writable.
3. Backend detection — informational (`shutil.which claude`, `codex`, `opencode`).
4. **state backend** — `file` (default) or `sqlite`.
5. **budget preset** — chosen from the presets in the shipped `budgets.yaml`.
6. **spec root** — the relative dir where specs live (default: `specs`).
7. **model tier picker** — pick a default for premium/standard/cheap from the routes in the shipped `model_router.yaml`.
8. **openspec/ scaffold** (y/n).

For existing projects, the wizard prompts per-file before overwriting anything (default: keep).

After scaffolding, the wizard:

- Rewrites `state.backend`, `budgets_preset`, `spec_root` in the generated `config.yaml` (regex-edit that preserves the shipped comments).
- Stamps the tier picks into `tasks.json` `meta` for downstream tools.
- Runs `orch validate` inline and prints a green/red summary.
- Prints next-steps commands.

### CI / non-TTY safety

When `stdin` isn't a TTY (piped, CI runner) and no scaffolder flags were provided, the wizard errors out immediately:

```
orch init: error: interactive mode requires a TTY on stdin; pass PATH explicitly or use --non-interactive
```

Use `orch init /path --project-name foo` (or `--non-interactive` with a `PATH`) in CI.

---

## Typical operator workflows

### Onboarding a new machine

```bash
brew install jq
brew install opencode-cli  # or whichever backends you use

orch doctor                    # baseline the env
orch doctor --only backend     # narrow if something's missing
```

### Bringing up a new project

```bash
cd ~/projects
orch init                      # wizard
cd my-new-project
orch doctor                    # verify the fresh scaffold
orch atomize --file specs/f0-foundation.md --apply
orch validate                  # sanity check the generated graph
orch dry-run
```

### Debugging a stuck dispatch

```bash
orch doctor --only backend    # is the CLI even installed?
orch doctor --only jq          # jq missing = task-*.sh silently mangles output
orch validate                  # is the graph even sane?
orch doctor --json | jq '.checks[] | select(.status=="error")'
```

### CI pipeline gate

```yaml
# .github/workflows/orch-preflight.yml
- run: orch doctor --json > doctor.json
- run: orch validate --json > validate.json
- run: |
    jq -e '.summary.error == 0' doctor.json
    jq -e '.summary.errors == 0' validate.json
```

---

## See also

- Design doc: `docs/design/sprint-d-preflight-and-wizard.md`
- Sprint A (budget guardrails): `docs/design/sprint-b-sqlite-backend.md` neighbors
- Sprint B (sqlite backend): `docs/SQLITE-BACKEND.md`
- Sprint C (observability): `docs/OBSERVABILITY.md`
