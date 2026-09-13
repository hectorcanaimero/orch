package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/hectorcanaimero/orch/internal/budget"
	"github.com/hectorcanaimero/orch/internal/model"
)

// ---- Fixtures ------------------------------------------------------------

func task(id string, phase int, modelName string, deps ...string) model.Task {
	return model.Task{
		ID: id, Phase: phase, Title: id + " title", Model: modelName,
		Status: model.StatusTodo, Dependencies: deps, EstimateHours: 1,
	}
}

func route(backend model.Backend, cliModel string, premium bool) model.RouteEntry {
	tier := model.TierStandard
	if premium {
		tier = model.TierPremium
	}
	return model.RouteEntry{
		Backend: backend, CLIModel: cliModel, Tier: tier, IsPremium: premium,
	}
}

// schedulerFixture wires a scheduler whose dispatches all go through the fake
// provider, so the tests exercise real forks and real semaphores without any
// coding CLI.
type schedulerFixture struct {
	t        *testing.T
	s        *Scheduler
	stateDir string
}

func newSchedulerFixture(t *testing.T, tasks []model.Task, routes map[string]model.RouteEntry, opts SchedulerOptions) *schedulerFixture {
	t.Helper()

	stateDir := filepath.Join(t.TempDir(), "state")
	fakeRoot := t.TempDir()

	// One canned response per backend, answering every task via _default.
	backends := map[model.Backend]bool{}
	for _, r := range routes {
		backends[r.Backend] = true
	}
	for b := range backends {
		dir := filepath.Join(fakeRoot, string(b))
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("mkdir fake: %v", err)
		}
		// Sleep long enough that a dispatch is still in flight while the
		// test inspects the semaphores. The reaper is not in this PR, so
		// nothing releases a slot: that is exactly what lets the caps be
		// asserted at their ceiling.
		write(t, filepath.Join(dir, "_default.out"), `{"type":"result","subtype":"success","is_error":false,"total_cost_usd":0,"usage":{}}`)
		write(t, filepath.Join(dir, "_default.sleep"), "30")
	}
	t.Setenv(FakeProviderEnv, fakeRoot)

	// Every provider on PromptStdin reads this file.
	promptDir := filepath.Join(stateDir, "prompts")
	if err := os.MkdirAll(promptDir, 0o750); err != nil {
		t.Fatalf("mkdir prompts: %v", err)
	}
	for _, tk := range tasks {
		write(t, PromptPathFor(stateDir, tk.ID), "prompt for "+tk.ID+"\n")
	}

	opts.StateDir = stateDir
	opts.Cwd = t.TempDir()
	if opts.TimeoutMultiplier == 0 {
		opts.TimeoutMultiplier = 1.5
	}
	if opts.RunID == "" {
		opts.RunID = "run-1"
	}

	q, err := NewTaskQueue(tasks)
	if err != nil {
		t.Fatalf("NewTaskQueue: %v", err)
	}
	s := NewScheduler(q, routes, opts)

	f := &schedulerFixture{t: t, s: s, stateDir: stateDir}
	t.Cleanup(f.killAll)
	return f
}

// killAll reaps whatever the test left running. Without it a test that
// asserts a cap leaves sleeping children behind for the next one.
//
// It kills the groups and then drains through the scheduler rather than
// calling Spawned.Wait itself: every dispatch has a supervising goroutine
// already waiting on it, and racing that was what made this fixture panic in
// CI with "Wait was already called". Wait is idempotent now, but going
// through DrainWait is also what a real shutdown does.
func (f *schedulerFixture) killAll() {
	for _, e := range f.s.InFlight() {
		e.Spawned.signalGroup(syscall.SIGKILL)
	}
	if _, err := f.s.DrainWait(context.Background(), 30*time.Second); err != nil {
		f.t.Errorf("draining the fixture: %v", err)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// ---- The concurrency contract -------------------------------------------

// TestRefillRespectsEveryCap is what this package exists to get right:
// 10 tasks across 3 providers, and neither the global ceiling nor any
// per-provider cap is ever exceeded.
//
// The assertion is on the observed in-flight counts, not on a return value:
// a scheduler that dispatched 4 claude tasks and then "corrected" itself
// would still have run 4 agents.
func TestRefillRespectsEveryCap(t *testing.T) {
	// Two of the three backends are names no adapter serves. Until G3.4 that
	// role was played by codex and opencode, which had no Go adapter yet;
	// every Python backend is ported now, so the case is reached the way it
	// will be reached from here on — a route naming a backend this binary
	// does not know, from a typo or a newer config.
	const (
		backendX = model.Backend("nosuch-x")
		backendO = model.Backend("nosuch-o")
	)
	routes := map[string]model.RouteEntry{
		"claude/opus":  route(model.BackendClaude, "opus", false),
		"claude/haiku": route(model.BackendClaude, "haiku", false),
		"unknown/x":    route(backendX, "some-model", false),
		"unknown/o":    route(backendO, "other-model", false),
	}
	// 10 independent tasks: 4 claude, 3 on each unusable backend.
	tasks := []model.Task{
		task("C-1", 1, "claude/opus"), task("C-2", 1, "claude/haiku"),
		task("C-3", 1, "claude/opus"), task("C-4", 1, "claude/haiku"),
		task("X-1", 1, "unknown/x"), task("X-2", 1, "unknown/x"), task("X-3", 1, "unknown/x"),
		task("O-1", 1, "unknown/o"), task("O-2", 1, "unknown/o"), task("O-3", 1, "unknown/o"),
	}

	const (
		globalMax = 5
		claudeCap = 2
		xCap      = 2
		oCap      = 3
	)

	f := newSchedulerFixture(t, tasks, routes, SchedulerOptions{
		Mode:      ModeAuto,
		GlobalMax: globalMax,
		PerProvider: map[string]int{
			"claude": claudeCap, string(backendX): xCap, string(backendO): oCap,
		},
	})

	// Get() refuses both unusable backends. That must not end the tick: the
	// claude tasks behind them in the ready set still have to run, or one bad
	// route sorting ahead of a working one stalls the whole run.
	started, err := f.s.Refill(context.Background())
	if err != nil {
		t.Fatalf("Refill: %v — an unusable backend must not end the tick", err)
	}

	// Everything claude could take is in flight, and it stopped at its cap.
	if got := f.s.Sems.Provider["claude"].Current(); got != claudeCap {
		t.Errorf("claude in flight = %d, want its cap of %d", got, claudeCap)
	}
	if got := f.s.Sems.Global.Current(); got > globalMax {
		t.Errorf("global in flight = %d, over the ceiling of %d", got, globalMax)
	}
	if started != claudeCap {
		t.Errorf("started = %d, want %d", started, claudeCap)
	}
	// The unusable ones say why, rather than looking not-ready.
	for _, id := range []string{"X-1", "O-1"} {
		if got := f.s.DeferReasons()[id]; !strings.HasPrefix(got, "backend-unavailable:") {
			t.Errorf("defer reason for %s = %q, want a backend-unavailable reason", id, got)
		}
	}
	// The semaphore count and the in-flight map must agree; a leak in either
	// direction is how a run deadlocks with nothing running.
	if len(f.s.InFlight()) != claudeCap {
		t.Errorf("in-flight map has %d, semaphores say %d", len(f.s.InFlight()), claudeCap)
	}
}

// TestRefillHonoursTheGlobalCeilingBelowProviderCaps: when the provider caps
// add up to more than global_max, the global one is what binds.
func TestRefillHonoursTheGlobalCeiling(t *testing.T) {
	routes := map[string]model.RouteEntry{"claude/opus": route(model.BackendClaude, "opus", false)}
	var tasks []model.Task
	for i := 1; i <= 6; i++ {
		tasks = append(tasks, task(fmt.Sprintf("C-%d", i), 1, "claude/opus"))
	}

	f := newSchedulerFixture(t, tasks, routes, SchedulerOptions{
		Mode:        ModeAuto,
		GlobalMax:   2,
		PerProvider: map[string]int{"claude": 10}, // deliberately looser
	})

	started, err := f.s.Refill(context.Background())
	if err != nil {
		t.Fatalf("Refill: %v", err)
	}
	if started != 2 {
		t.Errorf("started = %d, want the global ceiling of 2", started)
	}
	if got := f.s.Sems.Global.Current(); got != 2 {
		t.Errorf("global in flight = %d, want 2", got)
	}
	// A second tick must not sneak past it.
	again, err := f.s.Refill(context.Background())
	if err != nil {
		t.Fatalf("second Refill: %v", err)
	}
	if again != 0 {
		t.Errorf("second tick started %d more, want 0", again)
	}
}

// TestRefillLeaksNoCapacityOnError: a refused dispatch must give back both
// semaphore slots AND its task lock, or the run bleeds capacity until it
// stalls with nothing running and no way to tell why.
func TestRefillLeaksNoCapacityOnError(t *testing.T) {
	// The refusal comes from a backend no adapter serves; before G3.4 codex
	// played that part.
	const backend = model.Backend("nosuch")
	routes := map[string]model.RouteEntry{"unknown/x": route(backend, "some-model", false)}
	tasks := []model.Task{task("X-1", 1, "unknown/x")}

	f := newSchedulerFixture(t, tasks, routes, SchedulerOptions{
		Mode:         ModeAuto,
		GlobalMax:    4,
		PerProvider:  map[string]int{string(backend): 2},
		UseTaskLocks: true,
	})

	started, err := f.s.Refill(context.Background())
	if err != nil {
		t.Fatalf("Refill: %v", err)
	}
	if started != 0 {
		t.Fatalf("started = %d, want 0 — no adapter serves %q", started, backend)
	}

	if got := f.s.Sems.Global.Current(); got != 0 {
		t.Errorf("global slots still held: %d", got)
	}
	if got := f.s.Sems.Provider[string(backend)].Current(); got != 0 {
		t.Errorf("%s slots still held: %d", backend, got)
	}
	// And the lock file must be free for another orch.
	lock, err := TryAcquireTaskLock(f.stateDir, "X-1")
	if err != nil {
		t.Fatalf("re-acquire: %v", err)
	}
	if lock == nil {
		t.Error("the task lock was never released")
	}
	lock.Release()
}

// TestRefillRefusesABackendWithNoConfiguredCap. Treating "no cap configured"
// as "no limit" is how global_max quietly stops being a ceiling.
func TestRefillRefusesABackendWithNoConfiguredCap(t *testing.T) {
	routes := map[string]model.RouteEntry{"claude/opus": route(model.BackendClaude, "opus", false)}
	tasks := []model.Task{task("C-1", 1, "claude/opus")}

	f := newSchedulerFixture(t, tasks, routes, SchedulerOptions{
		Mode:        ModeAuto,
		GlobalMax:   4,
		PerProvider: map[string]int{"codex": 2}, // claude missing on purpose
	})

	started, err := f.s.Refill(context.Background())
	if err != nil {
		t.Fatalf("Refill: %v", err)
	}
	if started != 0 {
		t.Errorf("started = %d, want 0 — an unconfigured backend must not dispatch", started)
	}
	if got := f.s.Sems.Global.Current(); got != 0 {
		t.Errorf("global slots taken for a refused dispatch: %d", got)
	}
}

// ---- Dependencies and ordering ------------------------------------------

func TestRefillDispatchesOnlyWhatIsReady(t *testing.T) {
	routes := map[string]model.RouteEntry{"claude/opus": route(model.BackendClaude, "opus", false)}
	tasks := []model.Task{
		task("A-1", 1, "claude/opus"),
		task("A-2", 1, "claude/opus", "A-1"), // blocked on A-1
		task("A-3", 2, "claude/opus"),
	}

	f := newSchedulerFixture(t, tasks, routes, SchedulerOptions{
		Mode:        ModeAuto,
		GlobalMax:   8,
		PerProvider: map[string]int{"claude": 8},
	})

	started, err := f.s.Refill(context.Background())
	if err != nil {
		t.Fatalf("Refill: %v", err)
	}
	if started != 2 {
		t.Fatalf("started = %d, want 2 (A-1 and A-3; A-2 waits on A-1)", started)
	}
	for _, e := range f.s.InFlight() {
		if e.Task.ID == "A-2" {
			t.Error("A-2 dispatched while its dependency was not done")
		}
	}
}

func TestRefillRespectsMaxTasks(t *testing.T) {
	routes := map[string]model.RouteEntry{"claude/opus": route(model.BackendClaude, "opus", false)}
	var tasks []model.Task
	for i := 1; i <= 5; i++ {
		tasks = append(tasks, task(fmt.Sprintf("C-%d", i), 1, "claude/opus"))
	}

	f := newSchedulerFixture(t, tasks, routes, SchedulerOptions{
		Mode:        ModeAuto,
		GlobalMax:   8,
		PerProvider: map[string]int{"claude": 8},
		MaxTasks:    3,
	})

	started, err := f.s.Refill(context.Background())
	if err != nil {
		t.Fatalf("Refill: %v", err)
	}
	if started != 3 {
		t.Errorf("started = %d, want the MaxTasks cap of 3", started)
	}
}

func TestRefillRespectsOnlyGlob(t *testing.T) {
	routes := map[string]model.RouteEntry{"claude/opus": route(model.BackendClaude, "opus", false)}
	tasks := []model.Task{
		task("F0.T1", 1, "claude/opus"),
		task("F0.T2", 1, "claude/opus"),
		task("F1.T1", 1, "claude/opus"),
	}

	f := newSchedulerFixture(t, tasks, routes, SchedulerOptions{
		Mode:        ModeAuto,
		GlobalMax:   8,
		PerProvider: map[string]int{"claude": 8},
		Only:        "F0.*",
	})

	started, err := f.s.Refill(context.Background())
	if err != nil {
		t.Fatalf("Refill: %v", err)
	}
	if started != 2 {
		t.Fatalf("started = %d, want the 2 matching F0.*", started)
	}
	for _, e := range f.s.InFlight() {
		if !strings.HasPrefix(e.Task.ID, "F0.") {
			t.Errorf("%s dispatched despite --only F0.*", e.Task.ID)
		}
	}
}

// ---- The semi-mode gate --------------------------------------------------

// scriptedGate answers from a per-task script and records what it was asked.
type scriptedGate struct {
	mu      sync.Mutex
	answers map[string]Decision
	asked   []string
	reasons map[string]string
}

func (g *scriptedGate) Ask(t model.Task, reason string) Decision {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.asked = append(g.asked, t.ID)
	if g.reasons == nil {
		g.reasons = map[string]string{}
	}
	g.reasons[t.ID] = reason
	if d, ok := g.answers[t.ID]; ok {
		return d
	}
	return DecisionDispatch
}

func TestSemiGateDecisions(t *testing.T) {
	tests := []struct {
		name        string
		answer      Decision
		wantStarted int
		wantStatus  model.Status
		wantDrain   bool
	}{
		{name: "dispatch goes ahead", answer: DecisionDispatch, wantStarted: 1, wantStatus: model.StatusInProgress},
		{name: "defer leaves it todo", answer: DecisionDefer, wantStarted: 0, wantStatus: model.StatusTodo},
		{name: "skip blocks it", answer: DecisionSkip, wantStarted: 0, wantStatus: model.StatusBlocked},
		{name: "quit drains", answer: DecisionQuit, wantStarted: 0, wantStatus: model.StatusTodo, wantDrain: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			routes := map[string]model.RouteEntry{"claude/opus": route(model.BackendClaude, "opus", true)}
			tasks := []model.Task{task("C-1", 1, "claude/opus")}

			f := newSchedulerFixture(t, tasks, routes, SchedulerOptions{
				Mode:        ModeSemi,
				GlobalMax:   4,
				PerProvider: map[string]int{"claude": 4},
			})
			gate := &scriptedGate{answers: map[string]Decision{"C-1": tt.answer}}
			f.s.Gate = gate

			started, err := f.s.Refill(context.Background())
			if err != nil {
				t.Fatalf("Refill: %v", err)
			}
			if started != tt.wantStarted {
				t.Errorf("started = %d, want %d", started, tt.wantStarted)
			}
			if got, _ := f.s.Queue.Status("C-1"); got != tt.wantStatus {
				t.Errorf("status = %q, want %q", got, tt.wantStatus)
			}
			if f.s.Draining() != tt.wantDrain {
				t.Errorf("Draining = %v, want %v", f.s.Draining(), tt.wantDrain)
			}
			if len(gate.asked) != 1 {
				t.Errorf("the gate was asked %d times, want 1", len(gate.asked))
			}
		})
	}
}

// TestSemiGateOnlyAsksAboutCriticalTasks: a standard-tier task with nothing
// critical about it goes straight through, even in semi mode. Asking about
// everything is how an operator learns to hold down `y`.
func TestSemiGateOnlyAsksAboutCriticalTasks(t *testing.T) {
	routes := map[string]model.RouteEntry{
		"claude/opus":  route(model.BackendClaude, "opus", true),   // premium → gated
		"claude/haiku": route(model.BackendClaude, "haiku", false), // standard → not
	}
	tasks := []model.Task{
		task("C-1", 1, "claude/opus"),
		task("C-2", 1, "claude/haiku"),
	}

	f := newSchedulerFixture(t, tasks, routes, SchedulerOptions{
		Mode:        ModeSemi,
		GlobalMax:   4,
		PerProvider: map[string]int{"claude": 4},
	})
	gate := &scriptedGate{answers: map[string]Decision{}}
	f.s.Gate = gate

	if _, err := f.s.Refill(context.Background()); err != nil {
		t.Fatalf("Refill: %v", err)
	}
	if len(gate.asked) != 1 || gate.asked[0] != "C-1" {
		t.Errorf("gate asked about %v, want only the premium C-1", gate.asked)
	}
	if got := gate.reasons["C-1"]; got != "premium tier (premium)" {
		t.Errorf("reason = %q", got)
	}
}

// TestAutoModeNeverAsks: the gate is inert outside semi mode, even when one
// is wired up.
func TestAutoModeNeverAsks(t *testing.T) {
	routes := map[string]model.RouteEntry{"claude/opus": route(model.BackendClaude, "opus", true)}
	tasks := []model.Task{task("C-1", 1, "claude/opus")}

	f := newSchedulerFixture(t, tasks, routes, SchedulerOptions{
		Mode:        ModeAuto,
		GlobalMax:   4,
		PerProvider: map[string]int{"claude": 4},
	})
	gate := &scriptedGate{answers: map[string]Decision{"C-1": DecisionSkip}}
	f.s.Gate = gate

	started, err := f.s.Refill(context.Background())
	if err != nil {
		t.Fatalf("Refill: %v", err)
	}
	if len(gate.asked) != 0 {
		t.Errorf("the gate was consulted in auto mode: %v", gate.asked)
	}
	if started != 1 {
		t.Errorf("started = %d, want 1", started)
	}
}

// TestDeferIsNotOfferedTwice: a deferred task is skipped on later ticks
// without asking again, which is what makes "N" mean "not this run".
func TestDeferIsNotOfferedTwice(t *testing.T) {
	routes := map[string]model.RouteEntry{"claude/opus": route(model.BackendClaude, "opus", true)}
	tasks := []model.Task{task("C-1", 1, "claude/opus")}

	f := newSchedulerFixture(t, tasks, routes, SchedulerOptions{
		Mode:        ModeSemi,
		GlobalMax:   4,
		PerProvider: map[string]int{"claude": 4},
	})
	gate := &scriptedGate{answers: map[string]Decision{"C-1": DecisionDefer}}
	f.s.Gate = gate

	for i := 0; i < 3; i++ {
		if _, err := f.s.Refill(context.Background()); err != nil {
			t.Fatalf("Refill %d: %v", i, err)
		}
	}
	if len(gate.asked) != 1 {
		t.Errorf("the gate was asked %d times across 3 ticks, want 1", len(gate.asked))
	}
	if got := f.s.DeferReasons()["C-1"]; got != "deferred-by-operator" {
		t.Errorf("defer reason = %q", got)
	}
}

// ---- The budget gate, and its fail-open ---------------------------------

type stubBudget struct {
	decision budget.Decision
	err      error
	calls    int
}

func (b *stubBudget) CanDispatch(context.Context, string) (budget.Decision, error) {
	b.calls++
	return b.decision, b.err
}

func TestBudgetGate(t *testing.T) {
	resetAt := time.Date(2026, 9, 12, 15, 0, 0, 0, time.UTC)

	tests := []struct {
		name        string
		stub        *stubBudget
		wantStarted int
		wantReason  string
	}{
		{
			name:        "an open budget dispatches",
			stub:        &stubBudget{decision: budget.Decision{OK: true}},
			wantStarted: 1,
		},
		{
			name: "a capped provider defers with a reason",
			stub: &stubBudget{decision: budget.Decision{
				OK: false, Reason: "claude over threshold", ResetAt: resetAt,
			}},
			wantStarted: 0,
			wantReason:  "blocked-by-budget:claude",
		},
		{
			// The shape of bug 5: Python swallows a database error inside the
			// gate and returns zero rows, which reads as zero spend and
			// therefore as "go ahead". Go returns the error and the run
			// carries on deliberately, with a log line beside the decision.
			name:        "an unreadable budget fails OPEN",
			stub:        &stubBudget{err: errors.New("database is locked")},
			wantStarted: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			routes := map[string]model.RouteEntry{"claude/opus": route(model.BackendClaude, "opus", false)}
			tasks := []model.Task{task("C-1", 1, "claude/opus")}

			f := newSchedulerFixture(t, tasks, routes, SchedulerOptions{
				Mode:        ModeAuto,
				GlobalMax:   4,
				PerProvider: map[string]int{"claude": 4},
			})
			f.s.Budget = tt.stub

			started, err := f.s.Refill(context.Background())
			if err != nil {
				t.Fatalf("Refill: %v", err)
			}
			if started != tt.wantStarted {
				t.Errorf("started = %d, want %d", started, tt.wantStarted)
			}
			if tt.stub.calls != 1 {
				t.Errorf("the budget was consulted %d times, want 1", tt.stub.calls)
			}
			if got := f.s.DeferReasons()["C-1"]; got != tt.wantReason {
				t.Errorf("defer reason = %q, want %q", got, tt.wantReason)
			}
		})
	}
}

// TestBudgetGateRunsBeforeTheSemaphores. Checking after would acquire and
// release a slot on every tick for a capped provider, so the in-flight count
// AS-05 reads would flap.
func TestBudgetGateRunsBeforeTheSemaphores(t *testing.T) {
	routes := map[string]model.RouteEntry{"claude/opus": route(model.BackendClaude, "opus", false)}
	tasks := []model.Task{task("C-1", 1, "claude/opus")}

	f := newSchedulerFixture(t, tasks, routes, SchedulerOptions{
		Mode:        ModeAuto,
		GlobalMax:   4,
		PerProvider: map[string]int{"claude": 4},
	})
	f.s.Budget = &stubBudget{decision: budget.Decision{OK: false, Reason: "capped"}}

	if _, err := f.s.Refill(context.Background()); err != nil {
		t.Fatalf("Refill: %v", err)
	}
	if got := f.s.Sems.Global.Current(); got != 0 {
		t.Errorf("global slots held after a budget refusal: %d", got)
	}
	if got := f.s.Sems.Provider["claude"].Current(); got != 0 {
		t.Errorf("claude slots held after a budget refusal: %d", got)
	}
}

// ---- Task locks ----------------------------------------------------------

// TestTaskLockIsHeldWhileTheChildRuns: the lock lives on the in-flight entry,
// not in a local variable, so it survives until the reaper releases it.
func TestTaskLockIsHeldWhileTheChildRuns(t *testing.T) {
	routes := map[string]model.RouteEntry{"claude/opus": route(model.BackendClaude, "opus", false)}
	tasks := []model.Task{task("C-1", 1, "claude/opus")}

	f := newSchedulerFixture(t, tasks, routes, SchedulerOptions{
		Mode:         ModeAuto,
		GlobalMax:    4,
		PerProvider:  map[string]int{"claude": 4},
		UseTaskLocks: true,
	})

	if _, err := f.s.Refill(context.Background()); err != nil {
		t.Fatalf("Refill: %v", err)
	}

	var held *InFlight
	for _, e := range f.s.InFlight() {
		held = e
	}
	if held == nil {
		t.Fatal("nothing dispatched")
	}
	if held.Lock == nil {
		t.Fatal("the in-flight entry is not holding a lock")
	}

	// A second orch must not be able to take it.
	second, err := TryAcquireTaskLock(f.stateDir, "C-1")
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	if second != nil {
		second.Release()
		t.Error("the task lock was available while the child was running")
	}

	// And releasing it frees the task for someone else.
	held.Lock.Release()
	third, err := TryAcquireTaskLock(f.stateDir, "C-1")
	if err != nil {
		t.Fatalf("third acquire: %v", err)
	}
	if third == nil {
		t.Error("the lock was not released")
	}
	third.Release()
}

// TestTaskLockSkipsATaskAnotherOrchOwns.
func TestTaskLockSkipsATaskAnotherOrchOwns(t *testing.T) {
	routes := map[string]model.RouteEntry{"claude/opus": route(model.BackendClaude, "opus", false)}
	tasks := []model.Task{task("C-1", 1, "claude/opus"), task("C-2", 1, "claude/opus")}

	f := newSchedulerFixture(t, tasks, routes, SchedulerOptions{
		Mode:         ModeAuto,
		GlobalMax:    4,
		PerProvider:  map[string]int{"claude": 4},
		UseTaskLocks: true,
	})

	// Stand in for another orch holding C-1.
	other, err := TryAcquireTaskLock(f.stateDir, "C-1")
	if err != nil || other == nil {
		t.Fatalf("pre-acquire: lock=%v err=%v", other, err)
	}
	defer other.Release()

	started, err := f.s.Refill(context.Background())
	if err != nil {
		t.Fatalf("Refill: %v", err)
	}
	if started != 1 {
		t.Fatalf("started = %d, want 1 (C-2 only)", started)
	}
	for _, e := range f.s.InFlight() {
		if e.Task.ID == "C-1" {
			t.Error("dispatched a task another orch holds the lock on")
		}
	}
}
