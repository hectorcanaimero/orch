package engine

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Wait blocks until the child exits or timeout elapses, and returns its exit
// code, a reason string (empty unless the timeout fired), and whether it timed
// out. Port of dispatcher.py's _wait_with_timeout.
//
// The timeout path is SIGTERM to the process group, TermToKillGrace, then
// SIGKILL to the group, then a short reap window. Signalling the group rather
// than the pid is what makes this reach the bash wrappers and sub-agents a
// coding CLI forks; without it the CLI dies and its children are reparented to
// init, still holding the workspace (Sprint A / Issue #12, checklist rule 18).
//
// Wait always closes the handles Spawn opened, on every path.
func (s *Spawned) Wait(timeout time.Duration) (exitCode int, reason string, timedOut bool) {
	// Idempotent, and concurrency-safe: exec.Cmd.Wait panics if called twice,
	// and since the reaper gave each dispatch a supervising goroutine there
	// are two plausible callers — the supervisor, and any cleanup path that
	// wants to make sure a child is gone. A second caller blocks until the
	// first finishes and gets the same answer.
	s.waitOnce.Do(func() {
		s.exitCode, s.reason, s.timedOut = s.wait(timeout)
	})
	return s.exitCode, s.reason, s.timedOut
}

func (s *Spawned) wait(timeout time.Duration) (exitCode int, reason string, timedOut bool) {
	defer s.closeHandles()
	defer s.stopWatch()

	if s.cmd == nil {
		return 0, "", false
	}

	done := make(chan error, 1)
	go func() { done <- s.cmd.Wait() }()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case err := <-done:
		return exitCodeOf(s.cmd, err), "", false
	case <-timer.C:
	}

	timedOut = true
	// Python renders the seconds with %.0f: "orchestrator timeout after 300s".
	reason = fmt.Sprintf("%s after %.0fs", TimeoutReason, timeout.Seconds())

	// Polite first.
	s.signalGroup(syscall.SIGTERM)
	grace := time.NewTimer(TermToKillGrace)
	defer grace.Stop()
	select {
	case err := <-done:
		return exitCodeOf(s.cmd, err), reason, true
	case <-grace.C:
	}

	// Grace expired: SIGKILL the group and reap unconditionally.
	s.signalGroup(syscall.SIGKILL)
	reap := time.NewTimer(reapGrace)
	defer reap.Stop()
	select {
	case err := <-done:
		return exitCodeOf(s.cmd, err), reason, true
	case <-reap.C:
		// Truly zombied — SIGKILL cannot be caught, so reaching here means
		// the process is stuck in uninterruptible sleep (usually blocked I/O
		// on a dead mount). Return the marker and let the caller move on
		// rather than hold the slot forever.
		return ExitZombied, reason, true
	}
}

// exitCodeOf turns a cmd.Wait error into the exit code Python's
// `_exit_code_from_status` would report.
//
// A process killed by a signal has no exit code of its own; Python's
// subprocess reports -N for signal N, and the reap loop's own
// `_exit_code_from_status` does the same. Go reports signals separately, so
// the sign is reapplied here — the value ends up in the `exit_code` field of
// an event row, where the two binaries must agree.
func exitCodeOf(cmd *exec.Cmd, waitErr error) int {
	if cmd.ProcessState != nil {
		if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok {
			if status.Signaled() {
				return -int(status.Signal())
			}
			return status.ExitStatus()
		}
		return cmd.ProcessState.ExitCode()
	}
	var ee *exec.ExitError
	if errors.As(waitErr, &ee) {
		return ee.ExitCode()
	}
	if waitErr != nil {
		// Wait failed for a reason that is not the child's exit — a double
		// Wait, or a pipe error. Not the child's fault, but not a success
		// either.
		return 1
	}
	return 0
}

// ReadLog reads a dispatch's captured output back for parsing. Port of
// dispatcher.py's _read_log: a missing or unreadable file is "", never an
// error, because the reap loop must reach a verdict either way — and an empty
// capture is itself the verdict (FailureParser, or FailureTimeout once the
// wait path stamps its reason on top).
func ReadLog(path string) string {
	b, err := os.ReadFile(path) // #nosec G304 -- the log path the engine itself just wrote
	if err != nil {
		return ""
	}
	return string(b)
}
