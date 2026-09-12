package state

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// AllEvents returns events across every task, oldest first. n <= 0 means all;
// n > 0 returns the TAIL — the newest n, still in chronological order.
//
// `Events` answers "what happened to this task"; this answers "what happened",
// which is the question a log view and any per-task aggregate both start from.
// Doing it as one query rather than a loop over `Events` matters at both ends:
// a project with 400 tasks is 400 round trips, and a log view that fetched
// per task could not order the result without reading everything anyway.
//
// # Ordering
//
// By `ts`, then by `id`. Python sorts its merged JSONL + SQLite rows with
// `sort(key=ts)`, which is stable, so rows sharing a timestamp keep the order
// they were read in — insertion order, which is `id`. Two events inside the
// same second are not rare: a dispatch and its block land together all the
// time. Ordering by `id` alone would be right for one project and wrong for
// one that migrated from JSONL, where ids were assigned at import.
func (b *SQLite) AllEvents(ctx context.Context, n int) ([]Event, error) {
	q := `SELECT id, run_id, event_type, task_id, COALESCE(backend, ''), ts,
	             COALESCE(extra_json, '{}')
	        FROM events
	       WHERE project_id = ?
	       ORDER BY ts, id`
	args := []any{b.projectID}
	if n > 0 {
		q = strings.Replace(q, "ORDER BY ts, id", "ORDER BY ts DESC, id DESC LIMIT ?", 1)
		args = append(args, n)
	}

	rows, err := b.db.read.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
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
			// A row whose extra_json does not parse loses its extra, not its
			// event. `Events` does the same: the row is still evidence that
			// something happened, and dropping it would hide that.
			if err := json.Unmarshal([]byte(extra), &e.Extra); err != nil {
				e.Extra = nil
			}
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate events: %w", err)
	}
	if n > 0 {
		reverse(out)
	}
	return out, nil
}
