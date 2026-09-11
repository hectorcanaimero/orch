package engine

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/hectorcanaimero/orch/internal/graph"
	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/pyfmt"
)

// TaskQueue is the dispatch loop's live view of the DAG. Port of
// orchestrator/task_queue.py.
//
// tasks.json is read-only from the orchestrator's point of view (FR-STATE-2):
// this holds the status map that reacts to MarkInFlight / MarkDone /
// MarkBlocked between reap ticks. Since F-12 the database is the source of
// truth for runtime status, so a queue built with Hydrate starts from what
// the backend says, not from what tasks.json says.
//
// Not safe for concurrent use. The dispatch loop is single-threaded by
// design — the concurrency is in the child processes, not in the scheduler —
// and a mutex here would only hide a second scheduler that should not exist.
type TaskQueue struct {
	byID   map[string]model.Task
	status map[string]model.Status
	// order is the ids sorted by (phase, id) once at construction. Ready is
	// called every tick, and re-sorting the whole DAG each time is the kind
	// of quiet O(n log n) that only shows up on a big project.
	order []string
}

// NewTaskQueue builds a queue from tasks.json's rows.
//
// Dependency and cycle validation happen here, as they do in Python's
// __init__: a DAG that cannot be walked should fail at startup, not at the
// first dispatch. Cycle detection delegates to internal/graph rather than
// carrying a second depth-first search that could disagree with `orch graph`.
func NewTaskQueue(tasks []model.Task) (*TaskQueue, error) {
	q := &TaskQueue{
		byID:   make(map[string]model.Task, len(tasks)),
		status: make(map[string]model.Status, len(tasks)),
	}
	for _, t := range tasks {
		if _, dup := q.byID[t.ID]; dup {
			return nil, fmt.Errorf("duplicate task id in tasks.json: %s", pyfmt.Quote(t.ID))
		}
		q.byID[t.ID] = t
		q.status[t.ID] = t.Status
	}

	if err := q.validateDeps(); err != nil {
		return nil, err
	}
	// Python runs TWO different cycle checks, and this package needs both.
	//
	// `preflight.find_cycles` — which internal/graph ports — ignores a
	// self-dependency by design: a length-1 loop is reported by
	// `validateDependencies` as `dep.cycle` ("task 'A' depends on itself")
	// instead, so `orch validate` catches it on both sides byte for byte.
	// `task_queue.py`'s own `_detect_cycles` DOES catch it, because the
	// dispatch loop cannot survive one: a self-dependent task never becomes
	// ready (its dependency is never done), so it stays todo, Pending()
	// stays true, and the loop spins forever on a task it will never
	// dispatch — a silent hang rather than an error.
	//
	// So this check is the port of the queue's, not a workaround for the
	// graph package. Deleting it on the assumption that FindCycles covers it
	// brings the hang back.
	for _, t := range tasks {
		for _, dep := range t.Dependencies {
			if dep == t.ID {
				return nil, &CycleError{Cycle: []string{t.ID, t.ID}}
			}
		}
	}
	if cycles := graph.FindCycles(tasks); len(cycles) > 0 {
		return nil, &CycleError{Cycle: cycles[0]}
	}

	q.order = make([]string, 0, len(q.byID))
	for id := range q.byID {
		q.order = append(q.order, id)
	}
	sort.Slice(q.order, func(i, j int) bool {
		a, b := q.byID[q.order[i]], q.byID[q.order[j]]
		if a.Phase != b.Phase {
			return a.Phase < b.Phase
		}
		return a.ID < b.ID
	})
	return q, nil
}

// Hydrate overwrites the seeded statuses with the backend's, for the tasks
// the backend knows about.
//
// Python does this inside the constructor and swallows a backend that cannot
// answer, falling back to the tasks.json seed so the dispatch loop still
// starts. Keeping it a separate call makes that choice the caller's: the
// scheduler logs and carries on, and a test can build a queue with no
// backend at all.
func (q *TaskQueue) Hydrate(runtime map[string]model.Status) {
	for id, st := range runtime {
		if _, known := q.status[id]; known {
			q.status[id] = st
		}
	}
}

// CycleError reports a dependency cycle, naming the ring.
//
// The message is copied from Python's TaskCycleError, closing node included,
// because scripts/parity.sh diffs the two binaries' output line for line.
type CycleError struct{ Cycle []string }

func (e *CycleError) Error() string {
	chain := "(unknown)"
	if len(e.Cycle) > 0 {
		chain = strings.Join(e.Cycle, " -> ")
	}
	return "dependency cycle detected in tasks.json: " + chain +
		". Break the cycle before running the orchestrator."
}

// MissingDepError reports dependency references that name no task.
type MissingDepError struct {
	// Offenders are (task id, missing dependency) pairs, sorted.
	Offenders [][2]string
}

func (e *MissingDepError) Error() string {
	lines := make([]string, 0, len(e.Offenders))
	for _, o := range e.Offenders {
		lines = append(lines, fmt.Sprintf("  %s -> %s", o[0], o[1]))
	}
	return fmt.Sprintf("tasks.json has %d unresolved dependency reference(s):\n%s",
		len(e.Offenders), strings.Join(lines, "\n"))
}

func (q *TaskQueue) validateDeps() error {
	var offenders [][2]string
	for _, t := range q.byID {
		for _, dep := range t.Dependencies {
			if _, ok := q.byID[dep]; !ok {
				offenders = append(offenders, [2]string{t.ID, dep})
			}
		}
	}
	if len(offenders) == 0 {
		return nil
	}
	sort.Slice(offenders, func(i, j int) bool {
		if offenders[i][0] != offenders[j][0] {
			return offenders[i][0] < offenders[j][0]
		}
		return offenders[i][1] < offenders[j][1]
	})
	return &MissingDepError{Offenders: offenders}
}

// Status returns a task's current status, and whether the task is known.
func (q *TaskQueue) Status(id string) (model.Status, bool) {
	st, ok := q.status[id]
	return st, ok
}

// Task returns one task's definition.
func (q *TaskQueue) Task(id string) (model.Task, bool) {
	t, ok := q.byID[id]
	return t, ok
}

// AllTasks returns every task in (phase, id) order — the same deterministic
// order the display commands use (FR-Q-2).
func (q *TaskQueue) AllTasks() []model.Task {
	out := make([]model.Task, 0, len(q.order))
	for _, id := range q.order {
		out = append(out, q.byID[id])
	}
	return out
}

// ReadyOpts narrows what Ready considers.
type ReadyOpts struct {
	// InFlight excludes tasks the caller has picked but not yet marked
	// in-progress, so one tick cannot dispatch the same task twice.
	InFlight map[string]bool
	// Only is an fnmatch-style glob on the task id. When set, only matching
	// tasks are candidates — but the WHOLE DAG still resolves dependencies,
	// so a task whose deps sit outside the glob becomes ready as soon as
	// those deps are done. Narrowing the candidate set is the right home for
	// --only; narrowing the graph would silently change readiness.
	Only string
	// Deferred excludes tasks the operator deferred at the semi gate.
	Deferred map[string]bool
	// Waiting excludes tasks another queue owns until it releases them —
	// today, the retry queue's backoff. Without it a task reset to todo for
	// a retry is dispatched by this pass before its backoff expires, and can
	// then be dispatched a second time when the retry fires.
	Waiting map[string]bool
}

// Ready returns the tasks whose dependencies are all done and which are
// themselves todo, in (phase, id) order.
//
// Pure with respect to its inputs (FR-Q-5): the same status map and the same
// options give the same answer.
func (q *TaskQueue) Ready(opts ReadyOpts) []model.Task {
	var out []model.Task
	for _, id := range q.order {
		if q.status[id] != model.StatusTodo {
			continue
		}
		if opts.InFlight[id] || opts.Deferred[id] || opts.Waiting[id] {
			continue
		}
		if opts.Only != "" && !matchGlob(opts.Only, id) {
			continue
		}
		t := q.byID[id]
		if q.depsDone(t) {
			out = append(out, t)
		}
	}
	return out
}

func (q *TaskQueue) depsDone(t model.Task) bool {
	for _, dep := range t.Dependencies {
		if q.status[dep] != model.StatusDone {
			return false
		}
	}
	return true
}

// matchGlob is Python's fnmatch.fnmatchcase for the patterns --only accepts.
//
// path.Match is case-sensitive like fnmatchcase, and supports the same `*`,
// `?` and `[...]`. The one difference is that path.Match's `*` does not cross
// a `/`; task ids have no slashes (`B-020`, `F0.T1`), so the two agree on
// every id that can reach here. A malformed pattern matches nothing rather
// than erroring, which is also what fnmatch does.
func matchGlob(pattern, id string) bool {
	ok, err := path.Match(pattern, id)
	return err == nil && ok
}

// Pending reports whether any task is still todo or in-progress — the
// dispatch loop's "keep going" condition.
func (q *TaskQueue) Pending() bool {
	for _, st := range q.status {
		if st == model.StatusTodo || st == model.StatusInProgress {
			return true
		}
	}
	return false
}

// MarkInFlight, MarkDone and MarkBlocked move a task in the live view.
//
// They do not write to the database. Python's queue mirrors each mark into
// the backend and swallows the error so a backend hiccup cannot stall the
// loop; here the scheduler owns that call, because it is the thing that knows
// what note to record and what to do when the write fails. A queue that
// silently diverged from the database would be the worst of both.
func (q *TaskQueue) MarkInFlight(id string) error { return q.mark(id, model.StatusInProgress) }
func (q *TaskQueue) MarkDone(id string) error     { return q.mark(id, model.StatusDone) }
func (q *TaskQueue) MarkBlocked(id string) error  { return q.mark(id, model.StatusBlocked) }

// MarkTodo puts a task back in the ready set after a failed attempt.
//
// Python reaches into TaskQueue._status directly here, with a comment saying
// retry is the only caller and the class has no mark_todo. It does now: a
// retry is a legitimate move in this view, and reaching through the struct
// would only hide who does it.
func (q *TaskQueue) MarkTodo(id string) error { return q.mark(id, model.StatusTodo) }

func (q *TaskQueue) mark(id string, st model.Status) error {
	if _, ok := q.byID[id]; !ok {
		return fmt.Errorf("unknown task id: %s", pyfmt.Quote(id))
	}
	q.status[id] = st
	return nil
}
