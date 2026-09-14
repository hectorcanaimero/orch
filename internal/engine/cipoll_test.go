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

	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/state"
	"github.com/hectorcanaimero/orch/internal/vcs"
)

// ---- Doubles -------------------------------------------------------------

type fakeVCS struct {
	mu sync.Mutex
	// status is what CIStatus answers per PR URL.
	status    map[string]vcs.CIState
	statusErr error
	logs      string
	logsErr   error
	mergeErr  error
	// merged records every MergePR call, so "did not merge" is assertable.
	merged []string

	// The CreatePR side, for the auto-PR tests.
	createdPR string
	createErr error
	created   int
	prHead    string
	prBase    string
	prTitle   string
	prBody    string
}

func (v *fakeVCS) CreatePR(head, base, title, body string) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.created++
	v.prHead, v.prBase, v.prTitle, v.prBody = head, base, title, body
	if v.createErr != nil {
		return "", v.createErr
	}
	return v.createdPR, nil
}

func (v *fakeVCS) CIStatus(prURL string) (vcs.CIState, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.statusErr != nil {
		return "", v.statusErr
	}
	return v.status[prURL], nil
}

func (v *fakeVCS) CILogs(string) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.logsErr != nil {
		return "", v.logsErr
	}
	return v.logs, nil
}

func (v *fakeVCS) MergePR(prURL string, _, _ bool) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.merged = append(v.merged, prURL)
	return v.mergeErr
}

func (v *fakeVCS) mergedPRs() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]string(nil), v.merged...)
}

type fakeCIBackend struct {
	mu         sync.Mutex
	rows       []state.TaskRuntime
	listErr    error
	ciStatuses []string
	increments []string
	// counters is what IncrementCIAttempts hands back, per task.
	counters     map[string]int
	incrementErr error
}

func (b *fakeCIBackend) TasksWithPendingCI(context.Context) ([]state.TaskRuntime, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.listErr != nil {
		return nil, b.listErr
	}
	return append([]state.TaskRuntime(nil), b.rows...), nil
}

func (b *fakeCIBackend) SetTaskCIStatus(_ context.Context, taskID, status string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ciStatuses = append(b.ciStatuses, taskID+"="+status)
	// Like the real filter: a row whose CI resolved is no longer pending.
	if status != CIStatusPending {
		kept := b.rows[:0]
		for _, r := range b.rows {
			if r.ID != taskID {
				kept = append(kept, r)
			}
		}
		b.rows = kept
	}
	return nil
}

func (b *fakeCIBackend) IncrementCIAttempts(_ context.Context, taskID string) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.increments = append(b.increments, taskID)
	if b.incrementErr != nil {
		return 0, b.incrementErr
	}
	b.counters[taskID]++
	return b.counters[taskID], nil
}

func (b *fakeCIBackend) statuses() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.ciStatuses...)
}

func (b *fakeCIBackend) incremented() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.increments...)
}

// fakeCIWorktree hands back a real directory, so the feedback file is
// actually written and can be read back.
type fakeCIWorktree struct {
	dir         string
	recreateErr error
	recreated   []string
}

func (w *fakeCIWorktree) Recreate(_ context.Context, taskID string) (string, error) {
	w.recreated = append(w.recreated, taskID)
	if w.recreateErr != nil {
		return "", w.recreateErr
	}
	path := filepath.Join(w.dir, taskID)
	if err := os.MkdirAll(path, 0o750); err != nil {
		return "", err
	}
	return path, nil
}

// ---- Fixture -------------------------------------------------------------

type ciFixture struct {
	*reapFixture
	poller *CIPoller
	vcs    *fakeVCS
	ci     *fakeCIBackend
	wt     *fakeCIWorktree
}

func newCIFixture(t *testing.T, row state.TaskRuntime) *ciFixture {
	t.Helper()
	rf := newReapFixture(t,
		[]model.Task{task(row.ID, 1, "claude/opus")},
		map[string]fakeResponse{row.ID: okResponse()},
		SchedulerOptions{})

	v := &fakeVCS{status: map[string]vcs.CIState{}}
	b := &fakeCIBackend{rows: []state.TaskRuntime{row}, counters: map[string]int{row.ID: row.CIAttempts}}
	w := &fakeCIWorktree{dir: t.TempDir()}

	return &ciFixture{
		reapFixture: rf,
		vcs:         v,
		ci:          b,
		wt:          w,
		poller:      &CIPoller{Provider: v, Backend: b, Worktree: w},
	}
}

func pendingRow(id, prURL string, attempts int) state.TaskRuntime {
	return state.TaskRuntime{
		ID: id, Status: model.StatusInProgress, PRURL: prURL,
		CIStatus: CIStatusPending, CIAttempts: attempts, LastBackend: "claude",
	}
}

// ---- Green CI ------------------------------------------------------------

func TestCISuccessFinishesTheTask(t *testing.T) {
	f := newCIFixture(t, pendingRow("C-1", "https://github.com/o/r/pull/1", 0))
	f.vcs.status["https://github.com/o/r/pull/1"] = vcs.CISuccess

	acted, err := f.poller.Poll(context.Background(), f.s)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if acted != 1 {
		t.Errorf("acted on %d tasks, want 1", acted)
	}
	if got, _ := f.s.Queue.Status("C-1"); got != model.StatusDone {
		t.Errorf("status = %q, want done", got)
	}
	if got := f.ci.statuses(); !equalStrings(got, []string{"C-1=success"}) {
		t.Errorf("ci status writes = %v", got)
	}
	if got := f.backend.eventTypes("C-1"); !equalStrings(got, []string{EventCISuccess}) {
		t.Errorf("events = %v, want just ci_success", got)
	}
	// auto_merge is off by default, so nothing was merged.
	if got := f.vcs.mergedPRs(); len(got) != 0 {
		t.Errorf("merged %v with auto-merge off", got)
	}
}

func TestCISuccessAutoMerge(t *testing.T) {
	tests := []struct {
		name      string
		mergeErr  error
		wantEvent string
	}{
		{name: "a merge that works", wantEvent: EventPRAutoMerged},
		{
			// The PR is green and open; a human merging it is a fine
			// outcome, so this is reported rather than retried.
			name:      "a merge that does not",
			mergeErr:  errors.New("branch is not up to date"),
			wantEvent: EventPRAutoMergeFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newCIFixture(t, pendingRow("C-1", "https://github.com/o/r/pull/1", 0))
			f.vcs.status["https://github.com/o/r/pull/1"] = vcs.CISuccess
			f.vcs.mergeErr = tt.mergeErr
			f.poller.AutoMerge = true

			if _, err := f.poller.Poll(context.Background(), f.s); err != nil {
				t.Fatalf("Poll: %v", err)
			}
			if got := f.vcs.mergedPRs(); len(got) != 1 {
				t.Fatalf("merged %v, want one attempt", got)
			}
			events := f.backend.eventTypes("C-1")
			if len(events) != 2 || events[1] != tt.wantEvent {
				t.Errorf("events = %v, want ci_success then %s", events, tt.wantEvent)
			}
			// Either way the task is done: the merge is not what decides it.
			if got, _ := f.s.Queue.Status("C-1"); got != model.StatusDone {
				t.Errorf("status = %q, want done", got)
			}
		})
	}
}

// ---- Red CI --------------------------------------------------------------

// TestCIFailureRedispatchesWithTheLogs: the logs are the whole point of a CI
// retry, so they must reach the worktree the agent will open.
func TestCIFailureRedispatchesWithTheLogs(t *testing.T) {
	f := newCIFixture(t, pendingRow("C-1", "https://github.com/o/r/pull/1", 0))
	f.vcs.status["https://github.com/o/r/pull/1"] = vcs.CIFailure
	f.vcs.logs = "FAIL internal/foo: TestBar\n  expected 3, got 4"

	if _, err := f.poller.Poll(context.Background(), f.s); err != nil {
		t.Fatalf("Poll: %v", err)
	}

	if got, _ := f.s.Queue.Status("C-1"); got != model.StatusTodo {
		t.Errorf("status = %q, want todo so the retry can pick it up", got)
	}
	queued := f.s.RetryQueue()
	if len(queued) != 1 {
		t.Fatalf("retry queue has %d items, want 1", len(queued))
	}
	// CI already took its time; the fix should go in while the logs are
	// fresh, so there is no extra backoff.
	if queued[0].EarliestAt.After(f.s.now()) {
		t.Errorf("the CI retry was given a backoff: %v", queued[0].EarliestAt)
	}

	body, err := os.ReadFile(filepath.Join(f.wt.dir, "C-1", CIFeedbackFile)) // #nosec G304 -- a temp dir this test made
	if err != nil {
		t.Fatalf("reading the feedback file: %v", err)
	}
	if !strings.Contains(string(body), "expected 3, got 4") {
		t.Errorf("the feedback file does not carry the logs:\n%s", body)
	}
	if !strings.Contains(string(body), "CI Failure") {
		t.Errorf("the feedback file lost its heading:\n%s", body)
	}

	if got := f.ci.incremented(); !equalStrings(got, []string{"C-1"}) {
		t.Errorf("incremented = %v, want C-1", got)
	}
	if got := f.ci.statuses(); !equalStrings(got, []string{"C-1=pending"}) {
		t.Errorf("ci status writes = %v, want it back to pending", got)
	}
	if got := f.backend.eventTypes("C-1"); !equalStrings(got,
		[]string{EventCIRedispatch, EventCIFailureRetry}) {
		t.Errorf("events = %v", got)
	}
}

// TestCIFailureRetryEventCountsTheAttemptJustStarted. Python emits
// `ci_attempts + 1`, not the stored counter — an off-by-one here is only
// visible with ci_max_retries > 1, which is why it gets its own test.
func TestCIFailureRetryEventCountsTheAttemptJustStarted(t *testing.T) {
	f := newCIFixture(t, pendingRow("C-1", "https://github.com/o/r/pull/1", 1))
	f.vcs.status["https://github.com/o/r/pull/1"] = vcs.CIFailure
	f.poller.MaxRetries = 3

	if _, err := f.poller.Poll(context.Background(), f.s); err != nil {
		t.Fatalf("Poll: %v", err)
	}

	f.backend.mu.Lock()
	defer f.backend.mu.Unlock()
	for _, e := range f.backend.events {
		if e.eventType != EventCIFailureRetry {
			continue
		}
		if got := e.extra["attempt"]; got != 2 {
			t.Errorf("attempt = %v, want 2 — the number the write handed back, not the stored 1", got)
		}
		return
	}
	t.Error("no ci_failure_retry event")
}

// TestCIFailureBlocksOnceTheAttemptsAreUsed.
func TestCIFailureBlocksOnceTheAttemptsAreUsed(t *testing.T) {
	f := newCIFixture(t, pendingRow("C-1", "https://github.com/o/r/pull/1", 1))
	f.vcs.status["https://github.com/o/r/pull/1"] = vcs.CIFailure
	f.poller.MaxRetries = 1 // already used

	if _, err := f.poller.Poll(context.Background(), f.s); err != nil {
		t.Fatalf("Poll: %v", err)
	}

	if got, _ := f.s.Queue.Status("C-1"); got != model.StatusBlocked {
		t.Errorf("status = %q, want blocked", got)
	}
	if len(f.s.RetryQueue()) != 0 {
		t.Error("a task out of CI attempts was queued for retry")
	}
	if got := f.ci.statuses(); !equalStrings(got, []string{"C-1=failure"}) {
		t.Errorf("ci status writes = %v", got)
	}
	if got := f.backend.eventTypes("C-1"); !equalStrings(got, []string{EventCIBlocked}) {
		t.Errorf("events = %v, want just ci_blocked", got)
	}
	if got := f.wt.recreated; len(got) != 0 {
		t.Errorf("recreated %v — a blocked task needs no worktree", got)
	}
}

// TestCIFailureWithUnreadableLogsStillRetries: a placeholder beats skipping
// the retry, which is Python's choice too.
func TestCIFailureWithUnreadableLogsStillRetries(t *testing.T) {
	f := newCIFixture(t, pendingRow("C-1", "https://github.com/o/r/pull/1", 0))
	f.vcs.status["https://github.com/o/r/pull/1"] = vcs.CIFailure
	f.vcs.logsErr = errors.New("gh: rate limited")

	if _, err := f.poller.Poll(context.Background(), f.s); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(f.s.RetryQueue()) != 1 {
		t.Fatal("want the retry queued despite the missing logs")
	}
	body, err := os.ReadFile(filepath.Join(f.wt.dir, "C-1", CIFeedbackFile)) // #nosec G304 -- a temp dir this test made
	if err != nil {
		t.Fatalf("reading the feedback file: %v", err)
	}
	if !strings.Contains(string(body), "log retrieval failed") {
		t.Errorf("want the placeholder in the file:\n%s", body)
	}
}

// TestCIRedispatchSkippedWhenTheWorktreeCannotBeRecreated: sending the agent
// back with no idea what broke is worse than leaving the task alone.
func TestCIRedispatchSkippedWhenTheWorktreeCannotBeRecreated(t *testing.T) {
	f := newCIFixture(t, pendingRow("C-1", "https://github.com/o/r/pull/1", 0))
	f.vcs.status["https://github.com/o/r/pull/1"] = vcs.CIFailure
	f.wt.recreateErr = errors.New("worktree is locked")

	if _, err := f.poller.Poll(context.Background(), f.s); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(f.s.RetryQueue()) != 0 {
		t.Error("re-dispatched with nowhere to leave the logs")
	}
	// And the counter did NOT move, so the attempt is still available once
	// whatever locked the worktree lets go.
	if got := f.ci.incremented(); len(got) != 0 {
		t.Errorf("incremented %v for a re-dispatch that did not happen", got)
	}
}

// TestCIFailureIgnoredWhileTheRedispatchIsStillRunning (#248). A re-dispatch
// does not push a new commit the instant it starts: the PR head CI reads is
// still describing the OLD failure until the new agent finishes. Without a
// guard, the very next poll sees that same stale failure and re-dispatches
// again — spawning a second live agent for the task and burning the retry
// budget on a failure that was never new.
func TestCIFailureIgnoredWhileTheRedispatchIsStillRunning(t *testing.T) {
	f := newCIFixture(t, pendingRow("C-1", "https://github.com/o/r/pull/1", 1))
	f.vcs.status["https://github.com/o/r/pull/1"] = vcs.CIFailure
	f.poller.MaxRetries = 2

	// Simulate a live agent already dispatched for C-1 — the re-dispatch this
	// same failure already caused, per the previous poll.
	task, _ := f.s.Queue.Task("C-1")
	f.s.inFlight[12345] = &InFlight{Task: task}

	if _, err := f.poller.Poll(context.Background(), f.s); err != nil {
		t.Fatalf("Poll: %v", err)
	}

	if got := f.ci.incremented(); len(got) != 0 {
		t.Errorf("incremented %v — the attempt already in flight must not be double-counted", got)
	}
	if len(f.s.RetryQueue()) != 0 {
		t.Error("queued a second retry while the first agent for this task is still running")
	}
	if got, _ := f.s.Queue.Status("C-1"); got == model.StatusBlocked {
		t.Error("blocked the task while its re-dispatched agent is still running")
	}
	if got := f.backend.eventTypes("C-1"); len(got) != 0 {
		t.Errorf("events = %v, want none — nothing changed since the last poll", got)
	}
}

// TestCIFailureIgnoredWhileAlreadyQueuedForRetry: the same race, one tick
// earlier — the re-dispatch has been queued but the scheduler has not yet
// forked the agent for it (still sitting in the retry queue, not s.inFlight).
func TestCIFailureIgnoredWhileAlreadyQueuedForRetry(t *testing.T) {
	f := newCIFixture(t, pendingRow("C-1", "https://github.com/o/r/pull/1", 1))
	f.vcs.status["https://github.com/o/r/pull/1"] = vcs.CIFailure
	f.poller.MaxRetries = 2

	task, _ := f.s.Queue.Task("C-1")
	f.s.retryQueue = append(f.s.retryQueue, RetryItem{Task: task, Attempt: 1, EarliestAt: f.s.now()})

	if _, err := f.poller.Poll(context.Background(), f.s); err != nil {
		t.Fatalf("Poll: %v", err)
	}

	if got := f.ci.incremented(); len(got) != 0 {
		t.Errorf("incremented %v — already queued for retry", got)
	}
	if len(f.s.RetryQueue()) != 1 {
		t.Errorf("retry queue has %d items, want the original 1", len(f.s.RetryQueue()))
	}
}

// TestCIFailureIgnoredWhileDraining: SIGINT drains an `orch run` — Refill
// stops, so a re-dispatch queued here can never actually start. Treating the
// unchanged failure as new anyway burns the whole retry budget in the couple
// of polls it takes to drain, blocking the task before a human ever sees it
// (#248 comment: "draining after SIGINT").
func TestCIFailureIgnoredWhileDraining(t *testing.T) {
	f := newCIFixture(t, pendingRow("C-1", "https://github.com/o/r/pull/1", 1))
	f.vcs.status["https://github.com/o/r/pull/1"] = vcs.CIFailure
	f.poller.MaxRetries = 1 // already used, so ciFailed would otherwise block immediately
	f.s.StartDraining()

	if _, err := f.poller.Poll(context.Background(), f.s); err != nil {
		t.Fatalf("Poll: %v", err)
	}

	if got, _ := f.s.Queue.Status("C-1"); got == model.StatusBlocked {
		t.Error("blocked the task while draining; nothing can dispatch a fix right now")
	}
	if got := f.ci.incremented(); len(got) != 0 {
		t.Errorf("incremented %v while draining", got)
	}
	if got := f.backend.eventTypes("C-1"); len(got) != 0 {
		t.Errorf("events = %v, want none while draining", got)
	}
}

// ---- No checks at all ----------------------------------------------------

// TestCINoChecksFinishesAfterTheGrace: #233. A repo with no workflow never
// reports a check, and waiting on one forever deadlocked the run — including
// the task meant to add CI. Past the grace the task is done, as it would be
// without auto_pr, and nothing is merged: no check vouched for it.
func TestCINoChecksFinishesAfterTheGrace(t *testing.T) {
	f := newCIFixture(t, pendingRow("C-1", "https://github.com/o/r/pull/1", 0))
	f.vcs.status["https://github.com/o/r/pull/1"] = vcs.CINone
	f.poller.AutoMerge = true
	f.poller.PollInterval = time.Second

	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	f.s.now = func() time.Time { return now }

	if _, err := f.poller.Poll(context.Background(), f.s); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	now = now.Add(ciNoChecksGrace - time.Second)
	if _, err := f.poller.Poll(context.Background(), f.s); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if got, _ := f.s.Queue.Status("C-1"); got == model.StatusDone {
		t.Fatal("finished inside the grace: a PR opened a moment ago has no checks yet either")
	}

	now = now.Add(time.Second)
	acted, err := f.poller.Poll(context.Background(), f.s)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if acted != 1 {
		t.Errorf("acted on %d tasks, want 1", acted)
	}
	if got, _ := f.s.Queue.Status("C-1"); got != model.StatusDone {
		t.Errorf("status = %q, want done once the grace passed", got)
	}
	if got := f.ci.statuses(); !equalStrings(got, []string{"C-1=" + CIStatusSkipped}) {
		t.Errorf("ci status writes = %v, want skipped", got)
	}
	if got := f.backend.eventTypes("C-1"); !equalStrings(got, []string{EventCINoChecks}) {
		t.Errorf("events = %v, want just ci_no_checks", got)
	}
	if got := f.vcs.mergedPRs(); len(got) != 0 {
		t.Errorf("merged %v with no check to vouch for it", got)
	}
}

// Checks that show up restart the clock: the grace is for a PR that has had
// none the whole time, not one whose CI is between runs.
func TestCINoChecksGraceRestartsWhenChecksAppear(t *testing.T) {
	f := newCIFixture(t, pendingRow("C-1", "https://github.com/o/r/pull/1", 0))
	url := "https://github.com/o/r/pull/1"
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	f.s.now = func() time.Time { return now }

	poll := func(st vcs.CIState, advance time.Duration) {
		t.Helper()
		now = now.Add(advance)
		f.vcs.mu.Lock()
		f.vcs.status[url] = st
		f.vcs.mu.Unlock()
		if _, err := f.poller.Poll(context.Background(), f.s); err != nil {
			t.Fatalf("Poll: %v", err)
		}
	}
	poll(vcs.CINone, 0)
	poll(vcs.CIPending, ciNoChecksGrace/2)
	poll(vcs.CINone, ciNoChecksGrace/2)
	poll(vcs.CINone, ciNoChecksGrace/2)

	if got, _ := f.s.Queue.Status("C-1"); got == model.StatusDone {
		t.Error("finished although checks were reported inside the grace")
	}
}

// TestCIConflictBlocksTheTask: #236. A conflicting PR gets no workflow run, so
// it must neither wait forever nor pass as a repo without CI.
func TestCIConflictBlocksTheTask(t *testing.T) {
	f := newCIFixture(t, pendingRow("C-1", "https://github.com/o/r/pull/1", 0))
	f.vcs.status["https://github.com/o/r/pull/1"] = vcs.CIConflict
	n := &recordingNotifier{}
	f.s.Notify = n

	if _, err := f.poller.Poll(context.Background(), f.s); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if got, _ := f.s.Queue.Status("C-1"); got != model.StatusBlocked {
		t.Errorf("status = %q, want blocked", got)
	}
	if got := f.ci.statuses(); !equalStrings(got, []string{"C-1=" + CIStatusFailure}) {
		t.Errorf("ci status writes = %v, want failure", got)
	}
	if got := f.backend.eventTypes("C-1"); !equalStrings(got, []string{EventCIBlocked}) {
		t.Errorf("events = %v, want ci_blocked", got)
	}
	if got := n.ciBlockedList(); len(got) != 1 {
		t.Errorf("announced %d CI blocks, want 1", len(got))
	}
}

// ---- Polling behaviour ---------------------------------------------------

func TestPollRespectsItsInterval(t *testing.T) {
	f := newCIFixture(t, pendingRow("C-1", "https://github.com/o/r/pull/1", 0))
	f.vcs.status["https://github.com/o/r/pull/1"] = vcs.CIPending
	f.poller.PollInterval = time.Minute

	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	f.s.now = func() time.Time { return now }

	if _, err := f.poller.Poll(context.Background(), f.s); err != nil {
		t.Fatalf("first Poll: %v", err)
	}
	first := len(f.ci.statuses())

	// Well inside the interval: nothing asked.
	now = now.Add(10 * time.Second)
	if _, err := f.poller.Poll(context.Background(), f.s); err != nil {
		t.Fatalf("second Poll: %v", err)
	}

	f.vcs.mu.Lock()
	f.vcs.status["https://github.com/o/r/pull/1"] = vcs.CISuccess
	f.vcs.mu.Unlock()
	if _, err := f.poller.Poll(context.Background(), f.s); err != nil {
		t.Fatalf("third Poll: %v", err)
	}
	if got := len(f.ci.statuses()); got != first {
		t.Errorf("the poller acted inside its own interval (%d writes)", got)
	}

	// Past it: the green status is picked up.
	now = now.Add(time.Minute)
	if _, err := f.poller.Poll(context.Background(), f.s); err != nil {
		t.Fatalf("fourth Poll: %v", err)
	}
	if got, _ := f.s.Queue.Status("C-1"); got != model.StatusDone {
		t.Errorf("status = %q, want done once the interval passed", got)
	}
}

func TestPollIsInertWithoutAProviderOrBackend(t *testing.T) {
	f := newCIFixture(t, pendingRow("C-1", "https://github.com/o/r/pull/1", 0))
	f.vcs.status["https://github.com/o/r/pull/1"] = vcs.CISuccess

	for name, p := range map[string]*CIPoller{
		"nil poller":  nil,
		"no provider": {Backend: f.ci},
		"no backend":  {Provider: f.vcs},
	} {
		t.Run(name, func(t *testing.T) {
			acted, err := p.Poll(context.Background(), f.s)
			if err != nil {
				t.Fatalf("Poll: %v", err)
			}
			if acted != 0 {
				t.Errorf("acted on %d tasks while disabled", acted)
			}
		})
	}
	if got, _ := f.s.Queue.Status("C-1"); got != model.StatusTodo {
		t.Errorf("status = %q — a disabled poller changed something", got)
	}
}

// TestPollSkipsRowsWithNoPR: asking `gh` about an empty URL is an error per
// tick, forever.
func TestPollSkipsRowsWithNoPR(t *testing.T) {
	f := newCIFixture(t, state.TaskRuntime{
		ID: "C-1", Status: model.StatusInProgress, CIStatus: CIStatusPending,
	})
	f.vcs.status[""] = vcs.CISuccess // would "succeed" if asked

	acted, err := f.poller.Poll(context.Background(), f.s)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if acted != 0 {
		t.Errorf("acted on a row with no PR")
	}
	if got, _ := f.s.Queue.Status("C-1"); got != model.StatusTodo {
		t.Errorf("status = %q, want it untouched", got)
	}
}

// TestPollSurvivesAProviderError: one unreadable PR must not stop the sweep,
// and must not be mistaken for a verdict.
func TestPollSurvivesAProviderError(t *testing.T) {
	f := newCIFixture(t, pendingRow("C-1", "https://github.com/o/r/pull/1", 0))
	f.vcs.statusErr = errors.New("gh: connection reset")

	acted, err := f.poller.Poll(context.Background(), f.s)
	if err != nil {
		t.Fatalf("Poll: %v — a CLI hiccup is not a poller failure", err)
	}
	if acted != 0 {
		t.Errorf("acted on %d tasks despite not knowing their status", acted)
	}
	if got, _ := f.s.Queue.Status("C-1"); got != model.StatusTodo {
		t.Errorf("status = %q — an unreadable status became a verdict", got)
	}
}

func TestPollReportsAListingFailure(t *testing.T) {
	f := newCIFixture(t, pendingRow("C-1", "https://github.com/o/r/pull/1", 0))
	f.ci.listErr = errors.New("database is locked")

	if _, err := f.poller.Poll(context.Background(), f.s); err == nil {
		t.Fatal("want an error when the whole sweep cannot happen")
	}
}

// TestCIAttemptNumberComesFromTheWrite: the event carries the number the
// increment handed back, not arithmetic on the row read at the start of the
// sweep. The two can disagree — the row is a snapshot — and the column is
// what the next poll compares against.
func TestCIAttemptNumberComesFromTheWrite(t *testing.T) {
	f := newCIFixture(t, pendingRow("C-1", "https://github.com/o/r/pull/1", 0))
	f.vcs.status["https://github.com/o/r/pull/1"] = vcs.CIFailure
	f.poller.MaxRetries = 5
	// Something else already bumped the counter since the row was read.
	f.ci.counters["C-1"] = 3

	if _, err := f.poller.Poll(context.Background(), f.s); err != nil {
		t.Fatalf("Poll: %v", err)
	}

	f.backend.mu.Lock()
	defer f.backend.mu.Unlock()
	for _, e := range f.backend.events {
		if e.eventType != EventCIFailureRetry {
			continue
		}
		if got := e.extra["attempt"]; got != 4 {
			t.Errorf("attempt = %v, want 4 from the write — not 1 from the stale row", got)
		}
		return
	}
	t.Error("no ci_failure_retry event")
}

// TestCIAttemptFallsBackWhenTheIncrementFails: the retry is already queued,
// so the event still has to say something, and the stale-row arithmetic is
// the best available answer.
func TestCIAttemptFallsBackWhenTheIncrementFails(t *testing.T) {
	f := newCIFixture(t, pendingRow("C-1", "https://github.com/o/r/pull/1", 1))
	f.vcs.status["https://github.com/o/r/pull/1"] = vcs.CIFailure
	f.poller.MaxRetries = 5
	f.ci.incrementErr = errors.New("database is locked")

	if _, err := f.poller.Poll(context.Background(), f.s); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(f.s.RetryQueue()) != 1 {
		t.Error("the retry should still be queued")
	}

	f.backend.mu.Lock()
	defer f.backend.mu.Unlock()
	for _, e := range f.backend.events {
		if e.eventType == EventCIFailureRetry {
			if got := e.extra["attempt"]; got != 2 {
				t.Errorf("attempt = %v, want the stale-row fallback of 2", got)
			}
			return
		}
	}
	t.Error("no ci_failure_retry event")
}

// TestCIBlockedIsAnnounced: a task blocked by CI is the other thing the
// operator wants pushed to them rather than discovered.
func TestCIBlockedIsAnnounced(t *testing.T) {
	f := newCIFixture(t, pendingRow("C-1", "https://github.com/o/r/pull/9", 1))
	f.vcs.status["https://github.com/o/r/pull/9"] = vcs.CIFailure
	f.poller.MaxRetries = 1 // already used
	n := &recordingNotifier{}
	f.s.Notify = n

	if _, err := f.poller.Poll(context.Background(), f.s); err != nil {
		t.Fatalf("Poll: %v", err)
	}

	got := n.ciBlockedList()
	if len(got) != 1 {
		t.Fatalf("announced %d CI blocks, want 1: %v", len(got), got)
	}
	// The PR and the attempt count are what make the message actionable.
	for _, want := range []string{"C-1", "https://github.com/o/r/pull/9", "x1"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("the announcement is missing %q: %q", want, got[0])
		}
	}
}

// TestCIRetryIsNotAnnounced: a CI failure that re-dispatches has not given up.
func TestCIRetryIsNotAnnounced(t *testing.T) {
	f := newCIFixture(t, pendingRow("C-1", "https://github.com/o/r/pull/9", 0))
	f.vcs.status["https://github.com/o/r/pull/9"] = vcs.CIFailure
	n := &recordingNotifier{}
	f.s.Notify = n

	if _, err := f.poller.Poll(context.Background(), f.s); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if got := n.ciBlockedList(); len(got) != 0 {
		t.Errorf("announced %v for a CI failure that will retry", got)
	}
}
