package project_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/project"
	"github.com/hectorcanaimero/orch/internal/prompt"
	"github.com/hectorcanaimero/orch/internal/state"
	"github.com/hectorcanaimero/orch/internal/vcs"
	"github.com/hectorcanaimero/orch/internal/worktree"
)

// fakeMerger records the merges asked of it; the rest of vcs.Provider is
// never called.
type fakeMerger struct {
	vcs.Provider
	merged []string
	err    error
}

func (f *fakeMerger) MergePR(prURL string, squash, auto bool) error {
	if !squash || auto {
		return errors.New("want a squash merge, now")
	}
	f.merged = append(f.merged, prURL)
	return f.err
}

func reviewBackend(t *testing.T, status model.Status) state.Backend {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	db, _, err := state.Open(ctx, filepath.Join(dir, "orch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	backend := state.NewSQLite(db, "p", dir)
	if err := backend.Bootstrap(ctx, []model.Task{{ID: "T-1", Title: "t", Model: "claude/opus", Status: model.StatusTodo}}); err != nil {
		t.Fatal(err)
	}
	if status != model.StatusTodo {
		if err := backend.Transition(ctx, "T-1", status, state.Note{}); err != nil {
			t.Fatal(err)
		}
	}
	return backend
}

// TestAcceptMergesTheTasksPR: #454. Accept squash-merges the task's PR now
// and records the merge as the task's event.
func TestAcceptMergesTheTasksPR(t *testing.T) {
	ctx := context.Background()
	backend := reviewBackend(t, model.StatusDone)
	if err := backend.SetTaskPR(ctx, "T-1", "https://github.com/o/r/pull/7"); err != nil {
		t.Fatal(err)
	}
	fake := &fakeMerger{}
	steer := project.Steer{Backend: backend, StateDir: t.TempDir(), Source: "dashboard", Provider: fake}
	msg, err := steer.Accept(ctx, "T-1")
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if len(fake.merged) != 1 || fake.merged[0] != "https://github.com/o/r/pull/7" {
		t.Errorf("merged %v, want the task's PR once", fake.merged)
	}
	if !strings.Contains(msg, "merged") {
		t.Errorf("message %q does not say it merged", msg)
	}
	events, err := backend.Events(ctx, "T-1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 || events[len(events)-1].EventType != project.EventPRMerged {
		t.Errorf("events %+v, want a pr_merged last", events)
	}
}

// TestAcceptWithoutAPRMergesNothing: a task that never opened a PR is said
// so rather than merged.
func TestAcceptWithoutAPRMergesNothing(t *testing.T) {
	ctx := context.Background()
	fake := &fakeMerger{}
	steer := project.Steer{Backend: reviewBackend(t, model.StatusDone), StateDir: t.TempDir(), Provider: fake}
	if _, err := steer.Accept(ctx, "T-1"); !errors.Is(err, project.ErrNoPR) {
		t.Errorf("Accept = %v, want ErrNoPR", err)
	}
	if len(fake.merged) != 0 {
		t.Errorf("merged %v", fake.merged)
	}
}

// TestSendBackNotesAndReopensTheTask: the note reaches the next attempt and
// a done task is todo again, for the run to dispatch.
func TestSendBackNotesAndReopensTheTask(t *testing.T) {
	ctx := context.Background()
	backend := reviewBackend(t, model.StatusDone)
	steer := project.Steer{Backend: backend, StateDir: t.TempDir(), Source: "cli"}
	if _, err := steer.SendBack(ctx, "T-1", "keep the old flag"); err != nil {
		t.Fatalf("SendBack: %v", err)
	}
	got, err := backend.Task(ctx, "T-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.StatusTodo {
		t.Errorf("status %s, want todo", got.Status)
	}
	notes := prompt.OperatorNotes(got.Comments)
	if len(notes) != 1 || notes[0] != "keep the old flag" {
		t.Errorf("notes %v, want the operator's", notes)
	}
}

// TestBlockingADoneTaskReopensItFirst: the Changes tab's Block on finished
// work. done only reopens to todo, so the move goes through it.
func TestBlockingADoneTaskReopensItFirst(t *testing.T) {
	ctx := context.Background()
	backend := reviewBackend(t, model.StatusDone)
	steer := project.Steer{Backend: backend, StateDir: t.TempDir(), Source: "dashboard"}
	if _, err := steer.Move(ctx, "T-1", model.StatusBlocked, "the migration is wrong"); err != nil {
		t.Fatalf("Move: %v", err)
	}
	got, err := backend.Task(ctx, "T-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.StatusBlocked {
		t.Errorf("status %s, want blocked", got.Status)
	}
}

// TestAcceptThatDoesNotMergeRecordsNothing: a merge gh refuses is the
// operator's error, with gh's reason, and no pr_merged is written.
func TestAcceptThatDoesNotMergeRecordsNothing(t *testing.T) {
	ctx := context.Background()
	backend := reviewBackend(t, model.StatusDone)
	if err := backend.SetTaskPR(ctx, "T-1", "https://github.com/o/r/pull/7"); err != nil {
		t.Fatal(err)
	}
	refused := &vcs.MergeError{Detail: "Pull request is not mergeable: the merge commit cannot be cleanly created"}
	steer := project.Steer{Backend: backend, StateDir: t.TempDir(), Provider: &fakeMerger{err: refused}}
	_, err := steer.Accept(ctx, "T-1")
	if !errors.Is(err, vcs.ErrMergeFailed) || !strings.Contains(err.Error(), "pull/7") {
		t.Errorf("Accept = %v, want the merge failure naming the PR", err)
	}
	events, err := backend.Events(ctx, "T-1", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.EventType == project.EventPRMerged {
			t.Errorf("a refused merge recorded %+v", e)
		}
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	// #nosec G204 -- this file's own fixed git subcommands.
	cmd := exec.Command("git", append([]string{"-c", "user.email=test@example.test", "-c", "user.name=test"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// localSteer is a local-mode project with no remote: T-1 has a worktree on
// orch/T-1 holding an uncommitted edit of file, and main is checked out at
// the root.
func localSteer(t *testing.T, file string) (project.Steer, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatal(err)
	}
	gitOut(t, root, "init", "-q", "-b", "main")
	tasks := filepath.Join(root, "tasks.json")
	if err := os.WriteFile(tasks, []byte(`{"meta":{"project":"p"},"phases":[],"tasks":[{"id":"T-1","phase":1,"title":"Add the flag","model":"claude/opus","specRef":"specs/f1.md"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("initial\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitOut(t, root, "add", "-A")
	gitOut(t, root, "commit", "-q", "-m", "initial")
	wt, err := worktree.NewManager(root, false).Create("T-1", "main")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, file), []byte("from T-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return project.Steer{Backend: reviewBackend(t, model.StatusDone), StateDir: t.TempDir(), Source: "cli",
		VCS: vcs.Config{Provider: "local"}, Root: root, BaseBranch: "main", TasksJSON: tasks}, root
}

// TestAcceptLandsLocally: with vcs.provider local and no remote, Accept
// commits the task's pending work and squash-merges it into main with the
// engine's message, and records merged_locally.
func TestAcceptLandsLocally(t *testing.T) {
	ctx := context.Background()
	steer, root := localSteer(t, "flag.go")
	msg, err := steer.Accept(ctx, "T-1")
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(root, "flag.go")); err != nil || string(b) != "from T-1\n" { // #nosec G304 -- the test repo
		t.Errorf("flag.go on main = %q, %v; want the task's work", b, err)
	}
	if got := gitOut(t, root, "log", "-1", "--format=%B", "main"); !strings.HasPrefix(got, "Add the flag\n\nTask: `T-1`\nSpec: specs/f1.md") {
		t.Errorf("squash message = %q, want the engine's", got)
	}
	head := gitOut(t, root, "rev-parse", "--short", "main")
	if !strings.Contains(msg, "main") || !strings.Contains(msg, head) {
		t.Errorf("message %q does not name main and %s", msg, head)
	}
	got, err := steer.Backend.Task(ctx, "T-1")
	if err != nil || got.Status != model.StatusDone {
		t.Errorf("status %v, %v; want done", got.Status, err)
	}
	events, err := steer.Backend.Events(ctx, "T-1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 || events[len(events)-1].EventType != project.EventMergedLocally {
		t.Errorf("events %+v, want a merged_locally last", events)
	}
}

// TestAcceptLocallyConflictChangesNothing: work that conflicts with main is
// an error naming the file; main, the task and its events stay as they were.
func TestAcceptLocallyConflictChangesNothing(t *testing.T) {
	ctx := context.Background()
	steer, root := localSteer(t, "README.md")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("from main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitOut(t, root, "commit", "-q", "-am", "main moved")
	before := gitOut(t, root, "rev-parse", "main")
	_, err := steer.Accept(ctx, "T-1")
	if err == nil || !strings.Contains(err.Error(), "README.md") {
		t.Fatalf("Accept = %v, want the conflict naming README.md", err)
	}
	if gitOut(t, root, "rev-parse", "main") != before {
		t.Error("a conflicting Accept moved main")
	}
	gitOut(t, root, "rev-parse", "--verify", "orch/T-1")
	events, err := steer.Backend.Events(ctx, "T-1", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.EventType == project.EventMergedLocally {
			t.Errorf("a conflicting Accept recorded %+v", e)
		}
	}
}
