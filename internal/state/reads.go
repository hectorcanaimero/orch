package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

// The read side of `spend` and `runs`.
//
// The Backend could write both before this and read neither, which left
// `orch status --json` reporting `cost_usd: 0.0` and `latest_run: null` on a
// project that had plenty of both. It matched Python only because the parity
// fixture has neither.
//
// The budget gate is the other caller, and the reason `SpendSince` is shaped
// the way it is — see below.

// ErrNoRuns means the project has never been run.
//
// Distinct from an empty result so a caller can tell "nothing has happened
// yet" from "the query found nothing", which read differently to an operator
// looking at `orch status`.
var ErrNoRuns = errors.New("no runs recorded for this project")

// Run is one dispatch session.
type Run struct {
	RunID     string
	StartedAt string
	UpdatedAt string
	Mode      string
	Status    string
	ParentPID int
	// InFlight counts dispatches still marked in_flight for this run. A
	// non-zero value on a run whose process is gone is what the reconciler
	// cleans up after a crash.
	InFlight int

	// The three tallies `runs` keeps as JSON arrays of task ids, reported as
	// lengths because that is all any caller wants — `orch status --json`
	// renders them and nothing reads the ids back out of here.
	//
	// A column holding JSON that does not parse counts as 0 rather than
	// failing the read. Python's `list_runs` lets the `json.loads` raise, so
	// one corrupt column takes down `orch status` entirely; trading "a tally
	// says 0" for "orch cannot tell you anything about this project" is the
	// wrong trade for three numbers that feed no decision. Where these lists
	// DO feed a decision — `blocked_json` in the reconciler — the error has
	// to surface, and that path is not this one.
	CompletedCount int
	BlockedCount   int
	DeferredCount  int
}

// countJSONList returns the length of a JSON array of task ids.
//
// Empty or NULL is 0, matching Python's `json.loads(col or "[]")`. So is
// anything that does not parse, or that parses as something other than an
// array — see the note on Run's tallies for why this does not return an
// error.
func countJSONList(raw string) int {
	if raw == "" {
		return 0
	}
	var ids []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return 0
	}
	return len(ids)
}

// SpendSince returns the spend rows for one backend newer than `since`,
// oldest first.
//
// The signature mirrors what `budget.BudgetGate._entries_since` needs — a
// provider and a cutoff — rather than a general query, because the budget
// gate is the caller that matters and a rolling window is the only shape it
// asks for.
//
// Python reads those rows from `state/spend-<date>.jsonl` and NOT from
// SQLite, which is why its budget gate sees nothing on a sqlite project and
// never trips (reported separately; the fix is in flight on the Python side).
// Go has one source of spend, so the class of bug cannot recur here: there is
// nowhere else for a spend row to hide.
//
// The window is applied in Go, not with `ts >= ?` in SQL. See ParseTS for
// why: the stored timestamps are not in one canonical form, and a lexical
// comparison silently drops rows.
//
// A row with an unparseable or empty ts is EXCLUDED rather than assumed
// recent. Both choices are wrong in some direction — excluding under-reports
// usage, including over-reports it and stalls dispatch on a guess — and this
// is the one Python makes, so the two agree about a database neither wrote
// cleanly.
func (b *SQLite) SpendSince(ctx context.Context, backend string, since time.Time) ([]Spend, error) {
	rows, err := b.db.read.QueryContext(ctx,
		`SELECT project_id, ts, task_id, backend, model, tokens_in, tokens_out,
		        cost_usd, duration_s, COALESCE(estimated, 0)
		   FROM spend
		  WHERE project_id = ? AND backend = ?`,
		b.projectID, backend)
	if err != nil {
		return nil, fmt.Errorf("query spend for %q: %w", backend, err)
	}
	defer func() { _ = rows.Close() }()

	cutoff := since.UTC()
	var out []spendAt
	for rows.Next() {
		var s Spend
		var estimated int
		if err := rows.Scan(&s.ProjectID, &s.TS, &s.TaskID, &s.Backend, &s.Model,
			&s.TokensIn, &s.TokensOut, &s.CostUSD, &s.DurationS, &estimated); err != nil {
			return nil, fmt.Errorf("scan spend row: %w", err)
		}
		s.Estimated = estimated != 0
		ts, ok := ParseTS(s.TS)
		if !ok {
			continue // undated: see the doc comment
		}
		if ts.Before(cutoff) {
			continue
		}
		out = append(out, spendAt{Spend: s, at: ts})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate spend for %q: %w", backend, err)
	}

	sort.Slice(out, func(i, j int) bool {
		if !out[i].at.Equal(out[j].at) {
			return out[i].at.Before(out[j].at)
		}
		return out[i].TaskID < out[j].TaskID
	})
	result := make([]Spend, 0, len(out))
	for _, s := range out {
		result = append(result, s.Spend)
	}
	return result, nil
}

type spendAt struct {
	Spend
	at time.Time
}

// ParseTS reads the two timestamp forms that coexist in a real orch.db.
//
// This is not defensive coding, it is a fact about the file: the same
// database written by one Python version holds `2026-09-01T10:30:00+00:00`
// in `spend.ts` and `2026-09-11T18:32:54Z` in `runs.started_at`, because
// different call sites reach for `datetime.isoformat()` and
// `strftime("...Z")`. Python's `budget._parse_ts` accepts both and says why:
// "robustness with older rows".
//
// It is also why the window is filtered in Go rather than with `ts >= ?` in
// SQL. Lexically, "+" (0x2B) sorts before "Z" (0x5A), so a cutoff formatted
// one way silently drops rows stored the other way — under-reporting spend,
// which is precisely how a budget guardrail stops guarding without any error.
// The spend table is tens of rows, so parsing them is not a cost worth a
// correctness risk.
//
// Exported so `internal/budget` can date the rows it gets back without
// writing a second parser. A second parser is how the two forms would start
// disagreeing again: this is the only place that knows what an orch.db
// actually contains, so it is the only place that should be reading a
// timestamp out of one.
func ParseTS(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05"} {
		if ts, err := time.Parse(layout, s); err == nil {
			return ts.UTC(), true
		}
	}
	return time.Time{}, false
}

// TotalSpendUSD sums cost across every backend since `since`.
//
// Separate from SpendSince because `orch status` wants one number across every
// provider, not a window per provider.
func (b *SQLite) TotalSpendUSD(ctx context.Context, since time.Time) (float64, error) {
	// Summed in Go rather than with SQL's SUM for the same reason SpendSince
	// filters in Go: the cutoff cannot be compared as a string against
	// timestamps stored in two forms. See ParseTS.
	rows, err := b.db.read.QueryContext(ctx,
		`SELECT ts, cost_usd FROM spend WHERE project_id = ?`, b.projectID)
	if err != nil {
		return 0, fmt.Errorf("query spend: %w", err)
	}
	defer func() { _ = rows.Close() }()

	cutoff := since.UTC()
	total := 0.0
	for rows.Next() {
		var ts string
		var cost float64
		if err := rows.Scan(&ts, &cost); err != nil {
			return 0, fmt.Errorf("scan spend row: %w", err)
		}
		at, ok := ParseTS(ts)
		if !ok || at.Before(cutoff) {
			continue
		}
		total += cost
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate spend: %w", err)
	}
	return total, nil
}

// LatestRun returns the most recently started run, with its in-flight count.
//
// ErrNoRuns when the project has never been run.
func (b *SQLite) LatestRun(ctx context.Context) (Run, error) {
	runs, err := b.Runs(ctx)
	if err != nil {
		return Run{}, err
	}
	if len(runs) == 0 {
		return Run{}, ErrNoRuns
	}
	return runs[0], nil
}

// Runs returns every run for the project, newest first — what `orch status`
// and the dashboard list.
//
// Sorted in Go rather than with `ORDER BY started_at DESC`, for the same
// reason SpendSince filters in Go: a lexical sort over mixed timestamp forms
// puts `+00:00` before `Z` regardless of the actual times. Python sorts in
// SQL here and would mis-order such a database; the rows in `runs` happen to
// be written by one call site today, but `spend` is the proof that "one call
// site" is not a guarantee across versions.
//
// run_id breaks a tie. Two runs can share a started_at at second precision,
// and without a second key "latest" would vary between calls and `orch
// status` would flicker between them.
func (b *SQLite) Runs(ctx context.Context) ([]Run, error) {
	rows, err := b.db.read.QueryContext(ctx,
		`SELECT r.run_id, r.started_at, r.updated_at, r.mode, r.status,
		        COALESCE(r.parent_pid, 0),
		        (SELECT COUNT(*) FROM dispatches d
		          WHERE d.run_id = r.run_id AND d.status = 'in_flight'),
		        COALESCE(r.completed_json, ''), COALESCE(r.blocked_json, ''),
		        COALESCE(r.deferred_json, '')
		   FROM runs r
		  WHERE r.project_id = ?`, b.projectID)
	if err != nil {
		return nil, fmt.Errorf("query runs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	type runAt struct {
		Run
		at time.Time
	}
	var all []runAt
	for rows.Next() {
		var r Run
		var completed, blocked, deferred string
		if err := rows.Scan(&r.RunID, &r.StartedAt, &r.UpdatedAt, &r.Mode,
			&r.Status, &r.ParentPID, &r.InFlight,
			&completed, &blocked, &deferred); err != nil {
			return nil, fmt.Errorf("scan run row: %w", err)
		}
		r.CompletedCount = countJSONList(completed)
		r.BlockedCount = countJSONList(blocked)
		r.DeferredCount = countJSONList(deferred)
		// An undated run is kept, unlike an undated spend row: dropping a run
		// would hide a project's history, whereas counting it costs nothing.
		// It sorts last, where an unknown date belongs.
		at, _ := ParseTS(r.StartedAt)
		all = append(all, runAt{Run: r, at: at})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate runs: %w", err)
	}

	sort.Slice(all, func(i, j int) bool {
		if !all[i].at.Equal(all[j].at) {
			return all[i].at.After(all[j].at)
		}
		return all[i].RunID > all[j].RunID
	})
	out := make([]Run, 0, len(all))
	for _, r := range all {
		out = append(out, r.Run)
	}
	return out, nil
}

// InFlightDispatches returns every dispatch this project still has marked in
// flight, across all runs, ordered by run then task.
//
// The read half of RecordDispatch/ClearDispatch. `state.Backend` could write a
// dispatch and clear one but not see one, which is the third time the port has
// landed a writer without its reader — spend and runs were the first two, and
// each hole surfaced weeks later blocking a different lane.
//
// # What this deliberately does not do
//
// It does not probe whether the PIDs are alive, and it does not act on the
// answer. `reconcile_in_flight` in Python does all three inside one method;
// here the aliveness check and the orphan policy live in `internal/engine`,
// which is where the rest of the run loop's policy lives and where the queue
// that reverts a task's status already is. State says which rows exist; the
// engine decides what they mean.
//
// A caller writing that check should know the probe has THREE outcomes, not
// two: ESRCH means the process is gone, EPERM means it is alive and owned by
// another user, and any other error means neither — skip the row rather than
// guess. Reading EPERM as "dead" would revert tasks whose processes are
// running perfectly well.
//
// Rows are returned whatever their PID, including zero. A dispatch recorded
// without a usable PID is a real row that something has to decide about, and
// dropping it here would hide it from the only code that can.
func (b *SQLite) InFlightDispatches(ctx context.Context) ([]Dispatch, error) {
	rows, err := b.db.read.QueryContext(ctx,
		`SELECT run_id, task_id, backend, COALESCE(pid, 0),
		        COALESCE(session_id, ''), COALESCE(started_at, ''),
		        COALESCE(prompt_path, ''), COALESCE(log_path, ''),
		        COALESCE(output_path, ''), COALESCE(attempt, 1)
		   FROM dispatches
		  WHERE project_id = ? AND status = 'in_flight'`, b.projectID)
	if err != nil {
		return nil, fmt.Errorf("query in-flight dispatches: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Dispatch
	for rows.Next() {
		var d Dispatch
		if err := rows.Scan(&d.RunID, &d.TaskID, &d.Backend, &d.PID,
			&d.SessionID, &d.StartedAt, &d.PromptPath, &d.LogPath,
			&d.OutputPath, &d.Attempt); err != nil {
			return nil, fmt.Errorf("scan dispatch row: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate in-flight dispatches: %w", err)
	}

	// Sorted in Go, not with ORDER BY. Not the timestamp hazard this time —
	// these are ids — but the same principle: a caller that logs or reports
	// this list wants the same order every run, and SQLite promises none
	// without an ORDER BY.
	sort.Slice(out, func(i, j int) bool {
		if out[i].RunID != out[j].RunID {
			return out[i].RunID < out[j].RunID
		}
		return out[i].TaskID < out[j].TaskID
	})
	return out, nil
}
