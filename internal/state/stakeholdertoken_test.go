package state

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestStakeholderTokenNotFoundByDefault(t *testing.T) {
	b := seeded(t, "T1")
	hash, _, ok, err := b.StakeholderToken(context.Background())
	if err != nil {
		t.Fatalf("StakeholderToken: %v", err)
	}
	if ok {
		t.Errorf("ok = true on a project that never rotated one (hash=%q), want false", hash)
	}
}

func TestSetStakeholderTokenThenGetReturnsIt(t *testing.T) {
	b := seeded(t, "T1")
	ctx := context.Background()
	rotated := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	if err := b.SetStakeholderToken(ctx, "abc123", rotated); err != nil {
		t.Fatalf("SetStakeholderToken: %v", err)
	}
	hash, gotRotated, ok, err := b.StakeholderToken(ctx)
	if err != nil {
		t.Fatalf("StakeholderToken: %v", err)
	}
	if !ok {
		t.Fatal("ok = false right after SetStakeholderToken")
	}
	if hash != "abc123" {
		t.Errorf("hash = %q, want abc123", hash)
	}
	if !gotRotated.Equal(rotated) {
		t.Errorf("rotatedAt = %v, want %v", gotRotated, rotated)
	}
}

// A rotation replaces the previous row rather than erroring on it or
// leaving both around — `orch dashboard token rotate` calls this on every
// rotation, including the second and third.
func TestSetStakeholderTokenCanBeRotatedAgain(t *testing.T) {
	b := seeded(t, "T1")
	ctx := context.Background()

	if err := b.SetStakeholderToken(ctx, "first-hash", time.Now()); err != nil {
		t.Fatalf("first rotation: %v", err)
	}
	if err := b.SetStakeholderToken(ctx, "second-hash", time.Now()); err != nil {
		t.Fatalf("second rotation: %v", err)
	}
	hash, _, ok, err := b.StakeholderToken(ctx)
	if err != nil {
		t.Fatalf("StakeholderToken: %v", err)
	}
	if !ok || hash != "second-hash" {
		t.Errorf("hash = %q, ok = %v, want second-hash/true", hash, ok)
	}
}

// A corrupt rotated_at is surfaced as an error, not silently swallowed into
// a zero time.Time — this row is written only by SetStakeholderToken, using
// the same b.ts format everything else here does, so a value ParseTS can't
// read means something wrote outside that path, which is worth knowing
// about rather than reporting as if it had never been rotated at all.
func TestStakeholderTokenErrorsOnUnparsableRotatedAt(t *testing.T) {
	b := seeded(t, "T1")
	ctx := context.Background()

	if err := b.SetStakeholderToken(ctx, "some-hash", time.Now()); err != nil {
		t.Fatalf("SetStakeholderToken: %v", err)
	}
	if _, err := b.db.write.ExecContext(ctx,
		`UPDATE stakeholder_tokens SET rotated_at = 'not-a-timestamp' WHERE project_id = ?`,
		b.projectID); err != nil {
		t.Fatalf("corrupt rotated_at: %v", err)
	}

	_, _, ok, err := b.StakeholderToken(ctx)
	if err == nil {
		t.Fatal("StakeholderToken with an unparsable rotated_at returned no error")
	}
	if ok {
		t.Error("ok = true alongside an error")
	}
}

func TestSetStakeholderTokenRejectsEmptyHash(t *testing.T) {
	b := seeded(t, "T1")
	if err := b.SetStakeholderToken(context.Background(), "", time.Now()); err == nil {
		t.Fatal("SetStakeholderToken(\"\") succeeded, want an error")
	}
}

// The test G8.2/F3.3 exists to pass: two projects, two tokens, one does not
// open the other. Both backends share ONE database file — the shape a
// future --portfolio mode would use — so this proves project_id scoping,
// not just "two separate files happen not to collide."
func TestStakeholderTokenIsScopedToTheProject(t *testing.T) {
	ctx := context.Background()
	db, _, err := Open(ctx, filepath.Join(t.TempDir(), "orch.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	projectA := NewSQLite(db, "project-a", "/tmp/project-a")
	projectB := NewSQLite(db, "project-b", "/tmp/project-b")
	if err := projectA.Bootstrap(ctx, tasks("T1")); err != nil {
		t.Fatalf("bootstrap project-a: %v", err)
	}
	if err := projectB.Bootstrap(ctx, tasks("T1")); err != nil {
		t.Fatalf("bootstrap project-b: %v", err)
	}

	if err := projectA.SetStakeholderToken(ctx, "hash-for-a", time.Now()); err != nil {
		t.Fatalf("set project-a's token: %v", err)
	}
	if err := projectB.SetStakeholderToken(ctx, "hash-for-b", time.Now()); err != nil {
		t.Fatalf("set project-b's token: %v", err)
	}

	gotA, _, okA, err := projectA.StakeholderToken(ctx)
	if err != nil || !okA {
		t.Fatalf("project-a's token: ok=%v err=%v", okA, err)
	}
	if gotA != "hash-for-a" {
		t.Errorf("project-a resolved %q, want hash-for-a", gotA)
	}

	gotB, _, okB, err := projectB.StakeholderToken(ctx)
	if err != nil || !okB {
		t.Fatalf("project-b's token: ok=%v err=%v", okB, err)
	}
	if gotB != "hash-for-b" {
		t.Errorf("project-b resolved %q, want hash-for-b", gotB)
	}

	if gotA == gotB {
		t.Fatal("project-a and project-b resolved the same token hash — tenant isolation broken")
	}
}
