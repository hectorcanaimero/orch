package dashboard

import (
	"sort"

	"github.com/hectorcanaimero/orch/internal/pricing"
	"github.com/hectorcanaimero/orch/internal/state"
)

// Spend aggregation for the metrics page, ported from the per-model and
// per-day half of `orchestrator/dashboard/metrics.py`.
//
// Every figure here goes through `pricing.Table.ResolveCost`, so a row the
// provider billed keeps its real number and a row it did not gets an estimate
// from tokens. Mixing the two in one total is the point: the page answers
// "what has this project cost", and half the backends do not bill.

// modelStats is one row of the by-model table.
type modelStats struct {
	Model      string  `json:"model"`
	TasksTotal int     `json:"tasks_total"`
	TokensIn   int     `json:"tokens_in"`
	TokensOut  int     `json:"tokens_out"`
	CostUSD    float64 `json:"cost_usd"`
}

// dayStats is one row of the by-day table.
type dayStats struct {
	Date       string  `json:"date"`
	TokensIn   int     `json:"tokens_in"`
	TokensOut  int     `json:"tokens_out"`
	CostUSD    float64 `json:"cost_usd"`
	TasksTotal int     `json:"tasks_total"`
}

// spendBucket accumulates one group of spend rows.
type spendBucket struct {
	tokensIn  int
	tokensOut int
	cost      float64
	taskIDs   map[string]bool
}

func (b *spendBucket) add(s state.Spend, cost float64) {
	b.tokensIn += s.TokensIn
	b.tokensOut += s.TokensOut
	b.cost += cost
	if s.TaskID != "" {
		if b.taskIDs == nil {
			b.taskIDs = map[string]bool{}
		}
		b.taskIDs[s.TaskID] = true
	}
}

// metricsByModel groups spend by model name, most expensive first.
//
// `tasks_total` counts DISTINCT task ids, so a task that took three attempts
// on the same model counts once. That is what makes the column readable as
// "how much of the project went through this model" rather than as a retry
// count.
//
// A row with no model is grouped under "unknown" — Python's string, and worth
// keeping: it makes a data-quality problem visible in the table instead of
// folding it into the default price row silently.
func metricsByModel(spends []state.Spend, table pricing.Table) []modelStats {
	buckets := map[string]*spendBucket{}
	for _, s := range spends {
		model := s.Model
		if model == "" {
			model = "unknown"
		}
		b := buckets[model]
		if b == nil {
			b = &spendBucket{}
			buckets[model] = b
		}
		b.add(s, table.ResolveCost(s.CostUSD, model, s.TokensIn, s.TokensOut))
	}

	out := make([]modelStats, 0, len(buckets))
	for model, b := range buckets {
		out = append(out, modelStats{
			Model: model, TasksTotal: len(b.taskIDs),
			TokensIn: b.tokensIn, TokensOut: b.tokensOut, CostUSD: round4(b.cost),
		})
	}
	// Cost descending, then name, so two free models do not swap places
	// between requests.
	sort.Slice(out, func(i, j int) bool {
		if out[i].CostUSD != out[j].CostUSD {
			return out[i].CostUSD > out[j].CostUSD
		}
		return out[i].Model < out[j].Model
	})
	return out
}

// metricsByDay buckets spend by UTC date, newest first, at most `days` rows.
//
// A day with no spend does not appear. Python's comment is explicit that this
// is a choice rather than an oversight: the table reads better without runs of
// zero rows, and a chart that wants them can zero-fill from the dates it got.
func metricsByDay(spends []state.Spend, table pricing.Table, days int) []dayStats {
	buckets := map[string]*spendBucket{}
	for _, s := range spends {
		// The first ten characters of an ISO timestamp are its date. A
		// timestamp too short to hold one is skipped rather than dated to
		// today, which would move somebody's spend into the current day.
		if len(s.TS) < 10 {
			continue
		}
		day := s.TS[:10]
		model := s.Model
		if model == "" {
			model = "unknown"
		}
		b := buckets[day]
		if b == nil {
			b = &spendBucket{}
			buckets[day] = b
		}
		b.add(s, table.ResolveCost(s.CostUSD, model, s.TokensIn, s.TokensOut))
	}

	out := make([]dayStats, 0, len(buckets))
	for day, b := range buckets {
		out = append(out, dayStats{
			Date: day, TokensIn: b.tokensIn, TokensOut: b.tokensOut,
			CostUSD: round4(b.cost), TasksTotal: len(b.taskIDs),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date > out[j].Date })
	if days > 0 && len(out) > days {
		out = out[:days]
	}
	return out
}

// totalCost is every row resolved and summed.
//
// It is computed from the same rows the tables are, not from adding the tables
// up: the by-day table is truncated to 14 rows and the by-model one rounds
// each row, so either would give a different — and quietly wrong — total.
func totalCost(spends []state.Spend, table pricing.Table) float64 {
	total := 0.0
	for _, s := range spends {
		model := s.Model
		if model == "" {
			// Python reaches for "default" here and "unknown" in the tables.
			// Both land on the default row unless a project has defined a
			// model literally called "unknown"; kept as-is rather than
			// unified, because unifying it would change one of the two.
			model = "default"
		}
		total += table.ResolveCost(s.CostUSD, model, s.TokensIn, s.TokensOut)
	}
	return round4(total)
}

// round4 is Python's `round(x, 4)`.
func round4(v float64) float64 { return roundDecimals(v, 4) }
