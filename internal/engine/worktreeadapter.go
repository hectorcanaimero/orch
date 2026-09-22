package engine

import (
	"context"
	"fmt"

	"github.com/hectorcanaimero/orch/internal/worktree"
)

// WorktreeManager is everything the engine asks of internal/worktree: the
// dispatch side creates one per task, the reap side commits, pushes and
// removes it, the CI poller brings it back, and the drain cleans up.
//
// An interface rather than *worktree.Manager directly so the loop is testable
// without git — the ordering it has to honour is a contract the tests assert,
// and asserting it against real `git worktree add` would make them slow and
// dependent on a repo shape.
type WorktreeManager interface {
	// Create branches taskID's worktree from baseBranch and merges in the
	// branches of deps the base does not contain yet (#322).
	Create(ctx context.Context, taskID, baseBranch string, deps ...string) (string, error)
	CommitPending(ctx context.Context, taskID string) error
	Push(ctx context.Context, taskID string) error
	Remove(ctx context.Context, taskID string) error
	Recreate(ctx context.Context, taskID string) (string, error)
	RemoveAll(ctx context.Context) error
	// BranchName is what a PR is opened from.
	BranchName(taskID string) string
}

// managerAdapter wraps internal/worktree's Manager in the engine's interface.
//
// The shapes differ in three ways, each deliberate on the other side:
// Manager's methods take no context (it bounds every git command with its own
// timeout instead), CommitPending takes the message and reports whether there
// was anything to commit, and RemoveAll returns the ids it failed on rather
// than an error. This is where those meet.
type managerAdapter struct{ m *worktree.Manager }

// NewWorktreeManager adapts a worktree.Manager for the engine.
func NewWorktreeManager(m *worktree.Manager) WorktreeManager { return managerAdapter{m: m} }

func (a managerAdapter) Create(_ context.Context, taskID, baseBranch string, deps ...string) (string, error) {
	return a.m.Create(taskID, baseBranch, deps...)
}

// CommitPending stages and commits whatever the agent left behind.
//
// "Nothing to commit" is not an error: an agent that made no changes is a
// legitimate outcome, and Manager reports it as (false, nil).
func (a managerAdapter) CommitPending(_ context.Context, taskID string) error {
	_, err := a.m.CommitPending(taskID, fmt.Sprintf("%s: orch auto-commit", taskID))
	return err
}

func (a managerAdapter) Push(_ context.Context, taskID string) error { return a.m.Push(taskID) }

func (a managerAdapter) Remove(_ context.Context, taskID string) error {
	return a.m.Remove(taskID)
}

func (a managerAdapter) Recreate(_ context.Context, taskID string) (string, error) {
	return a.m.Recreate(taskID)
}

// RemoveAll reports the worktrees it could not remove, as one error naming
// them — the engine logs it and carries on, because a leftover worktree is a
// tidiness problem and the run is already over.
func (a managerAdapter) RemoveAll(_ context.Context) error {
	if failed := a.m.RemoveAll(); len(failed) > 0 {
		return fmt.Errorf("could not remove %d worktree(s): %v", len(failed), failed)
	}
	return nil
}

func (a managerAdapter) BranchName(taskID string) string { return a.m.BranchName(taskID) }
