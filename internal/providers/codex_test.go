package providers

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/hectorcanaimero/orch/internal/model"
)

func codexRoute(cliModel string) model.RouteEntry {
	return model.RouteEntry{
		Backend:   model.BackendCodex,
		CLIModel:  cliModel,
		Tier:      model.TierPremium,
		IsPremium: true,
	}
}

func TestCodexArgv(t *testing.T) {
	got := CodexProvider{}.Argv(Request{
		TaskID:     "B-035",
		Route:      codexRoute("gpt-5.6-codex"),
		OutputPath: "/tmp/state/logs/B-035.codex.json",
	})
	assertArgv(t, got, []string{
		"codex",
		"exec",
		"--skip-git-repo-check",
		"--json",
		"-o", "/tmp/state/logs/B-035.codex.json",
		"-C", ".",
		"--approve-for-me",
		"-m", "gpt-5.6-codex",
	})
}

// TestCodexArgvHasNoSandboxFlag pins the fix from the Python comment: codex
// >= 0.148 refuses `-s`/`--sandbox` alongside `--approve-for-me`, which
// already implies the workspace-write sandbox.
func TestCodexArgvHasNoSandboxFlag(t *testing.T) {
	argv := CodexProvider{}.Argv(Request{Route: codexRoute("gpt-5.6-codex")})
	for _, a := range argv {
		if a == "-s" || a == "--sandbox" {
			t.Fatalf("argv must not carry %q alongside --approve-for-me: %v", a, argv)
		}
	}
	if !contains(argv, "--approve-for-me") {
		t.Errorf("argv lost --approve-for-me: %v", argv)
	}
}

func TestCodexArgvIsPure(t *testing.T) {
	req := Request{Route: codexRoute("gpt-5.6-codex"), OutputPath: "/x/y.json"}
	assertArgv(t, CodexProvider{}.Argv(req), CodexProvider{}.Argv(req))
}

func TestCodexArgvNoShellMetacharacters(t *testing.T) {
	assertNoShellMetacharacters(t, CodexProvider{}.Argv(Request{
		Route:      codexRoute("gpt-5.6-codex"),
		OutputPath: "/tmp/state/logs/B-035.codex.json",
	}))
}

func TestCodexDefaultOutputPath(t *testing.T) {
	got := CodexProvider{}.DefaultOutputPath(filepath.Join("state", "logs"), "B-035")
	want := filepath.Join("state", "logs", "B-035.codex.json")
	if got != want {
		t.Errorf("DefaultOutputPath = %q, want %q", got, want)
	}
}

// ---- Parse: codex fixtures ----------------------------------------------
//
// The success, auth-error, unknown-model and truncated fixtures are REAL
// captures from codex 0.154.0. rate-limit, nonfatal-warning and killed-empty
// are still synthetic because none can be provoked on demand. See
// testdata/README.md.

func TestCodexParseRealSuccess(t *testing.T) {
	out := readFixture(t, "codex", "0.154.0", "success.jsonl")
	res := CodexProvider{}.Parse(0, out)

	if !res.Success {
		t.Fatalf("want success, got failure: %q", res.ErrorMessage)
	}
	if res.ErrorMessage != "" {
		t.Errorf("ErrorMessage = %q, want empty", res.ErrorMessage)
	}
	// Usage comes off turn.completed, which is where 0.154.0 puts it — the
	// pre-0.148 step_finish shape is gone.
	if res.TokensIn != 18345 || res.TokensOut != 5 {
		t.Errorf("tokens = (%d,%d), want (18345,5)", res.TokensIn, res.TokensOut)
	}
	// codex's JSONL reports no USD figure at all.
	if res.CostUSD != 0 {
		t.Errorf("CostUSD = %v, want 0", res.CostUSD)
	}
}

func TestCodexExtractCostMatchesParse(t *testing.T) {
	out := readFixture(t, "codex", "0.154.0", "success.jsonl")
	cost, in, outTok := CodexProvider{}.ExtractCost(out)
	res := CodexProvider{}.Parse(0, out)
	if cost != res.CostUSD || in != res.TokensIn || outTok != res.TokensOut {
		t.Errorf("ExtractCost = (%v,%d,%d), Parse = (%v,%d,%d); they must agree",
			cost, in, outTok, res.CostUSD, res.TokensIn, res.TokensOut)
	}
}

func TestCodexParseRealAuthError(t *testing.T) {
	out := readFixture(t, "codex", "0.154.0", "auth-error.jsonl")
	res := CodexProvider{}.Parse(1, out)

	if res.Success {
		t.Fatal("want failure")
	}
	// A real 401 arrives as a reconnect notice, not as a tidy "auth expired":
	// the CLI retries the websocket five times and the first error event is
	// the first of those retries.
	if !strings.Contains(res.ErrorMessage, "401 Unauthorized") {
		t.Errorf("ErrorMessage = %q, want it to carry the 401", res.ErrorMessage)
	}
	// Classified from the status code and the word "Unauthorized", not from
	// the "auth expired" spelling #117 added for the synthetic fixture. Both
	// reach the same verdict, which is the point of having markers and codes.
	if got := Classify(res); got != FailurePermission {
		t.Errorf("Classify = %q, want %q", got, FailurePermission)
	}
}

func TestCodexSyntheticParseRateLimit(t *testing.T) {
	out := readFixture(t, "codex", "synthetic", "rate-limit.jsonl")
	res := CodexProvider{}.Parse(1, out)

	if res.Success {
		t.Fatal("want failure")
	}
	if res.ErrorMessage != "429 Too Many Requests: rate limit exceeded for gpt-5.6-codex" {
		t.Errorf("ErrorMessage = %q", res.ErrorMessage)
	}
	if got := Classify(res); got != FailureRateLimit {
		t.Errorf("Classify = %q, want %q", got, FailureRateLimit)
	}
}

// TestCodexParseRealUnknownModel is bug 31, pinned rather than fixed.
//
// The synthetic fixture this replaces made an unknown model look like
// FailureVersionDrift, which is the verdict the retry logic needs: a bad model
// name cannot succeed on retry. The real capture does not behave that way.
// codex emits a *metadata warning* before the 400 — "Model metadata for `X`
// not found. Defaulting to fallback metadata" — and since the parser reports
// the first error-ish event, that warning is what Parse surfaces and what
// Classify reads. It matches no drift marker, so a typo'd model name is
// classified FailureOther and retried until the attempt budget runs out.
//
// Not fixed here for the same reason as opencode's twin problem: reporting a
// different event, or adding a marker, changes retry behaviour for every
// backend and belongs in its own change with its own argument. See
// docs/brainstorm/go-migration-notes/sonnet-2.md.
func TestCodexParseRealUnknownModel(t *testing.T) {
	out := readFixture(t, "codex", "0.154.0", "unknown-model.jsonl")
	res := CodexProvider{}.Parse(1, out)

	if res.Success {
		t.Fatal("want failure")
	}
	if !strings.Contains(res.ErrorMessage, "Model metadata for") {
		t.Errorf("ErrorMessage = %q, want the metadata warning codex emits first",
			res.ErrorMessage)
	}
	if got := Classify(res); got != FailureOther {
		t.Errorf("Classify = %q, want %q — if this now says version_drift, "+
			"bug 31 was fixed and the note needs closing", got, FailureOther)
	}

	// The sentence that would have classified correctly is in the stream, one
	// event later. Asserting it here keeps the claim above checkable.
	if !strings.Contains(string(out), "model is not supported") {
		t.Error("the real 400 is no longer in the fixture; bug 31's evidence is gone")
	}
}

// TestCodexSyntheticNonFatalWarning pins the carve-out for warnings codex
// reports as errors. A shortened-skill-descriptions notice must not sink a run
// the model went on to complete.
func TestCodexSyntheticNonFatalWarning(t *testing.T) {
	out := readFixture(t, "codex", "synthetic", "nonfatal-warning.jsonl")
	res := CodexProvider{}.Parse(0, out)

	if !res.Success {
		t.Fatalf("a non-fatal warning must not fail the run: %q", res.ErrorMessage)
	}
	if res.TokensIn != 500 || res.TokensOut != 60 {
		t.Errorf("tokens = (%d,%d), want (500,60)", res.TokensIn, res.TokensOut)
	}
}

// TestCodexSyntheticParseTruncated proves a half-written final line costs only
// itself: the events before it still parse, and the run fails because the
// terminal turn.completed was in the line that got cut.
func TestCodexParseRealTruncated(t *testing.T) {
	out := readFixture(t, "codex", "0.154.0", "truncated.jsonl")
	res := CodexProvider{}.Parse(0, out)

	if res.Success {
		t.Fatal("a stream whose terminal event was truncated is not a success")
	}
	if res.ErrorMessage != "codex terminal event was not success (exit=0)" {
		t.Errorf("ErrorMessage = %q", res.ErrorMessage)
	}
	// The truncated turn.completed carried the usage, so nothing is recovered.
	if res.TokensIn != 0 || res.TokensOut != 0 {
		t.Errorf("tokens = (%d,%d), want (0,0)", res.TokensIn, res.TokensOut)
	}
}

func TestCodexSyntheticParseKilledRun(t *testing.T) {
	out := readFixture(t, "codex", "synthetic", "killed-empty.jsonl")
	if len(out) != 0 {
		t.Fatalf("fixture must stay empty, got %d bytes", len(out))
	}
	res := CodexProvider{}.Parse(-9, out)

	if res.Success {
		t.Fatal("an empty stream is not a success")
	}
	if res.ErrorMessage != "codex produced no JSONL events" {
		t.Errorf("ErrorMessage = %q", res.ErrorMessage)
	}
	if got := Classify(res); got != FailureParser {
		t.Errorf("Classify = %q, want %q", got, FailureParser)
	}
}

// ---- Parse: the terminal-event predicate --------------------------------

func TestCodexTerminalEventPredicate(t *testing.T) {
	tests := []struct {
		name     string
		exitCode int
		stream   string
		want     bool
	}{
		{
			name:   "the last turn.* is turn.completed",
			stream: `{"type":"turn.started"}` + "\n" + `{"type":"turn.completed","usage":{}}`,
			want:   true,
		},
		{
			name:   "the last turn.* is turn.failed",
			stream: `{"type":"turn.completed","usage":{}}` + "\n" + `{"type":"turn.failed"}`,
			want:   false,
		},
		{
			name: "events after the terminal one do not change the verdict",
			stream: `{"type":"turn.completed","usage":{}}` + "\n" +
				`{"type":"item.completed","item":{"type":"agent_message","text":"bye"}}`,
			want: true,
		},
		{
			name:   "no turn.* event at all",
			stream: `{"type":"thread.started","thread_id":"x"}`,
			want:   false,
		},
		{
			name:     "a non-zero exit overrides a completed turn",
			exitCode: 1,
			stream:   `{"type":"turn.completed","usage":{}}`,
			want:     false,
		},
		{
			name: "the pre-0.148 schema is no longer accepted",
			stream: `{"type":"step_finish","cost":0.5,"tokens":{"input":1,"output":1}}` + "\n" +
				`{"type":"task_complete"}`,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := CodexProvider{}.Parse(tt.exitCode, []byte(tt.stream))
			if res.Success != tt.want {
				t.Errorf("Success = %v, want %v (error=%q)", res.Success, tt.want, res.ErrorMessage)
			}
		})
	}
}

func TestCodexErrorMessageFallbacks(t *testing.T) {
	tests := []struct {
		name   string
		stream string
		want   string
	}{
		{
			name:   "item.message on an item.completed error",
			stream: `{"type":"item.completed","item":{"type":"error","message":"boom"}}`,
			want:   "boom",
		},
		{
			name:   "top-level message on a type:error event",
			stream: `{"type":"error","message":"top level boom"}`,
			want:   "top level boom",
		},
		{
			name:   "error.message when there is no message field",
			stream: `{"type":"error","error":{"message":"nested boom"}}`,
			want:   "nested boom",
		},
		{
			name:   "the error object itself when it has no message",
			stream: `{"type":"error","error":{"code":"E_NOPE"}}`,
			want:   `{"code":"E_NOPE"}`,
		},
		{
			name:   "a scalar error field",
			stream: `{"type":"error","error":"just a string"}`,
			want:   "just a string",
		},
		{
			// Python's last resort is str(ev), a CPython dict repr. Go
			// echoes the raw JSONL line instead — see codexErrorMessage.
			name:   "an error event with nothing to say falls back to its own line",
			stream: `{"type":"error"}`,
			want:   `{"type":"error"}`,
		},
		{
			name:   "no error event, no events at all",
			stream: "",
			want:   "codex produced no JSONL events",
		},
		{
			name:   "no error event but no terminal event either",
			stream: `{"type":"thread.started","thread_id":"x"}`,
			want:   "codex terminal event was not success (exit=1)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := CodexProvider{}.Parse(1, []byte(tt.stream))
			if res.ErrorMessage != tt.want {
				t.Errorf("ErrorMessage = %q, want %q", res.ErrorMessage, tt.want)
			}
		})
	}
}

func TestIsCodexErrorShapes(t *testing.T) {
	tests := []struct {
		name  string
		line  string
		fatal bool
	}{
		{name: "top-level error", line: `{"type":"error","message":"x"}`, fatal: true},
		{name: "item.completed wrapping an error", line: `{"type":"item.completed","item":{"type":"error","message":"x"}}`, fatal: true},
		{name: "item.completed wrapping something else", line: `{"type":"item.completed","item":{"type":"agent_message","text":"x"}}`},
		{name: "item.completed with no item", line: `{"type":"item.completed"}`},
		{name: "item.completed with a non-object item", line: `{"type":"item.completed","item":"nope"}`},
		{name: "an ordinary event", line: `{"type":"turn.completed","usage":{}}`},
		{
			name: "a known non-fatal warning at the top level",
			line: `{"type":"error","message":"Skill descriptions were shortened to fit"}`,
		},
		{
			name: "a known non-fatal warning inside an item",
			line: `{"type":"item.completed","item":{"type":"error","message":"Skill descriptions were shortened"}}`,
		},
		{
			name:  "the warning match is case-sensitive, as in Python",
			line:  `{"type":"error","message":"skill descriptions were shortened"}`,
			fatal: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events := jsonlEvents([]byte(tt.line))
			if len(events) != 1 {
				t.Fatalf("fixture line must decode to exactly one event, got %d", len(events))
			}
			if got := isCodexError(events[0]); got != tt.fatal {
				t.Errorf("isCodexError = %v, want %v", got, tt.fatal)
			}
		})
	}
}

func TestSumCodexUsageTolerance(t *testing.T) {
	tests := []struct {
		name      string
		stream    string
		tokensIn  int
		tokensOut int
	}{
		{
			name:   "only turn.completed events count",
			stream: `{"type":"turn.started","usage":{"input_tokens":999,"output_tokens":999}}`,
		},
		{
			name:   "a turn.completed with no usage",
			stream: `{"type":"turn.completed"}`,
		},
		{
			name:   "a turn.completed whose usage is not an object",
			stream: `{"type":"turn.completed","usage":7}`,
		},
		{
			name:     "missing token keys default to zero",
			stream:   `{"type":"turn.completed","usage":{"input_tokens":5}}`,
			tokensIn: 5,
		},
		{
			name:     "numeric strings coerce, as int() does",
			stream:   `{"type":"turn.completed","usage":{"input_tokens":"5","output_tokens":"6"}}`,
			tokensIn: 5, tokensOut: 6,
		},
		{
			name:      "nulls read as zero, as `x or 0` does",
			stream:    `{"type":"turn.completed","usage":{"input_tokens":null,"output_tokens":4}}`,
			tokensOut: 4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cost, in, out := CodexProvider{}.ExtractCost([]byte(tt.stream))
			if cost != 0 {
				t.Errorf("CostUSD = %v, want 0 — codex reports no USD", cost)
			}
			if in != tt.tokensIn || out != tt.tokensOut {
				t.Errorf("tokens = (%d,%d), want (%d,%d)", in, out, tt.tokensIn, tt.tokensOut)
			}
		})
	}
}
