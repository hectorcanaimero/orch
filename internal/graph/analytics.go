package graph

import (
	"sort"

	"github.com/hectorcanaimero/orch/internal/model"
)

// Analytics over the DAG: what is ready, what is waiting, where the long pole
// is. Ported from the task-graph half of `orchestrator/dashboard/metrics.py`.
//
// It lives here rather than in the dashboard because none of it is about HTTP:
// every function takes tasks and returns numbers, and `orch status` wants the
// same answers the dashboard does. The dashboard half of that file — pricing,
// spend, sprint health — stays with the dashboard, because it needs the state
// backend and this package deliberately does not.

// Summary is the header bar: how many of each, how far along, how much work.
//
// `Backlog` is backlog + todo deliberately, matching Python's comment: the
// operator asking "how much is left" does not care about the split, and two
// adjacent numbers that always move together read as one number badly.
type Summary struct {
	Total              int
	Done               int
	InProgress         int
	Blocked            int
	Backlog            int
	PercentDone        float64
	EstimateHoursTotal float64
}

// Summarize counts a project in one pass.
//
// The `else` branch is why `Backlog` can exceed backlog+todo: any status that
// is not done / in-progress / blocked lands there, including one this build
// has never heard of. Python does the same, and the alternative — dropping
// unknown statuses — makes the numbers stop adding up to `Total`, which is the
// one property a header bar must have.
func Summarize(tasks []model.Task) Summary {
	var s Summary
	for _, t := range tasks {
		s.Total++
		s.EstimateHoursTotal += t.EstimateHours
		switch t.Status {
		case model.StatusDone:
			s.Done++
		case model.StatusInProgress:
			s.InProgress++
		case model.StatusBlocked:
			s.Blocked++
		default:
			s.Backlog++
		}
	}
	if s.Total > 0 {
		s.PercentDone = float64(s.Done) / float64(s.Total) * 100.0
	}
	return s
}

// PhaseRow is one phase's tally.
type PhaseRow struct {
	Phase      int
	Total      int
	Done       int
	InProgress int
	Blocked    int
}

// PhaseCounts tallies per phase, ordered by phase number.
//
// Only phases that have at least one task appear — a project whose phases run
// 1, 2, 5 gets three rows, not five. Python's `defaultdict` has the same
// effect, and inventing empty rows for the gap would be inventing phases.
func PhaseCounts(tasks []model.Task) []PhaseRow {
	byPhase := map[int]*PhaseRow{}
	for _, t := range tasks {
		row := byPhase[t.Phase]
		if row == nil {
			row = &PhaseRow{Phase: t.Phase}
			byPhase[t.Phase] = row
		}
		row.Total++
		switch t.Status {
		case model.StatusDone:
			row.Done++
		case model.StatusInProgress:
			row.InProgress++
		case model.StatusBlocked:
			row.Blocked++
		}
	}
	out := make([]PhaseRow, 0, len(byPhase))
	for _, row := range byPhase {
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Phase < out[j].Phase })
	return out
}

// Ready is what `orch run` would dispatch right now: Parallelizable narrowed
// to todo. A backlog task must be promoted first — engine.TaskQueue.Ready
// skips it — so calling it ready promised a dispatch that never came (#235).
// This is the notion every "ready" surface (orch explain, orch_context,
// orch_list_tasks{ready}) publishes.
func Ready(tasks []model.Task) []model.Task {
	par := Parallelizable(tasks)
	ready := par[:0]
	for _, t := range par {
		if t.Status == model.StatusTodo {
			ready = append(ready, t)
		}
	}
	return ready
}

// Parallelizable returns the tasks whose dependencies no longer hold them
// back: backlog or todo, with every dependency already done. It is Python's
// analytics figure (the dashboard's `parallelizable`), not what a run
// dispatches — that is Ready.
//
// A dependency that names a task nobody defined counts as NOT done. That is
// Python's `by_id.get(dep_id)` returning None, and it is the safe reading — a
// task whose dependency does not exist has an unmet dependency, whatever the
// reason. `OrphanDependencies` is how the operator finds out why.
//
// Sorted by (phase, id), which is also how Python returns it.
func Parallelizable(tasks []model.Task) []model.Task {
	byID := indexByID(tasks)

	ready := make([]model.Task, 0, len(byID))
	for _, t := range byID {
		if t.Status != model.StatusBacklog && t.Status != model.StatusTodo {
			continue
		}
		depsOK := true
		for _, dep := range t.Dependencies {
			d, ok := byID[dep]
			if !ok || d.Status != model.StatusDone {
				depsOK = false
				break
			}
		}
		if depsOK {
			ready = append(ready, t)
		}
	}
	sort.Slice(ready, func(i, j int) bool {
		if ready[i].Phase != ready[j].Phase {
			return ready[i].Phase < ready[j].Phase
		}
		return ready[i].ID < ready[j].ID
	})
	return ready
}

// DownstreamImpact answers "unblock this and how many tasks become reachable".
//
// Two rules make the number useful rather than merely large:
//
//   - Only NOT-DONE descendants count. A finished task no longer waits on
//     anything, so it must not keep inflating the score of everything upstream
//     of it once the chain has drained.
//   - Traversal still passes THROUGH done tasks, so pending work sitting below
//     a completed intermediate node is still reached.
//
// O(V+E) per task on one shared reverse adjacency.
func DownstreamImpact(tasks []model.Task) map[string]int {
	byID := indexByID(tasks)
	reverse := map[string][]string{}
	for _, t := range tasks {
		for _, dep := range t.Dependencies {
			reverse[dep] = append(reverse[dep], t.ID)
		}
	}

	out := make(map[string]int, len(tasks))
	for _, t := range tasks {
		// Seeded with the task itself so a cycle (A → B → A) cannot count the
		// starting task as its own descendant. tasks.json is DAG-shaped by
		// contract; this is the guard for when it is not.
		seen := map[string]bool{t.ID: true}
		stack := append([]string(nil), reverse[t.ID]...)
		for len(stack) > 0 {
			next := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if seen[next] {
				continue
			}
			seen[next] = true
			stack = append(stack, reverse[next]...)
		}
		delete(seen, t.ID)

		count := 0
		for id := range seen {
			if d, ok := byID[id]; ok && d.Status != model.StatusDone {
				count++
			}
		}
		out[t.ID] = count
	}
	return out
}

// CriticalPath returns the ids on the longest weighted path through the DAG.
//
// Weight is `estimate_hours`, falling back to 1 when it is zero or absent so
// an un-estimated project still produces a path rather than collapsing to
// nothing. Direction follows `dependencies`: a task's time accumulates after
// its upstream finishes.
//
// Kahn's order plus a DP, so it is O(V+E) and silent on cycles — a cycle stops
// draining the queue and whatever was computed before the stall is returned.
// That is Python's behaviour, and `FindCycles` is the function whose job is to
// complain about it.
//
// # Why the traversal order is reproduced exactly
//
// The DP result does not depend on the order ready nodes come off the queue —
// a node is only processed once every dependency has been. The FINAL PICK
// does: `max(dist, key=…)` returns the first key with the maximal value in
// dict insertion order, and insertion order comes from the pop order. So the
// stack pops from the end like Python's `list.pop()`, and the winner is found
// by scanning the ids in the order they entered `dist`. Two paths of equal
// length are not a hypothetical — they are what an un-estimated project of
// parallel branches is made of.
func CriticalPath(tasks []model.Task) map[string]bool {
	if len(tasks) == 0 {
		return map[string]bool{}
	}
	byID := indexByID(tasks)

	weight := func(t model.Task) float64 {
		if t.EstimateHours == 0 {
			return 1.0
		}
		return t.EstimateHours
	}

	indeg := make(map[string]int, len(tasks))
	forward := map[string][]string{}
	for _, t := range tasks {
		indeg[t.ID] = len(t.Dependencies)
	}
	for _, t := range tasks {
		for _, dep := range t.Dependencies {
			forward[dep] = append(forward[dep], t.ID)
		}
	}

	dist := map[string]float64{}
	// distOrder is the insertion order of `dist`, which decides ties. A Go map
	// would decide them at random, which is the kind of test that passes for
	// months and then does not.
	var distOrder []string
	parent := map[string]string{}
	// hasParent is separate from `parent` so an empty task id — which
	// `Validate` reports and `DOT` skips, but which still reaches here —
	// cannot read as "no parent" and truncate the walk back.
	hasParent := map[string]bool{}

	// Python builds `ready` by iterating `indeg`, which is in tasks order.
	var ready []string
	for _, t := range tasks {
		if indeg[t.ID] == 0 {
			ready = append(ready, t.ID)
		}
	}

	for len(ready) > 0 {
		id := ready[len(ready)-1]
		ready = ready[:len(ready)-1]
		t := byID[id]

		best := weight(t)
		bestParent, bestParentFound := "", false
		for _, dep := range t.Dependencies {
			d, ok := dist[dep]
			if !ok {
				continue
			}
			if cand := d + weight(t); cand > best {
				best = cand
				bestParent, bestParentFound = dep, true
			}
		}
		if _, seen := dist[id]; !seen {
			distOrder = append(distOrder, id)
		}
		dist[id] = best
		if bestParent != "" || bestParentFound {
			parent[id] = bestParent
			hasParent[id] = bestParentFound
		}

		for _, next := range forward[id] {
			indeg[next]--
			if indeg[next] == 0 {
				ready = append(ready, next)
			}
		}
	}

	if len(dist) == 0 {
		return map[string]bool{}
	}
	end := distOrder[0]
	for _, id := range distOrder[1:] {
		if dist[id] > dist[end] {
			end = id
		}
	}

	path := map[string]bool{}
	for cur, ok := end, true; ok; cur, ok = parent[cur], hasParent[cur] {
		if path[cur] {
			break // `parent` cannot cycle; not looping forever if it ever does
		}
		path[cur] = true
	}
	return path
}

// Orphan is one dependency pointing at a task nobody defined.
type Orphan struct {
	TaskID       string
	MissingDepID string
}

// OrphanDependencies lists dependencies on ids that do not exist.
//
// A healthy tasks.json has none; anything here is a typo, a deleted task, or
// a half-finished import. Reported in tasks order, then dependency order, so
// two runs on the same file produce the same list.
func OrphanDependencies(tasks []model.Task) []Orphan {
	known := make(map[string]bool, len(tasks))
	for _, t := range tasks {
		known[t.ID] = true
	}
	var out []Orphan
	for _, t := range tasks {
		for _, dep := range t.Dependencies {
			if !known[dep] {
				out = append(out, Orphan{TaskID: t.ID, MissingDepID: dep})
			}
		}
	}
	return out
}

// indexByID mirrors Python's `{t.id: t for t in tasks}`: on a duplicate id the
// LAST task wins. Not a rule worth defending, but it is the rule the Python
// dashboard has, and a duplicate id is already a broken file — `Validate` is
// what reports it.
func indexByID(tasks []model.Task) map[string]model.Task {
	byID := make(map[string]model.Task, len(tasks))
	for _, t := range tasks {
		byID[t.ID] = t
	}
	return byID
}
