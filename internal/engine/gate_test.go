package engine

import (
	"bytes"
	"strings"
	"sync"
	"testing"

	"github.com/hectorcanaimero/orch/internal/model"
)

func gated(files []string, hours float64, phase int, modelName string) model.Task {
	return model.Task{
		ID: "G-1", Phase: phase, Title: "gated", Model: modelName,
		Status: model.StatusTodo, Files: files, EstimateHours: hours,
	}
}

func TestIsCritical(t *testing.T) {
	routes := map[string]model.RouteEntry{
		"premium/m":  route(model.BackendClaude, "opus", true),
		"standard/m": route(model.BackendClaude, "haiku", false),
	}

	tests := []struct {
		name string
		task model.Task
		want bool
	}{
		{
			name: "a premium route",
			task: gated(nil, 1, 1, "premium/m"),
			want: true,
		},
		{
			name: "a standard route with nothing else",
			task: gated([]string{"src/app.ts"}, 1, 1, "standard/m"),
		},
		{
			name: "a migration",
			task: gated([]string{"supabase/migrations/0001_init.sql"}, 1, 1, "standard/m"),
			want: true,
		},
		{
			name: "an edge function entry point",
			task: gated([]string{"supabase/functions/pay/index.ts"}, 1, 1, "standard/m"),
			want: true,
		},
		{
			name: "a helper beside an edge function is not the entry point",
			task: gated([]string{"supabase/functions/pay/helpers.ts"}, 1, 1, "standard/m"),
		},
		{
			name: "a critical package library",
			task: gated([]string{"packages/core/lib/billing/charge.ts"}, 1, 1, "standard/m"),
			want: true,
		},
		{
			name: "another package library is not critical",
			task: gated([]string{"packages/core/lib/utils/date.ts"}, 1, 1, "standard/m"),
		},
		{
			name: "a critical-looking path outside packages/",
			task: gated([]string{"vendor/x/lib/auth/y.ts"}, 1, 1, "standard/m"),
		},
		{
			name: "a high estimate alone",
			task: gated(nil, 10, 1, "standard/m"),
			want: true,
		},
		{
			name: "just under the threshold",
			task: gated(nil, 9.5, 1, "standard/m"),
		},
		{
			name: "phase 10 on a standard route is not critical by itself",
			task: gated(nil, 1, 10, "standard/m"),
		},
		{
			name: "a task whose model has no route",
			task: gated(nil, 1, 1, "unrouted/m"),
		},
		{
			name: "no files at all",
			task: gated(nil, 1, 1, "standard/m"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsCritical(tt.task, routes); got != tt.want {
				t.Errorf("IsCritical = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGateReason(t *testing.T) {
	routes := map[string]model.RouteEntry{
		"premium/m":  route(model.BackendClaude, "opus", true),
		"standard/m": route(model.BackendClaude, "haiku", false),
	}

	tests := []struct {
		name string
		task model.Task
		want string
	}{
		{
			name: "premium wins over everything else",
			task: gated([]string{"supabase/migrations/x.sql"}, 20, 1, "premium/m"),
			want: "premium tier (premium)",
		},
		{
			name: "migrations",
			task: gated([]string{"supabase/migrations/x.sql"}, 1, 1, "standard/m"),
			want: "touches supabase/migrations/**",
		},
		{
			name: "edge functions",
			task: gated([]string{"supabase/functions/pay/index.ts"}, 1, 1, "standard/m"),
			want: "touches supabase/functions/**/index.ts",
		},
		{
			name: "critical packages",
			task: gated([]string{"packages/core/lib/auth/x.ts"}, 1, 1, "standard/m"),
			want: "touches packages/*/lib/{auth,billing,security}/**",
		},
		{
			// Python interpolates the float, so a 12-hour estimate reads
			// "12.0" and not "12".
			name: "high effort renders both floats Python-style",
			task: gated(nil, 12, 1, "standard/m"),
			want: "estimate_hours=12.0 >= 10.0",
		},
		{
			name: "nothing matched",
			task: gated(nil, 1, 1, "standard/m"),
			want: "critical",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GateReason(tt.task, routes); got != tt.want {
				t.Errorf("GateReason = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTerminalGateDecodesAnswers(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  Decision
	}{
		{name: "y dispatches", input: "y\n", want: DecisionDispatch},
		{name: "uppercase Y too", input: "Y\n", want: DecisionDispatch},
		{name: "enter defers", input: "\n", want: DecisionDefer},
		{name: "n defers", input: "n\n", want: DecisionDefer},
		{name: "s skips", input: "s\n", want: DecisionSkip},
		{name: "q quits", input: "q\n", want: DecisionQuit},
		{name: "surrounding spaces are trimmed", input: "  y  \n", want: DecisionDispatch},
		{name: "an unrecognised key re-prompts", input: "x\nzz\ns\n", want: DecisionSkip},
		{name: "end of input reads as quit", input: "", want: DecisionQuit},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			g := TerminalGate{In: strings.NewReader(tt.input), Out: &out}
			if got := g.Ask(gated(nil, 1, 1, "m"), "premium tier (premium)"); got != tt.want {
				t.Errorf("Ask = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestTerminalGateShowsWhatTheOperatorNeeds: the prompt is the only thing
// between an operator and dispatching a premium agent by reflex, so it has to
// say which task, why it stopped, and what the keys do.
func TestTerminalGateShowsWhatTheOperatorNeeds(t *testing.T) {
	var out bytes.Buffer
	g := TerminalGate{In: strings.NewReader("y\n"), Out: &out}
	task := model.Task{
		ID: "B-020", Phase: 10, Title: "Wire the billing webhook",
		Model: "claude/opus", Files: []string{"packages/core/lib/billing/x.ts"},
		EstimateHours: 12,
	}
	g.Ask(task, "premium tier (premium)")

	shown := out.String()
	for _, want := range []string{
		"B-020", "phase 10", "Wire the billing webhook", "claude/opus",
		"packages/core/lib/billing/x.ts", "12.0h", "premium tier (premium)",
		"[y]dispatch", "[N]defer", "[s]skip-permanently", "[q]uit",
	} {
		if !strings.Contains(shown, want) {
			t.Errorf("the prompt never showed %q:\n%s", want, shown)
		}
	}
}

func TestTerminalGateShowsEmptyFilesAsABracketPair(t *testing.T) {
	var out bytes.Buffer
	g := TerminalGate{In: strings.NewReader("y\n"), Out: &out}
	g.Ask(gated(nil, 1, 1, "m"), "critical")
	if !strings.Contains(out.String(), "files: []") {
		t.Errorf("want an empty files line, got:\n%s", out.String())
	}
}

// ---- The semaphores ------------------------------------------------------

func TestSem(t *testing.T) {
	s := NewSem(2)
	for i := 1; i <= 2; i++ {
		if !s.TryAcquire() {
			t.Fatalf("acquisition %d should have succeeded at a cap of 2", i)
		}
	}
	if s.TryAcquire() {
		t.Error("the third acquisition should fail at a cap of 2")
	}
	if got := s.Current(); got != 2 {
		t.Errorf("Current = %d, want 2", got)
	}
	s.Release()
	if got := s.Current(); got != 1 {
		t.Errorf("Current after release = %d, want 1", got)
	}
	if !s.TryAcquire() {
		t.Error("the freed slot should be available")
	}
}

func TestSemReleaseFloorsAtZero(t *testing.T) {
	s := NewSem(1)
	s.Release()
	s.Release()
	if got := s.Current(); got != 0 {
		t.Errorf("Current = %d, want 0 — a double release must not hand out phantom capacity", got)
	}
	if !s.TryAcquire() {
		t.Error("the one real slot should still be available")
	}
	if s.TryAcquire() {
		t.Error("and only one")
	}
}

func TestSemZeroCapAdmitsNothing(t *testing.T) {
	if NewSem(0).TryAcquire() {
		t.Error("a cap of 0 must admit nothing")
	}
	if NewSem(-1).TryAcquire() {
		t.Error("a negative cap must admit nothing")
	}
}

// TestSemIsRaceFree runs the -race detector over concurrent traffic. The
// scheduler is single-threaded today, but the reaper will release from the
// goroutine that reaps, so the counter has to be safe before that lands.
func TestSemIsRaceFree(t *testing.T) {
	s := NewSem(4)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if s.TryAcquire() {
				if got := s.Current(); got > 4 {
					t.Errorf("Current = %d, over the cap", got)
				}
				s.Release()
			}
		}()
	}
	wg.Wait()
	if got := s.Current(); got != 0 {
		t.Errorf("Current = %d after everything released, want 0", got)
	}
}

func TestSemsReleasesTheProviderSlotWhenTheGlobalRefuses(t *testing.T) {
	sems := NewSems(1, map[string]int{"claude": 5})

	if !sems.TryAcquire("claude") {
		t.Fatal("the first should succeed")
	}
	if sems.TryAcquire("claude") {
		t.Fatal("the second should be refused by the global cap of 1")
	}
	// The provider slot the refused attempt took must have been given back,
	// or claude's count creeps up by one per tick while nothing runs.
	if got := sems.Provider["claude"].Current(); got != 1 {
		t.Errorf("claude holds %d, want 1 — the refused attempt leaked a slot", got)
	}
}

func TestSemsRefusesAnUnknownBackend(t *testing.T) {
	sems := NewSems(4, map[string]int{"claude": 2})
	if sems.TryAcquire("codex") {
		t.Error("a backend with no configured cap must not dispatch")
	}
	if got := sems.Global.Current(); got != 0 {
		t.Errorf("global holds %d after refusing an unknown backend", got)
	}
	if sems.Knows("codex") {
		t.Error("Knows(codex) should be false")
	}
}
