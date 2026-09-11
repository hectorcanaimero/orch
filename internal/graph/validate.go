package graph

import (
	"fmt"

	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/pyfmt"
)

// Validate runs every static check and returns the problems in the same order
// Python's `validate_graph` does: schema, then dependencies, then cycles, then
// routes.
//
// `routes` is the set of keys from model_router.yaml. Pass nil to skip route
// checking entirely — that is how Python's `router_keys=None` behaves, and it
// is what `orch validate` does before the router has been loaded.
//
// Deliberately NOT included, matching Python: `files.writable` and
// `preset.sanity`. Both do I/O, and a caller who wants them composes them
// explicitly rather than having a "static validation" call touch the disk.
func Validate(tasks []model.Task, routes []string) []Problem {
	var out []Problem
	out = append(out, validateSchema(tasks)...)
	out = append(out, validateDependencies(tasks)...)
	out = append(out, validateCycles(tasks)...)
	if routes != nil {
		out = append(out, validateRoutes(tasks, routes)...)
	}
	return out
}

// validateSchema checks the semantic constraints the loader cannot: a
// non-empty id, a non-negative phase, a non-empty model.
//
// A task with no id is reported and then SKIPPED — every later message is
// keyed by id, and a stream of findings all saying `task_id: ""` tells the
// reader nothing about which line to fix.
func validateSchema(tasks []model.Task) []Problem {
	var out []Problem
	for _, t := range tasks {
		if t.ID == "" {
			out = append(out, Problem{
				Field:    "id",
				Kind:     KindSchemaTasks,
				Message:  "task is missing a non-empty string id",
				Severity: SeverityError,
			})
			continue
		}
		if t.Phase < 0 {
			out = append(out, Problem{
				TaskID:   t.ID,
				Field:    "phase",
				Kind:     KindSchemaTasks,
				Message:  fmt.Sprintf("phase must be a non-negative int, got %d", t.Phase),
				Severity: SeverityError,
			})
		}
		if t.Model == "" {
			out = append(out, Problem{
				TaskID:   t.ID,
				Field:    "model",
				Kind:     KindSchemaTasks,
				Message:  "model must be a non-empty string",
				Severity: SeverityError,
			})
		}
	}
	return out
}

// validateDependencies checks that every dependency exists.
//
// A self-dependency is reported as `dep.cycle`, not `dep.missing`: the task
// does exist, and the problem is the edge. Catching it here is also what lets
// FindCycles ignore self-loops entirely.
func validateDependencies(tasks []model.Task) []Problem {
	known := make(map[string]bool, len(tasks))
	for _, t := range tasks {
		known[t.ID] = true
	}

	var out []Problem
	for _, t := range tasks {
		for _, dep := range t.Dependencies {
			if dep == t.ID {
				out = append(out, Problem{
					TaskID:   t.ID,
					Field:    "dependencies",
					Kind:     KindDepCycle,
					Message:  fmt.Sprintf("task %s depends on itself", pyfmt.Quote(t.ID)),
					Severity: SeverityError,
				})
				continue
			}
			if !known[dep] {
				out = append(out, Problem{
					TaskID:   t.ID,
					Field:    "dependencies",
					Kind:     KindDepMissing,
					Message:  fmt.Sprintf("depends on unknown task %s", pyfmt.Quote(dep)),
					Severity: SeverityError,
				})
			}
		}
	}
	return out
}

// validateCycles turns each cycle into a problem, printed as the path so the
// reader can see which edge to cut rather than just that a cycle exists.
func validateCycles(tasks []model.Task) []Problem {
	var out []Problem
	for _, cycle := range FindCycles(tasks) {
		out = append(out, Problem{
			TaskID:      cycle[0],
			Field:       "dependencies",
			Kind:        KindDepCycle,
			Message:     "dependency cycle: " + joinArrows(cycle),
			Remediation: "Break the cycle by removing one of the edges above.",
			Severity:    SeverityError,
		})
	}
	return out
}

// validateRoutes checks every task.model resolves to a router entry.
func validateRoutes(tasks []model.Task, routes []string) []Problem {
	keys := make(map[string]bool, len(routes))
	for _, k := range routes {
		keys[k] = true
	}

	var out []Problem
	for _, t := range tasks {
		// An empty model is already reported by validateSchema; reporting it
		// again as unroutable would be two findings for one mistake.
		if t.Model == "" || keys[t.Model] {
			continue
		}
		out = append(out, Problem{
			TaskID:      t.ID,
			Field:       "model",
			Kind:        KindRouteUnresolved,
			Message:     fmt.Sprintf("model %s has no entry in model_router.yaml", pyfmt.Quote(t.Model)),
			Remediation: "Add a route for this model or fix the task.model value.",
			Severity:    SeverityError,
		})
	}
	return out
}

func joinArrows(path []string) string {
	out := ""
	for i, n := range path {
		if i > 0 {
			out += " -> "
		}
		out += n
	}
	return out
}

// HasErrors reports whether any problem is fatal. `orch validate` exits
// non-zero on this, and warnings alone must not fail a build.
func HasErrors(problems []Problem) bool {
	for _, p := range problems {
		if p.Severity == SeverityError {
			return true
		}
	}
	return false
}
