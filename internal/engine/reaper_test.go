package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hectorcanaimero/orch/internal/config"
	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/providers"
	"github.com/hectorcanaimero/orch/internal/vcs"
)

// ---- A recording backend, so the events can be asserted without SQL -------

type recordedEvent struct {
	eventType string
	taskID    string
	backend   string
	extra     map[string]any
}

type fakeBackend struct {
	mu          sync.Mutex
	events      []recordedEvent
	finishes    []Outcome
	transitions []struct {
		taskID string
		to     model.Status
	}
	// status is what TaskStatus answers, per task.
	status map[string]model.Status
	// statusErr makes TaskStatus fail, for the conservative path.
	statusErr error
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{status: map[string]model.Status{}}
}

func (b *fakeBackend) RecordDispatchAndEvent(_ context.Context, _ string, d Dispatch, sp *Spawned, attempt int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, recordedEvent{
		eventType: EventDispatch, taskID: d.Req.TaskID,
		backend: string(d.Req.Route.Backend),
		extra:   map[string]any{"pid": sp.PID, "attempt": attempt},
	})
	return nil
}

func (b *fakeBackend) RecordFinish(_ context.Context, _ string, d Dispatch, o Outcome, _ int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.finishes = append(b.finishes, o)
	et := EventSuccess
	switch {
	case o.TimedOut:
		et = EventTimeout
	case !o.Result.Success:
		et = EventFail
	}
	b.events = append(b.events, recordedEvent{
		eventType: et, taskID: d.Req.TaskID, backend: string(d.Req.Route.Backend),
	})
	return nil
}

func (b *fakeBackend) AppendEngineEvent(_ context.Context, _, eventType, taskID, backend string, extra map[string]any) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, recordedEvent{
		eventType: eventType, taskID: taskID, backend: backend, extra: extra,
	})
	return nil
}

func (b *fakeBackend) TaskStatus(_ context.Context, taskID string) (model.Status, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.statusErr != nil {
		return "", b.statusErr
	}
	st, ok := b.status[taskID]
	if !ok {
		return model.StatusTodo, nil
	}
	return st, nil
}

func (b *fakeBackend) Transition(_ context.Context, taskID string, to model.Status, _ string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.transitions = append(b.transitions, struct {
		taskID string
		to     model.Status
	}{taskID, to})
	return nil
}

// eventTypes returns the recorded event types for one task, in order.
func (b *fakeBackend) eventTypes(taskID string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for _, e := range b.events {
		if e.taskID == taskID {
			out = append(out, e.eventType)
		}
	}
	return out
}

// ---- A fixture that runs a whole dispatch to completion -------------------

// reapFixture dispatches through the fake provider with a canned response per
// task, then reaps. Nothing sleeps: the fake exits immediately and DrainWait
// blocks on the completion channel.
type reapFixture struct {
	s       *Scheduler
	backend *fakeBackend
}

func newReapFixture(t *testing.T, tasks []model.Task, responses map[string]fakeResponse, opts SchedulerOptions) *reapFixture {
	t.Helper()

	stateDir := filepath.Join(t.TempDir(), "state")
	fakeRoot := t.TempDir()
	dir := filepath.Join(fakeRoot, string(model.BackendClaude))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir fake: %v", err)
	}
	for id, r := range responses {
		write(t, filepath.Join(dir, id+".out"), r.out)
		write(t, filepath.Join(dir, id+".exit"), r.exitCode)
	}
	t.Setenv(FakeProviderEnv, fakeRoot)

	for _, tk := range tasks {
		write(t, PromptPathFor(stateDir, tk.ID), "prompt for "+tk.ID+"\n")
	}

	opts.StateDir = stateDir
	opts.Cwd = t.TempDir()
	if opts.Mode == "" {
		opts.Mode = ModeAuto
	}
	if opts.GlobalMax == 0 {
		opts.GlobalMax = 4
	}
	if opts.PerProvider == nil {
		opts.PerProvider = map[string]int{"claude": 4}
	}
	if opts.TimeoutMultiplier == 0 {
		opts.TimeoutMultiplier = 1.5
	}
	if opts.RunID == "" {
		opts.RunID = "run-1"
	}
	if opts.Cfg.Retry.MaxAttempts == 0 {
		opts.Cfg = config.Config{Retry: config.Retry{
			MaxAttempts: 2, BackoffSeconds: 5, RateLimitBackoffSeconds: 60,
		}}
	}

	q, err := NewTaskQueue(tasks)
	if err != nil {
		t.Fatalf("NewTaskQueue: %v", err)
	}
	s := NewScheduler(q, map[string]model.RouteEntry{
		"claude/opus": route(model.BackendClaude, "opus", false),
	}, opts)
	b := newFakeBackend()
	s.Backend = b

	return &reapFixture{s: s, backend: b}
}

type fakeResponse struct {
	out      string
	exitCode string
}

func okResponse() fakeResponse {
	return fakeResponse{
		out:      `{"type":"result","subtype":"success","is_error":false,"total_cost_usd":0.25,"usage":{"input_tokens":10,"output_tokens":20}}`,
		exitCode: "0",
	}
}

func failResponse(terminalReason string) fakeResponse {
	return fakeResponse{
		out:      `{"type":"result","subtype":"error","is_error":true,"total_cost_usd":0.1,"terminal_reason":"` + terminalReason + `"}`,
		exitCode: "1",
	}
}

// runOnce dispatches then drains, so a test can assert on the finished state.
func (f *reapFixture) runOnce(t *testing.T) {
	t.Helper()
	if _, err := f.s.Refill(context.Background()); err != nil {
		t.Fatalf("Refill: %v", err)
	}
	if _, err := f.s.DrainWait(context.Background(), 30*time.Second); err != nil {
		t.Fatalf("DrainWait: %v", err)
	}
}

// ---- The happy path ------------------------------------------------------

func TestReapSuccess(t *testing.T) {
	f := newReapFixture(t,
		[]model.Task{task("C-1", 1, "claude/opus")},
		map[string]fakeResponse{"C-1": okResponse()},
		SchedulerOptions{})

	f.runOnce(t)

	if got, _ := f.s.Queue.Status("C-1"); got != model.StatusDone {
		t.Errorf("status = %q, want done", got)
	}
	if got := f.backend.eventTypes("C-1"); !equalStrings(got, []string{EventDispatch, EventSuccess}) {
		t.Errorf("events = %v, want dispatch then success", got)
	}
	if len(f.s.RetryQueue()) != 0 {
		t.Errorf("a success queued a retry: %v", f.s.RetryQueue())
	}
	// Capacity came back.
	if got := f.s.Sems.Global.Current(); got != 0 {
		t.Errorf("global slots still held: %d", got)
	}
	if got := f.s.SpentUSD("C-1"); got != 0.25 {
		t.Errorf("SpentUSD = %v, want 0.25", got)
	}
}

// TestReapReleasesCapacityForTheNextTask: the whole point of reaping is that
// the freed slot lets the next task run.
func TestReapFreesTheSlotForTheNextTask(t *testing.T) {
	f := newReapFixture(t,
		[]model.Task{task("C-1", 1, "claude/opus"), task("C-2", 1, "claude/opus")},
		map[string]fakeResponse{"C-1": okResponse(), "C-2": okResponse()},
		SchedulerOptions{GlobalMax: 1, PerProvider: map[string]int{"claude": 1}})

	started, err := f.s.Refill(context.Background())
	if err != nil {
		t.Fatalf("Refill: %v", err)
	}
	if started != 1 {
		t.Fatalf("started = %d, want 1 at a ceiling of 1", started)
	}

	if _, err := f.s.DrainWait(context.Background(), 30*time.Second); err != nil {
		t.Fatalf("DrainWait: %v", err)
	}
	again, err := f.s.Refill(context.Background())
	if err != nil {
		t.Fatalf("second Refill: %v", err)
	}
	if again != 1 {
		t.Errorf("second tick started %d, want the freed slot to carry C-2", again)
	}
}

// ---- Retry ---------------------------------------------------------------

func TestReapQueuesARetry(t *testing.T) {
	f := newReapFixture(t,
		[]model.Task{task("C-1", 1, "claude/opus")},
		map[string]fakeResponse{"C-1": failResponse("503 service unavailable")},
		SchedulerOptions{})

	f.runOnce(t)

	queued := f.s.RetryQueue()
	if len(queued) != 1 {
		t.Fatalf("retry queue has %d items, want 1", len(queued))
	}
	if queued[0].Attempt != 2 {
		t.Errorf("Attempt = %d, want 2", queued[0].Attempt)
	}
	// Back to todo so the ready set can pick it up.
	if got, _ := f.s.Queue.Status("C-1"); got != model.StatusTodo {
		t.Errorf("status = %q, want todo", got)
	}
	if got := f.backend.eventTypes("C-1"); !equalStrings(got, []string{EventDispatch, EventFail, EventRetry}) {
		t.Errorf("events = %v, want dispatch, fail, retry", got)
	}
	// Spend is recorded for the failed attempt too — it was paid for.
	if got := f.s.SpentUSD("C-1"); got != 0.1 {
		t.Errorf("SpentUSD = %v, want the failed attempt's 0.1", got)
	}
}

// TestRetryWaitsOutItsBackoff, on an injected clock so nothing sleeps.
func TestRetryWaitsOutItsBackoff(t *testing.T) {
	f := newReapFixture(t,
		[]model.Task{task("C-1", 1, "claude/opus")},
		map[string]fakeResponse{"C-1": failResponse("503 service unavailable")},
		SchedulerOptions{})

	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	f.s.now = func() time.Time { return now }

	f.runOnce(t)
	if len(f.s.RetryQueue()) != 1 {
		t.Fatalf("nothing queued")
	}

	// The backoff has not expired: the item stays queued and nothing starts.
	started, err := f.s.Refill(context.Background())
	if err != nil {
		t.Fatalf("Refill: %v", err)
	}
	if started != 0 {
		t.Errorf("started %d before the backoff expired", started)
	}
	if len(f.s.RetryQueue()) != 1 {
		t.Errorf("the waiting item was dropped from the queue")
	}

	// Move the clock past it.
	now = now.Add(10 * time.Second)
	started, err = f.s.Refill(context.Background())
	if err != nil {
		t.Fatalf("Refill after the backoff: %v", err)
	}
	if started != 1 {
		t.Errorf("started = %d, want the retry to go once its backoff expired", started)
	}
	if len(f.s.RetryQueue()) != 0 {
		t.Errorf("the dispatched item is still queued")
	}

	if _, err := f.s.DrainWait(context.Background(), 30*time.Second); err != nil {
		t.Fatalf("DrainWait: %v", err)
	}
}

// TestRetriesTakePrecedenceOverTheReadySet (FR-D-4): a task marked for retry
// must not wait behind a newly-ready peer.
func TestRetriesTakePrecedenceOverTheReadySet(t *testing.T) {
	f := newReapFixture(t,
		[]model.Task{task("C-1", 1, "claude/opus"), task("C-2", 1, "claude/opus")},
		map[string]fakeResponse{
			"C-1": failResponse("503 service unavailable"),
			"C-2": okResponse(),
		},
		SchedulerOptions{GlobalMax: 1, PerProvider: map[string]int{"claude": 1}})

	// C-1 runs and fails, and is queued for retry with no backoff left.
	if _, err := f.s.Refill(context.Background()); err != nil {
		t.Fatalf("Refill: %v", err)
	}
	if _, err := f.s.DrainWait(context.Background(), 30*time.Second); err != nil {
		t.Fatalf("DrainWait: %v", err)
	}
	f.s.now = func() time.Time { return time.Now().Add(time.Hour) } // backoff expired

	// One slot, and both C-1 (retry) and C-2 (ready) want it. The retry wins.
	if _, err := f.s.Refill(context.Background()); err != nil {
		t.Fatalf("second Refill: %v", err)
	}
	var running []string
	for _, e := range f.s.InFlight() {
		running = append(running, e.Task.ID)
	}
	if !equalStrings(running, []string{"C-1"}) {
		t.Errorf("in flight = %v, want the retry C-1 to take the only slot", running)
	}

	if _, err := f.s.DrainWait(context.Background(), 30*time.Second); err != nil {
		t.Fatalf("final DrainWait: %v", err)
	}
}

// TestReapBlocksAfterTheLastAttempt.
func TestReapBlocksAfterTheLastAttempt(t *testing.T) {
	f := newReapFixture(t,
		[]model.Task{task("C-1", 1, "claude/opus")},
		map[string]fakeResponse{"C-1": failResponse("503 service unavailable")},
		SchedulerOptions{Cfg: config.Config{Retry: config.Retry{MaxAttempts: 1}}})

	f.runOnce(t)

	if got, _ := f.s.Queue.Status("C-1"); got != model.StatusBlocked {
		t.Errorf("status = %q, want blocked", got)
	}
	if len(f.s.RetryQueue()) != 0 {
		t.Errorf("a terminal failure queued a retry")
	}
	if got := f.backend.eventTypes("C-1"); !equalStrings(got, []string{EventDispatch, EventFail, EventFail}) {
		t.Errorf("events = %v, want the outcome event then the block event", got)
	}
}

// TestTerminalClassIsNotRetried: a permission failure is terminal on the
// first attempt, however many attempts are configured.
func TestTerminalClassIsNotRetried(t *testing.T) {
	f := newReapFixture(t,
		[]model.Task{task("C-1", 1, "claude/opus")},
		map[string]fakeResponse{"C-1": failResponse("authentication_error: invalid key")},
		SchedulerOptions{Cfg: config.Config{Retry: config.Retry{MaxAttempts: 5}}})

	f.runOnce(t)

	if got, _ := f.s.Queue.Status("C-1"); got != model.StatusBlocked {
		t.Errorf("status = %q, want blocked on the first attempt", got)
	}
	if len(f.s.RetryQueue()) != 0 {
		t.Errorf("a permission failure was queued for retry")
	}
}

// TestRetryConfigChangesObservableBehaviour is the test orch-98 asked for:
// changing retry.rate_limit.max_attempts changes what the reaper does, with
// everything else held constant.
func TestRetryConfigChangesObservableBehaviour(t *testing.T) {
	run := func(t *testing.T, rule config.RetryRule) (model.Status, int) {
		t.Helper()
		f := newReapFixture(t,
			[]model.Task{task("C-1", 1, "claude/opus")},
			map[string]fakeResponse{"C-1": failResponse("429 rate limit exceeded")},
			SchedulerOptions{Cfg: config.Config{Retry: config.Retry{
				MaxAttempts: 2, BackoffSeconds: 5, RateLimitBackoffSeconds: 60,
				RateLimit: rule,
			}}})
		f.runOnce(t)
		st, _ := f.s.Queue.Status("C-1")
		return st, len(f.s.RetryQueue())
	}

	t.Run("the default retries", func(t *testing.T) {
		status, queued := run(t, config.RetryRule{})
		if status != model.StatusTodo || queued != 1 {
			t.Errorf("status=%q queued=%d, want todo and 1 queued", status, queued)
		}
	})

	t.Run("max_attempts 0 blocks instead", func(t *testing.T) {
		status, queued := run(t, config.RetryRule{MaxAttempts: intp(0)})
		if status != model.StatusBlocked || queued != 0 {
			t.Errorf("status=%q queued=%d, want blocked and nothing queued", status, queued)
		}
	})
}

// ---- The sub-agent-finished guard ---------------------------------------

// TestSubAgentFinishedDespiteTheWrapper: the agent called task-finish.sh and
// the backend says done, while the CLI wrapper reported a failure anyway.
// The sub-agent wins.
//
// Python reads tasks.json for this, which stopped working when SQLite became
// the default backend — see go-migration-notes.md. Reading the backend is the
// port of the intent.
func TestSubAgentFinishedDespiteTheWrapper(t *testing.T) {
	f := newReapFixture(t,
		[]model.Task{task("C-1", 1, "claude/opus")},
		map[string]fakeResponse{"C-1": failResponse("Skill descriptions were shortened")},
		SchedulerOptions{})
	f.backend.status["C-1"] = model.StatusDone

	f.runOnce(t)

	if got, _ := f.s.Queue.Status("C-1"); got != model.StatusDone {
		t.Errorf("status = %q, want done — the sub-agent finished the work", got)
	}
	if len(f.s.RetryQueue()) != 0 {
		t.Errorf("a task the sub-agent finished was queued for retry")
	}
}

// TestSubAgentGuardTrustsTheWrapperWhenTheBackendCannotAnswer: a backend that
// errors reads as "not done", which is the conservative side.
func TestSubAgentGuardTrustsTheWrapperOnError(t *testing.T) {
	f := newReapFixture(t,
		[]model.Task{task("C-1", 1, "claude/opus")},
		map[string]fakeResponse{"C-1": failResponse("503 service unavailable")},
		SchedulerOptions{})
	f.backend.statusErr = errors.New("database is locked")

	f.runOnce(t)

	if len(f.s.RetryQueue()) != 1 {
		t.Errorf("want the wrapper's verdict to stand and the retry to be queued")
	}
}

// ---- The worktree Caller contract ---------------------------------------

type recordingWorktree struct {
	mu        sync.Mutex
	calls     []string
	pushErr   error
	createErr error
	dir       string
}

func (w *recordingWorktree) record(name string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls = append(w.calls, name)
}

// callList snapshots what has been called so far.
//
// Every assertion goes through this rather than locking around the
// comparison: holding the lock across anything that reaches the scheduler
// deadlocks, because reaping calls straight back into record(). That is how
// this suite hung for six minutes once.
func (w *recordingWorktree) callList() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.calls...)
}

func (w *recordingWorktree) CommitPending(context.Context, string) error {
	w.record("commit")
	return nil
}
func (w *recordingWorktree) Push(context.Context, string) error {
	w.record("push")
	return w.pushErr
}
func (w *recordingWorktree) Remove(context.Context, string) error {
	w.record("remove")
	return nil
}
func (w *recordingWorktree) RemoveAll(context.Context) error {
	w.record("remove_all")
	return nil
}
func (w *recordingWorktree) Create(_ context.Context, taskID, _ string, _ ...string) (string, error) {
	w.record("create")
	if w.createErr != nil {
		return "", w.createErr
	}
	// A real Manager hands back a directory that exists, and the child is
	// spawned with it as its working directory — so the double has to make
	// it too, or every dispatch fails on a missing cwd.
	path := filepath.Join(w.dir, taskID)
	if err := os.MkdirAll(path, 0o750); err != nil {
		return "", err
	}
	return path, nil
}
func (w *recordingWorktree) Recreate(_ context.Context, taskID string) (string, error) {
	w.record("recreate")
	path := filepath.Join(w.dir, taskID)
	if err := os.MkdirAll(path, 0o750); err != nil {
		return "", err
	}
	return path, nil
}
func (w *recordingWorktree) BranchName(taskID string) string { return "orch/" + taskID }

func TestWorktreeCallerContract(t *testing.T) {
	tests := []struct {
		name      string
		response  fakeResponse
		pushErr   error
		wantCalls []string
		wantDone  bool
	}{
		{
			name:      "a finished task commits, pushes, then removes",
			response:  okResponse(),
			wantCalls: []string{"commit", "push", "remove"},
			wantDone:  true,
		},
		{
			// Publishing incomplete work is the thing this ordering exists
			// to prevent.
			name:      "a failed task commits and removes, and never pushes",
			response:  failResponse("503 service unavailable"),
			wantCalls: []string{"commit", "remove"},
		},
		{
			// A broken remote must not strand a worktree, and must not
			// downgrade a task whose work is committed locally either way.
			name:      "a push failure still removes, and the task stays done",
			response:  okResponse(),
			pushErr:   errors.New("remote hung up"),
			wantCalls: []string{"commit", "push", "remove"},
			wantDone:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newReapFixture(t,
				[]model.Task{task("C-1", 1, "claude/opus")},
				map[string]fakeResponse{"C-1": tt.response},
				SchedulerOptions{})
			wt := &recordingWorktree{pushErr: tt.pushErr}
			f.s.Worktree = wt

			f.runOnce(t)

			got := wt.callList()
			if !equalStrings(got, tt.wantCalls) {
				t.Errorf("calls = %v, want %v", got, tt.wantCalls)
			}

			status, _ := f.s.Queue.Status("C-1")
			if tt.wantDone && status != model.StatusDone {
				t.Errorf("status = %q, want done", status)
			}
		})
	}
}

// TestRemoveAllRunsAfterTheDrain, never during it — cleanup racing a
// still-draining task would delete a worktree a backend process is writing
// into.
func TestRemoveAllRunsAfterTheDrain(t *testing.T) {
	f := newReapFixture(t,
		[]model.Task{task("C-1", 1, "claude/opus")},
		map[string]fakeResponse{"C-1": okResponse()},
		SchedulerOptions{})
	wt := &recordingWorktree{}
	f.s.Worktree = wt

	if _, err := f.s.Refill(context.Background()); err != nil {
		t.Fatalf("Refill: %v", err)
	}
	if _, err := f.s.DrainWait(context.Background(), 30*time.Second); err != nil {
		t.Fatalf("DrainWait: %v", err)
	}

	for _, c := range wt.callList() {
		if c == "remove_all" {
			t.Fatal("RemoveAll ran during the drain")
		}
	}

	f.s.CleanupWorktrees(context.Background())
	calls := wt.callList()
	if last := calls[len(calls)-1]; last != "remove_all" {
		t.Errorf("last call = %q, want remove_all after the drain", last)
	}
}

// ---- Draining ------------------------------------------------------------

func TestDrainWaitReturnsWhenEverythingIsReaped(t *testing.T) {
	f := newReapFixture(t,
		[]model.Task{task("C-1", 1, "claude/opus"), task("C-2", 1, "claude/opus")},
		map[string]fakeResponse{"C-1": okResponse(), "C-2": okResponse()},
		SchedulerOptions{})

	if _, err := f.s.Refill(context.Background()); err != nil {
		t.Fatalf("Refill: %v", err)
	}
	reaped, err := f.s.DrainWait(context.Background(), 30*time.Second)
	if err != nil {
		t.Fatalf("DrainWait: %v", err)
	}
	if reaped != 2 {
		t.Errorf("reaped = %d, want 2", reaped)
	}
	if len(f.s.InFlight()) != 0 {
		t.Errorf("%d still in flight after the drain", len(f.s.InFlight()))
	}
}

func TestDrainWaitOnAnEmptyRun(t *testing.T) {
	f := newReapFixture(t, []model.Task{task("C-1", 1, "claude/opus")},
		map[string]fakeResponse{"C-1": okResponse()}, SchedulerOptions{})
	reaped, err := f.s.DrainWait(context.Background(), time.Second)
	if err != nil {
		t.Fatalf("DrainWait: %v", err)
	}
	if reaped != 0 {
		t.Errorf("reaped = %d with nothing dispatched", reaped)
	}
}

// TestReapIsNonBlocking: a tick with nothing finished returns immediately,
// which is what lets a caller alternate Reap and Refill.
func TestReapIsNonBlocking(t *testing.T) {
	f := newReapFixture(t, []model.Task{task("C-1", 1, "claude/opus")},
		map[string]fakeResponse{"C-1": okResponse()}, SchedulerOptions{})

	done := make(chan int, 1)
	go func() {
		n, err := f.s.Reap(context.Background())
		if err != nil {
			t.Errorf("Reap: %v", err)
		}
		done <- n
	}()

	select {
	case n := <-done:
		if n != 0 {
			t.Errorf("reaped = %d with nothing dispatched", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Reap blocked with nothing in flight")
	}
}

// TestReapHandlesAStrayCompletion: a pid the scheduler does not know about is
// ignored rather than panicking, as Python skips a stray child.
func TestReapIgnoresAStrayCompletion(t *testing.T) {
	f := newReapFixture(t, []model.Task{task("C-1", 1, "claude/opus")},
		map[string]fakeResponse{"C-1": okResponse()}, SchedulerOptions{})

	f.s.done <- completion{pid: 999999, outcome: Outcome{}}
	n, err := f.s.Reap(context.Background())
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if n != 1 {
		t.Errorf("reaped = %d, want the stray to be consumed", n)
	}
}

// ---- Truncation ----------------------------------------------------------

// TestBlockReasonIsTruncated: Python cuts the reason to 500 characters before
// it reaches the event row, because a CLI traceback would otherwise bury it.
func TestBlockReasonIsTruncated(t *testing.T) {
	long := strings.Repeat("boom ", 300) // well past 500
	f := newReapFixture(t,
		[]model.Task{task("C-1", 1, "claude/opus")},
		map[string]fakeResponse{"C-1": failResponse(long)},
		SchedulerOptions{Cfg: config.Config{Retry: config.Retry{MaxAttempts: 1}}})

	f.runOnce(t)

	f.backend.mu.Lock()
	defer f.backend.mu.Unlock()
	for _, e := range f.backend.events {
		if reason, ok := e.extra["reason"].(string); ok {
			if len([]rune(reason)) > reasonLimit {
				t.Errorf("a %d-character reason reached the event row", len([]rune(reason)))
			}
		}
	}
}

var _ providers.Provider = providers.ClaudeProvider{}

// TestRetryQueueOwnsItsTaskUntilTheBackoffExpires is the regression guard for
// bug 15.
//
// A retry resets the task to todo so the queue will consider it again. In
// Python that also makes it eligible in the ready-set pass of the very same
// refill, so the backoff just computed is bypassed — a rate-limited provider
// is hammered again at once instead of waiting out its window — and the
// RetryItem stays queued, so the task can be dispatched a SECOND time when
// its backoff finally expires. Go excludes anything the retry queue owns.
func TestRetryQueueOwnsItsTaskUntilTheBackoffExpires(t *testing.T) {
	f := newReapFixture(t,
		[]model.Task{task("C-1", 1, "claude/opus")},
		map[string]fakeResponse{"C-1": failResponse("429 rate limit exceeded")},
		SchedulerOptions{})

	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	f.s.now = func() time.Time { return now }

	f.runOnce(t)
	if len(f.s.RetryQueue()) != 1 {
		t.Fatalf("nothing queued for retry")
	}
	// The task is back to todo — which is exactly what makes the ready set
	// consider it, and exactly what must not dispatch it yet.
	if got, _ := f.s.Queue.Status("C-1"); got != model.StatusTodo {
		t.Fatalf("status = %q, want todo", got)
	}

	// A 60-second rate-limit backoff: several ticks must start nothing.
	for i := 0; i < 3; i++ {
		started, err := f.s.Refill(context.Background())
		if err != nil {
			t.Fatalf("Refill %d: %v", i, err)
		}
		if started != 0 {
			t.Fatalf("tick %d dispatched during the backoff — the ready set raced the retry queue", i)
		}
		if len(f.s.InFlight()) != 0 {
			t.Fatalf("tick %d put the task in flight during its backoff", i)
		}
	}

	// Past the window it goes exactly once, and leaves the queue.
	now = now.Add(61 * time.Second)
	started, err := f.s.Refill(context.Background())
	if err != nil {
		t.Fatalf("Refill after the backoff: %v", err)
	}
	if started != 1 {
		t.Fatalf("started = %d, want exactly 1", started)
	}
	if len(f.s.RetryQueue()) != 0 {
		t.Errorf("the dispatched retry is still queued — it would go a second time")
	}
	if len(f.s.InFlight()) != 1 {
		t.Errorf("%d in flight, want exactly one", len(f.s.InFlight()))
	}

	if _, err := f.s.DrainWait(context.Background(), 30*time.Second); err != nil {
		t.Fatalf("DrainWait: %v", err)
	}
}

// ---- Auto-PR -------------------------------------------------------------

type recordingPR struct {
	mu    sync.Mutex
	saved map[string]string
	err   error
}

func (r *recordingPR) SetTaskPR(_ context.Context, taskID, prURL string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	if r.saved == nil {
		r.saved = map[string]string{}
	}
	r.saved[taskID] = prURL
	return nil
}

func (r *recordingPR) get(taskID string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.saved[taskID]
}

// prFixture is a finished, successful task with worktree mode and auto-PR on.
func prFixture(t *testing.T) (*reapFixture, *recordingWorktree, *fakeVCS, *recordingPR) {
	t.Helper()
	f := newReapFixture(t,
		[]model.Task{{
			ID: "C-1", Phase: 1, Title: "Wire the webhook", Model: "claude/opus",
			Status: model.StatusTodo, EstimateHours: 1, SpecRef: "specs/f1.md#T1",
			Reason: "the webhook is unhandled",
		}},
		map[string]fakeResponse{"C-1": okResponse()},
		SchedulerOptions{AutoPR: true, WorktreeMode: true, BaseBranch: "main"})

	wt := &recordingWorktree{dir: t.TempDir()}
	v := &fakeVCS{status: map[string]vcs.CIState{}, createdPR: "https://github.com/o/r/pull/7"}
	pr := &recordingPR{}
	f.s.Worktree = wt
	f.s.VCS = v
	f.s.CIRecorder = pr
	return f, wt, v, pr
}

// TestSuccessfulPushOpensAPRAndWaitsForCI. A task under review is NOT done:
// marking it done here would finish work whose tests have not run.
func TestSuccessfulPushOpensAPRAndWaitsForCI(t *testing.T) {
	f, wt, v, pr := prFixture(t)

	f.runOnce(t)

	if got := pr.get("C-1"); got != "https://github.com/o/r/pull/7" {
		t.Errorf("recorded PR = %q, want the created one", got)
	}
	if got, _ := f.s.Queue.Status("C-1"); got != model.StatusInProgress {
		t.Errorf("status = %q, want it still in-progress while CI runs", got)
	}
	if got := f.backend.eventTypes("C-1"); !contains(got, EventPRCreated) {
		t.Errorf("events = %v, want a pr_created", got)
	}
	// The PR is opened from the task's own branch, into the base.
	if got := v.prHead; got != "orch/C-1" {
		t.Errorf("PR head = %q, want the task's branch", got)
	}
	if got := v.prBase; got != "main" {
		t.Errorf("PR base = %q, want main", got)
	}
	// Python's body shape: the task id, its spec, then its reason.
	for _, want := range []string{"`C-1`", "specs/f1.md#T1", "the webhook is unhandled"} {
		if !strings.Contains(v.prBody, want) {
			t.Errorf("PR body is missing %q:\n%s", want, v.prBody)
		}
	}
	// And the ordering still holds around it.
	if got := wt.callList(); !equalStrings(got, []string{"create", "commit", "push", "remove"}) {
		t.Errorf("worktree calls = %v", got)
	}
}

// TestCIRetryPushesToTheExistingPR pins #276. A CI retry pushes its fix to
// the branch that already has a PR, so asking the forge for a new one fails
// ("a pull request already exists"). That used to read as "no PR was
// opened": the task was blocked while its updated PR was still in CI.
func TestCIRetryPushesToTheExistingPR(t *testing.T) {
	f, _, v, pr := prFixture(t)
	const existing = "https://github.com/o/r/pull/3"
	f.s.ciRetryPR["C-1"] = existing
	v.createdPR = "" // what gh answers for a branch that already has a PR

	f.runOnce(t)

	if v.created != 0 {
		t.Errorf("asked the forge for %d new PRs on a CI retry", v.created)
	}
	if got := pr.get("C-1"); got != existing {
		t.Errorf("recorded PR = %q, want the existing %q waiting on CI again", got, existing)
	}
	if got, _ := f.s.Queue.Status("C-1"); got != model.StatusInProgress {
		t.Errorf("status = %q, want it in-progress while the updated PR runs CI", got)
	}
	if _, left := f.s.ciRetryPR["C-1"]; left {
		t.Errorf("the retry's PR is still remembered after the reap")
	}
}

// TestBlockingATaskTheAgentMarkedDone pins the second half of #276: the
// agent reports its task done before orch opens the PR, so when no PR could
// be opened the block is done -> blocked, which the transition table refuses.
// The refusal left the database at done while this run had blocked the task.
func TestBlockingATaskTheAgentMarkedDone(t *testing.T) {
	f, _, _, _ := prFixture(t)
	strict := newStrictBackend(map[string]model.Status{"C-1": model.StatusDone})
	f.s.Backend = strict
	task, _ := f.s.Queue.Task("C-1")

	if err := f.s.blockTask(context.Background(), &InFlight{Task: task}, "no PR was opened", Outcome{}); err != nil {
		t.Fatalf("blockTask: %v", err)
	}
	if got, _ := strict.TaskStatus(context.Background(), "C-1"); got != model.StatusBlocked {
		t.Errorf("backend status = %q, want blocked like this run's queue", got)
	}
}

// TestNoPRWithoutASuccessfulPush: a PR opened from a branch that never
// reached the remote cannot be reviewed, and the task would wait on CI that
// will never run.
func TestNoPRWithoutASuccessfulPush(t *testing.T) {
	f, _, v, pr := prFixture(t)
	f.s.Worktree.(*recordingWorktree).pushErr = errors.New("remote hung up")

	f.runOnce(t)

	if v.created != 0 {
		t.Errorf("opened %d PRs after a failed push", v.created)
	}
	if got := pr.get("C-1"); got != "" {
		t.Errorf("recorded a PR after a failed push: %q", got)
	}
	// Issue #230: in worktree mode the work only reaches the base through a
	// PR, so a dependent released now would build on work that is not there.
	if got, _ := f.s.Queue.Status("C-1"); got != model.StatusBlocked {
		t.Errorf("status = %q, want blocked", got)
	}
}

// TestPRFailuresBlockTheTask. Every one of these leaves no recorded PR URL,
// so nothing would ever poll it — waiting on CI would wait forever. Finishing
// it instead (Python's choice) released its dependents onto work that never
// reached the base branch: issue #230. Blocked is the one honest verdict.
func TestPRFailuresBlockTheTask(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(v *fakeVCS, pr *recordingPR)
	}{
		{
			name:    "the CLI errors",
			prepare: func(v *fakeVCS, _ *recordingPR) { v.createErr = errors.New("gh: not authenticated") },
		},
		{
			// Python's None: the CLI ran and produced no PR.
			name:    "the CLI produces no PR",
			prepare: func(v *fakeVCS, _ *recordingPR) { v.createdPR = "" },
		},
		{
			name:    "the URL cannot be recorded",
			prepare: func(_ *fakeVCS, pr *recordingPR) { pr.err = errors.New("database is locked") },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, _, v, pr := prFixture(t)
			tt.prepare(v, pr)

			f.runOnce(t)

			if got, _ := f.s.Queue.Status("C-1"); got != model.StatusBlocked {
				t.Errorf("status = %q, want blocked — neither waiting on CI nor done", got)
			}
			if got := f.backend.eventTypes("C-1"); !contains(got, EventFail) {
				t.Errorf("events = %v, want a fail event saying why", got)
			}
		})
	}
}

// TestAgentBlockedSurvivesACleanExit: issue #230. The agent called orch_block
// (a missing input) and exited 0. The wrapper's success must not overwrite
// the agent's verdict, nor publish its half-done branch.
func TestAgentBlockedSurvivesACleanExit(t *testing.T) {
	f, _, v, _ := prFixture(t)
	f.backend.status["C-1"] = model.StatusBlocked

	f.runOnce(t)

	if got, _ := f.s.Queue.Status("C-1"); got != model.StatusBlocked {
		t.Errorf("status = %q, want the agent's blocked kept", got)
	}
	if v.created != 0 {
		t.Errorf("opened %d PRs for a task the agent blocked", v.created)
	}
	for _, tr := range f.backend.transitions {
		if tr.taskID == "C-1" && tr.to == model.StatusDone {
			t.Error("recorded done over the agent's blocked")
		}
	}
}

// Without worktrees there is no PR to expect, but the agent's block still wins.
func TestAgentBlockedSurvivesACleanExitWithoutWorktrees(t *testing.T) {
	f := newReapFixture(t,
		[]model.Task{task("C-1", 1, "claude/opus")},
		map[string]fakeResponse{"C-1": okResponse()},
		SchedulerOptions{})
	f.backend.status["C-1"] = model.StatusBlocked

	f.runOnce(t)

	if got, _ := f.s.Queue.Status("C-1"); got != model.StatusBlocked {
		t.Errorf("status = %q, want the agent's blocked kept", got)
	}
}

func TestNoPRWhenAutoPRIsOff(t *testing.T) {
	f, _, v, _ := prFixture(t)
	f.s.Opts.AutoPR = false

	f.runOnce(t)

	if v.created != 0 {
		t.Errorf("opened %d PRs with auto_pr off", v.created)
	}
	if got, _ := f.s.Queue.Status("C-1"); got != model.StatusDone {
		t.Errorf("status = %q, want done", got)
	}
}

// ---- Worktree creation on dispatch --------------------------------------

// TestTheChildRunsInsideItsWorktree: the whole point of worktree mode is that
// one agent cannot edit another's files.
func TestTheChildRunsInsideItsWorktree(t *testing.T) {
	f, wt, _, _ := prFixture(t)

	if _, err := f.s.Refill(context.Background()); err != nil {
		t.Fatalf("Refill: %v", err)
	}
	var entry *InFlight
	for _, e := range f.s.InFlight() {
		entry = e
	}
	if entry == nil {
		t.Fatal("nothing dispatched")
	}
	want := filepath.Join(wt.dir, "C-1")
	if entry.Dispatch.Cwd != want {
		t.Errorf("the child's cwd = %q, want its worktree %q", entry.Dispatch.Cwd, want)
	}
	if entry.Dispatch.Req.Cwd != want {
		t.Errorf("the provider's cwd = %q, want its worktree", entry.Dispatch.Req.Cwd)
	}

	if _, err := f.s.DrainWait(context.Background(), 30*time.Second); err != nil {
		t.Fatalf("DrainWait: %v", err)
	}
}

// TestWorktreeCreationFailureBlocksTheTask: dispatching into the project root
// instead would let the agent edit files another task owns, which is the
// thing worktree mode exists to prevent.
func TestWorktreeCreationFailureBlocksTheTask(t *testing.T) {
	f, wt, _, _ := prFixture(t)
	wt.createErr = errors.New("branch already checked out")

	started, err := f.s.Refill(context.Background())
	if err != nil {
		t.Fatalf("Refill: %v", err)
	}
	if started != 0 {
		t.Fatalf("started %d dispatches with no worktree", started)
	}
	if got, _ := f.s.Queue.Status("C-1"); got != model.StatusBlocked {
		t.Errorf("status = %q, want blocked", got)
	}
	if got := f.s.Sems.Global.Current(); got != 0 {
		t.Errorf("global slots held after a failed worktree create: %d", got)
	}
}

// TestNoWorktreeWhenModeIsOff: a project that never asked runs in its own
// root, exactly as it did before F-2.
func TestNoWorktreeWhenModeIsOff(t *testing.T) {
	f, wt, _, _ := prFixture(t)
	f.s.Opts.WorktreeMode = false

	if _, err := f.s.Refill(context.Background()); err != nil {
		t.Fatalf("Refill: %v", err)
	}
	for _, e := range f.s.InFlight() {
		if e.Dispatch.Cwd != f.s.Opts.Cwd {
			t.Errorf("cwd = %q, want the project root", e.Dispatch.Cwd)
		}
	}
	if contains(wt.callList(), "create") {
		t.Error("a worktree was created with the mode off")
	}

	if _, err := f.s.DrainWait(context.Background(), 30*time.Second); err != nil {
		t.Fatalf("DrainWait: %v", err)
	}
}

// contains reports whether hay holds needle.
func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// ---- The notification side channel --------------------------------------

// recordingNotifier captures what the engine announced, so the two call sites
// can be asserted without an HTTP server.
type recordingNotifier struct {
	mu       sync.Mutex
	blocked  []string
	ciBlocks []string
	stalls   []string
}

func (n *recordingNotifier) Blocked(_ context.Context, taskID, reason string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.blocked = append(n.blocked, taskID+": "+reason)
}

func (n *recordingNotifier) CIBlocked(_ context.Context, taskID, prURL string, attempts int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.ciBlocks = append(n.ciBlocks, fmt.Sprintf("%s: %s x%d", taskID, prURL, attempts))
}

func (n *recordingNotifier) Stalled(_ context.Context, stalledFor time.Duration, waitingOn string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.stalls = append(n.stalls, fmt.Sprintf("%s: %s", stalledFor, waitingOn))
}

func (n *recordingNotifier) stallList() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.stalls...)
}

func (n *recordingNotifier) blockedList() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.blocked...)
}

func (n *recordingNotifier) ciBlockedList() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.ciBlocks...)
}

// TestABlockedTaskIsAnnounced: the whole reason the side channel exists is
// that an operator should not have to watch the dashboard to learn a task
// gave up.
func TestABlockedTaskIsAnnounced(t *testing.T) {
	f := newReapFixture(t,
		[]model.Task{task("C-1", 1, "claude/opus")},
		map[string]fakeResponse{"C-1": failResponse("authentication_error: bad key")},
		SchedulerOptions{Cfg: config.Config{Retry: config.Retry{MaxAttempts: 1}}})
	n := &recordingNotifier{}
	f.s.Notify = n

	f.runOnce(t)

	got := n.blockedList()
	if len(got) != 1 {
		t.Fatalf("announced %d blocks, want 1: %v", len(got), got)
	}
	if !strings.Contains(got[0], "C-1") {
		t.Errorf("the announcement does not name the task: %q", got[0])
	}
	if !strings.Contains(got[0], "authentication_error") {
		t.Errorf("the announcement does not carry the reason: %q", got[0])
	}
}

// TestASuccessIsNotAnnounced: the channel is for bad news. Announcing every
// finished task is how a team mutes the channel, and then the blocks go
// unseen too.
func TestASuccessIsNotAnnounced(t *testing.T) {
	f := newReapFixture(t,
		[]model.Task{task("C-1", 1, "claude/opus")},
		map[string]fakeResponse{"C-1": okResponse()},
		SchedulerOptions{})
	n := &recordingNotifier{}
	f.s.Notify = n

	f.runOnce(t)

	if got := n.blockedList(); len(got) != 0 {
		t.Errorf("announced %v for a successful task", got)
	}
}

// TestARetriedTaskIsNotAnnounced: a task that will try again has not given
// up, and a message per attempt is noise.
func TestARetriedTaskIsNotAnnounced(t *testing.T) {
	f := newReapFixture(t,
		[]model.Task{task("C-1", 1, "claude/opus")},
		map[string]fakeResponse{"C-1": failResponse("503 service unavailable")},
		SchedulerOptions{})
	n := &recordingNotifier{}
	f.s.Notify = n

	f.runOnce(t)

	if len(f.s.RetryQueue()) != 1 {
		t.Fatal("expected the task to be queued for retry")
	}
	if got := n.blockedList(); len(got) != 0 {
		t.Errorf("announced %v for a task that will retry", got)
	}
}

// The dispatch prompt invites feedback about orch exactly when the project
// opted into report_findings: the scheduler, not a caller, carries the setting
// from config.yaml into what the agent reads.
func TestDispatchPromptCarriesTheFindingsOptIn(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		opts := SchedulerOptions{Cfg: config.Config{
			Retry:          config.Retry{MaxAttempts: 2, BackoffSeconds: 5, RateLimitBackoffSeconds: 60},
			ReportFindings: config.ReportFindings{Enabled: enabled},
		}}
		f := newReapFixture(t, []model.Task{task("C-1", 1, "claude/opus")},
			map[string]fakeResponse{"C-1": okResponse()}, opts)
		f.runOnce(t)

		raw, err := os.ReadFile(filepath.Join(f.s.Opts.StateDir, "prompts", f.s.Opts.RunID, "C-1.txt"))
		if err != nil {
			t.Fatalf("read the dispatched prompt: %v", err)
		}
		if got := strings.Contains(string(raw), "orch_report_finding"); got != enabled {
			t.Errorf("report_findings.enabled=%v: prompt mentions orch_report_finding = %v", enabled, got)
		}
	}
}
