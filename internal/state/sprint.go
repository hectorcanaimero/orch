package state

import (
	"context"
	"fmt"
)

// The two reads sprint health is built from: how fast the project is going,
// and why the blocked tasks are blocked.

// CountDoneLastNDays counts tasks that FINISHED within the last n days. It is
// the numerator of velocity, and of every ETA the dashboard projects from it.
//
// # A deliberate divergence from Python (bug 21)
//
// Python counts `updated_at >= datetime('now', -N days)`, which is the row's
// mtime, not a completion. A task that finished three weeks ago and was edited
// yesterday counts as finished yesterday; a burst of edits on old rows reads
// as a productive week. Velocity is the one number in the dashboard that
// claims to be a measurement, and it was measuring touches.
//
// This counts `finished_at`, falling back to `updated_at` only where
// `finished_at` is missing — rows written before the column was populated.
// Those rows keep the old, wrong-ish answer because there is nothing better to
// ask them; every row written since is counted by when the work actually
// finished.
//
// `finished_at` holds the MOST RECENT entry into done, not the first (see
// Transition, where the order of the COALESCE arguments is deliberate and
// pinned by a Python-generated fixture). So a task re-opened and finished
// again counts ONCE, on the day it was finished the second time — which is
// the answer that keeps velocity meaning "work completed in this window".
//
// Not fixed in Python: it would move the figures of every project on the
// legacy branch, and the migration is where the corrected version belongs.
//
// The cutoff is computed by SQLite (`datetime('now', '-N days')`) rather than
// in Go, so it compares against the same clock and the same string format the
// rows were written with.
func (b *SQLite) CountDoneLastNDays(ctx context.Context, days int) (int, error) {
	var n int
	err := b.db.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tasks_runtime
		  WHERE project_id = ? AND status = 'done'
		    AND COALESCE(NULLIF(finished_at, ''), updated_at) >= datetime('now', ?)`,
		b.projectID, fmt.Sprintf("-%d days", days)).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count tasks done in the last %d days: %w", days, err)
	}
	return n, nil
}

// LastEventByTask returns the newest event for each of the given task ids.
//
// One query, not one per id: the caller is the sprint page asking about every
// blocked task at once, and a project with thirty blockers would otherwise be
// thirty round trips to render one panel.
//
// An empty id list returns an empty map without touching the database — that
// is a project with nothing blocked, which is the common case and does not
// deserve a query. A nil list means EVERY task, matching Python's
// `task_ids=None`.
//
// "Newest" is by row id, not by timestamp. Two events written in the same
// second are ordered by insertion, and insertion order is the truth about
// which happened last; a timestamp comparison would pick one of them at random.
func (b *SQLite) LastEventByTask(ctx context.Context, taskIDs []string) (map[string]Event, error) {
	if taskIDs != nil && len(taskIDs) == 0 {
		return map[string]Event{}, nil
	}

	q := `SELECT e.id, e.run_id, e.event_type, e.task_id, COALESCE(e.backend, ''),
	             e.ts, COALESCE(e.extra_json, '{}')
	        FROM events e
	        JOIN (SELECT task_id, MAX(id) AS max_id
	                FROM events
	               WHERE project_id = ?%s
	               GROUP BY task_id) newest
	          ON newest.max_id = e.id
	       WHERE e.project_id = ?`
	args := []any{b.projectID}
	filter := ""
	if taskIDs != nil {
		filter = " AND task_id IN (" + placeholders(len(taskIDs)) + ")"
		for _, id := range taskIDs {
			args = append(args, id)
		}
	}
	args = append(args, b.projectID)

	rows, err := b.db.read.QueryContext(ctx, fmt.Sprintf(q, filter), args...)
	if err != nil {
		return nil, fmt.Errorf("query last event per task: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := map[string]Event{}
	for rows.Next() {
		var (
			e     Event
			extra string
		)
		if err := rows.Scan(&e.ID, &e.RunID, &e.EventType, &e.TaskID,
			&e.Backend, &e.TS, &extra); err != nil {
			return nil, fmt.Errorf("scan event row: %w", err)
		}
		e.Extra = decodeExtra(extra)
		out[e.TaskID] = e
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate last events: %w", err)
	}
	return out, nil
}
