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

// TestTaskStatusAndTaskSetExitCodes is a table over both commands' exit
// codes — ported directly from test_task_status_cmd.py / test_task_set_cmd.py
// assertions on `rc == N`. `pre` runs before the assertion (ignoring its own
// exit code) to set up state the case needs, e.g. bootstrapping via
// `status`, or moving a task somewhere first so the real command under test
// can attempt an illegal transition out of it.
func TestTaskStatusAndTaskSetExitCodes(t *testing.T) {
	cases := []struct {
		name string
		pre  [][]string
		args []string
		want int
	}{
		{
			name: "task-status happy path",
			args: []string{"task-status", "F2.T2", "done", "--author", "test", "--note", "finished"},
			want: 0,
		},
		{
			name: "task-status unknown task",
			args: []string{"task-status", "NOPE", "done"},
			want: 2,
		},
		{
			name: "task-status invalid status",
			args: []string{"task-status", "F0.T1", "bogus-status"},
			want: 2,
		},
		{
			// F0.T1 starts `done` in the fixture; done -> in-progress is
			// illegal (must reset to todo first). `pre` bootstraps via
			// `status` so the row exists before task-status is called.
			name: "task-status illegal transition",
			pre:  [][]string{{"status", "--json"}},
			args: []string{"task-status", "F0.T1", "in-progress"},
			want: 3,
		},
		{
			name: "task set with no mutation flag",
			args: []string{"task", "set", "--id", "F0.T1"},
			want: 1,
		},
		{
			name: "task set --status",
			args: []string{"task", "set", "--id", "F2.T2", "--status", "done"},
			want: 0,
		},
		{
			// F0.T1 is `done`; done -> blocked is illegal.
			name: "task set illegal transition",
			args: []string{"task", "set", "--id", "F0.T1", "--status", "blocked"},
			want: 3,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, common := newTestProject(t)
			for _, p := range tc.pre {
				_ = cli.Run("test", append(append([]string{}, p...), common...))
			}
			args := append(append([]string{}, tc.args...), common...)
			if rc := cli.Run("test", args); rc != tc.want {
				t.Fatalf("cli.Run(%v) = %d, want %d", tc.args, rc, tc.want)
			}
		})
	}
}

func TestTaskSetUnimplementedFlagsError(t *testing.T) {
	_, common := newTestProject(t)
	for _, flag := range []string{"--model", "--backend"} {
		args := append([]string{"task", "set", "--id", "F0.T1", flag, "x"}, common...)
		if rc := cli.Run("test", args); rc == 0 {
			t.Errorf("task set %s: rc = 0, want non-zero (not implemented)", flag)
		}
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

// TestResetInvalidOnlyGlobFails covers inProgressCandidates' error path: a
// malformed --only glob (unterminated character class) must fail loudly,
// not silently match nothing — same rule status.go's --only follows.
func TestResetInvalidOnlyGlobFails(t *testing.T) {
	_, common := newTestProject(t)
	statusArgs := append([]string{"status", "--json"}, common...)
	if rc := cli.Run("test", statusArgs); rc != 0 {
		t.Fatalf("bootstrap via status: rc = %d, want 0", rc)
	}
	args := append([]string{"reset", "--only", "F0["}, common...)
	if rc := cli.Run("test", args); rc == 0 {
		t.Fatalf("reset --only 'F0[': rc = 0, want non-zero (malformed glob)")
	}
}
