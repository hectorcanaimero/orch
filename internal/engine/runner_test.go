package engine

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/state"
)

// newRunnerFixture builds a runner over a reap fixture, with the clock and
// the signal channel under the test's control so nothing waits on real time.
type runnerFixture struct {
	*reapFixture
	r       *Runner
	signals chan os.Signal
	// slept records every sleep the loop asked for, so the budget pause can
	// be asserted without spending it.
	slept []time.Duration
	mu    sync.Mutex
	now   time.Time
}

func newRunnerFixture(t *testing.T, tasks []model.Task, responses map[string]fakeResponse, opts SchedulerOptions) *runnerFixture {
	t.Helper()
	rf := newReapFixture(t, tasks, responses, opts)

	f := &runnerFixture{
		reapFixture: rf,
		signals:     make(chan os.Signal, 4),
		now:         time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC),
	}
	f.r = NewRunner(rf.s)
	f.r.Signals = f.signals
	f.r.now = func() time.Time {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.now
	}
	f.r.sleep = func(d time.Duration) {
		f.mu.Lock()
		f.slept = append(f.slept, d)
		f.now = f.now.Add(d)
		f.mu.Unlock()
	}
	rf.s.now = f.r.now
	return f
}

func (f *runnerFixture) sleeps() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Duration(nil), f.slept...)
}

// runWithin runs the loop and fails if it does not finish in time, so a
// hanging loop is a failure rather than a stuck suite.
func (f *runnerFixture) runWithin(t *testing.T, d time.Duration) int {
	t.Helper()
	type result struct {
		code int
		err  error
	}
	done := make(chan result, 1)
	go func() {
		code, err := f.r.Run(context.Background())
		done <- result{code, err}
	}()

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("Run: %v", got.err)
		}
		return got.code
	case <-time.After(d):
		t.Fatal("the run loop did not finish")
		return -1
	}
}

// ---- Termination ---------------------------------------------------------

func TestRunLoopFinishesTheWork(t *testing.T) {
	f := newRunnerFixture(t,
		[]model.Task{task("C-1", 1, "claude/opus"), task("C-2", 1, "claude/opus", "C-1")},
		map[string]fakeResponse{"C-1": okResponse(), "C-2": okResponse()},
		SchedulerOptions{})

	if code := f.runWithin(t, 30*time.Second); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}

	for _, id := range []string{"C-1", "C-2"} {
		if got, _ := f.s.Queue.Status(id); got != model.StatusDone {
			t.Errorf("%s = %q, want done", id, got)
		}
	}
	// C-2 depended on C-1, so the loop had to come back round for it — that
	// is the whole point of looping rather than dispatching once.
	if f.s.Dispatched() != 2 {
		t.Errorf("dispatched = %d, want 2", f.s.Dispatched())
	}
}

func TestRunLoopExitsWithNothingToDo(t *testing.T) {
	f := newRunnerFixture(t,
		[]model.Task{{ID: "C-1", Phase: 1, Title: "t", Model: "claude/opus", Status: model.StatusDone}},
		map[string]fakeResponse{"C-1": okResponse()},
		SchedulerOptions{})

	if code := f.runWithin(t, 10*time.Second); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if f.s.Dispatched() != 0 {
		t.Errorf("dispatched %d with nothing to do", f.s.Dispatched())
	}
}

// TestRunLoopStopsWhenEverythingIsBlocked: a task that fails past its
// attempts blocks, and the loop ends rather than spinning on a ready set that
// will never refill.
func TestRunLoopStopsWhenEverythingIsBlocked(t *testing.T) {
	f := newRunnerFixture(t,
		[]model.Task{task("C-1", 1, "claude/opus")},
		map[string]fakeResponse{"C-1": failResponse("authentication_error: bad key")},
		SchedulerOptions{})

	if code := f.runWithin(t, 30*time.Second); code != 0 {
		t.Errorf("exit code = %d, want 0 — a blocked task is a finished run, not a crash", code)
	}
	if got, _ := f.s.Queue.Status("C-1"); got != model.StatusBlocked {
		t.Errorf("status = %q, want blocked", got)
	}
}

// TestRunLoopWaitsOutARetry: the loop must not decide the work is done while
// a retry is sitting in the queue on a backoff.
func TestRunLoopWaitsOutARetry(t *testing.T) {
	f := newRunnerFixture(t,
		[]model.Task{task("C-1", 1, "claude/opus")},
		map[string]fakeResponse{"C-1": failResponse("503 service unavailable")},
		SchedulerOptions{})

	// Both attempts fail, so the run ends with the task blocked — but it
	// must have taken two dispatches to get there.
	if code := f.runWithin(t, 30*time.Second); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if f.s.Dispatched() != 2 {
		t.Errorf("dispatched = %d, want 2 (the first attempt and its retry)", f.s.Dispatched())
	}
	if got, _ := f.s.Queue.Status("C-1"); got != model.StatusBlocked {
		t.Errorf("status = %q, want blocked after the last attempt", got)
	}
}

// ---- Signals -------------------------------------------------------------

// TestSignalDrainsAndExits130: a signal stops new dispatches, lets what is
// running finish, and exits with the shell's interrupted code.
func TestSignalDrainsAndExits130(t *testing.T) {
	f := newRunnerFixture(t,
		[]model.Task{task("C-1", 1, "claude/opus"), task("C-2", 1, "claude/opus")},
		map[string]fakeResponse{"C-1": okResponse(), "C-2": okResponse()},
		SchedulerOptions{GlobalMax: 1, PerProvider: map[string]int{"claude": 1}})

	f.signals <- syscall.SIGINT

	if code := f.runWithin(t, 30*time.Second); code != ExitInterrupted {
		t.Errorf("exit code = %d, want %d", code, ExitInterrupted)
	}
	if !f.s.Draining() {
		t.Error("the scheduler should be draining after a signal")
	}
	if len(f.s.InFlight()) != 0 {
		t.Errorf("%d children left running after the drain", len(f.s.InFlight()))
	}
	// The signal arrived before anything was dispatched, so nothing should
	// have started: draining means no NEW work.
	if f.s.Dispatched() != 0 {
		t.Errorf("dispatched %d after the drain began", f.s.Dispatched())
	}
}

func TestSigtermDrainsLikeSigint(t *testing.T) {
	f := newRunnerFixture(t,
		[]model.Task{task("C-1", 1, "claude/opus")},
		map[string]fakeResponse{"C-1": okResponse()},
		SchedulerOptions{})

	f.signals <- syscall.SIGTERM

	if code := f.runWithin(t, 30*time.Second); code != ExitInterrupted {
		t.Errorf("exit code = %d, want %d — kill <pid> behaves like Ctrl-C", code, ExitInterrupted)
	}
}

// TestSecondSignalKillsTheGroups: the first signal is polite, the second is
// not. A child ignoring SIGTERM must still die.
func TestSecondSignalKillsTheGroups(t *testing.T) {
	f := newRunnerFixture(t,
		[]model.Task{task("C-1", 1, "claude/opus")},
		map[string]fakeResponse{"C-1": {out: "never printed", exitCode: "0"}},
		SchedulerOptions{})
	// Dispatch first, so there is something in flight to kill.
	if _, err := f.s.Refill(context.Background()); err != nil {
		t.Fatalf("Refill: %v", err)
	}
	if len(f.s.InFlight()) != 1 {
		t.Fatalf("nothing in flight to interrupt")
	}

	f.signals <- syscall.SIGINT
	f.signals <- syscall.SIGINT

	if code := f.runWithin(t, 30*time.Second); code != ExitInterrupted {
		t.Errorf("exit code = %d, want %d", code, ExitInterrupted)
	}
	if len(f.s.InFlight()) != 0 {
		t.Errorf("%d children survived two signals", len(f.s.InFlight()))
	}
}

// TestCancelledContextDrains: a cancelled context behaves like a signal —
// stop dispatching, let what is running finish.
func TestCancelledContextDrains(t *testing.T) {
	f := newRunnerFixture(t,
		[]model.Task{task("C-1", 1, "claude/opus")},
		map[string]fakeResponse{"C-1": okResponse()},
		SchedulerOptions{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	code, err := f.r.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != ExitInterrupted {
		t.Errorf("exit code = %d, want %d", code, ExitInterrupted)
	}
}

// ---- The budget window ---------------------------------------------------

type stubWindow struct {
	capped    bool
	cappedErr error
	resetAt   time.Time
	resetErr  error
	// allCappedCalls and resetCalls prove the ordering.
	allCappedCalls int
	resetCalls     int
	mu             sync.Mutex
}

func (w *stubWindow) AllCapped(context.Context) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.allCappedCalls++
	return w.capped, w.cappedErr
}

func (w *stubWindow) EarliestReset(context.Context) (time.Time, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.resetCalls++
	return w.resetAt, w.resetErr
}

func (w *stubWindow) counts() (allCapped, reset int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.allCappedCalls, w.resetCalls
}

// TestEarliestResetIsNeverConsultedWithoutAllCapped is the trap in
// budget.EarliestReset: it answers with the soonest reset among the providers
// that are BLOCKED, not with a promise that they all are. Sleeping on it
// without checking AllCapped would park a run that still had a free provider.
func TestEarliestResetIsNeverConsultedWithoutAllCapped(t *testing.T) {
	f := newRunnerFixture(t,
		[]model.Task{task("C-1", 1, "claude/opus")},
		map[string]fakeResponse{"C-1": okResponse()},
		SchedulerOptions{})

	w := &stubWindow{capped: false, resetAt: f.now.Add(time.Hour)}
	f.r.Budget = w

	if code := f.runWithin(t, 30*time.Second); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}

	allCapped, reset := w.counts()
	if allCapped == 0 {
		t.Error("AllCapped was never consulted")
	}
	if reset != 0 {
		t.Errorf("EarliestReset was consulted %d times while providers were free", reset)
	}
	for _, d := range f.sleeps() {
		if d >= time.Second {
			t.Errorf("the loop slept %v with a free provider", d)
		}
	}
}

// TestSleepsUntilTheWindowResets, in chunks, so a signal still lands.
func TestSleepsUntilTheWindowResets(t *testing.T) {
	f := newRunnerFixture(t,
		[]model.Task{task("C-1", 1, "claude/opus")},
		map[string]fakeResponse{"C-1": okResponse()},
		SchedulerOptions{})

	w := &stubWindow{capped: true, resetAt: f.now.Add(2 * time.Minute)}
	f.r.Budget = w

	// Uncap once the loop has actually parked twice, so the run can end.
	// Counting only the long sleeps matters: the loop also sleeps a 200ms
	// tick between passes, and counting those would uncap the window before
	// a single budget pause had happened — which is what this test is for.
	go func() {
		for {
			if countLongSleeps(f.sleeps()) >= 2 {
				w.mu.Lock()
				w.capped = false
				w.mu.Unlock()
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	if code := f.runWithin(t, 30*time.Second); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}

	long := longSleeps(f.sleeps())
	if len(long) == 0 {
		t.Fatal("the loop never parked while every provider was capped")
	}
	for _, d := range long {
		if d > budgetSleepChunk {
			t.Errorf("slept %v in one go, over the %v chunk — a signal would not land", d, budgetSleepChunk)
		}
	}

	// The pause is recorded against "-", because it is the run's and not any
	// one task's.
	found := false
	f.backend.mu.Lock()
	for _, e := range f.backend.events {
		if e.eventType == EventBudgetPause && e.taskID == "-" {
			found = true
		}
	}
	f.backend.mu.Unlock()
	if !found {
		t.Error("no budget_pause event was recorded")
	}
}

// TestBudgetWindowFailureDoesNotStopTheRun: the same fail-open the
// per-dispatch gate takes. A window that cannot be read is not a reason to
// stop dispatching.
func TestBudgetWindowFailureDoesNotStopTheRun(t *testing.T) {
	f := newRunnerFixture(t,
		[]model.Task{task("C-1", 1, "claude/opus")},
		map[string]fakeResponse{"C-1": okResponse()},
		SchedulerOptions{})
	f.r.Budget = &stubWindow{cappedErr: errors.New("database is locked")}

	if code := f.runWithin(t, 30*time.Second); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if got, _ := f.s.Queue.Status("C-1"); got != model.StatusDone {
		t.Errorf("status = %q — an unreadable budget window stopped the work", got)
	}
}

// TestCappedWithNoResetDoesNotSleepForever.
func TestCappedWithNoResetDoesNotSleepForever(t *testing.T) {
	f := newRunnerFixture(t,
		[]model.Task{task("C-1", 1, "claude/opus")},
		map[string]fakeResponse{"C-1": okResponse()},
		SchedulerOptions{})
	f.r.Budget = &stubWindow{capped: true} // zero reset time

	if code := f.runWithin(t, 30*time.Second); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
}

// ---- Resume and the orphan sweep ----------------------------------------

type stubDispatches struct {
	mu      sync.Mutex
	rows    []state.Dispatch
	cleared []string
	listErr error
}

func (d *stubDispatches) InFlightDispatches(context.Context) ([]state.Dispatch, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.listErr != nil {
		return nil, d.listErr
	}
	return append([]state.Dispatch(nil), d.rows...), nil
}

func (d *stubDispatches) ClearDispatch(_ context.Context, _, taskID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cleared = append(d.cleared, taskID)
	// A cleared row is gone from the next listing.
	var kept []state.Dispatch
	for _, r := range d.rows {
		if r.TaskID != taskID {
			kept = append(kept, r)
		}
	}
	d.rows = kept
	return nil
}

func (d *stubDispatches) clearedIDs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.cleared...)
}

// deadPID returns a pid that is certainly not running: a child started and
// reaped, so the number existed and no longer does.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting a throwaway child: %v", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatalf("waiting for it: %v", err)
	}
	return pid
}

// TestStartupReconcileFreesAnOrphanedTask is the resume case: a previous orch
// died leaving a dispatch row and a task stuck in-progress. Without the
// sweep, that task never becomes ready again.
func TestStartupReconcileFreesAnOrphanedTask(t *testing.T) {
	f := newRunnerFixture(t,
		[]model.Task{task("C-1", 1, "claude/opus")},
		map[string]fakeResponse{"C-1": okResponse()},
		SchedulerOptions{})

	// The state a crashed run leaves behind.
	if err := f.s.Queue.MarkInFlight("C-1"); err != nil {
		t.Fatal(err)
	}
	d := &stubDispatches{rows: []state.Dispatch{
		{RunID: "old-run", TaskID: "C-1", PID: deadPID(t), Backend: "claude"},
	}}
	f.r.Dispatches = d

	if code := f.runWithin(t, 30*time.Second); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}

	if got := d.clearedIDs(); !equalStrings(got, []string{"C-1"}) {
		t.Errorf("cleared = %v, want the orphan C-1", got)
	}
	// Freed, then dispatched, then done — the point of the sweep.
	if got, _ := f.s.Queue.Status("C-1"); got != model.StatusDone {
		t.Errorf("status = %q, want done: the orphan should have been picked up", got)
	}

	found := false
	f.backend.mu.Lock()
	for _, e := range f.backend.events {
		if e.eventType == EventReconciled {
			found = true
		}
	}
	f.backend.mu.Unlock()
	if !found {
		t.Error("no reconciled event was recorded")
	}
}

// TestReconcileLeavesLiveProcessesAlone. Reaping a row whose process is still
// running would orphan a real agent mid-edit.
func TestReconcileLeavesLiveProcessesAlone(t *testing.T) {
	f := newRunnerFixture(t,
		[]model.Task{task("C-1", 1, "claude/opus")},
		map[string]fakeResponse{"C-1": okResponse()},
		SchedulerOptions{})

	// This test process is certainly alive.
	d := &stubDispatches{rows: []state.Dispatch{
		{RunID: "other-run", TaskID: "SOMEONE-ELSES", PID: os.Getpid(), Backend: "claude"},
	}}
	f.r.Dispatches = d

	if code := f.runWithin(t, 30*time.Second); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if got := d.clearedIDs(); len(got) != 0 {
		t.Errorf("cleared %v — a live process was reaped", got)
	}
}

// TestReconcileIgnoresOurOwnChildren: a row this process is supervising right
// now is not an orphan, even though another orch would see it as one.
func TestReconcileIgnoresOurOwnChildren(t *testing.T) {
	f := newRunnerFixture(t,
		[]model.Task{task("C-1", 1, "claude/opus")},
		map[string]fakeResponse{"C-1": okResponse()},
		SchedulerOptions{})

	if _, err := f.s.Refill(context.Background()); err != nil {
		t.Fatalf("Refill: %v", err)
	}
	var pid int
	for p := range f.s.InFlight() {
		pid = p
	}
	if pid == 0 {
		t.Fatal("nothing in flight")
	}

	d := &stubDispatches{rows: []state.Dispatch{
		{RunID: "run-1", TaskID: "C-1", PID: pid, Backend: "claude"},
	}}
	f.r.Dispatches = d

	if err := f.r.reconcile(context.Background(), "test"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := d.clearedIDs(); len(got) != 0 {
		t.Errorf("cleared %v — the sweep reaped a child we are supervising", got)
	}

	if _, err := f.s.DrainWait(context.Background(), 30*time.Second); err != nil {
		t.Fatalf("DrainWait: %v", err)
	}
}

// TestReconcileFailureDoesNotStopTheRun: an observability sweep must not be
// able to end a run, the same reasoning as the budget gate's fail-open.
func TestReconcileFailureDoesNotStopTheRun(t *testing.T) {
	f := newRunnerFixture(t,
		[]model.Task{task("C-1", 1, "claude/opus")},
		map[string]fakeResponse{"C-1": okResponse()},
		SchedulerOptions{})
	f.r.Dispatches = &stubDispatches{listErr: errors.New("database is locked")}

	if code := f.runWithin(t, 30*time.Second); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if got, _ := f.s.Queue.Status("C-1"); got != model.StatusDone {
		t.Errorf("status = %q — a failed sweep stopped the work", got)
	}
}

func TestPidAlive(t *testing.T) {
	t.Run("this process is alive", func(t *testing.T) {
		alive, known := pidAlive(os.Getpid())
		if !alive || !known {
			t.Errorf("pidAlive(self) = (%v, %v), want (true, true)", alive, known)
		}
	})

	t.Run("a reaped child is gone", func(t *testing.T) {
		alive, known := pidAlive(deadPID(t))
		if alive || !known {
			t.Errorf("pidAlive(reaped) = (%v, %v), want (false, true)", alive, known)
		}
	})

	t.Run("pid 1 exists and belongs to someone else", func(t *testing.T) {
		// EPERM means the process EXISTS and is not ours, which is alive.
		// Reading that as dead would reap running dispatches.
		alive, known := pidAlive(1)
		if !alive || !known {
			t.Errorf("pidAlive(1) = (%v, %v), want (true, true)", alive, known)
		}
	})

	t.Run("a non-pid is unknown", func(t *testing.T) {
		for _, pid := range []int{0, -1} {
			if alive, known := pidAlive(pid); alive || known {
				t.Errorf("pidAlive(%d) = (%v, %v), want (false, false)", pid, alive, known)
			}
		}
	})
}

// ---- Semi mode's second offer -------------------------------------------

// TestSemiModeReoffersDeferredTasksOnce: "not now" should not silently become
// "never", but it also must not loop forever.
func TestSemiModeReoffersDeferredTasksOnce(t *testing.T) {
	f := newRunnerFixture(t,
		[]model.Task{task("C-1", 1, "claude/opus")},
		map[string]fakeResponse{"C-1": okResponse()},
		SchedulerOptions{Mode: ModeSemi})

	asked := 0
	f.s.Gate = gateFunc(func(model.Task, string) Decision {
		asked++
		if asked == 1 {
			return DecisionDefer
		}
		return DecisionDispatch
	})
	// Make the task critical so the gate is consulted at all.
	f.s.Routes["claude/opus"] = route(model.BackendClaude, "opus", true)

	if code := f.runWithin(t, 30*time.Second); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if asked != 2 {
		t.Errorf("the gate was asked %d times, want 2 — deferred once, then re-offered", asked)
	}
	if got, _ := f.s.Queue.Status("C-1"); got != model.StatusDone {
		t.Errorf("status = %q, want done on the second offer", got)
	}
}

// TestSemiModeGivesUpOnAPersistentDefer: re-offering must not loop forever.
func TestSemiModeGivesUpOnAPersistentDefer(t *testing.T) {
	f := newRunnerFixture(t,
		[]model.Task{task("C-1", 1, "claude/opus")},
		map[string]fakeResponse{"C-1": okResponse()},
		SchedulerOptions{Mode: ModeSemi})

	asked := 0
	f.s.Gate = gateFunc(func(model.Task, string) Decision {
		asked++
		if asked > 20 {
			t.Error("the loop kept re-offering a deferred task")
			return DecisionQuit
		}
		return DecisionDefer
	})
	f.s.Routes["claude/opus"] = route(model.BackendClaude, "opus", true)

	code := f.runWithin(t, 30*time.Second)
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if asked > 3 {
		t.Errorf("the gate was asked %d times for one task; a second offer is enough", asked)
	}
	if got, _ := f.s.Queue.Status("C-1"); got != model.StatusTodo {
		t.Errorf("status = %q, want todo — a deferred task is not blocked", got)
	}
}

// gateFunc adapts a function to the Gate interface.
type gateFunc func(model.Task, string) Decision

func (g gateFunc) Ask(t model.Task, reason string) Decision { return g(t, reason) }

// longSleeps filters out the loop's short inter-tick naps, leaving the ones
// that can only be a budget pause.
func longSleeps(all []time.Duration) []time.Duration {
	var out []time.Duration
	for _, d := range all {
		if d >= time.Second {
			out = append(out, d)
		}
	}
	return out
}

func countLongSleeps(all []time.Duration) int { return len(longSleeps(all)) }

// ---- The loop must not spin on work it can never do ---------------------

// TestLoopStopsWhenNothingCanEverBeDispatched.
//
// Python's terminate condition is "nothing in flight, nothing ready, no
// retries", so a ready task that can never be dispatched — no route for its
// model, a backend with no adapter, a backend with no configured cap — keeps
// the ready set non-empty forever and the loop ticks on in silence. A hung
// orchestrator with no output is worse than an error, so this stops and says
// which tasks and why.
func TestLoopStopsWhenNothingCanEverBeDispatched(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(f *runnerFixture)
		want    string
	}{
		{
			name: "no route for the task's model",
			prepare: func(f *runnerFixture) {
				delete(f.s.Routes, "claude/opus")
			},
			want: "no route for model",
		},
		{
			name: "no concurrency cap configured for the backend",
			prepare: func(f *runnerFixture) {
				f.s.Sems = NewSems(4, map[string]int{"codex": 2})
			},
			want: "no concurrency cap",
		},
		{
			// Until G3.4 this was an unported backend; every Python backend
			// has an adapter now, so the reachable case is a route naming a
			// backend this binary does not know.
			name: "a backend no adapter serves",
			prepare: func(f *runnerFixture) {
				f.s.Routes["claude/opus"] = route(model.Backend("nosuch"), "some-model", false)
				f.s.Sems = NewSems(4, map[string]int{"nosuch": 2})
			},
			want: "unknown backend",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newRunnerFixture(t,
				[]model.Task{task("C-1", 1, "claude/opus")},
				map[string]fakeResponse{"C-1": okResponse()},
				SchedulerOptions{})
			tt.prepare(f)

			done := make(chan error, 1)
			go func() {
				_, err := f.r.Run(context.Background())
				done <- err
			}()

			select {
			case err := <-done:
				if err == nil {
					t.Fatal("want an error naming the stuck task")
				}
				if !strings.Contains(err.Error(), "C-1") {
					t.Errorf("err = %q, want it to name the task", err)
				}
				if !strings.Contains(err.Error(), tt.want) {
					t.Errorf("err = %q, want it to contain %q", err, tt.want)
				}
			case <-time.After(15 * time.Second):
				t.Fatal("the loop spun on a task it can never dispatch")
			}
		})
	}
}

// TestLoopKeepsGoingWhileSomethingCanStillRun: one undispatchable task must
// not stop a run that still has work it can do.
func TestLoopKeepsGoingWhileSomethingCanStillRun(t *testing.T) {
	f := newRunnerFixture(t,
		[]model.Task{
			task("C-1", 1, "claude/opus"),
			task("Z-1", 1, "nowhere/model"), // no route at all
		},
		map[string]fakeResponse{"C-1": okResponse()},
		SchedulerOptions{})

	done := make(chan error, 1)
	go func() {
		_, err := f.r.Run(context.Background())
		done <- err
	}()

	select {
	case err := <-done:
		// C-1 runs and finishes; only then is Z-1 the only thing left, and
		// only then does the loop give up — naming Z-1, not C-1.
		if err == nil {
			t.Fatal("want an error once only the unroutable task is left")
		}
		if !strings.Contains(err.Error(), "Z-1") {
			t.Errorf("err = %q, want it to name Z-1", err)
		}
		if got, _ := f.s.Queue.Status("C-1"); got != model.StatusDone {
			t.Errorf("C-1 = %q, want done — the runnable task should have run", got)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the loop did not finish")
	}
}

// ---- sprint_done (F4.7) ---------------------------------------------------

// sprintDoneEvents returns the run-level events the backend recorded.
func sprintDoneEvents(b *fakeBackend) []recordedEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []recordedEvent
	for _, e := range b.events {
		if e.eventType == EventSprintDone {
			out = append(out, e)
		}
	}
	return out
}

// TestSprintDoneIsEmittedOnceWhenTheQueueEmpties is F4.7's whole contract:
// one event, at the end, carrying what the run finished with.
func TestSprintDoneIsEmittedOnceWhenTheQueueEmpties(t *testing.T) {
	f := newRunnerFixture(t,
		[]model.Task{task("C-1", 1, "claude/opus"), task("C-2", 1, "claude/opus", "C-1")},
		map[string]fakeResponse{"C-1": okResponse(), "C-2": okResponse()},
		SchedulerOptions{RunID: "run-1"})

	if code := f.runWithin(t, 30*time.Second); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}

	events := sprintDoneEvents(f.backend)
	if len(events) != 1 {
		t.Fatalf("sprint_done emitted %d times, want exactly 1", len(events))
	}
	ev := events[0]
	// Run-level: no task owns it. A task id here would put the row in one
	// task's `orch events` history, which is not where a reader looks for
	// "the run finished".
	if ev.taskID != "" {
		t.Errorf("task_id = %q, want empty — sprint_done belongs to the run", ev.taskID)
	}
	for key, want := range map[string]int{"total": 2, "done": 2, "blocked": 0, "dispatched": 2} {
		if got, ok := ev.extra[key].(int); !ok || got != want {
			t.Errorf("extra[%q] = %v, want %d", key, ev.extra[key], want)
		}
	}
	if ev.extra["mode"] != string(ModeAuto) && ev.extra["mode"] != "" {
		t.Errorf("extra[mode] = %v", ev.extra["mode"])
	}
}

// A run whose remaining work is all blocked still emptied its queue, and the
// counts are what say so. Emitting nothing here would leave a cron job unable
// to tell "finished" from "still going"; emitting a bare "done" would claim
// the work succeeded. `blocked > 0` is the difference, in the row itself.
func TestSprintDoneCountsBlockedWork(t *testing.T) {
	f := newRunnerFixture(t,
		[]model.Task{task("C-1", 1, "claude/opus")},
		map[string]fakeResponse{"C-1": failResponse("authentication_error: bad key")},
		SchedulerOptions{RunID: "run-1"})

	if code := f.runWithin(t, 30*time.Second); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}

	events := sprintDoneEvents(f.backend)
	if len(events) != 1 {
		t.Fatalf("sprint_done emitted %d times, want 1", len(events))
	}
	if got := events[0].extra["blocked"]; got != 1 {
		t.Errorf("extra[blocked] = %v, want 1", got)
	}
	if got := events[0].extra["done"]; got != 0 {
		t.Errorf("extra[done] = %v, want 0", got)
	}
}

// An interrupted run did not finish, and must not say it did. Rule 25: the
// assertion is what the loop did NOT write — a cron job that treated a
// Ctrl-C'd run as a completed sprint would report a sprint nobody finished.
func TestSprintDoneIsNotEmittedAfterASignal(t *testing.T) {
	f := newRunnerFixture(t,
		[]model.Task{task("C-1", 1, "claude/opus")},
		map[string]fakeResponse{"C-1": okResponse()},
		SchedulerOptions{RunID: "run-1"})

	f.signals <- syscall.SIGINT
	if code := f.runWithin(t, 30*time.Second); code != ExitInterrupted {
		t.Fatalf("exit code = %d, want %d", code, ExitInterrupted)
	}
	if events := sprintDoneEvents(f.backend); len(events) != 0 {
		t.Errorf("sprint_done was emitted after a signal: %+v", events)
	}
}

// The guard, exercised where it lives.
//
// Through the loop it is unreachable — the single call site is followed by
// `break` — so a test that drove the loop would pass with the guard deleted
// and prove nothing. Calling the method twice is the only way to see it work,
// and this is the shape the next exit path added to Run will lean on.
func TestSprintDoneIsWrittenOnlyOnce(t *testing.T) {
	f := newRunnerFixture(t,
		[]model.Task{task("C-1", 1, "claude/opus")},
		map[string]fakeResponse{"C-1": okResponse()},
		SchedulerOptions{RunID: "run-1"})

	ctx := context.Background()
	f.r.recordSprintDone(ctx)
	f.r.recordSprintDone(ctx)

	if events := sprintDoneEvents(f.backend); len(events) != 1 {
		t.Errorf("two calls wrote %d events, want 1", len(events))
	}
}
