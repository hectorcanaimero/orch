package state

import (
	"context"
	"path/filepath"
	"testing"
)

// AllEvents against a real database, because what it is for is the ordering
// and the tail, and both live in the SQL.
func TestAllEventsIsChronologicalAcrossTasks(t *testing.T) {
	ctx := context.Background()
	b := seeded(t, "T1", "T2")

	// Appended out of order on purpose, and two share a timestamp.
	rows := []struct{ task, etype, ts string }{
		{"T2", "dispatch", "2026-09-11T10:05:00Z"},
		{"T1", "dispatch", "2026-09-11T10:00:00Z"},
		{"T1", "success", "2026-09-11T10:05:00Z"},
		{"T2", "fail", "2026-09-11T10:10:00Z"},
	}
	for i, r := range rows {
		if err := b.AppendEvent(ctx, "run-1", Event{
			EventType: r.etype, TaskID: r.task, Backend: "claude", TS: r.ts,
			Extra: map[string]any{"pid": i},
		}); err != nil {
			t.Fatalf("AppendEvent %d: %v", i, err)
		}
	}

	all, err := b.AllEvents(ctx, 0)
	if err != nil {
		t.Fatalf("AllEvents: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("got %d events, want 4", len(all))
	}
	for i := 1; i < len(all); i++ {
		if all[i].TS < all[i-1].TS {
			t.Fatalf("not chronological: %q after %q", all[i].TS, all[i-1].TS)
		}
	}
	// The two rows sharing 10:05 keep insertion order, which is id order —
	// T2's dispatch was written first.
	if all[1].TaskID != "T2" || all[2].TaskID != "T1" {
		t.Errorf("tie at 10:05 resolved as %s then %s, want T2 then T1",
			all[1].TaskID, all[2].TaskID)
	}
}

// n > 0 is the TAIL, still oldest first. "The last 2 events" read forwards is
// how a person follows what just happened.
func TestAllEventsTailKeepsChronologicalOrder(t *testing.T) {
	ctx := context.Background()
	b := seeded(t, "T1")
	for _, ts := range []string{
		"2026-09-11T10:00:00Z", "2026-09-11T10:01:00Z", "2026-09-11T10:02:00Z",
	} {
		if err := b.AppendEvent(ctx, "run-1", Event{
			EventType: "dispatch", TaskID: "T1", Backend: "claude", TS: ts,
			Extra: map[string]any{"pid": ts},
		}); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
	}

	got, err := b.AllEvents(ctx, 2)
	if err != nil {
		t.Fatalf("AllEvents: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2", len(got))
	}
	if got[0].TS != "2026-09-11T10:01:00Z" || got[1].TS != "2026-09-11T10:02:00Z" {
		t.Errorf("tail = %q, %q; want the newest two, oldest first", got[0].TS, got[1].TS)
	}
}

// A project with no events answers an empty list, not an error. An empty log
// is the normal state of a project nobody has run yet.
func TestAllEventsOnAnUntouchedProject(t *testing.T) {
	got, err := seeded(t, "T1").AllEvents(context.Background(), 0)
	if err != nil {
		t.Fatalf("AllEvents: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d events on a project that has never run", len(got))
	}
}

// Another project's events are another project's. The dashboard reads through
// this and a leak here would be one project's log rendered on another's page.
func TestAllEventsIsScopedToItsProject(t *testing.T) {
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
		EventType: "dispatch", TaskID: "A1", Backend: "claude",
		TS: "2026-09-11T10:00:00Z", Extra: map[string]any{"pid": 1},
	}); err != nil {
		t.Fatalf("alpha AppendEvent: %v", err)
	}

	got, err2 := z.AllEvents(ctx, 0)
	if err2 != nil {
		t.Fatalf("zulu AllEvents: %v", err2)
	}
	if len(got) != 0 {
		t.Errorf("zulu sees %d of alpha's events", len(got))
	}
}
