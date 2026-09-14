package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hectorcanaimero/orch/internal/graph"
	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/project"
	"github.com/hectorcanaimero/orch/internal/state"
)

// taskSummary is one row of orch_list_tasks.
//
// The DAG's shape (title, phase, dependencies) and the database's runtime
// (status, attempts, PR) are joined here rather than left to the caller,
// because an agent asking "what is ready?" needs both halves and has no way
// to join them itself.
type taskSummary struct {
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	Phase        int      `json:"phase"`
	Status       string   `json:"status"`
	Model        string   `json:"model"`
	Dependencies []string `json:"dependencies"`
	Attempts     int      `json:"attempts"`
	UpdatedAt    string   `json:"updated_at,omitempty"`
	PRURL        string   `json:"pr_url,omitempty"`
	CIStatus     string   `json:"ci_status,omitempty"`
}

type listTasksIn struct {
	Status []string `json:"status,omitempty" jsonschema:"only these statuses (backlog, todo, in-progress, done, blocked)"`
	IDs    []string `json:"ids,omitempty" jsonschema:"only these task ids"`

	Milestone string `json:"milestone,omitempty" jsonschema:"only tasks in this milestone id"`
	Ready     bool   `json:"ready,omitempty" jsonschema:"only tasks that could be dispatched now: backlog or todo with every dependency done"`
	Limit     int    `json:"limit,omitempty" jsonschema:"at most this many rows; 0 means every match"`
}

type listTasksOut struct {
	Tasks []taskSummary `json:"tasks"`
	// Count is len(Tasks) — after Limit, so a caller can tell a truncated
	// page from a complete one by comparing it against Total.
	Count int `json:"count"`
	// Total is how many tasks matched before Limit was applied.
	Total int `json:"total"`
}

func (s *server) listTasks(ctx context.Context, _ *mcpsdk.CallToolRequest, in listTasksIn) (*mcpsdk.CallToolResult, listTasksOut, error) {
	tasks, runtime, err := s.load(ctx)
	if err != nil {
		return nil, listTasksOut{}, err
	}

	// Ready is computed from the hydrated set, not from tasks.json's own
	// status field — which F-12 froze at whatever it was when the file was
	// written. `graph.Ready` is the dispatch loop's notion (todo, dependencies
	// done), so "ready" here means the same thing it means in a run (#235).
	var ready map[string]bool
	if in.Ready {
		ready = map[string]bool{}
		for _, t := range graph.Ready(tasks) {
			ready[t.ID] = true
		}
	}

	wantStatus, err := statusSet(in.Status)
	if err != nil {
		return nil, listTasksOut{}, err
	}
	wantID := stringSet(in.IDs)

	out := listTasksOut{Tasks: []taskSummary{}}
	for _, t := range tasks {
		rt := runtime[t.ID]
		if len(wantStatus) > 0 && !wantStatus[t.Status] {
			continue
		}
		if len(wantID) > 0 && !wantID[t.ID] {
			continue
		}
		if in.Milestone != "" && rt.MilestoneID != in.Milestone {
			continue
		}
		if ready != nil && !ready[t.ID] {
			continue
		}
		out.Total++
		if in.Limit > 0 && len(out.Tasks) >= in.Limit {
			continue
		}
		out.Tasks = append(out.Tasks, summarize(t, runtime))
	}
	out.Count = len(out.Tasks)
	return nil, out, nil
}

type getTaskIn struct {
	TaskID string `json:"task_id" jsonschema:"the task's id, e.g. F1.T3"`
}

type getTaskOut struct {
	taskSummary
	Description   string   `json:"description"`
	Files         []string `json:"files"`
	SpecRef       string   `json:"spec_ref"`
	EstimateHours float64  `json:"estimate_hours"`
	StartedAt     string   `json:"started_at,omitempty"`
	FinishedAt    string   `json:"finished_at,omitempty"`
	LastModel     string   `json:"last_model,omitempty"`
	LastBackend   string   `json:"last_backend,omitempty"`
	CIAttempts    int      `json:"ci_attempts"`
	// Comments is the task's trail, each entry as the database holds it.
	// It comes from the runtime row, NOT from tasks.json: `Transition`
	// appends to `tasks_runtime.comments_json`, and `project.Hydrate`
	// overlays only `Status`, so the file's copy is whatever it was when
	// the file was last written. Reading it from the file returned an empty
	// trail for a task that had just reported a summary.
	//
	// Python types a comment `list[dict]` with no fixed shape, so each
	// entry crosses this wire as it was written rather than flattened into
	// something this package has decided it understands.
	Comments []any `json:"comments"`
	// LegalTransitions is every status this task may move to next. It is
	// here so an agent can ask once instead of discovering the table by
	// being refused — the same list orch_set_status returns on a refusal.
	LegalTransitions []string `json:"legal_transitions"`
}

func (s *server) getTask(ctx context.Context, _ *mcpsdk.CallToolRequest, in getTaskIn) (*mcpsdk.CallToolResult, getTaskOut, error) {
	if strings.TrimSpace(in.TaskID) == "" {
		return nil, getTaskOut{}, fmt.Errorf("task_id is required")
	}
	tasks, runtime, err := s.load(ctx)
	if err != nil {
		return nil, getTaskOut{}, err
	}
	t, ok := findTask(tasks, in.TaskID)
	if !ok {
		return nil, getTaskOut{}, fmt.Errorf("unknown task id %q", in.TaskID)
	}
	rt := runtime[t.ID]

	out := getTaskOut{
		taskSummary:      summarize(t, runtime),
		Description:      t.Description,
		Files:            nonNil(t.Files),
		SpecRef:          t.SpecRef,
		EstimateHours:    t.EstimateHours,
		StartedAt:        rt.StartedAt,
		FinishedAt:       rt.FinishedAt,
		LastModel:        rt.LastModel,
		LastBackend:      rt.LastBackend,
		CIAttempts:       rt.CIAttempts,
		Comments:         decodeComments(rt.Comments),
		LegalTransitions: transitionNames(t.Status),
	}
	return nil, out, nil
}

// load reads the DAG from tasks.json and overlays the live status, returning
// the hydrated tasks plus the raw runtime rows by id.
//
// Both halves are needed: `project.Hydrate` overwrites `Status` and nothing
// else, so attempts, the PR URL and the milestone still have to come from the
// rows themselves.
func (s *server) load(ctx context.Context) ([]model.Task, map[string]state.TaskRuntime, error) {
	tasks, err := project.Load(ctx, s.opts.Backend, s.opts.TasksJSON)
	if err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", s.opts.TasksJSON, err)
	}
	rows, err := s.opts.Backend.Tasks(ctx, state.TaskFilter{})
	if err != nil {
		return nil, nil, fmt.Errorf("read task runtime: %w", err)
	}
	byID := make(map[string]state.TaskRuntime, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
	}
	return tasks, byID, nil
}

func summarize(t model.Task, runtime map[string]state.TaskRuntime) taskSummary {
	rt := runtime[t.ID]
	return taskSummary{
		ID:           t.ID,
		Title:        t.Title,
		Phase:        t.Phase,
		Status:       string(t.Status),
		Model:        t.Model,
		Dependencies: nonNil(t.Dependencies),
		Attempts:     rt.Attempts,
		UpdatedAt:    rt.UpdatedAt,
		PRURL:        rt.PRURL,
		CIStatus:     rt.CIStatus,
	}
}

func findTask(tasks []model.Task, id string) (model.Task, bool) {
	for _, t := range tasks {
		if t.ID == id {
			return t, true
		}
	}
	return model.Task{}, false
}

// statusSet validates and indexes the caller's status filter. An unknown
// status is an error rather than a filter that silently matches nothing —
// a model that typed "in_progress" needs to be told, not handed an empty list.
func statusSet(names []string) (map[model.Status]bool, error) {
	if len(names) == 0 {
		return nil, nil
	}
	out := make(map[model.Status]bool, len(names))
	for _, n := range names {
		st, err := model.ParseStatus(n)
		if err != nil {
			return nil, fmt.Errorf("status filter: %w", err)
		}
		out[st] = true
	}
	return out, nil
}

func stringSet(v []string) map[string]bool {
	if len(v) == 0 {
		return nil
	}
	out := make(map[string]bool, len(v))
	for _, s := range v {
		out[s] = true
	}
	return out
}

func transitionNames(from model.Status) []string {
	legal := model.Transitions(from)
	out := make([]string, 0, len(legal))
	for _, s := range legal {
		out = append(out, string(s))
	}
	return out
}

// decodeComments turns the raw comment entries into plain JSON values.
//
// json.RawMessage would infer a schema of "string" — it is a []byte — so the
// entries are decoded once here instead of shipping the task's trail as a
// list of escaped JSON strings. An entry that does not parse is dropped
// rather than failing the call: a comment nobody can read must not make the
// task unreadable.
func decodeComments(raw []json.RawMessage) []any {
	out := make([]any, 0, len(raw))
	for _, r := range raw {
		var v any
		if err := json.Unmarshal(r, &v); err != nil {
			continue
		}
		out = append(out, v)
	}
	return out
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}
