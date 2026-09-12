package explain

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hectorcanaimero/orch/internal/budget"
	"github.com/hectorcanaimero/orch/internal/model"
)

type fakeBudget struct {
	disabled  bool
	providers map[string]budget.ProviderSnapshot
	err       error
}

func (f fakeBudget) Disabled() bool { return f.disabled }
func (f fakeBudget) Snapshot(context.Context) (map[string]budget.ProviderSnapshot, error) {
	return f.providers, f.err
}

func task(id string, phase int, status model.Status, deps []string, hours float64, title string) model.Task {
	return model.Task{
		ID: id, Phase: phase, Status: status, Dependencies: deps,
		EstimateHours: hours, Title: title, Model: "claude/sonnet",
	}
}

// A chain with one task done: exactly one is ready, and the counts cover every
// status whether or not the project uses it.
func demoTasks() []model.Task {
	return []model.Task{
		task("T-1", 0, model.StatusDone, nil, 1, "Scaffold"),
		task("T-2", 0, model.StatusTodo, []string{"T-1"}, 2, "Database"),
		task("T-3", 1, model.StatusTodo, []string{"T-2"}, 4, "API"),
		task("T-4", 1, model.StatusBlocked, []string{"T-2"}, 1, "Deploy"),
	}
}

func TestGatherCountsEveryStatusIncludingTheZeros(t *testing.T) {
	got, err := Gather(context.Background(), Options{Tasks: demoTasks()})
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]int{
		"backlog": 0, "todo": 2, "in-progress": 0, "done": 1, "blocked": 1,
	}
	if len(got.Project.Counts) != len(want) {
		t.Fatalf("counts = %v, want every status present", got.Project.Counts)
	}
	for st, n := range want {
		if got.Project.Counts[st] != n {
			t.Errorf("counts[%s] = %d, want %d", st, got.Project.Counts[st], n)
		}
	}
	if got.Project.Total != 4 {
		t.Errorf("total = %d, want 4", got.Project.Total)
	}
}

// `ready` is graph.Parallelizable's answer, not a second definition. T-2's
// only dependency is done; T-3 waits on T-2; T-4 is blocked, which is not a
// status that can be dispatched however satisfied its dependencies are.
func TestGatherReadyIsTheDispatchLoopsNotionOfReady(t *testing.T) {
	got, err := Gather(context.Background(), Options{Tasks: demoTasks()})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Ready) != 1 || got.Ready[0].ID != "T-2" {
		t.Fatalf("ready = %+v, want just T-2", got.Ready)
	}
	if got.Ready[0].Title != "Database" || got.Ready[0].EstimateHours != 2 {
		t.Errorf("ready[0] = %+v", got.Ready[0])
	}
}

// No budgets.yaml is `enabled:false` with an EMPTY map, never null — a caller
// iterates it without branching on a case that does not exist.
func TestGatherBudgetIsNeverNull(t *testing.T) {
	for _, tc := range []struct {
		name   string
		budget BudgetReporter
	}{
		{"no gate at all", nil},
		{"a gate with nothing configured", fakeBudget{disabled: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Gather(context.Background(), Options{Tasks: demoTasks(), Budget: tc.budget})
			if err != nil {
				t.Fatal(err)
			}
			if got.Budget.Enabled {
				t.Error("enabled = true")
			}
			if got.Budget.Providers == nil {
				t.Error("providers = null; it must be an empty map")
			}
		})
	}
}

func TestGatherBudgetSnapshot(t *testing.T) {
	got, err := Gather(context.Background(), Options{
		Tasks: demoTasks(),
		Budget: fakeBudget{providers: map[string]budget.ProviderSnapshot{
			"claude": {TokensUsed: 850, TokenBudget: 1000, UsagePct: 85, Capped: false},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Budget.Enabled || got.Budget.Providers["claude"].TokensUsed != 850 {
		t.Errorf("budget = %+v", got.Budget)
	}
}

// A guardrail that cannot be read is an error, not a quiet zero. The window is
// the one number here somebody might act on.
func TestGatherReportsABudgetReadFailure(t *testing.T) {
	boom := errors.New("database is locked")
	_, err := Gather(context.Background(), Options{
		Tasks: demoTasks(), Budget: fakeBudget{err: boom},
	})
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want it to wrap %v", err, boom)
	}
}

func TestTextIsOneScreen(t *testing.T) {
	got, err := Gather(context.Background(), Options{
		ProjectID: "demo", ProjectRoot: "/tmp/demo", SpecRoot: "specs",
		Tasks: demoTasks(),
		Budget: fakeBudget{providers: map[string]budget.ProviderSnapshot{
			"claude": {TokensUsed: 850, TokenBudget: 1000, UsagePct: 85},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	var b strings.Builder
	if err := got.Text(&b); err != nil {
		t.Fatal(err)
	}
	out := b.String()

	// Thirty lines is the promise the command makes. A screen that scrolls is
	// a screen that gets skimmed, and this one exists to be read in full.
	if lines := strings.Count(out, "\n"); lines > 30 {
		t.Errorf("the summary is %d lines; it is meant to fit on one screen:\n%s", lines, out)
	}

	for _, want := range []string{
		"Project demo",
		"specs",
		"4 total, 1 done (25%)",
		"blocked", // present even when the count is small — a reader looks for it
		"Ready to dispatch: 1",
		"T-2",
		"claude",
		"850 / 1,000 tokens (85%)",
		"Safe to run",
		"orch status",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the summary does not mention %q:\n%s", want, out)
		}
	}

	// `orch run` dispatches work to an AI CLI that edits files and opens PRs.
	// It is the obvious next step and deliberately NOT under a heading that
	// says these are safe.
	safe := out[strings.Index(out, "Safe to run"):]
	if strings.Contains(safe, "orch run") {
		t.Errorf("`orch run` is listed as safe; it dispatches:\n%s", safe)
	}
}

// A project with nothing in it is the first thing a new user sees, and "ready:
// none" with no explanation is the least useful thing to tell them.
func TestTextExplainsWhyNothingIsReady(t *testing.T) {
	for _, tc := range []struct {
		name  string
		tasks []model.Task
		want  string
	}{
		{"no tasks at all", nil, "orch atomize"},
		{"everything done", []model.Task{
			task("T-1", 0, model.StatusDone, nil, 1, "a"),
		}, "everything is done"},
		{"work in flight", []model.Task{
			task("T-1", 0, model.StatusInProgress, nil, 1, "a"),
			task("T-2", 0, model.StatusTodo, []string{"T-1"}, 1, "b"),
		}, "waiting on a dependency"},
		{"all blocked", []model.Task{
			task("T-1", 0, model.StatusBlocked, nil, 1, "a"),
		}, "blocked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Gather(context.Background(), Options{Tasks: tc.tasks})
			if err != nil {
				t.Fatal(err)
			}
			var b strings.Builder
			if err := got.Text(&b); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(b.String(), "Ready to dispatch: none") {
				t.Fatalf("expected no ready tasks:\n%s", b.String())
			}
			if !strings.Contains(b.String(), tc.want) {
				t.Errorf("the summary does not say why (%q):\n%s", tc.want, b.String())
			}
		})
	}
}

// Nothing to start and something in flight: the useful next question is what
// that task is doing, not what to launch. An empty "safe to run" tail is the
// version of this page that leaves somebody with no next move.
func TestTextSuggestsSomethingWhenNothingIsReady(t *testing.T) {
	got, err := Gather(context.Background(), Options{Tasks: []model.Task{
		task("T-1", 0, model.StatusInProgress, nil, 1, "a"),
		task("T-2", 0, model.StatusTodo, []string{"T-1"}, 1, "b"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	if err := got.Text(&b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "orch tasks --status in-progress") {
		t.Errorf("no next step offered:\n%s", b.String())
	}
}

// A capped provider is the line that changes what somebody does next, so it
// says so in words rather than leaving them to compare two numbers.
func TestTextNamesACappedProvider(t *testing.T) {
	reset := "2026-09-12T18:00:00Z"
	got, err := Gather(context.Background(), Options{
		Tasks: demoTasks(),
		Budget: fakeBudget{providers: map[string]budget.ProviderSnapshot{
			"claude": {TokensUsed: 1000, TokenBudget: 1000, UsagePct: 100,
				Capped: true, ResetAt: &reset},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	if err := got.Text(&b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "CAPPED until "+reset) {
		t.Errorf("a capped provider is not called out:\n%s", b.String())
	}
}
