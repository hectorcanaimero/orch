package tunnel

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Filenames under <state_dir>/tunnel/, matching
// orchestrator/dashboard/tunnel/manager.py's LOCK_FILENAME/PID_FILENAME/
// STATE_FILENAME/STDOUT_LOG_FILENAME exactly — a project's tunnel/
// directory is shared between whichever binary (Python or Go) last ran
// the dashboard, so the on-disk shape is a wire contract too.
const (
	lockFilename      = "lock"
	pidFilename       = "pid"
	stateFilename     = "state.json"
	stdoutLogFilename = "stdout.log"
)

const (
	defaultStopTimeout    = 5 * time.Second
	defaultLogBufferLines = 500
)

// Tunnel states, matching manager.py's STATE_* constants (and therefore
// the "state"/"phase" values GET /api/tunnel/status returns) verbatim.
const (
	StateIdle     = "idle"
	StateStarting = "starting"
	StateRunning  = "running"
	StateStopping = "stopping"
	StateError    = "error"
)

// redactPatterns mirrors manager.py's _REDACT_PATTERNS (TUN-10). Order
// matters: the three prefixed forms run first so their explicit prefix
// text survives replacement; the loose 32+ hex fallback catches a raw
// token appearing unattached. Go's RE2 supports \b same as Python's re.
var redactPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(Bearer\s+)[A-Za-z0-9._-]{8,}`),
	regexp.MustCompile(`(?i)(token=)[A-Za-z0-9._-]{8,}`),
	regexp.MustCompile(`(?i)(Authorization:\s*Bearer\s+)[A-Za-z0-9._-]{8,}`),
	regexp.MustCompile(`\b[A-Fa-f0-9]{32,}\b`),
}

const redactPlaceholder = "***REDACTED***"

// redact replaces known token shapes with a fixed placeholder before a
// line is buffered or logged (TUN-10) — the first three patterns keep
// their capturing prefix, the last replaces the whole match.
func redact(line string) string {
	out := line
	for i, pat := range redactPatterns {
		if i < 3 {
			out = pat.ReplaceAllString(out, "${1}"+redactPlaceholder)
		} else {
			out = pat.ReplaceAllString(out, redactPlaceholder)
		}
	}
	return out
}

func utcISO(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05Z")
}

// atomicWriteJSON mirrors manager.py's `_atomic_write_json`: tmp file in
// the same directory, then a rename — atomic on the same filesystem, so a
// reader never observes a half-written state.json.
func atomicWriteJSON(path string, data any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("tunnel: create %s: %w", filepath.Dir(path), err)
	}
	body, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("tunnel: marshal %s: %w", path, err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return fmt.Errorf("tunnel: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("tunnel: rename %s to %s: %w", tmp, path, err)
	}
	return nil
}

// pidAlive reports whether pid names a live process. Signal 0 checks
// existence without delivering anything; EPERM means the process exists
// but isn't ours, which is still alive. Duplicated from the same
// three-outcome shape internal/engine's own pidAlive uses rather than
// exported from there — the two packages have no dependency relationship
// (CHECKLIST rule 14) and this is a five-line syscall wrapper, not
// business logic worth an import for.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	switch {
	case err == nil:
		return true
	case errors.Is(err, syscall.ESRCH):
		return false
	case errors.Is(err, syscall.EPERM):
		return true
	default:
		return false
	}
}

// pidComm returns the basename of the binary running as pid ("autossh"),
// or "" if it can't be determined. Shells out to `ps -p <pid> -o comm=`
// rather than reading /proc directly — ported as-is from manager.py's
// `_pid_comm`, whose own comment explains why: portable across Linux and
// macOS, where the latter has no procfs. `ps` here is a bounded, one-shot
// diagnostic read (not a supervised long-lived child), so it gets the
// context timeout CHECKLIST rule 18 asks for; Setpgid doesn't apply to a
// command that exits on its own within 2s.
func pidComm(pid int) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output() // #nosec G204 -- pid is our own int, ps/-p/-o are fixed literals
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	if line == "" {
		return ""
	}
	return filepath.Base(line)
}

// State is the manager's current status snapshot: the exact JSON shape
// GET /api/tunnel/status returns and state.json persists on disk. Field
// names and null-vs-zero-value handling match manager.py's
// `_blank_state()` dict verbatim — this is a wire and on-disk contract,
// not just an internal type, so nullable fields are pointers rather than
// zero values a JSON reader could confuse with a real 0/"".
type State struct {
	State             string  `json:"state"`
	Provider          *string `json:"provider"`
	PID               *int    `json:"pid"`
	URL               *string `json:"url"`
	StartedAt         *string `json:"started_at"`
	URLAt             *string `json:"url_at"`
	RestartCount      int     `json:"restart_count"`
	AutosshReconnects int     `json:"autossh_reconnects"`
	LastError         *string `json:"last_error"`
	LastExitCode      *int    `json:"last_exit_code"`
	Phase             string  `json:"phase"`
}

func blankState() State {
	return State{State: StateIdle, Phase: StateIdle}
}

func strPtr(s string) *string { return &s }
func intPtr(i int) *int       { return &i }

// ManagerConfig is the runtime-facing subset of config.yaml's `tunnel:`
// block the manager needs — its own type rather than a reference into the
// full config.Config, so a test builds one without dragging in the whole
// config graph. Mirrors manager.py's TunnelManagerConfig field-for-field.
type ManagerConfig struct {
	Provider string
	Command  string
	Args     []string
	// URLRegex overrides the provider's own URLPattern when non-empty.
	URLRegex         string
	URLParseTimeoutS int
	StopTimeout      time.Duration
}

// Spawned is a started child process plus the read end of its merged
// stdout+stderr — the thing a SpawnFunc hands back, already running
// (Popen semantics: Python's constructor spawns immediately, and so does
// this).
type Spawned struct {
	Cmd    *exec.Cmd
	Stdout io.ReadCloser
}

// SpawnFunc starts argv as its own process group and returns the running
// command plus a reader for its combined output. Overridable per-Manager
// for tests — mirrors manager.py's `popen_factory` constructor parameter.
type SpawnFunc func(argv []string) (Spawned, error)

// defaultSpawn is the real SpawnFunc: a fresh process group (so a SIGTERM
// to this process doesn't propagate to the child — CHECKLIST rule 18's
// Setpgid half) with stdout and stderr merged into one pipe, matching
// Python's `stdout=PIPE, stderr=STDOUT`. No context timeout here: unlike
// a bounded provider dispatch, this child is meant to run indefinitely
// until Stop() signals it — rule 18's timeout half is about a task with a
// deadline, and a supervised daemon has none by design (same as Python's
// own subprocess.Popen call, which is timeout-free for exactly this
// reason).
func defaultSpawn(argv []string) (Spawned, error) {
	if len(argv) == 0 {
		return Spawned{}, errors.New("tunnel: empty argv")
	}
	cmd := exec.Command(argv[0], argv[1:]...) // #nosec G204 -- argv comes from the config-gated provider registry, not arbitrary input
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	pr, pw, err := os.Pipe()
	if err != nil {
		return Spawned{}, fmt.Errorf("tunnel: create output pipe: %w", err)
	}
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		_ = pr.Close()
		_ = pw.Close()
		return Spawned{}, err
	}
	// The child inherited its own copy of the write end; closing ours here
	// is what lets pr see EOF once the child (and anything it forked)
	// exits, instead of blocking forever on a fd this process still holds
	// open.
	if err := pw.Close(); err != nil {
		return Spawned{}, fmt.Errorf("tunnel: close write end after spawn: %w", err)
	}
	return Spawned{Cmd: cmd, Stdout: pr}, nil
}

// signalGroup mirrors manager.py's `_signal_pgroup`: signal the whole
// process group so a child ssh spawned by autossh dies too, falling back
// to a plain kill if the group is already gone.
func signalGroup(pid int, sig syscall.Signal) {
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		_ = syscall.Kill(pid, sig)
		return
	}
	if err := syscall.Kill(-pgid, sig); err != nil {
		_ = syscall.Kill(pid, sig)
	}
}

// Manager is a single-child supervisor for the tunnel provider process.
// Ported from orchestrator/dashboard/tunnel/manager.py's TunnelManager;
// see that file's module doc for the concurrency and reconciler design
// this mirrors. Zero value is not usable — construct with NewManager.
type Manager struct {
	stateDir  string
	tunnelDir string
	spawn     SpawnFunc
	logMax    int

	mu            sync.Mutex
	state         State
	proc          *exec.Cmd
	stopRequested bool

	logsMu sync.Mutex
	logs   []string

	// readerDone is closed when the current reader goroutine (started by
	// startReader) finishes — after onChildExit has run and state is
	// final. Tests synchronize on it instead of sleeping (CHECKLIST rule
	// 22); production code never reads it.
	readerDone chan struct{}
}

// NewManager builds a Manager rooted at stateDir (its own "tunnel"
// subdirectory holds the lock/pid/state/log files). spawn nil uses the
// real subprocess spawner; logBufferLines <= 0 uses the same 500-line
// default Python does.
func NewManager(stateDir string, spawn SpawnFunc, logBufferLines int) *Manager {
	if spawn == nil {
		spawn = defaultSpawn
	}
	if logBufferLines <= 0 {
		logBufferLines = defaultLogBufferLines
	}
	return &Manager{
		stateDir:  stateDir,
		tunnelDir: filepath.Join(stateDir, "tunnel"),
		spawn:     spawn,
		logMax:    logBufferLines,
		state:     blankState(),
	}
}

func (m *Manager) lockPath() string  { return filepath.Join(m.tunnelDir, lockFilename) }
func (m *Manager) pidPath() string   { return filepath.Join(m.tunnelDir, pidFilename) }
func (m *Manager) statePath() string { return filepath.Join(m.tunnelDir, stateFilename) }
func (m *Manager) stdoutLogPath() string {
	return filepath.Join(m.tunnelDir, stdoutLogFilename)
}

// Status returns a copy of the current state snapshot.
func (m *Manager) Status() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

// LogsSnapshot returns the current tail. Real live-tailing is the HTTP
// layer's SSE route (G5.2), reading this repeatedly — mirrors
// manager.py's `logs_iter` docstring on the same split.
func (m *Manager) LogsSnapshot() []string {
	m.logsMu.Lock()
	defer m.logsMu.Unlock()
	out := make([]string, len(m.logs))
	copy(out, m.logs)
	return out
}

// ErrAlreadyRunning and ErrLocked are Start's two 409-worthy failures;
// ErrNotRunning is Stop's. Sentinel errors rather than string comparison
// (Python raises RuntimeError("already_running") and matches the message
// string) — errors.Is is the Go-native version of that same dispatch.
var (
	ErrAlreadyRunning = errors.New("tunnel: already running")
	ErrLocked         = errors.New("tunnel: locked by another process")
	ErrNotRunning     = errors.New("tunnel: not running")
)

// Start spawns the tunnel subprocess and returns the resulting status
// snapshot. Returns ErrAlreadyRunning when the manager isn't idle, and
// ErrLocked when the on-disk lock is held by a live peer (another
// dashboard process) — the HTTP layer maps both to 409, same as Python.
func (m *Manager) Start(cfg ManagerConfig) (State, error) {
	m.mu.Lock()
	if m.state.State != StateIdle {
		m.mu.Unlock()
		return State{}, ErrAlreadyRunning
	}
	if err := m.acquireLockOrErr(cfg.Provider); err != nil {
		m.mu.Unlock()
		return State{}, err
	}
	spec, ok := Resolve(cfg.Provider)
	if !ok {
		m.mu.Unlock()
		m.releaseLockFiles()
		return State{}, fmt.Errorf("tunnel: unknown provider %q", cfg.Provider)
	}
	args := cfg.Args
	if len(args) == 0 {
		args = spec.DefaultArgs
	}
	argv := append([]string{cfg.Command}, args...)

	m.state = State{
		State:     StateStarting,
		Phase:     StateStarting,
		Provider:  strPtr(cfg.Provider),
		StartedAt: strPtr(utcISO(time.Now())),
	}
	if err := m.writeStateLocked(); err != nil {
		m.mu.Unlock()
		return State{}, err
	}

	spawned, err := m.spawn(argv)
	if err != nil {
		m.state.State = StateError
		m.state.Phase = StateError
		m.state.LastError = strPtr("spawn_failed:" + err.Error())
		_ = m.writeStateLocked()
		m.mu.Unlock()
		m.releaseLockFiles()
		return State{}, fmt.Errorf("tunnel: spawn %q: %w", cfg.Command, err)
	}

	m.proc = spawned.Cmd
	pid := spawned.Cmd.Process.Pid
	m.state.State = StateRunning
	m.state.Phase = StateRunning
	m.state.PID = intPtr(pid)
	if err := m.writeStateLocked(); err != nil {
		m.mu.Unlock()
		return State{}, err
	}
	if err := m.writePIDFile(pid); err != nil {
		m.mu.Unlock()
		return State{}, err
	}
	snapshot := m.state
	m.mu.Unlock()

	m.startReader(spec, cfg, spawned.Stdout)
	return snapshot, nil
}

// Stop SIGTERMs the process group and escalates to SIGKILL after
// cfg's StopTimeout (5s if zero) has elapsed without the child exiting.
// Returns ErrNotRunning when the manager is already idle.
// Stop signals the tunnel subprocess and waits for it to exit.
//
// Unlike Python's TunnelManager (where subprocess.Popen.wait() is safe to
// call concurrently from multiple threads, guarded by CPython's own
// _waitpid_lock), Go's exec.Cmd.Wait() is documented as unsafe to call
// more than once or from more than one goroutine. So there is exactly one
// caller of proc.Wait() in this whole package — onChildExit, run by the
// reader goroutine once the child's stdout hits EOF — and Stop signals
// then waits on readerDone instead of reaping the process itself.
// stopRequested tells onChildExit this exit was requested (bump
// RestartCount, treat as clean) rather than a crash.
func (m *Manager) Stop() (State, error) {
	m.mu.Lock()
	if m.state.State == StateIdle {
		m.mu.Unlock()
		return State{}, ErrNotRunning
	}
	proc := m.proc
	done := m.readerDone
	m.stopRequested = true
	m.state.State = StateStopping
	m.state.Phase = StateStopping
	_ = m.writeStateLocked()
	m.mu.Unlock()

	reaped := true
	if proc != nil && proc.Process != nil {
		signalGroup(proc.Process.Pid, syscall.SIGTERM)
		reaped = done == nil || waitForDone(done, defaultStopTimeout)
		if !reaped {
			signalGroup(proc.Process.Pid, syscall.SIGKILL)
			reaped = waitForDone(done, defaultStopTimeout)
		}
	}

	if !reaped {
		// The child survived SIGKILL long enough that we stop waiting —
		// same "swallow the second TimeoutExpired and finalize anyway"
		// behavior as Python. onChildExit still runs once the pipe
		// eventually closes; by then Start() cannot have run again
		// (state is idle), so its re-write of the same idle state is a
		// harmless no-op.
		m.mu.Lock()
		m.state = State{State: StateIdle, Phase: StateIdle, RestartCount: m.state.RestartCount + 1}
		m.stopRequested = false
		_ = m.writeStateLocked()
		m.proc = nil
		m.mu.Unlock()
		m.releaseLockFiles()
	}

	return m.Status(), nil
}

// waitForDone reports whether done closed within d.
func waitForDone(done <-chan struct{}, d time.Duration) bool {
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// SweepStaleLock is the boot-time reconciler (TUN-8): if state/tunnel/pid
// names a PID that's still alive AND whose comm matches cfg's command, the
// manager adopts it as running. Otherwise both lock and pid files are
// removed and state stays idle. cfg nil means "no expected command" —
// any live PID is adopted (only used by tests that don't care).
func (m *Manager) SweepStaleLock(cfg *ManagerConfig) {
	_, pidErr := os.Stat(m.pidPath())
	_, lockErr := os.Stat(m.lockPath())
	if os.IsNotExist(pidErr) && os.IsNotExist(lockErr) {
		return
	}

	raw, err := os.ReadFile(m.pidPath()) // #nosec G304 -- fixed path under this manager's own state dir
	pid := 0
	if err == nil {
		pid, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
	}

	adopted := false
	if pid > 0 && pidAlive(pid) {
		if cfg == nil {
			adopted = true
		} else {
			comm := pidComm(pid)
			adopted = comm != "" && comm == filepath.Base(cfg.Command)
		}
	}

	if adopted && cfg != nil {
		m.mu.Lock()
		m.state = State{
			State:     StateRunning,
			Phase:     StateRunning,
			Provider:  strPtr(cfg.Provider),
			PID:       intPtr(pid),
			StartedAt: strPtr(utcISO(time.Now())),
		}
		_ = m.writeStateLocked()
		m.mu.Unlock()
		return
	}
	m.releaseLockFiles()
}

func (m *Manager) acquireLockOrErr(provider string) error {
	lockPath := m.lockPath()
	if data, err := os.ReadFile(lockPath); err == nil { // #nosec G304 -- fixed path under this manager's own state dir
		var payload struct {
			PID int `json:"pid"`
		}
		if json.Unmarshal(data, &payload) == nil && payload.PID > 0 && pidAlive(payload.PID) {
			return ErrLocked
		}
		_ = os.Remove(lockPath) // stale — sweep and continue
	}
	return atomicWriteJSON(lockPath, map[string]any{
		"pid":        os.Getpid(),
		"started_at": utcISO(time.Now()),
		"provider":   provider,
		"phase":      StateStarting,
	})
}

func (m *Manager) writePIDFile(pid int) error {
	if err := os.MkdirAll(m.tunnelDir, 0o750); err != nil {
		return fmt.Errorf("tunnel: create %s: %w", m.tunnelDir, err)
	}
	tmp := m.pidPath() + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(pid)), 0o600); err != nil {
		return fmt.Errorf("tunnel: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, m.pidPath()); err != nil {
		return fmt.Errorf("tunnel: rename %s: %w", tmp, err)
	}
	return nil
}

func (m *Manager) releaseLockFiles() {
	for _, p := range []string{m.lockPath(), m.pidPath()} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			// Best-effort cleanup, matching Python's own OSError-swallow
			// here — a lingering lock file is recovered by the next
			// SweepStaleLock, not by this call succeeding.
			continue
		}
	}
}

// writeStateLocked persists m.state. Caller must hold m.mu.
func (m *Manager) writeStateLocked() error {
	return atomicWriteJSON(m.statePath(), m.state)
}

// startReader tails stdout on its own goroutine, redacting each line,
// buffering it, appending to stdout.log, and watching for the provider's
// URL and (autossh only) reconnect patterns — mirrors manager.py's
// `_start_reader_thread` closure.
func (m *Manager) startReader(spec ProviderSpec, cfg ManagerConfig, stdout io.ReadCloser) {
	done := make(chan struct{})
	m.readerDone = done

	pattern := cfg.URLRegex
	if pattern == "" {
		pattern = spec.URLPattern
	}
	urlRe, err := compileURLRegex(pattern)
	if err != nil {
		m.mu.Lock()
		m.state.LastError = strPtr("bad_url_regex:" + err.Error())
		_ = m.writeStateLocked()
		m.mu.Unlock()
		_ = stdout.Close()
		close(done)
		return
	}
	reconnectRe, err := compileReconnectRegex(spec)
	if err != nil {
		reconnectRe = regexp.MustCompile(`$^`) // matches nothing, same effect as Python's empty pattern
	}
	timeoutS := cfg.URLParseTimeoutS
	if timeoutS < 1 {
		timeoutS = 1
	}
	deadline := time.Now().Add(time.Duration(timeoutS) * time.Second)

	go func() {
		defer close(done)
		defer func() { _ = stdout.Close() }()
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		urlSeen := false
		timeoutFlagged := false
		for scanner.Scan() {
			line := redact(scanner.Text())
			m.appendLog(line)
			m.appendStdoutLog(line)

			if !urlSeen {
				if loc := urlRe.FindStringSubmatchIndex(line); loc != nil {
					match := submatches(line, loc)
					captured := formatURL(spec.URLTemplate, urlRe, match)
					m.mu.Lock()
					m.state.URL = strPtr(captured)
					m.state.URLAt = strPtr(utcISO(time.Now()))
					_ = m.writeStateLocked()
					m.mu.Unlock()
					urlSeen = true
				}
			}
			if spec.ReconnectPattern != "" && reconnectRe.MatchString(line) {
				m.mu.Lock()
				m.state.AutosshReconnects++
				_ = m.writeStateLocked()
				m.mu.Unlock()
			}
			if !urlSeen && !timeoutFlagged && time.Now().After(deadline) {
				m.mu.Lock()
				m.state.LastError = strPtr("url_parse_timeout")
				_ = m.writeStateLocked()
				m.mu.Unlock()
				timeoutFlagged = true
			}
		}
		if err := scanner.Err(); err != nil {
			m.mu.Lock()
			m.state.LastError = strPtr("reader_error:" + err.Error())
			_ = m.writeStateLocked()
			m.mu.Unlock()
		}
		m.onChildExit()
	}()
}

// submatches renders regexp match indices (from FindStringSubmatchIndex)
// into the []string shape formatURL expects (whole match + each group,
// "" for a group that didn't participate) — the Go equivalent of Python's
// re.Match object indexing.
func submatches(s string, loc []int) []string {
	out := make([]string, len(loc)/2)
	for i := range out {
		start, end := loc[2*i], loc[2*i+1]
		if start < 0 || end < 0 {
			continue
		}
		out[i] = s[start:end]
	}
	return out
}

func (m *Manager) appendLog(line string) {
	m.logsMu.Lock()
	defer m.logsMu.Unlock()
	m.logs = append(m.logs, line)
	if len(m.logs) > m.logMax {
		m.logs = m.logs[len(m.logs)-m.logMax:]
	}
}

func (m *Manager) appendStdoutLog(line string) {
	if err := os.MkdirAll(m.tunnelDir, 0o750); err != nil {
		return
	}
	f, err := os.OpenFile(m.stdoutLogPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) // #nosec G304 -- fixed path under this manager's own state dir
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = f.WriteString(line + "\n")
}

// onChildExit finalizes state once the reader goroutine's scan loop ends
// (the child's stdout closed, which only happens on exit). STOPPING or
// IDLE means Stop() already drove this to completion and this is a race
// arriving after the fact — either way the manager stays idle. Anything
// else means the child died on its own: error.
func (m *Manager) onChildExit() {
	proc := m.proc
	var exitCode *int
	if proc != nil {
		// The sole call to proc.Wait() in this package — see Stop's doc
		// comment for why Stop itself must not also call it.
		_ = proc.Wait()
		if proc.ProcessState != nil {
			exitCode = intPtr(proc.ProcessState.ExitCode())
		}
	}
	m.mu.Lock()
	cleanExit := m.stopRequested || m.state.State == StateStopping || m.state.State == StateIdle
	restartCount := m.state.RestartCount
	if m.stopRequested {
		restartCount++
	}
	next := State{
		State:        StateIdle,
		Phase:        StateIdle,
		RestartCount: restartCount,
		LastExitCode: exitCode,
	}
	if !cleanExit {
		next.State = StateError
		next.Phase = StateError
		next.LastError = strPtr("child_exited")
	}
	m.state = next
	m.stopRequested = false
	_ = m.writeStateLocked()
	m.proc = nil
	m.mu.Unlock()
	m.releaseLockFiles()
}

// ReadState reads <state_dir>/tunnel/state.json directly — for a caller
// that wants a snapshot without holding a Manager reference (mirrors
// manager.py's module-level `read_state` convenience function). Returns
// (State{}, false) when absent or corrupt, never an error: an unreadable
// state file means "nothing running", the same as a fresh project.
func ReadState(stateDir string) (State, bool) {
	path := filepath.Join(stateDir, "tunnel", stateFilename)
	data, err := os.ReadFile(path) // #nosec G304 -- fixed path under the caller's own project state dir
	if err != nil {
		return State{}, false
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return State{}, false
	}
	return s, true
}
