package worktree

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureSlogWarnings redirects the default slog logger to buf for the
// duration of the test, restoring the previous default on cleanup.
func captureSlogWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// TestRemoveWarnsButIsNotFatalWhenGitRefusesTheRemoval reproduces the
// PR #123 review finding: Remove's underlying git command can fail (here,
// because the worktree's registration under the main repo's .git/worktrees
// was deleted out from under it while the directory itself survives) and
// that must surface as a logged warning plus a non-nil return, never a
// panic or a fatal error — Remove's whole contract is best-effort cleanup.
func TestRemoveWarnsButIsNotFatalWhenGitRefusesTheRemoval(t *testing.T) {
	root := newTestRepo(t)
	m := NewManager(root, true)
	wtPath, err := m.Create("F2.1.T1", "main")
	if err != nil {
		t.Fatal(err)
	}

	// Corrupt git's own bookkeeping for this worktree without touching the
	// directory on disk: pathExists(wtPath) still sees it, but git no
	// longer recognizes it as one of its worktrees, so `git worktree
	// remove --force` fails against a directory that's still right there.
	adminDir := filepath.Join(root, ".git", "worktrees", "F2.1.T1")
	if err := os.RemoveAll(adminDir); err != nil {
		t.Fatal(err)
	}

	buf := captureSlogWarnings(t)
	err = m.Remove("F2.1.T1")

	if err == nil {
		t.Fatalf("expected Remove to report the underlying git failure")
	}
	if !strings.Contains(buf.String(), "level=WARN") {
		t.Errorf("expected a WARN log line, got:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "F2.1.T1") {
		t.Errorf("warning log missing task_id, got:\n%s", buf.String())
	}
	// Cleanup bookkeeping still happens — this is best-effort, not fatal.
	if _, ok := m.active["F2.1.T1"]; ok {
		t.Errorf("active still tracks F2.1.T1 after a warned Remove")
	}
	if _, statErr := os.Stat(wtPath); statErr != nil {
		t.Logf("worktree directory gone despite the warning (also acceptable): %v", statErr)
	}
}
