package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/hectorcanaimero/orch/internal/budget"
	"github.com/hectorcanaimero/orch/internal/config"
	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/prompt"
	"github.com/hectorcanaimero/orch/internal/providers"
	"github.com/hectorcanaimero/orch/internal/vcs"
)

// Mode is how much the operator is asked. Port of orch.py's `--mode`.
type Mode string

const (
	// ModeAuto dispatches everything that is ready.
	ModeAuto Mode = "auto"
	// ModeSemi offers every critical task to the operator first (FR-C-2).
	ModeSemi Mode = "semi"
)

// BudgetGate is the slice of budget.Gate the scheduler uses. An interface so
// a test can drive the fail-open path without building a spend history, and
// so the scheduler does not care how the window is computed.
type BudgetGate interface {
	CanDispatch(ctx context.Context, provider string) (budget.Decision, error)
}

// InFlight is one dispatched task under supervision, and everything the
// reaper needs to finish it.
type InFlight struct {
	Task    model.Task
	Route   model.RouteEntry
	Attempt int
	Spawned *Spawned
	// Lock is the per-task flock, held for as long as the child runs. Nil
	// when task locks are off. The reaper releases it; see TaskLock.
	Lock *TaskLock
	// StartedAt is when the child was forked, for the spend row's duration.
	StartedAt time.Time
	// Dispatch is what was asked of the provider, kept so the reaper can
	// record the outcome against the same paths and route.
	Dispatch Dispatch
}

// SchedulerOptions configures one run.
type SchedulerOptions struct {
	// Mode is auto or semi.
	Mode Mode
	// GlobalMax and PerProvider are config's concurrency block.
	GlobalMax   int
	PerProvider map[string]int
	// TimeoutMultiplier is config's default_timeout_multiplier.
	TimeoutMultiplier float64
	// Cfg is the loaded config, for the retry policy and the budget cap.
	Cfg config.Config
	// BudgetUSD, when non-nil, is passed to providers that take a
	// per-dispatch cap.
	BudgetUSD *float64
	// UseTaskLocks turns on the per-task flock (Python's `--task-locks`).
	UseTaskLocks bool
	// Only narrows the candidate set to task ids matching this glob.
	Only string
	// MaxTasks caps how many dispatches one run performs. Zero means no cap.
	MaxTasks int
	// StateDir is where logs and task locks live.
	StateDir string
	// Cwd is the project root the children run in.
	Cwd string
	// RunID ties every event and dispatch row to this run.
	RunID string

	// WorktreeMode gives each task its own git worktree and branch (F-2).
	WorktreeMode bool
	// BaseBranch is what a worktree branches from and what a PR targets.
	BaseBranch string
	// AutoPR opens a pull request after a successful push (F-4).
	AutoPR bool
}

// Scheduler decides what to dispatch and starts it. Port of orch.py's
// `_refill` and `_spawn_one`.
//
// It does not reap: Refill starts children and records them in InFlight, and
// the reaper (G3.2) finishes them, releases their slots and their locks, and
// applies the retry policy. Splitting the two keeps the gating order — which
// is the part with the sharp edges — testable on its own.
type Scheduler struct {
	Queue  *TaskQueue
	Routes map[string]model.RouteEntry
	Sems   *Sems
	Opts   SchedulerOptions

	// Gate is consulted in semi mode. Nil means every task is dispatched,
	// which is also what auto mode does.
	Gate Gate
	// Budget is consulted before the semaphores. Nil disables the check.
	Budget BudgetGate
	// Notify announces blocked tasks on the operator's webhooks. The zero
	// value is a working no-op, so this is never nil-checked at the call
	// sites — which is the point: a side channel must not become a branch.
	Notify Notifier
	// Backend records dispatches, outcomes and events. Nil skips recording,
	// which is how the concurrency tests run without a database.
	Backend RecordBackend
	// Worktree isolates each task in its own git worktree when the project
	// asks for it. Nil turns the whole thing off, and the task runs in the
	// project root as it did before F-2.
	Worktree WorktreeManager
	// VCS opens the pull request after a successful push. Nil, or AutoPR
	// off, means no PR is opened and the task finishes normally.
	VCS vcs.Provider
	// CIRecorder writes the PR URL, which is also what puts the task into
	// the CI poller's filter. Without it a PR would be opened and never
	// watched, so openPR refuses to leave one in that state.
	CIRecorder PRRecorder
	// Comments reads a finished dependency's notes for the prompt's
	// "Completed dependencies (context)" block. Nil falls back to whatever
	// tasks.json carried, which since F-12 is nothing — see completedDeps.
	Comments CommentReader
	// Log receives the operator-facing lines. Nil uses slog's default.
	Log *slog.Logger

	// inFlight is keyed by pid, as Python's is.
	inFlight map[int]*InFlight
	// deferred holds tasks the operator deferred; they are not offered again
	// this run.
	deferred map[string]bool
	// deferReasons explains why a ready task was not dispatched, so
	// `orch status` can tell blocked-by-budget from not-ready-yet. Rewritten
	// every tick, so it is always current.
	deferReasons map[string]string
	// dispatched counts successful spawns against MaxTasks.
	dispatched int
	// draining is set when the operator answers "quit": no new dispatches,
	// but the children already running are left to finish.
	draining bool

	// done carries finished children from their supervising goroutines to
	// Reap. Buffered to the global ceiling so a supervisor never blocks on a
	// reaper that is mid-tick.
	done chan completion
	// retryQueue holds tasks waiting out a backoff. Drained BEFORE the ready
	// set, so a retry never waits behind a newly-ready peer (FR-D-4).
	retryQueue []RetryItem
	// spentUSD accumulates each task's cost across attempts, for the
	// escalation budget check (FR-D-8).
	spentUSD map[string]float64
	// now is the clock, injectable so the backoff tests do not wait.
	now func() time.Time
}

// Notifier is the side channel the engine announces bad news on. An
// interface so the engine does not depend on internal/notify's transport, and
// so a test can assert that a block was announced without standing up an HTTP
// server.
//
// No method returns an error, and that is the contract, not an oversight: a
// broken webhook must never be able to reach the dispatch loop.
type Notifier interface {
	Blocked(ctx context.Context, taskID, reason string)
	CIBlocked(ctx context.Context, taskID, prURL string, attempts int)
}

// PRRecorder writes a task's pull request URL. Separate from RecordBackend
// because only the auto-PR path needs it, and state.Backend's SetTaskPR moves
// ci_status to pending in the same write — the thing that makes the CI poller
// notice the task at all.
type PRRecorder interface {
	SetTaskPR(ctx context.Context, taskID, prURL string) error
}

// RecordBackend is the slice of state.Backend the engine writes through.
//
// A narrow interface rather than state.Backend itself, so the concurrency and
// retry tests run without a database — they are about semaphores and policy,
// not about SQL — and so every write the engine performs is listed in one
// place.
type RecordBackend interface {
	// RecordDispatchAndEvent writes the in-flight row and the dispatch event.
	RecordDispatchAndEvent(ctx context.Context, runID string, d Dispatch, sp *Spawned, attempt int) error
	// RecordFinish writes the spend row and the outcome event, and clears
	// the in-flight row.
	RecordFinish(ctx context.Context, runID string, d Dispatch, o Outcome, attempt int) error
	// AppendEngineEvent writes one event of the engine's own (retry,
	// escalate, id_spoof_detected).
	AppendEngineEvent(ctx context.Context, runID, eventType, taskID, backend string, extra map[string]any) error
	// TaskStatus reads a task's current status back, for the guard that
	// respects a sub-agent which finished despite a wrapper failure.
	TaskStatus(ctx context.Context, taskID string) (model.Status, error)
	// Transition moves a task, recording the note.
	Transition(ctx context.Context, taskID string, to model.Status, note string) error
}

// CommentReader reads a finished task's comment trail.
//
// Separate from RecordBackend rather than a method on it: the concurrency
// tests build a RecordBackend double and none of them is about prompts, and
// this is read at one call site for one block of one file.
type CommentReader interface {
	TaskComments(ctx context.Context, taskID string) ([]json.RawMessage, error)
}

// NewScheduler wires a scheduler over a queue and a route table.
func NewScheduler(q *TaskQueue, routes map[string]model.RouteEntry, opts SchedulerOptions) *Scheduler {
	capacity := opts.GlobalMax
	if capacity < 1 {
		capacity = 1
	}
	return &Scheduler{
		Queue:        q,
		Routes:       routes,
		Sems:         NewSems(opts.GlobalMax, opts.PerProvider),
		Opts:         opts,
		inFlight:     make(map[int]*InFlight),
		deferred:     make(map[string]bool),
		deferReasons: make(map[string]string),
		done:         make(chan completion, capacity),
		spentUSD:     make(map[string]float64),
		now:          time.Now,
	}
}

// InFlight returns the live dispatches, keyed by pid. The map is the
// scheduler's own; the reaper mutates it through Reap, not directly.
func (s *Scheduler) InFlight() map[int]*InFlight { return s.inFlight }

// Draining reports whether the operator asked to stop.
func (s *Scheduler) Draining() bool { return s.draining }

// DeferReasons explains, per task id, why a ready task was skipped.
func (s *Scheduler) DeferReasons() map[string]string { return s.deferReasons }

// Dispatched is how many children this run has started.
func (s *Scheduler) Dispatched() int { return s.dispatched }

// Refill dispatches as many ready tasks as capacity allows, and returns how
// many it started this tick.
//
// The ready set is walked ONCE per tick: a task that cannot get a slot is
// skipped rather than waited on, so a project with 300 ready tasks does not
// turn one tick into a stall. The next tick retries it.
func (s *Scheduler) Refill(ctx context.Context) (int, error) {
	if s.draining {
		return 0, nil
	}

	started := 0

	// (1) Retries first. A task marked for retry must not wait behind a
	// newly-ready peer (FR-D-4), and its backoff is checked passively: an
	// item whose time has not come is left in the queue rather than slept
	// on, because the loop already ticks.
	n, err := s.drainRetryQueue(ctx)
	started += n
	if err != nil {
		return started, err
	}

	// (2) Then the ready set, minus anything the retry queue still owns.
	//
	// That exclusion is a fix, not a port. A retry resets the task to todo so
	// the queue will consider it again, which in Python also makes it
	// immediately eligible in this very pass — so the backoff that was just
	// computed is bypassed, and a rate-limited provider gets hammered again
	// at once instead of waiting out its window. Worse, the RetryItem stays
	// queued, so the same task can be dispatched a second time when its
	// backoff finally expires. Bug 15; see the notes.
	for _, task := range s.Queue.Ready(ReadyOpts{
		InFlight: s.inFlightIDs(),
		Only:     s.Opts.Only,
		Deferred: s.deferred,
		Waiting:  s.retryQueueIDs(),
	}) {
		if s.Opts.MaxTasks > 0 && s.dispatched >= s.Opts.MaxTasks {
			break
		}
		if s.draining {
			break
		}

		route, ok := s.Routes[task.Model]
		if !ok {
			// Validated at startup (FR-D-6); this is the safety net, and it
			// skips rather than blocks so one bad row cannot end a run.
			s.logger().Error("route missing at dispatch time",
				"task", task.ID, "model", task.Model)
			continue
		}

		if s.Opts.Mode == ModeSemi && s.Gate != nil && IsCritical(task, s.Routes) {
			stop, dispatch := s.askOperator(task)
			if stop {
				break
			}
			if !dispatch {
				continue
			}
		}

		ok, err := s.spawnOne(ctx, task, route, 1)
		if err != nil {
			// Reserved for failures that are about the run rather than one
			// task — an unwritable state directory, say. Anything a single
			// task can be blamed for is handled inside spawnOne, so that one
			// bad row cannot end a run.
			return started, err
		}
		if ok {
			started++
			s.dispatched++
		}
	}
	return started, nil
}

// drainRetryQueue dispatches everything whose backoff has expired, and
// returns how many it started. Items still waiting stay queued.
func (s *Scheduler) drainRetryQueue(ctx context.Context) (int, error) {
	if len(s.retryQueue) == 0 {
		return 0, nil
	}

	started := 0
	now := s.now()
	remaining := s.retryQueue[:0]

	for _, item := range s.retryQueue {
		switch {
		case s.draining,
			s.Opts.MaxTasks > 0 && s.dispatched >= s.Opts.MaxTasks,
			item.EarliestAt.After(now):
			remaining = append(remaining, item)
			continue
		}

		ok, err := s.spawnOne(ctx, item.Task, item.Route, item.Attempt)
		if err != nil {
			remaining = append(remaining, item)
			s.retryQueue = remaining
			return started, err
		}
		if ok {
			started++
			s.dispatched++
			continue
		}
		// Not dispatched. Requeue only if this was a capacity miss — if
		// spawnOne blocked the task instead, its status has moved and
		// requeuing would re-dispatch something already given up on.
		if status, known := s.Queue.Status(item.Task.ID); known && status == model.StatusTodo {
			remaining = append(remaining, item)
		}
	}

	s.retryQueue = remaining
	return started, nil
}

// askOperator runs the semi-mode gate. It returns (stop, dispatch): stop ends
// the tick and starts draining, dispatch says whether to go ahead.
//
// Blocking and skipping are the caller's side effects in Python too — the
// gate itself only decodes a keystroke — but the bookkeeping differs by
// answer, so it lives here rather than in the Refill loop's body.
func (s *Scheduler) askOperator(task model.Task) (stop, dispatch bool) {
	switch s.Gate.Ask(task, GateReason(task, s.Routes)) {
	case DecisionDispatch:
		return false, true
	case DecisionDefer:
		// Deferred for this run only: not blocked, not dispatched, and
		// offered again the next time orch runs.
		s.deferred[task.ID] = true
		s.deferReasons[task.ID] = "deferred-by-operator"
		return false, false
	case DecisionSkip:
		// Skipping is permanent: the task is blocked, so its dependents stay
		// unready (AS-02).
		if err := s.Queue.MarkBlocked(task.ID); err != nil {
			s.logger().Error("skip: mark blocked failed", "task", task.ID, "err", err)
		}
		s.deferReasons[task.ID] = "skipped-by-operator"
		return false, false
	case DecisionQuit:
		s.draining = true
		return true, false
	default:
		// An implementation that answers with something else gets the
		// conservative reading: do not dispatch, do not stop the run.
		return false, false
	}
}

// spawnOne applies the gating order and starts one child. Port of
// `_spawn_one`, whose ordering is load-bearing and reproduced exactly.
func (s *Scheduler) spawnOne(ctx context.Context, task model.Task, route model.RouteEntry, attempt int) (bool, error) {
	backend := string(route.Backend)

	// (1) Per-task lock, first: a task another orch owns should cost nothing
	// else to discover.
	var lock *TaskLock
	if s.Opts.UseTaskLocks {
		var err error
		lock, err = TryAcquireTaskLock(s.Opts.StateDir, task.ID)
		if err != nil {
			return false, fmt.Errorf("task lock for %s: %w", task.ID, err)
		}
		if lock == nil {
			// Another orch owns it. Silently skip, as Python does.
			return false, nil
		}
	}

	// (2) Budget gate, BEFORE the semaphores, so a capped provider does not
	// churn a slot it cannot use. Skipping here is equivalent to a full
	// semaphore: the next tick re-checks.
	if s.Budget != nil {
		if !s.checkBudget(ctx, task, backend) {
			lock.Release()
			return false, nil
		}
	}

	// (3) Semaphores: provider first, then global, releasing the provider's
	// if the global refuses. See Sems.TryAcquire on why that order matters.
	if !s.Sems.TryAcquire(backend) {
		lock.Release()
		if !s.Sems.Knows(backend) {
			// A route naming a provider with no configured cap would
			// otherwise be skipped every tick, forever, in silence.
			s.noteSkip(task.ID, "no-concurrency-cap:"+backend,
				"no concurrency cap configured for this backend; the task cannot dispatch")
		}
		return false, nil
	}

	// From here on, every failure path must give back both the slots and the
	// lock, or the run leaks capacity until it deadlocks with nothing
	// running.
	release := func() {
		s.Sems.Release(backend)
		lock.Release()
	}

	provider, err := providers.Get(route.Backend)
	if err != nil {
		release()
		// A backend this binary cannot drive is a property of the binary,
		// not a verdict on the task. Skip it with a reason the operator can
		// read, the same way a budget-capped provider is skipped, and let
		// the rest of the ready set through — a half-ported orch must still
		// make progress on the backends it does have. Blocking would write
		// "this task failed" into the project's database because our own
		// rewrite is unfinished, and the next release would have to undo it
		// by hand.
		s.noteSkip(task.ID, "backend-unavailable:"+backend,
			"cannot dispatch: no usable provider", "err", err)
		return false, nil
	}

	// The worktree is created before the prompt is rendered, because the
	// child runs IN it: the prompt's "Working dir:" line has to name the
	// place the agent will actually be. A failure here blocks the task —
	// dispatching into the project root instead would let one task's agent
	// edit another's files, which is the whole thing worktree mode exists to
	// prevent.
	workdir := s.Opts.Cwd
	if s.Worktree != nil && s.Opts.WorktreeMode {
		created, err := s.Worktree.Create(ctx, task.ID, s.baseBranch())
		if err != nil {
			release()
			s.logger().Error("creating the worktree failed", "task", task.ID, "err", err)
			s.blockAtDispatch(ctx, task, route, fmt.Sprintf("worktree create failed: %v", err))
			return false, nil
		}
		workdir = created
	}

	// Render the prompt and write it before forking. The provider pipes this
	// file to the child's stdin, so a dispatch without it is a CLI with no
	// instructions — Python renders it at the same point, for the same
	// reason.
	promptPath, warnings, err := prompt.Write(task, s.completedDeps(ctx, task), task.SpecRef, prompt.Options{
		RunID:       s.Opts.RunID,
		StateDir:    s.Opts.StateDir,
		ProjectRoot: workdir,
		SpecRoot:    s.Opts.Cfg.SpecRoot,
	})
	if err != nil {
		release()
		s.logger().Error("rendering the prompt failed", "task", task.ID, "err", err)
		s.blockAtDispatch(ctx, task, route, fmt.Sprintf("prompt render failed: %v", err))
		return false, nil
	}
	for _, w := range warnings {
		s.logger().Warn(w, "task", task.ID)
	}

	d := Dispatch{
		Provider: provider,
		Req: providers.Request{
			TaskID:    task.ID,
			Route:     route,
			Cwd:       workdir,
			SessionID: providers.NewSessionID(),
			BudgetUSD: s.Opts.BudgetUSD,
			Settings:  projectClaudeSettings(s.Opts.Cwd, workdir),
		},
		PromptPath: promptPath,
		LogPath:    LogPathFor(s.Opts.StateDir, task.ID),
		Cwd:        workdir,
		Timeout:    TimeoutFor(task.EstimateHours, s.Opts.TimeoutMultiplier),
	}

	sp, err := Spawn(ctx, SpawnRequest{
		Provider:   d.Provider,
		Req:        d.Req,
		PromptPath: d.PromptPath,
		LogPath:    d.LogPath,
		Cwd:        d.Cwd,
	})
	if err != nil {
		release()
		// A spawn failure is this task's problem, not the run's: block it
		// and carry on, which is what Python does.
		s.logger().Error("spawn failed", "task", task.ID, "err", err)
		s.blockAtDispatch(ctx, task, route, fmt.Sprintf("spawn failed: %v", err))
		return false, nil
	}

	if err := s.Queue.MarkInFlight(task.ID); err != nil {
		s.logger().Error("mark in-flight failed", "task", task.ID, "err", err)
	}
	// And in the database, which is what `orch status` and the dashboard
	// read. Python does this by shelling `scripts/task-start.sh`, which
	// shells straight back into `orch task-status <id> in-progress`; going
	// to the backend directly is the same write without the round trip, and
	// it is the source of truth since F-12.
	//
	// Without it a running agent shows as `todo` to everyone outside this
	// process, and a crashed run leaves no task-level trace to reconcile.
	if s.Backend != nil {
		if err := s.Backend.Transition(ctx, task.ID, model.StatusInProgress,
			"dispatched to "+string(route.Backend)+"/"+route.CLIModel); err != nil {
			s.logger().Error("recording in-progress failed", "task", task.ID, "err", err)
		}
	}

	s.inFlight[sp.PID] = &InFlight{
		Task:      task,
		Route:     route,
		Attempt:   attempt,
		Spawned:   sp,
		Lock:      lock,
		StartedAt: sp.StartedAt,
		Dispatch:  d,
	}
	delete(s.deferReasons, task.ID)

	// One supervisor per child, posting to the reaper when it finishes.
	go s.supervise(sp.PID, d, sp)

	if s.Backend != nil {
		if err := s.Backend.RecordDispatchAndEvent(ctx, s.Opts.RunID, d, sp, attempt); err != nil {
			// The child is already running. Losing its dispatch row costs
			// observability, not correctness, and killing a live agent to
			// keep the database tidy would be the worse trade.
			s.logger().Error("recording the dispatch failed; the child is running anyway",
				"task", task.ID, "pid", sp.PID, "err", err)
		}
	}

	return true, nil
}

// blockAtDispatch gives up on a task that could not be started, in the live
// view AND in the database.
//
// Both, because they are read by different people: the queue decides what
// this run does next, and the row is what `orch status` shows afterwards.
// Marking only the queue — which is what an earlier version of this did —
// leaves a run that gave up on a task while the database still calls it todo.
func (s *Scheduler) blockAtDispatch(ctx context.Context, task model.Task, route model.RouteEntry, reason string) {
	reason = truncateReason(reason)
	if err := s.Queue.MarkBlocked(task.ID); err != nil {
		s.logger().Error("mark blocked failed", "task", task.ID, "err", err)
	}
	if s.Backend == nil {
		return
	}
	if err := s.Backend.AppendEngineEvent(ctx, s.Opts.RunID, EventBlock, task.ID,
		string(route.Backend), map[string]any{"reason": reason}); err != nil {
		s.logger().Error("recording the block failed", "task", task.ID, "err", err)
	}
	if err := s.Backend.Transition(ctx, task.ID, model.StatusBlocked, reason); err != nil {
		s.logger().Error("recording blocked failed", "task", task.ID, "err", err)
	}
}

// completedDeps are the task's dependencies that are done, for the prompt's
// "what came before" section.
//
// The comment trail is read from the database here, not taken from the queue's
// copy of tasks.json. Two reasons, and the second is the one that decides it:
//
//   - Since F-12 the trail lives in `tasks_runtime.comments_json`. The queue is
//     built from tasks.json, whose `comments` array is whatever was in the file
//     — empty for every project orch has actually run. That is the other half
//     of bug 24.
//   - The dependency most likely finished during THIS run, minutes ago. A
//     snapshot taken when the queue was built would be empty for exactly the
//     summary that matters most.
//
// A read that fails is logged and the dependency still goes in the block with
// no comment: a prompt without one dependency's sentence is worse than the
// same prompt, and far better than no dispatch.
func (s *Scheduler) completedDeps(ctx context.Context, task model.Task) []model.Task {
	var out []model.Task
	for _, id := range task.Dependencies {
		dep, ok := s.Queue.Task(id)
		if !ok {
			continue
		}
		if st, _ := s.Queue.Status(id); st != model.StatusDone {
			continue
		}
		if s.Comments != nil {
			comments, err := s.Comments.TaskComments(ctx, id)
			if err != nil {
				s.logger().Warn("reading a dependency's notes for the prompt",
					"task", task.ID, "dep", id, "err", err)
			} else {
				dep.Comments = comments
			}
		}
		out = append(out, dep)
	}
	return out
}

// checkBudget consults the guardrail, and fails OPEN.
//
// A gate that cannot read the spend window must not stop a run: Python
// swallows the database error inside the gate and returns zero rows, which
// reads as zero spend and therefore as "go ahead" — the shape of bug 5. Go's
// gate returns the error instead, and the decision to carry on lives here,
// where it can be logged next to the dispatch it is affecting rather than
// disappearing inside the guardrail.
func (s *Scheduler) checkBudget(ctx context.Context, task model.Task, backend string) bool {
	decision, err := s.Budget.CanDispatch(ctx, backend)
	if err != nil {
		s.logger().Warn("budget check failed, dispatching anyway",
			"task", task.ID, "backend", backend, "err", err)
		return true
	}
	if decision.OK {
		// Clear any stale marker so `orch status` shows the fresh state.
		delete(s.deferReasons, task.ID)
		return true
	}

	s.deferReasons[task.ID] = "blocked-by-budget:" + backend
	s.logger().Info("deferred: over budget",
		"task", task.ID, "backend", backend,
		"reason", decision.Reason, "resets_at", resetAtText(decision.ResetAt))
	return false
}

// noteSkip records why a ready task was not dispatched and logs it, at most
// once per (task, reason) — the refill loop revisits the same task every
// tick, and a line per tick would bury the log it belongs in.
func (s *Scheduler) noteSkip(taskID, reason, msg string, args ...any) {
	if s.deferReasons[taskID] == reason {
		return
	}
	s.deferReasons[taskID] = reason
	s.logger().Warn(msg, append([]any{"task", taskID, "reason", reason}, args...)...)
}

func resetAtText(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02T15:04:05Z")
}

// retryQueueIDs are the tasks the retry queue owns until their backoff
// expires. The ready set must not race it for them.
func (s *Scheduler) retryQueueIDs() map[string]bool {
	if len(s.retryQueue) == 0 {
		return nil
	}
	ids := make(map[string]bool, len(s.retryQueue))
	for _, item := range s.retryQueue {
		ids[item.Task.ID] = true
	}
	return ids
}

func (s *Scheduler) inFlightIDs() map[string]bool {
	ids := make(map[string]bool, len(s.inFlight))
	for _, e := range s.inFlight {
		ids[e.Task.ID] = true
	}
	return ids
}

// baseBranch is what a worktree branches from and what a PR targets.
// Python's default is "main".
func (s *Scheduler) baseBranch() string {
	if s.Opts.BaseBranch == "" {
		return "main"
	}
	return s.Opts.BaseBranch
}

func (s *Scheduler) logger() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// PromptPathFor is where a task's rendered prompt goes:
// `<stateDir>/prompts/<id>.txt`, matching Python's layout.
func PromptPathFor(stateDir, taskID string) string {
	return filepath.Join(stateDir, "prompts", taskID+".txt")
}
