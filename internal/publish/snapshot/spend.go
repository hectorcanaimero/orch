package snapshot

import (
	"sort"
	"time"

	"github.com/hectorcanaimero/orch/internal/state"
)

// totalSpend sums every spend row's recorded cost — no per-backend or
// per-model breakdown, which the stakeholder contract forbids outright.
//
// Known gap, same one internal/cli's computeCostByTask already has: a
// recorded `cost_usd` of exactly 0 is ambiguous. Python's `total_cost`
// (orchestrator/dashboard/pricing.py) treats a positive cost as ground
// truth and otherwise ESTIMATES from tokens against pricing.yaml, because
// the backends that genuinely cost nothing are the same ones that never
// report a cost at all (opus, reviewing this file, confirmed a whole
// opencode-only project would otherwise show as $0 spent, not "unknown").
// `internal/pricing` (opus, G5.2 b2, PR #175, not yet merged as this was
// written) will be the shared place to do that estimation once it lands —
// tracked in docs/brainstorm/go-migration-notes/sonnet.md rather than
// duplicated here ahead of time.
func totalSpend(spends []state.Spend) float64 {
	var total float64
	for _, s := range spends {
		total += s.CostUSD
	}
	return total
}

// spendByDayLast14 groups spend into daily totals over the trailing 14 days,
// ported from `_stakeholder_payload`'s own inline grouping: "safe to show
// because we never break down by model" — only the day and the sum.
func spendByDayLast14(spends []state.Spend, now time.Time) []DailySpend {
	nowMinus14 := now.UTC().AddDate(0, 0, -14)
	cutoff := time.Date(nowMinus14.Year(), nowMinus14.Month(), nowMinus14.Day(), 0, 0, 0, 0, time.UTC)
	byDay := map[string]float64{}
	for _, s := range spends {
		day, ok := spendDay(s.TS)
		if !ok {
			continue
		}
		d, err := time.Parse("2006-01-02", day)
		if err != nil || d.Before(cutoff) {
			continue
		}
		byDay[day] += s.CostUSD
	}
	days := make([]string, 0, len(byDay))
	for d := range byDay {
		days = append(days, d)
	}
	sort.Strings(days)
	out := make([]DailySpend, 0, len(days))
	for _, d := range days {
		out = append(out, DailySpend{Date: d, CostUSD: round4(byDay[d])})
	}
	return out
}

// spendDay reads the first 10 characters of an ISO-8601 timestamp as its
// date, matching Python's `str(s.get("ts", ""))[:10]`.
func spendDay(ts string) (string, bool) {
	if len(ts) < 10 {
		return "", false
	}
	return ts[:10], true
}
