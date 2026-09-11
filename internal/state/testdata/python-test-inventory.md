# What the Python state tests specify, and where each case lands in Go

The Python tests are the specification, not the work (artifact §05). This is
the reading pass: every case in `test_sqlite_backend.py` (828 lines),
`test_state.py` (699) and `test_backend_parity.py` (411), sorted into what the
Go port must reproduce, what dies with the file backend, and what Python never
covered.

Delete this file once `internal/state` is done and its tests are the record.

---

## Ports to Go — `test_sqlite_backend.py`

Grouped the way the Go table tests will be grouped, not the way the Python
file is ordered.

### Schema and bootstrap → `migrate_test.go`

| Python case | What it pins |
|---|---|
| `test_schema_bootstrap_sets_user_version` | a fresh DB ends at `user_version` = max migration |
| `test_migration_files_discovered` | every `NNN_*.sql` is found and ordered by N |
| `test_bootstrap_is_idempotent` | re-running `bootstrap` inserts nothing new |
| `test_bootstrap_seeds_tasks_definition` | title, model, deps, spec_ref, phase, estimate, reason, files all land |
| `test_bootstrap_definition_is_ignore_on_rerun` | a second bootstrap never overwrites runtime status |
| — (**new**) | the Python-written fixture opens with **zero** pending migrations |

### Task status → `status_test.go`

| Python case | What it pins |
|---|---|
| `test_task_status_round_trip` | write then read gives the same status |
| `test_task_status_unknown_raises_key_error` | unknown task id is an error, not a silent no-op |
| `test_task_status_illegal_transition_raises_value_error` | the transition table is enforced |
| `test_status_transition_todo_to_done_allowed` | `todo → done` is legal (no forced `in-progress`) |
| `test_set_task_status_backlog_to_done_persists_to_tasks_runtime` | issue #81: manual `orch task set` writes through |
| `test_set_task_status_backlog_to_in_progress_persists` | same, second pair |
| `test_set_task_status_blocked_to_done_persists` | same, unblocking |
| `test_task_status_comments_round_trip` | `comments_json` survives a round trip |
| `test_get_all_task_status_snapshot` | the bulk read matches the per-task reads |
| — (**new**, F-13) | `rowcount == 0` on the UPDATE is an error, never a silent success |

### Multi-project → `project_test.go`

| Python case | What it pins |
|---|---|
| `test_multi_project_isolation` | two project_ids in one DB never see each other's rows |
| — (**new**, F-9) | orphan-row detection finds runtime/definition rows with no `projects` row |

### Runs and dispatches → `run_test.go`

`test_run_create_load_save_round_trip`, `test_add_and_remove_dispatch`,
`test_clear_in_flight_for_run`, `test_list_runs_returns_newest_first`
(ordering is newest-first and the test means it).

### Events and spend → `events_test.go`

| Python case | What it pins |
|---|---|
| `test_events_append_and_iter` | round trip incl. `extra_json` |
| `test_events_dedup_hash_prevents_duplicates` | **the compatibility-critical one** |
| `test_events_iter_since_id` | the dashboard's `id > last_seen` tail |
| `test_spend_append_and_iter` | round trip |
| `test_spend_window_filter` | the rolling window the budget gate reads |
| `test_spend_dedup` | as above, different preimage |
| — (**new**) | the six preimage/SHA-256 vectors in `README.md` |
| — (**new**) | `pyFloat` matches Python's `repr` for the float table in `README.md` |

### Concurrency → `concurrency_test.go`

| Python case | What it pins |
|---|---|
| `test_wal_concurrent_writers_from_two_processes` | two writers, no `SQLITE_BUSY` |
| `test_write_rollback_on_error` | a failed write inside a transaction leaves nothing behind |
| — (**new**) | 50 goroutines transitioning distinct tasks under `-race` |

Python had no race detector; this is the class of bug the artifact calls out
as a top risk, so the Go test is stricter than its source.

### Definition updates → `definition_test.go`

`test_upsert_task_definition_inserts_and_updates`,
`test_set_task_model_updates_definition`,
`test_set_task_backend_updates_definition`, and the two
`..._raises_on_missing_task` cases — same `rowcount == 0` guard as status.

`test_atomize_apply_upserts_tasks_definition` belongs to `internal/atomize`
(G2), not here. Noted so it is not lost.

---

## Ports to Go — `test_backend_parity.py`

Written to run the same assertions against both backends. With the file
backend gone (ADR-G4) the parametrisation collapses and each case becomes an
ordinary SQLite test. The ones that add coverage beyond
`test_sqlite_backend.py`:

- `test_parity_iter_events_task_id_filter` — filter by task
- `test_parity_iter_events_limit_returns_tail` — limit returns the **tail**, not the head
- `test_parity_get_task_last_events_full` / `_whitelist` / `_no_events` — what `orch events <id>` reads
- `test_parity_reconcile_in_flight_smoke` — reconciliation after a crash
- `test_parity_get_unknown_task_returns_none` — read of a missing task is `nil`, not an error (note: the **read** returns nil; the **write** raises. Easy to get backwards.)

**Dropped:** the six `test_parity_findings_*` cases. Findings are cut from the
product (artifact Mapa: *tirar*), and migration `002_findings.sql` is still
applied only so an existing DB's `user_version` lines up.

---

## Ports to Go — `test_state.py`

Most of this file tests the **file** backend (`EventLog`, `SpendLog`,
`RunFile`, JSONL rotation, atomic writes, flock) and dies with it. What
survives:

| Python case | Where it goes |
|---|---|
| `test_event_types_constant_locked` | `internal/model` — the event-type enum is a contract (FR-STATE-7) |
| `test_event_entry_tolerates_legacy_row_without_project_id` | `internal/state` — old rows have no `project_id` |
| `test_event_log_rejects_unknown_type` | `internal/state` — reject on append |
| `test_reconcile_alive_pid_is_adopted` | `internal/state` reconcile |
| `test_reconcile_dead_pid_no_files_reverts` | ditto |
| `test_reconcile_dead_pid_with_dirty_files_adopts_as_done` | ditto — the subtle one |
| `test_reconcile_reverts_in_progress_with_dead_pid` | ditto |
| `test_reconcile_keeps_in_progress_with_alive_pid` | ditto |
| `test_reset_task_in_place_reverts_in_progress_to_todo` | `internal/state` |
| `test_reset_task_in_place_noop_when_not_in_progress` | `internal/state` |

**Dropped with the file backend:** `test_spend_log_rotates_on_utc_date`,
`test_run_file_atomic_write_retries_once_on_rename_failure`,
`test_atomic_write_creates_parent_dirs`, the three `test_acquire_flock_*`,
and the `RunFile` round trips.

**Moves elsewhere:** `test_call_task_start_invokes_script_with_correct_args`,
`test_call_task_finish_and_block_forward_args`,
`test_call_task_reset_prefers_shell_script_when_present`,
`test_call_task_reset_falls_back_when_script_missing` — the `scripts/task-*.sh`
contract, which is `internal/engine` or `internal/cli`, not state.

`test_get_backend_selects_sqlite_from_config` /
`test_get_backend_defaults_to_file` / `test_get_backend_unknown_raises` move to
`internal/config`: in Go there is one backend, so the cases become "`sqlite`
and absent both work, `file` is a clear error naming `orch migrate`".

---

## Count

| | cases |
|---|---|
| Ported to `internal/state` | ~45 |
| New in Go (race, vectors, fixture, rowcount guard, orphans) | 6 |
| Dropped with the file backend | ~12 |
| Dropped with findings | 6 |
| Moved to another package | 6 |
