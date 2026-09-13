package publish

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// DefaultGitBranch is where a static host looks by default.
const DefaultGitBranch = "gh-pages"

// GitOptions controls a push of the export to a branch.
type GitOptions struct {
	// RepoDir is the git work tree whose `origin` receives the push —
	// normally the project root. The export never lands in THIS checkout:
	// the branch is built in a temporary clone, so a publish can never
	// disturb the tree somebody is working in, and `orch publish` during a
	// run is not a thing that touches the run's files.
	RepoDir string
	// Remote defaults to "origin".
	Remote string
	// Branch defaults to DefaultGitBranch.
	Branch string
	// Message is the commit subject; a default naming the digest is used
	// when empty.
	Message string
	// Now is the timestamp in the default commit message.
	Now time.Time
}

// GitResult says what the push did.
type GitResult struct {
	Branch string
	Remote string
	// Pushed is false when the export was byte-identical to what the branch
	// already held. That is the common case on a `--watch` tick and is a
	// success, not a no-op worth an error.
	Pushed bool
	Commit string
	// URL is the remote's URL, for the line printed to the operator.
	URL string
}

// PublishToGit force-replaces branch's content with the export in dir, commits
// and pushes it.
//
// The branch is treated as OUTPUT, not as history: every publish replaces the
// tree wholesale, because a stale asset left behind by an export that stopped
// emitting it is a page that half-works. The commits accumulate normally, so
// the branch is still a record of what was published and when — it is the
// working tree that is authoritative, not the diff.
func PublishToGit(ctx context.Context, dir string, o GitOptions) (GitResult, error) {
	if o.Remote == "" {
		o.Remote = "origin"
	}
	if o.Branch == "" {
		o.Branch = DefaultGitBranch
	}
	res := GitResult{Branch: o.Branch, Remote: o.Remote}

	url, err := git(ctx, o.RepoDir, "remote", "get-url", o.Remote)
	if err != nil {
		return res, fmt.Errorf(
			"%s has no %q remote to publish to — `orch publish --to git` pushes "+
				"the export to a branch of the project's own repository: %w",
			o.RepoDir, o.Remote, err)
	}
	res.URL = strings.TrimSpace(url)

	tmp, err := os.MkdirTemp("", "orch-publish-")
	if err != nil {
		return res, fmt.Errorf("creating a temporary checkout: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	checkout := filepath.Join(tmp, "repo")

	if err := cloneBranch(ctx, res.URL, o.Branch, checkout); err != nil {
		return res, err
	}

	// Everything but .git goes, then the export lands. Anything the previous
	// publish wrote and this one did not is gone from the branch, which is
	// the point.
	if err := clearTree(checkout); err != nil {
		return res, err
	}
	if err := copyTree(dir, checkout); err != nil {
		return res, fmt.Errorf("staging the export into the checkout: %w", err)
	}

	if _, err := git(ctx, checkout, "add", "-A"); err != nil {
		return res, err
	}
	// `diff --cached --quiet` exits 1 when there IS something staged. Only
	// that exact shape means "there are changes"; anything else is an error
	// worth surfacing rather than reading as one.
	if clean, err := indexIsClean(ctx, checkout); err != nil {
		return res, err
	} else if clean {
		return res, nil
	}

	msg := o.Message
	if msg == "" {
		when := o.Now
		if when.IsZero() {
			when = time.Now()
		}
		msg = "publish: stakeholder snapshot " + when.UTC().Format("2006-01-02 15:04:05Z")
	}

	args := []string{"commit", "-m", msg}
	// A CI runner has no identity configured, and a commit that fails for
	// that reason reads as a git bug rather than a missing setting. Supplied
	// only when there is none — a publish from the operator's machine is
	// still authored by the operator.
	if !hasIdentity(ctx, checkout) {
		args = append([]string{
			"-c", "user.name=orch",
			"-c", "user.email=orch@localhost",
		}, args...)
	}
	if _, err := git(ctx, checkout, args...); err != nil {
		return res, err
	}
	sha, err := git(ctx, checkout, "rev-parse", "HEAD")
	if err != nil {
		return res, err
	}
	res.Commit = strings.TrimSpace(sha)

	if _, err := git(ctx, checkout, "push", o.Remote, "HEAD:refs/heads/"+o.Branch); err != nil {
		return res, err
	}
	res.Pushed = true
	return res, nil
}

// cloneBranch gets a checkout of branch, creating it as an orphan when the
// remote does not have it yet.
//
// Orphan and not a branch off main: the published site shares no history with
// the source tree, and a gh-pages carrying the repository's commits would ship
// every source file to a static host on its first publish.
func cloneBranch(ctx context.Context, url, branch, dst string) error {
	if _, err := git(ctx, "", "clone", "--quiet", "--depth", "1",
		"--single-branch", "--branch", branch, url, dst); err == nil {
		return nil
	}
	// The branch does not exist yet (or the remote is empty). Clone whatever
	// is there and start the branch with no parent.
	if err := os.RemoveAll(dst); err != nil {
		return fmt.Errorf("cleaning a failed checkout: %w", err)
	}
	if _, err := git(ctx, "", "clone", "--quiet", url, dst); err != nil {
		return fmt.Errorf("cloning %s: %w", url, err)
	}
	if _, err := git(ctx, dst, "switch", "--orphan", branch); err != nil {
		return fmt.Errorf("creating the %s branch: %w", branch, err)
	}
	return nil
}

// clearTree removes everything in the checkout except .git.
func clearTree(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("reading the checkout: %w", err)
	}
	for _, e := range entries {
		if e.Name() == ".git" {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return fmt.Errorf("clearing the checkout: %w", err)
		}
	}
	return nil
}

func indexIsClean(ctx context.Context, dir string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "diff", "--cached", "--quiet")
	cmd.Dir = dir
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("checking what is staged: %w", err)
}

func hasIdentity(ctx context.Context, dir string) bool {
	email, err := git(ctx, dir, "config", "user.email")
	return err == nil && strings.TrimSpace(email) != ""
}

// git runs one git command, returning stdout. dir may be empty for commands
// that take their target as an argument (clone).
func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...) // #nosec G204 -- fixed verbs, operator-supplied paths
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// A publish must never stop on a credential prompt: in a watch loop or a
	// cron job there is nobody to answer it, and the process would hang
	// rather than fail.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return stdout.String(), nil
}
