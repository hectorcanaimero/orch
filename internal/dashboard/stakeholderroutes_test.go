package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hectorcanaimero/orch/internal/state"
)

func getSummary(t *testing.T, s *Server, token string) (*httptest.ResponseRecorder, stakeholderSummaryPayload) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/stakeholder/summary", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	var body stakeholderSummaryPayload
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v\n%s", err, rec.Body.String())
		}
	}
	return rec, body
}

// summaryState is the fixture: three tasks over two phases, one of each
// interesting status, and a spend row. testTasksJSON's own phases are 0 and 1.
func summaryState() *fakeState {
	return &fakeState{
		tasks: []state.TaskRuntime{
			{ID: "T-1", Status: "done"},
			{ID: "T-2", Status: "in-progress"},
			{ID: "T-3", Status: "blocked"},
		},
		spends: []state.Spend{
			spend("claude-sonnet-4-6", 1000, 500, 1.20, "2026-09-11T10:00:00Z", "T-1"),
		},
	}
}

// TestStakeholderSummaryHasEveryKeyTheSPAReads is the whole point of the
// route: the page destructures these nine names and would throw on a missing
// one. Asserted against the raw JSON rather than the Go struct, because a
// struct with a typo'd tag round-trips through itself perfectly.
func TestStakeholderSummaryHasEveryKeyTheSPAReads(t *testing.T) {
	s := portfolioServer(t, "demo", ProfileOperator, "", summaryState())

	rec, _ := getSummary(t, s, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// web/src/lib/types.ts, StakeholderSummary.
	for _, key := range []string{
		"project_id", "summary", "milestones", "spend_rounded_usd", "eta_hours",
		"refresh_interval_s", "phases_timeline", "spend_by_day", "exec_summary",
	} {
		if _, ok := raw[key]; !ok {
			t.Errorf("missing key %q — the page destructures it", key)
		}
	}
	var stats map[string]json.RawMessage
	if err := json.Unmarshal(raw["summary"], &stats); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	for _, key := range []string{
		"total", "done", "in_progress", "blocked", "backlog",
		"percent_done", "estimate_hours_total",
	} {
		if _, ok := stats[key]; !ok {
			t.Errorf("summary is missing %q", key)
		}
	}
}

func TestStakeholderSummaryCounts(t *testing.T) {
	s := portfolioServer(t, "demo", ProfileOperator, "", summaryState())

	_, body := getSummary(t, s, "")
	if body.ProjectID != "demo" {
		t.Errorf("project_id = %q", body.ProjectID)
	}
	if body.Summary.Total != 3 || body.Summary.Done != 1 ||
		body.Summary.InProgress != 1 || body.Summary.Blocked != 1 {
		t.Errorf("summary = %+v", body.Summary)
	}
	if body.RefreshIntervalS != stakeholderRefreshIntervalS {
		t.Errorf("refresh_interval_s = %d", body.RefreshIntervalS)
	}
	if body.ExecSummary == "" {
		t.Error("exec_summary is empty")
	}
	// Milestones are Python's checklist shape: one row per phase, `done`
	// meaning every task in it finished.
	if len(body.Milestones) == 0 {
		t.Fatal("no milestones")
	}
	for _, m := range body.Milestones {
		if m.TotalCount == 0 {
			t.Errorf("phase %d has total_count 0", m.Phase)
		}
		if m.Done && m.DoneCount != m.TotalCount {
			t.Errorf("phase %d says done with %d/%d", m.Phase, m.DoneCount, m.TotalCount)
		}
	}
}

// The phase timeline carries the one figure that is computed here rather than
// mapped from the snapshot, so it gets its own assertion: hours summed per
// phase, and a percentage rounded the way Python rounds it.
func TestPhasesTimelineSumsEstimateHoursPerPhase(t *testing.T) {
	s := portfolioServer(t, "demo", ProfileOperator, "", summaryState())

	_, body := getSummary(t, s, "")
	if len(body.PhasesTimeline) < 2 {
		t.Fatalf("phases_timeline = %+v; testTasksJSON has two phases", body.PhasesTimeline)
	}
	// testTasksJSON: phase 0 holds T-1 (1.0h) and T-2 (2.0h); phase 1 holds
	// T-3 (0.5h).
	var byPhase = map[int]stakeholderPhase{}
	for _, p := range body.PhasesTimeline {
		byPhase[p.Phase] = p
	}
	if got := byPhase[0].EstimateHours; got != 3.0 {
		t.Errorf("phase 0 estimate_hours = %v, want 3", got)
	}
	if got := byPhase[1].EstimateHours; got != 0.5 {
		t.Errorf("phase 1 estimate_hours = %v, want 0.5", got)
	}
	// Phase 0: one of two done -> 50%.
	if got := byPhase[0].PctDone; got != 50 {
		t.Errorf("phase 0 pct_done = %d, want 50", got)
	}
	if byPhase[0].Name == "" {
		t.Error("phase 0 has no name — the timeline labels its bars with it")
	}
}

// Bug 22's rule, on the route that page actually opens: with the flag off,
// every spend figure is absent — and `spend_rounded_usd` is NULL, not zero,
// because "nothing was spent" is a different claim from "you may not see it".
func TestSpendIsGatedByTheStakeholderFlag(t *testing.T) {
	cases := []struct {
		name      string
		showSpend bool
		wantTotal bool
		wantDays  bool
	}{
		{"flag off (the default)", false, false, false},
		{"flag on", true, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := portfolioServer(t, "demo", ProfileOperator, "", summaryState())
			s.cfg.ShowSpendToStakeholder = tc.showSpend

			_, body := getSummary(t, s, "")
			if (body.SpendRoundedUSD != nil) != tc.wantTotal {
				t.Errorf("spend_rounded_usd = %v, want present=%v", body.SpendRoundedUSD, tc.wantTotal)
			}
			if (len(body.SpendByDay) > 0) != tc.wantDays {
				t.Errorf("spend_by_day = %+v, want any=%v", body.SpendByDay, tc.wantDays)
			}
			// Never null, whichever way the flag points: the page ranges
			// over it.
			if body.SpendByDay == nil {
				t.Error("spend_by_day is null; want an empty list")
			}
			if tc.wantTotal {
				// $1.20 rounds UP to $1.50 — the same step the executive
				// summary quotes, so the card and the sentence agree.
				if *body.SpendRoundedUSD != 1.5 {
					t.Errorf("spend_rounded_usd = %v, want 1.5 (rounded up to the $0.50 step)",
						*body.SpendRoundedUSD)
				}
			}
		})
	}
}

// The route is on the stakeholder allow-list — it is the one the profile
// exists for — so a valid token reaches it and no token does not.
func TestStakeholderSummaryIsReachableWithATokenAndNotWithout(t *testing.T) {
	s := portfolioServer(t, "demo", ProfileStakeholder, testToken, summaryState())

	if rec, _ := getSummary(t, s, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token = %d, want 401", rec.Code)
	}
	if rec, _ := getSummary(t, s, "wrong"); rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong token = %d, want 401", rec.Code)
	}
	rec, body := getSummary(t, s, testToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid token = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if body.Summary.Total == 0 {
		t.Error("the payload came back empty for a valid stakeholder")
	}
}

// A backend that will not answer is a failed read, not a 200 with zeros. The
// page has an error branch and it must be reachable.
func TestStakeholderSummaryReportsAFailedRead(t *testing.T) {
	s := portfolioServer(t, "demo", ProfileOperator, "", &fakeState{tasksErr: errBoom})

	rec, _ := getSummary(t, s, "")
	if rec.Code == http.StatusOK {
		t.Fatalf("a broken backend answered 200: %s", rec.Body.String())
	}
}
