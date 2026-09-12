package state

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

// The same-second case, against a REAL database — which is where the
// semantics live. The dashboard's own stream test drives a fake, and a fake
// that indexes by id cannot reproduce the failure this is about; asserting it
// there would be asserting the fake.
//
// Bug 25: Python's tailer remembers the last TIMESTAMP it delivered and polls
// `ts > last_ts`. Event timestamps have second precision, so a row written in
// the same second as the last one delivered — but after the poll that
// delivered it — is skipped and never comes back. A dispatch and the block it
// causes land in the same second all the time.
func TestEventsSinceDeliversRowsSharingATimestamp(t *testing.T) {
	ctx := context.Background()
	b := seeded(t, "T1")
	const sameSecond = "2026-09-11T10:00:00Z"

	for i, etype := range []string{"dispatch", "block", "retry"} {
		if err := b.AppendEvent(ctx, "run-1", Event{
			EventType: etype, TaskID: "T1", Backend: "claude", TS: sameSecond,
			Extra: map[string]any{"pid": fmt.Sprint(i)},
		}); err != nil {
			t.Fatalf("AppendEvent %s: %v", etype, err)
		}
	}

	// A tail that has delivered the first one asks for what came after it.
	all, err := b.EventsSince(ctx, 0, 0)
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("got %d rows, want 3", len(all))
	}

	after := all[0].ID
	rest, err := b.EventsSince(ctx, after, 0)
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
	if len(rest) != 2 {
		t.Fatalf("got %d rows after the first, want 2 — the two sharing its "+
			"timestamp are the ones a ts-keyed tail drops", len(rest))
	}
	if rest[0].EventType != "block" || rest[1].EventType != "retry" {
		t.Errorf("rows = %q, %q; want block then retry", rest[0].EventType, rest[1].EventType)
	}
	// And every row carries the same timestamp, so the test is about the key
	// rather than about rows that happen to be ordered anyway.
	for _, e := range all {
		if e.TS != sameSecond {
			t.Fatalf("row %d has ts %q; the fixture is not exercising the collision", e.ID, e.TS)
		}
	}
}

func TestEventsSinceLimitAndOrder(t *testing.T) {
	ctx := context.Background()
	b := seeded(t, "T1")
	for i := 0; i < 5; i++ {
		if err := b.AppendEvent(ctx, "run-1", Event{
			EventType: "dispatch", TaskID: "T1", Backend: "claude",
			TS:    fmt.Sprintf("2026-09-11T10:00:0%dZ", i),
			Extra: map[string]any{"pid": i},
		}); err != nil {
			t.Fatalf("AppendEvent %d: %v", i, err)
		}
	}

	first, err := b.EventsSince(ctx, 0, 2)
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("got %d rows with limit 2", len(first))
	}
	if first[0].ID >= first[1].ID {
		t.Errorf("rows are not in id order: %d then %d", first[0].ID, first[1].ID)
	}

	// Picking up where that left off covers the rest exactly once — which is
	// what a tail that fell behind does over several polls.
	rest, err := b.EventsSince(ctx, first[1].ID, 0)
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
	if len(rest) != 3 {
		t.Errorf("got %d remaining rows, want 3", len(rest))
	}
}

func TestLatestEventID(t *testing.T) {
	ctx := context.Background()
	b := seeded(t, "T1")

	// A project that has never run has no events, and 0 is where a tail then
	// starts — not an error, and not a nil the caller has to unwrap.
	got, err := b.LatestEventID(ctx)
	if err != nil {
		t.Fatalf("LatestEventID: %v", err)
	}
	if got != 0 {
		t.Errorf("got %d for a project with no events, want 0", got)
	}

	if err := b.AppendEvent(ctx, "run-1", Event{
		EventType: "dispatch", TaskID: "T1", Backend: "claude",
		TS: "2026-09-11T10:00:00Z", Extra: map[string]any{"pid": 1},
	}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	got, err = b.LatestEventID(ctx)
	if err != nil {
		t.Fatalf("LatestEventID: %v", err)
	}
	if got == 0 {
		t.Error("got 0 after appending an event")
	}
	// Nothing is newer than the newest, which is what makes it a safe place
	// for a tail to start.
	after, err := b.EventsSince(ctx, got, 0)
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
	if len(after) != 0 {
		t.Errorf("got %d rows newer than the latest id", len(after))
	}
}

func TestEventsSinceIsScopedToItsProject(t *testing.T) {
	ctx := context.Background()
	db, _, err := Open(ctx, filepath.Join(t.TempDir(), "orch.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close the database: %v", cerr)
		}
	})

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

	got, err := z.EventsSince(ctx, 0, 0)
	if err != nil {
		t.Fatalf("zulu EventsSince: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("zulu sees %d of alpha's events", len(got))
	}
	// And zulu's tail starts at 0 rather than at alpha's newest id, which
	// would have it skip its own first events.
	id, err := z.LatestEventID(ctx)
	if err != nil {
		t.Fatalf("zulu LatestEventID: %v", err)
	}
	if id != 0 {
		t.Errorf("zulu's latest id is %d; alpha's rows are not zulu's", id)
	}
}

// A row whose extra_json is corrupt keeps its event and says so. Dropping the
// event would hide the thing the operator needs; dropping the extra silently
// would leave a reader unable to tell "no extra" from "unreadable extra".
func TestDecodeExtraKeepsTheEventAndNamesTheDamage(t *testing.T) {
	if got := decodeExtra(""); got != nil {
		t.Errorf("an empty extra decoded to %v, want nil", got)
	}
	if got := decodeExtra(`{"pid":42}`); got == nil || got["pid"] != float64(42) {
		t.Errorf("a good extra decoded to %v", got)
	}

	const broken = `{"pid": 42`
	got := decodeExtra(broken)
	if got == nil {
		t.Fatal("a corrupt extra decoded to nil; it is indistinguishable from an absent one")
	}
	if got["malformed_extra_json"] != broken {
		t.Errorf("got %v, want the unparsed text preserved", got)
	}
}
