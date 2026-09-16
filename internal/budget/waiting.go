package budget

import (
	"time"

	"github.com/hectorcanaimero/orch/internal/state"
)

// Waiting is a task the gate deferred: which provider it is waiting on, when
// that window is estimated to reset, and the gate's own reason.
type Waiting struct {
	TaskID   string `json:"task_id"`
	Provider string `json:"provider"`
	ResetAt  string `json:"reset_at"`
	Reason   string `json:"reason"`
}

// WaitingOn reads a task's newest event back into a deferral.
//
// The scheduler writes `budget_skip` when a ready task starts waiting on a
// capped provider. The task is still waiting while that is its newest event
// and the reset estimate is ahead of `now`: a dispatch replaces it as the
// newest event, and a reset in the past means the window has freed — which
// also keeps a run that died mid-wait from showing "waiting" forever. The
// caller checks the task is still todo.
func WaitingOn(last state.Event, now time.Time) (Waiting, bool) {
	if last.EventType != "budget_skip" {
		return Waiting{}, false
	}
	resetAt, _ := last.Extra["reset_at"].(string)
	at, err := time.Parse(time.RFC3339, resetAt)
	if err != nil || !at.After(now) {
		return Waiting{}, false
	}
	reason, _ := last.Extra["reason"].(string)
	return Waiting{TaskID: last.TaskID, Provider: last.Backend, ResetAt: resetAt, Reason: reason}, true
}
