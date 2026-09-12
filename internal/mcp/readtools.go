package mcp

import (
	"context"
	"fmt"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hectorcanaimero/orch/internal/budget"
	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/prompt"
	"github.com/hectorcanaimero/orch/internal/state"
)

// ---- orch_budget -----------------------------------------------------------

type budgetIn struct{}

type budgetOut struct {
	// Enabled is false when the project has no budgets.yaml, or the preset
	// it names has no providers. The map is empty in that case rather than
	// absent, so a caller can iterate it without a nil check.
	Enabled bool `json:"enabled"`
	// Providers is keyed by provider name. The row shape is the dashboard's
	// own (`/api/budgets`), unchanged — one spelling of a provider's window
	// across every surface.
	Providers map[string]budget.ProviderSnapshot `json:"providers"`
}

func (s *server) budget(ctx context.Context, _ *mcpsdk.CallToolRequest, _ budgetIn) (*mcpsdk.CallToolResult, budgetOut, error) {
	out := budgetOut{Providers: map[string]budget.ProviderSnapshot{}}
	if s.opts.Budget == nil || s.opts.Budget.Disabled() {
		return nil, out, nil
	}
	providers, err := s.opts.Budget.Snapshot(ctx)
	if err != nil {
		return nil, budgetOut{}, fmt.Errorf("read the budget window: %w", err)
	}
	out.Enabled = true
	out.Providers = providers
	return nil, out, nil
}

// ---- orch_events -----------------------------------------------------------

// eventOut is one row of the event log. The field names are the ones
// `orch events --json` prints, so an agent and an operator reading the same
// log see the same keys.
type eventOut struct {
	ID        int64          `json:"id"`
	EventType string         `json:"event_type"`
	TaskID    string         `json:"task_id"`
	Backend   string         `json:"backend"`
	TS        string         `json:"ts"`
	RunID     string         `json:"run_id"`
	Extra     map[string]any `json:"extra"`
}

type eventsIn struct {
	TaskID string `json:"task_id,omitempty" jsonschema:"only this task's events; omit for every task"`
	Limit  int    `json:"limit,omitempty" jsonschema:"the newest N events, oldest first; 0 means every event (default 20)"`
}

type eventsOut struct {
	Events []eventOut `json:"events"`
	Count  int        `json:"count"`
}

// defaultEventLimit matches `orch events --tail`'s own default. An agent that
// asks for "the events" wants the recent ones, and a project with months of
// history would otherwise return a payload nothing can read.
const defaultEventLimit = 20

func (s *server) events(ctx context.Context, _ *mcpsdk.CallToolRequest, in eventsIn) (*mcpsdk.CallToolResult, eventsOut, error) {
	limit := in.Limit
	if limit == 0 {
		limit = defaultEventLimit
	}
	// A negative limit means the same as zero to the backend ("every
	// event"), which is not what a caller who typed -1 meant. It is
	// normalised here so the two spellings cannot mean opposite things.
	if limit < 0 {
		limit = defaultEventLimit
	}

	var (
		evs []state.Event
		err error
	)
	if in.TaskID != "" {
		evs, err = s.opts.Backend.Events(ctx, in.TaskID, limit)
	} else {
		evs, err = s.opts.Backend.AllEvents(ctx, limit)
	}
	if err != nil {
		return nil, eventsOut{}, fmt.Errorf("read events: %w", err)
	}

	out := eventsOut{Events: make([]eventOut, 0, len(evs))}
	for _, e := range evs {
		extra := e.Extra
		if extra == nil {
			// Python's `json.loads(row or "{}")` is never None, and a null
			// here would make an agent branch on a case that cannot happen.
			extra = map[string]any{}
		}
		out.Events = append(out.Events, eventOut{
			ID: e.ID, EventType: e.EventType, TaskID: e.TaskID,
			Backend: e.Backend, TS: e.TS, RunID: e.RunID, Extra: extra,
		})
	}
	out.Count = len(out.Events)
	return nil, out, nil
}

// ---- orch_context ----------------------------------------------------------

type contextIn struct {
	TaskID string `json:"task_id,omitempty" jsonschema:"include this task's full dispatch context; omit for the project only"`
}

type projectContext struct {
	ID   string `json:"id"`
	Root string `json:"root"`
	// SpecRoot is what a task's spec_ref is relative to.
	SpecRoot string `json:"spec_root"`
	// Counts is tasks per status, keyed by status name. Every status that
	// exists is present, zero included — an agent comparing "how many are
	// blocked" against a missing key would read it as none.
	Counts map[string]int `json:"counts"`
	Total  int            `json:"total"`
}

// depContext is one finished dependency: the id, the title, and the last
// thing it said — the same three the dispatch prompt's "Completed
// dependencies (context)" block carries.
type depContext struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	LastComment string `json:"last_comment,omitempty"`
}

type taskContextOut struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Status      string   `json:"status"`
	Model       string   `json:"model"`
	Files       []string `json:"files"`
	// SpecRefPath is spec_root joined to the task's spec_ref — the path the
	// prompt's "Spec ref (READ FIRST)" line names. Empty when the task
	// declares none, which is the case the prompt warns about.
	SpecRefPath string `json:"spec_ref_path,omitempty"`
	// Dependencies are the task's completed dependencies, with what each
	// one reported — the same sentence, chosen the same way, that the
	// dispatch prompt renders (prompt.AgentComment). This is how an agent
	// re-reads them without its prompt in front of it.
	//
	// Untruncated, unlike the prompt's copy: a prompt is a fixed budget and
	// caps each note at 500 characters, but a tool result is fetched on
	// demand and orch_get_task returns the whole trail anyway.
	Dependencies []depContext `json:"dependencies"`
	// PendingDependencies are the dependency ids that are not done yet —
	// the reason a task is not ready, named rather than implied.
	PendingDependencies []string `json:"pending_dependencies"`
	LegalTransitions    []string `json:"legal_transitions"`
}

type contextOut struct {
	Project projectContext  `json:"project"`
	Task    *taskContextOut `json:"task,omitempty"`
}

func (s *server) taskContext(ctx context.Context, _ *mcpsdk.CallToolRequest, in contextIn) (*mcpsdk.CallToolResult, contextOut, error) {
	tasks, runtime, err := s.load(ctx)
	if err != nil {
		return nil, contextOut{}, err
	}

	specRoot := s.opts.SpecRoot
	if specRoot == "" {
		specRoot = prompt.DefaultSpecRoot
	}

	counts := map[string]int{}
	for _, st := range []model.Status{
		model.StatusBacklog, model.StatusTodo, model.StatusInProgress,
		model.StatusDone, model.StatusBlocked,
	} {
		counts[string(st)] = 0
	}
	for _, t := range tasks {
		counts[string(t.Status)]++
	}

	out := contextOut{Project: projectContext{
		ID:       s.opts.ProjectID,
		Root:     s.opts.ProjectRoot,
		SpecRoot: specRoot,
		Counts:   counts,
		Total:    len(tasks),
	}}

	if in.TaskID == "" {
		return nil, out, nil
	}

	t, ok := findTask(tasks, in.TaskID)
	if !ok {
		return nil, contextOut{}, fmt.Errorf("unknown task id %q", in.TaskID)
	}

	byID := make(map[string]model.Task, len(tasks))
	for _, other := range tasks {
		byID[other.ID] = other
	}

	tc := taskContextOut{
		ID:                  t.ID,
		Title:               t.Title,
		Description:         t.Description,
		Status:              string(t.Status),
		Model:               t.Model,
		Files:               nonNil(t.Files),
		Dependencies:        []depContext{},
		PendingDependencies: []string{},
		LegalTransitions:    transitionNames(t.Status),
	}
	if t.SpecRef != "" {
		tc.SpecRefPath = specRoot + "/" + t.SpecRef
	}
	for _, id := range t.Dependencies {
		dep, known := byID[id]
		// A dependency naming a task nobody defined counts as not done —
		// the same reading `graph.Parallelizable` takes, and the safe one.
		if !known || dep.Status != model.StatusDone {
			tc.PendingDependencies = append(tc.PendingDependencies, id)
			continue
		}
		tc.Dependencies = append(tc.Dependencies, depContext{
			ID:          dep.ID,
			Title:       dep.Title,
			LastComment: prompt.AgentComment(runtime[dep.ID].Comments),
		})
	}

	out.Task = &tc
	return nil, out, nil
}
