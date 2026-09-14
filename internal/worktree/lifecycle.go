package worktree

import (
	"errors"
	"fmt"
	"log/slog"
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
// exists" and the task blocks permanently.
//
// Unlike Remove, a failure here is not logged: "the branch doesn't exist"
// (git exits 1) is the expected outcome on every normal Create — logging
// it would mean a WARN line on every single task, training whoever reads
// the log to ignore them. The error is still returned rather than
// swallowed inside this function, so a caller with reason to care about a
// specific failure mode can inspect it.
func (m *Manager) PurgeOrphanBranch(taskID string) error {
	return m.runBestEffort(taskID, "git", "branch", "-D", m.BranchName(taskID))
}

// startPoint is what a new task branch starts from: the remote's base when
// there is a remote to push to, freshly fetched, else the local base.
//
// A task dispatched right after its dependency's PR merged would otherwise
// branch from a local base that never saw the merge; its PR then conflicts,
// and GitHub runs no CI on a conflicting PR (#236). A fetch that fails (no
// network, no such branch on origin) falls back to the local base with a
// warning — a stale start is a conflict to resolve later, not a reason to
// block the task now.
func (m *Manager) startPoint(taskID, baseBranch string) string {
	if !m.pushEnabled {
		return baseBranch
	}
	if _, err := m.run(taskID, "git", "fetch", "origin", baseBranch); err != nil {
		slog.Warn("worktree: fetching the base failed; branching from the local base",
			"task_id", taskID, "base", baseBranch, "error", err)
		return baseBranch
	}
	return "origin/" + baseBranch
}

// Create makes an isolated worktree for taskID branched off baseBranch. A
// stale worktree directory left by a crashed prior run is removed first;
// PurgeOrphanBranch then clears any branch ref a partial prior attempt left
// behind (see its doc comment) before `git worktree add -b` runs.
//
// Both cleanup steps are best-effort — Create can succeed despite either
// failing — but neither error is dropped (rule 19): Remove already logs a
// genuine failure at WARN; PurgeOrphanBranch's routine "branch doesn't
// exist" is logged at DEBUG (see its doc comment for why not WARN). If
// Create goes on to fail anyway, both are folded into the returned error
// via errors.Join so nothing gets lost on the path that actually matters.
func (m *Manager) Create(taskID, baseBranch string) (string, error) {
	wtPath := m.WorktreePath(taskID)
	var cleanupErr error
	if pathExists(wtPath) {
		cleanupErr = m.Remove(taskID)
	}
	if pathExists(wtPath) {
		// This only fires if Remove silently failed to clear a directory it
		// saw as present, which "git worktree remove --force" essentially
		// never does — kept as a hard stop rather than letting `add -b`
		// collide with it.
		return "", errors.Join(
			fmt.Errorf("worktree: stale worktree at %s could not be removed", wtPath),
			cleanupErr,
		)
	}

	worktreesDir := filepath.Join(m.root, ".worktrees")
	if err := os.MkdirAll(worktreesDir, 0o750); err != nil {
		return "", errors.Join(fmt.Errorf("worktree: create %s: %w", worktreesDir, err), cleanupErr)
	}

	purgeErr := m.PurgeOrphanBranch(taskID)
	if purgeErr != nil {
		slog.Debug("worktree: orphan branch purge failed (expected when the task has no prior branch)",
			"task_id", taskID, "error", purgeErr)
	}

	if _, err := m.run(taskID, "git", "worktree", "add", wtPath, "-b", m.BranchName(taskID), m.startPoint(taskID, baseBranch)); err != nil {
		return "", errors.Join(err, cleanupErr, purgeErr)
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
// agent) is removed without complaint. A git failure here is best-effort —
// matching Python, removal must never block a caller cleaning up after a
// task that already failed for its own reason — but is logged (never
// silenced, rule 19) and returned: a non-nil error means "removed with
// warnings" (the active-worktree bookkeeping is cleared either way), not
// that the task itself should be treated as failed.
func (m *Manager) Remove(taskID string) error {
	wtPath := m.WorktreePath(taskID)
	var err error
	if pathExists(wtPath) {
		args := []string{"git", "worktree", "remove", "--force", wtPath}
		if err = m.runBestEffort(taskID, args...); err != nil {
			logGitWarning(taskID, args, err)
		}
	}
	m.mu.Lock()
	delete(m.active, taskID)
	m.mu.Unlock()
	return err
}

// Recreate checks out the already-pushed branch orch/<taskID> into a fresh
// worktree at the usual path, for CI re-dispatch. Unlike Create, it does
// not branch off a base — it fetches and reuses the remote branch a prior
// run already pushed, so downstream code (context file injection, the
// per-attempt spawn) works unchanged regardless of whether the worktree is
// fresh or recreated.
//
// The fetch only moves origin/orch/<taskID>; checking out the local
// orch/<taskID> ref afterwards would silently ignore it whenever the local
// ref is stale or missing commits an operator (or another orch instance)
// pushed straight to origin (#254). `worktree add -B` resets the local
// branch to match the freshly fetched remote ref before checkout, so the
// re-dispatch always starts from what's actually on origin. If origin has
// no such branch, the fetch fails and Recreate returns an error instead of
// falling back to a possibly-stale local branch.
func (m *Manager) Recreate(taskID string) (string, error) {
	// Clean up any stale dir first. Best-effort — Remove already logs a
	// genuine failure at WARN — but folded into the returned error via
	// errors.Join below if Recreate goes on to fail anyway (rule 19).
	cleanupErr := m.Remove(taskID)
	branch := m.BranchName(taskID)
	wtPath := m.WorktreePath(taskID)
	remoteBranch := "origin/" + branch

	if _, err := m.run(taskID, "git", "fetch", "origin", branch); err != nil {
		return "", errors.Join(err, cleanupErr)
	}
	if _, err := m.run(taskID, "git", "worktree", "add", "-B", branch, wtPath, remoteBranch); err != nil {
		return "", errors.Join(err, cleanupErr)
	}

	m.mu.Lock()
	m.active[taskID] = wtPath
	m.mu.Unlock()
	return wtPath, nil
}

// RemoveAll removes every tracked worktree. Called from the run loop's
// shutdown path (SIGTERM/drain), after — not during — the drain wait: see
// the package doc comment's caller-contract section. Returns the task IDs
// that were removed with warnings (Remove's error was non-nil — already
// logged by runBestEffort), if a caller wants to report that; a shutdown
// path that doesn't care can ignore the return.
func (m *Manager) RemoveAll() []string {
	m.mu.Lock()
	ids := make([]string, 0, len(m.active))
	for id := range m.active {
		ids = append(ids, id)
	}
	m.mu.Unlock()

	var withWarnings []string
	for _, id := range ids {
		if err := m.Remove(id); err != nil {
			withWarnings = append(withWarnings, id)
		}
	}
	return withWarnings
}
