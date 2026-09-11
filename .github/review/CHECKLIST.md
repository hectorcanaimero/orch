# Review checklist

This file is the reviewer's entire brief. It is versioned on purpose: when the
review gets something wrong, the fix is a PR against this file, not a prompt
edited in a workflow.

Every rule is phrased so that a violation is a fact about the diff, not a
matter of taste. If you find yourself wanting to add "prefer X over Y", it
does not belong here.

---

## General

These apply to every PR, in any language.

### Compatibility contract

The following are contracts with projects already on disk. Changing them
breaks people who upgrade, so they are blocking unless the PR body explains
the migration:

1. **`tasks.json` schema.** Field names are camelCase on the wire
   (`estimateHours`, `specRef`, `dependencies`). A renamed, removed or
   newly-required field is blocking. A new optional field with a safe default
   is fine.
2. **`config.yaml` keys.** A removed or renamed key, or a default that
   changes behaviour for an existing project, is blocking. Flipping a default
   ON that spends money or writes to a remote is always blocking.
3. **SQLite schema and migrations.** Migrations are append-only: an existing
   `NNN_*.sql` file must never be edited, only superseded by a new one. A
   dropped column or table is blocking. A migration that is not idempotent
   is blocking.
4. **CLI surface.** A removed subcommand or flag, or a flag whose meaning
   changes, is blocking.

### Safety

5. **No secrets.** API keys, tokens, passwords, private URLs — in code, in
   tests, in fixtures, in docs, in example config. A string that looks like a
   credential is blocking even if it is expired or fake.
6. **No personal paths.** `/Users/<name>`, `/home/<name>`, `C:\Users\...`,
   or a hostname specific to one machine. Blocking.
7. **No third-party URLs** added as a runtime dependency — an endpoint the
   tool calls, a script piped into a shell, a CDN the dashboard loads from.
   Links in prose and docs are fine. Blocking when it is a runtime call.

### Hygiene

8. **New behaviour has a test.** A new function, branch or error path with no
   test is blocking. A refactor that keeps behaviour identical does not need
   a new test. A bug fix needs a test that fails without the fix.
9. **Docs follow commands.** If a PR adds, removes or changes a command, a
   flag, or a config key, the corresponding doc under `docs/` must change in
   the same PR. Blocking.
10. **Conventional commit.** The PR title must start with one of
    `feat:` `fix:` `test:` `docs:` `chore:` `refactor:` (an optional scope in
    parentheses is fine: `fix(dashboard):`). Minor, not blocking.

---

## Go

These apply to files under `cmd/` and `internal/`.

> The dependency rules below are confirmed (plan "Orch en Go", ADR-G2 and the
> Layout section) and apply from the first Go PR. If a PR has a good reason to
> cross one of these lines, it must say so in the PR body; a crossing without
> that justification is blocking.

### Package dependencies

11. **`internal/model` imports nothing from `internal/`.** It is the leaf:
    `Task`, `Finding`, the DAG, the domain types. A model file importing
    `state`, `engine` or `config` is blocking.
12. **`internal/engine` never imports `internal/cli` or
    `internal/dashboard`.** The engine is driven by them, not the reverse.
    Blocking.
13. **`internal/dashboard` reads state; it never drives the engine.** A
    dashboard file importing `internal/engine` is blocking — the dashboard is
    read-only by design and that is the property it rests on.
14. **`internal/providers` imports `model` and `config`, nothing else from
    `internal/`.** Each provider is a thin adapter over one external CLI.
15. **No import cycles**, and no package importing `cmd/`.

### Correctness

16. **State transitions are legal.** A task moves
    `backlog → todo → in-progress → done | blocked | skipped`. Writing a
    status that skips a step, or moving out of `done`, is blocking unless it
    goes through the explicit transition table.
17. **One writer to SQLite.** Writes go through the single writer path;
    a second `*sql.DB` opened for writing, or a write from a goroutine that
    does not hold the writer, is blocking. Reads may be concurrent.
18. **`os/exec` for a provider sets `SysProcAttr.Setpgid = true` and runs
    under a context with a timeout.** Without the process group, a killed
    agent leaves orphans; without the timeout, a hung CLI blocks the slot
    forever. Either one missing is blocking.
19. **Errors are wrapped with context** (`fmt.Errorf("...: %w", err)`) and
    never silently dropped. A bare `_ = err` on a path that can fail is
    blocking.

### Tests

20. **Table tests for anything with more than two cases** — status
    transitions, router resolution, config merging, path matching. A stack of
    near-identical test functions where a table belongs is minor.
21. **Provider tests use real fixtures** under `internal/providers/testdata/`:
    actual captured CLI output, not a hand-written string that happens to
    parse. A provider parser tested only against invented output is blocking —
    that is exactly the test that passes while the real CLI breaks.
22. **No `time.Sleep` in tests** to wait for a goroutine. Blocking — use a
    channel, a `sync.WaitGroup`, or an injected clock.

---

## How to review

Review **only what is in the diff**. Do not comment on code the PR did not
touch, however tempting.

Flag something as missing **only when a rule above requires it** — a missing
test for new behaviour (rule 8), a missing doc change for a changed command
(rule 9). Do not invent requirements.

**Do not suggest style refactors.** Naming, file organisation, "this could be
a helper", "consider using X instead of Y" — all out of scope. `gofmt` and
the linter own style; you own correctness and the contracts above.

If the diff is clean against every rule, say so in one sentence and return
`approve`. A short review is a good review.
