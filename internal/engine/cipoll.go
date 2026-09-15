package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/state"
	"github.com/hectorcanaimero/orch/internal/vcs"
)

// CI status values, as written into `tasks_runtime.ci_status`. Python's
// strings, because the dashboard and `orch status` read the column.
const (
	CIStatusPending = "pending"
	CIStatusSuccess = "success"
	CIStatusFailure = "failure"
	// CIStatusSkipped is how a project with auto-PR but no CI configured
	// says there was never anything to wait for — a different answer from
	// success. The poller never sees it, because TasksWithPendingCI returns
	// only rows still `pending`; it is named here so the four values that
	// the column's CHECK constraint allows are all accounted for in one
	// place.
	CIStatusSkipped = "skipped"
)

// CI event types. Python's spellings, from the set orch.py emits.
const (
	EventCIRedispatch      = "ci_redispatch"
	EventCISuccess         = "ci_success"
	EventCIFailureRetry    = "ci_failure_retry"
	EventCIBlocked         = "ci_blocked"
	EventPRAutoMerged      = "pr_auto_merged"
	EventPRAutoMergeFailed = "pr_auto_merge_failed"
	// EventCINoChecks is Go only: a PR finished with no check ever reported.
	EventCINoChecks = "ci_no_checks"
	// EventPRMerged is Go only: the PR was merged outside orch while its CI
	// was still being watched (#255).
	EventPRMerged = "pr_merged"
)

// ciNoChecksGrace is how long a PR may report no check at all before orch
// stops waiting for one. GitHub registers a workflow's checks within seconds
// of the push; a PR still bare after this has no CI to wait on (#233).
//
// ponytail: fixed and in memory, so a restarted run waits the grace again;
// a vcs.* config key if a repo's checks are ever slower to appear.
const ciNoChecksGrace = 10 * time.Minute

// defaultCIPollInterval matches Python's `vcs.ci_poll_interval_s` default.
const defaultCIPollInterval = 30 * time.Second

// defaultCIMaxRetries matches Python's `vcs.ci_max_retries` default: one
// re-dispatch with the CI logs, then the task is blocked.
const defaultCIMaxRetries = 1

// CIBackend is the slice of state.Backend the poller reads and writes. A
// narrow interface so the poller is testable without a database, and so
// every CI column this package touches is listed in one place.
type CIBackend interface {
	// TasksWithPendingCI returns tasks that have a PR open and whose CI has
	// not resolved — both conditions, because a task with no PR has no CI to
	// poll and one whose CI finished must not come back round.
	TasksWithPendingCI(ctx context.Context) ([]state.TaskRuntime, error)
	SetTaskCIStatus(ctx context.Context, taskID, status string) error
	// IncrementCIAttempts bumps the counter and returns its new value —
	// which is the number of the attempt just started. Python compares the
	// stored counter, increments it, and then emits `ci_attempts + 1`
	// separately; having the write hand back the number removes the chance
	// of the event and the column disagreeing, and the arithmetic only ever
	// showed its off-by-one with ci_max_retries > 1.
	IncrementCIAttempts(ctx context.Context, taskID string) (int, error)
}

// CIWorktree is the slice of internal/worktree the poller needs: bringing a
// removed worktree back so the failing logs can be left in it.
type CIWorktree interface {
	Recreate(ctx context.Context, taskID string) (string, error)
}

// CIPoller watches the pull requests a run opened and acts on what CI says.
//
// Port of orch.py's `_check_ci_once`. It is only ever active when worktree
// mode AND auto-PR are both on — without a PR there is nothing to poll — so
// a nil Provider or a nil Backend disables it entirely rather than being an
// error.
type CIPoller struct {
	Provider vcs.Provider
	Backend  CIBackend
	// Worktree recreates a task's worktree so the CI logs have somewhere to
	// live. Nil means worktree mode is off, and a CI failure then blocks the
	// task rather than retrying blind.
	Worktree CIWorktree

	// PollInterval is how often CI is actually asked. Python reads
	// `vcs.ci_poll_interval_s`; zero means the default.
	PollInterval time.Duration
	// MaxRetries is how many times a failing CI re-dispatches the task with
	// its logs before the task is blocked. Python reads `vcs.ci_max_retries`.
	MaxRetries int
	// AutoMerge merges a PR whose CI went green. Python reads
	// `github.auto_merge`.
	AutoMerge bool
	// Squash and MergeWhenPassing are passed to MergePR. Python hardcodes
	// both on (`gh pr merge --squash --auto`); internal/vcs made them
	// parameters, so they are surfaced here and `orch run` sets both. Both
	// off is a `gh pr merge` with no method, which gh refuses outside a
	// terminal (#232).
	Squash           bool
	MergeWhenPassing bool

	lastPoll time.Time
	// noChecksSince is when each task's PR was first seen with no checks.
	noChecksSince map[string]time.Time
}

// Poll asks CI about every task waiting on it, if enough time has passed.
// Returns how many tasks it acted on.
//
// Never returns an error for a CI outcome — a red build is a result, not a
// failure of the poller. It returns one only when the whole sweep could not
// happen, and even then the loop carries on: a CI poller that can end a run
// would be worse than one that is briefly blind.
func (p *CIPoller) Poll(ctx context.Context, s *Scheduler) (int, error) {
	if p == nil || p.Provider == nil || p.Backend == nil {
		return 0, nil
	}
	now := s.now()
	if !p.lastPoll.IsZero() && now.Sub(p.lastPoll) < p.pollInterval() {
		return 0, nil
	}
	p.lastPoll = now

	pending, err := p.Backend.TasksWithPendingCI(ctx)
	if err != nil {
		return 0, fmt.Errorf("listing tasks with pending CI: %w", err)
	}

	acted := 0
	for _, row := range pending {
		if row.PRURL == "" {
			// Defensive: a row with no PR has no CI to ask about, and
			// asking `gh` about an empty URL is an error per tick forever.
			continue
		}
		if p.prSettled(ctx, s, row) {
			acted++
			continue
		}
		state, err := p.Provider.CIStatus(row.PRURL)
		if err != nil {
			s.logger().Warn("reading CI status failed; will retry next poll",
				"task", row.ID, "pr", row.PRURL, "err", err)
			continue
		}
		if state != vcs.CINone {
			delete(p.noChecksSince, row.ID)
		}
		switch state {
		case vcs.CISuccess:
			p.ciSucceeded(ctx, s, row)
			acted++
		case vcs.CIFailure:
			p.ciFailed(ctx, s, row)
			acted++
		case vcs.CIConflict:
			p.block(ctx, s, row, "the PR conflicts with its base branch, so CI will not run")
			acted++
		case vcs.CINone:
			if p.noChecksFor(row.ID, now) >= ciNoChecksGrace {
				delete(p.noChecksSince, row.ID)
				p.ciNoChecks(ctx, s, row)
				acted++
			}
		case vcs.CIPending:
			// Nothing to do yet.
		}
	}
	return acted, nil
}

// prSettled finishes the task of a PR merged outside orch, and blocks the task
// of one closed without merging. Reports whether it did. Either PR's CI is
// stale or unreadable, and polling it would go on forever (#255).
//
// A state it cannot read, or a provider that cannot tell, leaves the row to
// the CI verdict as before.
func (p *CIPoller) prSettled(ctx context.Context, s *Scheduler, row state.TaskRuntime) bool {
	// A CI retry running or queued for this task decides it at its reap, as
	// in ciFailed (#248): settling it here would dispatch a finished task.
	if s.inFlightIDs()[row.ID] || s.retryQueueIDs()[row.ID] {
		return false
	}
	prState, err := p.Provider.PRState(row.PRURL)
	if errors.Is(err, vcs.ErrPRStateUnsupported) {
		return false
	}
	if err != nil {
		s.logger().Warn("reading the PR state failed; asking CI instead",
			"task", row.ID, "pr", row.PRURL, "err", err)
		return false
	}
	switch prState {
	case vcs.PRMerged:
		delete(p.noChecksSince, row.ID)
		s.logger().Info("the PR was merged outside orch; finishing the task", "task", row.ID, "pr", row.PRURL)
		// success: the column has no "merged", and a merge is the change
		// accepted. The event says it was a person, not CI.
		p.finishTask(ctx, s, row, CIStatusSuccess, "PR merged: "+row.PRURL)
		p.emit(ctx, s, EventPRMerged, row, map[string]any{"pr_url": row.PRURL})
		return true
	case vcs.PRClosed:
		delete(p.noChecksSince, row.ID)
		p.block(ctx, s, row, "PR closed without merging: "+row.PRURL)
		return true
	case vcs.PROpen:
	}
	return false
}

// noChecksFor is how long the task's PR has reported no checks, starting the
// clock the first time it is seen that way.
func (p *CIPoller) noChecksFor(taskID string, now time.Time) time.Duration {
	if p.noChecksSince == nil {
		p.noChecksSince = map[string]time.Time{}
	}
	since, ok := p.noChecksSince[taskID]
	if !ok {
		p.noChecksSince[taskID] = now
		return 0
	}
	return now.Sub(since)
}

func (p *CIPoller) pollInterval() time.Duration {
	if p.PollInterval <= 0 {
		return defaultCIPollInterval
	}
	return p.PollInterval
}

func (p *CIPoller) maxRetries() int {
	if p.MaxRetries <= 0 {
		return defaultCIMaxRetries
	}
	return p.MaxRetries
}

// ciSucceeded finishes a task whose CI went green, and merges its PR when
// the project asked for that.
func (p *CIPoller) ciSucceeded(ctx context.Context, s *Scheduler, row state.TaskRuntime) {
	p.finishTask(ctx, s, row, CIStatusSuccess, "CI passed")
	p.emit(ctx, s, EventCISuccess, row, map[string]any{"pr_url": row.PRURL})

	if !p.AutoMerge {
		return
	}
	// A merge that does not happen is reported, not retried: the PR is open
	// and green, and a human merging it by hand is a perfectly good outcome.
	// Python distinguishes the two events for exactly that reason.
	event := EventPRAutoMerged
	if err := p.Provider.MergePR(row.PRURL, p.Squash, p.MergeWhenPassing); err != nil {
		s.logger().Warn("auto-merge failed; the PR is green and still open",
			"task", row.ID, "pr", row.PRURL, "err", err)
		event = EventPRAutoMergeFailed
	}
	p.emit(ctx, s, event, row, map[string]any{"pr_url": row.PRURL})
}

// ciNoChecks finishes a task whose PR never reported a check within the
// grace: the repo has no CI to wait on. `skipped` is the column's word for
// exactly that, and it takes the row out of the pending filter. No
// auto-merge — nothing vouched for the change.
func (p *CIPoller) ciNoChecks(ctx context.Context, s *Scheduler, row state.TaskRuntime) {
	s.logger().Warn("no CI check was reported; finishing the task without CI and without merging",
		"task", row.ID, "pr", row.PRURL, "grace", ciNoChecksGrace)
	p.finishTask(ctx, s, row, CIStatusSkipped, "no CI checks reported")
	p.emit(ctx, s, EventCINoChecks, row, map[string]any{"pr_url": row.PRURL})
}

// finishTask records a task done by the poller's verdict.
func (p *CIPoller) finishTask(ctx context.Context, s *Scheduler, row state.TaskRuntime, ciStatus, note string) {
	if err := p.Backend.SetTaskCIStatus(ctx, row.ID, ciStatus); err != nil {
		s.logger().Error("recording the CI status failed", "task", row.ID, "status", ciStatus, "err", err)
	}
	if err := s.Queue.MarkDone(row.ID); err != nil {
		s.logger().Error("mark done failed", "task", row.ID, "err", err)
	}
	// And in the database. The queue is this run's view; the row is what
	// `orch status` and the dashboard show afterwards, and a task whose CI
	// went green while the queue alone learned about it would read as
	// in-progress forever.
	if err := s.Backend.Transition(ctx, row.ID, model.StatusDone, note); err != nil {
		s.logger().Error("recording done failed", "task", row.ID, "err", err)
	}
}

// block is the CI verdict nothing more can change: a failure out of retries,
// or a PR whose CI will never run.
func (p *CIPoller) block(ctx context.Context, s *Scheduler, row state.TaskRuntime, reason string) {
	if err := p.Backend.SetTaskCIStatus(ctx, row.ID, CIStatusFailure); err != nil {
		s.logger().Error("recording CI failure failed", "task", row.ID, "err", err)
	}
	if err := s.Queue.MarkBlocked(row.ID); err != nil {
		s.logger().Error("mark blocked failed", "task", row.ID, "err", err)
	}
	if err := transitionThroughTodo(ctx, s.Backend, row.ID, model.StatusBlocked, reason); err != nil {
		s.logger().Error("recording blocked failed", "task", row.ID, "err", err)
	}
	p.emit(ctx, s, EventCIBlocked, row, map[string]any{
		"pr_url": row.PRURL, "attempts": row.CIAttempts, "reason": reason,
	})
	if s.Notify != nil {
		s.Notify.CIBlocked(ctx, row.ID, row.PRURL, row.CIAttempts)
	}
}

// ciFailed re-dispatches the task with the failing logs, or blocks it once
// the attempts are used up.
func (p *CIPoller) ciFailed(ctx context.Context, s *Scheduler, row state.TaskRuntime) {
	// A re-dispatch does not push a new commit the instant it is queued: the
	// PR head this poll just read is still describing the failure that
	// caused the LAST re-dispatch until the agent it started finishes and
	// pushes a fix. Nothing here tells a stale failure apart from a fresh one
	// by itself (CIStatus reports only the current state, not which head it
	// is for), so instead this skips the row entirely while that re-dispatch
	// is still outstanding — either already running (s.inFlightIDs) or
	// queued and waiting for the scheduler to fork it
	// (s.retryQueueIDs) — rather than counting the same failure twice (#248).
	if s.inFlightIDs()[row.ID] || s.retryQueueIDs()[row.ID] {
		return
	}
	// While draining, Refill never runs, so a re-dispatch queued here could
	// never actually start: it would just sit until the process exits. Same
	// unresolved failure, so it is left pending CI for the next run rather
	// than spending a retry — or blocking the task — on nothing (#248).
	if s.Draining() {
		return
	}
	if row.CIAttempts >= p.maxRetries() {
		p.block(ctx, s, row, "CI failed")
		return
	}

	// The logs are what makes the retry worth anything — without them the
	// agent re-runs the same work blind. A failure to fetch them is not a
	// reason to skip the retry, so it becomes the same placeholder Python
	// uses.
	logs, err := p.Provider.CILogs(row.PRURL)
	if err != nil {
		s.logger().Warn("fetching CI logs failed; retrying without them",
			"task", row.ID, "err", err)
		logs = "(log retrieval failed)"
	}

	if !p.redispatch(ctx, s, row, logs) {
		return
	}

	// The counter goes up AFTER the re-dispatch is queued. If the process
	// dies between the two, the task is queued with the count unchanged and
	// the next run tries again — biased towards retrying once too often
	// rather than giving up too early, which for CI is the right side.
	//
	// The attempt number in the event is the one the write hands back, not
	// arithmetic on the row we read: the row is a snapshot from the start of
	// the sweep, and a number computed here could disagree with the column.
	attempt, err := p.Backend.IncrementCIAttempts(ctx, row.ID)
	if err != nil {
		s.logger().Error("incrementing CI attempts failed", "task", row.ID, "err", err)
		attempt = row.CIAttempts + 1
	}
	if err := p.Backend.SetTaskCIStatus(ctx, row.ID, CIStatusPending); err != nil {
		s.logger().Error("resetting CI status failed", "task", row.ID, "err", err)
	}
	p.emit(ctx, s, EventCIFailureRetry, row, map[string]any{
		"pr_url": row.PRURL, "attempt": attempt,
	})
}

// redispatch puts the task back in the retry queue, with the failing CI logs
// waiting for it in its worktree. Reports whether it did.
//
// The worktree is recreated first, and a failure there skips the whole
// re-dispatch: the feedback file is the entire point of a CI retry, and
// sending the agent back in with no idea what broke is worse than leaving the
// task for a human.
func (p *CIPoller) redispatch(ctx context.Context, s *Scheduler, row state.TaskRuntime, ciLogs string) bool {
	task, ok := s.Queue.Task(row.ID)
	if !ok {
		s.logger().Warn("CI re-dispatch: the task is not in this run's DAG", "task", row.ID)
		return false
	}
	route, ok := s.Routes[task.Model]
	if !ok {
		s.logger().Warn("CI re-dispatch: no route for the task's model",
			"task", row.ID, "model", task.Model)
		return false
	}

	if p.Worktree == nil {
		s.logger().Warn("CI re-dispatch: worktree mode is off, so there is nowhere to leave the logs",
			"task", row.ID)
		return false
	}
	wtPath, err := p.Worktree.Recreate(ctx, row.ID)
	if err != nil {
		s.logger().Warn("CI re-dispatch: recreating the worktree failed; skipping",
			"task", row.ID, "err", err)
		return false
	}
	if err := writeCIFeedback(wtPath, ciLogs); err != nil {
		// The retry is still worth doing with a stale or missing file —
		// Python logs and carries on here too.
		s.logger().Warn("CI re-dispatch: writing the feedback file failed",
			"task", row.ID, "err", err)
	}

	// The backend has to be reopened to `todo` here, not just the in-memory
	// queue below: the agent already left the task `done` before its PR's CI
	// ran, and `done` only reopens to `todo`, never straight to
	// `in-progress`. Without this, the scheduler's own in-progress
	// transition moments from now is illegal, the write is rejected, and the
	// agent is spawned anyway on a task the database still calls done
	// (#251).
	if err := s.Backend.Transition(ctx, row.ID, model.StatusTodo, "CI failed; re-dispatching with logs"); err != nil {
		s.logger().Warn("CI re-dispatch: reopening the task failed; skipping", "task", row.ID, "err", err)
		return false
	}

	if err := s.Queue.MarkTodo(row.ID); err != nil {
		s.logger().Error("CI re-dispatch: resetting to todo failed", "task", row.ID, "err", err)
		return false
	}
	s.retryQueue = append(s.retryQueue, RetryItem{
		Task:    task,
		Route:   route,
		Attempt: 1,
		// No backoff: CI already took its time, and the point is to get the
		// fix in front of the agent while the logs are fresh.
		EarliestAt: s.now(),
	})
	s.ciRetryPR[row.ID] = row.PRURL
	p.emit(ctx, s, EventCIRedispatch, row, map[string]any{"pr_url": row.PRURL})
	return true
}

// CIFeedbackFile is where the failing CI logs are left for the agent, inside
// its own worktree. The name is Python's, and it is a contract with whatever
// the agent is told to read.
const CIFeedbackFile = ".orch-ci-feedback.md"

func writeCIFeedback(worktreePath, ciLogs string) error {
	path := filepath.Join(worktreePath, CIFeedbackFile)
	body := "# CI Failure — Please fix\n\n```\n" + ciLogs + "\n```\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// transitionThroughTodo moves a task to `to`, hopping through `todo` first
// when the direct move is illegal. An agent reports its own task `done`
// (task-finish.sh, orch_set_status) before orch ever opens the PR, so by the
// time CI hands down its own verdict the backend already has the task at
// `done` — which only reopens to `todo` (model.CanTransition), never
// straight to `in-progress` or `blocked`. Asking for the illegal move
// directly leaves the task `done` with a failing PR while its dependents
// are released as though the work had been accepted (#251).
func transitionThroughTodo(ctx context.Context, backend RecordBackend, taskID string, to model.Status, note string) error {
	err := backend.Transition(ctx, taskID, to, note)
	if err == nil || !errors.Is(err, state.ErrIllegalTransition) {
		return err
	}
	if reopenErr := backend.Transition(ctx, taskID, model.StatusTodo, note); reopenErr != nil {
		return fmt.Errorf("reopening %q to retry the move to %s failed: %w (original: %s)",
			taskID, to, reopenErr, err)
	}
	return backend.Transition(ctx, taskID, to, note)
}

func (p *CIPoller) emit(ctx context.Context, s *Scheduler, eventType string, row state.TaskRuntime, extra map[string]any) {
	if s.Backend == nil {
		return
	}
	if err := s.Backend.AppendEngineEvent(ctx, s.Opts.RunID, eventType, row.ID,
		row.LastBackend, extra); err != nil {
		s.logger().Error("recording a CI event failed",
			"task", row.ID, "event", eventType, "err", err)
	}
}
