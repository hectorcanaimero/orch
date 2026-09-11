package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hectorcanaimero/orch/internal/config"
	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/providers"
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
	opts.Mode = ModeAuto
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
	mu       sync.Mutex
	calls    []string
	pushErr  error
	removeAt int
}

func (w *recordingWorktree) record(name string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls = append(w.calls, name)
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

			wt.mu.Lock()
			got := append([]string(nil), wt.calls...)
			wt.mu.Unlock()
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

	wt.mu.Lock()
	duringDrain := append([]string(nil), wt.calls...)
	wt.mu.Unlock()
	for _, c := range duringDrain {
		if c == "remove_all" {
			t.Fatal("RemoveAll ran during the drain")
		}
	}

	f.s.CleanupWorktrees(context.Background())
	wt.mu.Lock()
	defer wt.mu.Unlock()
	if last := wt.calls[len(wt.calls)-1]; last != "remove_all" {
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
