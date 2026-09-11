package vcs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// defaultTimeout bounds every gh/glab invocation this package makes.
// Python has no such bound; CHECKLIST rule 18 requires a context deadline
// on any os/exec call, and a network-bound CLI call is exactly the kind
// that benefits from one — a hung `gh pr checks` on a bad network
// connection would otherwise wedge a CI poll forever.
const defaultTimeout = 2 * time.Minute

// runError is the raw outcome of one CLI invocation this package doesn't
// automatically turn into a *CLIError — the caller decides what a non-zero
// exit means for its own subcommand (Python-parity "pending"/""/false, or
// something worth checking auth over).
type runError struct {
	binary string
	args   []string
	stderr string
	err    error // non-nil only when the process itself couldn't be run
}

func (e *runError) Error() string {
	stderr := e.stderr
	if len(stderr) > 200 {
		stderr = stderr[:200]
	}
	if e.err != nil {
		return fmt.Sprintf("vcs: run %s %v: %v", e.binary, e.args, e.err)
	}
	return fmt.Sprintf("vcs: %s %v exited non-zero: %s", e.binary, e.args, stderr)
}

// checkBinary reports whether binary is on PATH, as a typed *CLIError —
// the one check every public method makes before doing anything else, so
// a missing gh/glab is never mistaken for "CI still pending" (see the
// package doc comment).
func checkBinary(binary string) error {
	if _, err := exec.LookPath(binary); err != nil {
		return &CLIError{Binary: binary, Reason: "not found in PATH", Err: err}
	}
	return nil
}

// run executes binary with args under a bounded context, in its own
// process group (CHECKLIST rule 18), with extraEnv appended to the
// inherited environment (used for GitLab's GITLAB_HOST). It returns
// stdout on a zero exit, or a *runError otherwise — never a *CLIError;
// callers that care about the CLI-missing/not-authenticated distinction
// make that call after seeing a *runError, at whatever granularity suits
// their own polling frequency (see the package doc comment).
func run(binary string, extraEnv []string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	// #nosec G204 -- binary is one of this package's own two fixed CLI
	// names; args are subcommands and caller-supplied PR/task text, not
	// arbitrary input.
	cmd := exec.CommandContext(ctx, binary, args...)
	if len(extraEnv) > 0 {
		cmd.Env = append(cmd.Environ(), extraEnv...)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", &runError{binary: binary, args: args, stderr: stderr.String()}
		}
		return "", &runError{binary: binary, args: args, err: err}
	}
	return stdout.String(), nil
}

// checkAuth runs `<binary> auth status`, the one dedicated, reliable way
// (rather than sniffing another command's stderr) to tell "not
// authenticated" apart from an ordinary failure. Both gh and glab support
// this subcommand. Used by CreatePR/MergePR on failure (cheap relative to
// their once-per-task call frequency) and by CheckAuth below.
func checkAuth(binary string, extraEnv []string) error {
	if err := checkBinary(binary); err != nil {
		return err
	}
	if _, err := run(binary, extraEnv, "auth", "status"); err != nil {
		return &CLIError{Binary: binary, Reason: "not authenticated", Err: err}
	}
	return nil
}

// CheckAuth reports whether binary ("gh" or "glab") is installed and
// authenticated, as a *CLIError when it isn't. host, when non-empty, is
// passed as GITLAB_HOST (glab reads it the same way GitLabProvider does);
// ignored for gh. Exported for internal/doctor's readiness check (G4.5),
// which needs this exact probe independent of opening or merging anything.
func CheckAuth(binary, host string) error {
	var env []string
	if host != "" {
		env = []string{"GITLAB_HOST=" + host}
	}
	return checkAuth(binary, env)
}

// lastNonEmptyLine returns the last non-blank line of s, trimmed — glab's
// create commands print the MR URL as their last output line.
func lastNonEmptyLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			return t
		}
	}
	return ""
}

const maxLogChars = 8000

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
