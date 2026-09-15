// Package worktree ports orchestrator/worktree.py: per-task git worktree
// isolation so concurrent dispatched agents cannot overwrite each other's
// file changes. A Manager is created once per `orch run` invocation and
// lives until the run loop exits.
//
// # Caller contract (for internal/engine, not yet built)
//
// Python's orch.py pins two ordering rules across _reap_once and its SIGINT
// handler (see orchestrator/tests/test_orch_worktree.py) that this package
// cannot enforce itself — it has no notion of a dispatch's outcome — but
// that whoever drives Manager from the dispatch loop must:
//
//  1. On a finished task, call CommitPending then Push then Remove, in that
//     order, and only call Push when the task succeeded (skip straight to
//     Remove on failure). A Push failure must not skip Remove, and must not
//     downgrade an otherwise-successful task — log it and move on.
//  2. Call RemoveAll after the drain wait completes, not from inside a
//     SIGTERM/SIGINT handler — cleanup racing a still-draining task would
//     delete a worktree a backend process is still writing into.
package worktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// defaultTimeout bounds every git command this package runs. Python has no
// such bound (a hung `git push` on a broken remote blocks the reap loop
// forever); CHECKLIST rule 18 requires a context deadline on any os/exec
// call this tree makes, and a worktree op is exactly the kind of network- or
// lock-bound command that benefits from one.
const defaultTimeout = 2 * time.Minute

// Error is returned when a git command this package runs exits non-zero.
// Ports WorktreeError.
type Error struct {
	TaskID string
	Cmd    []string
	Stderr string
}

func (e *Error) Error() string {
	stderr := e.Stderr
	if len(stderr) > 200 {
		stderr = stderr[:200]
	}
	return fmt.Sprintf("worktree: git command failed for %s: %q: %s", e.TaskID, strings.Join(e.Cmd, " "), stderr)
}

// Manager creates, commits, pushes and removes per-task git worktrees, all
// as commands run from root (the main repo) — never from inside a
// worktree, except where a command explicitly targets one via `-C`.
//
// Manager is safe for concurrent use: Create/CommitPending/Push/Remove may
// run from multiple goroutines (one per in-flight dispatched task), guarded
// by an internal mutex around the active-worktree bookkeeping. The
// underlying git commands are not synchronized against each other beyond
// what git itself serializes (its own index/ref locks).
type Manager struct {
	root        string
	pushEnabled bool
	timeout     time.Duration

	mu     sync.Mutex
	active map[string]string // task id -> worktree path
}

// NewManager builds a Manager rooted at root. pushEnabled mirrors Python's
// push_enabled kwarg (default True there): pass false for a project with no
// remote so Push becomes a silent no-op instead of failing once per task
// (see the G0.2 note on Push).
func NewManager(root string, pushEnabled bool) *Manager {
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	return &Manager{
		root:        abs,
		pushEnabled: pushEnabled,
		timeout:     defaultTimeout,
		active:      map[string]string{},
	}
}

// dirSuffix names the directory holding a project's task worktrees: it sits
// NEXT TO the project, as `<project>.worktrees/`, never inside it. Inside the
// checkout, a worktree had the project's own node_modules and
// pnpm-workspace.yaml among its ancestors, and Node, TypeScript and pnpm all
// walk up to find them (#249).
const dirSuffix = ".worktrees"

// Dir returns the directory holding root's task worktrees:
// `/work/app` → `/work/app.worktrees`.
func Dir(root string) string { return filepath.Clean(root) + dirSuffix }

// LegacyDir is where orch before #249 put them, `<project>/.worktrees`. Only
// read, to clean up and report what an older run left behind.
func LegacyDir(root string) string { return filepath.Join(root, dirSuffix) }

// WorktreePath returns the on-disk path for taskID's worktree, whether or
// not it currently exists.
func (m *Manager) WorktreePath(taskID string) string {
	return filepath.Join(Dir(m.root), taskID)
}

// worktreesDir creates Dir(m.root), refusing the two layouts that have no
// usable sibling: a project at the filesystem root, whose sibling would be
// inside it again, and a project named `*.worktrees`, which would read as
// another project's worktree directory to config.ResolvePaths.
func (m *Manager) worktreesDir() (string, error) {
	dir := Dir(m.root)
	if filepath.Dir(m.root) == m.root || strings.HasSuffix(filepath.Base(m.root), dirSuffix) {
		return "", fmt.Errorf("worktree: the project %s has no sibling directory for its worktrees (%s); "+
			"move or rename the project, or set dispatch.worktree_mode: false", m.root, dir)
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("worktree: create %s, the directory next to the project that holds its worktrees: %w", dir, err)
	}
	return dir, nil
}

// BranchName returns the branch a task's worktree is checked out on.
func (m *Manager) BranchName(taskID string) string {
	return "orch/" + taskID
}

// Exists reports whether taskID's worktree directory is present on disk.
func (m *Manager) Exists(taskID string) bool {
	return pathExists(m.WorktreePath(taskID))
}

// run executes a git (or other) command from root, under a bounded context,
// in its own process group (so a timeout kill takes any children with it —
// CHECKLIST rule 18). A non-zero exit becomes *Error carrying taskID, the
// command, and stderr; a failure to even start the process (binary missing,
// context already done, ...) is wrapped with fmt.Errorf instead, since
// that's a different failure than "git ran and objected" and rule 19 wants
// the context to say which.
func (m *Manager) run(taskID string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), m.timeout)
	defer cancel()

	// #nosec G204 -- args are this package's own fixed git subcommands plus
	// caller-supplied task IDs/branch names/messages, not arbitrary input.
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = m.root
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err == nil {
		return stdout.String(), nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return "", &Error{TaskID: taskID, Cmd: args, Stderr: stderr.String()}
	}
	return "", fmt.Errorf("worktree: run %q for %s: %w", strings.Join(args, " "), taskID, err)
}

// runBestEffort runs args for one of the two cleanup steps (branch purge,
// worktree remove) Python deliberately treats as non-fatal, returning
// whatever error m.run produced (nil on success) without deciding for the
// caller whether it's worth surfacing — see the doc comments on
// PurgeOrphanBranch (whose common failure, "branch doesn't exist", is
// entirely expected and not logged) and Remove (whose failure is not
// expected and is logged at WARN) for why that decision belongs there
// rather than here.
func (m *Manager) runBestEffort(taskID string, args ...string) error {
	_, err := m.run(taskID, args...)
	return err
}

// logGitWarning reports a best-effort git command's failure at WARN with
// enough context to debug it (rule 19: never silently drop an error),
// truncating stderr the same way *Error.Error() does.
func logGitWarning(taskID string, args []string, err error) {
	stderr := err.Error()
	var wtErr *Error
	if errors.As(err, &wtErr) {
		stderr = wtErr.Stderr
		if len(stderr) > 200 {
			stderr = stderr[:200]
		}
	}
	slog.Warn("worktree: best-effort git command failed",
		"task_id", taskID, "cmd", strings.Join(args, " "), "stderr", stderr)
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
