package publish

import (
	"context"
	"errors"
	"io"
	"strings"
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

// startWatch runs watch on a clock the test owns, so no test waits on a real
// ticker. tick hands the loop one tick: the channel is unbuffered, so tick
// returns once the loop has taken it, and since the loop handles one tick at
// a time, every earlier tick has been fully handled by then. stop cancels and
// waits for watch to return, which also waits out the tick in progress.
//
// A watch whose first publish fails returns before it ever reads a tick, so
// calling tick on one would block; that case calls Watch directly instead.
func startWatch(build func(context.Context) (snapshot.Snapshot, error), pub Publisher,
	log io.Writer) (tick func(), stop func() error) {
	ticks := make(chan time.Time)
	clock := func() (<-chan time.Time, func()) { return ticks, func() {} }

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- watch(ctx, time.Second, clock, build, pub, log)
	}()
	return func() { ticks <- time.Time{} },
		func() error { cancel(); return <-done }
}

// No tick at all, and there is already one publish on disk.
func TestWatchPublishesOnceImmediately(t *testing.T) {
	rec := &recorder{}
	_, stop := startWatch(func(context.Context) (snapshot.Snapshot, error) {
		return testSnapshot(), nil
	}, rec.publish, io.Discard)

	if err := stop(); err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if n := rec.count(); n != 1 {
		t.Errorf("published %d times before any tick, want 1", n)
	}
}

// The whole point of the digest: a project that is not moving produces one
// publish, not one per tick.
func TestWatchDoesNotRepublishUnchangedContent(t *testing.T) {
	rec := &recorder{}
	calls := 0
	tick, stop := startWatch(func(context.Context) (snapshot.Snapshot, error) {
		// A fresh generated_at every call, like the real builder. Offset by
		// the call count so two back-to-back calls never share a timestamp:
		// the digest, not a coarse clock, is what has to ignore it.
		calls++
		s := testSnapshot()
		s.GeneratedAt = time.Now().Add(time.Duration(calls)).Format(time.RFC3339Nano)
		return s, nil
	}, rec.publish, io.Discard)

	for range 5 {
		tick()
	}
	if err := stop(); err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if calls != 6 {
		t.Fatalf("built %d snapshots, want 6 (the first publish and five ticks)", calls)
	}
	if n := rec.count(); n != 1 {
		t.Errorf("published %d times; a snapshot that says the same thing should publish once", n)
	}
}

func TestWatchRepublishesWhenTheContentChanges(t *testing.T) {
	rec := &recorder{}
	var mu sync.Mutex
	name := "Primero"

	tick, stop := startWatch(func(context.Context) (snapshot.Snapshot, error) {
		mu.Lock()
		defer mu.Unlock()
		s := testSnapshot()
		s.ProjectName = name
		return s, nil
	}, rec.publish, io.Discard)

	tick() // same content: nothing new to publish
	mu.Lock()
	name = "Segundo"
	mu.Unlock()
	tick() // whichever of the two ticks reads the new name, one of them does
	if err := stop(); err != nil {
		t.Fatalf("Watch: %v", err)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.got) != 2 || rec.got[0] != "Primero" || rec.got[1] != "Segundo" {
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
	calls := 0
	failing := errors.New("transient")
	var log strings.Builder

	tick, stop := startWatch(func(context.Context) (snapshot.Snapshot, error) {
		calls++
		s := testSnapshot()
		s.Summary.Done = calls // always different, so every tick publishes
		return s, nil
	}, func(context.Context, snapshot.Snapshot) error {
		if calls == 1 {
			return nil // the first one succeeds
		}
		return failing
	}, &log)

	for range 3 {
		tick()
	}
	if err := stop(); err != nil {
		t.Errorf("Watch should have kept going and exited cleanly, got %v", err)
	}
	if calls != 4 {
		t.Errorf("built %d snapshots, want 4 (the first publish and three failing ticks)", calls)
	}
	if n := strings.Count(log.String(), "[warn] publish failed"); n != 3 {
		t.Errorf("logged %d retry warnings, want one per failing tick (3):\n%s", n, log.String())
	}
}
