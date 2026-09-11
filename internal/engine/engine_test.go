package engine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/providers"
)

// ---- Fixtures ------------------------------------------------------------

// fakeDir writes a canned response for one backend/task under a temp dir and
// returns the dir, ready for ORCH_FAKE_PROVIDER.
func fakeDir(t *testing.T, backend model.Backend, taskID, out string, exitCode int, sleep string) string {
	t.Helper()
	dir := t.TempDir()
	base := filepath.Join(dir, string(backend))
	if err := os.MkdirAll(base, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	write := func(ext, content string) {
		if err := os.WriteFile(filepath.Join(base, taskID+ext), []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", ext, err)
		}
	}
	write(fakeOutExt, out)
	write(fakeExitExt, strconv.Itoa(exitCode))
	if sleep != "" {
		write(fakeSleepExt, sleep)
	}
	return dir
}

func promptFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "prompt.txt")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	return p
}

func claudeDispatch(t *testing.T, taskID string) Dispatch {
	t.Helper()
	return Dispatch{
		Provider: providers.ClaudeProvider{},
		Req: providers.Request{
			TaskID: taskID,
			Route: model.RouteEntry{
				Backend:  model.BackendClaude,
				CLIModel: "claude-haiku-4-5-20251001",
				Tier:     model.TierCheap,
			},
			SessionID: providers.NewSessionID(),
		},
		PromptPath: promptFile(t, "do the thing\n"),
		LogPath:    filepath.Join(t.TempDir(), "logs", taskID+".log"),
		Cwd:        t.TempDir(),
		Timeout:    10 * time.Second,
	}
}

// successEnvelope is the shape claude 2.1.269 really emits, trimmed to the
// fields the parser reads. The full captures live in
// internal/providers/testdata; this only needs to parse.
func successEnvelope(cost float64, in, out int) string {
	return fmt.Sprintf(
		`{"type":"result","subtype":"success","is_error":false,`+
			`"total_cost_usd":%v,"usage":{"input_tokens":%d,"output_tokens":%d}}`,
		cost, in, out)
}

// ---- TimeoutFor ----------------------------------------------------------

func TestTimeoutFor(t *testing.T) {
	tests := []struct {
		name       string
		estimate   float64
		multiplier float64
		want       time.Duration
	}{
		{name: "one hour at the default multiplier", estimate: 1, multiplier: 1.5, want: 90 * time.Minute},
		{name: "six hours", estimate: 6, multiplier: 1.5, want: 9 * time.Hour},
		{name: "half an hour", estimate: 0.5, multiplier: 1.5, want: 45 * time.Minute},
		{name: "a multiplier of 1", estimate: 2, multiplier: 1, want: 2 * time.Hour},
		{name: "no estimate falls back", estimate: 0, multiplier: 1.5, want: FallbackTimeout},
		{name: "a negative estimate falls back", estimate: -3, multiplier: 1.5, want: FallbackTimeout},
		{
			// Python does the same: `cfg.get(...) or 1.5` maps a falsy 0 to
			// the default before the multiplication.
			name:     "a zero multiplier means the default, as in Python",
			estimate: 8, multiplier: 0, want: 12 * time.Hour,
		},
		{
			// Here Go and Python differ, deliberately. Python keeps a
			// negative multiplier (`-2 or 1.5` is -2), so `seconds` goes
			// negative and an 8-hour task lands on the 60s floor. Treating
			// nonsense config as "no multiplier given" keeps the task's real
			// timeout instead. Only reachable by writing a negative number
			// into config.yaml on purpose.
			name:     "a negative multiplier means the default, where Python would give 60s",
			estimate: 8, multiplier: -2, want: 12 * time.Hour,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TimeoutFor(tt.estimate, tt.multiplier); got != tt.want {
				t.Errorf("TimeoutFor(%v, %v) = %v, want %v", tt.estimate, tt.multiplier, got, tt.want)
			}
		})
	}
}

func TestLogPathFor(t *testing.T) {
	got := LogPathFor(filepath.Join("proj", "state"), "B-020")
	want := filepath.Join("proj", "state", "logs", "B-020.log")
	if got != want {
		t.Errorf("LogPathFor = %q, want %q", got, want)
	}
}

// ---- The happy path through a real process -------------------------------

func TestRunSuccess(t *testing.T) {
	d := claudeDispatch(t, "B-020")
	t.Setenv(FakeProviderEnv, fakeDir(t, model.BackendClaude, "B-020",
		successEnvelope(0.0475971, 19, 603)+"\n", 0, ""))

	out, err := Run(context.Background(), d)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !out.Result.Success {
		t.Fatalf("want success, got %q", out.Result.ErrorMessage)
	}
	if out.Failure != "" {
		t.Errorf("Failure = %q, want empty on success", out.Failure)
	}
	if out.TimedOut {
		t.Error("TimedOut on a run that exited on its own")
	}
	if out.Result.CostUSD != 0.0475971 {
		t.Errorf("CostUSD = %v", out.Result.CostUSD)
	}
	if out.Result.TokensIn != 19 || out.Result.TokensOut != 603 {
		t.Errorf("tokens = (%d,%d), want (19,603)", out.Result.TokensIn, out.Result.TokensOut)
	}
	if out.PID <= 0 {
		t.Errorf("PID = %d", out.PID)
	}
	if out.Duration <= 0 {
		t.Errorf("Duration = %v, want positive", out.Duration)
	}

	// The log file must hold what the child wrote — the id-spoof check reads
	// it back, so an empty or missing log is a silent hole in that defence.
	logged := ReadLog(d.LogPath)
	if !strings.Contains(logged, `"subtype":"success"`) {
		t.Errorf("log does not hold the child's output: %q", logged)
	}
}

// TestRunPipesThePromptToStdin proves the prompt actually reaches the child.
// The fake reads stdin and the provider is on PromptStdin, so a broken wiring
// here would show up as a CLI that silently gets no instructions.
func TestRunPipesThePromptToStdin(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, string(model.BackendClaude))
	if err := os.MkdirAll(base, 0o750); err != nil {
		t.Fatal(err)
	}
	// This fake echoes its stdin instead of a canned file.
	if err := os.WriteFile(filepath.Join(base, "B-021.out"), []byte("unused"), 0o600); err != nil {
		t.Fatal(err)
	}

	d := claudeDispatch(t, "B-021")
	d.PromptPath = promptFile(t, "TASK_ID=B-021\nhello from the prompt\n")
	d.Provider = echoStdinProvider{}
	t.Setenv(FakeProviderEnv, "") // the echo provider is the fake here

	out, err := Run(context.Background(), d)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !out.Result.Success {
		t.Fatalf("the echo child should have exited 0: %q", out.Result.ErrorMessage)
	}
	logged := ReadLog(d.LogPath)
	if !strings.Contains(logged, "TASK_ID=B-021") || !strings.Contains(logged, "hello from the prompt") {
		t.Errorf("the prompt did not reach the child's stdin; log = %q", logged)
	}
}

// echoStdinProvider runs `cat`, so whatever is piped in lands in the log.
type echoStdinProvider struct{}

func (echoStdinProvider) Name() model.Backend                      { return model.BackendClaude }
func (echoStdinProvider) PromptDelivery() providers.PromptDelivery { return providers.PromptStdin }
func (echoStdinProvider) Argv(providers.Request) []string          { return []string{"cat"} }
func (echoStdinProvider) Parse(exitCode int, output []byte) providers.Result {
	return providers.Result{ExitCode: exitCode, Success: exitCode == 0, Stdout: string(output)}
}

// ---- Failure classes -----------------------------------------------------

func TestRunClassifiesFailures(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		exitCode int
		want     providers.Failure
	}{
		{
			name:     "a rejected model is version drift",
			output:   `{"is_error":true,"subtype":"success","api_error_status":404,"result":"It may not exist or you may not have access to it."}`,
			exitCode: 1,
			want:     providers.FailureVersionDrift,
		},
		{
			name:     "a rate limit",
			output:   `{"is_error":true,"subtype":"error","terminal_reason":"429 rate limit exceeded"}`,
			exitCode: 1,
			want:     providers.FailureRateLimit,
		},
		{
			name:     "an auth failure",
			output:   `{"is_error":true,"subtype":"error","api_error_status":{"code":"authentication_error","message":"Invalid API key"}}`,
			exitCode: 1,
			want:     providers.FailurePermission,
		},
		{
			name:     "a transient 503",
			output:   `{"is_error":true,"subtype":"error","terminal_reason":"503 service unavailable"}`,
			exitCode: 1,
			want:     providers.FailureTransient,
		},
		{
			name:     "unreadable output is a parser failure",
			output:   "Traceback (most recent call last):\n  RuntimeError: boom\n",
			exitCode: 1,
			want:     providers.FailureParser,
		},
		{
			name:     "a budget stop",
			output:   `{"is_error":true,"subtype":"error","terminal_reason":"max budget reached"}`,
			exitCode: 1,
			want:     providers.FailureBudget,
		},
		{
			name:     "anything else is the conservative default",
			output:   `{"is_error":true,"subtype":"error","terminal_reason":"something odd"}`,
			exitCode: 1,
			want:     providers.FailureOther,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := claudeDispatch(t, "B-030")
			t.Setenv(FakeProviderEnv, fakeDir(t, model.BackendClaude, "B-030", tt.output+"\n", tt.exitCode, ""))

			out, err := Run(context.Background(), d)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if out.Result.Success {
				t.Fatal("want failure")
			}
			if out.Failure != tt.want {
				t.Errorf("Failure = %q, want %q (error=%q)", out.Failure, tt.want, out.Result.ErrorMessage)
			}
		})
	}
}

// TestRunRecordsExitCodeOfASignalledChild pins the sign convention: Python's
// subprocess reports -N for a child killed by signal N, and that number ends
// up in an event row both binaries write.
func TestRunRecordsExitCodeOfASignalledChild(t *testing.T) {
	d := claudeDispatch(t, "B-031")
	d.Provider = suicideProvider{}
	d.Timeout = 10 * time.Second

	out, err := Run(context.Background(), d)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Result.ExitCode != -int(syscall.SIGKILL) {
		t.Errorf("ExitCode = %d, want %d", out.Result.ExitCode, -int(syscall.SIGKILL))
	}
}

// suicideProvider runs a child that SIGKILLs itself.
type suicideProvider struct{}

func (suicideProvider) Name() model.Backend                      { return model.BackendClaude }
func (suicideProvider) PromptDelivery() providers.PromptDelivery { return providers.PromptStdin }
func (suicideProvider) Argv(providers.Request) []string {
	return []string{"sh", "-c", `kill -9 $$`}
}
func (suicideProvider) Parse(exitCode int, output []byte) providers.Result {
	return providers.Result{ExitCode: exitCode, Stdout: string(output), ErrorMessage: "killed"}
}

// ---- The timeout path, against real processes ----------------------------

// TestRunTimeoutKillsTheWholeProcessGroup is the test this package exists for.
//
// A coding CLI forks: a shell wrapper, a sub-agent, a language server. Killing
// only the direct child leaves those reparented to init, still holding the
// workspace and still spending. The child here forks a grandchild that
// outlives its parent on purpose; both must be gone when Run returns.
func TestRunTimeoutKillsTheWholeProcessGroup(t *testing.T) {
	markers := t.TempDir()
	grandchildPIDFile := filepath.Join(markers, "grandchild.pid")

	d := claudeDispatch(t, "B-040")
	d.Provider = &forkingProvider{pidFile: grandchildPIDFile}
	d.Timeout = 300 * time.Millisecond

	started := time.Now()
	out, err := Run(context.Background(), d)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !out.TimedOut {
		t.Fatal("want TimedOut")
	}
	if out.Result.Success {
		t.Error("a timed-out dispatch is never a success")
	}
	if out.Failure != providers.FailureTimeout {
		t.Errorf("Failure = %q, want %q", out.Failure, providers.FailureTimeout)
	}
	if !strings.HasPrefix(out.Result.ErrorMessage, TimeoutReason) {
		t.Errorf("ErrorMessage = %q, want it to start with %q", out.Result.ErrorMessage, TimeoutReason)
	}
	// The child ignores SIGTERM, so this must have gone through the full
	// escalation — but nothing like the 10s grace plus the sleep.
	if elapsed > TermToKillGrace+5*time.Second {
		t.Errorf("took %v; the SIGKILL escalation did not fire", elapsed)
	}

	// The direct child is reaped by Wait. The grandchild is the point.
	pid := readPID(t, grandchildPIDFile)
	if pid <= 0 {
		t.Fatalf("the fake never recorded a grandchild pid (read %d)", pid)
	}
	waitForExit(t, pid, 10*time.Second)
}

// forkingProvider runs a child that forks a grandchild, records its pid, and
// then ignores SIGTERM — so only a process-group SIGKILL ends the tree.
type forkingProvider struct{ pidFile string }

func (*forkingProvider) Name() model.Backend                      { return model.BackendClaude }
func (*forkingProvider) PromptDelivery() providers.PromptDelivery { return providers.PromptStdin }
func (f *forkingProvider) Argv(providers.Request) []string {
	// `trap '' TERM` makes the parent ignore SIGTERM outright, which is what
	// forces the escalation rather than merely making it likely.
	script := `trap '' TERM; sleep 120 & echo $! > "$1"; sleep 120`
	return []string{"sh", "-c", script, "sh", f.pidFile}
}
func (*forkingProvider) Parse(exitCode int, output []byte) providers.Result {
	return providers.Result{ExitCode: exitCode, Stdout: string(output)}
}

// TestRunTimeoutOnAWellBehavedChild covers the other branch: a child that
// honours SIGTERM exits during the grace window and never needs SIGKILL.
func TestRunTimeoutOnAWellBehavedChild(t *testing.T) {
	d := claudeDispatch(t, "B-041")
	t.Setenv(FakeProviderEnv, fakeDir(t, model.BackendClaude, "B-041", "never printed", 0, "120"))
	d.Timeout = 200 * time.Millisecond

	started := time.Now()
	out, err := Run(context.Background(), d)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !out.TimedOut {
		t.Fatal("want TimedOut")
	}
	// It died to SIGTERM, so this must not have waited out the grace period.
	if elapsed >= TermToKillGrace {
		t.Errorf("took %v; a SIGTERM-honouring child should not reach the SIGKILL grace", elapsed)
	}
	if out.Failure != providers.FailureTimeout {
		t.Errorf("Failure = %q, want %q", out.Failure, providers.FailureTimeout)
	}
}

// TestRunTimeoutBeatsWhateverTheCLIPrinted: a killed CLI's output can say
// anything, including a rate-limit line from earlier in the run. The
// orchestrator owns this verdict.
func TestRunTimeoutBeatsCLIOutput(t *testing.T) {
	d := claudeDispatch(t, "B-042")
	d.Provider = &noisyThenHangingProvider{}
	d.Timeout = 300 * time.Millisecond

	out, err := Run(context.Background(), d)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Failure != providers.FailureTimeout {
		t.Errorf("Failure = %q, want %q — the CLI's 429 must not win", out.Failure, providers.FailureTimeout)
	}
	if !strings.Contains(ReadLog(d.LogPath), "429") {
		t.Error("the fixture should have printed its misleading line before hanging")
	}
}

type noisyThenHangingProvider struct{}

func (*noisyThenHangingProvider) Name() model.Backend { return model.BackendClaude }
func (*noisyThenHangingProvider) PromptDelivery() providers.PromptDelivery {
	return providers.PromptStdin
}
func (*noisyThenHangingProvider) Argv(providers.Request) []string {
	return []string{"sh", "-c", `echo "429 rate limit exceeded"; sleep 120`}
}
func (*noisyThenHangingProvider) Parse(exitCode int, output []byte) providers.Result {
	return providers.Result{ExitCode: exitCode, Stdout: string(output), ErrorMessage: "429 rate limit"}
}

// TestRunCancelledContextKillsTheGroup: Ctrl-C on the dispatch loop must not
// leave a tree of agents behind either.
//
// Cancellation is triggered on a marker the child writes before it hangs,
// not on a delay: waiting a fixed duration for a process to reach a state is
// the flake this suite is meant to avoid (checklist rule 22).
func TestRunCancelledContextKillsTheGroup(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "started")

	d := claudeDispatch(t, "B-043")
	d.Provider = &markerThenHangProvider{marker: marker}
	d.Timeout = 30 * time.Second // long: cancellation, not the timeout, ends this

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		waitForFile(t, marker, 10*time.Second)
		cancel()
	}()

	started := time.Now()
	out, err := Run(ctx, d)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if elapsed >= 30*time.Second {
		t.Fatalf("took %v; cancellation did not reach the child", elapsed)
	}
	// Cancellation is not a timeout: Wait was not the one that gave up, so
	// the outcome carries the child's own death rather than a timeout reason.
	if out.TimedOut {
		t.Error("a cancelled dispatch should not be reported as a timeout")
	}
}

// markerThenHangProvider writes a marker file, then sleeps past any timeout
// the test would otherwise rely on.
type markerThenHangProvider struct{ marker string }

func (*markerThenHangProvider) Name() model.Backend { return model.BackendClaude }
func (*markerThenHangProvider) PromptDelivery() providers.PromptDelivery {
	return providers.PromptStdin
}
func (m *markerThenHangProvider) Argv(providers.Request) []string {
	return []string{"sh", "-c", `echo ready > "$1"; sleep 120`, "sh", m.marker}
}
func (*markerThenHangProvider) Parse(exitCode int, output []byte) providers.Result {
	return providers.Result{ExitCode: exitCode, Stdout: string(output)}
}

// ---- Output capture ------------------------------------------------------

// TestRunCapturesStderrIntoTheSameLog pins the merge. Python spawns with
// stderr=STDOUT, and the claude fixtures were captured through that merge —
// the unrecognised-model capture is only a non-JSON blob because its stderr
// line lands in the same stream.
func TestRunCapturesStderrIntoTheSameLog(t *testing.T) {
	d := claudeDispatch(t, "B-050")
	d.Provider = &twoStreamProvider{}

	out, err := Run(context.Background(), d)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	logged := ReadLog(d.LogPath)
	if !strings.Contains(logged, "to stdout") {
		t.Error("stdout missing from the log")
	}
	if !strings.Contains(logged, "to stderr") {
		t.Error("stderr missing from the log — the merge is broken")
	}
	// Result.Stderr stays empty, as in Python: there is one stream, and
	// Classify reads it through the stdout tail.
	if out.Result.Stderr != "" {
		t.Errorf("Result.Stderr = %q, want empty", out.Result.Stderr)
	}
}

type twoStreamProvider struct{}

func (*twoStreamProvider) Name() model.Backend                      { return model.BackendClaude }
func (*twoStreamProvider) PromptDelivery() providers.PromptDelivery { return providers.PromptStdin }
func (*twoStreamProvider) Argv(providers.Request) []string {
	return []string{"sh", "-c", `echo "to stdout"; echo "to stderr" >&2`}
}
func (*twoStreamProvider) Parse(exitCode int, output []byte) providers.Result {
	return providers.Result{ExitCode: exitCode, Success: exitCode == 0, Stdout: string(output)}
}

// TestRunCapturesOutputLargerThanAPipeBuffer is the regression guard for the
// drain goroutine. A child that writes more than a pipe holds (64 KiB on
// Linux) blocks until someone reads; a supervisor that only reads after the
// child exits would deadlock here, and every long JSONL stream would hang.
func TestRunCapturesOutputLargerThanAPipeBuffer(t *testing.T) {
	const lines = 20000 // ~1 MB, comfortably past any pipe buffer

	d := claudeDispatch(t, "B-051")
	d.Provider = &chattyProvider{lines: lines}
	d.Timeout = 60 * time.Second

	out, err := Run(context.Background(), d)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.TimedOut {
		t.Fatal("timed out — the pipe was not being drained while the child ran")
	}
	got := strings.Count(ReadLog(d.LogPath), "\n")
	if got != lines {
		t.Errorf("captured %d lines, want %d — output was lost", got, lines)
	}
}

type chattyProvider struct{ lines int }

func (*chattyProvider) Name() model.Backend                      { return model.BackendClaude }
func (*chattyProvider) PromptDelivery() providers.PromptDelivery { return providers.PromptStdin }
func (c *chattyProvider) Argv(providers.Request) []string {
	return []string{"sh", "-c", `i=0; while [ "$i" -lt "$1" ]; do echo "line $i padding padding padding padding padding"; i=$((i+1)); done`, "sh", strconv.Itoa(c.lines)}
}
func (*chattyProvider) Parse(exitCode int, output []byte) providers.Result {
	return providers.Result{ExitCode: exitCode, Success: exitCode == 0, Stdout: string(output)}
}

// TestRunAppendsToAnExistingLog: a retry writes after the previous attempt
// rather than erasing it, matching Python's "ab" open mode.
func TestRunAppendsToAnExistingLog(t *testing.T) {
	d := claudeDispatch(t, "B-052")
	t.Setenv(FakeProviderEnv, fakeDir(t, model.BackendClaude, "B-052", successEnvelope(0.01, 1, 1)+"\n", 0, ""))

	if _, err := Run(context.Background(), d); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	first := ReadLog(d.LogPath)
	if _, err := Run(context.Background(), d); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	second := ReadLog(d.LogPath)

	if len(second) <= len(first) {
		t.Errorf("second attempt did not append: %d bytes then %d", len(first), len(second))
	}
	if !strings.HasPrefix(second, first) {
		t.Error("the first attempt's output was overwritten")
	}
}

// ---- Errors --------------------------------------------------------------

func TestRunErrors(t *testing.T) {
	t.Run("no provider", func(t *testing.T) {
		if _, err := Run(context.Background(), Dispatch{}); err == nil {
			t.Fatal("want an error")
		}
	})

	t.Run("a missing prompt file", func(t *testing.T) {
		d := claudeDispatch(t, "B-060")
		d.PromptPath = filepath.Join(t.TempDir(), "does-not-exist.txt")
		t.Setenv(FakeProviderEnv, fakeDir(t, model.BackendClaude, "B-060", "x", 0, ""))
		_, err := Run(context.Background(), d)
		if err == nil {
			t.Fatal("want an error")
		}
		if !strings.Contains(err.Error(), "prompt") {
			t.Errorf("err = %q, want it to name the prompt", err)
		}
	})

	t.Run("a binary that does not exist", func(t *testing.T) {
		d := claudeDispatch(t, "B-061")
		d.Provider = missingBinaryProvider{}
		_, err := Run(context.Background(), d)
		if err == nil {
			t.Fatal("want an error")
		}
		if !strings.Contains(err.Error(), "orch-no-such-binary") {
			t.Errorf("err = %q, want it to name the binary", err)
		}
	})

	t.Run("an empty argv", func(t *testing.T) {
		d := claudeDispatch(t, "B-062")
		d.Provider = emptyArgvProvider{}
		_, err := Run(context.Background(), d)
		if err == nil {
			t.Fatal("want an error")
		}
		if !strings.Contains(err.Error(), "empty argv") {
			t.Errorf("err = %q", err)
		}
	})
}

type missingBinaryProvider struct{}

func (missingBinaryProvider) Name() model.Backend                      { return model.BackendClaude }
func (missingBinaryProvider) PromptDelivery() providers.PromptDelivery { return providers.PromptStdin }
func (missingBinaryProvider) Argv(providers.Request) []string          { return []string{"orch-no-such-binary"} }
func (missingBinaryProvider) Parse(int, []byte) providers.Result       { return providers.Result{} }

type emptyArgvProvider struct{}

func (emptyArgvProvider) Name() model.Backend                      { return model.BackendClaude }
func (emptyArgvProvider) PromptDelivery() providers.PromptDelivery { return providers.PromptStdin }
func (emptyArgvProvider) Argv(providers.Request) []string          { return nil }
func (emptyArgvProvider) Parse(int, []byte) providers.Result       { return providers.Result{} }

// ---- ShouldRetryWithFallback --------------------------------------------

func TestShouldRetryWithFallback(t *testing.T) {
	fallback := "claude-sonnet-4-6"

	tests := []struct {
		name    string
		failure providers.Failure
		route   model.RouteEntry
		want    bool
	}{
		{
			name:    "drift with a fallback configured",
			failure: providers.FailureVersionDrift,
			route:   model.RouteEntry{FallbackCLIModel: &fallback},
			want:    true,
		},
		{
			name:    "drift with no fallback to swap in",
			failure: providers.FailureVersionDrift,
			route:   model.RouteEntry{},
		},
		{
			name:    "a rate limit is not drift",
			failure: providers.FailureRateLimit,
			route:   model.RouteEntry{FallbackCLIModel: &fallback},
		},
		{
			name:  "a success",
			route: model.RouteEntry{FallbackCLIModel: &fallback},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := Outcome{Failure: tt.failure}
			if got := o.ShouldRetryWithFallback(tt.route); got != tt.want {
				t.Errorf("ShouldRetryWithFallback = %v, want %v", got, tt.want)
			}
		})
	}
}

// ---- Helpers -------------------------------------------------------------

func readPID(t *testing.T, path string) int {
	t.Helper()
	waitForFile(t, path, 5*time.Second)
	b, err := os.ReadFile(path) // #nosec G304 -- a marker file this test just wrote
	if err != nil {
		t.Fatalf("read pid file: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatalf("pid file holds %q: %v", b, err)
	}
	return pid
}

// waitForFile polls for a file to appear and be non-empty. Polling rather
// than sleeping keeps checklist rule 22 satisfied: the test never waits a
// fixed duration, it waits for the condition and fails on the deadline.
func waitForFile(t *testing.T, path string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if fi, err := os.Stat(path); err == nil && fi.Size() > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s did not appear within %v", path, within)
}

// waitForExit polls until the pid is gone, which is what "the whole group
// died" actually means.
//
// Signal 0 only reports liveness for processes we could signal; these are our
// own descendants, so it is the right check. A pid can in principle be reused,
// but not inside a test that has just killed the tree.
func waitForExit(t *testing.T, pid int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return // gone
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Leave nothing running even when the assertion fails.
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("pid %d survived the dispatch — the process group was not killed", pid)
}

// TestMain fails loudly if a test leaks a process, rather than letting the
// next run inherit it.
func TestMain(m *testing.M) {
	code := m.Run()
	if leaked := leakedChildren(); len(leaked) > 0 {
		fmt.Fprintf(os.Stderr, "engine tests leaked child processes: %v\n", leaked)
		for _, pid := range leaked {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

// leakedChildren returns any direct children still alive at exit.
func leakedChildren() []int {
	// #nosec G204 -- the only argument is this test process's own pid.
	out, err := exec.Command("pgrep", "-P", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		return nil // pgrep absent, or no children — either way, nothing to report
	}
	var pids []int
	for _, line := range strings.Fields(string(out)) {
		if pid, err := strconv.Atoi(line); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}
