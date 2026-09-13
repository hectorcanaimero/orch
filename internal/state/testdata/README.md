# `internal/state/testdata`

## `orch-py-0.11.0.db`

A real `orch.db` written by **orch v0.11.0 (Python)**. Go has never touched it.

That is the whole point: the Go tests that open it prove *"a database Python
created opens in Go, with the same schema version and the same rows"* rather
than *"Go agrees with itself"*. ADR-G3 says an existing project migrates by
swapping the binary; this file is what holds us to it.

### What is in it

| Table | Rows | Notes |
|---|---|---|
| `projects` | 1 | `billing-api`, root `/tmp/billing-api` |
| `tasks_definition` | 5 | phases 0–2, deps, estimates, spec refs |
| `tasks_runtime` | 5 | 2 `done`, 1 `in-progress`, 1 `blocked` (with a note), 1 `todo` |
| `events` | **4** | 5 were appended — one was a byte-identical duplicate, dropped by the dedup hash. Types: `dispatch`, `success`, `block` |
| `spend` | 2 | `cost_usd` 0.42 and 1.0, `duration_s` 5400.0 and 1.5 |
| `dispatches` | 1 | in-flight, `F1.T1`, pid 4242 |
| `runs` | 1 | `fixture-run-0001` |
| `milestones` | 1 | `M1`, two tasks assigned, target 2026-09-20 |
| `findings` | 0 | the feature is dropped in Go (ADR mapping: *tirar*) |

`PRAGMA user_version` = **5**, journal mode WAL. Opening it in Go must apply
**zero** migrations.

Every timestamp the tests assert on is fixed. The one exception is
`runs.started_at`, which `create_run` stamps from the clock — nothing asserts
on it.

### Regenerating it

It is frozen now — see *Frozen goldens* at the end. It was made by
`make-fixture.py` in a venv with orch v0.11.0 installed.

Do not regenerate it casually. If the file changes, the compatibility claim it
underwrites changes with it — and a Go-side bug can be "fixed" by quietly
rewriting the fixture, which is exactly the failure this file exists to catch.
Regenerate only when Python's schema genuinely changes, and say so in the PR.

It has been regenerated once, and the rule above is why this paragraph exists.
The first version carried an `exit_ok` event. That is not an event type: it
comes from the superseded FR-STATE-7 list (`docs/history/spec.md:107`) and no
version of orch has ever emitted it, so the fixture was asserting Go could read
a row Python cannot write. It now carries `success`, the name that replaced it,
and `make-fixture.py` checks every event against `EVENT_TYPES` before writing,
so the same mistake cannot be made again by hand. One hash changed with it (the
`success` row below); nothing else in the file moved.

---

## Dedup hash golden vectors

`events` and `spend` de-duplicate on a SHA-256 of a `|`-joined string. Go must
compute it **identically** or two orch binaries sharing one `orch.db` will
write duplicate rows.

Preimages (`orchestrator/state/sqlite_backend.py`, `append_event` /
`append_spend`):

```
event: "{project_id}|{ts}|{task_id}|{event_type}|{run_id}|{pid_hint}"
       pid_hint = extra["pid"] if present, else the empty string

spend: "{project_id}|{ts}|{task_id}|{backend}|{model}|{cost_usd}|{duration_s}"
       project_id = entry.project_id or the backend's own project_id
```

These are the values Python actually produced for the fixture above. They are
the test vectors for the Go implementation:

| Kind | Preimage | SHA-256 |
|---|---|---|
| event | `billing-api\|2026-09-01T09:00:00+00:00\|F0.T1\|dispatch\|fixture-run-0001\|4240` | `772af28dead398ce110c22a5fe8ec1c0dca102bc2a2b9b6aab385e29c350e09b` |
| event | `billing-api\|2026-09-01T10:30:00+00:00\|F0.T1\|success\|fixture-run-0001\|` | `5e6aafc4f8a20f0ad6c781cae04c3c36dcea9e95f30339857de0bac8207f03c3` |
| event | `billing-api\|2026-09-02T11:15:00+00:00\|F1.T1\|dispatch\|fixture-run-0001\|4242` | `6afe96a086be445cbcbc98740c6bbe055ac89a124daa5962da1ef9037ba57497` |
| event | `billing-api\|2026-09-03T14:45:00+00:00\|F1.T2\|block\|fixture-run-0001\|` | `fe6ec73c044b44dc591f33729d2782a5ec3eb7cdbb0620056d249ab251f384cd` |
| spend | `billing-api\|2026-09-01T10:30:00+00:00\|F0.T1\|claude\|claude/claude-sonnet-4-6\|0.42\|5400.0` | `623f4c698b191a2ffa77dc11ce55628be8ef46775985bdde51c2b083f628a2c9` |
| spend | `billing-api\|2026-09-02T11:15:00+00:00\|F0.T2\|claude\|claude/claude-opus-4-6\|1.0\|1.5` | `a7cd159f3edd7730502bbb81ddf5224ebefba00a0843d4016c6734f4f7204ca2` |

### The float trap

`cost_usd` and `duration_s` go into the preimage through a Python f-string,
i.e. `repr(float)`. **Go's default float formatting does not match it.**

| value | Python `f"{v}"` | Go `strconv.FormatFloat(v, 'g', -1, 64)` |
|---|---|---|
| `1.0` | `1.0` | `1` ← **differs** |
| `5400.0` | `5400.0` | `5400` ← **differs** |
| `0.42` | `0.42` | `0.42` |
| `1.5` | `1.5` | `1.5` |
| `0.1+0.2` | `0.30000000000000004` | same |
| `-0.0` | `-0.0` | `-0`  ← **differs** |

Python's rule: shortest decimal that round-trips; a `.0` suffix whenever the
result would otherwise look like an integer; exponent form outside a bounded
range, written as `1e+21` / `1e-07`.

The fixture deliberately contains `cost_usd = 1.0` and `duration_s = 5400.0`
so a naive Go implementation fails the very first comparison rather than
silently diverging in production.

---

## `event-types.json`

The closed set of event types, exported from the Python tree so Go's
`eventTypes` cannot drift from it. `TestEventTypesMatchPython` compares them.

All 21 come from `EVENT_TYPES` (`orchestrator/state/file_backend.py`), which
both `EventLog.emit` and `SqliteEventLog.emit` check before writing. `all` is
sorted for comparison; `declaration_order` preserves the order of the tuple,
which is the order a reader of the Python file sees.

It was generated by `make-event-types.py` and is frozen with the rest. The
script refused to emit a set that has lost one of the seven types bug 9
(#121) declared, or that had grown one of the four superseded FR-STATE-7 names
back. Both are silent failures otherwise: the first shrinks what Go accepts,
the second widens it to rows nothing writes.

---

## `make-fixture.py`

The generator, kept in the repo while the Python tree was, so the fixture was
reproducible and the rows it writes reviewable without opening a binary file.
It imported `orchestrator.*` and was never run by `go test`. With the Python
tree gone the `.db` is a frozen artifact of the format we promised to keep
reading: nothing can regenerate it, and nothing should.

## Frozen goldens

The goldens and fixtures here were generated by running the Python
implementation — `make-fixture.py`, `make-timestamps-vector.py` and `make-event-types.py`, removed from `main` together with the Python tree
(G7.5). They were generated by Python ≤ v0.11.1-py and are now frozen: the
scripts remain in `main`'s history (search it for the script's path; they
import `orchestrator.*`, so they run against a `python-legacy` checkout). Nothing
regenerates them any more, so a Go change that moves one of these files is a
deliberate divergence from the last Python release, edited in the same PR that
says why.
