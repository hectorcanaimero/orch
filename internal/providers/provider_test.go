package providers

import (
	"errors"
	"strings"
	"testing"

	"github.com/hectorcanaimero/orch/internal/model"
)

func assertArgv(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("argv length %d, want %d\n got: %q\nwant: %q", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("argv[%d] = %q, want %q\n got: %q\nwant: %q", i, got[i], want[i], got, want)
		}
	}
}

// assertNoShellMetacharacters backs the "NO shell metacharacters" contract
// Python states on build_cmd. The engine execs directly, so this is not a
// vulnerability today; it is the property that keeps it from becoming one.
func assertNoShellMetacharacters(t *testing.T, argv []string) {
	t.Helper()
	const meta = "|&;<>()$`\\\"'\n\t*?[#~"
	for i, a := range argv {
		if strings.ContainsAny(a, meta) {
			t.Errorf("argv[%d] = %q contains a shell metacharacter", i, a)
		}
	}
}

func TestGet(t *testing.T) {
	tests := []struct {
		name    string
		backend model.Backend
		want    model.Backend
		wantErr error
		// errContains is checked when wantErr is nil but an error is expected.
		errContains string
	}{
		{name: "claude", backend: model.BackendClaude, want: model.BackendClaude},
		{name: "codex", backend: model.BackendCodex, want: model.BackendCodex},
		{name: "opencode is known but not ported", backend: model.BackendOpencode, wantErr: ErrNotPorted},
		{name: "gemini is known but not ported", backend: model.BackendGemini, wantErr: ErrNotPorted},
		{name: "agy is known but not ported", backend: model.BackendAgy, wantErr: ErrNotPorted},
		{name: "an unknown name", backend: model.Backend("bogus"), errContains: "unknown backend"},
		{name: "the empty name", backend: model.Backend(""), errContains: "unknown backend"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := Get(tt.backend)

			switch {
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				// The name must be in the message; the engine surfaces it.
				if !strings.Contains(err.Error(), string(tt.backend)) {
					t.Errorf("err %q does not name the backend", err)
				}
			case tt.errContains != "":
				if err == nil {
					t.Fatal("want an error, got none")
				}
				if errors.Is(err, ErrNotPorted) {
					t.Error("an unknown backend must not read as ErrNotPorted")
				}
				if !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("err = %q, want it to contain %q", err, tt.errContains)
				}
			default:
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if p.Name() != tt.want {
					t.Errorf("Name = %q, want %q", p.Name(), tt.want)
				}
			}

			if err != nil && p != nil {
				t.Error("Get must return a nil Provider alongside an error")
			}
		})
	}
}

// TestPromptDelivery pins how each ported CLI wants its prompt. Both take it
// on stdin; the engine reads this rather than assuming, because gemini and agy
// will answer PromptArg when they land.
func TestPromptDelivery(t *testing.T) {
	for _, p := range []Provider{ClaudeProvider{}, CodexProvider{}} {
		t.Run(string(p.Name()), func(t *testing.T) {
			if got := p.PromptDelivery(); got != PromptStdin {
				t.Errorf("PromptDelivery = %v, want PromptStdin", got)
			}
		})
	}
}

func TestNewSessionID(t *testing.T) {
	first := NewSessionID()
	// claude wants a UUID; the CLI rejects anything else outright.
	if len(first) != 36 {
		t.Errorf("session id %q has length %d, want 36", first, len(first))
	}
	if strings.Count(first, "-") != 4 {
		t.Errorf("session id %q is not UUID-shaped", first)
	}
	if second := NewSessionID(); second == first {
		t.Errorf("two calls returned the same id %q", first)
	}
}

// TestParseNeverPanics feeds every provider the kinds of output a killed or
// confused CLI produces. Parse is documented total: the engine calls it on
// whatever bytes it captured, with no recover() to fall back on.
func TestParseNeverPanics(t *testing.T) {
	outputs := map[string]string{
		"empty":                   "",
		"whitespace":              "  \n\t\r\n ",
		"not json":                "Traceback (most recent call last):\n  boom\n",
		"a bare array":            `[1,2,3]`,
		"a bare scalar":           `42`,
		"a null":                  `null`,
		"an empty object":         `{}`,
		"half an object":          `{"type":"turn.comple`,
		"nul bytes":               "\x00\x00\x00",
		"invalid utf-8":           "\xff\xfe{\"type\":\"error\"}",
		"a very long single line": strings.Repeat(`{"type":"noise"}`, 1),
		"crlf line endings":       "{\"type\":\"turn.started\"}\r\n{\"type\":\"turn.completed\",\"usage\":{}}\r\n",
	}

	for _, p := range []Provider{ClaudeProvider{}, CodexProvider{}} {
		for name, out := range outputs {
			t.Run(string(p.Name())+"/"+name, func(t *testing.T) {
				for _, exit := range []int{0, 1, -9} {
					res := p.Parse(exit, []byte(out))
					if res.ExitCode != exit {
						t.Errorf("ExitCode = %d, want %d", res.ExitCode, exit)
					}
					if !res.Success && res.ErrorMessage == "" {
						t.Error("a failed Result must carry an ErrorMessage for Classify to read")
					}
					// Classify must cope with whatever Parse produced.
					if got := Classify(res); got == "" {
						t.Error("Classify returned the empty Failure")
					}
				}
			})
		}
	}
}

// TestParseStdoutIsVerbatim: the engine hands Result.Stdout to the id-spoof
// check, which greps it for the task-finish.sh marker. A parser that
// normalised or truncated it would break that check silently.
func TestParseStdoutIsVerbatim(t *testing.T) {
	out := "noise\n{\"type\":\"turn.completed\",\"usage\":{}}\n"
	for _, p := range []Provider{ClaudeProvider{}, CodexProvider{}} {
		t.Run(string(p.Name()), func(t *testing.T) {
			if got := p.Parse(0, []byte(out)).Stdout; got != out {
				t.Errorf("Stdout = %q, want %q", got, out)
			}
		})
	}
}

// TestEveryPortedBackendIsReachable keeps Get and the Provider set in step: a
// new adapter that nobody wired into Get is invisible to the engine.
func TestEveryPortedBackendIsReachable(t *testing.T) {
	ported := []model.Backend{model.BackendClaude, model.BackendCodex}
	for _, b := range ported {
		if notPorted[b] {
			t.Errorf("%q is in the notPorted set but has an adapter", b)
		}
		p, err := Get(b)
		if err != nil {
			t.Fatalf("Get(%q): %v", b, err)
		}
		if p.Name() != b {
			t.Errorf("Get(%q).Name() = %q", b, p.Name())
		}
	}
}
