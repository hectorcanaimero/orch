package dashboard

import (
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/hectorcanaimero/orch/internal/budget"
	"github.com/hectorcanaimero/orch/internal/pricing"
)

// The money endpoints of G5.2(b2): what the project has spent, and what the
// guardrail thinks of it.
//
// They are separate on purpose and Python's comment says why: `/api/metrics`
// reports cost in USD, which for half the backends is an ESTIMATE from a price
// table, while `/api/budget/summary` reports the rolling TOKEN window, which is
// the unit the gate actually enforces. Putting a percentage on the dollars
// would invent a USD limit the config does not have.

func (s *Server) metricRoutes() []route {
	return []route{
		{pattern: "GET /api/metrics", name: "api_metrics", handler: s.handleMetrics},
		{pattern: "GET /api/budget/summary", name: "api_budget_summary", handler: s.handleBudgetSummary},
		{pattern: "GET /api/config", name: "api_config", handler: s.handleConfig},
	}
}

// metricsPayload is `/api/metrics`'s body.
type metricsPayload struct {
	ProjectID          string       `json:"project_id"`
	TotalCostUSD       float64      `json:"total_cost_usd"`
	ByModel            []modelStats `json:"by_model"`
	ByDay              []dayStats   `json:"by_day"`
	EstimateHoursTotal float64      `json:"estimate_hours_total"`
}

// byDayWindow is how many days the chart shows. Python's default, and it is
// the width of the chart rather than a retention policy — the rows are all
// still in the database.
const byDayWindow = 14

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if s.state == nil {
		s.failRead(w, "metrics", errNoBackend)
		return
	}
	view, err := s.loadView(r.Context())
	if err != nil {
		s.failRead(w, "metrics", err)
		return
	}
	// All of history, not a window: the totals on this page are lifetime
	// figures and the by-day table does its own truncating.
	spends, err := s.state.AllSpend(r.Context(), time.Time{})
	if err != nil {
		s.failRead(w, "metrics", err)
		return
	}

	table := s.pricing()
	writeJSON(w, http.StatusOK, metricsPayload{
		ProjectID:          s.paths.ID,
		TotalCostUSD:       totalCost(spends, table),
		ByModel:            metricsByModel(spends, table),
		ByDay:              metricsByDay(spends, table, byDayWindow),
		EstimateHoursTotal: round1(view.Summary.EstimateHoursTotal),
	})
}

// pricing loads the price table, project overrides included.
//
// Loaded per request rather than once at startup so an operator editing
// `pricing.yaml` sees the effect on a refresh. It is one small embedded file
// plus an optional one on disk, and the page it serves is not a hot path.
func (s *Server) pricing() pricing.Table {
	return pricing.Load(s.paths.Root)
}

// budgetSummaryPayload is `/api/budget/summary`'s body.
//
// `available: false` is a first-class answer, not an error: a project with no
// budgets.yaml has no guardrail, and the SPA hides the panel rather than
// drawing empty bars. Distinguishing that from "configured, nothing spent" is
// the whole reason the flag exists.
type budgetSummaryPayload struct {
	Available bool               `json:"available"`
	Rows      []budgetSummaryRow `json:"rows"`
}

// budgetSummaryRow pairs one provider's configured window with what it has
// actually used. Ported from `budget_vs_actual`.
type budgetSummaryRow struct {
	Provider      string  `json:"provider"`
	TokenBudget   int     `json:"token_budget"`
	TokensUsed    int     `json:"tokens_used"`
	Pct           int     `json:"pct"`
	ThresholdPct  float64 `json:"threshold_pct"`
	OverThreshold bool    `json:"over_threshold"`
	// CostUSD rides along as information only. There is no USD limit in the
	// config, so nothing here is a percentage of it.
	CostUSD float64 `json:"cost_usd"`
}

func (s *Server) handleBudgetSummary(w http.ResponseWriter, r *http.Request) {
	if s.state == nil {
		s.failRead(w, "budget summary", errNoBackend)
		return
	}

	cfg := s.budgetConfig()
	if cfg == nil || len(cfg.Providers) == 0 {
		writeJSON(w, http.StatusOK, budgetSummaryPayload{Available: false, Rows: []budgetSummaryRow{}})
		return
	}

	snapshot, err := budget.NewGate(s.state, cfg).Snapshot(r.Context())
	if err != nil {
		s.failRead(w, "budget summary", err)
		return
	}
	costByProvider, err := s.todaysCostByProvider(r)
	if err != nil {
		s.failRead(w, "budget summary", err)
		return
	}

	rows := make([]budgetSummaryRow, 0, len(cfg.Providers))
	for _, provider := range cfg.Names() {
		pb := cfg.Providers[provider]
		used := snapshot[provider].TokensUsed
		// Integer division, like Python's `int(used / budget * 100)`: it
		// truncates, so 99.9% of a budget reads as 99 and only a full budget
		// reads as 100.
		pct := 0
		if pb.TokenBudget > 0 {
			pct = int(float64(used) / float64(pb.TokenBudget) * 100)
		}
		rows = append(rows, budgetSummaryRow{
			Provider:      provider,
			TokenBudget:   pb.TokenBudget,
			TokensUsed:    used,
			Pct:           pct,
			ThresholdPct:  pb.ThresholdPct,
			OverThreshold: pb.TokenBudget > 0 && float64(pct) >= pb.ThresholdPct,
			CostUSD:       round4(costByProvider[provider]),
		})
	}
	writeJSON(w, http.StatusOK, budgetSummaryPayload{Available: true, Rows: rows})
}

// todaysCostByProvider sums what each backend has cost since midnight UTC.
//
// Python reads today's `spend-YYYY-MM-DD.jsonl` file, which is the same window
// expressed as a filename. The Go tree has SQLite as the single source of
// truth (F-12), so the window is a WHERE clause instead — same day, same
// boundary, one fewer thing that can be out of step with the database.
//
// The recorded cost is used as recorded, with no pricing fallback: this figure
// sits next to a token budget and inventing dollars for a backend that does
// not bill would make the column read as spend when it is an estimate.
func (s *Server) todaysCostByProvider(r *http.Request) (map[string]float64, error) {
	midnight := time.Now().UTC().Truncate(24 * time.Hour)
	spends, err := s.state.AllSpend(r.Context(), midnight)
	if err != nil {
		return nil, err
	}
	out := map[string]float64{}
	for _, sp := range spends {
		out[sp.Backend] += sp.CostUSD
	}
	return out, nil
}

// budgetConfig finds the project's budgets.yaml, in Python's order: next to
// config.yaml first, then the project root.
//
// Every failure resolves to nil, which the handler reports as "no budget
// configured". A malformed file is the one case worth a thought: Python
// catches ValueError and moves on, so a typo in `budgets.yaml` silently turns
// the panel off rather than 500ing the page. Kept, because the gate itself
// refuses to start on the same file — an operator finds out from the run, not
// from a dashboard panel that is only ever informational.
func (s *Server) budgetConfig() *budget.Config {
	preset := os.Getenv("ORCH_BUDGETS_PRESET")
	if preset == "" {
		preset = "conservative"
	}
	for _, candidate := range []string{
		filepath.Join(filepath.Dir(s.paths.ConfigYAML), "budgets.yaml"),
		filepath.Join(s.paths.Root, "budgets.yaml"),
	} {
		cfg, err := budget.LoadConfig(candidate, preset)
		if err == nil && cfg != nil {
			return cfg
		}
	}
	return nil
}
