package publish

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/hectorcanaimero/orch/internal/publish/snapshot"
)

// DefaultIntervalS is how often `--watch` looks when `publish.interval_s` says
// nothing.
//
// Thirty seconds against a local SQLite file is cheap, and the acceptance bar
// for this feature is "a state change shows up in under two minutes" — which
// has to hold with a git push and a static host's own cache in the budget too,
// so the part orch controls gets a small share of it.
const DefaultIntervalS = 30

// Publisher is what Watch does with a snapshot it decides to publish. Both
// destinations (a directory, a branch) are one of these, and Watch knows about
// neither.
type Publisher func(ctx context.Context, snap snapshot.Snapshot) error

// Watch re-publishes whenever the snapshot's content changes, until ctx ends.
//
// build is called on every tick — it is the thing that reads the database, so
// Watch holds no handle and no state beyond the last digest it published.
//
// The first publish is unconditional: a watch that started against an
// already-current export would otherwise leave nothing on disk until the
// project's next state change, which for a finished project is never.
//
// Content, not time, is what triggers it (see Digest). A tick whose snapshot
// says the same thing writes nothing, which is what keeps `--to git` from
// growing a commit per interval.
func Watch(ctx context.Context, interval time.Duration, build func(context.Context) (snapshot.Snapshot, error),
	publish Publisher, log io.Writer) error {
	if interval <= 0 {
		interval = DefaultIntervalS * time.Second
	}
	start := func() (<-chan time.Time, func()) {
		ticker := time.NewTicker(interval)
		return ticker.C, ticker.Stop
	}
	return watch(ctx, interval, start, build, publish, log)
}

// watch is Watch with the clock handed in. start is called once, after the
// first publish, and returns the tick channel and its stop function — so a
// test drives the loop tick by tick instead of sleeping until a real ticker
// has probably fired. interval is only named in the retry warning.
func watch(ctx context.Context, interval time.Duration, start func() (<-chan time.Time, func()),
	build func(context.Context) (snapshot.Snapshot, error), publish Publisher, log io.Writer) error {
	var last string
	publishNow := func() error {
		snap, err := build(ctx)
		if err != nil {
			return err
		}
		d := Digest(snap)
		if d == last {
			return nil
		}
		if err := publish(ctx, snap); err != nil {
			return err
		}
		last = d
		return nil
	}

	if err := publishNow(); err != nil {
		return err
	}

	ticks, stop := start()
	defer stop()
	for {
		select {
		case <-ctx.Done():
			// Ctrl+C is how this command is meant to end. Returning the
			// cancellation as an error would make `orch publish --watch`
			// exit non-zero on its normal exit.
			return nil
		case <-ticks:
			if err := publishNow(); err != nil {
				// A failed tick does not end the watch: the database is
				// busy, the network blipped, the remote rejected a push
				// because somebody else pushed first. The next tick is
				// thirty seconds away and will try again; a watch that
				// exits on the first transient error is a watch nobody can
				// leave running.
				if log != nil {
					_, _ = fmt.Fprintf(log, "[warn] publish failed, will retry in %s: %v\n", interval, err)
				}
			}
		}
	}
}
