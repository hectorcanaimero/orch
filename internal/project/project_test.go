package project

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/state"
)

// fakeReader stands in for the backend. Hydrate's contract is about which
// status wins, not about SQL, and a fake makes "the database was asked exactly
// once" something the test can assert.
type fakeReader struct {
	rows  []state.TaskRuntime
	err   error
	calls int
}

func (f *fakeReader) Tasks(context.Context, state.TaskFilter) ([]state.TaskRuntime, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.rows, nil
}

func tasks(pairs ...string) []model.Task {
	out := make([]model.Task, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, model.Task{ID: pairs[i], Status: model.Status(pairs[i+1])})
	}
	return out
}

func TestHydrateOverlaysTheDatabaseOnTheFile(t *testing.T) {
	f := &fakeReader{rows: []state.TaskRuntime{
		{ID: "A", Status: model.StatusDone},
		{ID: "B", Status: model.StatusInProgress},
		// A row for a task tasks.json no longer defines. Nothing to overlay
		// it onto, and it must not appear from nowhere.
		{ID: "GHOST", Status: model.StatusBlocked},
	}}
	in := tasks("A", "todo", "B", "todo", "C", "todo")

	got, err := Hydrate(context.Background(), f, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d tasks, want 3 — the database must not add rows", len(got))
	}
	want := map[string]model.Status{
		"A": model.StatusDone,
		"B": model.StatusInProgress,
		// C has no row: tasks.json's status stands, which is the honest
		// answer before Bootstrap has seeded the project.
		"C": model.StatusTodo,
	}
	for _, task := range got {
		if task.Status != want[task.ID] {
			t.Errorf("%s = %q, want %q", task.ID, task.Status, want[task.ID])
		}
	}
	if f.calls != 1 {
		t.Errorf("queried the database %d times, want 1 — the whole point is not per task", f.calls)
	}
}

// The caller's slice is theirs. A view that hydrated in place would mutate
// whatever the caller read the file into, and the next reader of that slice
// would get statuses from a database it never asked about.
func TestHydrateDoesNotMutateTheInput(t *testing.T) {
	f := &fakeReader{rows: []state.TaskRuntime{{ID: "A", Status: model.StatusDone}}}
	in := tasks("A", "todo")

	if _, err := Hydrate(context.Background(), f, in); err != nil {
		t.Fatal(err)
	}
	if in[0].Status != model.StatusTodo {
		t.Errorf("input mutated: A is now %q", in[0].Status)
	}
}

func TestHydrateOnAnEmptyProjectAsksNothing(t *testing.T) {
	f := &fakeReader{}
	got, err := Hydrate(context.Background(), f, nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v", got, err)
	}
	if f.calls != 0 {
		t.Errorf("queried the database %d times for a project with no tasks", f.calls)
	}
}

// A database that cannot be read is an error, not a silent fallback to the
// file. Python swallows it and ships tasks.json's statuses, which is how a
// stale board looks exactly like a current one.
func TestHydrateReportsAReadFailure(t *testing.T) {
	boom := errors.New("database is locked")
	_, err := Hydrate(context.Background(), &fakeReader{err: boom}, tasks("A", "todo"))
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want it to wrap %v", err, boom)
	}
}

// Load is Hydrate with the file read for you — the shape every caller that is
// not mid-atomize wants.
func TestLoadReadsTheFileAndHydratesIt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tasks.json")
	const doc = `{"meta":{"project":"p"},"tasks":[
	  {"id":"A","phase":0,"title":"a","model":"m","status":"todo"},
	  {"id":"B","phase":0,"title":"b","model":"m","status":"todo"}]}`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}

	f := &fakeReader{rows: []state.TaskRuntime{{ID: "B", Status: model.StatusDone}}}
	got, err := Load(context.Background(), f, path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Status != model.StatusTodo || got[1].Status != model.StatusDone {
		t.Errorf("got %+v", got)
	}
}

// A missing or malformed tasks.json is an error the caller sees, not an empty
// project. Python returns [] here, which renders as a project with no tasks —
// the same page a brand new project shows, for a file that is broken.
func TestLoadReportsAMissingFile(t *testing.T) {
	_, err := Load(context.Background(), &fakeReader{},
		filepath.Join(t.TempDir(), "nope.json"))
	if err == nil {
		t.Error("a missing tasks.json must be an error, not an empty project")
	}
}
