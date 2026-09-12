package dashboard

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hectorcanaimero/orch/internal/config"
	"github.com/hectorcanaimero/orch/internal/state"
)

// newMoneyServer is newReadServer with somewhere to put a budgets.yaml or a
// config.yaml of the test's own choosing.
func newMoneyServer(t *testing.T, st StateReader, files map[string]string) (*Server, string) {
	t.Helper()
	root := writeProject(t, "spec_root: specs\n", testTasksJSON)
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s, err := New(cfg(ProfileOperator, ""), Options{
		Static: spaHandler(t),
		State:  st,
		Paths:  config.Paths{Root: root, ID: "demo", ConfigYAML: filepath.Join(root, "config.yaml")},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s, root
}

func TestMetricsReportsLifetimeSpend(t *testing.T) {
	f := &fakeState{spends: []state.Spend{
		// A billed row and an unbilled one: the total mixes a real number
		// with an estimate, which is what makes the page useful at all.
		spend("claude-sonnet-4-6", 1000, 500, 0.99, "2026-09-01T10:00:00Z", "T-1"),
		spend("gpt-5", 1_000_000, 0, 0, "2026-09-02T10:00:00Z", "T-2"),
	}}
	s, _ := newMoneyServer(t, f, nil)

	var got metricsPayload
	decode(t, get(t, s, "/api/metrics"), &got)

	if got.ProjectID != "demo" {
		t.Errorf("project_id = %q", got.ProjectID)
	}
	// 0.99 recorded + gpt-5's 2.50/1M estimate.
	if got.TotalCostUSD != 3.49 {
		t.Errorf("total_cost_usd = %v, want 3.49", got.TotalCostUSD)
	}
	if len(got.ByModel) != 2 || got.ByModel[0].Model != "gpt-5" {
		t.Errorf("by_model = %+v; want gpt-5 first (most expensive)", got.ByModel)
	}
	if len(got.ByDay) != 2 || got.ByDay[0].Date != "2026-09-02" {
		t.Errorf("by_day = %+v; want newest first", got.ByDay)
	}
	// The estimate total comes from tasks.json: 1.0 + 2.0 + 0.5.
	if got.EstimateHoursTotal != 3.5 {
		t.Errorf("estimate_hours_total = %v, want 3.5", got.EstimateHoursTotal)
	}
	// Lifetime, not a window.
	if !f.lastSince.IsZero() {
		t.Errorf("asked for spend since %v, want all of history", f.lastSince)
	}
}

// No budgets.yaml is `available: false`, which is a different answer from
// "configured and nothing spent". The SPA hides the panel on the first and
// draws empty bars on the second.
func TestBudgetSummaryWithoutAConfig(t *testing.T) {
	s, _ := newMoneyServer(t, &fakeState{}, nil)

	var got budgetSummaryPayload
	decode(t, get(t, s, "/api/budget/summary"), &got)
	if got.Available {
		t.Error("available = true with no budgets.yaml")
	}
	if got.Rows == nil {
		t.Error("rows = null; the SPA maps over it, so it must be []")
	}
}

const testBudgetsYAML = `presets:
  conservative:
    claude:
      window_hours: 5
      token_budget: 1000
      threshold_pct: 80
    codex:
      window_hours: 5
      token_budget: 100
      threshold_pct: 50
`

func TestBudgetSummaryPairsTheWindowWithWhatWasUsed(t *testing.T) {
	now := time.Now().UTC()
	f := &fakeState{spends: []state.Spend{
		// Dated NOW rather than "an hour ago": the two columns read different
		// windows — the tokens come from a 5h rolling one, the USD from
		// midnight UTC — and an hour ago is yesterday for the whole hour
		// after midnight. A test that only fails between 00:00 and 01:00 UTC
		// is the kind that gets re-run until it passes.
		{Backend: "claude", Model: "claude-sonnet-4-6", TokensIn: 500, TokensOut: 350,
			CostUSD: 1.25, TS: now.Format(time.RFC3339), TaskID: "T-1"},
	}}
	s, _ := newMoneyServer(t, f, map[string]string{"budgets.yaml": testBudgetsYAML})

	var got budgetSummaryPayload
	decode(t, get(t, s, "/api/budget/summary"), &got)
	if !got.Available || len(got.Rows) != 2 {
		t.Fatalf("got %+v", got)
	}

	// Sorted by provider name, which is what `Config.Names` guarantees.
	claude, codex := got.Rows[0], got.Rows[1]
	if claude.Provider != "claude" || codex.Provider != "codex" {
		t.Fatalf("rows = %+v, want claude then codex", got.Rows)
	}
	if claude.TokensUsed != 850 || claude.TokenBudget != 1000 {
		t.Errorf("claude = %+v, want 850 of 1000", claude)
	}
	// 850/1000 is 85%, at or over the 80% threshold.
	if claude.Pct != 85 || !claude.OverThreshold {
		t.Errorf("claude pct = %d, over = %v; want 85 and true", claude.Pct, claude.OverThreshold)
	}
	if claude.CostUSD != 1.25 {
		t.Errorf("claude cost_usd = %v, want the recorded 1.25", claude.CostUSD)
	}
	// A provider with no spend still gets a row — that is the panel saying
	// "configured, nothing used", which is not the same as absent.
	if codex.TokensUsed != 0 || codex.Pct != 0 || codex.OverThreshold {
		t.Errorf("codex = %+v, want an empty but present row", codex)
	}
	if codex.CostUSD != 0 {
		t.Errorf("codex cost_usd = %v, want 0", codex.CostUSD)
	}
}

// The percentage truncates, like Python's `int(used / budget * 100)`. 999 of
// 1000 reads as 99, and only a full budget reads as 100 — which means a
// threshold of 100 fires exactly at the budget and not a token before.
func TestBudgetSummaryPercentTruncates(t *testing.T) {
	now := time.Now().UTC()
	f := &fakeState{spends: []state.Spend{
		{Backend: "claude", TokensIn: 999, TokensOut: 0, CostUSD: 0,
			TS: now.Format(time.RFC3339), TaskID: "T-1"},
	}}
	s, _ := newMoneyServer(t, f, map[string]string{"budgets.yaml": testBudgetsYAML})

	var got budgetSummaryPayload
	decode(t, get(t, s, "/api/budget/summary"), &got)
	if got.Rows[0].Pct != 99 {
		t.Errorf("999 of 1000 reported as %d%%, want 99", got.Rows[0].Pct)
	}
}

// The USD column on this endpoint is TODAY's recorded spend, not an estimate
// and not all of history. It sits next to a token budget, and inventing
// dollars for a backend that does not bill would make it read as spend.
func TestBudgetSummaryCostIsTodaysRecordedSpend(t *testing.T) {
	now := time.Now().UTC()
	f := &fakeState{spends: []state.Spend{
		{Backend: "claude", Model: "claude-sonnet-4-6", TokensIn: 1_000_000, TokensOut: 0,
			CostUSD: 0, TS: now.Format(time.RFC3339), TaskID: "T-1"},
	}}
	s, _ := newMoneyServer(t, f, map[string]string{"budgets.yaml": testBudgetsYAML})

	var got budgetSummaryPayload
	decode(t, get(t, s, "/api/budget/summary"), &got)
	if got.Rows[0].CostUSD != 0 {
		t.Errorf("cost_usd = %v; an unbilled row must stay 0 here, not be estimated",
			got.Rows[0].CostUSD)
	}
	if got.Rows[0].TokensUsed != 1_000_000 {
		t.Errorf("tokens_used = %d; the token figure is the one that counts it",
			got.Rows[0].TokensUsed)
	}
	// Today, not all of history.
	if f.lastSince.IsZero() {
		t.Error("asked for all of history; the USD column is today's spend")
	}
}

// The allow-list is the point of this endpoint. A key nobody listed does not
// travel, however innocent it looks — that is what stops a config key added
// next year from publishing a token.
func TestConfigDoesNotShipUnknownKeys(t *testing.T) {
	const configYAML = `spec_root: specs
budgets_preset: conservative
state:
  backend: sqlite
dashboard:
  profile: stakeholder
  token: test-token-stakeholder
  show_spend_to_stakeholder: true
notify:
  slack_webhook: https://hooks.example.com/T000/B000/xxxx
some_future_key: a value nobody has written a whitelist entry for
`
	root := writeProject(t, configYAML, testTasksJSON)
	s, err := New(cfg(ProfileOperator, ""), Options{
		Static: spaHandler(t),
		State:  &fakeState{},
		Paths:  config.Paths{Root: root, ID: "demo", ConfigYAML: filepath.Join(root, "config.yaml")},
	})
	if err != nil {
		t.Fatal(err)
	}

	resp := get(t, s, "/api/config")
	defer func() { _ = resp.Body.Close() }()
	buf := make([]byte, 8192)
	n, _ := resp.Body.Read(buf)
	body := string(buf[:n])

	for _, forbidden := range []string{
		"hooks.example.com", "slack_webhook", "some_future_key",
		"test-token-stakeholder", "\"token\"", "\"profile\"",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("/api/config shipped %q:\n%s", forbidden, body)
		}
	}
	// What it DOES ship, so the test is not passing by serving nothing.
	for _, expected := range []string{`"spec_root":"specs"`, `"backend":"sqlite"`,
		`"preset":"conservative"`, `"show_spend_to_stakeholder":true`} {
		if !strings.Contains(body, expected) {
			t.Errorf("/api/config is missing %s:\n%s", expected, body)
		}
	}
}

// A key the file omits is null, not a zero value. "No per-file limit" and "a
// limit of zero" are different settings and the panel renders them
// differently.
func TestConfigOmittedKeysAreNull(t *testing.T) {
	s, _ := newMoneyServer(t, &fakeState{}, nil)

	var got map[string]any
	decode(t, get(t, s, "/api/config"), &got)

	concurrency, _ := got["concurrency"].(map[string]any)
	if v, present := concurrency["per_file"]; !present || v != nil {
		t.Errorf("concurrency.per_file = %v (present=%v), want null", v, present)
	}
	// The collections stay empty rather than null: the SPA iterates them.
	if v, _ := concurrency["per_provider"].(map[string]any); v == nil {
		t.Error("concurrency.per_provider is null; the SPA iterates it")
	}
	if v, _ := got["strict_files_phases"].([]any); v == nil {
		t.Error("strict_files_phases is null; the SPA iterates it")
	}
}

// An unreadable config is an empty view, not a 500. `/api/config/status` is
// the endpoint whose job is to say the project needs setting up.
func TestConfigSurvivesAnUnreadableFile(t *testing.T) {
	root := writeProject(t, "", testTasksJSON)
	if err := os.WriteFile(filepath.Join(root, "config.yaml"),
		[]byte("spec_root: [unterminated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := New(cfg(ProfileOperator, ""), Options{
		Static: spaHandler(t),
		State:  &fakeState{},
		Paths:  config.Paths{Root: root, ID: "demo", ConfigYAML: filepath.Join(root, "config.yaml")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp := get(t, s, "/api/config"); resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 with an empty view", resp.StatusCode)
	}
}

func TestMoneyEndpointsReportReadFailures(t *testing.T) {
	boom := errors.New("database is locked")
	for _, tc := range []struct {
		path  string
		state *fakeState
		files map[string]string
	}{
		{"/api/metrics", &fakeState{spendErr: boom}, nil},
		{"/api/budget/summary", &fakeState{spendErr: boom}, map[string]string{"budgets.yaml": testBudgetsYAML}},
	} {
		s, _ := newMoneyServer(t, tc.state, tc.files)
		if resp := get(t, s, tc.path); resp.StatusCode != http.StatusInternalServerError {
			t.Errorf("%s: status = %d, want 500", tc.path, resp.StatusCode)
		}
	}
}

// Like every other data route, these three are gated and none is on the
// stakeholder allow-list — spend is operator data.
func TestMoneyEndpointsAreGated(t *testing.T) {
	root := writeProject(t, "spec_root: specs\n", testTasksJSON)
	s, err := New(cfg(ProfileStakeholder, "test-token-stakeholder"), Options{
		Static: spaHandler(t),
		State:  &fakeState{},
		Paths:  config.Paths{Root: root, ID: "demo", ConfigYAML: filepath.Join(root, "config.yaml")},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/metrics", "/api/budget/summary", "/api/config"} {
		if resp := get(t, s, path); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s with no token: status = %d, want 401", path, resp.StatusCode)
		}
		if resp := get(t, s, path+"?token=test-token-stakeholder"); resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s with a token: status = %d, want 403", path, resp.StatusCode)
		}
	}
}
