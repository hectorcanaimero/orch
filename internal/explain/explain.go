// Package explain answers "where is this project and what can I do next" in
// one pass, and renders that answer as text for a person or as JSON for an
// agent.
//
// One gather, two renderings, on purpose. `orch explain` and the MCP server's
// `orch_context` tool exist to tell the same story to different readers, and
// the way that story drifts is by being assembled twice — the migration has
// already turned up a duplicated `dashboard:` block and two spellings of
// `burndown_by_day`, both the same shape of mistake.
//
// It does not live in internal/project: that package is deliberately the
// narrow "tasks.json plus the runtime row plus what the event log says" layer
// that `orch status` depends on, and pulling the budget guardrail into it
// would make every consumer of Hydrate depend on the gate. Imports project,
// graph, budget, model; imported by cli and mcp, never the other way.
package explain

import (
	"context"
	"fmt"

	"github.com/hectorcanaimero/orch/internal/budget"
	"github.com/hectorcanaimero/orch/internal/graph"
	"github.com/hectorcanaimero/orch/internal/model"
)

// BudgetReporter is the slice of *budget.Gate this needs.
//
// An interface rather than the concrete gate so a project with no budgets.yaml
// passes nil and gets "not configured", instead of this package having to
// build a disabled gate in order to ask. Same shape `internal/mcp` already
// uses for `orch_budget`.
type BudgetReporter interface {
	Disabled() bool
	Snapshot(ctx context.Context) (map[string]budget.ProviderSnapshot, error)
}

// Project is the frame: which project, where, and how its tasks stand.
//
// The JSON names are the ones `orch_context` already publishes. They are a
// contract with the agents reading that tool, not a fresh choice.
type Project struct {
	ID   string `json:"id"`
	Root string `json:"root"`
	// SpecRoot is what a task's spec_ref is relative to.
	SpecRoot string `json:"spec_root"`
	// Counts is tasks per status. EVERY status is present, zero included: an
	// agent comparing "how many are blocked" against a missing key would read
	// it as none.
	Counts map[string]int `json:"counts"`
	Total  int            `json:"total"`
}

// ReadyTask is one task that could be dispatched right now.
type ReadyTask struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Phase int    `json:"phase"`
	Model string `json:"model"`
	// EstimateHours is the plan's guess, not a measurement.
	EstimateHours float64 `json:"estimate_hours"`
}

// Budget is the rolling window per provider.
//
// Shape and spelling are `orch_budget`'s, which are `/api/budgets`'s, which
// are Python's — one spelling of a provider's window across every surface.
// `Providers` is an empty map rather than null when nothing is configured, so
// a caller iterates it without branching on a case that does not exist.
type Budget struct {
	Enabled   bool                               `json:"enabled"`
	Providers map[string]budget.ProviderSnapshot `json:"providers"`
}

// Overview is the whole answer.
type Overview struct {
	Project Project     `json:"project"`
	Ready   []ReadyTask `json:"ready"`
	Budget  Budget      `json:"budget"`
}

// Options is what Gather needs.
//
// Tasks arrive already loaded and hydrated, rather than being read here: the
// MCP server has them in hand by the time it asks, and reading them twice per
// call to keep this function self-contained would be paying for tidiness with
// the caller's round trips.
type Options struct {
	ProjectID   string
	ProjectRoot string
	SpecRoot    string
	Tasks       []model.Task
	// Budget may be nil, which means the project has no budgets.yaml.
	Budget BudgetReporter
}

// Gather assembles the overview.
func Gather(ctx context.Context, opts Options) (Overview, error) {
	out := Overview{
		Project: Project{
			ID:       opts.ProjectID,
			Root:     opts.ProjectRoot,
			SpecRoot: opts.SpecRoot,
			Counts:   countByStatus(opts.Tasks),
			Total:    len(opts.Tasks),
		},
		Ready:  readyTasks(opts.Tasks),
		Budget: Budget{Providers: map[string]budget.ProviderSnapshot{}},
	}

	if opts.Budget != nil && !opts.Budget.Disabled() {
		providers, err := opts.Budget.Snapshot(ctx)
		if err != nil {
			return Overview{}, fmt.Errorf("read the budget window: %w", err)
		}
		out.Budget.Enabled = true
		out.Budget.Providers = providers
	}
	return out, nil
}

// countByStatus seeds every known status at zero first. A status missing from
// the map reads as "none of those", and a caller cannot tell that from "this
// build does not know that status".
func countByStatus(tasks []model.Task) map[string]int {
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
	return counts
}

// readyTasks is `graph.Parallelizable`, not a second definition of "ready".
//
// That function is what the dispatch loop consults and what
// `orch_list_tasks{ready:true}` already publishes, so the word means the same
// thing in the prompt, in the tool and in the run. A narrower notion — say,
// "ready and not deferred by budget" — would be a different field with a
// different name, never a `ready` that disagrees with the published one.
func readyTasks(tasks []model.Task) []ReadyTask {
	ready := graph.Parallelizable(tasks)
	out := make([]ReadyTask, 0, len(ready))
	for _, t := range ready {
		out = append(out, ReadyTask{
			ID: t.ID, Title: t.Title, Phase: t.Phase,
			Model: t.Model, EstimateHours: t.EstimateHours,
		})
	}
	return out
}
