package state

import (
	"context"
	"testing"
)

// Ported for `orch migrate` (internal/cli), which needs both methods to go
// through the same reader/writer pools Bootstrap/AppendEvent/RecordSpend
// use — see their doc comments for why they aren't on the Backend
// interface.

func TestMigratedAtEmptyByDefault(t *testing.T) {
	b := seeded(t, "T1")
	at, err := b.MigratedAt(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if at != "" {
		t.Errorf("MigratedAt = %q, want empty before MarkMigrated", at)
	}
}

func TestMarkMigratedThenMigratedAtReturnsIt(t *testing.T) {
	b := seeded(t, "T1")
	ctx := context.Background()
	if err := b.MarkMigrated(ctx, "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	at, err := b.MigratedAt(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if at != "2026-01-01T00:00:00Z" {
		t.Errorf("MigratedAt = %q, want 2026-01-01T00:00:00Z", at)
	}
}

// TestMarkMigratedErrorsWhenProjectRowIsMissing pins the rowcount check:
// an UPDATE matching zero rows must not read as success (same shape of bug
// Transition, #107/issue #81, fixed for tasks_runtime).
func TestMarkMigratedErrorsWhenProjectRowIsMissing(t *testing.T) {
	db, _, err := Open(context.Background(), t.TempDir()+"/orch.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// A backend whose project_id was never Bootstrap-ped — no `projects`
	// row exists for it yet.
	b := NewSQLite(db, "never-bootstrapped", "/tmp/proj")

	if err := b.MarkMigrated(context.Background(), "2026-01-01T00:00:00Z"); err == nil {
		t.Fatal("expected an error when no project row exists to update")
	}
}

func TestMarkMigratedCanBeCalledAgain(t *testing.T) {
	b := seeded(t, "T1")
	ctx := context.Background()
	if err := b.MarkMigrated(ctx, "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := b.MarkMigrated(ctx, "2026-02-02T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	at, err := b.MigratedAt(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if at != "2026-02-02T00:00:00Z" {
		t.Errorf("MigratedAt = %q, want the second call's timestamp", at)
	}
}
