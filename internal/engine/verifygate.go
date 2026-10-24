package engine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/hectorcanaimero/orch/internal/model"
)

// EventVerifyFailed records a local verify that failed an attempt: the
// reason the agent reads, and the review's findings uncut (#463).
const EventVerifyFailed = "verify_failed"

// EventVerifyPassed records a local verify that passed an attempt: the
// commands it ran and the reviewer that read the diff (#463).
const EventVerifyPassed = "verify_passed"

// EventE2EReport records the E2E results of one verified attempt: the counts,
// and the report the dashboard opens.
const EventE2EReport = "e2e_report"

// defaultVerifyMaxAttempts is verify.max_attempts when it is unset or below 1.
const defaultVerifyMaxAttempts = 2

// Verifier checks an agent's work in its worktree after a clean exit and
// before the push (verify.enabled). It returns what failed, written for the
// agent's next attempt, or "" when the work passes. internal/verify is the
// implementation; it cannot live here because it reviews through internal/ci,
// which imports this package.
type Verifier interface {
	Verify(ctx context.Context, req VerifyRequest) VerifyResult
}

// VerifyResult is the verdict on one attempt's work.
type VerifyResult struct {
	// Failure is what failed, written for the agent's next attempt; empty
	// when the work passes.
	Failure string
	// E2E summarises the E2E run, when verify.e2e_report collected one.
	E2E *E2ESummary
	// Commands are the checks that ran, in order, with how long each took.
	Commands []VerifyCommand
	// Reviewer and ReviewModel name who reviewed the diff; empty when no
	// review completed.
	Reviewer, ReviewModel string
	// Findings are the review's, blocking and minor.
	Findings []VerifyFinding
}

// VerifyCommand is one check the verify ran.
type VerifyCommand struct {
	Command   string  `json:"command"`
	DurationS float64 `json:"duration_s"`
}

// VerifyFinding is one review finding, as internal/ci's Finding has it.
type VerifyFinding struct {
	Severity string `json:"severity"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	Title    string `json:"title"`
	Detail   string `json:"detail,omitempty"`
}

// E2ESummary is an E2E run in counts, and where its full report is.
type E2ESummary struct {
	Passed, Failed, Flaky, Skipped int
	// Report is the report file, under ArtifactsDir.
	Report string
}

// VerifyRequest is one clean exit's work to verify.
type VerifyRequest struct {
	TaskID string
	// Writer is the backend that did the work; the review picks another.
	Writer model.Backend
	// Dir is the task's worktree, with the work committed.
	Dir string
	// Env is the agent's own environment: the task's ORCH_TASK_SLOT and
	// COMPOSE_PROJECT_NAME, so the checks and the app run as the task's.
	Env []string
	// ArtifactsDir is where this attempt's E2E report and the files its
	// tests left are kept, under the state directory.
	ArtifactsDir string
}

// verifies reports whether a clean exit's work is checked before the push.
func (s *Scheduler) verifies() bool {
	return s.Verifier != nil && s.Worktree != nil && s.Opts.WorktreeMode
}

// verifyOutcome runs the local verify on a clean exit, from the dispatch's
// supervising goroutine: tests and a review take minutes, and the run loop
// must not wait on them. The work is committed first, so the review reads it
// as the diff the push would publish.
func (s *Scheduler) verifyOutcome(d Dispatch, attempt int, out *Outcome) {
	if !out.Result.Success || !s.verifies() {
		return
	}
	ctx := context.Background()
	s.commitWork(ctx, d.Req.TaskID, d.Cwd)
	res := s.Verifier.Verify(ctx, VerifyRequest{
		TaskID: d.Req.TaskID, Writer: d.Req.Route.Backend, Dir: d.Cwd, Env: d.Env,
		ArtifactsDir: E2EArtifactsDir(s.Opts.StateDir, s.Opts.RunID, d.Req.TaskID, attempt),
	})
	out.VerifyFailure, out.E2E, out.Verify = res.Failure, res.E2E, &res
}

// recordVerify records a verified attempt's verdict: verify_passed, or
// verify_failed with the reason and the findings, sent back or not.
func (s *Scheduler) recordVerify(ctx context.Context, entry *InFlight, res *VerifyResult, sentBack bool) {
	if res == nil || s.Backend == nil {
		return
	}
	id := entry.Task.ID
	event, extra := EventVerifyPassed, map[string]any{
		"attempt": s.verifyAttempts[id] + 1, "commands": res.Commands,
		"reviewer": res.Reviewer, "review_model": res.ReviewModel,
	}
	if res.Failure != "" {
		event, extra = EventVerifyFailed, map[string]any{
			"attempt": s.verifyAttempts[id] + 1, "reason": truncateReason(res.Failure), "sent_back": sentBack,
		}
	}
	if len(res.Findings) > 0 {
		extra["findings"] = res.Findings
	}
	if err := s.Backend.AppendEngineEvent(ctx, s.Opts.RunID, event, id, string(entry.Route.Backend), extra); err != nil {
		s.logger().Error("recording the verify result failed", "task", id, "err", err)
	}
}

// E2EArtifactsDir is where one attempt's E2E report and files are kept:
// <state>/e2e/<run>/<task>/attempt-<n>.
func E2EArtifactsDir(stateDir, runID, taskID string, attempt int) string {
	return filepath.Join(stateDir, "e2e", runID, taskID, fmt.Sprintf("attempt-%d", attempt))
}

// recordE2E records an attempt's E2E results, whatever its verdict.
func (s *Scheduler) recordE2E(ctx context.Context, entry *InFlight, e *E2ESummary) {
	if e == nil || s.Backend == nil {
		return
	}
	if err := s.Backend.AppendEngineEvent(ctx, s.Opts.RunID, EventE2EReport, entry.Task.ID,
		string(entry.Route.Backend), map[string]any{
			"attempt": entry.Attempt,
			"passed":  e.Passed, "failed": e.Failed, "flaky": e.Flaky, "skipped": e.Skipped,
			"report": e.Report,
		}); err != nil {
		s.logger().Error("recording the E2E report failed", "task", entry.Task.ID, "err", err)
	}
}

// commitWork commits what the agent left in its worktree, minus the feedback
// file it was given: that is for the agent, not the branch.
func (s *Scheduler) commitWork(ctx context.Context, taskID, workdir string) {
	if err := os.Remove(filepath.Join(workdir, CIFeedbackFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.logger().Error("worktree: removing the feedback file failed", "task", taskID, "err", err)
	}
	if err := s.Worktree.CommitPending(ctx, taskID); err != nil {
		s.logger().Error("worktree: commit failed", "task", taskID, "err", err)
	}
}

// verifyMax is how many times a task is sent back before it is blocked.
func (s *Scheduler) verifyMax() int {
	if s.Opts.Cfg.Verify.MaxAttempts < 1 {
		return defaultVerifyMaxAttempts
	}
	return s.Opts.Cfg.Verify.MaxAttempts
}

// sendBack returns a task whose work failed the local verify to its agent:
// the worktree is kept with the work committed in it, the failure is left in
// the feedback file the prompt points at, and the retry runs there at once.
// Nothing was pushed.
func (s *Scheduler) sendBack(ctx context.Context, entry *InFlight, out Outcome) error {
	id := entry.Task.ID
	s.verifyAttempts[id]++
	s.verifyRejected[id] = out.VerifyFailure
	if err := writeFeedback(entry.Dispatch.Cwd, "Local verify failed — please fix", out.VerifyFailure); err != nil {
		s.logger().Error("writing the verify feedback failed; the retry runs without it", "task", id, "err", err)
	}
	s.retryWorktree[id] = entry.Dispatch.Cwd
	s.logger().Warn("the local verify failed; sending the task back to its agent",
		"task", id, "attempt", s.verifyAttempts[id], "of", s.verifyMax())

	if s.Backend != nil {
		// The agent has most likely reported the task done already, and done
		// only reopens to todo.
		if err := transitionThroughTodo(ctx, s.Backend, id, model.StatusTodo, "local verify failed; sent back to its agent"); err != nil {
			s.logger().Error("reopening the task failed", "task", id, "err", err)
		}
	}
	if err := s.Queue.MarkTodo(id); err != nil {
		s.logger().Error("resetting to todo failed", "task", id, "err", err)
	}
	s.retryQueue = append(s.retryQueue, RetryItem{
		Task:       entry.Task,
		Route:      entry.Route,
		Attempt:    entry.Attempt + 1,
		EarliestAt: s.now(),
	})
	return nil
}

// verifyBlockReason is the block reason once the send-backs are used up.
func verifyBlockReason(failure string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(failure), "\n")
	return truncateReason(fmt.Sprintf("local verify still failing after the agent's retries: %s", first))
}

// writeFeedback leaves what failed in workdir for the agent's next attempt,
// under the file name the prompt's feedback block points at.
func writeFeedback(workdir, heading, body string) error {
	path := filepath.Join(workdir, CIFeedbackFile)
	if err := os.WriteFile(path, []byte("# "+heading+"\n\n"+body), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
