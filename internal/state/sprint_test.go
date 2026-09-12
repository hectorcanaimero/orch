package state

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/hectorcanaimero/orch/internal/model"
)

// Velocity counts COMPLETIONS, not row touches. This is the divergence from
// Python (bug 21) written as a test, and the two cases below are the two that
// made it worth diverging.
func TestCountDoneLastNDaysCountsCompletionsNotTouches(t *testing.T) {
	ctx := context.Background()
	b := seeded(t, "OLD", "RECENT")

	// Both tasks are done. OLD finished a month ago and its row was touched
	// today; RECENT finished today.
	for _, id := range []string{"OLD", "RECENT"} {
		if err := b.Transition(ctx, id, model.StatusDone, Note{}); err != nil {
			t.Fatalf("transition %s: %v", id, err)
		}
	}
	// Backdate OLD's completion and leave its mtime at now, which is exactly
	// the shape Python miscounts: an old task edited today.
	if _, err := b.db.write.ExecContext(ctx,
		`UPDATE tasks_runtime SET finished_at = datetime('now', '-30 days')
		  WHERE project_id = ? AND task_id = 'OLD'`, b.projectID); err != nil {
		t.Fatalf("backdate OLD: %v", err)
	}

	got, err := b.CountDoneLastNDays(ctx, 7)
	if err != nil {
		t.Fatalf("CountDoneLastNDays: %v", err)
	}
	if got != 1 {
		t.Errorf("counted %d tasks in the last 7 days, want 1 — OLD finished a month "+
			"ago and was only TOUCHED today", got)
	}

	// And the mtime really is recent, so the test is about the column choice
	// rather than about a row nobody touched.
	var updatedAt, finishedAt string
	if err := b.db.read.QueryRowContext(ctx,
		`SELECT updated_at, COALESCE(finished_at, '') FROM tasks_runtime
		  WHERE project_id = ? AND task_id = 'OLD'`, b.projectID).
		Scan(&updatedAt, &finishedAt); err != nil {
		t.Fatalf("read OLD: %v", err)
	}
	if updatedAt <= finishedAt {
		t.Fatalf("OLD's updated_at (%s) is not after its finished_at (%s); "+
			"the test is not exercising the divergence", updatedAt, finishedAt)
	}
}

// A task re-opened and finished again counts ONCE, on the day of the SECOND
// finish. `finished_at` holds the most recent entry into done — the order of
// the COALESCE arguments in Transition is deliberate, and pinned there by a
// fixture generated from the Python backend.
//
// Counting it once is the part that matters for velocity; which of the two
// dates it lands on is the part that would be easy to get wrong in either
// direction, so it is asserted rather than assumed. The legal path back is
// done → todo → done: the transition table has no done → in-progress edge.
func TestCountDoneLastNDaysCountsAReopenedTaskOnceAtTheSecondFinish(t *testing.T) {
	ctx := context.Background()
	b := seeded(t, "T1")

	if err := b.Transition(ctx, "T1", model.StatusDone, Note{}); err != nil {
		t.Fatalf("first done: %v", err)
	}
	if _, err := b.db.write.ExecContext(ctx,
		`UPDATE tasks_runtime SET finished_at = datetime('now', '-30 days')
		  WHERE project_id = ? AND task_id = 'T1'`, b.projectID); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	// Only the backdated finish exists so far, so the window is empty.
	if got, err := b.CountDoneLastNDays(ctx, 7); err != nil || got != 0 {
		t.Fatalf("counted %d (err %v) before the re-finish, want 0", got, err)
	}

	// Re-opened and finished again, today.
	if err := b.Transition(ctx, "T1", model.StatusTodo, Note{}); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if err := b.Transition(ctx, "T1", model.StatusDone, Note{}); err != nil {
		t.Fatalf("second done: %v", err)
	}

	got, err := b.CountDoneLastNDays(ctx, 7)
	if err != nil {
		t.Fatalf("CountDoneLastNDays: %v", err)
	}
	if got != 1 {
		t.Errorf("counted %d, want 1 — one task, credited to its second finish", got)
	}
}

// A row with no finished_at — written before the column was populated — falls
// back to updated_at. There is nothing better to ask it, and dropping it would
// read as a project that has never finished anything.
func TestCountDoneLastNDaysFallsBackToUpdatedAt(t *testing.T) {
	ctx := context.Background()
	b := seeded(t, "T1")

	if err := b.Transition(ctx, "T1", model.StatusDone, Note{}); err != nil {
		t.Fatalf("transition: %v", err)
	}
	if _, err := b.db.write.ExecContext(ctx,
		`UPDATE tasks_runtime SET finished_at = NULL WHERE project_id = ? AND task_id = 'T1'`,
		b.projectID); err != nil {
		t.Fatalf("clear finished_at: %v", err)
	}

	got, err := b.CountDoneLastNDays(ctx, 7)
	if err != nil {
		t.Fatalf("CountDoneLastNDays: %v", err)
	}
	if got != 1 {
		t.Errorf("counted %d, want 1 — a row with no finished_at falls back to updated_at", got)
	}

	// The same row, with an empty string rather than NULL.
	if _, err := b.db.write.ExecContext(ctx,
		`UPDATE tasks_runtime SET finished_at = '' WHERE project_id = ? AND task_id = 'T1'`,
		b.projectID); err != nil {
		t.Fatalf("blank finished_at: %v", err)
	}
	if got, err = b.CountDoneLastNDays(ctx, 7); err != nil || got != 1 {
		t.Errorf("counted %d (err %v), want 1 — an empty finished_at is also 'not set'", got, err)
	}
}

// A task that is not done does not count, however recently it was touched.
func TestCountDoneLastNDaysIgnoresUnfinishedWork(t *testing.T) {
	ctx := context.Background()
	b := seeded(t, "T1")
	if err := b.Transition(ctx, "T1", model.StatusInProgress, Note{}); err != nil {
		t.Fatalf("transition: %v", err)
	}
	if got, err := b.CountDoneLastNDays(ctx, 7); err != nil || got != 0 {
		t.Errorf("counted %d (err %v), want 0", got, err)
	}
}

func TestLastEventByTask(t *testing.T) {
	ctx := context.Background()
	b := seeded(t, "T1", "T2", "T3")

	rows := []struct{ task, etype, ts string }{
		{"T1", "dispatch", "2026-09-11T10:00:00Z"},
		{"T1", "block", "2026-09-11T11:00:00Z"},
		{"T2", "dispatch", "2026-09-11T09:00:00Z"},
	}
	for i, r := range rows {
		if err := b.AppendEvent(ctx, "run-1", Event{
			EventType: r.etype, TaskID: r.task, Backend: "claude", TS: r.ts,
			Extra: map[string]any{"pid": i},
		}); err != nil {
			t.Fatalf("AppendEvent %d: %v", i, err)
		}
	}

	got, err := b.LastEventByTask(ctx, []string{"T1", "T3"})
	if err != nil {
		t.Fatalf("LastEventByTask: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1 — T2 was not asked about and T3 has no events", len(got))
	}
	if got["T1"].EventType != "block" {
		t.Errorf("T1's last event is %q, want the later one (block)", got["T1"].EventType)
	}

	// A nil list means every task.
	all, err := b.LastEventByTask(ctx, nil)
	if err != nil {
		t.Fatalf("LastEventByTask(nil): %v", err)
	}
	if len(all) != 2 {
		t.Errorf("got %d entries for every task, want 2", len(all))
	}

	// An empty list means none, and asks the database nothing.
	none, err := b.LastEventByTask(ctx, []string{})
	if err != nil {
		t.Fatalf("LastEventByTask([]): %v", err)
	}
	if len(none) != 0 {
		t.Errorf("got %d entries for an empty id list", len(none))
	}
}

// Newest is by row id, not by timestamp: two events written in the same second
// are ordered by insertion, and insertion order is the truth about which
// happened last.
func TestLastEventByTaskBreaksTiesByInsertion(t *testing.T) {
	ctx := context.Background()
	b := seeded(t, "T1")
	const sameSecond = "2026-09-11T10:00:00Z"

	for i, etype := range []string{"dispatch", "fail", "block"} {
		if err := b.AppendEvent(ctx, "run-1", Event{
			EventType: etype, TaskID: "T1", Backend: "claude", TS: sameSecond,
			Extra: map[string]any{"pid": fmt.Sprint(i)},
		}); err != nil {
			t.Fatalf("AppendEvent %s: %v", etype, err)
		}
	}

	got, err := b.LastEventByTask(ctx, []string{"T1"})
	if err != nil {
		t.Fatalf("LastEventByTask: %v", err)
	}
	if got["T1"].EventType != "block" {
		t.Errorf("last event is %q, want block — the one written last", got["T1"].EventType)
	}
}

func TestLastEventByTaskIsScopedToItsProject(t *testing.T) {
	ctx := context.Background()
	db, _, err := Open(ctx, filepath.Join(t.TempDir(), "orch.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	a := NewSQLite(db, "alpha", "/tmp/alpha")
	z := NewSQLite(db, "zulu", "/tmp/zulu")
	if err := a.Bootstrap(ctx, tasks("A1")); err != nil {
		t.Fatalf("bootstrap alpha: %v", err)
	}
	if err := z.Bootstrap(ctx, tasks("Z1")); err != nil {
		t.Fatalf("bootstrap zulu: %v", err)
	}

	if err := a.AppendEvent(ctx, "run-a", Event{
		EventType: "block", TaskID: "A1", Backend: "claude",
		TS: "2026-09-11T10:00:00Z", Extra: map[string]any{"pid": 1},
	}); err != nil {
		t.Fatalf("alpha AppendEvent: %v", err)
	}

	got, err := z.LastEventByTask(ctx, nil)
	if err != nil {
		t.Fatalf("zulu LastEventByTask: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("zulu sees %d of alpha's events", len(got))
	}
}
