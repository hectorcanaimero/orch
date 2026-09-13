package providers

import (
	"strings"
	"testing"

	"github.com/hectorcanaimero/orch/internal/model"
)

func opencodeRoute(cliModel string) model.RouteEntry {
	return model.RouteEntry{Backend: model.BackendOpencode, CLIModel: cliModel}
}

func TestOpencodeArgv(t *testing.T) {
	argv := OpencodeProvider{}.Argv(Request{
		TaskID: "F1.T1",
		Route:  opencodeRoute("deepseek/deepseek-v4-flash"),
		Cwd:    "/abs/worktree",
	})

	want := []string{
		"opencode", "run",
		"--format", "json",
		"--model", "deepseek/deepseek-v4-flash",
		"--auto",
		"--dir", "/abs/worktree",
	}
	if len(argv) != len(want) {
		t.Fatalf("argv = %q, want %q", argv, want)
	}
	for i := range want {
		if argv[i] != want[i] {
			t.Fatalf("argv = %q, want %q", argv, want)
		}
	}
}

// TestOpencodeArgvPassesModelVerbatim pins that the provider prefix survives.
// Router entries carry it themselves and there is no second field to join, so
// stripping or rewriting it here would silently change which model runs.
func TestOpencodeArgvPassesModelVerbatim(t *testing.T) {
	argv := OpencodeProvider{}.Argv(Request{
		Route: opencodeRoute("google/gemini-2.5-pro"),
		Cwd:   "/w",
	})
	if !contains(argv, "google/gemini-2.5-pro") {
		t.Errorf("argv = %q, want the model verbatim with its prefix", argv)
	}
}

// TestOpencodeParseRealSuccess reads a real opencode 1.18.30 run.
//
// The cost is genuinely 0 there (a free-tier model) while both token counts
// are non-zero, which is the case that separates "no telemetry" from "no
// charge": Estimated must stay false.
func TestOpencodeParseRealSuccess(t *testing.T) {
	out := readFixture(t, "opencode", "1.18.30", "success.json")
	res := OpencodeProvider{}.Parse(0, out)

	if !res.Success {
		t.Fatalf("want success, got failure: %q", res.ErrorMessage)
	}
	if res.ErrorMessage != "" {
		t.Errorf("ErrorMessage = %q, want empty", res.ErrorMessage)
	}
	if res.TokensIn != 9717 || res.TokensOut != 15 {
		t.Errorf("tokens = (%d,%d), want (9717,15)", res.TokensIn, res.TokensOut)
	}
	if res.CostUSD != 0 {
		t.Errorf("CostUSD = %v, want 0", res.CostUSD)
	}
	if res.Estimated {
		t.Error("Estimated = true, want false — the run reported usage; a zero " +
			"cost on a free model is not missing telemetry")
	}
}

// TestOpencodeParseRealUnknownModel is the regression for bug 28. The real
// error envelope nests its sentence in error.data.message; Python reads
// error.message, which is absent, and falls back to a CPython dict repr.
// Asserting on a sentence is what catches a regression back to the repr.
func TestOpencodeParseRealUnknownModel(t *testing.T) {
	out := readFixture(t, "opencode", "1.18.30", "unknown-model.json")
	res := OpencodeProvider{}.Parse(1, out)

	if res.Success {
		t.Fatal("want failure")
	}
	// Python takes the FIRST error event, and so does this. Which one that is
	// matters more than it looks: opencode 1.18.30 emits a generic
	// "Unexpected server error" before the specific diagnosis, so the
	// operator is shown the useless one. Preferring a later, more specific
	// event would be a heuristic nobody asked for and would move what
	// Classify reads, so the behaviour is ported as-is and the observation
	// lives in docs/brainstorm/go-migration-notes/sonnet-2.md instead.
	if res.ErrorMessage != "Unexpected server error. Check server logs for details." {
		t.Errorf("ErrorMessage = %q, want the first error event's sentence",
			res.ErrorMessage)
	}
	if strings.Contains(res.ErrorMessage, "map[") ||
		strings.Contains(res.ErrorMessage, "UnknownError") {
		t.Errorf("ErrorMessage = %q — that is the whole error object, not its "+
			"message; bug 28 is back", res.ErrorMessage)
	}
}

// TestOpencodeRealUnknownModelHidesTheUsefulSentence pins the observation
// above as a fact about the fixture rather than a claim in a comment: the
// sentence an operator actually needs is in the stream, just not in the event
// the parser reports. If a future opencode stops emitting the generic error
// first, this fails and the note can be closed.
func TestOpencodeRealUnknownModelHidesTheUsefulSentence(t *testing.T) {
	out := readFixture(t, "opencode", "1.18.30", "unknown-model.json")

	events := jsonlEvents(out)
	var errors []string
	for _, ev := range events {
		if ev.eventType() == "error" {
			errors = append(errors, opencodeErrorMessage(ev))
		}
	}
	if len(errors) != 2 {
		t.Fatalf("want 2 error events, got %d: %q", len(errors), errors)
	}
	if !strings.Contains(errors[1], "Model not found: no-such-model-xyz") {
		t.Errorf("second error = %q, want the specific diagnosis", errors[1])
	}
}

// TestOpencodeParseInsufficientBalance is a real capture of a paid route with
// no credit left. It is the only fixture in the tree where opencode's error
// carries an HTTP status, and it lands on FailurePermission through the 401
// rather than through any word in the sentence — worth pinning, because
// "out of money" and "not allowed" are different operator actions and only
// the status code is telling them apart today.
func TestOpencodeParseInsufficientBalance(t *testing.T) {
	out := readFixture(t, "opencode", "1.18.30", "insufficient-balance.json")
	res := OpencodeProvider{}.Parse(1, out)

	if res.Success {
		t.Fatal("want failure")
	}
	if !strings.Contains(res.ErrorMessage, "Insufficient balance") {
		t.Errorf("ErrorMessage = %q", res.ErrorMessage)
	}
	if got := Classify(res); got != FailurePermission {
		t.Errorf("Classify = %q, want %q", got, FailurePermission)
	}
}

// TestOpencodeParsePrefixedModelNotFound is the evidence behind the router
// note: `deepseek/deepseek-v4-flash`, the spelling model_router.yaml ships,
// does not resolve against opencode 1.18.30 even with the account
// authenticated. The CLI answers with the bare ids, and `opencode models`
// serves those DeepSeek models under the `opencode-go/` provider.
func TestOpencodeParsePrefixedModelNotFound(t *testing.T) {
	out := readFixture(t, "opencode", "1.18.30", "unknown-model-prefixed.json")
	res := OpencodeProvider{}.Parse(1, out)

	if res.Success {
		t.Fatal("want failure")
	}
	if !strings.Contains(string(out),
		"Model not found: deepseek/deepseek-v4-flash") {
		t.Fatal("the fixture no longer shows the prefixed id being rejected")
	}
	// Unretryable, and classified so — a route pointing at a name the CLI
	// does not know must not burn the attempt budget.
	if got := Classify(res); got != FailureVersionDrift {
		t.Errorf("Classify = %q, want %q", got, FailureVersionDrift)
	}
}

// TestOpencodeParseNoUsageIsEstimated covers Issue #8: step_finish events with
// both token counts at zero mean the provider does not report usage, which the
// budget guardrail has to be able to tell from genuinely free work.
func TestOpencodeParseNoUsageIsEstimated(t *testing.T) {
	out := readFixture(t, "opencode", "synthetic", "no-usage.json")
	res := OpencodeProvider{}.Parse(0, out)

	if !res.Success {
		t.Fatalf("want success, got %q", res.ErrorMessage)
	}
	if !res.Estimated {
		t.Error("Estimated = false, want true")
	}
}

func TestOpencodeParseFailureReason(t *testing.T) {
	out := readFixture(t, "opencode", "synthetic", "aborted.json")
	res := OpencodeProvider{}.Parse(0, out)

	if res.Success {
		t.Fatal("want failure: 'aborted' is in the failure set")
	}
	if !strings.Contains(res.ErrorMessage, `reason="aborted"`) {
		t.Errorf("ErrorMessage = %q, want the terminal reason", res.ErrorMessage)
	}
	// Tokens are still reported: a run that aborted has already been paid for.
	if res.TokensIn != 12 {
		t.Errorf("TokensIn = %d, want 12", res.TokensIn)
	}
}

func TestOpencodeParseEdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		exitCode int
		output   string
		wantErr  string
	}{
		{
			name:     "no events at all",
			exitCode: 1,
			output:   "",
			wantErr:  "opencode produced no JSONL events",
		},
		{
			name:     "events but no terminal step_finish",
			exitCode: 0,
			output:   `{"type":"text","part":{"text":"hi"}}`,
			wantErr:  "did not emit a terminal step_finish",
		},
		{
			name:     "exit non-zero outranks a clean terminal event",
			exitCode: 3,
			output:   `{"type":"step_finish","part":{"reason":"stop","tokens":{"input":1,"output":1}}}`,
			wantErr:  "is in failure set",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := OpencodeProvider{}.Parse(tt.exitCode, []byte(tt.output))
			if res.Success {
				t.Fatal("want failure")
			}
			if !strings.Contains(res.ErrorMessage, tt.wantErr) {
				t.Errorf("ErrorMessage = %q, want it to contain %q",
					res.ErrorMessage, tt.wantErr)
			}
		})
	}
}

func TestOpencodeExtractCost(t *testing.T) {
	out := readFixture(t, "opencode", "1.18.30", "success.json")
	cost, in, outTok := OpencodeProvider{}.ExtractCost(out)
	if cost != 0 || in != 9717 || outTok != 15 {
		t.Errorf("ExtractCost = (%v,%d,%d), want (0,9717,15)", cost, in, outTok)
	}
}

// TestOpencodeStepFinishPayloadPrefersPart pins the drift tolerance: real
// opencode nests the payload under `part`, older fixtures put it at the top
// level, and reading the wrong layer silently returns zero.
func TestOpencodeStepFinishPayloadPrefersPart(t *testing.T) {
	nested := `{"type":"step_finish","cost":9,"part":{"reason":"stop","tokens":{"input":5,"output":7},"cost":1}}`
	flat := `{"type":"step_finish","reason":"stop","tokens":{"input":5,"output":7},"cost":1}`

	for _, line := range []string{nested, flat} {
		cost, in, out := sumStepFinishCosts(jsonlEvents([]byte(line)))
		if cost != 1 || in != 5 || out != 7 {
			t.Errorf("%s → (%v,%d,%d), want (1,5,7)", line, cost, in, out)
		}
	}
}
