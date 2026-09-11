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
// EventType is constrained by FR-STATE-7 — see eventTypes. Extra is free-form
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

// eventTypes is the closed set from FR-STATE-7. Appending an unknown type is
// an error rather than a silent write: the dashboard and `orch events` switch
// on these, and a typo would produce a row nothing ever renders.
var eventTypes = map[string]bool{
	"dispatch": true, "exit_ok": true, "exit_err": true,
	"timeout": true, "retry": true, "block": true,
	"resume_adopt": true, "resume_reset": true, "dry_run_planned": true,
	// Sprint F-4 added these two alongside PR tracking.
	"pr_created": true, "id_spoof_detected": true,
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
