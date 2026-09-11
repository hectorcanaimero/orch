package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// TaskLock is an advisory, non-blocking, exclusive lock on one task id,
// so several orch instances can share a project as long as each takes a
// distinct set of tasks. Port of orchestrator/state/flock.py.
//
// Opt-in: Python gates it behind `--task-locks`, and so does the scheduler.
//
// The lock lives for as long as the open file does, which is the whole reason
// this is a struct and not a function returning a bool. The *os.File is held
// on the in-flight dispatch and released by the reaper — a local variable
// would be collected, the descriptor closed, and the lock quietly dropped
// while the child is still running.
type TaskLock struct {
	f    *os.File
	path string
}

// TaskLocksDir is where the lock files live, under the state directory.
const TaskLocksDir = "task-locks"

// TryAcquireTaskLock takes the lock for one task, or returns nil when another
// orch already holds it.
//
// An error is returned only for a problem that is not contention — an
// unwritable state directory, say. Contention is (nil, nil): the caller skips
// the task this tick and tries again on the next, which is not an error
// condition.
func TryAcquireTaskLock(stateDir, taskID string) (*TaskLock, error) {
	dir := filepath.Join(stateDir, TaskLocksDir)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("task lock dir: %w", err)
	}
	lockPath := filepath.Join(dir, taskID+".lock")

	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 -- state dir plus a task id from tasks.json
	if err != nil {
		return nil, fmt.Errorf("open task lock %s: %w", lockPath, err)
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if err == syscall.EWOULDBLOCK {
			// Held by another orch. Not an error — just not ours.
			return nil, nil //nolint:nilnil // (nil, nil) IS the contention answer; see the doc comment
		}
		return nil, fmt.Errorf("flock %s: %w", lockPath, err)
	}

	// The pid is written for a human debugging "who holds this?". Best
	// effort: the lock is already held, and failing to annotate it is not a
	// reason to give it up. Python does the same and swallows the error.
	if err := f.Truncate(0); err == nil {
		if _, err := f.WriteAt([]byte(fmt.Sprintf("pid=%d\n", os.Getpid())), 0); err == nil {
			_ = f.Sync()
		}
	}

	return &TaskLock{f: f, path: lockPath}, nil
}

// Release drops the lock. Safe on a nil lock, so callers do not have to guard
// the "task locks are off" case, and safe to call twice.
//
// The lock file itself is left behind: flock is advisory and released when
// the descriptor closes, so deleting the file would race another orch that
// has just opened it and is about to lock it.
func (l *TaskLock) Release() {
	if l == nil || l.f == nil {
		return
	}
	// Closing the descriptor releases the flock; unlocking first is not
	// required, and doing it separately would open a window where the lock
	// is free but the file is still open.
	_ = l.f.Close()
	l.f = nil
}

// Path is the lock file this lock holds, for diagnostics.
func (l *TaskLock) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}
