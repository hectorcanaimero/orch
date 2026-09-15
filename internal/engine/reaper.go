package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/providers"
)

// completion is one finished child, posted by its supervising goroutine.
type completion struct {
	pid     int
	outcome Outcome
}

// The worktree ordering the reaper honours is the Caller contract in
// internal/worktree's package comment, which that package cannot enforce
// itself because it has no notion of a dispatch's outcome:
//
//  1. on a finished task, CommitPending then Push then Remove, and Push only
//     when the task succeeded — a Push failure must not skip Remove and must
//     not downgrade an otherwise-successful task;
//  2. RemoveAll after the drain wait completes, never from inside a signal
//     handler, because cleanup racing a still-draining task would delete a
//     worktree a backend process is still writing into.
//
// The interface itself is WorktreeManager, in worktreeadapter.go.

// supervise waits for one child and posts its outcome. Started by spawnOne
// for every successful spawn.
//
// One goroutine per dispatch rather than a poll loop: Go's os/exec reaps its
// own children, so a `wait4(-1)` sweep like Python's would race it. The
// channel is buffered to the concurrency ceiling so a supervisor never blocks
// on a reaper that is busy.
func (s *Scheduler) supervise(pid int, d Dispatch, sp *Spawned) {
	exitCode, reason, timedOut := sp.Wait(d.Timeout)
	duration := time.Since(sp.StartedAt)

	out := Outcome{
		PID:       pid,
		StartedAt: sp.StartedAt,
		Duration:  duration,
		TimedOut:  timedOut,
		LogPath:   d.LogPath,
	}
	out.Result = d.Provider.Parse(exitCode, []byte(ReadLog(d.LogPath)))
	if timedOut {
		out.Result.Success = false
		out.Result.ErrorMessage = reason
	}
	if !out.Result.Success {
		if timedOut {
			out.Failure = providers.FailureTimeout
		} else {
			out.Failure = providers.Classify(out.Result)
		}
	}

	s.done <- completion{pid: pid, outcome: out}
}

// Reap handles every child that has finished since the last call, and returns
// how many it handled. It never blocks: a tick with nothing finished does
// nothing, which is what lets the caller alternate Reap and Refill.
func (s *Scheduler) Reap(ctx context.Context) (int, error) {
	reaped := 0
	for {
		select {
		case c := <-s.done:
			if err := s.finish(ctx, c); err != nil {
				return reaped, err
			}
			reaped++
		default:
			return reaped, nil
		}
	}
}

// finish applies one completed dispatch: record it, tidy its worktree, then
// either queue a retry or reach a terminal verdict. Slots and the task lock
// are always given back, on every path.
func (s *Scheduler) finish(ctx context.Context, c completion) error {
	entry, ok := s.inFlight[c.pid]
	if !ok {
		// Not one of ours. Python skips a stray pid the same way.
		return nil
	}
	delete(s.inFlight, c.pid)

	out := c.outcome
	s.spentUSD[entry.Task.ID] += out.Result.CostUSD

	d := entry.Dispatch
	if s.Backend != nil {
		if err := s.Backend.RecordFinish(ctx, s.Opts.RunID, d, out, entry.Attempt); err != nil {
			// The child is already done and its cost already spent. Losing
			// the row costs observability, not correctness.
			s.logger().Error("recording the outcome failed",
				"task", entry.Task.ID, "err", err)
		}
	}

	// The agent may have blocked the task itself (orch_block, task-block.sh)
	// and then exited cleanly, which the wrapper reports as a success. The
	// state backend is asked rather than the wrapper believed — the same rule
	// handleFailure applies to a done — and a blocked task's branch is not
	// published: tidied as a failure, it is committed locally and no further.
	agentBlocked := out.Result.Success && s.taskStatusIs(ctx, entry.Task.ID, model.StatusBlocked)
	tidyOut := out
	if agentBlocked {
		tidyOut.Result.Success = false
	}
	if denied := out.Result.PermissionDenials; len(denied) > 0 {
		s.logger().Warn("the CLI denied tool calls; allow them in .claude/settings.json if the task needs them",
			"task", entry.Task.ID, "denied", strings.Join(denied, ", "))
	}
	prOpened, noPR := s.tidyWorktree(ctx, entry, tidyOut)

	// Capacity comes back before the verdict, so a retry queued below can be
	// picked up by the very next refill rather than waiting a tick.
	s.Sems.Release(string(entry.Route.Backend))
	entry.Lock.Release()

	switch {
	case agentBlocked:
		return s.keepAgentBlock(ctx, entry)
	case !out.Result.Success:
		return s.handleFailure(ctx, entry, out)
	case prOpened:
		// The work is pushed and under review. The CI poller decides
		// whether it is done; marking it now would finish a task whose
		// tests have not run.
		s.logger().Info("waiting on CI", "task", entry.Task.ID)
		return nil
	case noPR != "":
		// A PR was expected and none exists. In worktree mode that is the
		// only road from the task's branch to the base, so finishing it
		// would release dependents onto work that is not there (#230).
		// Waiting is no better: nothing records a URL to poll.
		return s.blockTask(ctx, entry, truncateReason(noPR), out)
	}
	return s.markDone(ctx, entry)
}

// keepAgentBlock honours a block the sub-agent recorded before exiting
// cleanly. The database already says blocked, with the agent's own note, so
// only the queue and the operator are told.
func (s *Scheduler) keepAgentBlock(ctx context.Context, entry *InFlight) error {
	s.logger().Info("the sub-agent blocked the task and exited cleanly; keeping it blocked",
		"task", entry.Task.ID)
	if err := s.Queue.MarkBlocked(entry.Task.ID); err != nil {
		s.logger().Error("mark blocked failed", "task", entry.Task.ID, "err", err)
	}
	if s.Notify != nil {
		s.Notify.Blocked(ctx, entry.Task.ID, "blocked by the agent")
	}
	return nil
}

// tidyWorktree runs the Caller contract from internal/worktree.
// tidyWorktree returns whether it opened a PR — a task waiting on CI is not
// finished, so the caller must not mark it done yet — and, when a PR was
// expected (auto_pr on) but none was opened, why not.
func (s *Scheduler) tidyWorktree(ctx context.Context, entry *InFlight, out Outcome) (prOpened bool, noPR string) {
	if s.Worktree == nil {
		return false, ""
	}
	id := entry.Task.ID

	// Commit first: without it the branch is empty and Remove then discards
	// whatever the agent wrote (Sprint F-6, issue #60).
	if err := s.Worktree.CommitPending(ctx, id); err != nil {
		s.logger().Error("worktree: commit failed", "task", id, "err", err)
	}

	// Push only on success — incomplete work does not get published. The
	// work is committed locally either way. Without auto_pr a push failure
	// does not downgrade the task; with it, no PR can follow, and the caller
	// blocks the task rather than finish work that never reached the base.
	//
	// The PR is opened only after the push actually succeeded. Opening one
	// from a branch that never reached the remote would produce a PR that
	// cannot be reviewed and a task waiting on CI that will never run.
	if out.Result.Success {
		if err := s.Worktree.Push(ctx, id); err != nil {
			s.logger().Error("worktree: push failed", "task", id, "err", err)
			if s.prExpected() {
				noPR = "the agent exited cleanly but its work was not pushed, so no PR was opened: " + err.Error()
			}
		} else if s.prExpected() {
			// A CI retry's PR is kept until a push reaches it, so a retry
			// whose agent failed first still finds it on the next attempt.
			noPR = s.openPR(ctx, entry, s.ciRetryPR[id])
			delete(s.ciRetryPR, id)
			prOpened = noPR == ""
		}
	}

	// Remove always, including after a failed push.
	if err := s.Worktree.Remove(ctx, id); err != nil {
		s.logger().Error("worktree: remove failed", "task", id, "err", err)
	}
	return prOpened, noPR
}

// prExpected reports whether a successful worktree task should end in a PR.
func (s *Scheduler) prExpected() bool {
	return s.VCS != nil && s.Opts.AutoPR
}

// openPR opens a pull request for a task whose work is pushed, and records
// the URL so the CI poller starts watching it. A CI retry passes the PR it
// pushed to as existing: that PR is watched again instead of opening another,
// which the forge refuses for a branch that already has one (#276).
//
// Returns "" when a PR is now open and being watched, otherwise why not.
// Every failure counts: without a recorded PR URL nothing would ever poll
// it, so a task that thinks it is waiting on CI would wait forever.
func (s *Scheduler) openPR(ctx context.Context, entry *InFlight, existing string) string {
	task := entry.Task

	spec := task.SpecRef
	if spec == "" {
		spec = "n/a"
	}
	title := task.Title
	if title == "" {
		title = task.ID
	}
	body := strings.TrimSpace(fmt.Sprintf("Task: `%s`\nSpec: %s\n\n%s", task.ID, spec, task.Reason))

	prURL, created := existing, existing == ""
	var err error
	if created {
		prURL, err = s.VCS.CreatePR(s.Worktree.BranchName(task.ID), s.Opts.BaseBranch, title, body)
	}
	if err != nil {
		s.logger().Error("opening the PR failed", "task", task.ID, "err", err)
		return "the agent exited cleanly but opening the PR failed: " + err.Error()
	}
	if prURL == "" {
		// The CLI ran and produced no PR — Python's None, typically a branch
		// with no commits: the agent changed nothing.
		s.logger().Warn("no PR was opened", "task", task.ID)
		return "the agent exited cleanly but no PR was opened (no changes to propose?)"
	}

	if s.CIRecorder == nil {
		s.logger().Error("a PR was opened with nowhere to record it; CI will not be polled",
			"task", task.ID, "pr", prURL)
		return "a PR was opened with nowhere to record it: " + prURL
	}
	// SetTaskPR also moves ci_status to pending, which is what puts the task
	// into the poller's filter. Without it the PR is open and nobody watches.
	if err := s.CIRecorder.SetTaskPR(ctx, task.ID, prURL); err != nil {
		s.logger().Error("recording the PR failed; CI will not be polled",
			"task", task.ID, "pr", prURL, "err", err)
		return "a PR was opened but recording it failed (" + prURL + "): " + err.Error()
	}
	if !created {
		s.logger().Info("the CI retry pushed to its PR; waiting on CI again", "task", task.ID, "pr", prURL)
		return ""
	}
	if err := s.Backend.AppendEngineEvent(ctx, s.Opts.RunID, EventPRCreated, task.ID,
		string(entry.Route.Backend), map[string]any{"pr_url": prURL}); err != nil {
		s.logger().Error("recording the pr_created event failed", "task", task.ID, "err", err)
	}
	return ""
}

// markDone finishes a successful task.
func (s *Scheduler) markDone(ctx context.Context, entry *InFlight) error {
	if err := s.Queue.MarkDone(entry.Task.ID); err != nil {
		s.logger().Error("mark done failed", "task", entry.Task.ID, "err", err)
	}
	if err := s.transition(ctx, entry.Task.ID, model.StatusDone, "dispatch succeeded"); err != nil {
		s.logger().Error("recording done failed", "task", entry.Task.ID, "err", err)
	}
	return nil
}

// handleFailure either queues a retry or blocks the task.
func (s *Scheduler) handleFailure(ctx context.Context, entry *InFlight, out Outcome) error {
	reason := truncateReason(out.Result.ErrorMessage)

	// The agent may have finished the work and called task-finish.sh while
	// the CLI wrapper reported a failure anyway — a skills-shortening
	// warning, a step_finish buffering race. The state backend is asked
	// rather than believed: if it says the task is done, the sub-agent wins.
	//
	// Python reads tasks.json here, which no longer works: since v0.11 made
	// SQLite the default, task-finish.sh writes the database and leaves
	// tasks.json alone, so the guard is dead on every project scaffolded
	// since. Reading the backend is the port of the intent; see
	// docs/brainstorm/go-migration-notes.md.
	if s.taskStatusIs(ctx, entry.Task.ID, model.StatusDone) {
		s.logger().Info("the sub-agent finished despite the wrapper's failure; respecting it",
			"task", entry.Task.ID, "detector_reason", reason)
		if err := s.Queue.MarkDone(entry.Task.ID); err != nil {
			s.logger().Error("mark done failed", "task", entry.Task.ID, "err", err)
		}
		return nil
	}

	decision := DecideRetry(RetryInput{
		Task:     entry.Task,
		Route:    entry.Route,
		Attempt:  entry.Attempt,
		Failure:  out.Failure,
		Reason:   reason,
		SpentUSD: s.spentUSD[entry.Task.ID],
		Routes:   s.Routes,
		Cfg:      s.Opts.Cfg,
	})

	if !decision.Retry {
		return s.blockTask(ctx, entry, reason, out)
	}
	return s.queueRetry(ctx, entry, decision, out)
}

// queueRetry puts a task back in line with its backoff.
func (s *Scheduler) queueRetry(ctx context.Context, entry *InFlight, d RetryDecision, out Outcome) error {
	// The escalate event goes BEFORE the retry event, so a timeline reader
	// sees the promotion that explains the new route.
	if d.Escalated && s.Backend != nil {
		if err := s.Backend.AppendEngineEvent(ctx, s.Opts.RunID, EventEscalate, entry.Task.ID,
			string(d.Route.Backend), map[string]any{
				"attempt":        d.Attempt,
				"from_route":     entry.Task.Model,
				"to_route":       d.ToRouteKey,
				"from_cli_model": entry.Route.CLIModel,
				"to_cli_model":   d.Route.CLIModel,
				"failure_class":  string(out.Failure),
				"reason":         truncateReason(out.Result.ErrorMessage),
			}); err != nil {
			s.logger().Error("recording the escalation failed", "task", entry.Task.ID, "err", err)
		}
	}

	if s.Backend != nil {
		if err := s.Backend.AppendEngineEvent(ctx, s.Opts.RunID, EventRetry, entry.Task.ID,
			string(d.Route.Backend), map[string]any{
				"attempt":       d.Attempt,
				"reason":        truncateReason(d.Reason),
				"cli_model":     d.Route.CLIModel,
				"failure_class": string(out.Failure),
			}); err != nil {
			s.logger().Error("recording the retry failed", "task", entry.Task.ID, "err", err)
		}
	}

	// Back to todo so the ready set picks it up again. The retry queue is
	// what actually re-dispatches it, but the status has to move or Ready
	// would never consider it.
	if err := s.Queue.MarkTodo(entry.Task.ID); err != nil {
		s.logger().Error("resetting to todo failed", "task", entry.Task.ID, "err", err)
	}

	s.retryQueue = append(s.retryQueue, RetryItem{
		Task:       entry.Task,
		Route:      d.Route,
		Attempt:    d.Attempt,
		EarliestAt: s.now().Add(d.Backoff),
	})
	return nil
}

// blockTask reaches the terminal verdict.
func (s *Scheduler) blockTask(ctx context.Context, entry *InFlight, reason string, out Outcome) error {
	eventType := EventFail
	if out.TimedOut {
		eventType = EventTimeout
	}

	if out.Failure == providers.FailureIDSpoof && s.Backend != nil {
		// Emitted alongside the failure event, not instead of it, as in
		// Python: the timeline needs both "this failed" and "this is why we
		// do not trust the result".
		if err := s.Backend.AppendEngineEvent(ctx, s.Opts.RunID, EventIDSpoof, entry.Task.ID,
			string(entry.Route.Backend), map[string]any{"reason": reason}); err != nil {
			s.logger().Error("recording the id spoof failed", "task", entry.Task.ID, "err", err)
		}
	}

	if s.Backend != nil {
		if err := s.Backend.AppendEngineEvent(ctx, s.Opts.RunID, eventType, entry.Task.ID,
			string(entry.Route.Backend), map[string]any{
				"reason":        reason,
				"exit_code":     out.Result.ExitCode,
				"attempt":       entry.Attempt,
				"failure_class": string(out.Failure),
			}); err != nil {
			s.logger().Error("recording the failure failed", "task", entry.Task.ID, "err", err)
		}
	}

	if err := s.Queue.MarkBlocked(entry.Task.ID); err != nil {
		s.logger().Error("mark blocked failed", "task", entry.Task.ID, "err", err)
	}
	// Through todo when needed: the agent may already have reported the task
	// done, and a PR that could not be opened still blocks it (#276).
	if s.Backend != nil {
		if err := transitionThroughTodo(ctx, s.Backend, entry.Task.ID, model.StatusBlocked, reason); err != nil {
			s.logger().Error("recording blocked failed", "task", entry.Task.ID, "err", err)
		}
	}
	// Last, and after the state is already written: the webhook is a nudge,
	// and a slow one must not sit between a task being blocked and the
	// database knowing it.
	if s.Notify != nil {
		s.Notify.Blocked(ctx, entry.Task.ID, reason)
	}
	return nil
}

// taskStatusIs asks the state backend whether the sub-agent left the task in
// the given status itself (done via task-finish.sh, blocked via orch_block).
// A backend that cannot answer reads as "no": the wrapper's verdict stands,
// which is the conservative side.
func (s *Scheduler) taskStatusIs(ctx context.Context, taskID string, want model.Status) bool {
	if s.Backend == nil {
		return false
	}
	status, err := s.Backend.TaskStatus(ctx, taskID)
	if err != nil {
		s.logger().Warn("could not read the task's status; trusting the wrapper",
			"task", taskID, "err", err)
		return false
	}
	return status == want
}

func (s *Scheduler) transition(ctx context.Context, taskID string, to model.Status, note string) error {
	if s.Backend == nil {
		return nil
	}
	return s.Backend.Transition(ctx, taskID, to, note)
}

// DrainWait blocks until every in-flight child has been reaped, or until the
// deadline passes. Returns how many it reaped.
//
// Called when the run is winding down — the queue is empty, the operator quit
// at the gate, or a signal arrived. The worktree's RemoveAll belongs AFTER
// this returns, never inside a signal handler, because cleanup racing a
// still-draining task would delete a worktree a backend process is still
// writing into.
func (s *Scheduler) DrainWait(ctx context.Context, timeout time.Duration) (int, error) {
	reaped := 0
	deadline := s.now().Add(timeout)

	for len(s.inFlight) > 0 {
		remaining := deadline.Sub(s.now())
		if remaining <= 0 {
			s.logger().Warn("drain timed out with children still running",
				"in_flight", len(s.inFlight))
			return reaped, nil
		}

		timer := time.NewTimer(remaining)
		select {
		case c := <-s.done:
			timer.Stop()
			if err := s.finish(ctx, c); err != nil {
				return reaped, err
			}
			reaped++
		case <-timer.C:
			s.logger().Warn("drain timed out with children still running",
				"in_flight", len(s.inFlight))
			return reaped, nil
		case <-ctx.Done():
			timer.Stop()
			return reaped, ctx.Err()
		}
	}
	return reaped, nil
}

// CleanupWorktrees runs RemoveAll. Call it after DrainWait, never from a
// signal handler.
func (s *Scheduler) CleanupWorktrees(ctx context.Context) {
	if s.Worktree == nil {
		return
	}
	if err := s.Worktree.RemoveAll(ctx); err != nil {
		s.logger().Error("worktree: cleanup failed", "err", err)
	}
}

// RetryQueue is what is waiting to be re-dispatched, for tests and for the
// status surface.
func (s *Scheduler) RetryQueue() []RetryItem { return s.retryQueue }

// SpentUSD is what a task has cost across every attempt.
func (s *Scheduler) SpentUSD(taskID string) float64 { return s.spentUSD[taskID] }
