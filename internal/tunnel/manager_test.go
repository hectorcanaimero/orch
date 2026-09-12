package tunnel

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func fakeTunnelScript(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs("testdata/fakebin/fake-tunnel.sh")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("fake-tunnel.sh missing: %v", err)
	}
	return path
}

// waitReaderDone blocks until the manager's current reader goroutine
// finishes (or fails the test after a generous timeout) — the
// channel-based wait CHECKLIST rule 22 asks for instead of a bare
// time.Sleep.
func waitReaderDone(t *testing.T, m *Manager) {
	t.Helper()
	select {
	case <-m.readerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the reader goroutine to finish")
	}
}

func TestStartWritesRunningStateAndOnDiskFiles(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, nil, 0)
	cfg := ManagerConfig{
		Provider: ProviderAutossh,
		Command:  fakeTunnelScript(t),
	}
	snap, err := m.Start(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if snap.State != StateRunning {
		t.Errorf("State = %q, want %q", snap.State, StateRunning)
	}
	if snap.PID == nil || *snap.PID <= 0 {
		t.Errorf("PID = %v, want a positive pid", snap.PID)
	}
	if snap.Provider == nil || *snap.Provider != ProviderAutossh {
		t.Errorf("Provider = %v, want %q", snap.Provider, ProviderAutossh)
	}

	for _, name := range []string{lockFilename, pidFilename, stateFilename} {
		if _, err := os.Stat(filepath.Join(dir, "tunnel", name)); err != nil {
			t.Errorf("expected %s to exist: %v", name, err)
		}
	}

	if _, err := m.Stop(); err != nil {
		t.Fatal(err)
	}
	waitReaderDone(t, m)
}

func TestStartCapturesURLFromStdout(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, nil, 0)
	cfg := ManagerConfig{
		Provider: ProviderAutossh,
		Command:  fakeTunnelScript(t),
	}
	t.Setenv("FAKE_TUNNEL_LINES", "connecting...\nhttps://abc123.a.pinggy.link\n")
	if _, err := m.Start(cfg); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	var url *string
	for time.Now().Before(deadline) {
		url = m.Status().URL
		if url != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if url == nil {
		t.Fatal("URL was never captured")
	}
	if *url != "https://abc123.a.pinggy.link" {
		t.Errorf("URL = %q, want %q", *url, "https://abc123.a.pinggy.link")
	}
	if _, err := m.Stop(); err != nil {
		t.Fatal(err)
	}
	waitReaderDone(t, m)
}

func TestStartAssemblesBoreURLFromNamedGroup(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, nil, 0)
	cfg := ManagerConfig{
		Provider: ProviderBore,
		Command:  fakeTunnelScript(t),
	}
	t.Setenv("FAKE_TUNNEL_LINES", "INFO bore_cli::client: listening at bore.pub:41230\n")
	if _, err := m.Start(cfg); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	var url *string
	for time.Now().Before(deadline) {
		url = m.Status().URL
		if url != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if url == nil || *url != "http://bore.pub:41230" {
		t.Fatalf("URL = %v, want %q", url, "http://bore.pub:41230")
	}
	if _, err := m.Stop(); err != nil {
		t.Fatal(err)
	}
	waitReaderDone(t, m)
}

func TestReconnectsAreCountedForAutosshOnly(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, nil, 0)
	cfg := ManagerConfig{Provider: ProviderAutossh, Command: fakeTunnelScript(t)}
	t.Setenv("FAKE_TUNNEL_LINES", "starting ssh\nssh exited\nstarting ssh\n")
	if _, err := m.Start(cfg); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && m.Status().AutosshReconnects < 3 {
		time.Sleep(10 * time.Millisecond)
	}
	if got := m.Status().AutosshReconnects; got != 3 {
		t.Errorf("AutosshReconnects = %d, want 3", got)
	}
	if _, err := m.Stop(); err != nil {
		t.Fatal(err)
	}
	waitReaderDone(t, m)
}

func TestRedactionAppliesToBufferedLogs(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, nil, 0)
	cfg := ManagerConfig{Provider: ProviderBore, Command: fakeTunnelScript(t)}
	secret := "abcdef0123456789ABCDEF0123456789"
	t.Setenv("FAKE_TUNNEL_LINES", "Authorization: Bearer "+secret+"\n")
	t.Setenv("FAKE_TUNNEL_EXIT", "0")
	if _, err := m.Start(cfg); err != nil {
		t.Fatal(err)
	}
	waitReaderDone(t, m)

	logs := strings.Join(m.LogsSnapshot(), "\n")
	if strings.Contains(logs, secret) {
		t.Errorf("raw secret leaked into logs: %q", logs)
	}
	if !strings.Contains(logs, redactPlaceholder) {
		t.Errorf("expected redaction placeholder in logs: %q", logs)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "tunnel", stdoutLogFilename)) // #nosec G304 -- t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Errorf("raw secret leaked into stdout.log: %q", raw)
	}
}

func TestStartWhenAlreadyRunningReturnsErrAlreadyRunning(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, nil, 0)
	cfg := ManagerConfig{Provider: ProviderAutossh, Command: fakeTunnelScript(t)}
	if _, err := m.Start(cfg); err != nil {
		t.Fatal(err)
	}
	_, err := m.Start(cfg)
	if err != ErrAlreadyRunning {
		t.Errorf("err = %v, want ErrAlreadyRunning", err)
	}
	if _, err := m.Stop(); err != nil {
		t.Fatal(err)
	}
	waitReaderDone(t, m)
}

func TestStartWhenLockedByLivePeerReturnsErrLocked(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, nil, 0)
	if err := os.MkdirAll(filepath.Join(dir, "tunnel"), 0o750); err != nil {
		t.Fatal(err)
	}
	// This test process's own pid is guaranteed alive — stands in for
	// "another dashboard process holds the lock".
	lockBody := `{"pid": ` + strconv.Itoa(os.Getpid()) + `, "provider": "autossh"}`
	if err := os.WriteFile(filepath.Join(dir, "tunnel", lockFilename), []byte(lockBody), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := ManagerConfig{Provider: ProviderAutossh, Command: fakeTunnelScript(t)}
	_, err := m.Start(cfg)
	if err != ErrLocked {
		t.Errorf("err = %v, want ErrLocked", err)
	}
}

func TestStopWhenIdleReturnsErrNotRunning(t *testing.T) {
	m := NewManager(t.TempDir(), nil, 0)
	if _, err := m.Stop(); err != ErrNotRunning {
		t.Errorf("err = %v, want ErrNotRunning", err)
	}
}

func TestStopTerminatesProcessAndClearsFiles(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, nil, 0)
	cfg := ManagerConfig{Provider: ProviderAutossh, Command: fakeTunnelScript(t)}
	if _, err := m.Start(cfg); err != nil {
		t.Fatal(err)
	}
	snap, err := m.Stop()
	if err != nil {
		t.Fatal(err)
	}
	if snap.State != StateIdle {
		t.Errorf("State = %q, want %q", snap.State, StateIdle)
	}
	if snap.PID != nil {
		t.Errorf("PID = %v, want nil", snap.PID)
	}
	if snap.RestartCount != 1 {
		t.Errorf("RestartCount = %d, want 1", snap.RestartCount)
	}
	waitReaderDone(t, m)
	for _, name := range []string{lockFilename, pidFilename} {
		if _, err := os.Stat(filepath.Join(dir, "tunnel", name)); !os.IsNotExist(err) {
			t.Errorf("expected %s to be removed after Stop, stat err = %v", name, err)
		}
	}
}

func TestSweepStaleLockNoFilesIsNoop(t *testing.T) {
	m := NewManager(t.TempDir(), nil, 0)
	before := m.Status()
	m.SweepStaleLock(nil)
	after := m.Status()
	if before != after {
		t.Errorf("SweepStaleLock changed state with nothing on disk: %+v -> %+v", before, after)
	}
}

func TestSweepStaleLockRemovesDeadPID(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, nil, 0)
	tunnelDir := filepath.Join(dir, "tunnel")
	if err := os.MkdirAll(tunnelDir, 0o750); err != nil {
		t.Fatal(err)
	}
	// PID 1 belongs to init/systemd in every real container and mount
	// namespace this test runs in, but the whole point of this fixture is
	// a PID that does NOT exist as seen by pidAlive from inside the test's
	// own namespace — use a value guaranteed unassigned instead: the
	// maximum on Linux is well below 2^22.
	deadPID := 1 << 22
	if err := os.WriteFile(filepath.Join(tunnelDir, pidFilename), []byte(strconv.Itoa(deadPID)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tunnelDir, lockFilename), []byte(`{"pid": 0}`), 0o600); err != nil {
		t.Fatal(err)
	}

	m.SweepStaleLock(nil)

	if m.Status().State != StateIdle {
		t.Errorf("State = %q, want %q after sweeping a dead pid", m.Status().State, StateIdle)
	}
	for _, name := range []string{lockFilename, pidFilename} {
		if _, err := os.Stat(filepath.Join(tunnelDir, name)); !os.IsNotExist(err) {
			t.Errorf("expected %s removed, stat err = %v", name, err)
		}
	}
}

func TestSweepStaleLockWithNilConfigAlwaysCleansUpRegardlessOfLiveness(t *testing.T) {
	// Ported from manager.py's sweep_stale_lock: `adopted` can be True
	// with cfg=None (any live pid), but the adoption branch itself is
	// gated on `cfg is not None` too — so cfg=None always falls through
	// to release_lock_files, live pid or not. cfg=None exists for the
	// pid-liveness check alone, not as a real "adopt with no command
	// check" mode.
	dir := t.TempDir()
	m := NewManager(dir, nil, 0)
	tunnelDir := filepath.Join(dir, "tunnel")
	if err := os.MkdirAll(tunnelDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tunnelDir, pidFilename), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}

	m.SweepStaleLock(nil)

	if _, err := os.Stat(filepath.Join(tunnelDir, pidFilename)); !os.IsNotExist(err) {
		t.Errorf("expected the pid file removed (cfg=nil never adopts), stat err = %v", err)
	}
	if m.Status().State != StateIdle {
		t.Errorf("State = %q, want %q", m.Status().State, StateIdle)
	}
}

func TestSweepStaleLockAdoptsLivePIDMatchingCommand(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, nil, 0)
	// Command is "sh", the script is an arg — `ps -o comm=` for a process
	// started this way deterministically reports "sh" everywhere;
	// invoking the script directly via its shebang leaves what `comm`
	// reports up to the platform's binfmt handling, which this test isn't
	// trying to pin down.
	cfg := ManagerConfig{Provider: ProviderAutossh, Command: "sh", Args: []string{fakeTunnelScript(t)}}
	// Start a real long-lived child so pidComm has something real to
	// report, then simulate a restarted dashboard process that only
	// knows the pid (as if state.json/pid survived a crash) by building a
	// FRESH manager pointed at the same state dir.
	if _, err := m.Start(cfg); err != nil {
		t.Fatal(err)
	}
	livePID := *m.Status().PID
	fresh := NewManager(dir, nil, 0)
	fresh.SweepStaleLock(&cfg)

	got := fresh.Status()
	if got.State != StateRunning {
		t.Fatalf("State = %q, want %q (adopted): %+v", got.State, StateRunning, got)
	}
	if got.PID == nil || *got.PID != livePID {
		t.Errorf("PID = %v, want %d", got.PID, livePID)
	}

	if _, err := m.Stop(); err != nil {
		t.Fatal(err)
	}
	waitReaderDone(t, m)
}

func TestReadStateReturnsFalseWhenAbsent(t *testing.T) {
	if _, ok := ReadState(t.TempDir()); ok {
		t.Error("ReadState on an empty dir: ok = true, want false")
	}
}

func TestReadStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, nil, 0)
	cfg := ManagerConfig{Provider: ProviderAutossh, Command: fakeTunnelScript(t)}
	if _, err := m.Start(cfg); err != nil {
		t.Fatal(err)
	}
	got, ok := ReadState(dir)
	if !ok {
		t.Fatal("ReadState: ok = false, want true")
	}
	if got.State != StateRunning {
		t.Errorf("State = %q, want %q", got.State, StateRunning)
	}
	if _, err := m.Stop(); err != nil {
		t.Fatal(err)
	}
	waitReaderDone(t, m)
}
