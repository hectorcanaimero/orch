package engine

import (
	"slices"
	"strings"
	"testing"

	"github.com/hectorcanaimero/orch/internal/model"
)

func TestNewTaskQueueValidation(t *testing.T) {
	tests := []struct {
		name    string
		tasks   []model.Task
		wantErr string
	}{
		{
			name:  "a clean DAG",
			tasks: []model.Task{task("A", 1, "m"), task("B", 1, "m", "A")},
		},
		{
			name:    "a duplicate id",
			tasks:   []model.Task{task("A", 1, "m"), task("A", 2, "m")},
			wantErr: "duplicate task id",
		},
		{
			name:    "a dependency that names no task",
			tasks:   []model.Task{task("A", 1, "m", "NOPE")},
			wantErr: "unresolved dependency",
		},
		{
			name: "a two-task cycle",
			tasks: []model.Task{
				task("A", 1, "m", "B"),
				task("B", 1, "m", "A"),
			},
			wantErr: "dependency cycle",
		},
		{
			// The queue's own check, ported from task_queue.py's
			// _detect_cycles. graph.FindCycles deliberately ignores a
			// length-1 loop (preflight.find_cycles does too, and `orch
			// validate` reports it as dep.cycle instead), but the dispatch
			// loop cannot survive one — see NewTaskQueue.
			name:    "a self-dependency is a cycle",
			tasks:   []model.Task{task("A", 1, "m", "A")},
			wantErr: "dependency cycle",
		},
		{
			name:  "no tasks at all",
			tasks: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewTaskQueue(tt.tasks)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want an error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("err = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

// TestMissingDepErrorNamesEveryOffender: the message is what an operator has
// to fix tasks.json from, so it lists every bad reference, sorted, not just
// the first one found.
func TestMissingDepErrorNamesEveryOffender(t *testing.T) {
	_, err := NewTaskQueue([]model.Task{
		task("B", 1, "m", "ZZZ"),
		task("A", 1, "m", "YYY", "XXX"),
	})
	if err == nil {
		t.Fatal("want an error")
	}
	msg := err.Error()
	for _, want := range []string{"A -> XXX", "A -> YYY", "B -> ZZZ", "3 unresolved"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message is missing %q:\n%s", want, msg)
		}
	}
	// Sorted, so two runs over the same file produce the same message.
	if strings.Index(msg, "A -> XXX") > strings.Index(msg, "B -> ZZZ") {
		t.Errorf("offenders are not sorted:\n%s", msg)
	}
}

func TestQueueReady(t *testing.T) {
	tasks := []model.Task{
		task("A", 1, "m"),
		task("B", 1, "m", "A"),
		task("C", 2, "m"),
		task("D", 1, "m"),
	}

	tests := []struct {
		name    string
		prepare func(q *TaskQueue)
		opts    ReadyOpts
		want    []string
	}{
		{
			name: "phase then id, and a blocked dependency holds B back",
			want: []string{"A", "D", "C"},
		},
		{
			name:    "B becomes ready once A is done",
			prepare: func(q *TaskQueue) { _ = q.MarkDone("A") },
			want:    []string{"B", "D", "C"},
		},
		{
			name:    "a blocked dependency does NOT release its dependents",
			prepare: func(q *TaskQueue) { _ = q.MarkBlocked("A") },
			want:    []string{"D", "C"},
		},
		{
			name:    "an in-progress task is not ready again",
			prepare: func(q *TaskQueue) { _ = q.MarkInFlight("A") },
			want:    []string{"D", "C"},
		},
		{
			name: "in-flight ids are excluded",
			opts: ReadyOpts{InFlight: map[string]bool{"A": true}},
			want: []string{"D", "C"},
		},
		{
			name: "deferred ids are excluded",
			opts: ReadyOpts{Deferred: map[string]bool{"D": true}},
			want: []string{"A", "C"},
		},
		{
			name: "only narrows the candidates",
			opts: ReadyOpts{Only: "A"},
			want: []string{"A"},
		},
		{
			name: "a glob that matches nothing yields nothing",
			opts: ReadyOpts{Only: "ZZ*"},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, err := NewTaskQueue(tasks)
			if err != nil {
				t.Fatalf("NewTaskQueue: %v", err)
			}
			if tt.prepare != nil {
				tt.prepare(q)
			}
			got := idsOf(q.Ready(tt.opts))
			if !equalStrings(got, tt.want) {
				t.Errorf("Ready = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestReadyResolvesDepsOutsideTheGlob is the property that makes --only safe:
// the glob narrows what may be dispatched, never how readiness is computed.
// A task whose dependency sits outside the glob becomes ready as soon as that
// dependency is done.
func TestReadyResolvesDepsOutsideTheGlob(t *testing.T) {
	q, err := NewTaskQueue([]model.Task{
		task("SETUP", 1, "m"),
		task("F0.T1", 1, "m", "SETUP"),
	})
	if err != nil {
		t.Fatalf("NewTaskQueue: %v", err)
	}

	if got := idsOf(q.Ready(ReadyOpts{Only: "F0.*"})); len(got) != 0 {
		t.Fatalf("Ready = %v, want nothing while SETUP is todo", got)
	}
	if err := q.MarkDone("SETUP"); err != nil {
		t.Fatal(err)
	}
	if got := idsOf(q.Ready(ReadyOpts{Only: "F0.*"})); !equalStrings(got, []string{"F0.T1"}) {
		t.Errorf("Ready = %v, want F0.T1 once its out-of-glob dep is done", got)
	}
}

func TestQueuePending(t *testing.T) {
	q, err := NewTaskQueue([]model.Task{task("A", 1, "m"), task("B", 1, "m")})
	if err != nil {
		t.Fatalf("NewTaskQueue: %v", err)
	}
	if !q.Pending() {
		t.Error("a fresh queue of todo tasks is pending")
	}
	if err := q.MarkDone("A"); err != nil {
		t.Fatal(err)
	}
	if !q.Pending() {
		t.Error("still pending while B is todo")
	}
	if err := q.MarkInFlight("B"); err != nil {
		t.Fatal(err)
	}
	if !q.Pending() {
		t.Error("in-progress counts as pending — the run is not over")
	}
	if err := q.MarkBlocked("B"); err != nil {
		t.Fatal(err)
	}
	if q.Pending() {
		t.Error("done and blocked are both terminal; nothing is pending")
	}
}

func TestQueueMarkUnknownTask(t *testing.T) {
	q, err := NewTaskQueue([]model.Task{task("A", 1, "m")})
	if err != nil {
		t.Fatalf("NewTaskQueue: %v", err)
	}
	for name, fn := range map[string]func(string) error{
		"in-flight": q.MarkInFlight,
		"done":      q.MarkDone,
		"blocked":   q.MarkBlocked,
	} {
		t.Run(name, func(t *testing.T) {
			err := fn("NOPE")
			if err == nil {
				t.Fatal("want an error for an unknown id")
			}
			// Python renders the id with !r; pyfmt.Quote keeps the messages
			// identical between the two binaries.
			if !strings.Contains(err.Error(), `'NOPE'`) {
				t.Errorf("err = %q, want it to quote the id Python-style", err)
			}
		})
	}
}

func TestQueueHydrate(t *testing.T) {
	q, err := NewTaskQueue([]model.Task{
		task("A", 1, "m"),
		task("B", 1, "m"),
	})
	if err != nil {
		t.Fatalf("NewTaskQueue: %v", err)
	}

	q.Hydrate(map[string]model.Status{
		"A":       model.StatusDone,
		"UNKNOWN": model.StatusDone, // a row for a task tasks.json no longer has
	})

	if got, _ := q.Status("A"); got != model.StatusDone {
		t.Errorf("A = %q, want the backend's status to win", got)
	}
	if got, _ := q.Status("B"); got != model.StatusTodo {
		t.Errorf("B = %q, want it untouched", got)
	}
	if _, known := q.Status("UNKNOWN"); known {
		t.Error("hydration invented a task the DAG does not have")
	}
}

func TestQueueAllTasksOrder(t *testing.T) {
	q, err := NewTaskQueue([]model.Task{
		task("Z", 1, "m"),
		task("A", 2, "m"),
		task("B", 1, "m"),
	})
	if err != nil {
		t.Fatalf("NewTaskQueue: %v", err)
	}
	if got := idsOf(q.AllTasks()); !equalStrings(got, []string{"B", "Z", "A"}) {
		t.Errorf("AllTasks = %v, want (phase, id) order", got)
	}
}

func TestMatchGlob(t *testing.T) {
	tests := []struct {
		pattern, id string
		want        bool
	}{
		{"F0.*", "F0.T1", true},
		{"F0.*", "F1.T1", false},
		{"*", "anything", true},
		{"B-0??", "B-020", true},
		{"B-0??", "B-0200", false},
		{"b-020", "B-020", false}, // case-sensitive, like fnmatchcase
		{"[AB]-1", "A-1", true},
		{"[AB]-1", "C-1", false},
		{"[", "A-1", false}, // a malformed pattern matches nothing
	}

	for _, tt := range tests {
		t.Run(tt.pattern+"/"+tt.id, func(t *testing.T) {
			if got := matchGlob(tt.pattern, tt.id); got != tt.want {
				t.Errorf("matchGlob(%q, %q) = %v, want %v", tt.pattern, tt.id, got, tt.want)
			}
		})
	}
}

func idsOf(tasks []model.Task) []string {
	out := make([]string, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, t.ID)
	}
	return out
}

func equalStrings(a, b []string) bool {
	return slices.Equal(a, b)
}

// TestCycleErrorMatchesPython pins the message, closing node included.
// scripts/parity.sh diffs the two binaries' output line for line, so this is
// a contract rather than phrasing.
func TestCycleErrorMatchesPython(t *testing.T) {
	tests := []struct {
		name  string
		cycle []string
		want  string
	}{
		{
			name:  "a self-dependency",
			cycle: []string{"A", "A"},
			want:  "dependency cycle detected in tasks.json: A -> A. Break the cycle before running the orchestrator.",
		},
		{
			name:  "a longer ring",
			cycle: []string{"A", "B", "A"},
			want:  "dependency cycle detected in tasks.json: A -> B -> A. Break the cycle before running the orchestrator.",
		},
		{
			// Python renders an empty cycle as "(unknown)".
			name:  "no ring to name",
			cycle: nil,
			want:  "dependency cycle detected in tasks.json: (unknown). Break the cycle before running the orchestrator.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &CycleError{Cycle: tt.cycle}
			if got := err.Error(); got != tt.want {
				t.Errorf("Error() =\n  %q\nwant\n  %q", got, tt.want)
			}
		})
	}
}

// TestSelfDependencyErrorIsTheWholeMessage walks it through NewTaskQueue, so
// the check and the message are pinned together.
func TestSelfDependencyErrorIsTheWholeMessage(t *testing.T) {
	_, err := NewTaskQueue([]model.Task{task("A", 1, "m", "A")})
	if err == nil {
		t.Fatal("want an error")
	}
	want := "dependency cycle detected in tasks.json: A -> A. Break the cycle before running the orchestrator."
	if err.Error() != want {
		t.Errorf("err =\n  %q\nwant\n  %q", err.Error(), want)
	}
}
