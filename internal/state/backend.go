package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hectorcanaimero/orch/internal/model"
)

// Backend is the only way anything reads or writes orch's state.
//
// The shape comes from the migration plan's interface sketch, widened with
// what `orch doctor` and `orch init` need. Everything takes a context: a
// dispatch loop draining on SIGINT has to be able to abandon a query rather
// than block on a busy writer.
//
// Nothing here knows about tasks.json. Since F-12 the database is the source
// of truth for runtime status, and tasks.json is read only for the DAG's
// shape — Bootstrap is the one place the two meet.
type Backend interface {
	// Bootstrap seeds `projects`, `tasks_definition` and `tasks_runtime`
	// from tasks.json. Idempotent: re-running never overwrites a runtime
	// status, so it is safe on every startup.
	Bootstrap(ctx context.Context, tasks []model.Task) error

	// Tasks returns the runtime rows matching filter, ordered by task id.
	Tasks(ctx context.Context, filter TaskFilter) ([]TaskRuntime, error)
	// Task returns one task. ErrTaskNotFound when it does not exist.
	Task(ctx context.Context, id string) (TaskRuntime, error)
	// Transition moves a task, enforcing the legal-transition table and
	// recording note in the task's comments.
	Transition(ctx context.Context, id string, to model.Status, note Note) error

	// StartRun opens a run, or leaves an existing one alone. `dispatches`
	// has a foreign key onto `runs`, so this is RecordDispatch's
	// precondition rather than a separate concern.
	StartRun(ctx context.Context, runID, mode string) error
	// RecordDispatch marks a subprocess in flight.
	RecordDispatch(ctx context.Context, d Dispatch) error
	// ClearDispatch removes an in-flight record once the process is reaped.
	ClearDispatch(ctx context.Context, runID, taskID string) error
	// InFlightDispatches returns the dispatches still marked in flight for
	// this project, across runs — what a resumed run reconciles against.
	InFlightDispatches(ctx context.Context) ([]Dispatch, error)

	// TasksWithPendingCI returns the tasks whose PR is open and whose CI has
	// not finished — what the CI poller iterates.
	TasksWithPendingCI(ctx context.Context) ([]TaskRuntime, error)
	// SetTaskPR records a task's pull request and marks its CI pending.
	SetTaskPR(ctx context.Context, taskID, prURL string) error
	// SetTaskCIStatus records how a task's CI finished: pending, success,
	// failure or skipped.
	SetTaskCIStatus(ctx context.Context, taskID, status string) error
	// IncrementCIAttempts bumps the retry counter and returns its new value —
	// the number of the attempt that just started.
	IncrementCIAttempts(ctx context.Context, taskID string) (int, error)
	// RecordSpend appends a completed dispatch's cost, de-duplicated.
	RecordSpend(ctx context.Context, s Spend) error

	// AppendEvent appends one event, de-duplicated.
	AppendEvent(ctx context.Context, runID string, e Event) error
	// Events returns the last n events for a task, oldest first. n <= 0
	// means every event.
	Events(ctx context.Context, taskID string, n int) ([]Event, error)

	// SpendSince returns one backend's spend rows newer than `since`,
	// oldest first — the rolling window the budget gate reads.
	SpendSince(ctx context.Context, backend string, since time.Time) ([]Spend, error)
	// TotalSpendUSD sums cost across every backend since `since`.
	TotalSpendUSD(ctx context.Context, since time.Time) (float64, error)

	// LatestRun returns the most recently started run. ErrNoRuns when the
	// project has never been run.
	LatestRun(ctx context.Context) (Run, error)
	// Runs returns every run, newest first.
	Runs(ctx context.Context) ([]Run, error)

	// Milestones returns every milestone with its progress counts.
	Milestones(ctx context.Context) ([]Milestone, error)

	// OrphanRows reports runtime and definition rows with no project row.
	OrphanRows(ctx context.Context) (OrphanRows, error)
}

// Sentinel errors callers act on. Everything else is wrapped context.
var (
	// ErrTaskNotFound means the id is not in tasks_runtime for this project.
	ErrTaskNotFound = errors.New("task not found")
	// ErrIllegalTransition means the move is not in the transition table.
	// The message names the legal destinations, because the caller is
	// usually a human who just typed a status.
	ErrIllegalTransition = errors.New("illegal transition")
	// ErrUnknownEventType means the event type is outside FR-STATE-7's set.
	ErrUnknownEventType = errors.New("unknown event type")
)

// SQLite implements Backend over one project's orch.db.
//
// A single database can hold several projects; every statement is scoped by
// projectID, and `TestTasksAreScopedToTheProject` is what keeps that true.
type SQLite struct {
	db        *DB
	projectID string
	// projectRoot is recorded in the `projects` row. Informational — it is
	// what `orch doctor` prints when two projects collide on an id.
	projectRoot string
	// now is the clock, injectable so tests do not race real time.
	now func() time.Time
}

// compile-time check that the implementation satisfies the interface.
var _ Backend = (*SQLite)(nil)

// NewSQLite wraps an open database as a project's backend.
func NewSQLite(db *DB, projectID, projectRoot string) *SQLite {
	return &SQLite{
		db:          db,
		projectID:   projectID,
		projectRoot: projectRoot,
		now:         time.Now,
	}
}

// utcNow formats a timestamp the way the shell scripts and the Python
// backend do: `2026-09-11T18:30:00Z`, second precision, always UTC. The
// format is shared with `scripts/task-*.sh`, so it is a contract, not a
// preference.
func (b *SQLite) utcNow() string {
	return b.now().UTC().Format("2006-01-02T15:04:05Z")
}

func (b *SQLite) ts(t time.Time) string {
	if t.IsZero() {
		return b.utcNow()
	}
	return t.UTC().Format("2006-01-02T15:04:05Z")
}

// ---- bootstrap -------------------------------------------------------------

// Bootstrap seeds the project and its tasks.
//
// Every insert is `INSERT OR IGNORE`, which is what makes re-running safe:
// tasks.json's `status` is used only on a task's very first insert. After
// that the database owns it, and a hand-edit of tasks.json cannot silently
// resurrect a finished task.
func (b *SQLite) Bootstrap(ctx context.Context, tasks []model.Task) error {
	now := b.utcNow()
	return b.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO projects
			   (project_id, project_root, created_at, schema_version)
			 VALUES (?, ?, ?, 1)`,
			b.projectID, b.projectRoot, now); err != nil {
			return fmt.Errorf("seed project %q: %w", b.projectID, err)
		}

		for _, t := range tasks {
			status := t.Status
			if _, err := model.ParseStatus(string(status)); err != nil {
				// tasks.json carrying a status the backend cannot hold is a
				// hand-edit, not a crash. Python lands these on "todo".
				status = model.StatusTodo
			}
			comments, err := json.Marshal(rawOrEmpty(t.Comments))
			if err != nil {
				return fmt.Errorf("encode comments for %q: %w", t.ID, err)
			}
			deps, err := json.Marshal(stringsOrEmpty(t.Dependencies))
			if err != nil {
				return fmt.Errorf("encode dependencies for %q: %w", t.ID, err)
			}
			files, err := json.Marshal(stringsOrEmpty(t.Files))
			if err != nil {
				return fmt.Errorf("encode files for %q: %w", t.ID, err)
			}

			if _, err := tx.ExecContext(ctx,
				`INSERT OR IGNORE INTO tasks_runtime
				   (project_id, task_id, status, comments_json, updated_at)
				 VALUES (?, ?, ?, ?, ?)`,
				b.projectID, t.ID, string(status), string(comments), now); err != nil {
				return fmt.Errorf("seed runtime row for %q: %w", t.ID, err)
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT OR IGNORE INTO tasks_definition
				   (project_id, task_id, title, model, backend, deps_json,
				    spec_ref, phase, estimate_h, reason, files_json, updated_at)
				 VALUES (?, ?, ?, ?, NULL, ?, ?, ?, ?, ?, ?, ?)`,
				b.projectID, t.ID, t.Title, t.Model, string(deps), t.SpecRef,
				t.Phase, t.EstimateHours, t.Reason, string(files), now); err != nil {
				return fmt.Errorf("seed definition row for %q: %w", t.ID, err)
			}
		}
		return nil
	})
}

// ---- reads -----------------------------------------------------------------

const taskSelect = `
	SELECT r.task_id, r.status, r.comments_json,
	       COALESCE(r.started_at, ''), COALESCE(r.finished_at, ''), r.updated_at,
	       COALESCE(r.attempts, 0), COALESCE(r.last_model, ''), COALESCE(r.last_backend, ''),
	       COALESCE(r.pr_url, ''), COALESCE(r.ci_status, ''), COALESCE(r.ci_attempts, 0),
	       COALESCE(d.milestone_id, '')
	  FROM tasks_runtime r
	  LEFT JOIN tasks_definition d
	    ON d.project_id = r.project_id AND d.task_id = r.task_id
	 WHERE r.project_id = ?`

func (b *SQLite) Tasks(ctx context.Context, filter TaskFilter) ([]TaskRuntime, error) {
	q := taskSelect
	args := []any{b.projectID}

	if len(filter.Statuses) > 0 {
		q += " AND r.status IN (" + placeholders(len(filter.Statuses)) + ")"
		for _, s := range filter.Statuses {
			args = append(args, string(s))
		}
	}
	if len(filter.IDs) > 0 {
		q += " AND r.task_id IN (" + placeholders(len(filter.IDs)) + ")"
		for _, id := range filter.IDs {
			args = append(args, id)
		}
	}
	if filter.MilestoneID != "" {
		q += " AND d.milestone_id = ?"
		args = append(args, filter.MilestoneID)
	}
	q += " ORDER BY r.task_id"

	rows, err := b.db.read.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query tasks: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []TaskRuntime
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tasks: %w", err)
	}
	return out, nil
}

func (b *SQLite) Task(ctx context.Context, id string) (TaskRuntime, error) {
	rows, err := b.db.read.QueryContext(ctx, taskSelect+" AND r.task_id = ?", b.projectID, id)
	if err != nil {
		return TaskRuntime{}, fmt.Errorf("query task %q: %w", id, err)
	}
	defer func() { _ = rows.Close() }()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return TaskRuntime{}, fmt.Errorf("query task %q: %w", id, err)
		}
		return TaskRuntime{}, fmt.Errorf("%q: %w", id, ErrTaskNotFound)
	}
	return scanTask(rows)
}

func scanTask(rows *sql.Rows) (TaskRuntime, error) {
	var (
		t        TaskRuntime
		status   string
		comments string
	)
	if err := rows.Scan(
		&t.ID, &status, &comments,
		&t.StartedAt, &t.FinishedAt, &t.UpdatedAt,
		&t.Attempts, &t.LastModel, &t.LastBackend,
		&t.PRURL, &t.CIStatus, &t.CIAttempts,
		&t.MilestoneID,
	); err != nil {
		return TaskRuntime{}, fmt.Errorf("scan task row: %w", err)
	}
	t.Status = model.Status(status)
	if comments != "" {
		if err := json.Unmarshal([]byte(comments), &t.Comments); err != nil {
			// A row with unreadable comments is still a usable task. Losing
			// the whole task over a malformed note would be worse than
			// losing the note.
			t.Comments = nil
		}
	}
	return t, nil
}

// ---- transitions -----------------------------------------------------------

// Transition moves a task and records why.
//
// Three things here are load-bearing, all of them learned the hard way:
//
//   - The read and the write are in ONE transaction. A check-then-write
//     across two connections lets a concurrent transition slip between them
//     and produce a move the table forbids.
//   - Legality comes from model.CanTransition, not a table copied into this
//     file. One definition of what is legal, and it is the one verified
//     against Python.
//   - A zero rowcount on the UPDATE is an error (F-13, issue #81). The SELECT
//     above already proved the row exists, so this can only mean it vanished
//     mid-transaction — and the caller must never print success for a write
//     that did nothing.
func (b *SQLite) Transition(ctx context.Context, id string, to model.Status, note Note) error {
	if _, err := model.ParseStatus(string(to)); err != nil {
		return fmt.Errorf("transition %q: %w", id, err)
	}
	at := b.ts(note.At)

	return b.inTx(ctx, func(tx *sql.Tx) error {
		var current, commentsJSON string
		err := tx.QueryRowContext(ctx,
			`SELECT status, COALESCE(comments_json, '[]') FROM tasks_runtime
			  WHERE project_id = ? AND task_id = ?`,
			b.projectID, id).Scan(&current, &commentsJSON)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%q: %w", id, ErrTaskNotFound)
		}
		if err != nil {
			return fmt.Errorf("read current status of %q: %w", id, err)
		}

		from := model.Status(current)
		if !model.CanTransition(from, to) {
			legal := model.Transitions(from)
			names := make([]string, 0, len(legal))
			for _, s := range legal {
				names = append(names, string(s))
			}
			return fmt.Errorf("%q: %w %s -> %s (legal from %s: %s)",
				id, ErrIllegalTransition, from, to, from, strings.Join(names, ", "))
		}

		comments, err := appendComment(commentsJSON, note, to, at)
		if err != nil {
			return fmt.Errorf("append the note for %q: %w", id, err)
		}

		// started_at and finished_at hold the MOST RECENT entry into each
		// state, not the first. The argument comes before the column in
		// COALESCE, so a non-NULL new timestamp wins and a NULL one leaves
		// the stored value alone.
		//
		// The order is not incidental and not a preference: it is what
		// sqlite_backend.py does, captured in
		// testdata/transition-timestamps.json by running the Python backend.
		// `metrics.py` reads finished_at for 7-day velocity and for "done in
		// the last N days", so a task reopened and finished again must report
		// the SECOND finish — otherwise the same project shows different
		// velocity depending on which binary closed the task.
		var started, finished any
		if to == model.StatusInProgress {
			started = at
		}
		if to == model.StatusDone {
			finished = at
		}

		res, err := tx.ExecContext(ctx,
			`UPDATE tasks_runtime
			    SET status = ?, comments_json = ?, updated_at = ?,
			        started_at = COALESCE(?, started_at),
			        finished_at = COALESCE(?, finished_at)
			  WHERE project_id = ? AND task_id = ?`,
			string(to), comments, at, started, finished, b.projectID, id)
		if err != nil {
			return fmt.Errorf("update %q: %w", id, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("check the update of %q: %w", id, err)
		}
		if n == 0 {
			return fmt.Errorf("task %q vanished from tasks_runtime between "+
				"the read and the write: %w", id, ErrTaskNotFound)
		}
		return nil
	})
}

// appendComment adds the transition's note to the task's comment list.
// An empty body records the status name, matching Python — the entry exists
// to say WHEN something moved even when nobody said why.
func appendComment(existing string, note Note, to model.Status, at string) (string, error) {
	var comments []json.RawMessage
	if existing != "" {
		if err := json.Unmarshal([]byte(existing), &comments); err != nil {
			// Do not drop a note because an older one is unreadable; start a
			// fresh list rather than fail the transition.
			comments = nil
		}
	}
	body := note.Body
	if body == "" {
		body = string(to)
	}
	author := note.Author
	if author == "" {
		author = "orch"
	}
	entry, err := json.Marshal(map[string]string{
		"author": author, "body": body, "at": at,
	})
	if err != nil {
		return "", err
	}
	comments = append(comments, entry)
	out, err := json.Marshal(comments)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// ---- dispatches ------------------------------------------------------------

// StartRun opens a run. Idempotent, so resuming an interrupted run with
// `--resume <id>` reattaches to the existing row instead of failing.
//
// It seeds the `projects` row too. Python does the same in `create_run`,
// as a fallback for the case where bootstrap has not run — without it, a
// first dispatch fails on a foreign key with nothing useful in the message.
func (b *SQLite) StartRun(ctx context.Context, runID, mode string) error {
	switch mode {
	case "auto", "semi":
	default:
		// The column has a CHECK constraint; catching it here says which
		// values are legal instead of surfacing a raw constraint error.
		return fmt.Errorf("run mode %q is not one of auto, semi", mode)
	}
	now := b.utcNow()
	return b.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO projects
			   (project_id, project_root, created_at, schema_version)
			 VALUES (?, ?, ?, 1)`,
			b.projectID, b.projectRoot, now); err != nil {
			return fmt.Errorf("seed project for run %q: %w", runID, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO runs
			   (run_id, project_id, started_at, updated_at, mode, parent_pid, status)
			 VALUES (?, ?, ?, ?, ?, 0, 'live')`,
			runID, b.projectID, now, now, mode); err != nil {
			return fmt.Errorf("open run %q: %w", runID, err)
		}
		return nil
	})
}

// RecordDispatch marks a subprocess in flight. StartRun must have been called
// for d.RunID: `dispatches` references `runs`, and without the parent row
// SQLite reports a bare foreign-key failure that says nothing about what is
// missing.
func (b *SQLite) RecordDispatch(ctx context.Context, d Dispatch) error {
	if d.Attempt <= 0 {
		d.Attempt = 1
	}
	_, err := b.db.write.ExecContext(ctx,
		`INSERT OR REPLACE INTO dispatches
		   (run_id, task_id, project_id, backend, pid, session_id,
		    started_at, prompt_path, log_path, output_path, attempt, status)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'in_flight')`,
		d.RunID, d.TaskID, b.projectID, d.Backend, d.PID, d.SessionID,
		d.StartedAt, d.PromptPath, d.LogPath, d.OutputPath, d.Attempt)
	if err != nil {
		return fmt.Errorf("record dispatch of %q: %w", d.TaskID, err)
	}
	return nil
}

func (b *SQLite) ClearDispatch(ctx context.Context, runID, taskID string) error {
	_, err := b.db.write.ExecContext(ctx,
		`DELETE FROM dispatches WHERE run_id = ? AND task_id = ?`, runID, taskID)
	if err != nil {
		return fmt.Errorf("clear dispatch of %q: %w", taskID, err)
	}
	return nil
}

// ---- spend -----------------------------------------------------------------

// RecordSpend appends a cost row, ignoring an exact duplicate.
//
// The dedup hash is the compatibility-critical part — see dedup.go. Replaying
// a run after a crash must not double the reported spend, and the budget gate
// reads these rows.
func (b *SQLite) RecordSpend(ctx context.Context, s Spend) error {
	projectID := s.ProjectID
	if projectID == "" {
		projectID = b.projectID
	}
	estimated := 0
	if s.Estimated {
		estimated = 1
	}
	hash := spendDedupHash(projectID, s.TS, s.TaskID, s.Backend, s.Model, s.CostUSD, s.DurationS)

	_, err := b.db.write.ExecContext(ctx,
		`INSERT OR IGNORE INTO spend
		   (project_id, ts, task_id, backend, model, tokens_in,
		    tokens_out, cost_usd, duration_s, estimated, dedup_hash)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		projectID, s.TS, s.TaskID, s.Backend, s.Model, s.TokensIn,
		s.TokensOut, s.CostUSD, s.DurationS, estimated, hash)
	if err != nil {
		return fmt.Errorf("record spend for %q: %w", s.TaskID, err)
	}
	return nil
}

// ---- events ----------------------------------------------------------------

func (b *SQLite) AppendEvent(ctx context.Context, runID string, e Event) error {
	if !eventTypes[e.EventType] {
		return fmt.Errorf("%w: %q", ErrUnknownEventType, e.EventType)
	}
	extra := e.Extra
	if extra == nil {
		extra = map[string]any{}
	}
	extraJSON, err := json.Marshal(extra)
	if err != nil {
		return fmt.Errorf("encode event extra for %q: %w", e.TaskID, err)
	}
	hash := eventDedupHash(b.projectID, e.TS, e.TaskID, e.EventType, runID, pidHintFromExtra(extra))

	_, err = b.db.write.ExecContext(ctx,
		`INSERT OR IGNORE INTO events
		   (project_id, run_id, event_type, task_id, backend, ts, extra_json, dedup_hash)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		b.projectID, runID, e.EventType, e.TaskID, e.Backend, e.TS,
		string(extraJSON), hash)
	if err != nil {
		return fmt.Errorf("append event for %q: %w", e.TaskID, err)
	}
	return nil
}

// Events returns a task's history oldest-first.
//
// With a limit it returns the TAIL — the most recent n, still in chronological
// order. "Show me the last 5 events" means the newest five, and reading them
// forwards is how a human follows what happened.
func (b *SQLite) Events(ctx context.Context, taskID string, n int) ([]Event, error) {
	q := `SELECT id, run_id, event_type, task_id, COALESCE(backend, ''), ts,
	             COALESCE(extra_json, '{}')
	        FROM events
	       WHERE project_id = ? AND task_id = ?
	       ORDER BY id`
	args := []any{b.projectID, taskID}
	if n > 0 {
		// Take the newest n by ordering backwards, then flip.
		q = strings.Replace(q, "ORDER BY id", "ORDER BY id DESC LIMIT ?", 1)
		args = append(args, n)
	}

	rows, err := b.db.read.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query events for %q: %w", taskID, err)
	}
	defer func() { _ = rows.Close() }()

	var out []Event
	for rows.Next() {
		var (
			e     Event
			extra string
		)
		if err := rows.Scan(&e.ID, &e.RunID, &e.EventType, &e.TaskID,
			&e.Backend, &e.TS, &extra); err != nil {
			return nil, fmt.Errorf("scan event row: %w", err)
		}
		if extra != "" {
			if err := json.Unmarshal([]byte(extra), &e.Extra); err != nil {
				e.Extra = nil
			}
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate events for %q: %w", taskID, err)
	}
	if n > 0 {
		reverse(out)
	}
	return out, nil
}

// ---- milestones ------------------------------------------------------------

func (b *SQLite) Milestones(ctx context.Context) ([]Milestone, error) {
	rows, err := b.db.read.QueryContext(ctx,
		`SELECT m.id, m.title, COALESCE(m.description, ''),
		        COALESCE(m.target_date, ''), COALESCE(m.status, ''), m.created_at,
		        COUNT(td.task_id),
		        COALESCE(SUM(CASE WHEN tr.status = 'done' THEN 1 ELSE 0 END), 0)
		   FROM milestones m
		   LEFT JOIN tasks_definition td
		     ON td.project_id = m.project_id AND td.milestone_id = m.id
		   LEFT JOIN tasks_runtime tr
		     ON tr.project_id = td.project_id AND tr.task_id = td.task_id
		  WHERE m.project_id = ?
		  GROUP BY m.id
		  ORDER BY m.created_at`, b.projectID)
	if err != nil {
		return nil, fmt.Errorf("query milestones: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Milestone
	for rows.Next() {
		var m Milestone
		if err := rows.Scan(&m.ID, &m.Title, &m.Description, &m.TargetDate,
			&m.Status, &m.CreatedAt, &m.Total, &m.Done); err != nil {
			return nil, fmt.Errorf("scan milestone row: %w", err)
		}
		if m.Total > 0 {
			m.PercentDone = m.Done * 100 / m.Total
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate milestones: %w", err)
	}
	return out, nil
}

// ---- migrate ---------------------------------------------------------------
//
// MigratedAt/MarkMigrated are deliberately NOT on the Backend interface,
// unlike Bootstrap/RecordSpend/AppendEvent (which are). The interface
// exists so a caller can test against a double standing in for ANY
// backend; `migrated_at` only means something for a one-shot file→sqlite
// import, which is sqlite-specific by definition — there is no other
// backend a stub implementation of these two would ever stand in for.
// What belongs on Backend is what every backend would have to know how to
// answer; this is what only one of them could.

// MigratedAt returns the project's `projects.migrated_at` timestamp, or
// ("", nil) if the project has no row yet OR has one but was never
// migrated — both are the ordinary "not migrated" case, not an error a
// caller failed to check. A plain read, so it goes through the read pool
// like every other query in this file (rule 17 only constrains writes).
func (b *SQLite) MigratedAt(ctx context.Context) (string, error) {
	var v sql.NullString
	err := b.db.read.QueryRowContext(ctx,
		`SELECT migrated_at FROM projects WHERE project_id = ?`, b.projectID).Scan(&v)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("read migrated_at for %q: %w", b.projectID, err)
	case !v.Valid:
		return "", nil
	default:
		return v.String, nil
	}
}

// MarkMigrated timestamps a one-shot file-to-sqlite import as done. Not
// part of the Backend interface for the same reason as MigratedAt, but a
// genuine method (not a raw connection `orch migrate` opens itself): rule
// 17 requires every write to go through the single writer pool, and this
// is the one write `orch migrate` needs beyond Bootstrap/AppendEvent/
// RecordSpend.
func (b *SQLite) MarkMigrated(ctx context.Context, ts string) error {
	res, err := b.db.write.ExecContext(ctx,
		`UPDATE projects SET migrated_at = ? WHERE project_id = ?`, ts, b.projectID)
	if err != nil {
		return fmt.Errorf("mark %q migrated: %w", b.projectID, err)
	}
	// An UPDATE matching zero rows does not fail on its own — same shape
	// of bug Transition (#107, issue #81) fixed: a missing project_id
	// silently "succeeds" at marking nothing, and the caller believes the
	// import happened. Bootstrap runs before this in every real call path
	// and always seeds the project row, so this should be unreachable in
	// practice; checking it anyway costs one RowsAffected call and turns a
	// silent no-op into a loud one if that invariant is ever broken.
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("check the migrated_at update for %q: %w", b.projectID, err)
	}
	if n == 0 {
		return fmt.Errorf("mark %q migrated: no project row to update — Bootstrap must run first", b.projectID)
	}
	return nil
}

// ---- doctor ----------------------------------------------------------------

// OrphanRows finds rows whose project_id has no row in `projects`.
//
// Read-only by design: it never deletes. Orphans mean somebody edited the
// database by hand or bootstrapped with a version that lacked FK enforcement,
// and in either case the operator has to look at the rows before losing them
// (F-9, issue #73).
func (b *SQLite) OrphanRows(ctx context.Context) (OrphanRows, error) {
	out := OrphanRows{}
	for _, table := range []string{"tasks_runtime", "tasks_definition"} {
		// #nosec G202 -- `table` is from the constant slice on the line above,
		// never from input. SQLite cannot parameterise a table name.
		q := `SELECT project_id, COUNT(*) FROM ` + table + `
		       WHERE project_id NOT IN (SELECT project_id FROM projects)
		       GROUP BY project_id`
		rows, err := b.db.read.QueryContext(ctx, q)
		if err != nil {
			return nil, fmt.Errorf("scan %s for orphans: %w", table, err)
		}
		for rows.Next() {
			var pid string
			var n int
			if err := rows.Scan(&pid, &n); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("scan orphan row in %s: %w", table, err)
			}
			if out[table] == nil {
				out[table] = map[string]int{}
			}
			out[table][pid] = n
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("iterate orphans in %s: %w", table, err)
		}
		_ = rows.Close()
	}
	return out, nil
}

// ---- plumbing --------------------------------------------------------------

// inTx runs fn in a transaction on the single writer connection, rolling back
// on any error. Rollback failures are joined onto the original error rather
// than replacing it: the first error is the one that explains what happened.
func (b *SQLite) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := b.db.write.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			return errors.Join(err, fmt.Errorf("rollback: %w", rbErr))
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func reverse[T any](s []T) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}

// rawOrEmpty and stringsOrEmpty keep `[]` out of the database as `null`.
// Python writes `json.dumps(list(...))`, which is `[]` for an empty list; a
// Go nil slice would marshal to `null` and read back differently.
func rawOrEmpty(v []json.RawMessage) []json.RawMessage {
	if v == nil {
		return []json.RawMessage{}
	}
	return v
}

func stringsOrEmpty(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}
