package engine

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/hectorcanaimero/orch/internal/budget"
	"github.com/hectorcanaimero/orch/internal/state"
)

// EventBudgetAlert marks a budget alert sent to the notification channels,
// so a restarted run does not send it again. Go only, like sprint_done.
const EventBudgetAlert = "budget_alert"

// budgetAlertInterval is how often the run re-reads the windows for alerts.
// Usage only grows when a dispatch is reaped, so a minute late is on time.
const budgetAlertInterval = time.Minute

// DefaultBudgetAlertPct is the share of a provider's cap at which the first
// alert goes out, when config does not say.
const DefaultBudgetAlertPct = 80.0

// BudgetUsage is the part of budget.Gate the alerts read.
type BudgetUsage interface {
	Snapshot(ctx context.Context) (map[string]budget.ProviderSnapshot, error)
}

// BudgetNotifier is where the alerts go. No error, like Notifier: a broken
// webhook never reaches the loop.
type BudgetNotifier interface {
	BudgetAlert(ctx context.Context, a budget.Alert)
}

// AlertEvents reads and writes the budget_alert events that make an alert
// once per window even across restarts. state.Backend and StateRecorder
// between them satisfy it; see NewBudgetAlerts.
type AlertEvents interface {
	Events(ctx context.Context, taskID string, n int) ([]state.Event, error)
	AppendEvent(ctx context.Context, runID string, e state.Event) error
}

// BudgetAlerts sends one message when a provider's weighted window crosses
// Pct of its cap, and one when it reaches the cap and the gate starts
// deferring — each at most once per window length per provider.
//
// ponytail: "once per window length" rather than tracking when usage drops
// back below the line, so a provider that dips and climbs again inside one
// window is not re-announced. Track the dip if that turns out to matter.
type BudgetAlerts struct {
	Usage  BudgetUsage
	Notify BudgetNotifier
	Events AlertEvents
	RunID  string
	// Pct is the share of the cap (not of token_budget) that triggers the
	// first alert, in (0, 100].
	Pct float64
	Log *slog.Logger

	// sent is when each provider+level was last announced, loaded from the
	// events on first use.
	sent map[string]time.Time
}

// alertTaskID is the pseudo task id the alert events are filed under. One per
// provider: the event dedup hash does not include the backend, so two
// providers alerted in the same second would otherwise collapse into one row.
func alertTaskID(provider string) string { return "budget:" + provider }

// Check reads every provider's window and sends what is due. waiting counts
// the ready tasks deferred per provider. Every failure is logged and swallowed.
func (a *BudgetAlerts) Check(ctx context.Context, now time.Time, waiting map[string]int) {
	if a == nil || a.Usage == nil || a.Notify == nil {
		return
	}
	snap, err := a.Usage.Snapshot(ctx)
	if err != nil {
		a.logger().Warn("budget alerts: reading the windows failed", "err", err)
		return
	}
	providers := make([]string, 0, len(snap))
	for p := range snap {
		providers = append(providers, p)
	}
	sort.Strings(providers)

	for _, provider := range providers {
		p := snap[provider]
		capTokens := float64(p.TokenBudget) * p.ThresholdPct / 100
		if capTokens <= 0 {
			continue
		}
		pct := float64(p.TokensUsed) / capTokens * 100
		level := ""
		switch {
		case p.Capped:
			level = budget.AlertCapped
		case pct >= a.Pct:
			level = budget.AlertNear
		default:
			continue
		}
		window := time.Duration(p.WindowHours * float64(time.Hour))
		if a.sentSince(ctx, provider, level, now.Add(-window)) {
			continue
		}

		alert := budget.Alert{
			Provider:    provider,
			Level:       level,
			TokensUsed:  p.TokensUsed,
			Cap:         int(capTokens),
			PctOfCap:    pct,
			WindowHours: p.WindowHours,
			Waiting:     waiting[provider],
			Estimated:   p.Estimated,
		}
		if p.ResetAt != nil {
			if t, ok := state.ParseTS(*p.ResetAt); ok {
				alert.ResetAt = t
			}
		}
		a.Notify.BudgetAlert(ctx, alert)
		a.sent[provider+"|"+level] = now
		a.record(ctx, now, alert)
	}
}

// sentSince reports whether provider+level was announced after since.
func (a *BudgetAlerts) sentSince(ctx context.Context, provider, level string, since time.Time) bool {
	if a.sent == nil {
		a.sent = map[string]time.Time{}
	}
	key := provider + "|" + level
	if _, ok := a.sent[key]; !ok && a.Events != nil {
		// First look at this provider in this run: what earlier runs sent.
		a.sent[key] = time.Time{}
		events, err := a.Events.Events(ctx, alertTaskID(provider), 50)
		if err != nil {
			a.logger().Warn("budget alerts: reading earlier alerts failed", "provider", provider, "err", err)
		}
		for _, e := range events {
			if e.EventType != EventBudgetAlert || e.Extra["level"] != level {
				continue
			}
			if t, ok := state.ParseTS(e.TS); ok && t.After(a.sent[key]) {
				a.sent[key] = t
			}
		}
	}
	return a.sent[key].After(since)
}

func (a *BudgetAlerts) record(ctx context.Context, now time.Time, alert budget.Alert) {
	if a.Events == nil {
		return
	}
	err := a.Events.AppendEvent(ctx, a.RunID, state.Event{
		RunID:     a.RunID,
		EventType: EventBudgetAlert,
		TaskID:    alertTaskID(alert.Provider),
		Backend:   alert.Provider,
		TS:        utcSecond(now),
		Extra: map[string]any{
			"level":       alert.Level,
			"tokens_used": alert.TokensUsed,
			"cap":         alert.Cap,
			"pct_of_cap":  alert.PctOfCap,
		},
	})
	if err != nil {
		a.logger().Error("budget alerts: recording the alert failed", "provider", alert.Provider, "err", err)
	}
}

func (a *BudgetAlerts) logger() *slog.Logger {
	if a.Log != nil {
		return a.Log
	}
	return slog.Default()
}

// budgetWaiting counts the ready tasks deferred on each provider's budget.
func (s *Scheduler) budgetWaiting() map[string]int {
	out := map[string]int{}
	for _, reason := range s.deferReasons {
		if provider, ok := strings.CutPrefix(reason, "blocked-by-budget:"); ok {
			out[provider]++
		}
	}
	return out
}
