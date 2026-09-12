package state

import (
	"context"
	"fmt"
)

// EventsSince returns events with an id greater than afterID, oldest first,
// at most limit rows (limit <= 0 means no cap).
//
// This is what a live tail polls. It is keyed on the row ID, not on the
// timestamp, and that is a deliberate divergence from Python — see bug 25 in
// docs/brainstorm/go-migration-notes/opus.md.
//
// # Why not `ts > last_ts`
//
// Event timestamps have SECOND precision (`utcNow`, a format shared with
// scripts/task-*.sh, so it is a contract). Python's SQLite tailer remembers
// the last timestamp it delivered and polls `WHERE ts > last_ts`, which
// silently drops any event written in the same second as the last one
// delivered but after the poll that delivered it. A dispatch and the block it
// causes land in the same second all the time.
//
// The id is monotonic and unique, so `id > afterID` has no such window: every
// row is delivered exactly once, whatever the clock's resolution.
func (b *SQLite) EventsSince(ctx context.Context, afterID int64, limit int) ([]Event, error) {
	q := `SELECT id, run_id, event_type, task_id, COALESCE(backend, ''), ts,
	             COALESCE(extra_json, '{}')
	        FROM events
	       WHERE project_id = ? AND id > ?
	       ORDER BY id`
	args := []any{b.projectID, afterID}
	if limit > 0 {
		q += " LIMIT ?"
		args = append(args, limit)
	}

	rows, err := b.db.read.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query events after %d: %w", afterID, err)
	}
	// Close's error is discarded because `rows.Err()` below is what reports a
	// failed iteration; a Close that fails after a complete read has nothing
	// left to tell a caller. Same form as every other reader in this package.
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
		e.Extra = decodeExtra(extra)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate events after %d: %w", afterID, err)
	}
	return out, nil
}

// LatestEventID is the id of the newest event, or 0 for a project that has
// none.
//
// A live tail starts here so it carries only what happens AFTER the client
// connected. Python starts from an empty timestamp, which replays the entire
// log as "new" on every connect — the SPA then deduplicates it against the
// history it already fetched, and the metrics page invalidates its task query
// once per replayed row.
func (b *SQLite) LatestEventID(ctx context.Context) (int64, error) {
	var id *int64
	err := b.db.read.QueryRowContext(ctx,
		`SELECT MAX(id) FROM events WHERE project_id = ?`, b.projectID).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("read the latest event id: %w", err)
	}
	if id == nil {
		return 0, nil
	}
	return *id, nil
}
