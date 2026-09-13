package dashboard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hectorcanaimero/orch/internal/pricing"
	"github.com/hectorcanaimero/orch/internal/state"
)

type metricsGolden struct {
	TotalCostUSD float64      `json:"total_cost_usd"`
	ByModel      []modelStats `json:"by_model"`
	ByDay        []dayStats   `json:"by_day"`
}

func spend(model string, tokensIn, tokensOut int, cost float64, ts, task string) state.Spend {
	return state.Spend{
		Model: model, TokensIn: tokensIn, TokensOut: tokensOut,
		CostUSD: cost, TS: ts, TaskID: task, Backend: "claude",
	}
}

// metricSpendScenarios mirrors SCENARIOS in testdata/make-metrics-golden.py.
func metricSpendScenarios() map[string][]state.Spend {
	const t0 = "2026-09-01T10:00:00Z"
	return map[string][]state.Spend{
		"empty":                  {},
		"recorded-cost-wins":     {spend("claude-sonnet-4-6", 1000, 500, 0.99, t0, "T-1")},
		"zero-cost-is-estimated": {spend("claude-sonnet-4-6", 1_000_000, 1_000_000, 0, t0, "T-1")},
		"unpriced-model":         {spend("a-model-nobody-priced", 1_000_000, 1_000_000, 0, t0, "T-1")},
		"empty-model-name":       {spend("", 1_000_000, 0, 0, t0, "T-1")},
		"retries-count-one-task": {
			spend("gpt-5", 100, 100, 0.01, t0, "T-1"),
			spend("gpt-5", 100, 100, 0.01, t0, "T-1"),
			spend("gpt-5", 100, 100, 0.01, t0, "T-2"),
		},
		"sorted-by-cost": {
			spend("claude-haiku-4-5", 1_000_000, 0, 0, t0, "T-1"),
			spend("claude-opus-4-7", 1_000_000, 0, 0, t0, "T-2"),
			spend("gpt-5", 1_000_000, 0, 0, t0, "T-3"),
		},
		"tie-breaks-on-name": {
			spend("zeta", 0, 0, 0, t0, "T-1"),
			spend("alpha", 0, 0, 0, t0, "T-2"),
		},
		"days-newest-first": {
			spend("gpt-5", 10, 10, 0.05, "2026-09-01T10:00:00Z", "T-1"),
			spend("gpt-5", 10, 10, 0.05, "2026-09-03T10:00:00Z", "T-2"),
			spend("gpt-5", 10, 10, 0.05, "2026-09-03T23:59:59Z", "T-3"),
		},
		"undated-row": {
			spend("gpt-5", 10, 10, 0.25, "2026-09", "T-1"),
			spend("gpt-5", 10, 10, 0.25, "2026-09-01T10:00:00Z", "T-1"),
		},
		"negative-tokens": {spend("claude-sonnet-4-6", -5_000_000, 1_000_000, 0, t0, "T-1")},
	}
}

func TestSpendAggregationMatchesThePythonGolden(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "metrics.golden.json"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var golden map[string]json.RawMessage
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatalf("parse golden: %v", err)
	}

	table := pricing.Load("")
	scenarios := metricSpendScenarios()
	if len(scenarios) != len(golden) {
		t.Fatalf("%d scenarios in Go, %d in the golden — one side grew alone",
			len(scenarios), len(golden))
	}

	for name, spends := range scenarios {
		body, ok := golden[name]
		if !ok {
			t.Fatalf("scenario %q has no golden — the goldens are frozen (see testdata/README.md): add the scenario's golden by hand, in the PR that adds the scenario", name)
		}
		var want metricsGolden
		if err := json.Unmarshal(body, &want); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		t.Run(name, func(t *testing.T) {
			if got := totalCost(spends, table); got != want.TotalCostUSD {
				t.Errorf("totalCost = %v, want %v", got, want.TotalCostUSD)
			}

			gotModels := metricsByModel(spends, table)
			if len(gotModels) != len(want.ByModel) {
				t.Fatalf("byModel = %+v, want %+v", gotModels, want.ByModel)
			}
			for i, w := range want.ByModel {
				if gotModels[i] != w {
					t.Errorf("byModel[%d] = %+v, want %+v", i, gotModels[i], w)
				}
			}

			gotDays := metricsByDay(spends, table, byDayWindow)
			if len(gotDays) != len(want.ByDay) {
				t.Fatalf("byDay = %+v, want %+v", gotDays, want.ByDay)
			}
			for i, w := range want.ByDay {
				if gotDays[i] != w {
					t.Errorf("byDay[%d] = %+v, want %+v", i, gotDays[i], w)
				}
			}
		})
	}
}
