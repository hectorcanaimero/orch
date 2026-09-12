package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/project"
	"github.com/hectorcanaimero/orch/internal/state"
)

// defaultAuthor matches `orch task-status`'s own `--author` default, so a
// tool call with no author leaves the same trail a script call would.
const defaultAuthor = "orch"

// Error codes carried by transitionError.Code. A model reads the code, not
// the prose, so they are a closed set and spelled here once.
const (
	codeUnknownTask        = "unknown_task"
	codeInvalidStatus      = "invalid_status"
	codeIllegalTransition  = "illegal_transition"
	codeMissingRequiredArg = "missing_required_argument"
)

// transitionError is the structured half of a refused write.
//
// The list is the point. `state.Transition` already refuses an illegal move
// and its message names the legal destinations — but as prose, inside an
// error string, which a model has to parse out of English. Here the same
// facts are fields: where the task actually is, where it may go, and which
// of the four failure kinds this was. An agent that tried `done` on a task
// still in `backlog` can read `valid_transitions` and pick `in-progress`
// without another round trip.
type transitionError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// From is the task's current status. Empty when the task does not
	// exist, which is the one failure with no "from".
	From string `json:"from,omitempty"`
	// ValidTransitions is every status the task may legally move to from
	// From, in the canonical status order.
	ValidTransitions []string `json:"valid_transitions,omitempty"`
}

// setStatusOut is what both writing tools return.
//
// `ok` is a field rather than an exception because MCP tool errors are
// content: the call itself succeeded, the write did not. IsError is set on
// the result as well, so a client that only looks at the envelope still sees
// a failure.
type setStatusOut struct {
	OK     bool   `json:"ok"`
	TaskID string `json:"task_id"`
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
	// Error is present exactly when OK is false.
	Error *transitionError `json:"error,omitempty"`
}

type setStatusIn struct {
	TaskID string `json:"task_id" jsonschema:"the task's id, e.g. F1.T3"`
	Status string `json:"status" jsonschema:"the new status: backlog, todo, in-progress, done or blocked"`
	Note   string `json:"note,omitempty" jsonschema:"what you did, or why — recorded in the task's comment trail"`
	Author string `json:"author,omitempty" jsonschema:"who is reporting; defaults to orch. Agents pass their model name."`
}

func (s *server) setStatus(ctx context.Context, _ *mcpsdk.CallToolRequest, in setStatusIn) (*mcpsdk.CallToolResult, setStatusOut, error) {
	return s.move(ctx, in.TaskID, in.Status, in.Note, in.Author)
}

type blockIn struct {
	TaskID string `json:"task_id" jsonschema:"the task's id, e.g. F1.T3"`
	Reason string `json:"reason" jsonschema:"why you cannot finish — recorded in the task's comment trail and sent to the project's notification channels"`
	Author string `json:"author,omitempty" jsonschema:"who is reporting; defaults to orch. Agents pass their model name."`
}

// block is orch_set_status with the status fixed and the note renamed.
//
// Not sugar for its own sake: `scripts/task-block.sh` is a separate script
// for the same reason, and the rename is load-bearing. `note` is optional
// everywhere else; a block with no reason is a task nobody can unblock, so
// here the field is required and the tool says so in its schema.
func (s *server) block(ctx context.Context, _ *mcpsdk.CallToolRequest, in blockIn) (*mcpsdk.CallToolResult, setStatusOut, error) {
	if strings.TrimSpace(in.Reason) == "" {
		return refuse(setStatusOut{
			TaskID: in.TaskID,
			To:     string(model.StatusBlocked),
			Error: &transitionError{
				Code:    codeMissingRequiredArg,
				Message: "reason is required: a block with no reason is a task nobody can pick up",
			},
		})
	}
	return s.move(ctx, in.TaskID, string(model.StatusBlocked), in.Reason, in.Author)
}

// move is the shared body of both writing tools.
func (s *server) move(ctx context.Context, taskID, rawStatus, note, author string) (*mcpsdk.CallToolResult, setStatusOut, error) {
	if strings.TrimSpace(taskID) == "" {
		return refuse(setStatusOut{
			To: rawStatus,
			Error: &transitionError{
				Code:    codeMissingRequiredArg,
				Message: "task_id is required",
			},
		})
	}

	status, err := model.ParseStatus(rawStatus)
	if err != nil {
		return refuse(setStatusOut{
			TaskID: taskID,
			To:     rawStatus,
			// The list of statuses that exist goes in the message
			// rather than in ValidTransitions: nothing was looked up,
			// so there is no "from" for that field to be relative to,
			// and a list meaning two different things depending on the
			// code is how a client ends up offering `done` from
			// `backlog`. ParseStatus's own message already enumerates
			// them.
			Error: &transitionError{
				Code:    codeInvalidStatus,
				Message: err.Error(),
			},
		})
	}

	// The current status is read before the write so a successful result can
	// report where the task came from, and so a refusal can name the legal
	// destinations. `state.Transition` re-reads it inside its own
	// transaction — this read is for the report, never for the decision, so
	// a concurrent move between the two cannot let through anything the
	// table forbids.
	from, err := s.currentStatus(ctx, taskID)
	switch {
	case errors.Is(err, state.ErrTaskNotFound):
		return refuse(setStatusOut{
			TaskID: taskID,
			To:     string(status),
			Error: &transitionError{
				Code:    codeUnknownTask,
				Message: fmt.Sprintf("unknown task id %q", taskID),
			},
		})
	case err != nil:
		return nil, setStatusOut{}, fmt.Errorf("read the current status of %q: %w", taskID, err)
	}

	if author == "" {
		author = defaultAuthor
	}
	err = s.opts.Backend.Transition(ctx, taskID, status, state.Note{Author: author, Body: note})
	switch {
	case err == nil:
		return nil, setStatusOut{
			OK:     true,
			TaskID: taskID,
			From:   string(from.Status),
			To:     string(status),
		}, nil

	case errors.Is(err, state.ErrIllegalTransition):
		return refuse(setStatusOut{
			TaskID: taskID,
			From:   string(from.Status),
			To:     string(status),
			Error: &transitionError{
				Code: codeIllegalTransition,
				Message: fmt.Sprintf("%s cannot move from %s to %s",
					taskID, from.Status, status),
				From:             string(from.Status),
				ValidTransitions: transitionNames(from.Status),
			},
		})

	case errors.Is(err, state.ErrTaskNotFound):
		// Reachable only if the row vanished between the read above and the
		// write — reported rather than folded into the generic error, so the
		// agent gets the same actionable code it would have got a moment
		// earlier.
		return refuse(setStatusOut{
			TaskID: taskID,
			To:     string(status),
			Error: &transitionError{
				Code:    codeUnknownTask,
				Message: fmt.Sprintf("unknown task id %q", taskID),
			},
		})

	default:
		return nil, setStatusOut{}, fmt.Errorf("move %q to %s: %w", taskID, status, err)
	}
}

// refuse packs a structured failure into a tool result.
//
// IsError is set so a client sees a failed call in the envelope, and the
// text content repeats the message so a model reading only the content — the
// common case — is not left with an empty error. The structured payload
// still rides along in `structuredContent`.
func refuse(out setStatusOut) (*mcpsdk.CallToolResult, setStatusOut, error) {
	text := "the write was refused"
	if out.Error != nil {
		text = out.Error.Message
		if len(out.Error.ValidTransitions) > 0 {
			text += " (valid from here: " + strings.Join(out.Error.ValidTransitions, ", ") + ")"
		}
	}
	return &mcpsdk.CallToolResult{
		IsError: true,
		Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: text}},
	}, out, nil
}

// currentStatus reads a task's runtime row, seeding it first if tasks.json
// has the task and the database does not.
//
// `orch task-status` calls Bootstrap on every invocation, so a task added to
// tasks.json since the last run is writable the moment the script runs. This
// server is long-lived, so bootstrapping only at startup would make the same
// task permanently unknown to an agent that was dispatched for it. The seed
// happens on the miss path only: the common case is one read of an existing
// row, and Bootstrap never overwrites a runtime status anyway.
func (s *server) currentStatus(ctx context.Context, taskID string) (state.TaskRuntime, error) {
	rt, err := s.opts.Backend.Task(ctx, taskID)
	if !errors.Is(err, state.ErrTaskNotFound) {
		return rt, err
	}

	tasks, loadErr := project.Load(ctx, s.opts.Backend, s.opts.TasksJSON)
	if loadErr != nil {
		// A tasks.json that cannot be read is not this call's problem to
		// report — the task is still unknown, which is the answer the
		// caller needs, and the original error is the accurate one.
		return state.TaskRuntime{}, err
	}
	if _, ok := findTask(tasks, taskID); !ok {
		return state.TaskRuntime{}, err
	}
	if bootErr := s.opts.Backend.Bootstrap(ctx, tasks); bootErr != nil {
		return state.TaskRuntime{}, fmt.Errorf("seed the project from %s: %w", s.opts.TasksJSON, bootErr)
	}
	return s.opts.Backend.Task(ctx, taskID)
}
