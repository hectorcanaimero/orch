package project

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/state"
	"github.com/hectorcanaimero/orch/internal/vcs"
)

// RunLockFile is the file under a project's state directory that one `orch
// run` holds while it runs (engine.AcquireRunLock, #344).
const RunLockFile = "run.lock"

// OperatorAuthor is the author of everything the operator writes through
// Steer.
const OperatorAuthor = "operator"

// HeldReason is the note on a task the operator stopped and held.
const HeldReason = "held by the operator"

// EventOperatorStop is the event a stop request is written as, and the event
// the run that acted on it writes back (with `stopped` in its extra).
const EventOperatorStop = "operator_stop"

// Steer is how the operator steers a task (#356): a note for its agent, or a
// stop. The CLI, the MCP server and the dashboard all go through it, so every
// source writes the same request and only `orch run` touches processes.
type Steer struct {
	Backend  state.Backend
	StateDir string
	// Source names who asked, in the request's extra: cli, mcp, dashboard.
	Source string
	// VCS is vcs.provider and vcs.host: which merge Accept makes.
	VCS vcs.Config
	// Provider, when set, is used instead of the one VCS names (tests).
	Provider vcs.Provider
	// Root, BaseBranch and TasksJSON are what Accept lands a task with in
	// local mode: the project's repository, dispatch.base_branch ("" is
	// main), and where the squash message's title and spec come from.
	Root, BaseBranch, TasksJSON string
}

// Providers are the backends orch has an adapter for, the names
// SetProvider accepts.
var Providers = []model.Backend{model.BackendClaude, model.BackendCodex, model.BackendOpencode, model.BackendGemini, model.BackendAgy, model.BackendMuse}

// SetProvider turns dispatching to backend off (with the operator's reason)
// or back on for this project: `orch run` leaves the tasks routed to a
// disabled backend waiting, as it does for a capped budget. The one function
// behind `orch provider disable|enable` and the dashboard's switch.
func (s Steer) SetProvider(ctx context.Context, backend string, disabled bool, reason string) error {
	if !slices.Contains(Providers, model.Backend(backend)) {
		return fmt.Errorf("unknown provider %q: one of %v", backend, Providers)
	}
	if !disabled {
		reason = ""
	}
	return s.Backend.SetProviderState(ctx, backend, disabled, strings.TrimSpace(reason), time.Now())
}

// Note records a note for the task's agent. It reaches the prompt of the
// task's next attempt, not the one running now.
func (s Steer) Note(ctx context.Context, taskID, body string) error {
	body = strings.TrimSpace(body)
	if body == "" {
		return errors.New("the note is empty")
	}
	return s.Backend.AddNote(ctx, taskID, state.Note{
		Author: OperatorAuthor, Body: body, Kind: state.NoteKindOperator,
	})
}

// StopResult is what Stop did.
type StopResult struct {
	TaskID string `json:"task_id"`
	// Requested is true when a running `orch run` was asked to stop the
	// task; it acts on its next tick. RequestID is the event row after which
	// its answer lands (see WaitStopped).
	Requested bool  `json:"requested"`
	RequestID int64 `json:"-"`
	Hold      bool  `json:"hold"`
	// From and To are the plain status change made when no run is running;
	// equal when there was nothing to change.
	From    model.Status `json:"from,omitempty"`
	To      model.Status `json:"to,omitempty"`
	Message string       `json:"message"`
}

// Stop ends the task's running attempt. hold keeps it from being dispatched
// again (blocked, HeldReason; Unblock is `orch task set --status todo`);
// without it the task is dispatched again, with note — when there is one —
// in its prompt.
//
// With an `orch run` running it writes a request that run acts on. With none,
// nothing is running, so it is a plain status change, and says so.
func (s Steer) Stop(ctx context.Context, taskID string, hold bool, note string) (StopResult, error) {
	t, err := s.Backend.Task(ctx, taskID)
	if err != nil {
		return StopResult{}, fmt.Errorf("reading %s: %w", taskID, err)
	}
	if strings.TrimSpace(note) != "" {
		if err := s.Note(ctx, taskID, note); err != nil {
			return StopResult{}, fmt.Errorf("adding the note to %s: %w", taskID, err)
		}
	}
	res := StopResult{TaskID: taskID, Hold: hold, From: t.Status, To: t.Status}

	live, err := s.runLive(ctx, taskID)
	if err != nil {
		return StopResult{}, err
	}
	if live {
		after, err := s.Backend.LatestEventID(ctx)
		if err != nil {
			return StopResult{}, fmt.Errorf("reading the event log: %w", err)
		}
		// No run id: the request comes from outside any run. That also
		// keeps the event dedup hash of the run's answer — written in the
		// same second, for the same task and type, under the run's id —
		// apart from this row's.
		if err := s.Backend.AppendEvent(ctx, "", state.Event{
			EventType: EventOperatorStop,
			TaskID:    taskID,
			TS:        time.Now().UTC().Format("2006-01-02T15:04:05Z"),
			// pid is the requester's, so two requests in one second both
			// land.
			Extra: map[string]any{"hold": hold, "requested_by": s.Source, "pid": os.Getpid()},
		}); err != nil {
			return StopResult{}, fmt.Errorf("writing the stop request for %s: %w", taskID, err)
		}
		res.Requested, res.RequestID = true, after
		then := "dispatches it again"
		if hold {
			then = "holds it (blocked, \"" + HeldReason + "\")"
		}
		res.Message = fmt.Sprintf("stop requested: the running orch run ends %s's attempt and %s", taskID, then)
		return res, nil
	}

	switch {
	case hold:
		res.To = model.StatusBlocked
	case t.Status == model.StatusInProgress:
		res.To = model.StatusTodo
	default:
		res.Message = fmt.Sprintf("no orch run is running and %s is %s: nothing to stop", taskID, t.Status)
		return res, nil
	}
	body := HeldReason
	if !hold {
		body = "stopped by the operator"
	}
	if err := s.Backend.Transition(ctx, taskID, res.To, state.Note{Author: OperatorAuthor, Body: body}); err != nil {
		return StopResult{}, fmt.Errorf("moving %s to %s: %w", taskID, res.To, err)
	}
	res.Message = fmt.Sprintf("no orch run is running: %s %s -> %s", taskID, res.From, res.To)
	return res, nil
}

// ErrRunOwnsTask is a move only `orch run` makes: starting a task, or
// finishing one that is running. 409 on the dashboard.
var ErrRunOwnsTask = errors.New("only orch run starts or finishes a running task")

// Move puts a task in another column of the board (v0.22): the dashboard's
// drag and its status picker. Blocking needs a reason, which becomes the
// task's note. A running task is not moved behind its run's back: to todo or
// blocked it is a Stop (redirect or hold), and nothing moves a task into
// in-progress or a running one to done but `orch run`.
func (s Steer) Move(ctx context.Context, taskID string, to model.Status, note string) (StopResult, error) {
	note = strings.TrimSpace(note)
	if to == model.StatusBlocked && note == "" {
		return StopResult{}, errors.New("blocking a task needs a reason")
	}
	t, err := s.Backend.Task(ctx, taskID)
	if err != nil {
		return StopResult{}, fmt.Errorf("reading %s: %w", taskID, err)
	}
	if to == model.StatusInProgress && t.Status != to {
		return StopResult{}, fmt.Errorf("%s: %w", taskID, ErrRunOwnsTask)
	}
	if t.Status == model.StatusInProgress && to != model.StatusInProgress {
		if to != model.StatusTodo && to != model.StatusBlocked {
			return StopResult{}, fmt.Errorf("%s is running: %w; stop it first", taskID, ErrRunOwnsTask)
		}
		return s.Stop(ctx, taskID, to == model.StatusBlocked, note)
	}
	// Blocking finished work (the Changes tab's Block, #454): done only
	// reopens to todo, so it goes through there.
	if t.Status == model.StatusDone && to == model.StatusBlocked {
		if err := s.Backend.Transition(ctx, taskID, model.StatusTodo, state.Note{Author: OperatorAuthor, Body: "reopened to block it"}); err != nil {
			return StopResult{}, fmt.Errorf("reopening %s: %w", taskID, err)
		}
	}
	if err := s.Backend.Transition(ctx, taskID, to, state.Note{Author: OperatorAuthor, Body: note}); err != nil {
		return StopResult{}, fmt.Errorf("moving %s to %s: %w", taskID, to, err)
	}
	return StopResult{TaskID: taskID, From: t.Status, To: to,
		Message: fmt.Sprintf("%s %s -> %s", taskID, t.Status, to)}, nil
}

// WaitStopped waits up to timeout for the run's answer to a request Stop
// made, and returns it. ok is false when the run has not answered in time.
func (s Steer) WaitStopped(ctx context.Context, res StopResult, timeout time.Duration) (state.Event, bool, error) {
	deadline := time.Now().Add(timeout)
	for after := res.RequestID; ; {
		events, err := s.Backend.EventsSince(ctx, after, 200)
		if err != nil {
			return state.Event{}, false, fmt.Errorf("reading the run's answer: %w", err)
		}
		for _, e := range events {
			after = e.ID
			if e.EventType == EventOperatorStop && e.TaskID == res.TaskID && e.Extra["stopped"] != nil {
				return e, true, nil
			}
		}
		if time.Now().After(deadline) {
			return state.Event{}, false, nil
		}
		select {
		case <-ctx.Done():
			return state.Event{}, false, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// runLive reports whether an `orch run` may be acting on this project: one
// holds the run lock, or (with --task-locks, which takes none) the task has a
// dispatch whose process is alive. An unreadable dispatch table is an error:
// guessing "no run" would change the status behind a run's back.
func (s Steer) runLive(ctx context.Context, taskID string) (bool, error) {
	if f, err := os.Open(filepath.Join(s.StateDir, RunLockFile)); err == nil { // #nosec G304 -- the project's own state dir
		defer func() { _ = f.Close() }()
		// A shared, non-blocking flock fails only while a run holds its
		// exclusive one.
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); errors.Is(err, syscall.EWOULDBLOCK) {
			return true, nil
		} else if err == nil {
			_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		}
	}
	rows, err := s.Backend.InFlightDispatches(ctx)
	if err != nil {
		return false, fmt.Errorf("reading the running dispatches: %w", err)
	}
	for _, d := range rows {
		if d.TaskID == taskID && d.PID > 0 {
			if err := syscall.Kill(d.PID, 0); err == nil || errors.Is(err, syscall.EPERM) {
				return true, nil
			}
		}
	}
	return false, nil
}
