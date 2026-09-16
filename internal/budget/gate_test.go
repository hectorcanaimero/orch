package budget

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hectorcanaimero/orch/internal/state"
)

// fakeSpend is a SpendReader that answers from a table.
//
// The vector test covers the arithmetic against a real database; this covers
// the paths a database cannot produce — a read that fails, a row with an
// unreadable timestamp — and the short-circuits that must not read at all.
type fakeSpend struct {
	rows map[string][]state.Spend
	err  error
	// calls records every (backend, since) pair, so a test can assert the
	// gate did NOT read rather than only that it answered correctly.
	calls []call
}

type call struct {
	backend string
	since   time.Time
}

func (f *fakeSpend) SpendSince(_ context.Context, backend string, since time.Time) ([]state.Spend, error) {
	f.calls = append(f.calls, call{backend, since})
	if f.err != nil {
		return nil, f.err
	}
	var out []state.Spend
	for _, r := range f.rows[backend] {
		at, ok := state.ParseTS(r.TS)
		if !ok || at.Before(since) {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

func spend(ts string, in, out int) state.Spend {
	return state.Spend{TS: ts, Backend: "claude", TokensIn: in, TokensOut: out}
}

func gateAt(t *testing.T, spendReader SpendReader, cfg *Config, now string) *Gate {
	t.Helper()
	g := NewGate(spendReader, cfg)
	at := mustParse(t, now)
	g.now = func() time.Time { return at }
	return g
}

func oneProvider(pb ProviderBudget) *Config {
	return &Config{Providers: map[string]ProviderBudget{"claude": pb}}
}

// A disabled gate answers without reading. Not just "answers yes": the point
// is that a project with no budgets.yaml does not pay for a query on every
// dispatch, and that a nil backend is safe to hand it.
func TestDisabledGateNeverReads(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  *Config
	}{
		{"nil config", nil},
		{"no providers", &Config{}},
		{"empty provider map", &Config{Providers: map[string]ProviderBudget{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeSpend{err: errors.New("the gate must not have read anything")}
			g := gateAt(t, f, tc.cfg, "2026-09-02T12:00:00Z")
			ctx := context.Background()

			if !g.Disabled() {
				t.Error("Disabled() = false")
			}
			d, err := g.CanDispatch(ctx, "claude")
			if err != nil || !d.OK {
				t.Errorf("CanDispatch = (%+v, %v), want OK with no error", d, err)
			}
			if capped, err := g.AllCapped(ctx); err != nil || capped {
				t.Errorf("AllCapped = (%v, %v), want false", capped, err)
			}
			if reset, err := g.EarliestReset(ctx); err != nil || !reset.IsZero() {
				t.Errorf("EarliestReset = (%v, %v), want the zero time", reset, err)
			}
			snap, err := g.Snapshot(ctx)
			if err != nil || len(snap) != 0 {
				t.Errorf("Snapshot = (%v, %v), want an empty map", snap, err)
			}
			if len(f.calls) != 0 {
				t.Errorf("the gate read %d times while disabled: %v", len(f.calls), f.calls)
			}
		})
	}
}

// An operator who has not configured a budget for a provider has not asked
// orch to ration it. Silently applying some other provider's numbers, or a
// default, would throttle work nobody asked to throttle.
func TestUnknownProviderIsNeverGated(t *testing.T) {
	f := &fakeSpend{err: errors.New("must not read for an unconfigured provider")}
	g := gateAt(t, f, oneProvider(ProviderBudget{
		WindowHours: 5, TokenBudget: 1, ThresholdPct: 1,
	}), "2026-09-02T12:00:00Z")

	d, err := g.CanDispatch(context.Background(), "agy")
	if err != nil {
		t.Fatalf("CanDispatch: %v", err)
	}
	if !d.OK || d.Reason != "" || !d.ResetAt.IsZero() {
		t.Errorf("CanDispatch(agy) = %+v, want a clean yes", d)
	}
	if len(f.calls) != 0 {
		t.Errorf("read %d times for an unconfigured provider", len(f.calls))
	}
}

// The divergence from Python, asserted.
//
// Python catches sqlite3.Error and yields no rows, which makes a failed read
// indistinguishable from zero spend — and zero spend always means "go ahead".
// Here the error comes back on every method, so a caller that wants to
// fail open has to say so.
func TestReadErrorsReachTheCaller(t *testing.T) {
	boom := errors.New("database is locked")
	cfg := oneProvider(ProviderBudget{WindowHours: 5, TokenBudget: 1000, ThresholdPct: 60})
	ctx := context.Background()

	t.Run("CanDispatch", func(t *testing.T) {
		g := gateAt(t, &fakeSpend{err: boom}, cfg, "2026-09-02T12:00:00Z")
		d, err := g.CanDispatch(ctx, "claude")
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want it to wrap %v", err, boom)
		}
		if d.OK {
			t.Error("a failed read must not answer OK — the caller decides that")
		}
	})
	t.Run("AllCapped", func(t *testing.T) {
		g := gateAt(t, &fakeSpend{err: boom}, cfg, "2026-09-02T12:00:00Z")
		if _, err := g.AllCapped(ctx); !errors.Is(err, boom) {
			t.Errorf("err = %v, want it to wrap %v", err, boom)
		}
	})
	t.Run("EarliestReset", func(t *testing.T) {
		g := gateAt(t, &fakeSpend{err: boom}, cfg, "2026-09-02T12:00:00Z")
		if _, err := g.EarliestReset(ctx); !errors.Is(err, boom) {
			t.Errorf("err = %v, want it to wrap %v", err, boom)
		}
	})
	t.Run("Snapshot", func(t *testing.T) {
		g := gateAt(t, &fakeSpend{err: boom}, cfg, "2026-09-02T12:00:00Z")
		if _, err := g.Snapshot(ctx); !errors.Is(err, boom) {
			t.Errorf("err = %v, want it to wrap %v", err, boom)
		}
	})

	// The error names the provider. With four providers configured, "database
	// is locked" alone does not say which window failed to read.
	g := gateAt(t, &fakeSpend{err: boom}, cfg, "2026-09-02T12:00:00Z")
	_, err := g.CanDispatch(ctx, "claude")
	if err == nil || !strings.Contains(err.Error(), "claude") {
		t.Errorf("error %v does not name the provider", err)
	}
}

// The cutoff the gate asks for is `now - window`, and a fractional window is
// not rounded to the hour. A 0.5h window that read six hours of history would
// block on spend that had already fallen out.
func TestCutoffIsNowMinusTheWindow(t *testing.T) {
	f := &fakeSpend{}
	g := gateAt(t, f, oneProvider(ProviderBudget{
		WindowHours: 2.5, TokenBudget: 1000, ThresholdPct: 60,
	}), "2026-09-02T12:00:00Z")

	if _, err := g.CanDispatch(context.Background(), "claude"); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("expected one read, got %d", len(f.calls))
	}
	want := mustParse(t, "2026-09-02T09:30:00Z")
	if !f.calls[0].since.Equal(want) {
		t.Errorf("cutoff = %s, want %s", f.calls[0].since.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

// A row the gate cannot date still counts against the budget; it just cannot
// contribute to the reset estimate.
//
// `state.SpendSince` drops undated rows before they get here, so this is a
// contract about what happens if that ever changes. Counting the tokens is the
// safe half — under-counting spend is how a guardrail stops guarding — and
// leaving `oldest` unset makes the reset fall back to `now`, the soonest
// instant it could be, so the caller re-checks rather than sleeps long.
func TestUndatedRowCountsButDoesNotDateTheReset(t *testing.T) {
	// staticSpend, not fakeSpend: the fake filters by timestamp the way the
	// real backend does, so it would drop this row before the gate saw it.
	raw := []state.Spend{{TS: "not a timestamp", Backend: "claude", TokensIn: 900}}
	g := gateAt(t, &staticSpend{rows: raw}, oneProvider(ProviderBudget{
		WindowHours: 5, TokenBudget: 1000, ThresholdPct: 60,
	}), "2026-09-02T12:00:00Z")

	d, err := g.CanDispatch(context.Background(), "claude")
	if err != nil {
		t.Fatal(err)
	}
	if d.OK {
		t.Fatal("900 tokens against a 600 cap must block, dated or not")
	}
	// now + window, because there was no datable row to age out.
	want := mustParse(t, "2026-09-02T17:00:00Z")
	if !d.ResetAt.Equal(want) {
		t.Errorf("ResetAt = %s, want %s (now + window)",
			d.ResetAt.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

// A dispatch whose provider reported no usage is not free. Its row is marked
// Estimated with zero tokens, and the gate counts it as the configured
// typical dispatch instead of as nothing — otherwise a provider that never
// reports usage could be dispatched to forever under any budget.
func TestUnreportedDispatchCountsAsATypicalOne(t *testing.T) {
	cfg := oneProvider(ProviderBudget{WindowHours: 5, TokenBudget: 1000, ThresholdPct: 60})
	cfg.UnreportedDispatchTokens = 400
	rows := []state.Spend{
		{TS: "2026-09-02T11:00:00Z", Backend: "claude", Estimated: true},
		{TS: "2026-09-02T11:30:00Z", Backend: "claude", Estimated: true},
		// Estimated but with numbers: those numbers are used as they are.
		{TS: "2026-09-02T11:40:00Z", Backend: "claude", Estimated: true, TokensIn: 5},
	}
	g := gateAt(t, &staticSpend{rows: rows}, cfg, "2026-09-02T12:00:00Z")

	d, err := g.CanDispatch(context.Background(), "claude")
	if err != nil {
		t.Fatal(err)
	}
	if d.OK {
		t.Fatal("two unreported dispatches at 400 each (805 tokens) against a 600 cap must block")
	}
	snap, err := g.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap["claude"].TokensUsed != 805 {
		t.Errorf("tokens_used = %d, want 805", snap["claude"].TokensUsed)
	}
}

// staticSpend returns its rows whatever the window, which is what lets a test
// hand the gate a row the real backend would have filtered out.
type staticSpend struct{ rows []state.Spend }

func (s *staticSpend) SpendSince(context.Context, string, time.Time) ([]state.Spend, error) {
	return s.rows, nil
}

// AllCapped stops at the first provider that can still dispatch. Not an
// optimisation to preserve for its own sake — it is what keeps the common
// case (nothing capped) to one query instead of one per provider.
func TestAllCappedStopsAtTheFirstFreeProvider(t *testing.T) {
	f := &fakeSpend{rows: map[string][]state.Spend{
		"codex": {spend("2026-09-02T11:00:00Z", 90_000, 0)},
	}}
	g := gateAt(t, f, &Config{Providers: map[string]ProviderBudget{
		// Sorted order is agy, claude, codex — agy has no rows and is free,
		// so the loop must never reach codex.
		"agy":    {WindowHours: 5, TokenBudget: 1000, ThresholdPct: 60},
		"claude": {WindowHours: 5, TokenBudget: 1000, ThresholdPct: 60},
		"codex":  {WindowHours: 5, TokenBudget: 1000, ThresholdPct: 60},
	}}, "2026-09-02T12:00:00Z")

	capped, err := g.AllCapped(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if capped {
		t.Error("AllCapped = true with a free provider")
	}
	if len(f.calls) != 1 || f.calls[0].backend != "agy" {
		t.Errorf("reads = %v, want one read of agy before short-circuiting", f.calls)
	}
}

// EarliestReset takes the soonest across capped providers — and the doc
// comment's warning is worth a test: it answers even when a provider is free,
// so a caller that sleeps on it without checking AllCapped parks a run that
// could still be dispatching.
func TestEarliestResetAnswersEvenWhenAProviderIsFree(t *testing.T) {
	rows := map[string][]state.Spend{
		"claude": {spend("2026-09-02T11:00:00Z", 90_000, 0)},
		"codex":  {spend("2026-09-02T11:30:00Z", 90_000, 0)},
	}
	g := gateAt(t, &fakeSpend{rows: rows}, &Config{Providers: map[string]ProviderBudget{
		"claude":   {WindowHours: 5, TokenBudget: 1000, ThresholdPct: 60},
		"codex":    {WindowHours: 3, TokenBudget: 1000, ThresholdPct: 60},
		"opencode": {WindowHours: 24, TokenBudget: 1_000_000, ThresholdPct: 60},
	}}, "2026-09-02T12:00:00Z")
	ctx := context.Background()

	capped, err := g.AllCapped(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if capped {
		t.Fatal("opencode is free, so AllCapped must be false")
	}

	reset, err := g.EarliestReset(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// codex: 11:30 + 3h = 14:30. claude: 11:00 + 5h = 16:00. Soonest wins.
	want := mustParse(t, "2026-09-02T14:30:00Z")
	if !reset.Equal(want) {
		t.Errorf("EarliestReset = %s, want %s", reset.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

// Nothing capped, nothing to wait for.
func TestEarliestResetIsZeroWhenNothingIsCapped(t *testing.T) {
	g := gateAt(t, &fakeSpend{}, oneProvider(ProviderBudget{
		WindowHours: 5, TokenBudget: 1000, ThresholdPct: 60,
	}), "2026-09-02T12:00:00Z")

	reset, err := g.EarliestReset(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reset.IsZero() {
		t.Errorf("EarliestReset = %s, want the zero time", reset.Format(time.RFC3339))
	}
}
