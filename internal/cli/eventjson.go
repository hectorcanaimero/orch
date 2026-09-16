package cli

import (
	"context"
	"fmt"

	"github.com/hectorcanaimero/orch/internal/state"
)

// eventJSON is one event the way Python's `iter_events`
// (orchestrator/state/sqlite_backend.py) renders a row: field names and
// order match exactly. `project_id` is included even though `state.Event`
// doesn't carry it (every Backend is already scoped to one project) — it's
// filled in by the caller from the resolved Paths.
//
// Shared by `orch status` (embedded as `last_event`) and `orch events`
// (the array `--json` prints), which is why it lives apart from either
// command's own file.
type eventJSON struct {
	ID        int64          `json:"id"`
	EventType string         `json:"event_type"`
	TaskID    string         `json:"task_id"`
	Backend   string         `json:"backend"`
	TS        string         `json:"ts"`
	Extra     map[string]any `json:"extra"`
	ProjectID string         `json:"project_id"`
	RunID     string         `json:"run_id"`
}

func toEventJSON(e state.Event, projectID string) eventJSON {
	extra := e.Extra
	if extra == nil {
		extra = map[string]any{} // Python's json.loads(... or "{}") is never nil
	}
	return eventJSON{
		ID: e.ID, EventType: e.EventType, TaskID: e.TaskID, Backend: e.Backend,
		TS: e.TS, Extra: extra, ProjectID: projectID, RunID: e.RunID,
	}
}

// lastEventFor returns a task's most recent event (nil if it has none) and
// the human-readable one-liner `_human_last_event`
// (orchestrator/observability.py) renders from it — "$type @ $ts", or just
// "$type" when there's no timestamp, matching Python exactly. The raw event
// rides along for readers that interpret it (the budget deferral).
func lastEventFor(ctx context.Context, backend state.Backend, taskID, projectID string) (*eventJSON, *string, state.Event, error) {
	evs, err := backend.Events(ctx, taskID, 1)
	if err != nil {
		return nil, nil, state.Event{}, fmt.Errorf("read last event for %q: %w", taskID, err)
	}
	if len(evs) == 0 {
		return nil, nil, state.Event{}, nil
	}
	last := evs[len(evs)-1]
	ej := toEventJSON(last, projectID)
	human := last.EventType
	if last.TS != "" {
		human = fmt.Sprintf("%s @ %s", last.EventType, last.TS)
	}
	return &ej, &human, last, nil
}
