package graph

import (
	"fmt"
	"sort"

	"github.com/hectorcanaimero/orch/internal/model"
)

// DisplayOrder returns tasks sorted by (phase, id) — the order `orch tasks`
// and `orch status` print, ported from `TaskQueue.all()` (FR-Q-2).
//
// It is NOT a dependency order. A task can appear before something it depends
// on if the phases say so. That is fine for a listing, where the reader wants
// the same rows in the same places every time, and wrong for a scheduler.
func DisplayOrder(tasks []model.Task) []model.Task {
	out := append([]model.Task(nil), tasks...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Phase != out[j].Phase {
			return out[i].Phase < out[j].Phase
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// TopoOrder returns the task ids in dependency order: every task appears
// after everything it depends on.
//
// **New in Go.** Python has no topological sort — `TaskQueue.all()` sorts by
// (phase, id) for display, and `TaskQueue.ready()` picks whatever has its
// dependencies satisfied right now. So this has no Python counterpart to be
// held to, and its tests are its own. It exists because the scheduler needs
// it, and because `orch graph` should be able to print a plan in the order it
// will actually happen.
//
// Ties are broken by (phase, id), the same rule DisplayOrder uses, so the
// output is deterministic: the same tasks.json always produces the same order,
// which is what makes it diffable in a parity harness at all.
//
// Returns an error when the graph cannot be ordered — a cycle, or a
// dependency on a task that does not exist. Both are already reported by
// Validate with a better message; the error here exists so a caller that
// skipped validation cannot silently receive a truncated order.
func TopoOrder(tasks []model.Task) ([]string, error) {
	byID := make(map[string]model.Task, len(tasks))
	for _, t := range tasks {
		byID[t.ID] = t
	}

	// Kahn's algorithm over a deterministic frontier. A plain queue would
	// order by insertion; sorting the ready set each round gives the same
	// answer regardless of how tasks.json happened to be written.
	remaining := make(map[string]int, len(tasks))
	dependents := make(map[string][]string, len(tasks))
	for _, t := range tasks {
		n := 0
		for _, dep := range t.Dependencies {
			if dep == t.ID {
				return nil, fmt.Errorf("task %s depends on itself", pyQuote(t.ID))
			}
			if _, ok := byID[dep]; !ok {
				return nil, fmt.Errorf("task %s depends on unknown task %s",
					pyQuote(t.ID), pyQuote(dep))
			}
			n++
			dependents[dep] = append(dependents[dep], t.ID)
		}
		remaining[t.ID] = n
	}

	ready := make([]string, 0, len(tasks))
	for _, t := range DisplayOrder(tasks) {
		if remaining[t.ID] == 0 {
			ready = append(ready, t.ID)
		}
	}

	out := make([]string, 0, len(tasks))
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		out = append(out, id)

		var freed []string
		for _, dependent := range dependents[id] {
			remaining[dependent]--
			if remaining[dependent] == 0 {
				freed = append(freed, dependent)
			}
		}
		// Newly-ready tasks join the frontier in (phase, id) order, and the
		// frontier as a whole is re-sorted so the choice never depends on
		// which task happened to free them.
		ready = append(ready, freed...)
		sortByPhaseThenID(ready, byID)
	}

	if len(out) != len(tasks) {
		stuck := make([]string, 0, len(tasks)-len(out))
		for id, n := range remaining {
			if n > 0 {
				stuck = append(stuck, id)
			}
		}
		sort.Strings(stuck)
		return nil, fmt.Errorf("dependency cycle: %d task(s) can never start (%s)",
			len(stuck), joinComma(stuck))
	}
	return out, nil
}

func sortByPhaseThenID(ids []string, byID map[string]model.Task) {
	sort.SliceStable(ids, func(i, j int) bool {
		a, b := byID[ids[i]], byID[ids[j]]
		if a.Phase != b.Phase {
			return a.Phase < b.Phase
		}
		return a.ID < b.ID
	})
}

func joinComma(s []string) string {
	out := ""
	for i, v := range s {
		if i > 0 {
			out += ", "
		}
		out += v
	}
	return out
}
