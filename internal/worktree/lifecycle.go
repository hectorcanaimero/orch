package worktree

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PurgeOrphanBranch best-effort deletes the local branch for taskID's
// worktree. Exported (not just inlined into Create) so a caller — or a
// test — can reproduce the F-7 cleanup (issue #71) on its own: a prior
// `git worktree add -b` may have created the branch ref before failing
// (rate limit, sqlite contention, a partial disk write), and Remove only
// cleans up the worktree directory, leaving the branch behind. Without
// purging it first, the next Create fails with "a branch named ... already
// exists" and the task blocks permanently. The result is discarded
// entirely — "the branch doesn't exist" is the healthy, common case.
func (m *Manager) PurgeOrphanBranch(taskID string) {
	m.runBestEffort(taskID, "git", "branch", "-D", m.BranchName(taskID))
}

// Create makes an isolated worktree for taskID branched off baseBranch. A
// stale worktree directory left by a crashed prior run is removed first;
// PurgeOrphanBranch then clears any branch ref a partial prior attempt left
// behind (see its doc comment) before `git worktree add -b` runs.
func (m *Manager) Create(taskID, baseBranch string) (string, error) {
	wtPath := m.WorktreePath(taskID)
	if pathExists(wtPath) {
		m.Remove(taskID)
	}
	if pathExists(wtPath) {
		// Remove is best-effort; this only fires if it silently failed to
		// clear a directory it saw as present, which "git worktree remove
		// --force" essentially never does — kept as a hard stop rather
		// than letting `add -b` collide with it.
		return "", fmt.Errorf("worktree: stale worktree at %s could not be removed", wtPath)
	}

	worktreesDir := filepath.Join(m.root, ".worktrees")
	if err := os.MkdirAll(worktreesDir, 0o750); err != nil {
		return "", fmt.Errorf("worktree: create %s: %w", worktreesDir, err)
	}

	m.PurgeOrphanBranch(taskID)

	if _, err := m.run(taskID, "git", "worktree", "add", wtPath, "-b", m.BranchName(taskID), baseBranch); err != nil {
		return "", err
	}

	m.mu.Lock()
	m.active[taskID] = wtPath
	m.mu.Unlock()
	return wtPath, nil
}

// CommitPending stages and commits any uncommitted changes inside taskID's
// worktree, using an inline git identity so it works even when the
// project's own git config has none set. Returns false (no error) when the
// tree was already clean. Ports commit_pending (Sprint F-6, fix #60): the
// dispatched agent writes files but is never asked to commit them, so
// without this step Push would send an empty branch and Remove would
// silently discard the agent's output.
func (m *Manager) CommitPending(taskID, message string) (bool, error) {
	wt := m.WorktreePath(taskID)

	if _, err := m.run(taskID, "git", "-C", wt, "add", "-A"); err != nil {
		return false, err
	}
	status, err := m.run(taskID, "git", "-C", wt, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(status) == "" {
		return false, nil
	}
	if _, err := m.run(taskID, "git", "-C", wt,
		"-c", "user.email=orch@local", "-c", "user.name=orch",
		"commit", "-m", message,
	); err != nil {
		return false, err
	}
	return true, nil
}

// Push pushes taskID's branch to origin with --force-with-lease, so a
// retried task overwrites its own previous attempt's branch without
// clobbering unrelated remote changes. A no-op when the Manager was built
// with pushEnabled=false (no remote configured).
func (m *Manager) Push(taskID string) error {
	if !m.pushEnabled {
		return nil
	}
	_, err := m.run(taskID, "git", "push", "--force-with-lease", "-u", "origin", m.BranchName(taskID))
	return err
}

// Remove deletes taskID's worktree directory. A no-op if it's already
// absent. Uses --force so a dirty tree (untracked files left by a failed
// agent) is removed without complaint, and swallows any git error — this
// is best-effort cleanup, matching Python (removal must never block a
// caller cleaning up after a task that already failed for its own reason).
func (m *Manager) Remove(taskID string) {
	wtPath := m.WorktreePath(taskID)
	if pathExists(wtPath) {
		m.runBestEffort(taskID, "git", "worktree", "remove", "--force", wtPath)
	}
	m.mu.Lock()
	delete(m.active, taskID)
	m.mu.Unlock()
}

// Recreate checks out the already-pushed branch orch/<taskID> into a fresh
// worktree at the usual path, for CI re-dispatch. Unlike Create, it does
// not branch off a base — it fetches and reuses the remote branch a prior
// run already pushed, so downstream code (context file injection, the
// per-attempt spawn) works unchanged regardless of whether the worktree is
// fresh or recreated.
func (m *Manager) Recreate(taskID string) (string, error) {
	m.Remove(taskID) // clean up any stale dir first
	branch := m.BranchName(taskID)
	wtPath := m.WorktreePath(taskID)

	if _, err := m.run(taskID, "git", "fetch", "origin", branch); err != nil {
		return "", err
	}
	if _, err := m.run(taskID, "git", "worktree", "add", wtPath, branch); err != nil {
		return "", err
	}

	m.mu.Lock()
	m.active[taskID] = wtPath
	m.mu.Unlock()
	return wtPath, nil
}

// RemoveAll removes every tracked worktree. Called from the run loop's
// shutdown path (SIGTERM/drain), after — not during — the drain wait: see
// the package doc comment's caller-contract section.
func (m *Manager) RemoveAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.active))
	for id := range m.active {
		ids = append(ids, id)
	}
	m.mu.Unlock()

	for _, id := range ids {
		m.Remove(id)
	}
}
