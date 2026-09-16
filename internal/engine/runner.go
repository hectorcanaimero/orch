package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/hectorcanaimero/orch/internal/budget"
	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/providers"
	"github.com/hectorcanaimero/orch/internal/pyfmt"
	"github.com/hectorcanaimero/orch/internal/state"
)

// ExitInterrupted is what `orch run` exits with after a signal drained it.
// 128 + SIGINT, the shell convention, and what the Python `orch run` returns.
const ExitInterrupted = 130

// Loop timings, ported from orch.py's main loop.
const (
	// tickInterval is how long the loop sleeps when there is nothing else to
	// do. Short, because it is what bounds how quickly a finished child is
	// noticed.
	tickInterval = 200 * time.Millisecond
	// reconcileInterval is the orphan-PID sweep's period. Coarse enough to
	// be near-free — one signal per in-flight row — and responsive enough
	// that a crashed CLI does not linger on the dashboard past a minute.
	reconcileInterval = 60 * time.Second
	// budgetSleepChunk caps a single sleep while every provider is capped,
	// so a signal still lands promptly.
	budgetSleepChunk = 30 * time.Second
	// drainTimeout bounds the final wait for children after a signal.
	drainTimeout = 5 * time.Minute
	// stallThreshold is how long a live run may go without progress (nothing
	// dispatched, reaped or decided by CI, and nothing running) before it
	// warns and notifies, once per stall (#255).
	//
	// A budget pause is not counted: the loop sleeps through it and logs it.
	//
	// ponytail: a constant; a config key if a project's CI routinely takes
	// longer than this.
	stallThreshold = 30 * time.Minute
)

// BudgetWindow is the part of budget.Gate the loop needs beyond the
// per-dispatch check.
type BudgetWindow interface {
	AllCapped(ctx context.Context) (bool, error)
	EarliestReset(ctx context.Context) (time.Time, error)
}

// DispatchReader lists the dispatch rows a project still has in flight, and
// clears the ones whose process is gone. A narrow interface so the loop is
// testable without a database; state.Backend satisfies it (#148).
type DispatchReader interface {
	InFlightDispatches(ctx context.Context) ([]state.Dispatch, error)
	ClearDispatch(ctx context.Context, runID, taskID string) error
}

// StatusReader reads one task's status from the source of truth.
type StatusReader interface {
	TaskStatus(ctx context.Context, taskID string) (model.Status, error)
}

// Runner drives one `orch run`: it alternates reaping and refilling until the
// work is done or a signal arrives.
type Runner struct {
	Scheduler *Scheduler
	// Budget supplies the all-capped check. Nil disables the sleep.
	Budget BudgetWindow
	// Dispatches backs the orphan sweep. Nil disables it.
	Dispatches DispatchReader
	// CI watches the pull requests this run opened. Nil disables it, which
	// is every project not running worktree mode with auto-PR.
	CI *CIPoller
	// Alerts announces budget windows near or at their cap. Nil disables it.
	Alerts *BudgetAlerts
	// Statuses is the database's view of each task, which this run adopts
	// where it changed behind the run's back. Nil disables it.
	Statuses StatusReader

	// Signals, when non-nil, is used instead of installing real handlers —
	// the tests drive it directly rather than signalling the test binary.
	Signals <-chan os.Signal

	// sprintDone guards the one-per-run rule for the sprint_done event.
	sprintDone bool
	// ciWaiting is how many of this run's PRs were last seen waiting on CI,
	// so the wait is logged when it changes rather than on every tick.
	ciWaiting int
	// lastProgress is when this run last moved anything, and stallWarned
	// whether the stall since then was already reported.
	lastProgress time.Time
	stallWarned  bool
	// lastBudgetAlert is when the budget alerts last read the windows.
	lastBudgetAlert time.Time

	// tick, sleep and now are the loop's clock, injectable so the tests do
	// not spend a real minute proving a sixty-second sweep.
	sleep func(time.Duration)
	now   func() time.Time
}

// NewRunner wires a runner over a scheduler.
func NewRunner(s *Scheduler) *Runner {
	return &Runner{
		Scheduler: s,
		sleep:     func(d time.Duration) { time.Sleep(d) },
		now:       time.Now,
	}
}

// Run walks the loop until there is nothing left to do, and returns the
// process exit code.
//
// Port of orch.py's `while True:` in main(). The order inside a tick is the
// Python's and matters: reap first so finished children free their slots
// before the refill that wants them, then refill, then the budget sleep, then
// the termination test.
//
// A signal drains rather than exits: children already running are given time
// to finish, because killing an agent mid-edit leaves a worktree in a state
// nobody asked for. A second signal escalates to SIGKILL on every process
// group. That is what makes Ctrl-C twice mean "I meant it".
func (r *Runner) Run(ctx context.Context) (int, error) {
	s := r.Scheduler

	signals, stop := r.signalSource()
	defer stop()

	// Resume: adopt or clear whatever a previous run left behind before
	// dispatching anything new, or a crashed orch's rows would hold tasks
	// in-progress forever and their slots would never come back.
	if err := r.reconcile(ctx, "startup"); err != nil {
		s.logger().Warn("startup reconcile failed; continuing", "err", err)
	}

	lastReconcile := r.now()
	r.lastProgress = r.now()
	interrupted := false
	reoffered := false

	for {
		select {
		case <-ctx.Done():
			return r.drain(ctx, true)
		case sig := <-signals:
			if s.Draining() {
				// Second signal: stop being polite.
				s.logger().Warn("second signal; killing every child process group",
					"signal", sig)
				r.killAllGroups()
			} else {
				s.logger().Warn("draining in-flight work; signal again to kill it",
					"signal", sig)
				s.StartDraining()
				interrupted = true
			}
		default:
		}

		reaped, err := s.Reap(ctx)
		if err != nil {
			return 1, fmt.Errorf("reap: %w", err)
		}

		// CI runs on its own interval, after the reap that may have just
		// opened a PR. A sweep that cannot happen is logged and the loop
		// carries on: being briefly blind to CI is better than a poller
		// that can end a run.
		ciActed, err := r.CI.Poll(ctx, s)
		if err != nil {
			s.logger().Warn("CI poll failed; continuing", "err", err)
		}
		progressed := reaped+ciActed > 0

		if s.Draining() && len(s.InFlight()) == 0 {
			break
		}

		if !s.Draining() {
			started, err := s.Refill(ctx)
			if err != nil {
				return 1, fmt.Errorf("refill: %w", err)
			}
			progressed = progressed || started > 0
		}

		// Before the capped sleep below, which skips the rest of the tick:
		// the moment every provider is capped is the one worth announcing.
		if r.Alerts != nil && (r.lastBudgetAlert.IsZero() || r.now().Sub(r.lastBudgetAlert) >= budgetAlertInterval) {
			r.lastBudgetAlert = r.now()
			r.Alerts.Check(ctx, r.lastBudgetAlert, s.budgetWaiting())
		}

		// Every provider capped and nothing running: sleep until the window
		// resets rather than spinning. Only with nothing in flight — a
		// running child needs the short tick to be noticed when it exits.
		if !s.Draining() && len(s.InFlight()) == 0 {
			slept, err := r.sleepWhileCapped(ctx)
			if err != nil {
				s.logger().Warn("budget window check failed; continuing", "err", err)
			}
			if slept {
				continue
			}
		}

		// A ready task that can never be dispatched would otherwise spin the
		// loop forever at one tick per pass, silently. Python does exactly
		// that: its terminate condition is "nothing in flight, nothing
		// ready, no retries", so a task whose model has no route — or whose
		// backend this binary cannot drive — keeps the ready set non-empty
		// and the loop never ends and never says why.
		if stuck := r.permanentlyStuck(); len(stuck) > 0 {
			return 1, fmt.Errorf(
				"nothing can be dispatched and nothing is running: %s. "+
					"Check the model_router.yaml entries and the concurrency caps for these tasks",
				strings.Join(stuck, ", "))
		}

		if r.workIsDone(ctx) {
			// Before giving up, look again: a task may have moved in the
			// database behind this run's back and made more work ready.
			if r.adoptStatuses(ctx) > 0 {
				continue
			}
			// Semi mode offers deferred tasks one more round before giving
			// up on them, so "not now" does not silently become "never".
			//
			// Once, though. Python re-offers on every pass, which terminates
			// only because a human eventually stops pressing N; with Gate an
			// interface, a caller that always defers would spin forever. One
			// second chance is the intent, and it cannot hang.
			if s.Opts.Mode == ModeSemi && !reoffered && s.ClearDeferred() > 0 {
				reoffered = true
				continue
			}
			r.recordSprintDone(ctx)
			break
		}

		if r.now().Sub(lastReconcile) >= reconcileInterval {
			lastReconcile = r.now()
			if err := r.reconcile(ctx, "tick"); err != nil {
				s.logger().Warn("tick reconcile failed; continuing", "err", err)
			}
			progressed = r.adoptStatuses(ctx) > 0 || progressed
		}

		r.checkStall(ctx, progressed)
		r.sleep(tickInterval)
	}

	code, err := r.drain(ctx, interrupted)
	return code, err
}

// drain waits out whatever is still running, cleans up the worktrees, and
// returns the exit code.
//
// RemoveAll runs here, after the wait — never from a signal handler, where it
// would race a task still writing into its worktree (internal/worktree's
// Caller contract).
func (r *Runner) drain(ctx context.Context, interrupted bool) (int, error) {
	s := r.Scheduler
	if len(s.InFlight()) > 0 {
		// A cancelled context still needs to wait, so the drain gets a
		// context of its own rather than the dead one.
		drainCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		defer cancel()
		if _, err := s.DrainWait(drainCtx, drainTimeout); err != nil {
			s.logger().Error("drain failed", "err", err)
		}
	}
	s.CleanupWorktrees(ctx)

	if interrupted {
		return ExitInterrupted, nil
	}
	return 0, nil
}

// recordSprintDone writes the one run-level event this loop emits (F4.7).
//
// It marks the queue emptying — nothing running, nothing ready, no retry
// waiting — which is the only exit from this loop that means the run finished
// rather than stopped. A signal drains out through `case <-signals`, a
// permanently-stuck task returns an error, and neither is a sprint that
// ended; emitting there would tell a cron job the work was finished when a
// human had just pressed Ctrl-C.
//
// "The queue emptied" is not the same as "everything is done": a run whose
// remaining tasks are all blocked ends here too. Rather than pick one meaning
// and be wrong half the time, the counts ride along in `extra` and the reader
// decides. `blocked > 0` is what "finished with work left" looks like.
//
// Once per run, and the guard is belt to the loop's braces: the one call site
// is followed immediately by `break`, so today it cannot run twice. It is kept
// because the next person to add an exit path — and there have already been
// four — would otherwise have to notice that this is the kind of event that
// must not repeat. The dedup hash does not cover it either: two rows a second
// apart hash differently and would both land. Exercised directly by
// TestSprintDoneIsWrittenOnlyOnce rather than through the loop, because a
// guard no test can reach is not a guard.
//
// Best effort. A run that finished must not fail on the way out because a
// side-channel row could not be written, so a failure is logged and the exit
// code is unchanged — the same contract the notifier has.
func (r *Runner) recordSprintDone(ctx context.Context) {
	s := r.Scheduler
	if r.sprintDone || s.Backend == nil {
		return
	}
	r.sprintDone = true

	counts := map[string]int{}
	for _, t := range s.Queue.AllTasks() {
		st, ok := s.Queue.Status(t.ID)
		if !ok {
			st = t.Status
		}
		counts[string(st)]++
	}
	total := len(s.Queue.AllTasks())
	extra := map[string]any{
		"total":       total,
		"done":        counts[string(model.StatusDone)],
		"blocked":     counts[string(model.StatusBlocked)],
		"todo":        counts[string(model.StatusTodo)],
		"backlog":     counts[string(model.StatusBacklog)],
		"in_progress": counts[string(model.StatusInProgress)],
		"dispatched":  s.dispatched,
		"mode":        string(s.Opts.Mode),
	}
	if err := s.Backend.AppendEngineEvent(ctx, s.Opts.RunID, EventSprintDone, "", "", extra); err != nil {
		s.logger().Warn("could not record the sprint_done event", "err", err)
	}
}

// workIsDone reports the termination condition: nothing running, nothing
// ready, no retry waiting out a backoff, and no PR of this run waiting on CI.
//
// The last one is Go's, not Python's. Without it a run whose remaining work
// was all under review exited, the CI poller with it, and nothing finished
// those tasks or dispatched their dependents until someone ran `orch run`
// again (#239).
func (r *Runner) workIsDone(ctx context.Context) bool {
	s := r.Scheduler
	if len(s.InFlight()) > 0 || len(s.RetryQueue()) > 0 {
		return false
	}
	if len(s.Queue.Ready(ReadyOpts{
		Only:     s.Opts.Only,
		Deferred: s.deferred,
	})) > 0 {
		return false
	}
	return !r.waitingOnCI(ctx)
}

// adoptStatuses takes the database's status for every task this run is not
// itself moving, and reports how many changed.
//
// The queue is this run's memory, and the database is where the truth lives:
// a person unblocks or fixes a task with `orch task-status`, or a bookkeeping
// write fails after the queue already moved. Left alone, the run idles or
// exits with ready work (#255, #276). Skipped: tasks running, waiting out a
// retry, or waiting on CI. Those are done in the database (the agent said so)
// but not accepted yet, and adopting that would release their dependents onto
// unreviewed work (#230).
//
// ponytail: one read per task per sweep, fine for hundreds of tasks; a bulk
// read if a DAG ever gets far bigger.
func (r *Runner) adoptStatuses(ctx context.Context) int {
	if r.Statuses == nil {
		return 0
	}
	s := r.Scheduler
	skip := s.inFlightIDs()
	for id := range s.retryQueueIDs() {
		skip[id] = true
	}
	if r.CI != nil && r.CI.Backend != nil {
		rows, err := r.CI.Backend.TasksWithPendingCI(ctx)
		if err != nil {
			s.logger().Warn("could not list tasks waiting on CI; not adopting statuses", "err", err)
			return 0
		}
		for _, row := range rows {
			skip[row.ID] = true
		}
	}

	changed := map[string]model.Status{}
	for _, t := range s.Queue.AllTasks() {
		if skip[t.ID] {
			continue
		}
		db, err := r.Statuses.TaskStatus(ctx, t.ID)
		if err != nil {
			continue // not in the database yet, or unreadable: keep the run's view
		}
		if cur, _ := s.Queue.Status(t.ID); cur != db {
			s.logger().Info("adopting a status changed outside this run",
				"task", t.ID, "run", cur, "database", db)
			changed[t.ID] = db
		}
	}
	s.Queue.Hydrate(changed)
	return len(changed)
}

// waitingOnCI reports whether a task of this run has a PR still waiting on
// CI. A read that fails answers no, so a broken database ends the run the way
// it did before rather than holding it open blind.
func (r *Runner) waitingOnCI(ctx context.Context) bool {
	if r.Scheduler.Draining() {
		return false
	}
	s := r.Scheduler
	rows, err := r.ciRows(ctx)
	if err != nil {
		s.logger().Warn("could not list tasks waiting on CI; not waiting for them", "err", err)
		return false
	}
	n := len(rows)
	if n != r.ciWaiting && n > 0 {
		s.logger().Info("waiting on CI", "tasks", n)
	}
	r.ciWaiting = n
	return n > 0
}

// ciRows lists this run's tasks with a PR waiting on CI. Tasks outside this
// run's DAG are left out: the poller would not re-dispatch them either.
func (r *Runner) ciRows(ctx context.Context) ([]state.TaskRuntime, error) {
	if r.CI == nil || r.CI.Provider == nil || r.CI.Backend == nil {
		return nil, nil
	}
	rows, err := r.CI.Backend.TasksWithPendingCI(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing tasks with pending CI: %w", err)
	}
	var out []state.TaskRuntime
	for _, row := range rows {
		if _, ok := r.Scheduler.Queue.Task(row.ID); ok && row.PRURL != "" {
			out = append(out, row)
		}
	}
	return out, nil
}

// checkStall says so when the run has gone stallThreshold without progress,
// once per stall. progressed is whether this tick moved anything; a child
// still running counts as progress too, since its own timeout bounds it.
//
// Before this, a run waiting on CI that never resolved, or on ready tasks it
// would not dispatch, idled in silence (#255).
func (r *Runner) checkStall(ctx context.Context, progressed bool) {
	s := r.Scheduler
	now := r.now()
	if progressed || len(s.InFlight()) > 0 {
		r.lastProgress, r.stallWarned = now, false
		return
	}
	if r.stallWarned || now.Sub(r.lastProgress) < stallThreshold {
		return
	}
	r.stallWarned = true
	stalled := now.Sub(r.lastProgress).Round(time.Second)
	waiting := r.stallSummary(ctx)
	s.logger().Warn("orch run has made no progress",
		"since", r.lastProgress.UTC().Format(time.RFC3339), "for", stalled, "waiting_on", waiting)
	if s.Notify != nil {
		s.Notify.Stalled(ctx, stalled, waiting)
	}
}

// stallSummary names what a stalled run is waiting on.
func (r *Runner) stallSummary(ctx context.Context) string {
	s := r.Scheduler
	var parts []string
	rows, err := r.ciRows(ctx)
	if err != nil {
		parts = append(parts, "CI, unreadable: "+err.Error())
	}
	for _, row := range rows {
		parts = append(parts, "CI on "+row.ID+" ("+row.PRURL+")")
	}
	for _, item := range s.RetryQueue() {
		parts = append(parts, "a retry of "+item.Task.ID+" due "+item.EarliestAt.UTC().Format(time.RFC3339))
	}
	reasons := s.DeferReasons()
	for _, t := range s.Queue.Ready(ReadyOpts{Only: s.Opts.Only, Deferred: s.deferred, Waiting: s.retryQueueIDs()}) {
		reason := reasons[t.ID]
		switch {
		case reason != "":
		case s.Opts.MaxTasks > 0 && s.dispatched >= s.Opts.MaxTasks:
			reason = "--max-tasks reached"
		default:
			reason = "no reason recorded"
		}
		parts = append(parts, t.ID+" ready but not dispatched: "+reason)
	}
	if len(parts) == 0 {
		return "nothing this run can name"
	}
	return strings.Join(parts, "; ")
}

// permanentlyStuck names the ready tasks that can never be dispatched, when
// that is the only thing left to do.
//
// "Never" is meant literally, and the distinction matters: a task waiting on
// capacity or on a budget window is not stuck, because a slot frees and a
// window resets. A task whose model has no route, whose backend this binary
// has no adapter for, or whose backend has no configured concurrency cap will
// be refused identically on every future pass. Waiting for that is waiting
// forever.
//
// Returns nothing while anything is in flight or queued for retry — that is
// progress, however slow.
func (r *Runner) permanentlyStuck() []string {
	s := r.Scheduler
	if len(s.InFlight()) > 0 || len(s.RetryQueue()) > 0 {
		return nil
	}
	ready := s.Queue.Ready(ReadyOpts{Only: s.Opts.Only, Deferred: s.deferred})
	if len(ready) == 0 {
		return nil
	}

	var stuck []string
	for _, task := range ready {
		reason := dispatchBlocker(task, s)
		if reason == "" {
			// At least one task can still go; the run is not stuck.
			return nil
		}
		stuck = append(stuck, task.ID+" ("+reason+")")
	}
	return stuck
}

// dispatchBlocker names why a task can never be dispatched, or "" when it
// can.
func dispatchBlocker(task model.Task, s *Scheduler) string {
	route, ok := s.Routes[task.Model]
	if !ok {
		return "no route for model " + pyfmt.Quote(task.Model)
	}
	backend := string(route.Backend)
	if !s.Sems.Knows(backend) {
		return "no concurrency cap configured for backend " + pyfmt.Quote(backend)
	}
	if _, err := providers.Get(route.Backend); err != nil {
		return err.Error()
	}
	return ""
}

// sleepWhileCapped parks the loop until the soonest budget window resets,
// and reports whether it slept.
//
// AllCapped is checked FIRST, and that ordering is the point: EarliestReset
// answers with the soonest reset among the providers that are currently
// blocked, not with a promise that they all are. Sleeping on it without the
// AllCapped check would park a run that still had a free provider to
// dispatch to — its own doc comment in internal/budget says so.
func (r *Runner) sleepWhileCapped(ctx context.Context) (bool, error) {
	if r.Budget == nil {
		return false, nil
	}
	capped, err := r.Budget.AllCapped(ctx)
	if err != nil || !capped {
		return false, err
	}

	resetAt, err := r.Budget.EarliestReset(ctx)
	if err != nil {
		return false, err
	}
	if resetAt.IsZero() {
		// Capped but with no reset to wait for. Sleeping forever would be
		// worse than ticking.
		return false, nil
	}

	wait := resetAt.Sub(r.now())
	if wait < time.Second {
		wait = time.Second
	}
	chunk := min(wait, budgetSleepChunk)

	r.Scheduler.logger().Info("every provider is capped; waiting for the window to reset",
		"sleeping", chunk, "resets_at", resetAt.UTC().Format(time.RFC3339))
	r.recordBudgetPause(ctx, resetAt, wait)
	r.sleep(chunk)
	return true, nil
}

func (r *Runner) recordBudgetPause(ctx context.Context, resetAt time.Time, wait time.Duration) {
	s := r.Scheduler
	if s.Backend == nil {
		return
	}
	// Python emits this against the task id "-", because the pause is the
	// run's and not any one task's.
	if err := s.Backend.AppendEngineEvent(ctx, s.Opts.RunID, EventBudgetPause, "-", "all",
		map[string]any{
			"reset_at":     resetAt.UTC().Format("2006-01-02T15:04:05Z"),
			"wait_seconds": wait.Seconds(),
		}); err != nil {
		s.logger().Error("recording the budget pause failed", "err", err)
	}
}

// reconcile sweeps for dispatch rows whose process is gone.
//
// A crashed orch, a closed shell, an OS session tear-down: the rows stay
// `in_flight` and their tasks stay `in-progress`, so nothing ever becomes
// ready again. The sweep removes the row and puts the task back to todo.
//
// It never fails the run. A reconciliation that cannot be done is not a
// reason to stop dispatching — the same fail-open the budget gate takes, and
// the same reason: an observability sweep must not be able to end a run.
func (r *Runner) reconcile(ctx context.Context, phase string) error {
	if r.Dispatches == nil {
		return nil
	}
	rows, err := r.Dispatches.InFlightDispatches(ctx)
	if err != nil {
		return fmt.Errorf("listing in-flight dispatches: %w", err)
	}

	s := r.Scheduler
	orphans := 0
	for _, row := range rows {
		// A row this process owns is not an orphan — it is a child we are
		// supervising right now.
		if _, ours := s.InFlight()[row.PID]; ours {
			continue
		}
		alive, known := pidAlive(row.PID)
		if !known || alive {
			continue
		}

		orphans++
		if err := r.Dispatches.ClearDispatch(ctx, row.RunID, row.TaskID); err != nil {
			s.logger().Error("clearing an orphan dispatch failed",
				"task", row.TaskID, "pid", row.PID, "err", err)
			continue
		}
		// Back to todo so the next tick can pick it up. A task stuck
		// in-progress behind a dead process is the symptom this exists for.
		if _, known := s.Queue.Status(row.TaskID); known {
			if err := s.Queue.MarkTodo(row.TaskID); err != nil {
				s.logger().Error("resetting an orphan task failed",
					"task", row.TaskID, "err", err)
			}
		}
		if s.Backend != nil {
			if err := s.Backend.AppendEngineEvent(ctx, row.RunID, EventReconciled,
				row.TaskID, row.Backend, map[string]any{
					"pid":    row.PID,
					"reason": "process is gone",
				}); err != nil {
				s.logger().Error("recording the reconciliation failed",
					"task", row.TaskID, "err", err)
			}
		}
	}

	if orphans > 0 {
		s.logger().Info("reconciled orphaned dispatches",
			"phase", phase, "orphans", orphans, "checked", len(rows))
	}
	return nil
}

// pidAlive reports whether a process exists, and whether the answer is known.
//
// Signal 0 checks for existence without delivering anything. The three
// outcomes are not two: ESRCH means gone, EPERM means the process exists and
// belongs to someone else — so it is ALIVE — and anything else means the
// question could not be answered, where the safe reading is to leave the row
// alone rather than reap a running dispatch.
func pidAlive(pid int) (alive, known bool) {
	if pid <= 0 {
		return false, false
	}
	err := syscall.Kill(pid, 0)
	switch {
	case err == nil:
		return true, true
	case errors.Is(err, syscall.ESRCH):
		return false, true
	case errors.Is(err, syscall.EPERM):
		return true, true
	default:
		return false, false
	}
}

// killAllGroups SIGKILLs every in-flight child's process group. The second
// signal's escalation.
func (r *Runner) killAllGroups() {
	for _, entry := range r.Scheduler.InFlight() {
		entry.Spawned.signalGroup(syscall.SIGKILL)
	}
}

// signalSource returns the channel the loop watches, and a function to stop
// watching. A test supplies its own channel rather than signalling the test
// binary, which would take the whole suite down with it.
func (r *Runner) signalSource() (<-chan os.Signal, func()) {
	if r.Signals != nil {
		return r.Signals, func() {}
	}
	// SIGTERM is handled identically to SIGINT, so `kill <pid>` behaves like
	// Ctrl-C rather than stranding children.
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	return ch, func() { signal.Stop(ch) }
}

// StartDraining stops new dispatches while letting what is running finish.
func (s *Scheduler) StartDraining() { s.draining = true }

// ClearDeferred forgets the operator's deferrals and returns how many there
// were, so semi mode can offer them again before the run ends.
func (s *Scheduler) ClearDeferred() int {
	n := len(s.deferred)
	s.deferred = make(map[string]bool)
	return n
}

// compile-time check that budget.Gate satisfies the window interface.
var _ BudgetWindow = (*budget.Gate)(nil)

// compile-time reminder that the loop only names statuses through model.
var _ model.Status = model.StatusTodo
