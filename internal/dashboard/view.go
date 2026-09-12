package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/hectorcanaimero/orch/internal/graph"
	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/project"
)

// projectView is what every task-shaped endpoint starts from: the DAG as
// tasks.json declares it, the statuses the database actually holds, and the
// figures derived from both. Ported from `_load_project_view`.
//
// Built per request. Python caches it on the app for the life of a page load;
// here every handler is its own request and the cost is two queries, so a
// cache would buy the time back and owe a whole invalidation problem. If a
// profile ever says otherwise, the place to put one is here, not in each
// handler.
// errNoBackend is what a read endpoint hits when the server was built without
// a state backend. Possible only in a test or a caller that skipped Options —
// New takes State as optional because (a) shipped before any endpoint needed
// it — so it is an error rather than an empty payload, which would read as
// "this project has no tasks".
var errNoBackend = errors.New("dashboard: no state backend")

// errNoTunnelManager is `tunnel.enabled: true` with no manager wired — a
// wiring bug rather than a config state, which is why it is a 500 and not the
// 404 a disabled tunnel gets.
var errNoTunnelManager = errors.New("dashboard: tunnel enabled but no manager")

type projectView struct {
	Tasks            []model.Task
	Summary          graph.Summary
	Parallelizable   map[string]bool
	DownstreamImpact map[string]int
	CriticalPath     map[string]bool
	HumanHours       map[string]float64
	LastUpdated      map[string]string
}

func (s *Server) loadView(ctx context.Context) (projectView, error) {
	var v projectView
	if s.state == nil {
		return v, errNoBackend
	}

	f, err := model.LoadTasksFile(s.paths.TasksJSON())
	if err != nil {
		return v, fmt.Errorf("reading %s: %w", s.paths.TasksJSON(), err)
	}
	tasks, err := project.Hydrate(ctx, s.state, f.Tasks)
	if err != nil {
		return v, err
	}
	events, err := s.state.AllEvents(ctx, 0)
	if err != nil {
		return v, fmt.Errorf("reading the event log: %w", err)
	}

	v.Tasks = tasks
	v.Summary = graph.Summarize(tasks)
	v.DownstreamImpact = graph.DownstreamImpact(tasks)
	v.CriticalPath = graph.CriticalPath(tasks)
	v.HumanHours = project.HumanHoursByTask(events)
	v.LastUpdated = project.LastUpdatedByTask(events)

	v.Parallelizable = map[string]bool{}
	for _, t := range graph.Parallelizable(tasks) {
		v.Parallelizable[t.ID] = true
	}
	return v, nil
}

// taskPayload is one task as the SPA reads it. Field order is Python's
// `_task_to_dict`, which is also the order its `json.dumps` emits.
type taskPayload struct {
	ID            string            `json:"id"`
	Phase         int               `json:"phase"`
	Title         string            `json:"title"`
	Description   string            `json:"description"`
	Model         string            `json:"model"`
	Reason        string            `json:"reason"`
	Status        string            `json:"status"`
	Dependencies  []string          `json:"dependencies"`
	DepCount      int               `json:"dep_count"`
	EstimateHours float64           `json:"estimate_hours"`
	Files         []string          `json:"files"`
	SpecRef       string            `json:"spec_ref"`
	Comments      []json.RawMessage `json:"comments"`
	HumanHours    float64           `json:"human_hours"`
	LastUpdated   string            `json:"last_updated"`
	Downstream    int               `json:"downstream_impact"`
	OnCritical    bool              `json:"on_critical_path"`
	// Parallelizable is only present on the single-task endpoint, which is
	// where Python adds it.
	Parallelizable *bool `json:"parallelizable,omitempty"`
}

func (v projectView) task(t model.Task) taskPayload {
	return taskPayload{
		ID:            t.ID,
		Phase:         t.Phase,
		Title:         t.Title,
		Description:   t.Description,
		Model:         t.Model,
		Reason:        t.Reason,
		Status:        string(t.Status),
		Dependencies:  nonNilStrings(t.Dependencies),
		DepCount:      len(t.Dependencies),
		EstimateHours: t.EstimateHours,
		Files:         nonNilStrings(t.Files),
		SpecRef:       t.SpecRef,
		Comments:      rawComments(t),
		HumanHours:    v.HumanHours[t.ID],
		LastUpdated:   v.LastUpdated[t.ID],
		Downstream:    v.DownstreamImpact[t.ID],
		OnCritical:    v.CriticalPath[t.ID],
	}
}

// summaryPayload is the header bar. Both floats are rounded to one decimal,
// like `ProjectSummary.as_dict`.
type summaryPayload struct {
	Total              int     `json:"total"`
	Done               int     `json:"done"`
	InProgress         int     `json:"in_progress"`
	Blocked            int     `json:"blocked"`
	Backlog            int     `json:"backlog"`
	PercentDone        float64 `json:"percent_done"`
	EstimateHoursTotal float64 `json:"estimate_hours_total"`
}

func toSummaryPayload(s graph.Summary) summaryPayload {
	return summaryPayload{
		Total:              s.Total,
		Done:               s.Done,
		InProgress:         s.InProgress,
		Blocked:            s.Blocked,
		Backlog:            s.Backlog,
		PercentDone:        round1(s.PercentDone),
		EstimateHoursTotal: round1(s.EstimateHoursTotal),
	}
}

// round1 is Python's `round(x, 1)`.
func round1(v float64) float64 { return roundDecimals(v, 1) }

// roundDecimals is Python's `round(x, n)` — correctly rounded, half to even,
// on the exact binary value.
//
// Not `math.Round(x*10^n)/10^n`: that rounds half AWAY from zero, and rounds
// an already-scaled value, so it disagrees with CPython twice over. Formatting
// to n decimals and reading back is what CPython's round actually does.
func roundDecimals(v float64, n int) float64 {
	f, err := strconv.ParseFloat(strconv.FormatFloat(v, 'f', n, 64), 64)
	if err != nil {
		return v
	}
	return f
}

// nonNilStrings turns a nil slice into an empty one so the JSON says `[]`
// rather than `null`. Python's lists are never nil and the SPA maps over
// these without checking.
func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// rawComments passes each comment through as the JSON it already is.
// orchestrator/models.py types them `list[dict]` with no fixed shape, so
// understanding them here would only be a way to lose fields.
func rawComments(t model.Task) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(t.Comments))
	out = append(out, t.Comments...)
	return out
}

// filterTasks applies the query-parameter filters in memory, in Python's
// order. Ported from `_apply_filters`, minus the four parameters no endpoint
// in this phase passes (has_comments, has_spec, blocked_since_days and the
// multi-phase form) — they arrive with the endpoint that needs them rather
// than as dead parameters nothing can reach.
type taskFilters struct {
	Phase          *int
	Status         string
	Model          string
	Q              string
	Parallelizable bool
}

func (v projectView) filter(f taskFilters) []model.Task {
	out := make([]model.Task, 0, len(v.Tasks))
	for _, t := range v.Tasks {
		if f.Phase != nil && t.Phase != *f.Phase {
			continue
		}
		if f.Status != "" && string(t.Status) != f.Status {
			continue
		}
		if f.Model != "" && t.Model != f.Model {
			continue
		}
		if f.Q != "" && !matchesQuery(t, f.Q) {
			continue
		}
		if f.Parallelizable && !v.Parallelizable[t.ID] {
			continue
		}
		out = append(out, t)
	}
	return out
}

// matchesQuery is Python's case-insensitive substring over id, title and
// description. Note what it does NOT search: the model, the spec ref, the
// files. Widening it here would make the Go dashboard answer a search the
// Python one does not.
func matchesQuery(t model.Task, q string) bool {
	q = strings.ToLower(q)
	return strings.Contains(strings.ToLower(t.ID), q) ||
		strings.Contains(strings.ToLower(t.Title), q) ||
		strings.Contains(strings.ToLower(t.Description), q)
}

// graphPayload is the DAG as the Graph page draws it.
type graphPayload struct {
	Nodes []graphNode `json:"nodes"`
	Edges []graphEdge `json:"edges"`
}

type graphNode struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Status string `json:"status"`
	Phase  int    `json:"phase"`
	OnPath bool   `json:"on_critical_path"`
}

type graphEdge struct {
	Source string `json:"source"`
	Target string `json:"target"`
}

func (v projectView) graph() graphPayload {
	out := graphPayload{
		Nodes: make([]graphNode, 0, len(v.Tasks)),
		Edges: []graphEdge{},
	}
	for _, t := range v.Tasks {
		label := t.Title
		if label == "" {
			label = t.ID
		}
		out.Nodes = append(out.Nodes, graphNode{
			ID: t.ID, Label: label, Status: string(t.Status),
			Phase: t.Phase, OnPath: v.CriticalPath[t.ID],
		})
		for _, dep := range t.Dependencies {
			// Direction is dep → task: the edge points at what waits.
			out.Edges = append(out.Edges, graphEdge{Source: dep, Target: t.ID})
		}
	}
	return out
}
