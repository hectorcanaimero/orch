package demo

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/hectorcanaimero/orch/internal/budget"
	"github.com/hectorcanaimero/orch/internal/config"
	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/project"
	"github.com/hectorcanaimero/orch/internal/router"
	"github.com/hectorcanaimero/orch/internal/state"
)

// The demo is only worth having if the dashboard reads it the way it reads a
// real project, so this checks it through the readers the dashboard uses.
func TestBuildSeedsAProjectTheDashboardCanRead(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	paths, err := Build(ctx, filepath.Join(t.TempDir(), "demo"), now)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	res, err := config.Load(paths.ConfigYAML, paths.Root)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	db, _, err := state.Open(ctx, paths.SQLitePath(res.Config))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	b := state.NewSQLite(db, paths.ID, paths.Root)

	got, err := project.Load(ctx, b, paths.TasksJSON())
	if err != nil {
		t.Fatalf("project.Load: %v", err)
	}
	counts := map[model.Status]int{}
	for _, task := range got {
		counts[task.Status]++
	}
	want := map[model.Status]int{
		model.StatusDone: 16, model.StatusInProgress: 3, model.StatusBlocked: 2,
		model.StatusTodo: 6, model.StatusBacklog: 3,
	}
	for s, n := range want {
		if counts[s] != n {
			t.Errorf("%s: got %d tasks, want %d (all: %v)", s, counts[s], n, counts)
		}
	}

	inFlight, err := b.InFlightDispatches(ctx)
	if err != nil || len(inFlight) != 3 {
		t.Errorf("in-flight dispatches: got %d (%v), want 3", len(inFlight), err)
	}
	spend, err := b.AllSpend(ctx, now.Add(-30*24*time.Hour))
	if err != nil || len(spend) == 0 {
		t.Fatalf("spend: got %d rows (%v)", len(spend), err)
	}
	byBackend := map[string]float64{}
	for _, s := range spend {
		byBackend[s.Backend] += s.CostUSD
	}
	if byBackend["claude"] == 0 || byBackend["codex"] != 0 {
		t.Errorf("claude reports dollars and codex only tokens; got %v", byBackend)
	}
	// claude's rows carry a cache breakdown, as the real CLI reports it, so
	// the Budget page shows the weighted window next to the raw one.
	cached := false
	for _, s := range spend {
		if s.Backend == "claude" && s.CacheReadTokens > 0 && s.CacheCreationTokens > 0 {
			cached = true
		}
	}
	if !cached {
		t.Error("no claude spend row reports cache tokens")
	}
	if _, ok := byBackend["gemini"]; !ok {
		t.Errorf("gemini should have zero-cost rows; got %v", byBackend)
	}
	events, err := b.AllEvents(ctx, 500)
	if err != nil || len(events) < 50 {
		t.Errorf("events: got %d (%v), want a Logs page's worth", len(events), err)
	}
	done, err := b.CountDoneLastNDays(ctx, 7)
	if err != nil || done == 0 {
		t.Errorf("done in the last 7 days: got %d (%v); velocity would be zero", done, err)
	}

	rtr, err := router.Load(paths.RouterYAML())
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range got {
		if _, ok := rtr[task.Model]; !ok {
			t.Errorf("%s: model %q has no route", task.ID, task.Model)
		}
	}
	if cfg, err := budget.LoadConfig(filepath.Join(paths.Root, ".orchestrator", "budgets.yaml"), "conservative"); err != nil || cfg == nil {
		t.Errorf("budgets.yaml: %v, %v", cfg, err)
	}
}
