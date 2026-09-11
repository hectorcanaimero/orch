package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// newTestRepo creates a real git repo at t.TempDir() with one commit on a
// branch named "main" (pinned via symbolic-ref rather than relying on
// init.defaultBranch, which varies by git version/config) and returns its
// path. Every WorktreeManager method this package exports runs real git
// commands — no subprocess mocking — per the G4.1 brief.
func newTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	runGit(t, dir, "symbolic-ref", "HEAD", "refs/heads/main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("initial\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "-m", "initial")
	return dir
}

// newBareRemote creates a bare repo to act as "origin" and wires it into
// project (the path newTestRepo returned).
func newBareRemote(t *testing.T, project string) string {
	t.Helper()
	remote := filepath.Join(t.TempDir(), "remote.git")
	runGit(t, "", "init", "-q", "--bare", remote)
	runGit(t, project, "remote", "add", "origin", remote)
	return remote
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	// #nosec G204 -- args are this test file's own fixed git subcommands, not external input.
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v (dir=%s) failed: %v\n%s", args, dir, err, out)
	}
	return string(out)
}

// branchExists reports whether a local branch exists in the repo at dir.
func branchExists(t *testing.T, dir, branch string) bool {
	t.Helper()
	// #nosec G204 -- branch is a task/branch name this test file constructed itself.
	cmd := exec.Command("git", "branch", "--list", branch)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git branch --list %s failed: %v\n%s", branch, err, out)
	}
	return len(out) > 0
}

func remoteHasBranch(t *testing.T, remote, branch string) bool {
	t.Helper()
	// #nosec G204 -- remote/branch are paths and names this test file constructed itself.
	cmd := exec.Command("git", "ls-remote", "--heads", remote, branch)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git ls-remote failed: %v\n%s", err, out)
	}
	return len(out) > 0
}
