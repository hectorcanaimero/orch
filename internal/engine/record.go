package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/state"
)

// Event types the dispatch path writes. They are Python's spellings, from
// EVENT_TYPES in orchestrator/state/file_backend.py, and this block is the one
// place the engine names them — a single mapping point, so a future rename is
// one edit and one test.
//
// `success` and `fail`, NOT `exit_ok`/`exit_err`: those two came from the
// superseded FR-STATE-7 list in the plan and no version of orch has ever
// emitted them (#122 aligned the Go set; see bug 9 and bug 11 in
// docs/brainstorm/go-migration-notes.md).
const (
	EventDispatch = "dispatch"
	EventSuccess  = "success"
	EventFail     = "fail"
	EventTimeout  = "timeout"
	EventIDSpoof  = "id_spoof_detected"
	EventRetry    = "retry"
	EventEscalate = "escalate"
	EventBlock    = "block"
	// EventBudgetPause marks the run parking until a provider window resets.
	EventBudgetPause = "budget_pause"
	// EventReconciled marks a dispatch row whose process was gone.
	EventReconciled = "reconciled"
	// EventPRCreated marks a pull request opened for a finished task.
	EventPRCreated = "pr_created"

	// EventSprintDone marks the run whose queue emptied (F4.7). Run-level,
	// so its task_id is empty; the counts ride in `extra`. Go only —
	// Python declares no such type, which internal/state's
	// goOnlyEventTypes explains.
	EventSprintDone = "sprint_done"
)

// RecordStart writes the `dispatch` event and the in-flight row, in that
// order, before the child is known to be doing anything useful.
//
// Both go through state.Backend rather than SQL, so the single writer stays
// single (checklist rule 17).
func RecordStart(ctx context.Context, b state.Backend, runID string, d Dispatch, sp *Spawned, attempt int) error {
	if err := b.RecordDispatch(ctx, state.Dispatch{
		RunID:      runID,
		TaskID:     d.Req.TaskID,
		Backend:    string(d.Req.Route.Backend),
		PID:        sp.PID,
		SessionID:  d.Req.SessionID,
		StartedAt:  utcSecond(sp.StartedAt),
		PromptPath: d.PromptPath,
		LogPath:    d.LogPath,
		OutputPath: d.Req.OutputPath,
		Attempt:    attempt,
	}); err != nil {
		return fmt.Errorf("record dispatch for %s: %w", d.Req.TaskID, err)
	}

	// Python emits backend, pid, cli_model and attempt on this event; the
	// dashboard's in-flight view reads pid, and the dedup hash uses it.
	if err := b.AppendEvent(ctx, runID, state.Event{
		RunID:     runID,
		EventType: EventDispatch,
		TaskID:    d.Req.TaskID,
		Backend:   string(d.Req.Route.Backend),
		TS:        utcSecond(sp.StartedAt),
		Extra: map[string]any{
			"pid":       sp.PID,
			"cli_model": d.Req.Route.CLIModel,
			"attempt":   attempt,
		},
	}); err != nil {
		return fmt.Errorf("append dispatch event for %s: %w", d.Req.TaskID, err)
	}
	return nil
}

// RecordFinish writes the spend row and the outcome event, and clears the
// in-flight row.
//
// Spend is recorded whether or not the dispatch succeeded: a failed run has
// usually already been paid for, and NFR-OBS-2 promises exactly one spend row
// per completed dispatch — including failures — so the cost view never misses
// a silent burn.
//
// The spend row goes first. If the process dies between the two writes, the
// cost is already accounted for; the reverse order would lose real money from
// the budget guardrail's view.
func RecordFinish(ctx context.Context, b state.Backend, runID string, d Dispatch, o Outcome, attempt int) error {
	ts := utcSecond(o.StartedAt.Add(o.Duration))

	if err := b.RecordSpend(ctx, state.Spend{
		TS:        ts,
		TaskID:    d.Req.TaskID,
		Backend:   string(d.Req.Route.Backend),
		Model:     d.Req.Route.CLIModel,
		TokensIn:  o.Result.TokensIn,
		TokensOut: o.Result.TokensOut,
		CostUSD:   o.Result.CostUSD,
		DurationS: o.Duration.Seconds(),
		Estimated: o.Result.Estimated,
	}); err != nil {
		return fmt.Errorf("record spend for %s: %w", d.Req.TaskID, err)
	}

	if err := b.AppendEvent(ctx, runID, outcomeEvent(runID, d, o, ts, attempt)); err != nil {
		return fmt.Errorf("append outcome event for %s: %w", d.Req.TaskID, err)
	}

	if err := b.ClearDispatch(ctx, runID, d.Req.TaskID); err != nil {
		return fmt.Errorf("clear dispatch for %s: %w", d.Req.TaskID, err)
	}
	return nil
}

// outcomeEvent builds the event for a finished dispatch.
//
// Python's reap loop picks the type the same way: `success` on success,
// `timeout` when the orchestrator killed it, `fail` otherwise — and
// `id_spoof_detected` is emitted separately by the post-run checks, alongside
// the failure event rather than instead of it, which is why it is a case here
// only for the class and not for the event type.
func outcomeEvent(runID string, d Dispatch, o Outcome, ts string, attempt int) state.Event {
	ev := state.Event{
		RunID:     runID,
		TaskID:    d.Req.TaskID,
		Backend:   string(d.Req.Route.Backend),
		TS:        ts,
		Extra:     map[string]any{"attempt": attempt},
		EventType: EventFail,
	}

	switch {
	case o.Result.Success:
		ev.EventType = EventSuccess
		// Python's success event carries cost and duration, not exit_code.
		ev.Extra["cost_usd"] = o.Result.CostUSD
		ev.Extra["duration_s"] = o.Duration.Seconds()
		return ev
	case o.TimedOut:
		ev.EventType = EventTimeout
	}

	// Python truncates the reason to 500 characters before it reaches the
	// event row; a CLI traceback is otherwise long enough to bury the row.
	ev.Extra["reason"] = truncateReason(o.Result.ErrorMessage)
	ev.Extra["exit_code"] = o.Result.ExitCode
	if o.Failure != "" {
		ev.Extra["failure_class"] = string(o.Failure)
	}
	return ev
}

// reasonLimit is Python's `(result.error_message or "unknown failure")[:500]`.
const reasonLimit = 500

func truncateReason(msg string) string {
	if msg == "" {
		return "unknown failure"
	}
	// Runes, not bytes: Python slices a str by characters, and a CLI error
	// can carry non-ASCII.
	r := []rune(msg)
	if len(r) <= reasonLimit {
		return msg
	}
	return string(r[:reasonLimit])
}

// utcSecond formats a timestamp the way the shell scripts, the Python backend
// and state.SQLite all do: second precision, always UTC, `Z` suffix. Shared
// with `scripts/task-*.sh`, so it is a contract rather than a preference.
func utcSecond(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05Z")
}

// compile-time reminder that the engine only ever names backends through
// model.Backend, never with a bare string literal.
var _ model.Backend = model.BackendClaude

// StateRecorder adapts a state.Backend to the scheduler's RecordBackend.
//
// The scheduler takes the narrow interface rather than state.Backend so the
// concurrency tests can run without a database — they are about semaphores,
// not about SQL — and so the one place that writes a dispatch row stays
// obvious.
type StateRecorder struct{ Backend state.Backend }

// NewStateRecorder wires a backend into the scheduler.
func NewStateRecorder(b state.Backend) StateRecorder { return StateRecorder{Backend: b} }

// RecordDispatchAndEvent writes the in-flight row and the `dispatch` event.
func (r StateRecorder) RecordDispatchAndEvent(ctx context.Context, runID string, d Dispatch, sp *Spawned, attempt int) error {
	return RecordStart(ctx, r.Backend, runID, d, sp, attempt)
}

// RecordFinish on the adapter writes the spend row, the outcome event and
// clears the in-flight row.
func (r StateRecorder) RecordFinish(ctx context.Context, runID string, d Dispatch, o Outcome, attempt int) error {
	return RecordFinish(ctx, r.Backend, runID, d, o, attempt)
}

// AppendEngineEvent writes one of the engine's own events.
func (r StateRecorder) AppendEngineEvent(ctx context.Context, runID, eventType, taskID, backend string, extra map[string]any) error {
	return r.Backend.AppendEvent(ctx, runID, state.Event{
		RunID:     runID,
		EventType: eventType,
		TaskID:    taskID,
		Backend:   backend,
		TS:        utcSecond(time.Now()),
		Extra:     extra,
	})
}

// TaskStatus reads a task's current status back.
func (r StateRecorder) TaskStatus(ctx context.Context, taskID string) (model.Status, error) {
	t, err := r.Backend.Task(ctx, taskID)
	if err != nil {
		return "", err
	}
	return t.Status, nil
}

// TaskComments reads a task's comment trail, for the prompt's dependency
// block. The rows are passed through as the JSON they already are: Python
// types a comment `list[dict]` with no fixed shape, and the renderer only
// needs two of its keys.
func (r StateRecorder) TaskComments(ctx context.Context, taskID string) ([]json.RawMessage, error) {
	t, err := r.Backend.Task(ctx, taskID)
	if err != nil {
		return nil, err
	}
	return t.Comments, nil
}

// Transition moves a task, recording who moved it and why.
func (r StateRecorder) Transition(ctx context.Context, taskID string, to model.Status, note string) error {
	return r.Backend.Transition(ctx, taskID, to, state.Note{Author: "orch", Body: note})
}
