package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/providers"
)

// FakeProviderEnv names the directory of canned CLI output that replaces every
// real provider. Set it and no coding CLI is ever executed.
//
// This exists so the dispatch loop can be exercised end to end — spawn,
// timeout, parse, classify, record — without an API key, a network, or a
// dollar of spend. G2.6 formalises it; the hook lands here because the
// alternative is engine tests that can only run on a machine with every CLI
// installed and authenticated, which is no machine.
const FakeProviderEnv = "ORCH_FAKE_PROVIDER"

// A fake directory holds one file per canned response:
//
//	<dir>/<backend>/<task id>.out    what the CLI would have printed
//	<dir>/<backend>/<task id>.exit   its exit code, as decimal text (default 0)
//	<dir>/<backend>/<task id>.sleep  seconds to sleep first (default 0)
//
// Falling back to `_default.*` when there is no file for the task id, so one
// file can answer a whole run. The `.sleep` file is what makes the timeout
// path testable: a fake that sleeps longer than its dispatch's timeout
// exercises the real SIGTERM/SIGKILL escalation against a real process.
const (
	fakeOutExt   = ".out"
	fakeExitExt  = ".exit"
	fakeSleepExt = ".sleep"
	fakeDefault  = "_default"
)

// FakeProviderDir returns the configured fake directory and whether the hook
// is active.
func FakeProviderDir() (string, bool) {
	dir := strings.TrimSpace(os.Getenv(FakeProviderEnv))
	return dir, dir != ""
}

// fakeArgv builds the argv that replays a canned response.
//
// It is a shell script rather than an in-process shortcut on purpose: the
// point of the fake is to exercise the real spawn path — a real fork, a real
// process group, a real pipe, a real signal — with only the CLI's output
// faked. A fake that returned bytes directly would test nothing that
// matters here.
//
// `sh` is invoked with a fixed script and the paths as positional arguments,
// so no value is ever interpolated into shell text.
func fakeArgv(dir string, backend model.Backend, taskID string) ([]string, error) {
	outPath, err := fakeFile(dir, backend, taskID, fakeOutExt)
	if err != nil {
		return nil, err
	}
	sleepFor := fakeScalar(dir, backend, taskID, fakeSleepExt, "0")
	exitCode := fakeScalar(dir, backend, taskID, fakeExitExt, "0")

	// "$1" and "$2" are the sleep and the exit code; "$3" is the output file.
	// Reading stdin to the end keeps the fake honest about the prompt: a
	// provider on PromptStdin has its prompt piped in, and a child that never
	// reads it would leave the writer blocked on a full pipe for a large one.
	const script = `sleep "$1"; cat >/dev/null; cat "$3"; exit "$2"`
	return []string{"sh", "-c", script, "sh", sleepFor, exitCode, outPath}, nil
}

// fakeFile resolves <dir>/<backend>/<taskID><ext>, falling back to the
// _default file. A missing response is an error rather than empty output:
// silently dispatching nothing is how a fake run looks green while testing
// nothing.
func fakeFile(dir string, backend model.Backend, taskID, ext string) (string, error) {
	specific := filepath.Join(dir, string(backend), taskID+ext)
	if _, err := os.Stat(specific); err == nil {
		return specific, nil
	}
	fallback := filepath.Join(dir, string(backend), fakeDefault+ext)
	if _, err := os.Stat(fallback); err == nil {
		return fallback, nil
	}
	return "", fmt.Errorf(
		"%s: no canned output for %s/%s — expected %s or %s",
		FakeProviderEnv, backend, taskID, specific, fallback)
}

// fakeScalar reads a one-value control file, returning def when it is absent
// or unreadable. Whitespace is trimmed so a file written with a trailing
// newline works.
func fakeScalar(dir string, backend model.Backend, taskID, ext, def string) string {
	path, err := fakeFile(dir, backend, taskID, ext)
	if err != nil {
		return def
	}
	b, err := os.ReadFile(path) // #nosec G304 -- a control file inside the operator's own fake dir
	if err != nil {
		return def
	}
	v := strings.TrimSpace(string(b))
	if v == "" {
		return def
	}
	// Guard the value rather than trusting the file: it becomes an argument
	// to `sleep` and `exit`, and a non-numeric one would make the script
	// behave in ways the test did not ask for.
	for _, c := range v {
		if (c < '0' || c > '9') && c != '.' {
			return def
		}
	}
	return v
}

// fakeProvider wraps a real provider, keeping its Name, PromptDelivery and
// Parse — the parser is the thing under test — and replacing only the argv.
type fakeProvider struct {
	inner providers.Provider
	dir   string
	// argvErr is reported by Spawn rather than by Argv, which cannot return
	// an error. An empty argv is the signal.
	argvErr error
}

func (f *fakeProvider) Name() model.Backend                      { return f.inner.Name() }
func (f *fakeProvider) PromptDelivery() providers.PromptDelivery { return f.inner.PromptDelivery() }
func (f *fakeProvider) Parse(exitCode int, output []byte) providers.Result {
	return f.inner.Parse(exitCode, output)
}

func (f *fakeProvider) Argv(req providers.Request) []string {
	argv, err := fakeArgv(f.dir, f.inner.Name(), req.TaskID)
	if err != nil {
		f.argvErr = err
		return nil
	}
	return argv
}

// withFake returns p unchanged unless the fake hook is set, in which case it
// returns a wrapper that replays canned output instead of running the CLI.
func withFake(p providers.Provider) providers.Provider {
	dir, ok := FakeProviderDir()
	if !ok {
		return p
	}
	return &fakeProvider{inner: p, dir: dir}
}

// fakeArgvError recovers the error a fakeProvider stashed when its response
// file was missing, so Spawn can report the real reason rather than "empty
// argv".
func fakeArgvError(p providers.Provider) error {
	if f, ok := p.(*fakeProvider); ok {
		return f.argvErr
	}
	return nil
}
