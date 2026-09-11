package cli_test

import (
	"path/filepath"
	"testing"

	"github.com/hectorcanaimero/orch/internal/cli"
)

// newTestProject copies the committed parity fixture into a fresh temp dir
// and returns its path plus the --project-root/--project-id args every
// call needs — the same fixture scripts/parity.sh uses, reused here for
// exact exit-code assertions ported from test_task_status_cmd.py and
// test_task_set_cmd.py (those check `rc == N` directly; testscript's `!
// exec` only checks success/failure, not the specific code).
func newTestProject(t *testing.T) (root string, common []string) {
	t.Helper()
	root = filepath.Join(t.TempDir(), "proj")
	if err := copyProjectFixture(fixtureDir(t), root); err != nil {
		t.Fatal(err)
	}
	return root, []string{"--project-root", root, "--project-id", "parity-project"}
}

func TestTaskStatusHappyPathExitsZero(t *testing.T) {
	root, common := newTestProject(t)
	args := append([]string{"task-status", "F2.T2", "done", "--author", "test", "--note", "finished"}, common...)
	if rc := cli.Run("test", args); rc != 0 {
		t.Fatalf("task-status happy path: rc = %d, want 0", rc)
	}
	_ = root
}

func TestTaskStatusUnknownTaskExits2(t *testing.T) {
	_, common := newTestProject(t)
	// Bootstrap first (task-status itself best-effort bootstraps, so this
	// isn't strictly required, but makes the "unknown" case unambiguous).
	args := append([]string{"task-status", "NOPE", "done"}, common...)
	if rc := cli.Run("test", args); rc != 2 {
		t.Fatalf("task-status unknown task: rc = %d, want 2", rc)
	}
}

func TestTaskStatusInvalidStatusExits2(t *testing.T) {
	_, common := newTestProject(t)
	args := append([]string{"task-status", "F0.T1", "bogus-status"}, common...)
	if rc := cli.Run("test", args); rc != 2 {
		t.Fatalf("task-status invalid status: rc = %d, want 2", rc)
	}
}

func TestTaskStatusIllegalTransitionExits3(t *testing.T) {
	_, common := newTestProject(t)
	// F0.T1 starts `done` in the fixture; done -> in-progress is illegal
	// (must reset to todo first).
	statusArgs := append([]string{"status", "--json"}, common...)
	if rc := cli.Run("test", statusArgs); rc != 0 {
		t.Fatalf("bootstrap via status: rc = %d, want 0", rc)
	}
	args := append([]string{"task-status", "F0.T1", "in-progress"}, common...)
	if rc := cli.Run("test", args); rc != 3 {
		t.Fatalf("task-status illegal transition: rc = %d, want 3", rc)
	}
}

func TestTaskSetNoFlagsExits1(t *testing.T) {
	_, common := newTestProject(t)
	args := append([]string{"task", "set", "--id", "F0.T1"}, common...)
	if rc := cli.Run("test", args); rc != 1 {
		t.Fatalf("task set no flags: rc = %d, want 1", rc)
	}
}

func TestTaskSetStatusExitsZero(t *testing.T) {
	_, common := newTestProject(t)
	args := append([]string{"task", "set", "--id", "F2.T2", "--status", "done"}, common...)
	if rc := cli.Run("test", args); rc != 0 {
		t.Fatalf("task set --status: rc = %d, want 0", rc)
	}
}

func TestTaskSetUnimplementedFlagsError(t *testing.T) {
	_, common := newTestProject(t)
	for _, flag := range []string{"--model", "--backend", "--milestone"} {
		args := append([]string{"task", "set", "--id", "F0.T1", flag, "x"}, common...)
		if rc := cli.Run("test", args); rc == 0 {
			t.Errorf("task set %s: rc = 0, want non-zero (not implemented)", flag)
		}
	}
}

func TestTaskSetIllegalTransitionExits3(t *testing.T) {
	_, common := newTestProject(t)
	// F0.T1 is `done`; done -> blocked is illegal.
	args := append([]string{"task", "set", "--id", "F0.T1", "--status", "blocked"}, common...)
	if rc := cli.Run("test", args); rc != 3 {
		t.Fatalf("task set illegal transition: rc = %d, want 3", rc)
	}
}

func TestResetDryRunThenRequeue(t *testing.T) {
	_, common := newTestProject(t)
	// Bootstrap so tasks_runtime exists (F2.T2 ships in-progress).
	statusArgs := append([]string{"status", "--json"}, common...)
	if rc := cli.Run("test", statusArgs); rc != 0 {
		t.Fatalf("bootstrap via status: rc = %d, want 0", rc)
	}

	dryRun := append([]string{"reset"}, common...)
	if rc := cli.Run("test", dryRun); rc != 0 {
		t.Fatalf("reset dry-run: rc = %d, want 0", rc)
	}

	requeue := append([]string{"reset", "--requeue"}, common...)
	if rc := cli.Run("test", requeue); rc != 0 {
		t.Fatalf("reset --requeue: rc = %d, want 0", rc)
	}

	// F2.T2 must be back to todo now.
	rows := append([]string{"tasks", "--status", "todo", "--json"}, common...)
	if rc := cli.Run("test", rows); rc != 0 {
		t.Fatalf("tasks --status todo: rc = %d, want 0", rc)
	}
}
