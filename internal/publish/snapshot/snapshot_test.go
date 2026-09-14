package snapshot

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/state"
)

// forbiddenKeys are JSON object keys that name an operator-only field
// elsewhere in this codebase — internal/cli's statusRow, internal/dashboard's
// taskPayload/graphNode, and the raw event log. None of them may appear
// anywhere in a snapshot document, at any nesting depth. This is an explicit
// allow/deny list, not a golden-file comparison, per orch-98's contract: a
// new field this package starts emitting must be added here deliberately
// before the test can pass, rather than the test silently accepting whatever
// shape Build happens to produce today.
var forbiddenKeys = map[string]bool{
	"id":                true,
	"task_id":           true,
	"project_id":        true,
	"project_root":      true,
	"backend":           true,
	"cli_model":         true,
	"model":             true,
	"tier":              true,
	"spec_ref":          true,
	"files":             true,
	"file_path":         true,
	"log_path":          true,
	"output_path":       true,
	"prompt_path":       true,
	"exit_code":         true,
	"comments":          true,
	"dependencies":      true,
	"dep_count":         true,
	"on_critical_path":  true,
	"downstream_impact": true,
	"parallelizable":    true,
	"critical_path":     true,
	"last_event":        true,
	"last_event_human":  true,
	"defer_reason":      true,
	"run_id":            true,
	"attempt":           true,
}

// forbiddenSubstrings must not appear in any STRING VALUE in the document —
// this is the check that would have caught Python's own leak
// (orchestrator/dashboard/server.py:1682-1687 ships the first 120 characters
// of a blocked task's raw comment, unredacted). It runs over the fixture's
// deliberately technical-looking task id, file path, spec ref, model name,
// and raw comment text.
var forbiddenSubstrings = []string{
	"F0.1.T1", "F0.1.T2", "F0.1.T3", // task ids
	"package.json", "README.md", // file paths
	"specs/f0.md",                          // spec ref
	"claude-sonnet-4-6", "claude-opus-4-7", // model names
	"rate limit 429", "exit code 17", "Traceback", // raw technical error text
}

func fixtureInput(now time.Time) Input {
	tasks := []model.Task{
		{ID: "F0.1.T1", Phase: 0, Title: "Setup monorepo", Status: model.StatusDone,
			EstimateHours: 1, Model: "claude-sonnet-4-6", SpecRef: "specs/f0.md",
			Files: []string{"package.json"}},
		{ID: "F0.1.T2", Phase: 0, Title: "Root README", Status: model.StatusInProgress,
			EstimateHours: 0.5, Model: "claude-sonnet-4-6", Files: []string{"README.md"}},
		{ID: "F0.1.T3", Phase: 1, Title: "Wire the router", Status: model.StatusBlocked,
			EstimateHours: 2, Model: "claude-opus-4-7",
			Comments: []json.RawMessage{
				[]byte(`{"body":"Traceback: rate limit 429 from provider, exit code 17"}`),
			}},
	}
	events := []state.Event{
		{TaskID: "F0.1.T1", EventType: "dispatch", TS: "2026-09-01T00:00:00Z"},
		{TaskID: "F0.1.T1", EventType: "success", TS: "2026-09-01T01:00:00Z",
			Extra: map[string]any{"duration_s": 3600.0}},
		{TaskID: "F0.1.T3", EventType: "fail", TS: "2026-09-05T00:00:00Z",
			Extra: map[string]any{"failure_class": "rate_limit", "reason": "rate limit 429 from provider, exit code 17"}},
	}
	spends := []state.Spend{
		{TaskID: "F0.1.T1", Backend: "claude", TS: now.Format("2006-01-02") + "T00:00:00Z", CostUSD: 1.5},
	}
	return Input{
		Tasks:            tasks,
		Phases:           []model.Phase{{ID: 0, Name: "F0 — Foundation"}, {ID: 1, Name: "F1 — Routing"}},
		Events:           events,
		Spends:           spends,
		ProjectName:      "demo",
		RefreshIntervalS: 30,
		Language:         "es",
		ShowSpend:        true,
		Now:              now,
	}
}

func TestNoOperatorFields(t *testing.T) {
	snap := Build(fixtureInput(time.Now()))
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	walkKeys(t, doc, "$")

	body := string(raw)
	for _, s := range forbiddenSubstrings {
		if strings.Contains(body, s) {
			t.Errorf("snapshot JSON contains forbidden substring %q:\n%s", s, body)
		}
	}
}

func walkKeys(t *testing.T, node any, path string) {
	t.Helper()
	switch v := node.(type) {
	case map[string]any:
		for k, val := range v {
			if forbiddenKeys[k] {
				t.Errorf("snapshot JSON has forbidden operator field %q at %s.%s", k, path, k)
			}
			walkKeys(t, val, path+"."+k)
		}
	case []any:
		for i, val := range v {
			walkKeys(t, val, path)
			_ = i
		}
	}
}

func TestSchemaIsOne(t *testing.T) {
	snap := Build(fixtureInput(time.Now()))
	if snap.Schema != 1 {
		t.Errorf("Schema = %d, want 1", snap.Schema)
	}
}

func TestBlockedTaskGetsTranslatedReasonNotRawText(t *testing.T) {
	snap := Build(fixtureInput(time.Now()))
	if len(snap.Blockers) != 1 {
		t.Fatalf("Blockers = %v, want exactly 1", snap.Blockers)
	}
	b := snap.Blockers[0]
	if b.Title != "Wire the router" {
		t.Errorf("Title = %q, want the task's title", b.Title)
	}
	if b.Reason != reasonPhrases["es"]["rate_limit"] {
		t.Errorf("Reason = %q, want the rate_limit business phrase", b.Reason)
	}
	if strings.Contains(b.Reason, "429") || strings.Contains(b.Reason, "exit code") {
		t.Errorf("Reason leaked raw technical text: %q", b.Reason)
	}
}

func TestBlockedTaskWithNoClassifiedFailureGetsGenericReason(t *testing.T) {
	in := fixtureInput(time.Now())
	in.Events = nil // no fail/timeout event at all — e.g. a manual defer
	snap := Build(in)
	if len(snap.Blockers) != 1 {
		t.Fatalf("Blockers = %v, want exactly 1", snap.Blockers)
	}
	if snap.Blockers[0].Reason != genericBlockedReason["es"] {
		t.Errorf("Reason = %q, want the generic phrase", snap.Blockers[0].Reason)
	}
}

// With nothing done yet there is no pace to measure, so ETA falls back to
// the raw remaining estimate rather than going nil — matching Python's own
// `if done_est <= 0 or done_actual <= 0: return remaining_est`. ETA is nil
// only when nothing remains at all (see the next test).
func TestETAHoursFallsBackToRawEstimateWithNothingDoneYet(t *testing.T) {
	in := fixtureInput(time.Now())
	in.Tasks = []model.Task{{ID: "X", Status: model.StatusTodo, EstimateHours: 5}}
	in.Events = nil
	snap := Build(in)
	if snap.Summary.ETAHours == nil || *snap.Summary.ETAHours != 5 {
		t.Errorf("ETAHours = %v, want 5 (raw remaining estimate)", snap.Summary.ETAHours)
	}
}

// With a measured pace the snapshot carries the same finish date the Sprint
// page shows, and the executive sentence quotes the date rather than hours:
// the summary said "Restan ~1.7h" next to a Sprint page promising a date.
func TestETADateFromVelocity(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	in := fixtureInput(now)
	in.Tasks = []model.Task{
		{ID: "A", Status: model.StatusDone, EstimateHours: 2},
		{ID: "B", Status: model.StatusTodo, EstimateHours: 3},
		{ID: "C", Status: model.StatusTodo, EstimateHours: 3},
		{ID: "D", Status: model.StatusBlocked, EstimateHours: 3},
	}
	in.DoneInVelocityWindow = 7 // one a day; B and C remain, D is blocked
	for lang, want := range map[string]string{
		"es": "Fecha estimada: 16 sept (confianza alta).",
		"en": "Estimated finish: Sep 16 (high confidence).",
	} {
		in.Language = lang
		snap := Build(in)
		if snap.Summary.ETADate == nil || *snap.Summary.ETADate != "2026-09-16" || snap.Summary.ETAConfidence != "high" {
			t.Errorf("%s: ETADate %v confidence %q, want 2026-09-16 high", lang, snap.Summary.ETADate, snap.Summary.ETAConfidence)
		}
		if !strings.Contains(snap.ExecutiveSummary.Text, want) {
			t.Errorf("%s summary = %q, want it to say %q", lang, snap.ExecutiveSummary.Text, want)
		}
		if strings.Contains(snap.ExecutiveSummary.Text, "h al ritmo") || strings.Contains(snap.ExecutiveSummary.Text, "h remaining") {
			t.Errorf("%s summary still quotes hours: %q", lang, snap.ExecutiveSummary.Text)
		}
	}

	// No pace yet: no date, and the hours sentence stays (Python's shape).
	in.DoneInVelocityWindow = 0
	in.Language = "es"
	snap := Build(in)
	if snap.Summary.ETADate != nil {
		t.Errorf("ETADate = %v with no velocity, want none", *snap.Summary.ETADate)
	}
}

// The roadmap a client reads: every phase named (from the specs when
// tasks.json has no name), its packages named, and each deliverable by title
// with its state — never a task id, a file or a spec path.
func TestRoadmapNamesPhasesPackagesAndDeliverables(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	in := fixtureInput(now)
	in.Phases = nil
	in.Tasks = []model.Task{
		{ID: "F6.1.T1", Phase: 6, Title: "Plan de campaña", Status: model.StatusBlocked},
		{ID: "F6.1.T2", Phase: 6, Title: "Presupuesto de pauta", Status: model.StatusDone},
		{ID: "F6.2.T1", Phase: 6, Title: "Textos de anuncios", Status: model.StatusTodo},
		{ID: "gh-7", Phase: 6, Title: "Arreglo pedido por el cliente", Status: model.StatusInProgress},
	}
	in.PhaseTitles = map[int]string{6: "Campaña de lanzamiento"}
	in.PackageTitles = map[string]string{"6.1": "estrategia", "6.2": "piezas"}
	in.FinishedAt = map[string]string{"F6.1.T2": "2026-09-13T10:00:00Z"}

	snap := Build(in)
	if len(snap.Milestones) != 1 || snap.Milestones[0].Name != "Campaña de lanzamiento" {
		t.Fatalf("milestones = %+v, want the phase named from its spec", snap.Milestones)
	}
	pk := snap.Milestones[0].Packages
	if len(pk) != 3 || pk[0].Name != "estrategia" || pk[1].Name != "piezas" || pk[2].Name != "" {
		t.Fatalf("packages = %+v, want estrategia, piezas, then the unpackaged task", pk)
	}
	if pk[0].Total != 2 || pk[0].Done != 1 {
		t.Errorf("estrategia counts = %d/%d, want 1/2", pk[0].Done, pk[0].Total)
	}
	d := pk[0].Deliverables
	if d[0].Title != "Plan de campaña" || d[0].Status != "blocked" || d[1].Status != "done" || d[1].FinishedAt != "2026-09-13T10:00:00Z" {
		t.Errorf("deliverables = %+v", d)
	}
	if pk[2].Deliverables[0].Status != "in_progress" {
		t.Errorf("unpackaged deliverable = %+v, want in_progress", pk[2].Deliverables[0])
	}

	raw, _ := json.Marshal(snap)
	for _, leak := range []string{"F6.1.T1", "gh-7", `"6.1"`} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("snapshot leaks %q", leak)
		}
	}
}

// "Since your last visit": what was delivered recently, newest first, by
// title and phase, with the day — a window, not the whole history.
func TestDeliveriesAreRecentNewestFirst(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	in := fixtureInput(now)
	in.Phases = []model.Phase{{ID: 1, Name: "Seguridad"}}
	in.Tasks = []model.Task{
		{ID: "F1.1.T1", Phase: 1, Title: "Old", Status: model.StatusDone},
		{ID: "F1.1.T2", Phase: 1, Title: "Recent", Status: model.StatusDone},
		{ID: "F1.1.T3", Phase: 1, Title: "Newest", Status: model.StatusDone},
		{ID: "F1.1.T4", Phase: 1, Title: "Undated", Status: model.StatusDone},
		{ID: "F1.1.T5", Phase: 1, Title: "Not done", Status: model.StatusTodo},
	}
	in.FinishedAt = map[string]string{
		"F1.1.T1": "2026-07-01T00:00:00Z",
		"F1.1.T2": "2026-09-10T08:00:00+00:00", // Python's spelling, parsed not compared
		"F1.1.T3": "2026-09-14T09:00:00Z",
		"F1.1.T5": "2026-09-14T09:30:00Z",
	}
	got := Build(in).Deliveries
	if len(got) != 2 || got[0].Title != "Newest" || got[1].Title != "Recent" || got[0].Phase != "Seguridad" {
		t.Errorf("deliveries = %+v, want Newest then Recent in Seguridad", got)
	}
}

func TestETAHoursNilWhenNothingRemains(t *testing.T) {
	in := fixtureInput(time.Now())
	in.Tasks = []model.Task{{ID: "X", Status: model.StatusDone, EstimateHours: 5}}
	snap := Build(in)
	if snap.Summary.ETAHours != nil {
		t.Errorf("ETAHours = %v, want nil (nothing left)", *snap.Summary.ETAHours)
	}
}

func TestBudgetHiddenWhenShowSpendIsFalse(t *testing.T) {
	in := fixtureInput(time.Now())
	in.ShowSpend = false
	snap := Build(in)
	if snap.Budget.Enabled {
		t.Error("Budget.Enabled = true, want false")
	}
	if snap.Budget.SpendUSD != nil {
		t.Errorf("SpendUSD = %v, want nil when show_spend_to_stakeholder is off", *snap.Budget.SpendUSD)
	}
	if len(snap.Budget.SpendByDay) != 0 {
		t.Errorf("SpendByDay = %v, want empty when show_spend_to_stakeholder is off", snap.Budget.SpendByDay)
	}
	raw, _ := json.Marshal(snap)
	if strings.Contains(string(raw), "1.5") {
		t.Error("spend amount present in JSON despite ShowSpend=false")
	}
}

func TestMilestonesUsePhaseNamesFromTasksJSON(t *testing.T) {
	snap := Build(fixtureInput(time.Now()))
	if len(snap.Milestones) != 2 {
		t.Fatalf("Milestones = %v, want 2 phases", snap.Milestones)
	}
	if snap.Milestones[0].Name != "F0 — Foundation" {
		t.Errorf("Milestones[0].Name = %q, want the phase's own name", snap.Milestones[0].Name)
	}
	if snap.Milestones[0].Complete {
		t.Error("phase 0 has a non-done task (F0.1.T2), Complete should be false")
	}
}

func TestExecutiveSummaryMentionsBlockedCountNotRawText(t *testing.T) {
	snap := Build(fixtureInput(time.Now()))
	if !strings.Contains(snap.ExecutiveSummary.Text, "1 bloqueada") {
		t.Errorf("executive summary = %q, want it to mention the blocked count", snap.ExecutiveSummary.Text)
	}
	if strings.Contains(snap.ExecutiveSummary.Text, "429") {
		t.Errorf("executive summary leaked raw technical text: %q", snap.ExecutiveSummary.Text)
	}
}
