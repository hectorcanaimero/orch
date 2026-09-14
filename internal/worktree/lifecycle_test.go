package worktree

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---- Create -----------------------------------------------------------

func TestCreateReturnsWorktreePathAndRegistersBranch(t *testing.T) {
	root := newTestRepo(t)
	m := NewManager(root, true)

	path, err := m.Create("F2.1.T1", "main")
	if err != nil {
		t.Fatal(err)
	}
	if path != m.WorktreePath("F2.1.T1") {
		t.Errorf("path = %q, want %q", path, m.WorktreePath("F2.1.T1"))
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("worktree dir not created: %v", err)
	}
	if !branchExists(t, root, "orch/F2.1.T1") {
		t.Errorf("branch orch/F2.1.T1 was not created")
	}
	if got := m.active["F2.1.T1"]; got != path {
		t.Errorf("active[%q] = %q, want %q", "F2.1.T1", got, path)
	}
}

// #236: a task dispatched right after its dependency's PR merged branched
// from the LOCAL base, which had never fetched that merge. Its PR conflicted,
// GitHub ran no CI on it, and the poller waited forever. With a remote, the
// worktree starts from the remote's base.
func TestCreateBranchesFromTheFreshRemoteBase(t *testing.T) {
	root := newTestRepo(t)
	remote := newBareRemote(t, root)
	runGit(t, root, "push", "-q", "origin", "main")

	// The dependency merges on the remote; the project never pulls.
	other := filepath.Join(t.TempDir(), "other")
	runGit(t, "", "clone", "-q", "-b", "main", remote, other)
	runGit(t, other, "config", "user.email", "test@example.com")
	runGit(t, other, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(other, "dep.txt"), []byte("merged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, other, "add", "-A")
	runGit(t, other, "commit", "-q", "-m", "the dependency")
	runGit(t, other, "push", "-q", "origin", "main")

	m := NewManager(root, true)
	wt, err := m.Create("F2.1.T9", "main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(wt, "dep.txt")); err != nil {
		t.Errorf("the worktree lacks the dependency merged on origin: %v", err)
	}
}

// With push disabled (--no-push, or no remote to speak of) nothing is
// fetched: the local base is all there is.
func TestCreateUsesTheLocalBaseWhenPushIsDisabled(t *testing.T) {
	root := newTestRepo(t)
	newBareRemote(t, root) // configured but empty: a fetch would fail
	m := NewManager(root, false)
	if _, err := m.Create("F2.1.T9", "main"); err != nil {
		t.Fatalf("Create with push disabled: %v", err)
	}
}

func TestCreateRecreatesOverAPriorRealWorktree(t *testing.T) {
	// Mirrors Python's test_create_cleans_stale_path_first, but against a
	// real prior worktree (a plain leftover directory isn't something git
	// worktree remove can clean up, so a genuine stale worktree is the only
	// faithful way to exercise this without mocking subprocess).
	root := newTestRepo(t)
	m := NewManager(root, true)

	first, err := m.Create("F2.1.T1", "main")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(first, "scratch.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	second, err := m.Create("F2.1.T1", "main")
	if err != nil {
		t.Fatalf("second Create failed: %v", err)
	}
	if second != first {
		t.Errorf("path changed across recreation: %q vs %q", first, second)
	}
	if _, err := os.Stat(filepath.Join(second, "scratch.txt")); err == nil {
		t.Errorf("stale file survived recreation — old worktree wasn't actually replaced")
	}
	if !branchExists(t, root, "orch/F2.1.T1") {
		t.Errorf("branch missing after recreation")
	}
}

func TestCreatePurgesOrphanBranchLeftByAPartialFailure(t *testing.T) {
	// The exact scenario G4.1's brief calls out: a prior `git worktree add
	// -b` created the branch ref and then failed before registering the
	// worktree directory (rate limit, disk full, ...). Simulated here by
	// creating the branch by hand with no worktree directory present.
	root := newTestRepo(t)
	m := NewManager(root, true)
	runGit(t, root, "branch", m.BranchName("F7.T1"), "main")

	path, err := m.Create("F7.T1", "main")
	if err != nil {
		t.Fatalf("Create should purge the orphan branch and succeed, got: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("worktree not created: %v", err)
	}
}

func TestCreateFailsOnUnknownBaseBranch(t *testing.T) {
	root := newTestRepo(t)
	m := NewManager(root, true)

	_, err := m.Create("F2.1.T1", "does-not-exist")
	if err == nil {
		t.Fatal("expected an error for an unknown base branch")
	}
	var wtErr *Error
	if !errors.As(err, &wtErr) {
		t.Fatalf("error = %v (%T), want *worktree.Error", err, err)
	}
	if wtErr.TaskID != "F2.1.T1" {
		t.Errorf("TaskID = %q", wtErr.TaskID)
	}
}

// ---- CommitPending (Sprint F-6, fix #60) -------------------------------

func TestCommitPendingReturnsFalseOnCleanTree(t *testing.T) {
	root := newTestRepo(t)
	m := NewManager(root, true)
	if _, err := m.Create("F6.1.T1", "main"); err != nil {
		t.Fatal(err)
	}

	committed, err := m.CommitPending("F6.1.T1", "auto-commit")
	if err != nil {
		t.Fatal(err)
	}
	if committed {
		t.Errorf("committed = true on a clean tree, want false")
	}
}

func TestCommitPendingCommitsAndReturnsTrueOnDirtyTree(t *testing.T) {
	root := newTestRepo(t)
	m := NewManager(root, true)
	wt, err := m.Create("F6.1.T1", "main")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "agent-output.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	committed, err := m.CommitPending("F6.1.T1", "F6.1.T1: orch auto-commit")
	if err != nil {
		t.Fatal(err)
	}
	if !committed {
		t.Fatalf("committed = false on a dirty tree, want true")
	}

	log := runGit(t, wt, "log", "-1", "--pretty=%s")
	if log != "F6.1.T1: orch auto-commit\n" {
		t.Errorf("log = %q", log)
	}

	// A second call must now see a clean tree again.
	committed, err = m.CommitPending("F6.1.T1", "second")
	if err != nil {
		t.Fatal(err)
	}
	if committed {
		t.Errorf("committed = true on an already-committed tree")
	}
}

func TestCommitPendingWorksWithoutAnyGlobalGitIdentity(t *testing.T) {
	// The whole point of the inline -c user.email/-c user.name is that
	// commit_pending must work even when the ambient git config has no
	// identity configured. newTestRepo sets a repo-local identity for the
	// *outer* repo's own commits; verify the worktree commit doesn't
	// depend on it by unsetting it before calling CommitPending.
	root := newTestRepo(t)
	m := NewManager(root, true)
	wt, err := m.Create("F6.1.T2", "main")
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "config", "--unset", "user.email")
	runGit(t, root, "config", "--unset", "user.name")
	if err := os.WriteFile(filepath.Join(wt, "x.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	committed, err := m.CommitPending("F6.1.T2", "no ambient identity")
	if err != nil {
		t.Fatal(err)
	}
	if !committed {
		t.Fatalf("commit_pending failed without an ambient git identity")
	}
}

// #253: an agent still writing into a task's worktree path after orch has
// already removed it (see #248's re-dispatch race) recreates the directory
// as a plain one, with no `.git` file anchoring it back to the worktree.
// `git -C <wt>` then walks up and finds the main checkout's `.git` instead,
// so CommitPending must refuse to commit rather than silently landing the
// agent's file — and whatever the operator has lying around — on the main
// checkout's current branch.
func TestCommitPendingRefusesWhenTheWorktreeIsGone(t *testing.T) {
	root := newTestRepo(t)
	m := NewManager(root, true)
	wt, err := m.Create("F3.1.T1", "main")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Remove("F3.1.T1"); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(wt, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "agent-output.txt"), []byte("late write\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "operator-scratch.txt"), []byte("mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	committed, err := m.CommitPending("F3.1.T1", "F3.1.T1: orch auto-commit")
	if err == nil {
		t.Fatalf("expected an error, got committed=%v", committed)
	}
	if committed {
		t.Errorf("committed = true, want false")
	}

	log := runGit(t, root, "log", "-1", "--pretty=%s")
	if strings.Contains(log, "orch auto-commit") {
		t.Errorf("main checkout got the auto-commit: %q", log)
	}
	status := runGit(t, root, "status", "--porcelain")
	if !strings.Contains(status, "operator-scratch.txt") {
		t.Errorf("operator's scratch file got swept up:\n%s", status)
	}
}

// ---- Push ----------------------------------------------------------------

func TestPushIsNoopWhenDisabled(t *testing.T) {
	root := newTestRepo(t)
	m := NewManager(root, false)
	if _, err := m.Create("F2.1.T3", "main"); err != nil {
		t.Fatal(err)
	}
	// No `origin` remote exists at all — if Push tried to run git push
	// anyway, it would fail. Getting nil back proves it short-circuited.
	if err := m.Push("F2.1.T3"); err != nil {
		t.Errorf("Push with pushEnabled=false returned an error: %v", err)
	}
}

func TestPushSendsBranchToOrigin(t *testing.T) {
	root := newTestRepo(t)
	remote := newBareRemote(t, root)
	m := NewManager(root, true)
	wt, err := m.Create("F2.1.T3", "main")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "x.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.CommitPending("F2.1.T3", "work"); err != nil {
		t.Fatal(err)
	}

	if err := m.Push("F2.1.T3"); err != nil {
		t.Fatal(err)
	}
	if !remoteHasBranch(t, remote, m.BranchName("F2.1.T3")) {
		t.Errorf("origin does not have %s after Push", m.BranchName("F2.1.T3"))
	}
}

func TestPushFailsWithoutARemote(t *testing.T) {
	root := newTestRepo(t)
	m := NewManager(root, true)
	if _, err := m.Create("F2.1.T3", "main"); err != nil {
		t.Fatal(err)
	}
	if err := m.Push("F2.1.T3"); err == nil {
		t.Fatal("expected an error pushing with no origin remote configured")
	}
}

// ---- Remove / RemoveAll ---------------------------------------------------

func TestRemoveIsNoopWhenDirectoryAbsent(t *testing.T) {
	root := newTestRepo(t)
	m := NewManager(root, true)
	if err := m.Remove("nonexistent-task"); err != nil {
		t.Errorf("Remove on an absent worktree returned an error: %v", err)
	}
}

func TestRemoveDeletesWorktreeAndClearsActive(t *testing.T) {
	root := newTestRepo(t)
	m := NewManager(root, true)
	path, err := m.Create("F2.1.T1", "main")
	if err != nil {
		t.Fatal(err)
	}

	if err := m.Remove("F2.1.T1"); err != nil {
		t.Errorf("Remove returned an error for a clean removal: %v", err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("worktree dir still present after Remove: err=%v", err)
	}
	if _, ok := m.active["F2.1.T1"]; ok {
		t.Errorf("active still tracks F2.1.T1 after Remove")
	}
}

func TestRemoveAllRemovesEveryActiveWorktree(t *testing.T) {
	root := newTestRepo(t)
	m := NewManager(root, true)
	ids := []string{"F2.1.T1", "F2.1.T2", "F2.1.T3"}
	for _, id := range ids {
		if _, err := m.Create(id, "main"); err != nil {
			t.Fatal(err)
		}
	}

	withWarnings := m.RemoveAll()

	if len(withWarnings) != 0 {
		t.Errorf("RemoveAll reported warnings for a clean removal: %v", withWarnings)
	}
	for _, id := range ids {
		if m.Exists(id) {
			t.Errorf("%s still exists after RemoveAll", id)
		}
	}
	if len(m.active) != 0 {
		t.Errorf("active = %v, want empty", m.active)
	}
}

func TestRemoveAllIsNoopWithNoActiveWorktrees(t *testing.T) {
	root := newTestRepo(t)
	m := NewManager(root, true)
	if got := m.RemoveAll(); len(got) != 0 {
		t.Errorf("RemoveAll = %v, want none", got)
	}
}

// ---- Recreate --------------------------------------------------------------

func TestRecreateChecksOutThePushedBranch(t *testing.T) {
	root := newTestRepo(t)
	newBareRemote(t, root)
	m := NewManager(root, true)

	wt, err := m.Create("F9.T1", "main")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "output.txt"), []byte("agent output\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.CommitPending("F9.T1", "work"); err != nil {
		t.Fatal(err)
	}
	if err := m.Push("F9.T1"); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove("F9.T1"); err != nil { // simulate cleanup between runs
		t.Fatal(err)
	}

	recreated, err := m.Recreate("F9.T1")
	if err != nil {
		t.Fatal(err)
	}
	if recreated != m.WorktreePath("F9.T1") {
		t.Errorf("path = %q, want %q", recreated, m.WorktreePath("F9.T1"))
	}
	// #nosec G304 -- recreated is a path this test just computed under t.TempDir().
	data, err := os.ReadFile(filepath.Join(recreated, "output.txt"))
	if err != nil {
		t.Fatalf("recreated worktree missing the pushed commit's file: %v", err)
	}
	if string(data) != "agent output\n" {
		t.Errorf("output.txt = %q", data)
	}
}

// ---- Exists / BranchName / WorktreePath ------------------------------------

func TestExistsReflectsDirectoryPresence(t *testing.T) {
	root := newTestRepo(t)
	m := NewManager(root, true)
	if m.Exists("F1") {
		t.Errorf("Exists true before Create")
	}
	if _, err := m.Create("F1", "main"); err != nil {
		t.Fatal(err)
	}
	if !m.Exists("F1") {
		t.Errorf("Exists false after Create")
	}
}

func TestBranchNameAndWorktreePathFormat(t *testing.T) {
	m := NewManager("/repo", true)
	if got, want := m.BranchName("F2.1.T3"), "orch/F2.1.T3"; got != want {
		t.Errorf("BranchName = %q, want %q", got, want)
	}
	if got, want := m.WorktreePath("F2.1.T3"), filepath.Join("/repo", ".worktrees", "F2.1.T3"); got != want {
		t.Errorf("WorktreePath = %q, want %q", got, want)
	}
}
