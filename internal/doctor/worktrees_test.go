package doctor

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func runGitDoctor(t *testing.T, dir string, args ...string) {
	t.Helper()
	// #nosec G204 -- args are this test file's own fixed git subcommands.
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

func TestCheckOrphanWorktreesCleanRepo(t *testing.T) {
	root := newTestGitRepo(t)
	c := CheckOrphanWorktrees(root)
	if c.Status != StatusOK {
		t.Errorf("c = %+v, want ok", c)
	}
}

func TestCheckOrphanWorktreesDetectsUnregisteredDir(t *testing.T) {
	root := newTestGitRepo(t)
	stale := filepath.Join(root, ".worktrees", "F1.T1")
	if err := os.MkdirAll(stale, 0o750); err != nil {
		t.Fatal(err)
	}

	c := CheckOrphanWorktrees(root)
	if c.Status != StatusWarn {
		t.Errorf("c = %+v, want warn", c)
	}
}

func TestCheckOrphanWorktreesDetectsBranchWithNoWorktree(t *testing.T) {
	root := newTestGitRepo(t)
	runGitDoctor(t, root, "branch", "orch/F2.T1")

	c := CheckOrphanWorktrees(root)
	if c.Status != StatusWarn {
		t.Errorf("c = %+v, want warn", c)
	}
}

func TestCheckOrphanWorktreesRegisteredWorktreeIsNotOrphan(t *testing.T) {
	root := newTestGitRepo(t)
	wtPath := filepath.Join(root, ".worktrees", "F3.T1")
	runGitDoctor(t, root, "worktree", "add", wtPath, "-b", "orch/F3.T1")

	c := CheckOrphanWorktrees(root)
	if c.Status != StatusOK {
		t.Errorf("c = %+v, want ok (registered worktree + matching branch)", c)
	}
}
