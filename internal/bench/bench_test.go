package bench

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/pricing"
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
	tasks []state.TaskRuntime
	spend []state.Spend
	err   error
}

func (f fakeDB) Tasks(context.Context, state.TaskFilter) ([]state.TaskRuntime, error) {
	return f.tasks, f.err
}

func (f fakeDB) AllSpend(context.Context, time.Time) ([]state.Spend, error) { return f.spend, f.err }

func TestCollect(t *testing.T) {
	prices := pricing.Load("")
	db := fakeDB{
		tasks: []state.TaskRuntime{
			{ID: "A", Status: model.StatusDone, Attempts: 1},
			{ID: "B", Status: model.StatusDone, Attempts: 3},
			{ID: "C", Status: model.StatusBlocked, Attempts: 2},
			{ID: "D", Status: model.StatusTodo},
		},
		spend: []state.Spend{
			{Backend: "claude", Model: "claude-sonnet-4-6", TokensIn: 1000, TokensOut: 100,
				CacheReadTokens: 800, CostUSD: 0.5, DurationS: 30},
			{Backend: "claude", Model: "claude-sonnet-4-6", TokensIn: 200, TokensOut: 20, DurationS: 12},
		},
	}
	got, err := Collect(context.Background(), db, prices)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tasks != 4 || got.Done != 2 || got.Blocked != 1 || got.Unfinished != 1 {
		t.Errorf("task counts = %+v", got)
	}
	if got.Retries != 3 {
		t.Errorf("retries = %d, want 3 (B's two extra attempts plus C's one)", got.Retries)
	}
	if got.Dispatches != 2 || got.AgentS != 42 {
		t.Errorf("dispatches %d agent %v, want 2 and 42", got.Dispatches, got.AgentS)
	}
	// 1320 raw tokens; the 800 cache reads count at 10%: 1320 - 720.
	if got.TokensIn+got.TokensOut != 1320 || got.WeightedTokens != 600 || got.CacheRead != 800 {
		t.Errorf("tokens raw %d weighted %d cache %d", got.TokensIn+got.TokensOut, got.WeightedTokens, got.CacheRead)
	}
	estimate := prices.ResolveCost(0, "claude-sonnet-4-6", 200, 20)
	if math.Abs(got.CostUSD-(0.5+estimate)) > 1e-9 || got.CostSource != "reported+estimated" {
		t.Errorf("cost %v source %s, want %v reported+estimated", got.CostUSD, got.CostSource, 0.5+estimate)
	}
	spent, err := SpentUSD(context.Background(), db, prices)
	if err != nil || math.Abs(spent-got.CostUSD) > 1e-9 {
		t.Errorf("SpentUSD = %v %v, want the same total as Collect (%v)", spent, err, got.CostUSD)
	}
}

func TestCostSource(t *testing.T) {
	for _, c := range []struct {
		rows                int
		reported, estimated float64
		want                string
	}{
		{0, 0, 0, "none"},
		{2, 1, 0, "reported"},
		{2, 0, 1, "estimated"},
		{2, 1, 1, "reported+estimated"},
		{2, 0, 0, "no_data"},
	} {
		if got := costSource(c.rows, c.reported, c.estimated); got != c.want {
			t.Errorf("costSource(%d, %v, %v) = %s, want %s", c.rows, c.reported, c.estimated, got, c.want)
		}
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
		Results: []Result{{Provider: "codex", Run: 1, Outcome: "finished", Tasks: 2, Done: 2, WallS: 61,
			AgentS: 50, CostUSD: 0.1234, CostSource: "estimated", TokensIn: 100, TokensOut: 10, WeightedTokens: 110,
			CLIVersion: "codex 0.154.0"}}}
	md := r.Markdown()
	for _, want := range []string{"| provider | run |", "| codex | 1 | finished | 2/2 | 0 | 0 | 0 | 0 | 1m1s | 50s | 0.12 | estimated | 110 | 110 | codex 0.154.0 |"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
}
