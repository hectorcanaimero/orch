package bench

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/pricing"
	"github.com/hectorcanaimero/orch/internal/receipt"
	"github.com/hectorcanaimero/orch/internal/router"
	"github.com/hectorcanaimero/orch/internal/state"
)

func write(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

const routerYAML = `claude/haiku:
  backend: claude
  cli_model: claude-haiku-4-5
  tier: cheap
  is_premium: false
claude/sonnet:
  backend: claude
  cli_model: claude-sonnet-4-6
  tier: standard
  is_premium: false
  fallback_cli_model: claude-sonnet-4-5
codex/gpt:
  backend: codex
  cli_model: gpt-5.4
  tier: standard
  is_premium: false
`

func sourceProject(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	write(t, filepath.Join(src, "tasks.json"), `{"meta":{"project":"p"},"tasks":[
{"id":"A","phase":1,"title":"t","estimateHours":1,"files":[],"specRef":"","status":"done","model":"claude/haiku","dependencies":[]},
{"id":"B","phase":1,"title":"t","estimateHours":1,"files":[],"specRef":"","status":"blocked","model":"claude/sonnet","dependencies":["A"]},
{"id":"C","phase":1,"title":"t","estimateHours":1,"files":[],"specRef":"","status":"backlog","model":"codex/gpt","dependencies":[]}]}`, 0o600)
	write(t, filepath.Join(src, ".orchestrator", "model_router.yaml"), routerYAML, 0o600)
	write(t, filepath.Join(src, ".orchestrator", "state", "p", "orch.db"), "real database", 0o600)
	write(t, filepath.Join(src, ".git", "HEAD"), "ref: refs/heads/main", 0o600)
	write(t, filepath.Join(src, "scripts", "task-start.sh"), "#!/bin/sh\n", 0o750)
	return src
}

func TestPrepareCopiesWithoutStateAndRoutesEverythingToTheProvider(t *testing.T) {
	src := sourceProject(t)
	dst := t.TempDir()
	if err := Prepare(src, dst, "codex", nil); err != nil {
		t.Fatal(err)
	}

	for _, gone := range []string{".orchestrator/state", ".git"} {
		if _, err := os.Stat(filepath.Join(dst, gone)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s was copied (err=%v); a bench copy must start without it", gone, err)
		}
	}
	info, err := os.Stat(filepath.Join(dst, "scripts", "task-start.sh"))
	if err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Errorf("task-start.sh lost its executable bit: %v %v", info, err)
	}

	tf, err := model.LoadTasksFile(filepath.Join(dst, "tasks.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]model.Status{"A": model.StatusTodo, "B": model.StatusTodo, "C": model.StatusBacklog}
	for _, task := range tf.Tasks {
		if task.Status != want[task.ID] {
			t.Errorf("task %s status %q, want %q", task.ID, task.Status, want[task.ID])
		}
	}
	if tf.Meta.Project != "p" {
		t.Errorf("meta was not kept: %v", tf.Meta)
	}

	rtr, err := router.Load(filepath.Join(dst, ".orchestrator", "model_router.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for key, e := range rtr {
		if e.Backend != model.BackendCodex || e.CLIModel != "gpt-5.4" {
			t.Errorf("route %s = %s/%s, want codex/gpt-5.4", key, e.Backend, e.CLIModel)
		}
		if e.FallbackCLIModel != nil {
			t.Errorf("route %s kept claude's fallback model %q", key, *e.FallbackCLIModel)
		}
	}
	if rtr["claude/haiku"].Tier != model.TierCheap {
		t.Errorf("the tier changed: %s", rtr["claude/haiku"].Tier)
	}
}

func TestPrepareModelFlagAndMissingRoute(t *testing.T) {
	src := sourceProject(t)

	dst := t.TempDir()
	if err := Prepare(src, dst, "claude", map[string]string{"claude": "opus"}); err != nil {
		t.Fatal(err)
	}
	rtr, err := router.Load(filepath.Join(dst, ".orchestrator", "model_router.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for key, e := range rtr {
		if e.Backend != model.BackendClaude || e.CLIModel != "opus" {
			t.Errorf("route %s = %s/%s, want claude/opus", key, e.Backend, e.CLIModel)
		}
	}

	err = Prepare(src, t.TempDir(), "gemini", nil)
	if err == nil || !strings.Contains(err.Error(), "--model gemini=") {
		t.Errorf("a provider with no route and no --model must say how to fix it, got %v", err)
	}
}

type fakeDB struct {
	tasks  []state.TaskRuntime
	events []state.Event
	spend  []state.Spend
}

func (f fakeDB) Tasks(context.Context, state.TaskFilter) ([]state.TaskRuntime, error) {
	return f.tasks, nil
}

func (f fakeDB) AllEvents(context.Context, int) ([]state.Event, error) { return f.events, nil }

func (f fakeDB) AllSpend(context.Context, time.Time) ([]state.Spend, error) { return f.spend, nil }

func ev(kind, task, ts string) state.Event {
	return state.Event{RunID: "r1", EventType: kind, TaskID: task, Backend: "claude", TS: ts}
}

func TestCollectReadsTheRunsReceipt(t *testing.T) {
	prices := pricing.Load("")
	db := fakeDB{
		tasks: []state.TaskRuntime{{ID: "A"}, {ID: "B"}, {ID: "C"}, {ID: "D"}},
		events: []state.Event{
			ev("dispatch", "A", "2026-09-16T10:00:00Z"),
			ev("success", "A", "2026-09-16T10:01:00Z"),
			ev("dispatch", "B", "2026-09-16T10:01:00Z"),
			ev("fail", "B", "2026-09-16T10:02:00Z"),
			ev("retry", "B", "2026-09-16T10:02:00Z"),
			ev("dispatch", "B", "2026-09-16T10:02:30Z"),
			ev("block", "B", "2026-09-16T10:03:00Z"),
			ev("sprint_done", "", "2026-09-16T10:04:00Z"),
		},
		spend: []state.Spend{
			{TS: "2026-09-16T10:01:00Z", Backend: "claude", Model: "claude-sonnet-4-6", TokensIn: 1000, TokensOut: 100,
				CacheReadTokens: 800, CostUSD: 0.5, DurationS: 30},
			{TS: "2026-09-16T10:02:00Z", Backend: "claude", Model: "claude-sonnet-4-6", TokensIn: 200, TokensOut: 20, DurationS: 12},
			// Outside the run's span: another run's row, not this one's.
			{TS: "2026-09-15T10:00:00Z", Backend: "claude", TokensIn: 9999, CostUSD: 9, DurationS: 99},
		},
	}
	got, err := Collect(context.Background(), db, "r1", prices)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tasks != 4 || got.Done != 1 || got.Blocked != 1 || got.Unfinished != 2 {
		t.Errorf("task counts = %+v", got)
	}
	if got.Dispatches != 3 || got.FailedAttempts != 1 || got.Retries != 1 {
		t.Errorf("dispatches %d failed %d retries %d, want 3, 1, 1", got.Dispatches, got.FailedAttempts, got.Retries)
	}
	if got.WallS != 240 || got.AgentS != 42 {
		t.Errorf("wall %v agent %v, want 240 and 42", got.WallS, got.AgentS)
	}
	// 1320 raw tokens; the 800 cache reads count at 10%: 1320 - 720.
	if got.TokensIn+got.TokensOut != 1320 || got.WeightedTokens != 600 {
		t.Errorf("tokens raw %d weighted %d, want 1320 and 600", got.TokensIn+got.TokensOut, got.WeightedTokens)
	}
	rc, err := receipt.Load(context.Background(), db, "r1", nil, prices)
	if err != nil {
		t.Fatal(err)
	}
	if got.CostUSD != rc.TotalCostUSD || got.EstimatedCostUSD != rc.EstimatedCostUSD || got.CostSource != "reported" {
		t.Errorf("cost %v (est %v, %s), want the receipt's %v (est %v, reported)",
			got.CostUSD, got.EstimatedCostUSD, got.CostSource, rc.TotalCostUSD, rc.EstimatedCostUSD)
	}
	spent, err := SpentUSD(context.Background(), db, "r1", prices)
	if err != nil || spent != got.CostUSD {
		t.Errorf("SpentUSD = %v %v, want the same total as Collect (%v)", spent, err, got.CostUSD)
	}
}

func TestCollectARunThatNeverStarted(t *testing.T) {
	db := fakeDB{tasks: []state.TaskRuntime{{ID: "A"}, {ID: "B"}}}
	got, err := Collect(context.Background(), db, "r1", pricing.Load(""))
	if err != nil {
		t.Fatal(err)
	}
	if got.Tasks != 2 || got.Unfinished != 2 || got.Done != 0 || got.Dispatches != 0 {
		t.Errorf("a run with no events = %+v, want 2 tasks, all unfinished", got)
	}
	if spent, err := SpentUSD(context.Background(), db, "r1", pricing.Load("")); spent != 0 || err != nil {
		t.Errorf("SpentUSD with no run = %v %v, want 0", spent, err)
	}
}

func TestWatchCapStopsAtTheCap(t *testing.T) {
	ticks := make(chan time.Time)
	totals := []float64{1, 2.5}
	stopped := false
	done := make(chan error, 1)
	go func() {
		done <- WatchCap(context.Background(), ticks, 2, func(context.Context) (float64, error) {
			v := totals[0]
			totals = totals[1:]
			return v, nil
		}, func() { stopped = true })
	}()
	ticks <- time.Time{}
	ticks <- time.Time{}
	if err := <-done; !errors.Is(err, ErrOverCap) || !stopped {
		t.Errorf("WatchCap = %v, stopped %v; want ErrOverCap and stop called", err, stopped)
	}
}

func TestWatchCapIgnoresReadErrorsAndEndsWithTheRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ticks := make(chan time.Time)
	done := make(chan error, 1)
	go func() {
		done <- WatchCap(ctx, ticks, 1, func(context.Context) (float64, error) {
			return 99, errors.New("database is locked")
		}, func() { t.Error("stop called on a read error") })
	}()
	ticks <- time.Time{}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("WatchCap after the run ended = %v, want nil", err)
	}
}

func TestMarkdown(t *testing.T) {
	r := Report{Date: "2026-09-16", OrchVersion: "v1", OS: "linux", Arch: "amd64", Project: "template:python-api",
		Results: []Result{{Provider: "codex", Run: 1, Outcome: "finished", Tasks: 2, Done: 2, Dispatches: 3,
			FailedAttempts: 1, Retries: 1, WallS: 61, AgentS: 50, CostUSD: 0.1234, CostSource: "estimated",
			TokensIn: 100, TokensOut: 10, WeightedTokens: 110, CLIVersion: "codex 0.154.0"}}}
	md := r.Markdown()
	for _, want := range []string{"| provider | run |",
		"| codex | 1 | finished | 2/2 | 0 | 0 | 3 | 1 | 1 | 1m | 50s | 0.12 | estimated | 110 | 110 | codex 0.154.0 |"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
}
