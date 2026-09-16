package dashboard

import (
	"net/http"
	"time"

	"github.com/hectorcanaimero/orch/internal/budget"
	"github.com/hectorcanaimero/orch/internal/config"
	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/pricing"
	"github.com/hectorcanaimero/orch/internal/state"
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
	ProjectID    string  `json:"project_id"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	// EstimatedCostUSD is the part of TotalCostUSD that no CLI reported:
	// priced from tokens with pricing.yaml. Zero when every dollar is real.
	EstimatedCostUSD   float64      `json:"estimated_cost_usd"`
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
		EstimatedCostUSD:   estimatedCost(spends, table),
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
// budgets.yaml has no guardrail, and the SPA says so rather than drawing
// empty bars. Distinguishing that from "configured, nothing spent" is the
// whole reason the flag exists.
type budgetSummaryPayload struct {
	Available bool `json:"available"`
	// Preset and Path are the ones `orch run` enforces: config.yaml's
	// budgets_preset and budget.ResolvePath's file, found or not.
	Preset string `json:"preset"`
	Path   string `json:"path"`
	// Reason says why Available is false: "not_configured" (budgets_config
	// is empty), "missing" (no file at Path) or "invalid" (Error says why).
	Reason string             `json:"reason,omitempty"`
	Error  string             `json:"error,omitempty"`
	Rows   []budgetSummaryRow `json:"rows"`
	// Waiting are the todo tasks the gate deferred and whose window has not
	// reset yet.
	Waiting []budget.Waiting `json:"waiting"`
}

// budgetSummaryRow pairs one provider's configured window with what it has
// actually used. Ported from `budget_vs_actual`.
type budgetSummaryRow struct {
	Provider    string `json:"provider"`
	TokenBudget int    `json:"token_budget"`
	TokensUsed  int    `json:"tokens_used"`
	// RawTokensUsed is the window as the CLIs reported it; TokensUsed is the
	// gate's weighted count (cache reads 10%, cache writes 125%).
	RawTokensUsed int     `json:"raw_tokens_used"`
	Pct           int     `json:"pct"`
	ThresholdPct  float64 `json:"threshold_pct"`
	OverThreshold bool    `json:"over_threshold"`
	WindowHours   float64 `json:"window_hours"`
	// ResetAt is the gate's estimate of when a capped window frees; null
	// while the provider is under its cap.
	ResetAt *string `json:"reset_at"`
	// CostUSD rides along as information only. There is no USD limit in the
	// config, so nothing here is a percentage of it.
	CostUSD float64 `json:"cost_usd"`
	// CostSource says what today's dollars are: "reported" by the CLI,
	// "estimated" from pricing.yaml (EstimatedCostUSD) because the CLI
	// reports tokens but no price, "no_data" when it reports neither, or
	// "none" when nothing ran today.
	CostSource       string  `json:"cost_source"`
	EstimatedCostUSD float64 `json:"estimated_cost_usd"`
}

func (s *Server) handleBudgetSummary(w http.ResponseWriter, r *http.Request) {
	if s.state == nil {
		s.failRead(w, "budget summary", errNoBackend)
		return
	}

	out := budgetSummaryPayload{Rows: []budgetSummaryRow{}, Waiting: []budget.Waiting{}}
	cfg := s.budgetConfig(&out)
	if cfg == nil || len(cfg.Providers) == 0 {
		writeJSON(w, http.StatusOK, out)
		return
	}

	snapshot, err := budget.NewGate(s.state, cfg).Snapshot(r.Context())
	if err != nil {
		s.failRead(w, "budget summary", err)
		return
	}
	today, err := s.todaysCostByProvider(r)
	if err != nil {
		s.failRead(w, "budget summary", err)
		return
	}

	for _, provider := range cfg.Names() {
		pb := cfg.Providers[provider]
		snap := snapshot[provider]
		used := snap.TokensUsed
		// Integer division, like Python's `int(used / budget * 100)`: it
		// truncates, so 99.9% of a budget reads as 99 and only a full budget
		// reads as 100.
		pct := 0
		if pb.TokenBudget > 0 {
			pct = int(float64(used) / float64(pb.TokenBudget) * 100)
		}
		c := today[provider]
		out.Rows = append(out.Rows, budgetSummaryRow{
			Provider:         provider,
			TokenBudget:      pb.TokenBudget,
			TokensUsed:       used,
			RawTokensUsed:    snap.RawTokensUsed,
			Pct:              pct,
			ThresholdPct:     pb.ThresholdPct,
			OverThreshold:    pb.TokenBudget > 0 && float64(pct) >= pb.ThresholdPct,
			WindowHours:      pb.WindowHours,
			ResetAt:          snap.ResetAt,
			CostUSD:          round4(c.recorded),
			CostSource:       c.source(),
			EstimatedCostUSD: round4(c.estimated),
		})
	}
	out.Available = true

	out.Waiting, err = s.waitingForBudget(r)
	if err != nil {
		s.failRead(w, "budget summary", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// providerCost is one provider's spend since midnight UTC.
type providerCost struct {
	rows      int
	recorded  float64
	estimated float64
}

func (c providerCost) source() string {
	switch {
	case c.rows == 0:
		return "none"
	case c.recorded > 0:
		return "reported"
	case c.estimated > 0:
		return "estimated"
	default:
		return "no_data"
	}
}

// todaysCostByProvider sums what each backend has cost since midnight UTC.
//
// Python reads today's `spend-YYYY-MM-DD.jsonl` file, which is the same window
// expressed as a filename. The Go tree has SQLite as the single source of
// truth (F-12), so the window is a WHERE clause instead — same day, same
// boundary, one fewer thing that can be out of step with the database.
//
// The recorded cost stays as recorded: this figure sits next to a token
// budget, and inventing dollars for a backend that does not bill would make
// that column read as spend. A row with tokens and no price gets its
// pricing.yaml estimate in its own field instead, labelled as such.
func (s *Server) todaysCostByProvider(r *http.Request) (map[string]providerCost, error) {
	midnight := time.Now().UTC().Truncate(24 * time.Hour)
	spends, err := s.state.AllSpend(r.Context(), midnight)
	if err != nil {
		return nil, err
	}
	table := s.pricing()
	out := map[string]providerCost{}
	for _, sp := range spends {
		c := out[sp.Backend]
		c.rows++
		switch {
		case sp.CostUSD > 0:
			c.recorded += sp.CostUSD
		case sp.TokensIn+sp.TokensOut > 0:
			c.estimated += table.EstimateCost(sp.Model, sp.TokensIn, sp.TokensOut)
		}
		out[sp.Backend] = c
	}
	return out, nil
}

// waitingForBudget lists the todo tasks the gate deferred whose window has
// not reset yet — the same rule `orch status` uses for `defer_reason`.
func (s *Server) waitingForBudget(r *http.Request) ([]budget.Waiting, error) {
	rows, err := s.state.Tasks(r.Context(), state.TaskFilter{})
	if err != nil {
		return nil, err
	}
	var todo []string
	for _, t := range rows {
		if t.Status == model.StatusTodo {
			todo = append(todo, t.ID)
		}
	}
	out := []budget.Waiting{}
	if len(todo) == 0 {
		return out, nil
	}
	last, err := s.state.LastEventByTask(r.Context(), todo)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	for _, id := range todo {
		if wt, ok := budget.WaitingOn(last[id], now); ok {
			wt.TaskID = id
			out = append(out, wt)
		}
	}
	return out, nil
}

// budgetConfig loads the budgets.yaml `orch run` enforces — config.yaml's
// budgets_config and budgets_preset, resolved by budget.ResolvePath — and
// fills in the payload's preset, path and, when there is no guardrail, why.
//
// It used to read the preset from ORCH_BUDGETS_PRESET, which `orch run` never
// looks at, so a project on any preset but `conservative` saw "no budget
// configured" while its run was being gated.
//
// A malformed file is reported in the payload, not as a 500: the panel is
// informational, and `orch run` names the same file when it loads it.
func (s *Server) budgetConfig(out *budgetSummaryPayload) *budget.Config {
	loaded, err := config.Load(s.paths.ConfigYAML, s.paths.Root)
	if err != nil {
		out.Reason, out.Error = "invalid", err.Error()
		return nil
	}
	c := loaded.Config
	out.Preset = c.BudgetsPreset
	out.Path = budget.ResolvePath(s.paths.Root, s.paths.ConfigYAML, c.BudgetsConfig)
	if out.Path == "" {
		out.Reason = "not_configured"
		return nil
	}
	cfg, err := budget.LoadConfig(out.Path, c.BudgetsPreset)
	switch {
	case err != nil:
		out.Reason, out.Error = "invalid", err.Error()
		return nil
	case cfg == nil:
		out.Reason = "missing"
		return nil
	}
	cfg.UnreportedDispatchTokens = c.TypicalDispatchToken
	return cfg
}
