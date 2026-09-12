package publish

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/hectorcanaimero/orch/internal/publish/snapshot"
)

// recorder counts publishes and remembers what was published.
type recorder struct {
	mu   sync.Mutex
	got  []string
	fail error
}

func (r *recorder) publish(_ context.Context, snap snapshot.Snapshot) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail != nil {
		return r.fail
	}
	r.got = append(r.got, snap.ProjectName)
	return nil
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.got)
}

func TestWatchPublishesOnceImmediately(t *testing.T) {
	rec := &recorder{}
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- Watch(ctx, 10*time.Millisecond, func(context.Context) (snapshot.Snapshot, error) {
			return testSnapshot(), nil
		}, rec.publish, io.Discard)
	}()

	waitFor(t, func() bool { return rec.count() >= 1 })
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Watch: %v", err)
	}
}

// The whole point of the digest: a project that is not moving produces one
// publish, not one per tick.
func TestWatchDoesNotRepublishUnchangedContent(t *testing.T) {
	rec := &recorder{}
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- Watch(ctx, time.Millisecond, func(context.Context) (snapshot.Snapshot, error) {
			// A fresh generated_at every call, like the real builder.
			s := testSnapshot()
			s.GeneratedAt = time.Now().Format(time.RFC3339Nano)
			return s, nil
		}, rec.publish, io.Discard)
	}()

	waitFor(t, func() bool { return rec.count() >= 1 })
	time.Sleep(50 * time.Millisecond) // many ticks, all of them identical
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if n := rec.count(); n != 1 {
		t.Errorf("published %d times; a snapshot that says the same thing should publish once", n)
	}
}

func TestWatchRepublishesWhenTheContentChanges(t *testing.T) {
	rec := &recorder{}
	var mu sync.Mutex
	name := "Primero"

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Watch(ctx, time.Millisecond, func(context.Context) (snapshot.Snapshot, error) {
			mu.Lock()
			defer mu.Unlock()
			s := testSnapshot()
			s.ProjectName = name
			return s, nil
		}, rec.publish, io.Discard)
	}()

	waitFor(t, func() bool { return rec.count() >= 1 })
	mu.Lock()
	name = "Segundo"
	mu.Unlock()
	waitFor(t, func() bool { return rec.count() >= 2 })
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Watch: %v", err)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.got[0] != "Primero" || rec.got[1] != "Segundo" {
		t.Errorf("published %v, want [Primero Segundo]", rec.got)
	}
}

// A first publish that fails is a real error: the operator is standing there
// and nothing was written.
func TestWatchFailsOnTheFirstPublish(t *testing.T) {
	boom := errors.New("no")
	rec := &recorder{fail: boom}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := Watch(ctx, time.Millisecond, func(context.Context) (snapshot.Snapshot, error) {
		return testSnapshot(), nil
	}, rec.publish, io.Discard)
	if !errors.Is(err, boom) {
		t.Errorf("Watch = %v, want the publish error", err)
	}
}

// A LATER failure is not: the remote rejected a push, the database was busy.
// A watch that exits on the first transient error is one nobody can leave
// running.
func TestWatchSurvivesALaterFailure(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	failing := errors.New("transient")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Watch(ctx, time.Millisecond, func(context.Context) (snapshot.Snapshot, error) {
			mu.Lock()
			defer mu.Unlock()
			calls++
			s := testSnapshot()
			s.Summary.Done = calls // always different, so every tick publishes
			return s, nil
		}, func(context.Context, snapshot.Snapshot) error {
			mu.Lock()
			defer mu.Unlock()
			if calls == 1 {
				return nil // the first one succeeds
			}
			return failing
		}, io.Discard)
	}()

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return calls > 3
	})
	cancel()
	if err := <-done; err != nil {
		t.Errorf("Watch should have kept going and exited cleanly, got %v", err)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for the watch loop")
}
