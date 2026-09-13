package publish

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// newRemote makes a bare repository that stands in for GitHub, and a project
// clone whose `origin` points at it — the same two-repo shape PublishToGit
// walks in real life, with no network in it.
func newRemote(t *testing.T) (project string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	bare := filepath.Join(root, "remote.git")
	project = filepath.Join(root, "project")

	mustGit(t, "", "init", "--quiet", "--bare", "-b", "main", bare)
	mustGit(t, "", "init", "--quiet", "-b", "main", project)
	mustGit(t, project, "config", "user.email", "test@example.com")
	mustGit(t, project, "config", "user.name", "Test")
	mustGit(t, project, "remote", "add", "origin", bare)
	if err := os.WriteFile(filepath.Join(project, "README.md"), []byte("source\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, project, "add", "-A")
	mustGit(t, project, "commit", "--quiet", "-m", "initial")
	mustGit(t, project, "push", "--quiet", "origin", "main")
	return project
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := git(context.Background(), dir, args...)
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return out
}

// fetched refreshes the project checkout's remote-tracking refs.
//
// PublishToGit pushes from its OWN temporary clone directly to the remote, so
// the project's origin/* refs know nothing about it until they are fetched.
// That is the design working — the publish never touches this checkout — and
// it means every assertion about the branch has to go to the remote for it.
func fetched(t *testing.T, project string) string {
	t.Helper()
	mustGit(t, project, "fetch", "--quiet", "origin")
	return project
}

func exportDir(t *testing.T, snap any) string {
	t.Helper()
	dir := t.TempDir()
	s := testSnapshot()
	if n, ok := snap.(string); ok {
		s.ProjectName = n
	}
	if _, err := exportFrom(fakeBundle(), dir, s, Options{}); err != nil {
		t.Fatalf("exportFrom: %v", err)
	}
	return dir
}

func TestPublishToGitCreatesTheBranchAndPushes(t *testing.T) {
	project := newRemote(t)
	dir := exportDir(t, nil)

	res, err := PublishToGit(context.Background(), dir, GitOptions{RepoDir: project})
	if err != nil {
		t.Fatalf("PublishToGit: %v", err)
	}
	if !res.Pushed {
		t.Fatal("the first publish should push")
	}
	if res.Branch != DefaultGitBranch {
		t.Errorf("Branch = %q, want %q", res.Branch, DefaultGitBranch)
	}

	// The page is on the branch, from the remote's point of view.
	files := mustGit(t, fetched(t, project), "ls-tree", "-r", "--name-only", "origin/"+DefaultGitBranch)
	for _, want := range []string{"index.html", "data.json", "data.js", "robots.txt"} {
		if !strings.Contains(files, want) {
			t.Errorf("%s is not on %s:\n%s", want, DefaultGitBranch, files)
		}
	}

	// And the source tree is NOT: the branch is an orphan, so a static host
	// serving it does not also serve the repository.
	if strings.Contains(files, "README.md") {
		t.Errorf("the published branch carries the source tree:\n%s", files)
	}

	// The project's own checkout is untouched — no stray branch, no dirty
	// tree. Publishing during a run must not disturb the run.
	if status := mustGit(t, project, "status", "--porcelain"); strings.TrimSpace(status) != "" {
		t.Errorf("the project checkout was modified:\n%s", status)
	}
	if head := strings.TrimSpace(mustGit(t, project, "rev-parse", "--abbrev-ref", "HEAD")); head != "main" {
		t.Errorf("the project checkout is on %q, not main", head)
	}
}

// The second publish of identical content must not make a commit. This is what
// keeps a `--watch --to git` branch from growing one commit per interval
// forever — Digest stops most of them, and this stops the rest.
func TestPublishToGitSkipsAnUnchangedExport(t *testing.T) {
	project := newRemote(t)
	dir := exportDir(t, nil)
	ctx := context.Background()

	first, err := PublishToGit(ctx, dir, GitOptions{RepoDir: project})
	if err != nil {
		t.Fatalf("first publish: %v", err)
	}
	second, err := PublishToGit(ctx, dir, GitOptions{RepoDir: project})
	if err != nil {
		t.Fatalf("second publish: %v", err)
	}
	if second.Pushed {
		t.Error("re-publishing identical content should push nothing")
	}

	mustGit(t, project, "fetch", "--quiet", "origin")
	head := strings.TrimSpace(mustGit(t, project, "rev-parse", "origin/"+DefaultGitBranch))
	if head != first.Commit {
		t.Errorf("the branch moved: %s != %s", head, first.Commit)
	}
}

// A file the previous export wrote and this one does not must disappear. A
// stale hashed asset left on the branch is how a page half-works.
func TestPublishToGitReplacesTheTreeWholesale(t *testing.T) {
	project := newRemote(t)
	ctx := context.Background()

	first := exportDir(t, nil)
	if err := os.WriteFile(filepath.Join(first, "assets", "old-hash.js"), []byte("stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PublishToGit(ctx, first, GitOptions{RepoDir: project}); err != nil {
		t.Fatalf("first publish: %v", err)
	}

	second := exportDir(t, "Otro Proyecto")
	if _, err := PublishToGit(ctx, second, GitOptions{RepoDir: project}); err != nil {
		t.Fatalf("second publish: %v", err)
	}

	files := mustGit(t, fetched(t, project), "ls-tree", "-r", "--name-only", "origin/"+DefaultGitBranch)
	if strings.Contains(files, "old-hash.js") {
		t.Errorf("a file dropped by the new export is still on the branch:\n%s", files)
	}
}

func TestPublishToGitPushesToAnExistingBranch(t *testing.T) {
	project := newRemote(t)
	ctx := context.Background()

	if _, err := PublishToGit(ctx, exportDir(t, nil), GitOptions{RepoDir: project}); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	// The branch now exists on the remote, so this one takes the clone path
	// rather than the orphan path.
	res, err := PublishToGit(ctx, exportDir(t, "Segundo"), GitOptions{RepoDir: project})
	if err != nil {
		t.Fatalf("second publish: %v", err)
	}
	if !res.Pushed {
		t.Fatal("changed content should push")
	}
	body := mustGit(t, fetched(t, project), "show", "origin/"+DefaultGitBranch+":data.json")
	if !strings.Contains(body, "Segundo") {
		t.Errorf("the branch does not carry the new snapshot:\n%s", body)
	}
}

func TestPublishToGitHonoursTheBranchName(t *testing.T) {
	project := newRemote(t)
	res, err := PublishToGit(context.Background(), exportDir(t, nil),
		GitOptions{RepoDir: project, Branch: "pages"})
	if err != nil {
		t.Fatalf("PublishToGit: %v", err)
	}
	if res.Branch != "pages" {
		t.Errorf("Branch = %q, want pages", res.Branch)
	}
	mustGit(t, fetched(t, project), "rev-parse", "origin/pages")
}

// Without a remote there is nothing to publish to, and the message has to say
// that rather than surface git's own.
func TestPublishToGitWithoutARemote(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	solo := t.TempDir()
	mustGit(t, "", "init", "--quiet", "-b", "main", solo)

	_, err := PublishToGit(context.Background(), exportDir(t, nil), GitOptions{RepoDir: solo})
	if err == nil {
		t.Fatal("publishing to a repo with no remote should fail")
	}
	if !strings.Contains(err.Error(), "origin") {
		t.Errorf("the error should name the missing remote, got: %v", err)
	}
}
