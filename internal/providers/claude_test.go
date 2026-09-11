package providers

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hectorcanaimero/orch/internal/model"
)

// readFixture loads one file from testdata. Every parser test goes through
// here rather than through a string literal in the test body: checklist rule
// 21 wants the parsers pinned against bytes a CLI produced, and testdata/README.md
// records which of these are real captures and which are not.
func readFixture(t *testing.T, parts ...string) []byte {
	t.Helper()
	p := filepath.Join(append([]string{"testdata"}, parts...)...)
	b, err := os.ReadFile(p) // #nosec G304 -- path is a test-local literal under testdata/
	if err != nil {
		t.Fatalf("read fixture %s: %v", p, err)
	}
	return b
}

func claudeRoute(cliModel string) model.RouteEntry {
	return model.RouteEntry{
		Backend:   model.BackendClaude,
		CLIModel:  cliModel,
		Tier:      model.TierPremium,
		IsPremium: true,
	}
}

func TestClaudeArgv(t *testing.T) {
	budget := 5.0
	wholeBudget := 12.0

	tests := []struct {
		name string
		req  Request
		want []string
	}{
		{
			name: "no session id and no budget",
			req:  Request{Route: claudeRoute("opus")},
			want: []string{
				"claude", "-p",
				"--output-format", "json",
				"--model", "opus",
				"--add-dir", ".",
				"--permission-mode", "acceptEdits",
			},
		},
		{
			name: "session id is placed before --add-dir, as in Python",
			req: Request{
				Route:     claudeRoute("opus"),
				SessionID: "3f2c9a10-7b4d-4e51-9c88-1d2e3f4a5b60",
			},
			want: []string{
				"claude", "-p",
				"--output-format", "json",
				"--model", "opus",
				"--session-id", "3f2c9a10-7b4d-4e51-9c88-1d2e3f4a5b60",
				"--add-dir", ".",
				"--permission-mode", "acceptEdits",
			},
		},
		{
			name: "budget is appended last",
			req: Request{
				Route:     claudeRoute("claude-haiku-4-5-20251001"),
				SessionID: "s",
				BudgetUSD: &budget,
			},
			want: []string{
				"claude", "-p",
				"--output-format", "json",
				"--model", "claude-haiku-4-5-20251001",
				"--session-id", "s",
				"--add-dir", ".",
				"--permission-mode", "acceptEdits",
				// Python renders this with str(5.0), not str(5).
				"--max-budget-usd", "5.0",
			},
		},
		{
			name: "whole-number budget still renders with the .0",
			req: Request{
				Route:     claudeRoute("opus"),
				BudgetUSD: &wholeBudget,
			},
			want: []string{
				"claude", "-p",
				"--output-format", "json",
				"--model", "opus",
				"--add-dir", ".",
				"--permission-mode", "acceptEdits",
				"--max-budget-usd", "12.0",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClaudeProvider{}.Argv(tt.req)
			assertArgv(t, got, tt.want)
		})
	}
}

func TestClaudeArgvIsPure(t *testing.T) {
	req := Request{Route: claudeRoute("opus"), SessionID: "s"}
	first := ClaudeProvider{}.Argv(req)
	second := ClaudeProvider{}.Argv(req)
	assertArgv(t, first, second)
}

// TestClaudeArgvNeverShellsOut guards the "NO shell metacharacters" note on
// Python's build_cmd: the engine execs the argv directly, so a metacharacter
// here would be a latent injection the day someone routes it through a shell.
func TestClaudeArgvNoShellMetacharacters(t *testing.T) {
	budget := 5.0
	argv := ClaudeProvider{}.Argv(Request{
		Route:     claudeRoute("opus"),
		SessionID: NewSessionID(),
		BudgetUSD: &budget,
	})
	assertNoShellMetacharacters(t, argv)
}

// ---- Parse: real claude 2.1.269 captures --------------------------------

func TestClaude2_1_269ParseSuccess(t *testing.T) {
	out := readFixture(t, "claude", "2.1.269", "success.json")
	res := ClaudeProvider{}.Parse(0, out)

	if !res.Success {
		t.Fatalf("want success, got failure: %q", res.ErrorMessage)
	}
	if res.ErrorMessage != "" {
		t.Errorf("ErrorMessage = %q, want empty", res.ErrorMessage)
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", res.ExitCode)
	}
	if res.CostUSD != 0.0475971 {
		t.Errorf("CostUSD = %v, want 0.0475971", res.CostUSD)
	}
	// Deliberately the top-level usage.input_tokens (19), NOT modelUsage's
	// inputTokens (916): Python reads usage.*, so a divergence here would
	// silently change every spend row.
	if res.TokensIn != 19 {
		t.Errorf("TokensIn = %d, want 19", res.TokensIn)
	}
	if res.TokensOut != 603 {
		t.Errorf("TokensOut = %d, want 603", res.TokensOut)
	}
	if res.Stdout != string(out) {
		t.Error("Stdout must carry the captured output verbatim")
	}
}

func TestClaude2_1_269ExtractCostMatchesParse(t *testing.T) {
	out := readFixture(t, "claude", "2.1.269", "success.json")
	cost, in, outTok := ClaudeProvider{}.ExtractCost(out)
	res := ClaudeProvider{}.Parse(0, out)
	if cost != res.CostUSD || in != res.TokensIn || outTok != res.TokensOut {
		t.Errorf("ExtractCost = (%v,%d,%d), Parse = (%v,%d,%d); they must agree",
			cost, in, outTok, res.CostUSD, res.TokensIn, res.TokensOut)
	}
}

// TestClaude2_1_269ParseUnrecognizedModel pins the real shape of a rejected
// --model: stderr and stdout merged (so the blob as a whole is not JSON),
// is_error true, subtype STILL "success", and api_error_status a bare number.
func TestClaude2_1_269ParseUnrecognizedModel(t *testing.T) {
	out := readFixture(t, "claude", "2.1.269", "unrecognized-model.log")
	res := ClaudeProvider{}.Parse(1, out)

	if res.Success {
		t.Fatal("a run with is_error=true must not be a success")
	}
	// Python: str(404). Not "404.0", and not the object branch.
	if res.ErrorMessage != "404" {
		t.Errorf("ErrorMessage = %q, want %q", res.ErrorMessage, "404")
	}
	if res.CostUSD != 0 {
		t.Errorf("CostUSD = %v, want 0", res.CostUSD)
	}
}

// TestClaude2_1_269UnrecognizedModelIsNotClassifiedAsDrift records a real gap
// between the marker table and the CLI, NOT desired behaviour.
//
// claude 2.1.269 says "It may not exist or you may not have access to it" for
// an unknown model. None of Python's _VERSION_DRIFT_MARKERS appear in that
// sentence, nor in the `[claude-code:unrecognized_model]` stderr line, so the
// one case the fallback-model retry exists for does not trigger it — the
// dispatch lands in FailureOther and retries the same broken model instead.
//
// Ported as-is because the migration does not fix Python bugs in Go. The gap
// is written up in docs/brainstorm/go-migration-notes.md; when it is fixed,
// fix it in both and change this test to want FailureVersionDrift.
func TestClaude2_1_269UnrecognizedModelIsNotClassifiedAsDrift(t *testing.T) {
	out := readFixture(t, "claude", "2.1.269", "unrecognized-model.log")
	res := ClaudeProvider{}.Parse(1, out)

	if got := Classify(res); got != FailureOther {
		t.Errorf("Classify = %q, want %q — if this now says version_drift, "+
			"the marker table changed and go-migration-notes.md needs updating",
			got, FailureOther)
	}
	if IsVersionDrift(res) {
		t.Error("IsVersionDrift must agree with Classify")
	}
}

// TestClaude2_1_269ParseKilledRun pins what a timed-out dispatch really
// leaves behind: nothing. The CLI buffers the whole envelope and writes it
// once at the end, so a SIGKILL mid-run yields an empty log, not a partial one.
func TestClaude2_1_269ParseKilledRun(t *testing.T) {
	out := readFixture(t, "claude", "2.1.269", "killed-empty.log")
	if len(out) != 0 {
		t.Fatalf("fixture must stay empty, got %d bytes", len(out))
	}
	res := ClaudeProvider{}.Parse(-9, out)

	if res.Success {
		t.Fatal("an empty log is not a success")
	}
	if res.ErrorMessage != "could not parse claude JSON envelope" {
		t.Errorf("ErrorMessage = %q", res.ErrorMessage)
	}
	if got := Classify(res); got != FailureParser {
		t.Errorf("Classify = %q, want %q", got, FailureParser)
	}
	// And once the engine stamps its timeout reason on top, TIMEOUT wins.
	res.ErrorMessage = "orchestrator timeout after 300s"
	if got := Classify(res); got != FailureTimeout {
		t.Errorf("Classify after timeout stamp = %q, want %q", got, FailureTimeout)
	}
}

func TestClaude2_1_269ParseTruncatedEnvelope(t *testing.T) {
	out := readFixture(t, "claude", "2.1.269", "truncated.json")
	res := ClaudeProvider{}.Parse(0, out)

	if res.Success {
		t.Fatal("a half-written envelope is not a success")
	}
	if res.ErrorMessage != "could not parse claude JSON envelope" {
		t.Errorf("ErrorMessage = %q", res.ErrorMessage)
	}
	// No cost can be recovered from an unparseable envelope.
	if res.CostUSD != 0 || res.TokensIn != 0 || res.TokensOut != 0 {
		t.Errorf("want zero cost/tokens, got (%v,%d,%d)", res.CostUSD, res.TokensIn, res.TokensOut)
	}
}

// TestClaude2_1_269TruncatedEnvelopeMisclassifiesAsPermission records a
// second real defect in the marker table, NOT desired behaviour.
//
// "401" and "403" are matched as bare substrings, so they hit inside any
// longer number that happens to contain those digits. This fixture carries
// `"cache_read_input_tokens":40321`, and "40321" contains "403", so a
// truncated envelope is classified PERMISSION — terminal, never retried —
// when it is plainly a PARSER failure. The same trap is set for "429" and
// "500" against token counts, durations and millisecond timings, which every
// claude envelope is full of.
//
// Ported as-is. Fixing it (anchoring the numeric markers to an HTTP-status
// context) is a change to Python and Go together; it is written up in
// docs/brainstorm/go-migration-notes.md.
func TestClaude2_1_269TruncatedEnvelopeMisclassifiesAsPermission(t *testing.T) {
	out := readFixture(t, "claude", "2.1.269", "truncated.json")
	res := ClaudeProvider{}.Parse(0, out)

	if res.ErrorMessage != "could not parse claude JSON envelope" {
		t.Fatalf("ErrorMessage = %q — this test is about how that message is classified", res.ErrorMessage)
	}
	if got := Classify(res); got != FailurePermission {
		t.Errorf("Classify = %q, want %q — if this now says parser, the numeric "+
			"markers were anchored and go-migration-notes.md needs updating", got, FailurePermission)
	}
}

// ---- Parse: synthetic claude fixtures -----------------------------------

// TestClaudeSyntheticAuthError covers the api_error_status-as-object branch,
// which the real captures do not reach. Fixture is the long-standing one from
// the Python suite, byte for byte.
func TestClaudeSyntheticAuthError(t *testing.T) {
	out := readFixture(t, "claude", "synthetic", "auth-error.json")
	res := ClaudeProvider{}.Parse(1, out)

	if res.Success {
		t.Fatal("want failure")
	}
	if res.ErrorMessage != "Invalid API key" {
		t.Errorf("ErrorMessage = %q, want %q", res.ErrorMessage, "Invalid API key")
	}
	// Cost is still extracted from a failed run — it was already paid for.
	if res.CostUSD != 0.0012 {
		t.Errorf("CostUSD = %v, want 0.0012", res.CostUSD)
	}
	if res.TokensIn != 50 || res.TokensOut != 0 {
		t.Errorf("tokens = (%d,%d), want (50,0)", res.TokensIn, res.TokensOut)
	}

	// And a third gap in the marker table, pinned rather than endorsed: an
	// expired key reads as `code: "authentication_error"` and the message
	// "Invalid API key", and the permission markers are "authentication
	// failed" and "auth error" — neither of which is a substring of
	// "authentication_error". So the one failure that certainly will not fix
	// itself gets the retry-once treatment. See
	// docs/brainstorm/go-migration-notes.md.
	if got := Classify(res); got != FailureOther {
		t.Errorf("Classify = %q, want %q — if this now says permission, the "+
			"markers grew an auth case and go-migration-notes.md needs updating",
			got, FailureOther)
	}
}

func TestClaudeSyntheticRateLimit(t *testing.T) {
	out := readFixture(t, "claude", "synthetic", "rate-limit.json")
	res := ClaudeProvider{}.Parse(1, out)

	if res.Success {
		t.Fatal("want failure")
	}
	if res.ErrorMessage != "429" {
		t.Errorf("ErrorMessage = %q, want %q", res.ErrorMessage, "429")
	}
	if got := Classify(res); got != FailureRateLimit {
		t.Errorf("Classify = %q, want %q", got, FailureRateLimit)
	}
}

// ---- Parse: the three-way success predicate ------------------------------

func TestClaudeParseSuccessPredicate(t *testing.T) {
	tests := []struct {
		name     string
		exitCode int
		envelope string
		want     bool
	}{
		{
			name:     "all three conditions hold",
			exitCode: 0,
			envelope: `{"is_error":false,"subtype":"success","total_cost_usd":0.1}`,
			want:     true,
		},
		{
			name:     "non-zero exit alone fails it",
			exitCode: 1,
			envelope: `{"is_error":false,"subtype":"success"}`,
			want:     false,
		},
		{
			name:     "is_error alone fails it",
			exitCode: 0,
			envelope: `{"is_error":true,"subtype":"success"}`,
			want:     false,
		},
		{
			name:     "a non-success subtype alone fails it",
			exitCode: 0,
			envelope: `{"is_error":false,"subtype":"error_during_execution"}`,
			want:     false,
		},
		{
			name:     "a missing subtype fails it",
			exitCode: 0,
			envelope: `{"is_error":false}`,
			want:     false,
		},
		{
			name:     "is_error is read with Python truthiness, so 0 is false",
			exitCode: 0,
			envelope: `{"is_error":0,"subtype":"success"}`,
			want:     true,
		},
		{
			name:     "and a non-empty string is true",
			exitCode: 0,
			envelope: `{"is_error":"yes","subtype":"success"}`,
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := ClaudeProvider{}.Parse(tt.exitCode, []byte(tt.envelope))
			if res.Success != tt.want {
				t.Errorf("Success = %v, want %v (error=%q)", res.Success, tt.want, res.ErrorMessage)
			}
		})
	}
}

func TestClaudeErrorMessageFallbacks(t *testing.T) {
	tests := []struct {
		name     string
		envelope string
		want     string
	}{
		{
			name:     "api_error_status wins over terminal_reason",
			envelope: `{"is_error":true,"api_error_status":500,"terminal_reason":"error"}`,
			want:     "500",
		},
		{
			name:     "terminal_reason is used when api_error_status is absent",
			envelope: `{"is_error":true,"terminal_reason":"api_error"}`,
			want:     "api_error",
		},
		{
			name:     "a null api_error_status falls through, as Python's `or` does",
			envelope: `{"is_error":true,"api_error_status":null,"terminal_reason":"api_error"}`,
			want:     "api_error",
		},
		{
			name:     "an object without a message falls back to the object itself",
			envelope: `{"is_error":true,"api_error_status":{"code":"overloaded"}}`,
			want:     `{"code":"overloaded"}`,
		},
		{
			name:     "with neither field, Python's formatted bool",
			envelope: `{"is_error":true,"subtype":"success"}`,
			want:     "claude reported is_error=True",
		},
		{
			name:     "and False when the failure was the exit code alone",
			envelope: `{"is_error":false,"subtype":"success"}`,
			want:     "claude reported is_error=False",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := ClaudeProvider{}.Parse(1, []byte(tt.envelope))
			if res.ErrorMessage != tt.want {
				t.Errorf("ErrorMessage = %q, want %q", res.ErrorMessage, tt.want)
			}
		})
	}
}

func TestParseClaudeEnvelope(t *testing.T) {
	tests := []struct {
		name  string
		input string
		ok    bool
		// subtype identifies which object came back, when one did.
		subtype string
	}{
		{name: "empty", input: "", ok: false},
		{name: "whitespace only", input: "  \n\t\n", ok: false},
		{name: "not json", input: "garbage output not json", ok: false},
		{
			name:    "whole body is the envelope",
			input:   `{"subtype":"whole"}`,
			ok:      true,
			subtype: "whole",
		},
		{
			name:    "envelope with surrounding whitespace",
			input:   "\n  {\"subtype\":\"padded\"}  \n",
			ok:      true,
			subtype: "padded",
		},
		{
			name:    "stderr noise before the envelope falls back to the last line",
			input:   "[claude-code:warning] something\n{\"subtype\":\"last\"}\n",
			ok:      true,
			subtype: "last",
		},
		{
			name:    "the LAST parseable line wins, not the first",
			input:   "{\"subtype\":\"first\"}\n{\"subtype\":\"last\"}\n",
			ok:      true,
			subtype: "last",
		},
		{
			name:    "a trailing unparseable line is skipped",
			input:   "{\"subtype\":\"good\"}\n{\"truncated\":",
			ok:      true,
			subtype: "good",
		},
		{
			name:  "a bare JSON array is not an envelope",
			input: `[{"subtype":"array"}]`,
			ok:    false,
		},
		{
			name:  "a bare JSON scalar is not an envelope",
			input: `42`,
			ok:    false,
		},
		{
			name:  "two objects on one line is Python's Extra data error",
			input: `{"a":1} {"b":2}`,
			ok:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, ok := parseClaudeEnvelope([]byte(tt.input))
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if !ok {
				return
			}
			if got := asString(env["subtype"]); got != tt.subtype {
				t.Errorf("subtype = %q, want %q", got, tt.subtype)
			}
		})
	}
}

func TestClaudeCostTolerance(t *testing.T) {
	tests := []struct {
		name      string
		envelope  string
		cost      float64
		tokensIn  int
		tokensOut int
	}{
		{
			name:     "no cost keys at all",
			envelope: `{"is_error":false,"subtype":"success"}`,
		},
		{
			name:     "usage present but not an object",
			envelope: `{"total_cost_usd":0.5,"usage":"nope"}`,
			cost:     0.5,
		},
		{
			name:     "a null cost reads as zero, as `x or 0.0` does",
			envelope: `{"total_cost_usd":null,"usage":{"input_tokens":3,"output_tokens":4}}`,
			tokensIn: 3, tokensOut: 4,
		},
		{
			name:     "numeric strings coerce, as float()/int() do",
			envelope: `{"total_cost_usd":"0.25","usage":{"input_tokens":"7","output_tokens":"8"}}`,
			cost:     0.25, tokensIn: 7, tokensOut: 8,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cost, in, out := ClaudeProvider{}.ExtractCost([]byte(tt.envelope))
			if cost != tt.cost || in != tt.tokensIn || out != tt.tokensOut {
				t.Errorf("= (%v,%d,%d), want (%v,%d,%d)",
					cost, in, out, tt.cost, tt.tokensIn, tt.tokensOut)
			}
		})
	}
}

func TestClaudeExtractCostOnUnparseableOutput(t *testing.T) {
	cost, in, out := ClaudeProvider{}.ExtractCost([]byte("not json at all"))
	if cost != 0 || in != 0 || out != 0 {
		t.Errorf("= (%v,%d,%d), want zeros", cost, in, out)
	}
}
