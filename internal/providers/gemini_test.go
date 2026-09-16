package providers

import (
	"strings"
	"testing"

	"github.com/hectorcanaimero/orch/internal/model"
)

func geminiRoute(cliModel string) model.RouteEntry {
	return model.RouteEntry{Backend: model.BackendGemini, CLIModel: cliModel}
}

func TestGeminiArgv(t *testing.T) {
	argv := GeminiProvider{}.Argv(Request{
		Route:      geminiRoute("gemini-2.5-flash"),
		PromptText: "do the thing",
		Cwd:        "/abs/worktree",
	})

	want := []string{"gemini", "-p", "do the thing", "--model", "gemini-2.5-flash"}
	if len(argv) != len(want) {
		t.Fatalf("argv = %q, want %q", argv, want)
	}
	for i := range want {
		if argv[i] != want[i] {
			t.Fatalf("argv = %q, want %q", argv, want)
		}
	}
	// No directory flag exists: the engine sets the child's working directory
	// and PWD. A --dir here would be invented, not ported.
	if contains(argv, "--dir") {
		t.Error("argv carries a --dir flag gemini does not have")
	}
}

func TestGeminiPromptDelivery(t *testing.T) {
	if (GeminiProvider{}).PromptDelivery() != PromptArg {
		t.Error("gemini takes the prompt as an argument, not on stdin")
	}
}

func TestGeminiParseRealSuccess(t *testing.T) {
	out := readFixture(t, "gemini", "0.59.0", "success.log")
	res := GeminiProvider{}.Parse(0, out)

	if !res.Success {
		t.Fatalf("want success, got %q", res.ErrorMessage)
	}
	if res.ErrorMessage != "" {
		t.Errorf("ErrorMessage = %q, want empty", res.ErrorMessage)
	}
	// gemini prints no envelope, so there is nothing to read a number out of.
	// Spend for a gemini route is estimated downstream from pricing.yaml.
	if res.CostUSD != 0 || res.TokensIn != 0 || res.TokensOut != 0 {
		t.Errorf("cost/tokens = (%v,%d,%d), want all zero",
			res.CostUSD, res.TokensIn, res.TokensOut)
	}
	// Those zeros are "not reported", not "free": the spend row must say so,
	// or the budget and cost views read a real run as costing nothing.
	if !res.Estimated {
		t.Error("Estimated = false; gemini reports no usage, so its zeros are unknown")
	}
	// The real capture opens with a tool notice ("Ripgrep is not available…")
	// and only then the answer. A hand-written fixture would have been the
	// answer alone, and would have hidden that gemini's stdout is a
	// conversation rather than a value — which is why the failure path reads
	// the LAST non-empty line and not the first.
	if !strings.Contains(string(out), "Ripgrep is not available") {
		t.Error("the preamble is gone from the fixture; this test now proves less")
	}
}

// TestGeminiParseUntrustedDirectory is bug 30, and it is the one finding in
// this batch that stops gemini working from orch at all.
//
// gemini refuses to run in a directory it has not been told to trust, exiting
// 55 before contacting any model. orch dispatches every task into a fresh git
// worktree, which is never a trusted directory, so every gemini dispatch ends
// here — in Python too, since GeminiBackend.build_cmd passes no --skip-trust
// and sets no GEMINI_CLI_TRUST_WORKSPACE. The fix is one flag or one
// environment variable, but it also switches off a security gate on a CLI orch
// runs unattended, so it is a decision rather than a patch: see
// docs/brainstorm/go-migration-notes/sonnet-2.md.
func TestGeminiParseUntrustedDirectory(t *testing.T) {
	out := readFixture(t, "gemini", "0.59.0", "untrusted-directory.log")
	res := GeminiProvider{}.Parse(55, out)

	if res.Success {
		t.Fatal("want failure")
	}
	if !strings.Contains(res.ErrorMessage, "not running in a trusted directory") {
		t.Errorf("ErrorMessage = %q", res.ErrorMessage)
	}
	// Nothing marks this as unretryable, so the reaper will try again and get
	// exit 55 again. Pinned so that changing it is deliberate.
	if got := Classify(res); got != FailureOther {
		t.Errorf("Classify = %q, want %q — if this changed, bug 30 moved",
			got, FailureOther)
	}
}

// TestGeminiParseRealAuthError reads a real gemini 0.59.0 run with no
// credentials configured. Its exit code is 41, not 1, which is why success is
// read from "exit != 0" rather than from any particular code.
func TestGeminiParseRealAuthError(t *testing.T) {
	out := readFixture(t, "gemini", "0.59.0", "auth-error.log")
	res := GeminiProvider{}.Parse(41, out)

	if res.Success {
		t.Fatal("want failure")
	}
	if res.ExitCode != 41 {
		t.Errorf("ExitCode = %d, want 41", res.ExitCode)
	}
	if !strings.Contains(res.ErrorMessage, "Please set an Auth method") {
		t.Errorf("ErrorMessage = %q, want the CLI's own line", res.ErrorMessage)
	}

	// Pinned, not endorsed. The sentence gemini prints — "Please set an Auth
	// method ... or specify one of the following environment variables" —
	// contains none of the permission markers, so a missing credential is
	// classified as FailureOther and retried like a transient blip. It cannot
	// succeed on retry. Adding a marker is a behaviour change for every
	// backend at once, so it is logged in
	// docs/brainstorm/go-migration-notes/sonnet-2.md rather than slipped in
	// here; if this assertion starts failing, that note is what to update.
	if got := Classify(res); got != FailureOther {
		t.Errorf("Classify = %q, want %q — if this now says permission, a "+
			"marker was added and the note needs closing", got, FailureOther)
	}
}

func TestGeminiParseEmptyOutput(t *testing.T) {
	res := GeminiProvider{}.Parse(2, nil)
	if res.Success {
		t.Fatal("want failure")
	}
	if res.ErrorMessage != "gemini exited with code 2" {
		t.Errorf("ErrorMessage = %q", res.ErrorMessage)
	}
}

func TestGeminiExtractCostIsAlwaysZero(t *testing.T) {
	out := readFixture(t, "gemini", "0.59.0", "auth-error.log")
	cost, in, outTok := GeminiProvider{}.ExtractCost(out)
	if cost != 0 || in != 0 || outTok != 0 {
		t.Errorf("ExtractCost = (%v,%d,%d), want zeros", cost, in, outTok)
	}
}

func TestLastNonEmptyLine(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"\n\n  \n", ""},
		{"one", "one"},
		{"one\ntwo\n", "two"},
		{"one\ntwo\n\n   \n", "two"},
		{"one\r\ntwo\r\n", "two"},
	}
	for _, tt := range tests {
		if got := lastNonEmptyLine(tt.in); got != tt.want {
			t.Errorf("lastNonEmptyLine(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
