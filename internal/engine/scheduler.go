package engine

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/hectorcanaimero/orch/internal/budget"
	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/providers"
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
	// Backend records dispatches and events. Nil skips recording, which is
	// how the concurrency tests run without a database.
	Backend RecordBackend
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
}

// RecordBackend is the slice of state.Backend the scheduler writes through.
type RecordBackend interface {
	RecordDispatchAndEvent(ctx context.Context, runID string, d Dispatch, sp *Spawned, attempt int) error
}

// NewScheduler wires a scheduler over a queue and a route table.
func NewScheduler(q *TaskQueue, routes map[string]model.RouteEntry, opts SchedulerOptions) *Scheduler {
	return &Scheduler{
		Queue:        q,
		Routes:       routes,
		Sems:         NewSems(opts.GlobalMax, opts.PerProvider),
		Opts:         opts,
		inFlight:     make(map[int]*InFlight),
		deferred:     make(map[string]bool),
		deferReasons: make(map[string]string),
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
	for _, task := range s.Queue.Ready(ReadyOpts{
		InFlight: s.inFlightIDs(),
		Only:     s.Opts.Only,
		Deferred: s.deferred,
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

	d := Dispatch{
		Provider: provider,
		Req: providers.Request{
			TaskID:    task.ID,
			Route:     route,
			Cwd:       s.Opts.Cwd,
			SessionID: providers.NewSessionID(),
			BudgetUSD: s.Opts.BudgetUSD,
		},
		PromptPath: PromptPathFor(s.Opts.StateDir, task.ID),
		LogPath:    LogPathFor(s.Opts.StateDir, task.ID),
		Cwd:        s.Opts.Cwd,
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
		if markErr := s.Queue.MarkBlocked(task.ID); markErr != nil {
			s.logger().Error("spawn failure: mark blocked failed", "task", task.ID, "err", markErr)
		}
		return false, nil
	}

	if err := s.Queue.MarkInFlight(task.ID); err != nil {
		s.logger().Error("mark in-flight failed", "task", task.ID, "err", err)
	}

	s.inFlight[sp.PID] = &InFlight{
		Task:      task,
		Route:     route,
		Attempt:   attempt,
		Spawned:   sp,
		Lock:      lock,
		StartedAt: sp.StartedAt,
	}
	delete(s.deferReasons, task.ID)

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

func (s *Scheduler) inFlightIDs() map[string]bool {
	ids := make(map[string]bool, len(s.inFlight))
	for _, e := range s.inFlight {
		ids[e.Task.ID] = true
	}
	return ids
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
