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
