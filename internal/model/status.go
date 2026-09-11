package model

import "fmt"

// Status is a task's position in the workflow. The zero value ("") is not
// a valid Status — build one with ParseStatus or use a constant below.
type Status string

const (
	StatusBacklog    Status = "backlog"
	StatusTodo       Status = "todo"
	StatusInProgress Status = "in-progress"
	StatusDone       Status = "done"
	StatusBlocked    Status = "blocked"
)

// allStatuses fixes the canonical order used by Transitions' output and by
// ParseStatus's error message. It mirrors the five-value `Status` literal
// in orchestrator/models.py — there is no "skipped" task status (that name
// is used only for CI-run status, a separate concept, in the Python state
// backend).
var allStatuses = [...]Status{StatusBacklog, StatusTodo, StatusInProgress, StatusDone, StatusBlocked}

// ParseStatus validates s against the five known statuses.
func ParseStatus(s string) (Status, error) {
	st := Status(s)
	for _, known := range allStatuses {
		if st == known {
			return st, nil
		}
	}
	return "", fmt.Errorf("model: invalid status %q (want one of %v)", s, allStatuses)
}

// statusTransitions is ported byte-for-byte from `_STATUS_TRANSITIONS` in
// orchestrator/state/sqlite_backend.py (as of the F-13 hygiene pass —
// generated into internal/model/testdata/transitions.json, which
// status_test.go checks this table against). Same-status writes are
// always legal (idempotent). `backlog` can move directly to any other
// status — the manual `orch task set` shortcuts for work that happened
// outside orch. `blocked` can resolve straight to `done`. `done` can only
// reopen to `todo` (a manual reset), never jump straight back to
// `in-progress` or `blocked`.
var statusTransitions = map[Status]map[Status]bool{
	StatusBacklog: {
		StatusTodo: true, StatusInProgress: true, StatusBlocked: true,
		StatusDone: true, StatusBacklog: true,
	},
	StatusTodo: {
		StatusInProgress: true, StatusBlocked: true, StatusTodo: true,
		StatusDone: true,
	},
	StatusInProgress: {
		StatusDone: true, StatusBlocked: true, StatusTodo: true,
		StatusInProgress: true,
	},
	StatusBlocked: {
		StatusTodo: true, StatusInProgress: true, StatusDone: true,
		StatusBlocked: true,
	},
	StatusDone: {
		StatusTodo: true, StatusDone: true,
	},
}

// CanTransition reports whether moving a task from `from` to `to` is a
// legal transition.
func CanTransition(from, to Status) bool {
	return statusTransitions[from][to]
}

// Transitions returns every status `from` may legally move to next, in
// the canonical status order — used by CLI error messages and `--help`
// text so the list is stable across runs.
func Transitions(from Status) []Status {
	out := make([]Status, 0, len(allStatuses))
	for _, to := range allStatuses {
		if statusTransitions[from][to] {
			out = append(out, to)
		}
	}
	return out
}
