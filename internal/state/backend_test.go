package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hectorcanaimero/orch/internal/model"
)

// The cases here are ported from `test_sqlite_backend.py` (828 lines),
// `test_backend_parity.py` and the sqlite half of `test_state.py`, grouped by
// behaviour rather than by the order of the Python file. See
// testdata/python-test-inventory.md for the mapping.

const fixedNow = "2026-09-11T12:00:00Z"

// newBackend gives a fresh database with a frozen clock, so a test never
// races real time and a timestamp assertion means something.
func newBackend(t *testing.T) *SQLite {
	t.Helper()
	db, _, err := Open(context.Background(), filepath.Join(t.TempDir(), "orch.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	b := NewSQLite(db, "proj", "/tmp/proj")
	frozen, err := time.Parse(time.RFC3339, fixedNow)
	if err != nil {
		t.Fatalf("parse the frozen clock: %v", err)
	}
	b.now = func() time.Time { return frozen }
	return b
}

func tasks(ids ...string) []model.Task {
	out := make([]model.Task, 0, len(ids))
	for i, id := range ids {
		out = append(out, model.Task{
			ID: id, Phase: i, Title: "Task " + id,
			Model: "claude/claude-sonnet-4-6", Status: model.StatusTodo,
			EstimateHours: 0.5, SpecRef: "specs/" + id + ".md",
		})
	}
	return out
}

func seeded(t *testing.T, ids ...string) *SQLite {
	t.Helper()
	b := newBackend(t)
	if err := b.Bootstrap(context.Background(), tasks(ids...)); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	return b
}

// ---- bootstrap -------------------------------------------------------------

func TestBootstrapSeedsRuntimeAndDefinition(t *testing.T) {
	ctx := context.Background()
	b := newBackend(t)

	want := model.Task{
		ID: "F0.T1", Phase: 2, Title: "Scaffold", Model: "claude/claude-opus-4-6",
		Reason: "risky", Status: model.StatusBacklog, Dependencies: []string{"F0.T0"},
		EstimateHours: 1.25, Files: []string{"a.go", "b.go"}, SpecRef: "specs/f0.md#T1",
	}
	if err := b.Bootstrap(ctx, []model.Task{want}); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	got, err := b.Task(ctx, "F0.T1")
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	if got.Status != model.StatusBacklog {
		t.Errorf("status = %q, want the value from tasks.json on first insert", got.Status)
	}
	if got.UpdatedAt != fixedNow {
		t.Errorf("updated_at = %q, want %q", got.UpdatedAt, fixedNow)
	}

	// The definition half is what the dashboard and the scheduler read.
	var (
		title, mdl, deps, specRef, files, reason string
		phase                                    int
		estimate                                 float64
	)
	err = b.db.read.QueryRowContext(ctx,
		`SELECT title, model, deps_json, spec_ref, phase, estimate_h, reason, files_json
		   FROM tasks_definition WHERE project_id = 'proj' AND task_id = 'F0.T1'`,
	).Scan(&title, &mdl, &deps, &specRef, &phase, &estimate, &reason, &files)
	if err != nil {
		t.Fatalf("read the definition row: %v", err)
	}
	for _, c := range []struct{ name, got, want string }{
		{"title", title, "Scaffold"},
		{"model", mdl, "claude/claude-opus-4-6"},
		{"deps_json", deps, `["F0.T0"]`},
		{"spec_ref", specRef, "specs/f0.md#T1"},
		{"reason", reason, "risky"},
		{"files_json", files, `["a.go","b.go"]`},
	} {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
	if phase != 2 {
		t.Errorf("phase = %d, want 2", phase)
	}
	if estimate != 1.25 {
		t.Errorf("estimate_h = %v, want 1.25", estimate)
	}
}

// The property that makes Bootstrap safe to run on every startup, and the
// reason tasks.json's status is ignored after the first insert: otherwise a
// stale checkout would resurrect finished tasks.
func TestBootstrapNeverOverwritesRuntimeStatus(t *testing.T) {
	ctx := context.Background()
	b := seeded(t, "T1")

	if err := b.Transition(ctx, "T1", model.StatusDone, Note{Author: "orch"}); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	// Same task, back to `todo` in tasks.json — a stale file, or a hand edit.
	if err := b.Bootstrap(ctx, tasks("T1")); err != nil {
		t.Fatalf("second Bootstrap: %v", err)
	}

	got, err := b.Task(ctx, "T1")
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	if got.Status != model.StatusDone {
		t.Errorf("status = %q; a re-bootstrap resurrected a finished task", got.Status)
	}
}

func TestBootstrapIsIdempotent(t *testing.T) {
	ctx := context.Background()
	b := seeded(t, "T1", "T2")
	for i := 0; i < 3; i++ {
		if err := b.Bootstrap(ctx, tasks("T1", "T2")); err != nil {
			t.Fatalf("Bootstrap %d: %v", i, err)
		}
	}
	got, err := b.Tasks(ctx, TaskFilter{})
	if err != nil {
		t.Fatalf("Tasks: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("got %d tasks after three bootstraps, want 2", len(got))
	}
}

// tasks.json carrying a status the backend cannot hold is a hand-edit, not a
// reason to crash. Python lands these on "todo"; so do we.
func TestBootstrapLandsAnUnknownStatusOnTodo(t *testing.T) {
	ctx := context.Background()
	b := newBackend(t)
	bad := tasks("T1")
	bad[0].Status = model.Status("skipped") // a display label, never a stored status

	if err := b.Bootstrap(ctx, bad); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	got, err := b.Task(ctx, "T1")
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	if got.Status != model.StatusTodo {
		t.Errorf("status = %q, want todo", got.Status)
	}
}

// Python writes `json.dumps([])` for an empty list. A Go nil slice would
// marshal to `null` and read back as a different thing.
func TestBootstrapWritesEmptyListsNotNull(t *testing.T) {
	ctx := context.Background()
	b := seeded(t, "T1")

	var deps, files, comments string
	err := b.db.read.QueryRowContext(ctx,
		`SELECT d.deps_json, d.files_json, r.comments_json
		   FROM tasks_definition d
		   JOIN tasks_runtime r ON r.task_id = d.task_id
		  WHERE d.project_id = 'proj' AND d.task_id = 'T1'`,
	).Scan(&deps, &files, &comments)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, c := range []struct{ name, got string }{
		{"deps_json", deps}, {"files_json", files}, {"comments_json", comments},
	} {
		if c.got != "[]" {
			t.Errorf("%s = %q, want `[]`", c.name, c.got)
		}
	}
}

// ---- transitions -----------------------------------------------------------

func TestTransitionTable(t *testing.T) {
	ctx := context.Background()
	// The legal table lives in model and is verified there against Python.
	// What this pins is that the BACKEND consults it — every legal move
	// persists, every illegal one is refused and changes nothing.
	cases := []struct {
		name  string
		path  []model.Status
		final model.Status
	}{
		{"the happy path", []model.Status{model.StatusInProgress, model.StatusDone}, model.StatusDone},
		{"blocked then recovered", []model.Status{model.StatusInProgress, model.StatusBlocked, model.StatusInProgress, model.StatusDone}, model.StatusDone},
		{"todo straight to done (manual completion)", []model.Status{model.StatusDone}, model.StatusDone},
		{"done reopened for a reset", []model.Status{model.StatusDone, model.StatusTodo}, model.StatusTodo},
		{"same status twice is idempotent", []model.Status{model.StatusTodo, model.StatusTodo}, model.StatusTodo},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := seeded(t, "T1")
			for i, to := range c.path {
				if err := b.Transition(ctx, "T1", to, Note{Author: "orch"}); err != nil {
					t.Fatalf("step %d (-> %s): %v", i, to, err)
				}
			}
			got, err := b.Task(ctx, "T1")
			if err != nil {
				t.Fatalf("Task: %v", err)
			}
			if got.Status != c.final {
				t.Errorf("final status = %q, want %q", got.Status, c.final)
			}
		})
	}
}

// TestTransitionToTodoClearsStalePRAndCI pins issue #255's second symptom: a
// task reset to todo (via `orch task-status <id> todo` or `orch reset`) kept
// its old pr_url and `ci_status = 'pending'`, so TasksWithPendingCI fed the
// CIPoller the same dead PR forever — logging "reading CI status failed" on
// every tick even after the task got a brand new PR. Landing back in todo has
// to mean "no PR to poll" until something records a new one.
func TestTransitionToTodoClearsStalePRAndCI(t *testing.T) {
	ctx := context.Background()
	b := seeded(t, "T1")

	if err := b.Transition(ctx, "T1", model.StatusInProgress, Note{}); err != nil {
		t.Fatalf("setup in-progress: %v", err)
	}
	if err := b.SetTaskPR(ctx, "T1", "https://example.test/pr/18"); err != nil {
		t.Fatalf("setup SetTaskPR: %v", err)
	}
	if _, err := b.IncrementCIAttempts(ctx, "T1"); err != nil {
		t.Fatalf("setup IncrementCIAttempts: %v", err)
	}
	if err := b.Transition(ctx, "T1", model.StatusBlocked, Note{}); err != nil {
		t.Fatalf("setup blocked: %v", err)
	}

	got, err := b.Task(ctx, "T1")
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	if got.PRURL == "" || got.CIStatus == "" || got.CIAttempts == 0 {
		t.Fatalf("setup did not stick: PRURL=%q CIStatus=%q CIAttempts=%d",
			got.PRURL, got.CIStatus, got.CIAttempts)
	}

	if err := b.Transition(ctx, "T1", model.StatusTodo, Note{Author: "orch-reset"}); err != nil {
		t.Fatalf("Transition to todo: %v", err)
	}

	got, err = b.Task(ctx, "T1")
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	if got.PRURL != "" {
		t.Errorf("PRURL = %q after reset to todo, want empty", got.PRURL)
	}
	if got.CIStatus != "" {
		t.Errorf("CIStatus = %q after reset to todo, want empty", got.CIStatus)
	}
	if got.CIAttempts != 0 {
		t.Errorf("CIAttempts = %d after reset to todo, want 0", got.CIAttempts)
	}

	pending, err := b.TasksWithPendingCI(ctx)
	if err != nil {
		t.Fatalf("TasksWithPendingCI: %v", err)
	}
	for _, row := range pending {
		if row.ID == "T1" {
			t.Fatalf("T1 is still in TasksWithPendingCI after its reset to todo")
		}
	}
}

func TestIllegalTransitionIsRefusedAndChangesNothing(t *testing.T) {
	ctx := context.Background()
	b := seeded(t, "T1")
	if err := b.Transition(ctx, "T1", model.StatusDone, Note{}); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// done -> blocked is not in the table.
	err := b.Transition(ctx, "T1", model.StatusBlocked, Note{Author: "orch"})
	if !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("got %v, want ErrIllegalTransition", err)
	}
	// The message has to tell a human what they CAN do — they just typed a
	// status and got told no.
	if !strings.Contains(err.Error(), "legal from done") {
		t.Errorf("the error does not list the legal destinations: %v", err)
	}

	got, err := b.Task(ctx, "T1")
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	if got.Status != model.StatusDone {
		t.Errorf("a refused transition still moved the task to %q", got.Status)
	}
	if n := len(got.Comments); n != 1 {
		t.Errorf("a refused transition left %d comments, want the 1 from setup", n)
	}
}

func TestTransitionOnAnUnknownTaskIsNotFound(t *testing.T) {
	b := seeded(t, "T1")
	err := b.Transition(context.Background(), "nope", model.StatusDone, Note{})
	if !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("got %v, want ErrTaskNotFound", err)
	}
}

// Reading a missing task returns ErrTaskNotFound; writing one does too. The
// pair is easy to get backwards — Python's read returns None and its write
// raises — so both are pinned.
func TestReadingAnUnknownTaskIsNotFound(t *testing.T) {
	b := seeded(t, "T1")
	_, err := b.Task(context.Background(), "nope")
	if !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("got %v, want ErrTaskNotFound", err)
	}
}

func TestTransitionRecordsTheNote(t *testing.T) {
	ctx := context.Background()
	b := seeded(t, "T1")

	at := time.Date(2026, 9, 12, 8, 30, 0, 0, time.UTC)
	err := b.Transition(ctx, "T1", model.StatusBlocked, Note{
		Author: "agent-claude",
		Body:   "Stripe sandbox key still pending from the client.",
		At:     at,
	})
	if err != nil {
		t.Fatalf("Transition: %v", err)
	}

	got, err := b.Task(ctx, "T1")
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	if len(got.Comments) != 1 {
		t.Fatalf("got %d comments, want 1", len(got.Comments))
	}
	var entry map[string]string
	if err := json.Unmarshal(got.Comments[0], &entry); err != nil {
		t.Fatalf("decode the comment: %v", err)
	}
	if entry["author"] != "agent-claude" {
		t.Errorf("author = %q", entry["author"])
	}
	if !strings.Contains(entry["body"], "Stripe sandbox key") {
		t.Errorf("body = %q", entry["body"])
	}
	if entry["at"] != "2026-09-12T08:30:00Z" {
		t.Errorf("at = %q, want the note's own timestamp", entry["at"])
	}
}

// An empty note still records WHEN something moved. Python puts the status
// name in the body; the entry exists even when nobody said why.
func TestEmptyNoteRecordsTheStatusAndTheClock(t *testing.T) {
	ctx := context.Background()
	b := seeded(t, "T1")
	if err := b.Transition(ctx, "T1", model.StatusInProgress, Note{}); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	got, err := b.Task(ctx, "T1")
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	var entry map[string]string
	if err := json.Unmarshal(got.Comments[0], &entry); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if entry["body"] != "in-progress" {
		t.Errorf("body = %q, want the status name", entry["body"])
	}
	if entry["author"] != "orch" {
		t.Errorf("author = %q, want the default", entry["author"])
	}
	if entry["at"] != fixedNow {
		t.Errorf("at = %q, want the backend clock %q", entry["at"], fixedNow)
	}
}

// started_at and finished_at follow Python step for step.
//
// I had this backwards, and the comment asserted the wrong thing confidently:
// `COALESCE(started_at, ?)` keeps the FIRST entry into a state, while
// sqlite_backend.py writes `COALESCE(?, started_at)` and so keeps the MOST
// RECENT. The difference only shows on a reopened task — and `metrics.py`
// reads finished_at for 7-day velocity and for "done in the last N days", so
// the same project would report different velocity depending on which binary
// closed the task.
//
// The expectations are not written by hand. testdata/transition-timestamps.json
// is the output of running the Python backend through these exact five steps
// (see make-timestamps-vector.py), so this test compares against what Python
// does rather than against what I believe it does.
func TestTransitionTimestampsMatchPythonStepByStep(t *testing.T) {
	ctx := context.Background()

	raw, err := os.ReadFile("testdata/transition-timestamps.json") // #nosec G304 -- fixed testdata path
	if err != nil {
		t.Fatalf("read the Python vector: %v", err)
	}
	var vector []struct {
		After      string  `json:"after"`
		Status     string  `json:"status"`
		StartedAt  *string `json:"started_at"`
		FinishedAt *string `json:"finished_at"`
		UpdatedAt  string  `json:"updated_at"`
	}
	if err := json.Unmarshal(raw, &vector); err != nil {
		t.Fatalf("parse the Python vector: %v", err)
	}
	if len(vector) != 5 {
		t.Fatalf("vector has %d steps, want the 5 the generator writes", len(vector))
	}

	// The same five steps the generator ran, in the same order.
	steps := []struct {
		to model.Status
		at string
	}{
		{model.StatusInProgress, "2026-09-01T09:00:00Z"},
		{model.StatusDone, "2026-09-01T11:00:00Z"},
		{model.StatusTodo, "2026-09-05T09:00:00Z"},
		{model.StatusInProgress, "2026-09-05T10:00:00Z"},
		{model.StatusDone, "2026-09-05T12:00:00Z"},
	}

	b := seeded(t, "T1")
	for i, s := range steps {
		at, err := time.Parse(time.RFC3339, s.at)
		if err != nil {
			t.Fatalf("parse %q: %v", s.at, err)
		}
		if err := b.Transition(ctx, "T1", s.to, Note{At: at}); err != nil {
			t.Fatalf("step %d (-> %s): %v", i, s.to, err)
		}

		got, err := b.Task(ctx, "T1")
		if err != nil {
			t.Fatalf("step %d: Task: %v", i, err)
		}
		want := vector[i]
		if string(got.Status) != want.Status {
			t.Errorf("after %s: status = %q, Python says %q", want.After, got.Status, want.Status)
		}
		if got.StartedAt != deref(want.StartedAt) {
			t.Errorf("after %s: started_at = %q, Python says %q",
				want.After, got.StartedAt, deref(want.StartedAt))
		}
		if got.FinishedAt != deref(want.FinishedAt) {
			t.Errorf("after %s: finished_at = %q, Python says %q",
				want.After, got.FinishedAt, deref(want.FinishedAt))
		}
		if got.UpdatedAt != want.UpdatedAt {
			t.Errorf("after %s: updated_at = %q, Python says %q",
				want.After, got.UpdatedAt, want.UpdatedAt)
		}
	}
}

// The headline of the vector, stated on its own so a failure reads as the
// behaviour it protects rather than as "step 5 differs".
func TestAReopenedTaskReportsItsSecondFinish(t *testing.T) {
	ctx := context.Background()
	b := seeded(t, "T1")

	first := time.Date(2026, 9, 1, 11, 0, 0, 0, time.UTC)
	second := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

	for _, s := range []struct {
		to model.Status
		at time.Time
	}{
		{model.StatusInProgress, first.Add(-2 * time.Hour)},
		{model.StatusDone, first},
		{model.StatusTodo, second.Add(-3 * time.Hour)},
		{model.StatusInProgress, second.Add(-2 * time.Hour)},
		{model.StatusDone, second},
	} {
		if err := b.Transition(ctx, "T1", s.to, Note{At: s.at}); err != nil {
			t.Fatalf("-> %s: %v", s.to, err)
		}
	}

	got, err := b.Task(ctx, "T1")
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	if got.FinishedAt != "2026-09-05T12:00:00Z" {
		t.Errorf("finished_at = %q, want the SECOND finish — 7-day velocity "+
			"and `done last N days` both read this column", got.FinishedAt)
	}
	if got.StartedAt != "2026-09-05T10:00:00Z" {
		t.Errorf("started_at = %q, want the SECOND start", got.StartedAt)
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ---- filters ---------------------------------------------------------------

func TestTasksFilter(t *testing.T) {
	ctx := context.Background()
	b := seeded(t, "T1", "T2", "T3", "T4")
	for id, st := range map[string]model.Status{
		"T1": model.StatusDone,
		"T2": model.StatusInProgress,
		"T3": model.StatusBlocked,
	} {
		if err := b.Transition(ctx, id, st, Note{}); err != nil {
			t.Fatalf("setup %s: %v", id, err)
		}
	}

	cases := []struct {
		name   string
		filter TaskFilter
		want   []string
	}{
		{"everything, ordered by id", TaskFilter{}, []string{"T1", "T2", "T3", "T4"}},
		{"one status", TaskFilter{Statuses: []model.Status{model.StatusDone}}, []string{"T1"}},
		{"several statuses", TaskFilter{Statuses: []model.Status{model.StatusDone, model.StatusBlocked}}, []string{"T1", "T3"}},
		{"by id", TaskFilter{IDs: []string{"T4", "T2"}}, []string{"T2", "T4"}},
		{"status and id together", TaskFilter{
			Statuses: []model.Status{model.StatusDone, model.StatusBlocked},
			IDs:      []string{"T3", "T4"},
		}, []string{"T3"}},
		{"no match", TaskFilter{IDs: []string{"nope"}}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := b.Tasks(ctx, c.filter)
			if err != nil {
				t.Fatalf("Tasks: %v", err)
			}
			ids := make([]string, 0, len(got))
			for _, task := range got {
				ids = append(ids, task.ID)
			}
			if strings.Join(ids, ",") != strings.Join(c.want, ",") {
				t.Errorf("got %v, want %v", ids, c.want)
			}
		})
	}
}

// One database holds several projects. Every statement is scoped by
// project_id, and this is what keeps that true.
func TestTasksAreScopedToTheProject(t *testing.T) {
	ctx := context.Background()
	db, _, err := Open(ctx, filepath.Join(t.TempDir(), "orch.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	a := NewSQLite(db, "alpha", "/tmp/alpha")
	z := NewSQLite(db, "zulu", "/tmp/zulu")
	if err := a.Bootstrap(ctx, tasks("A1", "A2")); err != nil {
		t.Fatalf("bootstrap alpha: %v", err)
	}
	if err := z.Bootstrap(ctx, tasks("Z1")); err != nil {
		t.Fatalf("bootstrap zulu: %v", err)
	}

	got, err := a.Tasks(ctx, TaskFilter{})
	if err != nil {
		t.Fatalf("Tasks: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("alpha sees %d tasks, want its own 2", len(got))
	}
	if _, err := a.Task(ctx, "Z1"); !errors.Is(err, ErrTaskNotFound) {
		t.Error("alpha can read zulu's task")
	}
	// And a transition in one project must not touch the other.
	if err := z.Transition(ctx, "Z1", model.StatusDone, Note{}); err != nil {
		t.Fatalf("zulu transition: %v", err)
	}
	a1, err := a.Task(ctx, "A1")
	if err != nil {
		t.Fatalf("read alpha's task after zulu wrote: %v", err)
	}
	if a1.Status != model.StatusTodo {
		t.Errorf("alpha's task moved to %q when zulu transitioned", a1.Status)
	}
}

// ---- events ----------------------------------------------------------------

func TestEventsRoundTripAndDedup(t *testing.T) {
	ctx := context.Background()
	b := seeded(t, "T1")

	e := Event{
		EventType: "dispatch", TaskID: "T1", Backend: "claude",
		TS: "2026-09-11T10:00:00Z", Extra: map[string]any{"pid": 4242},
	}
	for i := 0; i < 3; i++ {
		if err := b.AppendEvent(ctx, "run-1", e); err != nil {
			t.Fatalf("AppendEvent %d: %v", i, err)
		}
	}

	got, err := b.Events(ctx, "T1", 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("appending the same event three times stored %d rows, want 1", len(got))
	}
	if got[0].EventType != "dispatch" || got[0].Backend != "claude" {
		t.Errorf("round trip lost fields: %+v", got[0])
	}
	if pid, ok := got[0].Extra["pid"]; !ok || fmt.Sprint(pid) != "4242" {
		t.Errorf("extra lost the pid: %v", got[0].Extra)
	}
}

// The dedup key includes the timestamp and the pid, so a genuine retry of the
// same task is a different row.
func TestEventsWithDifferentTimestampsAreBothKept(t *testing.T) {
	ctx := context.Background()
	b := seeded(t, "T1")
	for _, ts := range []string{"2026-09-11T10:00:00Z", "2026-09-11T10:05:00Z"} {
		if err := b.AppendEvent(ctx, "run-1", Event{
			EventType: "dispatch", TaskID: "T1", Backend: "claude", TS: ts,
			Extra: map[string]any{"pid": 1},
		}); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
	}
	got, err := b.Events(ctx, "T1", 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("got %d events, want 2", len(got))
	}
}

func TestUnknownEventTypeIsRefused(t *testing.T) {
	b := seeded(t, "T1")
	err := b.AppendEvent(context.Background(), "run-1", Event{
		EventType: "dispatchd", TaskID: "T1", TS: fixedNow, // a typo
	})
	if !errors.Is(err, ErrUnknownEventType) {
		t.Fatalf("got %v, want ErrUnknownEventType", err)
	}
}

// "Show me the last 3" means the newest three, read forwards.
func TestEventsLimitReturnsTheTailInOrder(t *testing.T) {
	ctx := context.Background()
	b := seeded(t, "T1")
	for i := 0; i < 6; i++ {
		if err := b.AppendEvent(ctx, "run-1", Event{
			EventType: "retry", TaskID: "T1",
			TS: fmt.Sprintf("2026-09-11T10:0%d:00Z", i),
		}); err != nil {
			t.Fatalf("AppendEvent %d: %v", i, err)
		}
	}
	got, err := b.Events(ctx, "T1", 3)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d events, want 3", len(got))
	}
	wantTS := []string{"2026-09-11T10:03:00Z", "2026-09-11T10:04:00Z", "2026-09-11T10:05:00Z"}
	for i, w := range wantTS {
		if got[i].TS != w {
			t.Errorf("event %d ts = %q, want %q (the tail, oldest first)", i, got[i].TS, w)
		}
	}
}

func TestEventsForAnUnknownTaskIsEmptyNotAnError(t *testing.T) {
	got, err := seeded(t, "T1").Events(context.Background(), "nope", 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d events for an unknown task", len(got))
	}
}

// ---- spend -----------------------------------------------------------------

func TestSpendRoundTripAndDedup(t *testing.T) {
	ctx := context.Background()
	b := seeded(t, "T1")

	s := Spend{
		TS: "2026-09-11T10:00:00Z", TaskID: "T1", Backend: "claude",
		Model: "claude/claude-sonnet-4-6", TokensIn: 1000, TokensOut: 250,
		CostUSD: 0.42, DurationS: 5400.0,
	}
	for i := 0; i < 3; i++ {
		if err := b.RecordSpend(ctx, s); err != nil {
			t.Fatalf("RecordSpend %d: %v", i, err)
		}
	}

	var n int
	var cost, dur float64
	if err := b.db.read.QueryRowContext(ctx,
		`SELECT COUNT(*), MAX(cost_usd), MAX(duration_s) FROM spend WHERE project_id = 'proj'`,
	).Scan(&n, &cost, &dur); err != nil {
		t.Fatalf("count spend: %v", err)
	}
	if n != 1 {
		t.Errorf("recording the same spend three times stored %d rows, want 1 — "+
			"the budget gate reads these, so a duplicate double-counts", n)
	}
	if cost != 0.42 || dur != 5400.0 {
		t.Errorf("round trip changed the numbers: cost %v duration %v", cost, dur)
	}
}

// The compatibility check with teeth: the hash Go stores must be the one
// Python would have computed for the same row.
func TestSpendHashMatchesThePythonPreimage(t *testing.T) {
	ctx := context.Background()
	b := NewSQLite(newBackend(t).db, "billing-api", "/tmp/billing-api")
	if err := b.Bootstrap(ctx, tasks("F0.T1")); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	// Exactly the row in testdata/orch-py-0.11.0.db.
	if err := b.RecordSpend(ctx, Spend{
		TS: "2026-09-01T10:30:00+00:00", TaskID: "F0.T1", Backend: "claude",
		Model: "claude/claude-sonnet-4-6", TokensIn: 18000, TokensOut: 5200,
		CostUSD: 0.42, DurationS: 5400.0,
	}); err != nil {
		t.Fatalf("RecordSpend: %v", err)
	}
	var got string
	if err := b.db.read.QueryRowContext(ctx,
		`SELECT dedup_hash FROM spend WHERE task_id = 'F0.T1'`).Scan(&got); err != nil {
		t.Fatalf("read hash: %v", err)
	}
	const pythonWrote = "623f4c698b191a2ffa77dc11ce55628be8ef46775985bdde51c2b083f628a2c9"
	if got != pythonWrote {
		t.Errorf("stored %s\nPython would store %s", got, pythonWrote)
	}
}

func TestSpendProjectIDOverride(t *testing.T) {
	ctx := context.Background()
	b := seeded(t, "T1")
	if err := b.RecordSpend(ctx, Spend{
		ProjectID: "other", TS: fixedNow, TaskID: "T1",
		Backend: "claude", Model: "m", CostUSD: 1, DurationS: 1,
	}); err != nil {
		t.Fatalf("RecordSpend: %v", err)
	}
	var pid string
	if err := b.db.read.QueryRowContext(ctx,
		`SELECT project_id FROM spend WHERE task_id = 'T1'`).Scan(&pid); err != nil {
		t.Fatalf("read: %v", err)
	}
	if pid != "other" {
		t.Errorf("project_id = %q, want the explicit override", pid)
	}
}

// ---- dispatches ------------------------------------------------------------

func TestDispatchLifecycle(t *testing.T) {
	ctx := context.Background()
	b := seeded(t, "T1")

	if err := b.StartRun(ctx, "run-1", "auto"); err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	d := Dispatch{
		RunID: "run-1", TaskID: "T1", Backend: "claude", PID: 4242,
		SessionID: "sess-1", StartedAt: fixedNow, PromptPath: "p.md",
		LogPath: "l.log", OutputPath: "o.json",
	}
	if err := b.RecordDispatch(ctx, d); err != nil {
		t.Fatalf("RecordDispatch: %v", err)
	}

	var attempt, pid int
	if err := b.db.read.QueryRowContext(ctx,
		`SELECT attempt, pid FROM dispatches WHERE run_id='run-1' AND task_id='T1'`,
	).Scan(&attempt, &pid); err != nil {
		t.Fatalf("read dispatch: %v", err)
	}
	if attempt != 1 {
		t.Errorf("attempt = %d; an unset attempt should default to 1", attempt)
	}
	if pid != 4242 {
		t.Errorf("pid = %d", pid)
	}

	// A retry replaces the row rather than adding one — the table holds what
	// is in flight, not a history.
	d.Attempt = 2
	d.PID = 4343
	if err := b.RecordDispatch(ctx, d); err != nil {
		t.Fatalf("second RecordDispatch: %v", err)
	}
	var n int
	if err := b.db.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM dispatches WHERE run_id='run-1' AND task_id='T1'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("a retry left %d in-flight rows, want 1", n)
	}

	if err := b.ClearDispatch(ctx, "run-1", "T1"); err != nil {
		t.Fatalf("ClearDispatch: %v", err)
	}
	if err := b.db.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM dispatches WHERE run_id='run-1'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("ClearDispatch left %d rows", n)
	}
}

// ---- doctor ----------------------------------------------------------------

func TestOrphanRowsFindsWhatForeignKeysMissed(t *testing.T) {
	ctx := context.Background()
	b := seeded(t, "T1")

	got, err := b.OrphanRows(ctx)
	if err != nil {
		t.Fatalf("OrphanRows: %v", err)
	}
	if got.Total() != 0 {
		t.Fatalf("a healthy database reported %d orphans: %v", got.Total(), got)
	}

	// Reproduce what a hand-edited database looks like: rows whose project
	// has no row in `projects`. Foreign keys are enforced, so this has to be
	// done with them off — which is exactly how it happens in the wild
	// (sqlite3 CLI, or a bootstrap from before FK enforcement).
	if _, err := b.db.write.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatalf("disable FK: %v", err)
	}
	if _, err := b.db.write.ExecContext(ctx,
		`INSERT INTO tasks_runtime (project_id, task_id, status, comments_json, updated_at)
		 VALUES ('ghost', 'G1', 'todo', '[]', ?), ('ghost', 'G2', 'todo', '[]', ?)`,
		fixedNow, fixedNow); err != nil {
		t.Fatalf("seed orphans: %v", err)
	}

	got, err = b.OrphanRows(ctx)
	if err != nil {
		t.Fatalf("OrphanRows: %v", err)
	}
	if got["tasks_runtime"]["ghost"] != 2 {
		t.Errorf("got %v, want two orphan runtime rows for `ghost`", got)
	}
	if got.Total() != 2 {
		t.Errorf("Total() = %d, want 2", got.Total())
	}

	// Read-only: the operator has to look before anything is deleted.
	var n int
	if err := b.db.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tasks_runtime WHERE project_id = 'ghost'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Errorf("OrphanRows deleted rows; it must only report (F-9)")
	}
}

// ---- the Python database, through the real API -----------------------------

// #103 proved the fixture OPENS. This proves it is usable: the rows Python
// wrote read back through the Backend interface with the right values, and a
// task in it can be transitioned.
func TestPythonWrittenDatabaseIsReadableAndWritable(t *testing.T) {
	ctx := context.Background()
	path := copyPythonFixture(t)
	db, applied, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// 1, not 0: 006 (Go-only, no Python counterpart) still applies on top
	// of a v0.11.0 (schema 5) fixture — see migrate_test.go's
	// TestOpenPythonWrittenDatabaseAppliesOnlyGoOnlyMigrations.
	if applied != 1 {
		t.Fatalf("applied %d migrations to a Python database, want 1", applied)
	}

	b := NewSQLite(db, "billing-api", "/tmp/billing-api")

	got, err := b.Tasks(ctx, TaskFilter{})
	if err != nil {
		t.Fatalf("Tasks: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("read %d tasks from the Python fixture, want 5", len(got))
	}
	want := map[string]model.Status{
		"F0.T1": model.StatusDone, "F0.T2": model.StatusDone,
		"F1.T1": model.StatusInProgress, "F1.T2": model.StatusBlocked,
		"F2.T1": model.StatusTodo,
	}
	for _, task := range got {
		if want[task.ID] != task.Status {
			t.Errorf("%s: read %q, Python wrote %q", task.ID, task.Status, want[task.ID])
		}
	}

	// The blocked task's reason was written by Python as a comment.
	blocked, err := b.Task(ctx, "F1.T2")
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	if len(blocked.Comments) == 0 {
		t.Error("the blocked task's comments did not survive the read")
	}

	// Events Python wrote, read through our API.
	events, err := b.Events(ctx, "F0.T1", 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != 2 {
		t.Errorf("read %d events for F0.T1, want the 2 Python wrote", len(events))
	}

	// And a write: Go must be able to move a task Python created.
	if err := b.Transition(ctx, "F1.T1", model.StatusDone, Note{
		Author: "orch-go", Body: "finished by the Go binary",
	}); err != nil {
		t.Fatalf("Transition on a Python-written task: %v", err)
	}
	after, err := b.Task(ctx, "F1.T1")
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	if after.Status != model.StatusDone {
		t.Errorf("status = %q after the transition", after.Status)
	}
	// Python's own comment history is still there, with ours appended.
	if len(after.Comments) < 2 {
		t.Errorf("got %d comments; the Go write should have appended to "+
			"Python's history, not replaced it", len(after.Comments))
	}
}

// ---- concurrency -----------------------------------------------------------

// The same 50-goroutine shape as the raw-SQL test, but through the real
// Transition path — which does a read and a write in one transaction, so it
// is where a deadlock would actually show up.
func TestConcurrentTransitionsThroughTheBackend(t *testing.T) {
	ctx := context.Background()
	const workers = 50

	ids := make([]string, 0, workers)
	for i := 0; i < workers; i++ {
		ids = append(ids, fmt.Sprintf("T-%03d", i))
	}
	b := seeded(t, ids...)

	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			for _, to := range []model.Status{model.StatusInProgress, model.StatusDone} {
				if err := b.Transition(ctx, id, to, Note{Author: "worker"}); err != nil {
					errs <- fmt.Errorf("%s -> %s: %w", id, to, err)
					return
				}
			}
		}(id)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent transition failed: %v", err)
	}

	done, err := b.Tasks(ctx, TaskFilter{Statuses: []model.Status{model.StatusDone}})
	if err != nil {
		t.Fatalf("Tasks: %v", err)
	}
	if len(done) != workers {
		t.Errorf("%d tasks reached done, want %d", len(done), workers)
	}
}

func TestStartRunIsIdempotentAndValidatesMode(t *testing.T) {
	ctx := context.Background()
	b := seeded(t, "T1")

	if err := b.StartRun(ctx, "run-1", "auto"); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	// Resuming an interrupted run reattaches rather than failing.
	if err := b.StartRun(ctx, "run-1", "auto"); err != nil {
		t.Fatalf("second StartRun: %v", err)
	}
	var n int
	if err := b.db.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM runs WHERE run_id = 'run-1'`).Scan(&n); err != nil {
		t.Fatalf("count runs: %v", err)
	}
	if n != 1 {
		t.Errorf("got %d run rows, want 1", n)
	}

	// The column has a CHECK constraint; the error should name the legal
	// values rather than surfacing a raw constraint failure.
	err := b.StartRun(ctx, "run-2", "turbo")
	if err == nil {
		t.Fatal("an invalid run mode was accepted")
	}
	if !strings.Contains(err.Error(), "auto, semi") {
		t.Errorf("the error does not list the legal modes: %v", err)
	}
}

// A run can be opened on a database that was never bootstrapped — the engine
// may start before tasks.json is read. Without the projects fallback, the
// first dispatch fails on a foreign key with nothing useful in the message.
func TestStartRunSeedsTheProjectRow(t *testing.T) {
	ctx := context.Background()
	b := newBackend(t) // no Bootstrap

	if err := b.StartRun(ctx, "run-1", "semi"); err != nil {
		t.Fatalf("StartRun on a virgin database: %v", err)
	}
	var n int
	if err := b.db.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM projects WHERE project_id = 'proj'`).Scan(&n); err != nil {
		t.Fatalf("count projects: %v", err)
	}
	if n != 1 {
		t.Errorf("got %d project rows, want 1", n)
	}
}

// The guard at the top of Transition: a status string that is not one of the
// five is refused before any database work happens.
//
// Reachable in practice — `orch task set --status <anything>` hands the CLI
// argument straight through, and "skipped" in particular looks plausible
// because it appears in presentation.status_labels.
func TestTransitionRefusesAStatusOutsideTheEnum(t *testing.T) {
	ctx := context.Background()
	cases := []struct{ name, status string }{
		{"a display label that is not a state", "skipped"},
		{"a typo", "in_progress"}, // underscore, not hyphen
		{"empty", ""},
		{"something invented", "halfway"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := seeded(t, "T1")
			err := b.Transition(ctx, "T1", model.Status(c.status), Note{})
			if err == nil {
				t.Fatalf("Transition accepted %q", c.status)
			}
			// It must NOT be reported as an illegal transition: the status
			// is not a state at all, and telling the caller "you cannot go
			// from todo to skipped" would imply skipped exists.
			if errors.Is(err, ErrIllegalTransition) {
				t.Errorf("%q was reported as an illegal transition rather "+
					"than an unknown status: %v", c.status, err)
			}

			// And nothing moved.
			got, err := b.Task(ctx, "T1")
			if err != nil {
				t.Fatalf("Task: %v", err)
			}
			if got.Status != model.StatusTodo {
				t.Errorf("the task moved to %q on a rejected status", got.Status)
			}
			if len(got.Comments) != 0 {
				t.Errorf("a rejected status left %d comments", len(got.Comments))
			}
		})
	}
}
