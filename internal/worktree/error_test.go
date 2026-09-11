package worktree

import (
	"strings"
	"testing"
)

func TestErrorMessageIncludesTaskIDAndCommand(t *testing.T) {
	err := &Error{TaskID: "F2.1.T3", Cmd: []string{"git", "push"}, Stderr: "remote: rejected"}
	msg := err.Error()
	for _, want := range []string{"F2.1.T3", "git push", "remote: rejected"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Error() = %q, missing %q", msg, want)
		}
	}
}

func TestErrorMessageTruncatesLongStderr(t *testing.T) {
	long := strings.Repeat("x", 500)
	err := &Error{TaskID: "F1", Cmd: []string{"git", "x"}, Stderr: long}
	msg := err.Error()
	if strings.Contains(msg, long) {
		t.Errorf("Error() did not truncate a 500-char stderr")
	}
	if !strings.Contains(msg, strings.Repeat("x", 200)) {
		t.Errorf("Error() should still contain the first 200 chars of stderr")
	}
}

// CommitPending's "add" step is the one command a caller can realistically
// make fail without corrupting the repo out from under the test: pointing
// it at a task whose worktree was never created.
func TestCommitPendingFailsWhenWorktreeMissing(t *testing.T) {
	root := newTestRepo(t)
	m := NewManager(root, true)

	if _, err := m.CommitPending("never-created", "msg"); err == nil {
		t.Fatal("expected an error committing in a worktree that doesn't exist")
	}
}
