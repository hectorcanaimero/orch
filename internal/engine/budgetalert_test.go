package engine

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/hectorcanaimero/orch/internal/budget"
	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/state"
)

type stubUsage struct {
	snap map[string]budget.ProviderSnapshot
	err  error
}

func (u *stubUsage) Snapshot(context.Context) (map[string]budget.ProviderSnapshot, error) {
	return u.snap, u.err
}

type recordingAlerts struct {
	mu   sync.Mutex
	sent []budget.Alert
}

func (r *recordingAlerts) BudgetAlert(_ context.Context, a budget.Alert) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, a)
}

func (r *recordingAlerts) all() []budget.Alert {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]budget.Alert(nil), r.sent...)
}

// memEvents stands in for the events table: what one run appends, the next
// reads back.
type memEvents struct {
	rows []state.Event
}

func (m *memEvents) Events(_ context.Context, taskID string, _ int) ([]state.Event, error) {
	var out []state.Event
	for _, e := range m.rows {
		if e.TaskID == taskID {
			out = append(out, e)
		}
	}
	return out, nil
}

func (m *memEvents) AppendEvent(_ context.Context, _ string, e state.Event) error {
	m.rows = append(m.rows, e)
	return nil
}

// window builds a claude row: a 100k budget with an 80% threshold, so the cap
// is 80k tokens and 64k is 80% of it.
func window(used int, capped bool) map[string]budget.ProviderSnapshot {
	return map[string]budget.ProviderSnapshot{"claude": {
		TokensUsed: used, TokenBudget: 100000, ThresholdPct: 80, WindowHours: 5, Capped: capped,
	}}
}

var alertNow = time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)

func TestBudgetAlertFiresOncePerWindowWhenUsageCrossesThePct(t *testing.T) {
	usage := &stubUsage{snap: window(60000, false)}
	rec := &recordingAlerts{}
	a := &BudgetAlerts{Usage: usage, Notify: rec, Events: &memEvents{}, Pct: 80}

	a.Check(context.Background(), alertNow, nil)
	if got := len(rec.all()); got != 0 {
		t.Fatalf("75%% of the cap sent %d alerts, want none below 80%%", got)
	}

	usage.snap = window(64000, false)
	a.Check(context.Background(), alertNow.Add(time.Minute), nil)
	a.Check(context.Background(), alertNow.Add(2*time.Minute), nil)
	sent := rec.all()
	if len(sent) != 1 {
		t.Fatalf("sent %d alerts at 80%% of the cap, want exactly 1", len(sent))
	}
	if sent[0].Level != budget.AlertNear || sent[0].Cap != 80000 || sent[0].PctOfCap != 80 {
		t.Errorf("alert = %+v, want near at 80%% of an 80,000 cap", sent[0])
	}

	// A window length later the same usage is a new window's news.
	a.Check(context.Background(), alertNow.Add(5*time.Hour+2*time.Minute), nil)
	if got := len(rec.all()); got != 2 {
		t.Errorf("after the window passed, sent %d alerts in total, want 2", got)
	}
}

func TestBudgetAlertAtTheCapSaysWhenAndWhoIsWaiting(t *testing.T) {
	reset := "2026-09-16T14:05:00Z"
	snap := window(81000, true)
	p := snap["claude"]
	p.ResetAt = &reset
	p.Estimated = true
	snap["claude"] = p
	rec := &recordingAlerts{}
	a := &BudgetAlerts{Usage: &stubUsage{snap: snap}, Notify: rec, Events: &memEvents{}, Pct: 80}

	a.Check(context.Background(), alertNow, map[string]int{"claude": 2, "codex": 1})

	sent := rec.all()
	if len(sent) != 1 {
		t.Fatalf("sent %d alerts, want 1 (capped supersedes near)", len(sent))
	}
	got := sent[0]
	want := time.Date(2026, 9, 16, 14, 5, 0, 0, time.UTC)
	if got.Level != budget.AlertCapped || !got.ResetAt.Equal(want) || got.Waiting != 2 || !got.Estimated {
		t.Errorf("alert = %+v, want capped, reset %v, 2 waiting, estimated", got, want)
	}
}

func TestBudgetAlertIsNotRepeatedByARestartedRun(t *testing.T) {
	events := &memEvents{}
	first := &BudgetAlerts{Usage: &stubUsage{snap: window(81000, true)}, Notify: &recordingAlerts{}, Events: events, Pct: 80}
	first.Check(context.Background(), alertNow, nil)
	if len(events.rows) != 1 || events.rows[0].EventType != EventBudgetAlert {
		t.Fatalf("recorded %+v, want one budget_alert event", events.rows)
	}

	rec := &recordingAlerts{}
	restarted := &BudgetAlerts{Usage: &stubUsage{snap: window(81000, true)}, Notify: rec, Events: events, Pct: 80}
	restarted.Check(context.Background(), alertNow.Add(10*time.Minute), nil)
	if got := len(rec.all()); got != 0 {
		t.Errorf("a restarted run re-sent %d alerts inside the same window", got)
	}

	// The other level has not been announced, so it still goes out.
	restarted.Usage = &stubUsage{snap: window(70000, false)}
	restarted.Check(context.Background(), alertNow.Add(11*time.Minute), nil)
	if got := len(rec.all()); got != 1 {
		t.Errorf("near alert after a capped one: sent %d, want 1", got)
	}
}

func TestBudgetAlertSkipsAFailedReadAndUnconfiguredParts(t *testing.T) {
	rec := &recordingAlerts{}
	(&BudgetAlerts{Usage: &stubUsage{err: errors.New("database is locked")}, Notify: rec, Pct: 80}).
		Check(context.Background(), alertNow, nil)
	(&BudgetAlerts{Usage: &stubUsage{snap: map[string]budget.ProviderSnapshot{"claude": {TokensUsed: 5, WindowHours: 5}}}, Notify: rec, Pct: 80}).
		Check(context.Background(), alertNow, nil)
	var nilAlerts *BudgetAlerts
	nilAlerts.Check(context.Background(), alertNow, nil)
	(&BudgetAlerts{Usage: &stubUsage{snap: window(81000, true)}, Pct: 80}).Check(context.Background(), alertNow, nil)
	if got := len(rec.all()); got != 0 {
		t.Errorf("sent %d alerts from a failed read or a zero cap, want none", got)
	}
}

func TestBudgetWaitingCountsBudgetDeferralsPerProvider(t *testing.T) {
	s := &Scheduler{deferReasons: map[string]string{
		"A": "blocked-by-budget:claude",
		"B": "blocked-by-budget:claude",
		"C": "blocked-by-budget:codex",
		"D": "no-route",
	}}
	got := s.budgetWaiting()
	if len(got) != 2 || got["claude"] != 2 || got["codex"] != 1 {
		t.Errorf("budgetWaiting = %v, want claude:2 codex:1", got)
	}
}

// TestRunnerChecksBudgetAlerts: the loop asks on its first tick, before the
// capped sleep that would skip the rest of it.
func TestRunnerChecksBudgetAlerts(t *testing.T) {
	f := newRunnerFixture(t,
		[]model.Task{task("C-1", 1, "claude/opus")},
		map[string]fakeResponse{"C-1": okResponse()},
		SchedulerOptions{})
	rec := &recordingAlerts{}
	f.r.Alerts = &BudgetAlerts{Usage: &stubUsage{snap: window(64000, false)}, Notify: rec, Pct: 80}

	if code := f.runWithin(t, 30*time.Second); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if got := len(rec.all()); got != 1 {
		t.Errorf("the run sent %d budget alerts, want 1", got)
	}
}
