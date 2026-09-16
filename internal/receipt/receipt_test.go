package receipt

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hectorcanaimero/orch/internal/config"
	"github.com/hectorcanaimero/orch/internal/demo"
	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/pricing"
	"github.com/hectorcanaimero/orch/internal/state"
)

// The demo's history is one finished run (every task done more than a day
// ago, with PRs for phases 2+) and one live run (three agents working, one
// task blocked after its retries). The receipt is for the finished one.
func TestLatestReceiptOfTheDemo(t *testing.T) {
	ctx := context.Background()
	paths, err := demo.Build(ctx, filepath.Join(t.TempDir(), "demo"), time.Now())
	if err != nil {
		t.Fatalf("demo.Build: %v", err)
	}
	res, err := config.Load(paths.ConfigYAML, paths.Root)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	db, _, err := state.Open(ctx, paths.SQLitePath(res.Config))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	b := state.NewSQLite(db, paths.ID, paths.Root)

	tf, err := model.LoadTasksFile(paths.TasksJSON())
	if err != nil {
		t.Fatalf("LoadTasksFile: %v", err)
	}
	r, err := Load(ctx, b, "", Titles(tf.Tasks), pricing.Load(paths.Root))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if r == nil {
		t.Fatal("Load returned no receipt, want the demo's finished run")
	}

	if r.RunID != demo.HistoryRunID || !r.Finished || r.FinishedAt == "" {
		t.Errorf("run = %q finished=%v at %q, want %q finished", r.RunID, r.Finished, r.FinishedAt, demo.HistoryRunID)
	}
	if len(r.Done) != 16 || len(r.Blocked) != 0 || r.FailedAttempts != 0 || r.Dispatches != 16 {
		t.Errorf("done=%d blocked=%d failed=%d dispatches=%d, want 16/0/0/16",
			len(r.Done), len(r.Blocked), r.FailedAttempts, r.Dispatches)
	}
	if r.Done[0].TaskID != "F0.T1" || r.Done[0].Title != "Repository layout and CI skeleton" {
		t.Errorf("done[0] = %+v", r.Done[0])
	}
	// Phases 2 and 3 went through pull requests: F2.T1, F2.T2, F2.T7, F2.T3, F3.T1.
	if len(r.PRs) != 5 || r.PRs[0].URL == "" || r.PRs[0].CIStatus != "success" {
		t.Errorf("prs = %+v, want 5 with urls and CI success", r.PRs)
	}
	if r.WallSeconds <= 0 || r.AgentSeconds <= 0 || r.AgentSeconds > r.WallSeconds {
		t.Errorf("wall=%v agent=%v", r.WallSeconds, r.AgentSeconds)
	}

	by := map[string]Provider{}
	for _, p := range r.Providers {
		by[p.Provider] = p
	}
	cases := []struct {
		provider, source string
	}{
		{"claude", "reported"},
		{"codex", "estimated"},
		{"opencode", "estimated"},
		{"gemini", "no_data"},
	}
	for _, c := range cases {
		p, ok := by[c.provider]
		if !ok {
			t.Errorf("no %s row in %+v", c.provider, r.Providers)
			continue
		}
		if p.CostSource != c.source {
			t.Errorf("%s cost_source = %q, want %q", c.provider, p.CostSource, c.source)
		}
	}
	// Claude's input is mostly cache reads, which the budget gate weights at
	// 10%: the weighted count must be below the raw one.
	if c := by["claude"]; c.WeightedTokens <= 0 || c.WeightedTokens >= c.TokensIn+c.TokensOut {
		t.Errorf("claude weighted = %d, raw = %d; want 0 < weighted < raw", c.WeightedTokens, c.TokensIn+c.TokensOut)
	}
	if r.EstimatedCostUSD <= 0 || r.TotalCostUSD <= r.EstimatedCostUSD {
		t.Errorf("total=%v estimated=%v", r.TotalCostUSD, r.EstimatedCostUSD)
	}
}

// A run with no sprint_done is not finished, and "latest" skips it; asking
// for it by id still answers, marked unfinished.
func TestLatestSkipsAnUnfinishedRun(t *testing.T) {
	events := []state.Event{
		{RunID: "r1", EventType: "dispatch", TaskID: "A", Backend: "claude", TS: "2026-09-10T10:00:00Z"},
		{RunID: "r1", EventType: "success", TaskID: "A", Backend: "claude", TS: "2026-09-10T10:20:00Z"},
		{RunID: "r1", EventType: "sprint_done", TS: "2026-09-10T10:21:00Z"},
		{RunID: "r2", EventType: "dispatch", TaskID: "B", Backend: "codex", TS: "2026-09-11T10:00:00Z"},
		{RunID: "r2", EventType: "fail", TaskID: "B", Backend: "codex", TS: "2026-09-11T10:10:00Z"},
		{RunID: "r2", EventType: "retry", TaskID: "B", Backend: "codex", TS: "2026-09-11T10:11:00Z"},
	}
	latest := Build(events, nil, nil, nil, pricing.Table{}, "")
	if latest == nil || latest.RunID != "r1" {
		t.Fatalf("latest = %+v, want r1", latest)
	}
	r2 := Build(events, nil, nil, nil, pricing.Table{}, "r2")
	if r2 == nil || r2.Finished || r2.FailedAttempts != 1 || r2.Retries != 1 || len(r2.Done) != 0 {
		t.Errorf("r2 = %+v, want unfinished with 1 failed attempt and 1 retry", r2)
	}
	if Build(events, nil, nil, nil, pricing.Table{}, "nope") != nil {
		t.Error("an unknown run id should give no receipt")
	}
	if Build(events[3:], nil, nil, nil, pricing.Table{}, "") != nil {
		t.Error("with no finished run, latest should give no receipt")
	}
}

// A task blocked and then done in the same run counts once, as done; spend
// outside the run's window is not the run's.
func TestOutcomesAndSpendWindow(t *testing.T) {
	events := []state.Event{
		{RunID: "r", EventType: "dispatch", TaskID: "A", Backend: "claude", TS: "2026-09-10T10:00:00Z"},
		{RunID: "r", EventType: "block", TaskID: "A", Backend: "claude", TS: "2026-09-10T10:05:00Z"},
		{RunID: "r", EventType: "dispatch", TaskID: "A", Backend: "claude", TS: "2026-09-10T10:06:00Z"},
		{RunID: "r", EventType: "success", TaskID: "A", Backend: "claude", TS: "2026-09-10T10:30:00Z"},
		{RunID: "r", EventType: "dispatch", TaskID: "B", Backend: "claude", TS: "2026-09-10T10:31:00Z"},
		{RunID: "r", EventType: "ci_blocked", TaskID: "B", Backend: "claude", TS: "2026-09-10T10:40:00Z"},
		{RunID: "r", EventType: "sprint_done", TS: "2026-09-10T11:00:00Z"},
	}
	spend := []state.Spend{
		{TS: "2026-09-10T10:29:00Z", Backend: "claude", TokensIn: 1000, TokensOut: 100, CostUSD: 0.5, DurationS: 1200},
		{TS: "2026-09-09T10:29:00Z", Backend: "claude", TokensIn: 9999, TokensOut: 999, CostUSD: 9, DurationS: 9999},
	}
	r := Build(events, spend, nil, map[string]string{"A": "Alpha"}, pricing.Table{}, "")
	if r == nil {
		t.Fatal("no receipt")
	}
	if len(r.Done) != 1 || r.Done[0].Title != "Alpha" || len(r.Blocked) != 1 || r.Blocked[0].TaskID != "B" {
		t.Errorf("done=%+v blocked=%+v", r.Done, r.Blocked)
	}
	if r.TotalCostUSD != 0.5 || r.AgentSeconds != 1200 || r.WallSeconds != 3600 {
		t.Errorf("cost=%v agent=%v wall=%v, want 0.5/1200/3600", r.TotalCostUSD, r.AgentSeconds, r.WallSeconds)
	}
}

func TestMarkdown(t *testing.T) {
	r := &Receipt{
		RunID: "r", Finished: true, WallSeconds: 2*3600 + 5*60, AgentSeconds: 50 * 60,
		Dispatches: 3, FailedAttempts: 1,
		Done:    []TaskRef{{TaskID: "A", Title: "Alpha"}},
		Blocked: []TaskRef{{TaskID: "B", Title: "Beta"}},
		Providers: []Provider{
			{Provider: "claude", Dispatches: 2, TokensIn: 12000, TokensOut: 800, WeightedTokens: 4000, CostUSD: 1.25, CostSource: "reported"},
			{Provider: "gemini", Dispatches: 1, CostSource: "no_data"},
		},
		PRs: []PR{{TaskID: "A", Title: "Alpha", URL: "https://github.com/x/y/pull/7", CIStatus: "success"}},
	}
	md := r.Markdown()
	for _, want := range []string{
		"## orch run `r`",
		"1 done · 1 blocked · 1 failed attempt · 3 dispatches · 2h 5m wall time · 50m agent time",
		"| claude | 2 | 12,000 / 800 (4,000 weighted) | $1.25 reported |",
		"| gemini | 1 | — | no data |",
		"- A Alpha",
		"- B Beta",
		"- [#7](https://github.com/x/y/pull/7) A Alpha — CI success",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, BuiltWith) {
		t.Error("Markdown must not carry the footer; callers append BuiltWith")
	}
}
