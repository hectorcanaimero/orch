// Package vcs ports orchestrator/vcs/ (protocol.py, github.py, gitlab.py):
// opening a PR/MR, polling its CI status, fetching failed-job logs, and
// merging it, all through the `gh` / `glab` CLIs rather than a REST client.
//
// # Polling contract (for internal/engine, not yet built)
//
// CIStatus is a single poll, not a loop: Python's ci_poll_interval_s wait
// and ci_max_retries retry count live in orch.py's dispatch loop
// (_check_ci_once), which has no Go equivalent yet. Whoever writes
// internal/engine owns calling CIStatus on a timer and deciding when the
// retry budget is spent — this package only answers "what does CI say
// right now".
//
// # Error handling deliberately improves on Python here
//
// Python's three provider methods that can fail return a zero value on ANY
// failure — create_pr returns None, get_ci_status returns "pending",
// merge_pr returns False — whether the cause is "CI is still running" or
// "gh isn't even installed". That hides a structural setup problem behind
// what looks like ordinary polling. This port keeps the same zero-value
// behavior for a normal, expected outcome (CI genuinely pending, a PR
// that didn't get created this attempt), but a missing CLI binary always
// surfaces as a typed *CLIError — checked once, cheaply, via
// exec.LookPath, on every call — and CreatePR/MergePR (called once per
// task, not polled, so the extra cost is negligible) additionally probe
// `gh|glab auth status` on failure to tell "not authenticated" apart from
// an ordinary business failure (PR already exists, not mergeable yet).
// CIStatus/CILogs stay poll-loop-cheap and do not make that extra call.
package vcs

import (
	"errors"
	"fmt"
)

// ErrMergeFailed is MergePR's error when the CLI ran (and is authenticated)
// but the merge itself didn't happen — not mergeable yet, conflicts, branch
// protection, or a MR/PR that doesn't exist. Ports Python's merge_pr
// returning False for the same set of outcomes; wrapped so a caller can
// tell "merge genuinely didn't happen" apart from a *CLIError with
// errors.Is/errors.As.
var ErrMergeFailed = errors.New("vcs: merge did not succeed")

// CIState is a CI run's outcome as this package reports it — Python's
// get_ci_status docstring's exact three values. A GitHub check conclusion
// or GitLab pipeline status that doesn't map to a state below (see each
// provider's state map) is reported as CIPending: an unrecognized value is
// far more likely to be a new not-yet-terminal state than a green light.
type CIState string

const (
	CIPending CIState = "pending"
	CISuccess CIState = "success"
	CIFailure CIState = "failure"
	// CINone is a PR with no check (GitHub) or pipeline (GitLab) at all. Go
	// only: Python called it pending, which is true for a PR opened a moment
	// ago and never stops being "true" on a repo with no CI — #233. The
	// engine decides how long none may last.
	CINone CIState = "none"
	// CIConflict is a PR with no checks that conflicts with its base. GitHub
	// runs no workflow on a conflicting PR, so its checks are never coming
	// (#236). Only reported alongside no checks: a PR whose CI already ran
	// is judged by that run.
	CIConflict CIState = "conflict"
)

// PRState is whether a PR/MR is still open, or settled one way or the other.
// Go only: Python never asked, and polled a merged or closed PR's CI forever
// (#255).
type PRState string

const (
	PROpen   PRState = "open"
	PRMerged PRState = "merged"
	PRClosed PRState = "closed" // closed without merging
)

// ErrPRStateUnsupported is PRState's error for a provider that cannot tell
// yet. The caller treats it as "unknown" and carries on as before.
var ErrPRStateUnsupported = errors.New("vcs: reading a PR's state is not supported for this provider")

// Provider opens, polls, and merges a PR (GitHub) or MR (GitLab).
type Provider interface {
	// CreatePR opens a PR/MR from head into base and returns its URL.
	// Returns ("", nil) — never an error — when the CLI ran but didn't
	// produce a PR (Python's None); see the package doc comment for when
	// this instead becomes a typed error.
	CreatePR(head, base, title, body string) (string, error)

	// CIStatus reports head's current CI state for the PR/MR at prURL. A
	// single poll — see the package doc comment's "Polling contract".
	CIStatus(prURL string) (CIState, error)

	// PRState reports whether the PR/MR at prURL is open, merged or closed
	// without merging. ErrPRStateUnsupported when the provider cannot tell.
	PRState(prURL string) (PRState, error)

	// CILogs returns failed CI job logs as plain text, truncated to 8000
	// chars, or "" when there is nothing failed to show.
	CILogs(prURL string) (string, error)

	// MergePR merges prURL. squash requests a squash merge; auto requests
	// merge-when-checks-pass instead of merging immediately (GitHub's
	// `--auto`, GitLab's `--when-pipeline-succeeds`) — see the package doc
	// comment for why these are parameters here when Python hardcodes them.
	// Returns ErrMergeFailed (never a plain nil-vs-non-nil ambiguity) when
	// the CLI ran but the merge itself didn't happen.
	MergePR(prURL string, squash, auto bool) error
}

// Config is the subset of internal/config.Config a caller needs to build a
// Provider — small and duplicated here (rather than importing
// internal/config) so this package has no dependency on the config
// package's parsing/defaulting concerns, only the three values that decide
// which provider to build.
type Config struct {
	// Provider is "github" (default) or "gitlab".
	Provider string
	// Host is the GitLab instance host (e.g. "gitlab.com" or a self-hosted
	// domain); ignored for GitHub.
	Host string
}

// NewProvider builds the Provider cfg selects. Ports get_vcs_provider.
func NewProvider(cfg Config) Provider {
	if cfg.Provider == "gitlab" {
		host := cfg.Host
		if host == "" {
			host = "gitlab.com"
		}
		return NewGitLabProvider(host)
	}
	return NewGitHubProvider()
}

// CLIError reports that gh/glab itself couldn't do its job — missing from
// PATH or not authenticated — as opposed to an ordinary CI/PR outcome.
// Never returned for "CI is pending" or "no PR yet"; see the package doc
// comment.
type CLIError struct {
	Binary string // "gh" or "glab"
	Reason string // "not found in PATH" | "not authenticated"
	Err    error  // underlying error, if any
}

func (e *CLIError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("vcs: %s %s: %v", e.Binary, e.Reason, e.Err)
	}
	return fmt.Sprintf("vcs: %s %s", e.Binary, e.Reason)
}

func (e *CLIError) Unwrap() error { return e.Err }
