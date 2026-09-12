package state

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

// The PR and CI columns of `tasks_runtime`, added by migration 005.
//
// `scanTask` has read them since the start and nothing could write them or
// query on them, which made the whole CI-polling path unbuildable. Same gap as
// spend (#116), the run tallies (#130) and in-flight dispatches (#148) — the
// fourth time a set of columns arrived without the methods that use them.
//
// The split is the one the other three settled on: this file answers which
// rows are in which state, and `internal/engine` decides what to do about it.
// Whether to retry, how many times, and what to emit is policy, and policy
// lives with the run loop.

// ciStatuses is the closed set the schema's CHECK constraint enforces
// (migrations/005_pr_ci_tracking.sql) and Python validates against
// (`_ALLOWED_CI_STATUSES`).
//
// Four, not three. `skipped` is the one that gets forgotten — it is how a
// project with `auto_pr` on but no CI configured says "there was nothing to
// wait for", which is a different answer from `success`.
var ciStatuses = map[string]bool{
	"pending": true,
	"success": true,
	"failure": true,
	"skipped": true,
}

// ciStatusNames lists the valid values for an error message.
func ciStatusNames() string {
	out := make([]string, 0, len(ciStatuses))
	for name := range ciStatuses {
		out = append(out, name)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

// TasksWithPendingCI returns the tasks whose PR is open and whose CI has not
// finished, ordered by task id.
//
// Filtered on BOTH columns, which is the part that matters: a task with no PR
// has no CI to look at, and one whose CI already resolved must not come back
// round. Filtering on `ci_status` alone would feed the poller rows with an
// empty `pr_url` and it would ask the forge about a blank URL once per tick.
//
// `pr_url IS NOT NULL AND pr_url != ""` — Python checks only the NULL, which
// is enough there because nothing writes the empty string. The extra clause
// costs nothing and removes a row that would otherwise be a silent no-op
// forever.
func (b *SQLite) TasksWithPendingCI(ctx context.Context) ([]TaskRuntime, error) {
	rows, err := b.db.read.QueryContext(ctx,
		taskSelect+` AND r.pr_url IS NOT NULL AND r.pr_url != ''
		             AND r.ci_status = 'pending'
		    ORDER BY r.task_id`, b.projectID)
	if err != nil {
		return nil, fmt.Errorf("query tasks with pending CI: %w", err)
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
		return nil, fmt.Errorf("iterate tasks with pending CI: %w", err)
	}
	return out, nil
}

// SetTaskPR records the pull request opened for a task AND marks its CI
// pending, in one write.
//
// The second half is not incidental: `ci_status = 'pending'` is what puts the
// task into TasksWithPendingCI's filter, so a version of this that stored only
// the URL would open a PR nobody ever polls. Python does both in the same
// UPDATE for the same reason.
func (b *SQLite) SetTaskPR(ctx context.Context, taskID, prURL string) error {
	if prURL == "" {
		return fmt.Errorf("set PR for %q: the URL is empty", taskID)
	}
	return b.updateTaskRow(ctx, taskID, "set PR",
		`UPDATE tasks_runtime
		    SET pr_url = ?, ci_status = 'pending', updated_at = ?
		  WHERE project_id = ? AND task_id = ?`,
		prURL, b.ts(time.Time{}), b.projectID, taskID)
}

// SetTaskCIStatus records how a task's CI finished.
//
// The value is checked here as well as by the schema's CHECK constraint. The
// constraint would catch it, but as a bare "constraint failed" naming neither
// the column nor what was allowed — and this is a string a caller composes, so
// a typo is the likely way it goes wrong.
func (b *SQLite) SetTaskCIStatus(ctx context.Context, taskID, status string) error {
	if !ciStatuses[status] {
		return fmt.Errorf("set CI status for %q: %q is not one of %s",
			taskID, status, ciStatusNames())
	}
	return b.updateTaskRow(ctx, taskID, "set CI status",
		`UPDATE tasks_runtime SET ci_status = ?, updated_at = ?
		  WHERE project_id = ? AND task_id = ?`,
		status, b.ts(time.Time{}), b.projectID, taskID)
}

// IncrementCIAttempts bumps the retry counter and returns its new value.
//
// Returning the new count is a deliberate difference from Python, which
// returns nothing and leaves the caller to reason about it. The retry branch
// there compares `ci_attempts` BEFORE incrementing and then emits an event
// carrying `ci_attempts + 1` — the number of the attempt just started, not the
// counter as stored. That arithmetic is done by hand at the call site and only
// goes wrong when `ci_max_retries > 1`, which is not the default, so an
// off-by-one there would sit unnoticed.
//
// Handing back the post-increment value removes the hand arithmetic: it IS the
// number of the attempt that just started. The read and the write share one
// transaction, so a concurrent poller cannot see or skip a number.
func (b *SQLite) IncrementCIAttempts(ctx context.Context, taskID string) (int, error) {
	var attempts int
	err := b.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE tasks_runtime
			    SET ci_attempts = COALESCE(ci_attempts, 0) + 1, updated_at = ?
			  WHERE project_id = ? AND task_id = ?`,
			b.ts(time.Time{}), b.projectID, taskID)
		if err != nil {
			return fmt.Errorf("increment CI attempts for %q: %w", taskID, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("increment CI attempts for %q: %w", taskID, err)
		}
		if n == 0 {
			return fmt.Errorf("%q: %w", taskID, ErrTaskNotFound)
		}
		return tx.QueryRowContext(ctx,
			`SELECT COALESCE(ci_attempts, 0) FROM tasks_runtime
			  WHERE project_id = ? AND task_id = ?`,
			b.projectID, taskID).Scan(&attempts)
	})
	if err != nil {
		return 0, err
	}
	return attempts, nil
}

// updateTaskRow runs a single-row UPDATE and turns a rowcount of zero into
// ErrTaskNotFound.
//
// The check is the point. An UPDATE whose WHERE matches nothing succeeds, so
// without it every one of these writes would report success for a task id that
// does not exist — which is issue #81 exactly, the bug `Transition` was fixed
// for in #107.
func (b *SQLite) updateTaskRow(ctx context.Context, taskID, what, query string, args ...any) error {
	res, err := b.db.write.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("%s for %q: %w", what, taskID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s for %q: %w", what, taskID, err)
	}
	if n == 0 {
		return fmt.Errorf("%q: %w", taskID, ErrTaskNotFound)
	}
	return nil
}
