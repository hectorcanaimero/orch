package doctor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// Worktrees live next to the project since #249: `<root>.worktrees/<id>`.
func TestCheckOrphanWorktreesDetectsUnregisteredDir(t *testing.T) {
	root := newTestGitRepo(t)
	stale := filepath.Join(root+".worktrees", "F1.T1")
	if err := os.MkdirAll(stale, 0o750); err != nil {
		t.Fatal(err)
	}

	c := CheckOrphanWorktrees(root)
	if c.Status != StatusWarn || !strings.Contains(c.Detail, "F1.T1") {
		t.Errorf("c = %+v, want warn naming F1.T1", c)
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
	wtPath := filepath.Join(root+".worktrees", "F3.T1")
	runGitDoctor(t, root, "worktree", "add", wtPath, "-b", "orch/F3.T1")

	c := CheckOrphanWorktrees(root)
	if c.Status != StatusOK {
		t.Errorf("c = %+v, want ok (registered worktree + matching branch)", c)
	}
}

// A worktree an older orch left inside the checkout is reported even while
// git still knows it: in there it mixes the project's dependency tree into
// the task's (#249), and no current run will reuse it.
func TestCheckOrphanWorktreesReportsTheOldLayout(t *testing.T) {
	root := newTestGitRepo(t)
	runGitDoctor(t, root, "worktree", "add", filepath.Join(root, ".worktrees", "F4.T1"), "-b", "orch/F4.T1")

	c := CheckOrphanWorktrees(root)
	if c.Status != StatusWarn || !strings.Contains(c.Detail, "F4.T1") || !strings.Contains(c.Detail, "older orch") {
		t.Errorf("c = %+v, want warn naming the leftover F4.T1 from an older orch", c)
	}
	// Its branch still has a worktree, so it is not ALSO an orphan branch.
	if strings.Contains(c.Detail, "no live worktree") {
		t.Errorf("c = %+v, the leftover's branch was counted as orphaned too", c)
	}
}

func TestCheckOrphanWorktreesWarnsWhenWorktreesDirUnreadable(t *testing.T) {
	for name, dir := range map[string]func(root string) string{
		"sibling": func(root string) string { return root + ".worktrees" },
		"old":     func(root string) string { return filepath.Join(root, ".worktrees") },
	} {
		t.Run(name, func(t *testing.T) {
			root := newTestGitRepo(t)
			// A plain file where the directory should be makes ReadDir
			// fail with something other than "not exist" — the read
			// failure rule 19 says must not read as "found zero orphans".
			if err := os.WriteFile(dir(root), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}

			c := CheckOrphanWorktrees(root)
			if c.Status != StatusWarn || !strings.Contains(c.Detail, "could not read") {
				t.Errorf("c = %+v, want a warn saying it could not read the directory", c)
			}
		})
	}
}
