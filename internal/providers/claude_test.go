package providers

import (
	"os"
	"path/filepath"
	"strings"
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
			// #231: a worktree lacks the gitignored .claude/ settings, so the
			// engine hands the project root's over, and they go before the
			// budget.
			name: "settings from the project root",
			req: Request{
				Route:     claudeRoute("opus"),
				Settings:  `{"permissions":{"allow":["Bash(git status)"]}}`,
				BudgetUSD: &budget,
			},
			want: []string{
				"claude", "-p",
				"--output-format", "json",
				"--model", "opus",
				"--add-dir", ".",
				"--permission-mode", "acceptEdits",
				"--settings", `{"permissions":{"allow":["Bash(git status)"]}}`,
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

// #231: under acceptEdits, every tool not allow-listed is denied without a
// prompt, and the run still ends `success`. The denials are the only trace,
// so Parse carries them out — without turning the run into a failure, since
// an agent often works around a denial.
func TestClaudeParseReportsPermissionDenials(t *testing.T) {
	out := []byte(`{"type":"result","subtype":"success","is_error":false,"total_cost_usd":0.1,` +
		`"usage":{"input_tokens":1,"output_tokens":1},"permission_denials":[` +
		`{"tool_name":"Bash","tool_use_id":"a","tool_input":{"command":"git fetch origin dev"}},` +
		`{"tool_name":"WebFetch","tool_use_id":"b","tool_input":{"url":"https://example.com"}},` +
		`{"tool_name":"mcp__orch__orch_block","tool_use_id":"c","tool_input":{}}]}`)
	res := ClaudeProvider{}.Parse(0, out)
	if !res.Success {
		t.Fatalf("a denial is not a failure: %q", res.ErrorMessage)
	}
	want := []string{"Bash(git fetch origin dev)", "WebFetch", "mcp__orch__orch_block"}
	if strings.Join(res.PermissionDenials, "|") != strings.Join(want, "|") {
		t.Errorf("PermissionDenials = %q, want %q", res.PermissionDenials, want)
	}
	if got := (ClaudeProvider{}).Parse(0, readFixture(t, "claude", "2.1.269", "success.json")).PermissionDenials; len(got) != 0 {
		t.Errorf("an empty permission_denials gave %q", got)
	}
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

// TestClaude2_1_269UnrecognizedModelIsVersionDrift is the case the
// fallback-model retry exists for, pinned against the bytes the CLI really
// emits.
//
// It used to assert FailureOther, because none of the version-drift markers
// appeared in claude 2.1.269's rejection text and the retry therefore never
// fired. Fixed in Python first (#118) and mirrored here with the same two
// markers, so neither binary classifies this differently.
func TestClaude2_1_269UnrecognizedModelIsVersionDrift(t *testing.T) {
	out := readFixture(t, "claude", "2.1.269", "unrecognized-model.log")
	res := ClaudeProvider{}.Parse(1, out)

	if got := Classify(res); got != FailureVersionDrift {
		t.Errorf("Classify = %q, want %q", got, FailureVersionDrift)
	}
	if !IsVersionDrift(res) {
		t.Error("IsVersionDrift must agree with Classify")
	}
}

// TestClaude2_1_269UnrecognizedModelMatchesBothMarkers pins each of the two
// new markers on its own. The fixture contains both — the stderr line carries
// `unrecognized_model` and the envelope's result text carries the sentence —
// so a single assertion would keep passing if one of them were dropped.
func TestClaude2_1_269UnrecognizedModelMatchesBothMarkers(t *testing.T) {
	out := string(readFixture(t, "claude", "2.1.269", "unrecognized-model.log"))
	if !strings.Contains(out, "unrecognized_model") {
		t.Error("fixture no longer carries the stderr marker")
	}
	if !strings.Contains(out, "may not exist or you may not have access to it") {
		t.Error("fixture no longer carries the rejection sentence")
	}

	for _, tt := range []struct{ name, blob string }{
		{"the stderr marker alone", "[claude-code:unrecognized_model] {}"},
		{"the rejection sentence alone", "It may not exist or you may not have access to it."},
	} {
		t.Run(tt.name, func(t *testing.T) {
			res := Result{ExitCode: 1, Stdout: tt.blob}
			if got := Classify(res); got != FailureVersionDrift {
				t.Errorf("Classify = %q, want %q", got, FailureVersionDrift)
			}
		})
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

// TestClaude2_1_269TruncatedEnvelopeIsAParserFailure is the case the
// status-code boundary rule exists for.
//
// It used to assert FailurePermission: "401" and "403" were matched as bare
// substrings against a haystack that includes 2 KB of stdout, and this
// fixture carries `"cache_read_input_tokens":40321`, in which "40321"
// contains "403". A truncated envelope — retryable — was therefore classified
// terminal. Fixed in Python first (#118) and mirrored here.
func TestClaude2_1_269TruncatedEnvelopeIsAParserFailure(t *testing.T) {
	out := readFixture(t, "claude", "2.1.269", "truncated.json")
	if !strings.Contains(string(out), "40321") {
		t.Fatal("fixture no longer carries the number that used to trip the 403 marker")
	}
	res := ClaudeProvider{}.Parse(0, out)

	if res.ErrorMessage != "could not parse claude JSON envelope" {
		t.Fatalf("ErrorMessage = %q", res.ErrorMessage)
	}
	if got := Classify(res); got != FailureParser {
		t.Errorf("Classify = %q, want %q", got, FailureParser)
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

	// An expired key reads as `code: "authentication_error"` with the message
	// "Invalid API key". That used to classify as FailureOther — neither
	// "authentication failed" nor "auth error" is a substring of
	// "authentication_error" — so the one failure that certainly will not fix
	// itself got the retry-once treatment. Fixed in Python first (#118) and
	// mirrored here.
	if got := Classify(res); got != FailurePermission {
		t.Errorf("Classify = %q, want %q", got, FailurePermission)
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

// contains reports whether hay holds needle. Used by the argv assertions.
func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}
