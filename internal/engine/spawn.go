// Package engine runs dispatches: it spawns a provider's CLI, supervises it,
// and turns what comes back into a Result.
//
// The split with internal/providers is deliberate. A provider is pure — it
// builds an argv and parses bytes — so every parser can be pinned against
// captured CLI output. Everything impure lives here: processes, signals,
// files, clocks.
//
// Ported from the spawn and wait halves of orchestrator/dispatcher.py
// (`_spawn_generic`, `_wait_with_timeout`, `_signal_child_group`) and from
// `orchestrator/orch.py::_timeout_for`.
package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/hectorcanaimero/orch/internal/providers"
)

// TermToKillGrace is how long a child gets between SIGTERM and SIGKILL on
// timeout. Same 10 seconds as Python's _TERM_TO_KILL_GRACE_S (FR-D-3, AS-03).
const TermToKillGrace = 10 * time.Second

// reapGrace is how long we wait for a SIGKILLed process group to actually
// die before giving up and returning the zombie marker. Python waits 5s.
const reapGrace = 5 * time.Second

// ExitZombied is the exit code reported when a process survives SIGKILL long
// enough that we stop waiting. Python returns -9 here; the value is a marker,
// not a real wait status.
const ExitZombied = -9

// TimeoutReason is the prefix Python's wait path writes into error_message,
// and the marker providers.Classify keys FailureTimeout on. Changing it
// silently turns every timeout into FailureOther.
const TimeoutReason = "orchestrator timeout"

// Spawned is a child process under supervision, plus the handles Wait needs
// to clean up after it.
type Spawned struct {
	// PID is the child's pid, which is also its process-group id because
	// the child is made a session leader.
	PID int
	// LogPath is the file receiving the child's merged stdout and stderr.
	LogPath string
	// StartedAt is when the child was forked, for the spend row's duration.
	StartedAt time.Time

	cmd      *exec.Cmd
	logFile  *os.File
	promptFh *os.File
	// watchStop releases the context watcher when the dispatch is over, so
	// a long-lived ctx does not leak one goroutine per dispatch.
	watchStop     chan struct{}
	watchStopOnce sync.Once

	// waitOnce makes Wait idempotent; the results below are what every
	// caller after the first sees.
	waitOnce sync.Once
	exitCode int
	reason   string
	timedOut bool
}

// SpawnRequest is what Spawn needs beyond the provider's own argv.
type SpawnRequest struct {
	// Provider builds the argv and says how the prompt is delivered.
	Provider providers.Provider
	// Req is handed to Provider.Argv verbatim.
	Req providers.Request
	// PromptPath is the file whose bytes become the child's stdin, for a
	// provider on providers.PromptStdin. Python pipes the file rather than
	// embedding the prompt in the argv or using an @file reference, both of
	// which hit escaping edge cases in practice.
	PromptPath string
	// LogPath receives stdout and stderr, merged. Its directory is created
	// if missing.
	LogPath string
	// Cwd is the child's working directory.
	Cwd string
	// Env, when non-nil, replaces the inherited environment. Spawn always
	// sets PWD to Cwd on top of it — see the note in Spawn.
	Env []string
}

// Spawn forks the provider's CLI and returns the supervised child.
//
// The child is put in its own session, and therefore its own process group
// (checklist rule 18). Cleanup can then signal the whole group, catching the
// bash wrappers and sub-agents a coding CLI forks, rather than only the
// direct child — without it, killing the CLI leaves grandchildren orphaned in
// the user's login session (Sprint A / Issue #12).
//
// On any error the caller gets no Spawned and no leaked handles.
func Spawn(ctx context.Context, sr SpawnRequest) (*Spawned, error) {
	if sr.Provider == nil {
		return nil, errors.New("spawn: no provider")
	}
	// The fake hook swaps the argv — and only the argv — so every test below
	// exercises a real fork, a real process group and real signals.
	provider := withFake(sr.Provider)
	argv := provider.Argv(sr.Req)
	if len(argv) == 0 {
		if err := fakeArgvError(provider); err != nil {
			return nil, fmt.Errorf("spawn: %w", err)
		}
		return nil, fmt.Errorf("spawn: %s produced an empty argv", provider.Name())
	}

	if err := os.MkdirAll(filepath.Dir(sr.LogPath), 0o750); err != nil {
		return nil, fmt.Errorf("spawn: create log dir: %w", err)
	}
	// Append, matching Python's "ab": a retry of the same task writes after
	// the previous attempt rather than erasing it.
	//
	// 0600, tighter than the 0644 Python's default umask produces. A
	// dispatch log holds whatever the agent printed — file contents, error
	// messages, sometimes an echoed credential — and the mode is not part
	// of any contract, so there is no reason to keep it group-readable.
	logFile, err := os.OpenFile(sr.LogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) // #nosec G304 -- path is built by the engine from the state dir and a task id
	if err != nil {
		return nil, fmt.Errorf("spawn: open log: %w", err)
	}

	sp := &Spawned{
		LogPath: sr.LogPath,
		logFile: logFile,
	}

	// #nosec G204 -- argv comes from a provider's Argv, which is a fixed
	// flag list plus values from config and the router; it is exec'd
	// directly, never through a shell.
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = sr.Cwd
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Scrub PWD so a CLI that resolves writes against $PWD (opencode does,
	// ignoring the working directory we set) lands in the right tree.
	// Harmless for claude and codex, which both take an explicit dir flag.
	env := sr.Env
	if env == nil {
		env = os.Environ()
	}
	cmd.Env = append(withoutPWD(env), "PWD="+sr.Cwd)

	if provider.PromptDelivery() == providers.PromptStdin {
		promptFh, err := os.Open(sr.PromptPath) // #nosec G304 -- the prompt file the engine just wrote for this task
		if err != nil {
			sp.closeHandles()
			return nil, fmt.Errorf("spawn: open prompt: %w", err)
		}
		sp.promptFh = promptFh
		cmd.Stdin = promptFh
	}

	// The child writes straight into the log file: both streams get the same
	// *os.File, so its file descriptor is handed to the child twice and the
	// kernel interleaves the writes in the order they happen. This is exactly
	// what Python does (`stdout=log_fh, stderr=subprocess.STDOUT`), and it is
	// how the captured fixtures in internal/providers/testdata were recorded
	// — claude's unrecognised-model capture is only a non-JSON blob because
	// its stderr line lands in the same stream.
	//
	// Deliberately NOT cmd.StdoutPipe() with a copy goroutine, which the G2.5
	// brief's "stream it to the parser" reading would suggest. Two reasons,
	// the first of which cost a flaky test before it was understood:
	//
	//  1. `cmd.Wait` closes a pipe made by StdoutPipe as soon as the child
	//     exits — its own doc says it is "incorrect to call Wait before all
	//     reads from the pipe have completed". With the copy running
	//     concurrently, a child that exits while its last writes are still
	//     in flight loses them, and the parse sees a truncated envelope. It
	//     reproduced as roughly one failure in a full -race run.
	//  2. With no parent-side copy there is no pipe buffer to fill, so a
	//     chatty child can never block waiting for the supervisor to read,
	//     and there is no buffer size to tune.
	//
	// Nothing is lost: Parse is a pure function over the whole output
	// (G2.4), so it runs once on the finished log, which is what
	// dispatcher.py does too.
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		sp.closeHandles()
		return nil, fmt.Errorf("spawn: start %s: %w", argv[0], err)
	}

	sp.cmd = cmd
	sp.PID = cmd.Process.Pid
	sp.StartedAt = time.Now()
	sp.watchStop = make(chan struct{})

	// A cancelled context kills the group the same way a timeout does, so
	// Ctrl-C on the dispatch loop does not leave a tree of agents behind.
	if ctx != nil && ctx.Done() != nil {
		go func() {
			select {
			case <-ctx.Done():
				sp.signalGroup(syscall.SIGTERM)
			case <-sp.watchStop:
			}
		}()
	}

	return sp, nil
}

// withoutPWD returns env with any PWD entry removed, so the caller can append
// exactly one.
func withoutPWD(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if len(kv) >= 4 && kv[:4] == "PWD=" {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// stopWatch releases the context watcher. Idempotent, because Wait's defer
// and an error path may both reach it.
func (s *Spawned) stopWatch() {
	if s.watchStop == nil {
		return
	}
	s.watchStopOnce.Do(func() { close(s.watchStop) })
}

// closeHandles releases the file handles Spawn opened. Safe to call twice.
func (s *Spawned) closeHandles() {
	if s.logFile != nil {
		_ = s.logFile.Close()
		s.logFile = nil
	}
	if s.promptFh != nil {
		_ = s.promptFh.Close()
		s.promptFh = nil
	}
}

// signalGroup sends sig to the child's process group, falling back to the
// child alone if the group cannot be resolved. Port of _signal_child_group.
func (s *Spawned) signalGroup(sig syscall.Signal) {
	if s.cmd == nil || s.cmd.Process == nil {
		return
	}
	pid := s.cmd.Process.Pid
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		// The group is gone or unreadable; the direct pid is all that is
		// left to try. A failure here means the process already exited.
		_ = s.cmd.Process.Signal(sig)
		return
	}
	// Negative pid means "the process group", which is what catches the
	// wrappers and sub-agents the CLI forked.
	if err := syscall.Kill(-pgid, sig); err != nil {
		_ = s.cmd.Process.Signal(sig)
	}
}
