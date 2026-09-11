package budget

import (
	"context"
	"time"
)

// ProviderSnapshot is one provider's row in the dashboard's budget section.
//
// The JSON names are Python's dict keys, unchanged: the SPA reads this shape
// from `/api/budgets` today and a rename here is a broken panel, not a
// refactor.
type ProviderSnapshot struct {
	TokensUsed   int     `json:"tokens_used"`
	TokenBudget  int     `json:"token_budget"`
	UsagePct     float64 `json:"usage_pct"`
	ThresholdPct float64 `json:"threshold_pct"`
	WindowHours  float64 `json:"window_hours"`
	Capped       bool    `json:"capped"`
	// ResetAt is null unless the provider is capped AND has rows in its
	// window. See the note in Snapshot about why those are not the same
	// condition as CanDispatch's.
	ResetAt *string `json:"reset_at"`
}

// resetLayout is Python's `strftime("%Y-%m-%dT%H:%M:%SZ")`: UTC, second
// precision, literal Z. Not RFC3339 — that would write "+00:00" for a UTC
// time, and the dashboard's existing rows are all spelled with the Z.
const resetLayout = "2006-01-02T15:04:05Z"

// Snapshot is the per-provider payload the dashboard renders.
//
// Empty map when the gate is disabled — the dashboard hides the section
// entirely rather than drawing empty bars, which is how an operator can tell
// "no budgets.yaml" from "budgets.yaml with nothing spent yet".
//
// # A quirk preserved on purpose
//
// `reset_at` here is set only when the provider is capped *and* its window
// has rows in it. CanDispatch sets it whenever the provider is capped, dating
// an empty window from `now`. So a provider with a zero cap and no spend at
// all is reported capped with `reset_at: null` by this method and capped with
// a real instant by CanDispatch.
//
// Python does the same thing, and this is a port, not a correction: the
// dashboard has been rendering that null since Sprint 7 and the JSON shape is
// the contract. It is pinned by the window vector so it is a decision rather
// than an accident — and it only triggers on a threshold of zero, which is a
// config that blocks everything anyway.
func (g *Gate) Snapshot(ctx context.Context) (map[string]ProviderSnapshot, error) {
	out := map[string]ProviderSnapshot{}
	if g.Disabled() {
		return out, nil
	}
	now := g.now()
	for _, provider := range g.cfg.Names() {
		pb := g.cfg.Providers[provider]
		w, err := g.usage(ctx, provider, pb, now)
		if err != nil {
			return nil, err
		}

		capped := float64(w.tokens) >= pb.Cap()
		var resetAt *string
		if capped && !w.oldest.IsZero() {
			s := w.oldest.Add(time.Duration(pb.WindowHours * float64(time.Hour))).
				UTC().Format(resetLayout)
			resetAt = &s
		}

		usagePct := 0.0
		if pb.TokenBudget > 0 {
			usagePct = float64(w.tokens) / float64(pb.TokenBudget) * 100.0
		}

		out[provider] = ProviderSnapshot{
			TokensUsed:   w.tokens,
			TokenBudget:  pb.TokenBudget,
			UsagePct:     usagePct,
			ThresholdPct: pb.ThresholdPct,
			WindowHours:  pb.WindowHours,
			Capped:       capped,
			ResetAt:      resetAt,
		}
	}
	return out, nil
}
