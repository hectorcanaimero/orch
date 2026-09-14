package state

import (
	"encoding/json"
	"time"

	"github.com/hectorcanaimero/orch/internal/model"
)

// The types here are persistence types, not domain types. They live in state
// rather than model on purpose: a Task means something without a database, a
// Spend with its dedup hash does not. `model` stays the leaf that everyone
// imports.

// TaskRuntime is a task's mutable half — what the database owns. The static
// half (title, deps, estimate) lives in `tasks_definition` and mirrors
// tasks.json.
//
// Since the SQLite backend became the source of truth (F-12), tasks.json is
// read for the DAG's shape and nothing else: its `status` field is ignored on
// every load after the first bootstrap.
type TaskRuntime struct {
	ID     string
	Status model.Status
	// Comments are kept as raw JSON, matching model.Task: Python types them
	// `list[dict]` with no fixed shape, so parsing them would only create a
	// way to lose data.
	Comments []json.RawMessage

	StartedAt  string // first transition into in-progress; empty if never
	FinishedAt string // first transition into done; empty if never
	UpdatedAt  string

	Attempts    int
	LastModel   string
	LastBackend string

	// PR tracking (migration 005). Empty when the task has no PR.
	PRURL      string
	CIStatus   string
	CIAttempts int

	MilestoneID string
}

// TaskFilter narrows a Tasks query. A zero filter returns every task in the
// project.
type TaskFilter struct {
	// Statuses, when non-empty, restricts to those statuses.
	Statuses []model.Status
	// IDs, when non-empty, restricts to those task ids.
	IDs []string
	// MilestoneID, when non-empty, restricts to one milestone.
	MilestoneID string
}

// Note is the trail a transition leaves. It becomes an entry in the task's
// `comments_json`, which is what `orch events` and the dashboard read back
// to explain why a task moved.
type Note struct {
	// Author is who moved it: "orch" for the engine, a CLI name for an
	// agent, a person's handle for a manual `orch task set`.
	Author string
	// Body is free text. Empty is allowed and means "no explanation"; the
	// status name is recorded in its place, matching Python.
	Body string
	// At is the timestamp. Zero means "now", resolved by the backend so a
	// caller never has to reach for a clock.
	At time.Time
}

// Dispatch is one in-flight subprocess.
type Dispatch struct {
	RunID      string
	TaskID     string
	Backend    string
	PID        int
	SessionID  string
	StartedAt  string
	PromptPath string
	LogPath    string
	OutputPath string
	Attempt    int
}

// Spend is one completed dispatch's cost.
//
// CostUSD and DurationS feed the dedup hash through pyFloat, so their exact
// float values matter beyond arithmetic — see dedup.go.
type Spend struct {
	ProjectID string // empty means "this backend's project"
	TS        string
	TaskID    string
	Backend   string
	Model     string
	TokensIn  int
	TokensOut int
	CostUSD   float64
	DurationS float64
	// Estimated marks a row whose token counts orch inferred because the
	// provider reported none. Downstream surfaces flag it so nobody reads
	// an invented number as telemetry.
	Estimated bool
}

// Event is one line of a run's history.
//
// EventType is constrained to a closed set — see eventTypes. Extra is free-form
// per type; the `pid` key in a dispatch event feeds the dedup hash.
type Event struct {
	ID        int64
	RunID     string
	EventType string
	TaskID    string
	Backend   string
	TS        string
	Extra     map[string]any
}

// eventTypes is the closed set an event must belong to. Appending an unknown
// type is an error rather than a silent write: the dashboard and `orch events`
// switch on these, and a typo would produce a row nothing ever renders.
//
// The set is Python's, not the plan's. FR-STATE-7 in `docs/history/spec.md`
// lists `exit_ok`, `exit_err`, `resume_reset` and `dry_run_planned`; the note
// at spec.md:107 records that those names were superseded on 2026-08-19 —
// `success`/`fail` replaced the first two, `resume_revert` the third, and
// `dry_run_planned` went away because dry-run writes nothing to disk. An
// earlier version of this file took the stale list, which would have made a
// Go binary reject four types Python emits and accept four nothing produces.
//
// The 21 are `EVENT_TYPES` in `orchestrator/state/file_backend.py`, which is
// what both EventLog.emit and SqliteEventLog.emit validate against. The last
// seven arrived with bug 9 (#121): `orch.py` had been emitting them against
// its own validator, so every PR/CI event raised ValueError at the emit site
// and the run history lost them with no error anywhere.
//
// `testdata/event-types.json` is exported from the Python tree — not
// transcribed — and TestEventTypesMatchPython holds this map to it in both
// directions.
var eventTypes = map[string]bool{
	"dispatch": true, "success": true, "fail": true, "block": true,
	"timeout": true, "retry": true, "escalate": true,
	"resume_adopt": true, "resume_revert": true,
	"id_spoof_detected": true, "flock_contention": true,
	"reconciled": true, "budget_pause": true, "budget_skip": true,
	// Bug 9 (#121): the PR/CI path in orch.py.
	"pr_created": true, "ci_redispatch": true, "ci_success": true,
	"pr_auto_merged": true, "pr_auto_merge_failed": true,
	"ci_failure_retry": true, "ci_blocked": true,
	// Go only. Python declares no such type and never emits one — see
	// goOnlyEventTypes in eventtypes_test.go for why that is safe in a
	// database both binaries write to.
	"sprint_done": true, "ci_no_checks": true,
}

// Milestone groups tasks for the stakeholder view, with the progress counts
// computed from its tasks rather than stored.
type Milestone struct {
	ID          string
	Title       string
	Description string
	TargetDate  string
	Status      string
	CreatedAt   string
	Total       int
	Done        int
	// PercentDone is Done/Total as a whole number, 0 when Total is 0.
	PercentDone int
}

// OrphanRows reports rows whose project_id has no row in `projects`, keyed by
// table and then by project_id.
//
// With `PRAGMA foreign_keys = ON` this should be impossible, but it has been
// seen after direct sqlite3-CLI edits and after a bootstrap from a version
// that predated FK enforcement (F-9, issue #73). Orphaned runtime rows make a
// DAG look stalled with no error anywhere, so doctor reports them — and never
// deletes them, because the operator has to look first.
type OrphanRows map[string]map[string]int

// Total counts every orphan row across every table.
func (o OrphanRows) Total() int {
	n := 0
	for _, byProject := range o {
		for _, count := range byProject {
			n += count
		}
	}
	return n
}

// decodeExtra turns an event row's `extra_json` into a map.
//
// A row whose extra does not parse keeps the EVENT and loses the structure:
// the row is evidence that something happened, and `extra` is decoration on
// top of it. Dropping the whole event because its decoration is corrupt would
// hide the thing the operator needs.
//
// What it does not do is lose it silently. The unparsed text is preserved
// under `malformed_extra_json`, so a log view shows the broken value instead
// of an empty object, and nothing downstream has to guess whether the extra
// was absent or unreadable. Python drops it without trace; this is a
// deliberate, one-key divergence on a path that only a corrupt row reaches.
func decodeExtra(raw string) map[string]any {
	if raw == "" {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return map[string]any{"malformed_extra_json": raw}
	}
	return out
}
