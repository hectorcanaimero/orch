package providers

import (
	"strings"
	"testing"
)

// The cases below are a port of orchestrator/tests/test_classify_failure.py,
// which is the spec for this function. Keep them in step: if a case is added
// there, add it here.

func fail(errorMessage, stderr, stdout string) Result {
	return Result{
		ExitCode:     1,
		Success:      false,
		Stdout:       stdout,
		Stderr:       stderr,
		ErrorMessage: errorMessage,
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		res  Result
		want Failure
	}{
		// ID_SPOOF — checked first, set by orch and never by a CLI.
		{
			name: "id spoof marker",
			res:  fail("id spoofing detected: agent called task-finish.sh 'B-999'", "", ""),
			want: FailureIDSpoof,
		},
		{
			name: "id spoof wins over a rate limit in stderr",
			res:  fail("id spoofing detected: task-finish.sh 'X'", "429 rate limit exceeded", ""),
			want: FailureIDSpoof,
		},
		{
			name: "id spoof beats a timeout marker in the same message",
			res:  fail("id spoofing detected: orchestrator timeout", "", ""),
			want: FailureIDSpoof,
		},

		// TIMEOUT — checked second, also orch-set.
		{
			name: "timeout marker from the wait path",
			res:  fail("orchestrator timeout after 300s", "", ""),
			want: FailureTimeout,
		},
		{
			name: "timeout marker with no suffix, as the reap loop writes it",
			res:  fail("orchestrator timeout", "", ""),
			want: FailureTimeout,
		},
		{
			name: "timeout beats a stale rate-limit line in the log",
			res:  fail("orchestrator timeout after 300s", "429 rate limit exceeded", ""),
			want: FailureTimeout,
		},

		// RATE_LIMIT.
		{name: "429 in stderr", res: fail("", "HTTP 429: too many requests to the API", ""), want: FailureRateLimit},
		{name: "rate limit phrase in stdout", res: fail("", "", "Rate limit exceeded, please retry later"), want: FailureRateLimit},
		{name: "too many requests", res: fail("", "429 Too Many Requests", ""), want: FailureRateLimit},
		{name: "matching is case insensitive", res: fail("", "RATE LIMIT reached", ""), want: FailureRateLimit},
		{
			name: "rate limit beats transient when both appear",
			res:  fail("", "429 Too Many Requests (upstream returned 500)", ""),
			want: FailureRateLimit,
		},

		// PERMISSION.
		{name: "permission denied", res: fail("", "permission denied: cannot access /secret", ""), want: FailurePermission},
		{name: "not authorized", res: fail("Not authorized to use this endpoint", "", ""), want: FailurePermission},
		{name: "auth error with a 401", res: fail("", "401 auth error: token invalid", ""), want: FailurePermission},
		{name: "permission beats transient", res: fail("", "permission denied (500 upstream)", ""), want: FailurePermission},

		// BUDGET.
		{name: "max budget", res: fail("", "Reached max budget of $5.00", ""), want: FailureBudget},
		{name: "budget exceeded", res: fail("budget exceeded during execution", "", ""), want: FailureBudget},
		{name: "over budget", res: fail("", "run stopped: over budget", ""), want: FailureBudget},

		// VERSION_DRIFT.
		{name: "model not found", res: fail("model not found: zhipu/glm-9.9", "", ""), want: FailureVersionDrift},
		{name: "unknown model", res: fail("", "unknown model: foo", ""), want: FailureVersionDrift},
		{name: "model does not exist", res: fail("model does not exist", "", ""), want: FailureVersionDrift},
		{name: "drift beats transient", res: fail("", "model not found (500 upstream)", ""), want: FailureVersionDrift},

		// TRANSIENT.
		{name: "500", res: fail("", "500 Internal Server Error from upstream", ""), want: FailureTransient},
		{name: "502", res: fail("", "502 Bad Gateway", ""), want: FailureTransient},
		{name: "UnknownError", res: fail("", "", "UnknownError: connection dropped"), want: FailureTransient},
		{name: "an opencode err_ correlation id", res: fail("", "opencode failed: err_a1b2c3d4", ""), want: FailureTransient},

		// PARSER — after TRANSIENT on purpose.
		{name: "could not parse the claude envelope", res: fail("could not parse claude JSON envelope", "", ""), want: FailureParser},
		{name: "no JSONL events from opencode", res: fail("opencode produced no JSONL events", "", ""), want: FailureParser},
		{name: "no JSONL events from codex", res: fail("codex produced no JSONL events", "", ""), want: FailureParser},
		{
			name: "a parse failure after a 500 leans transient",
			res:  fail("could not parse claude JSON envelope", "500 Internal Server Error", ""),
			want: FailureTransient,
		},

		// OTHER.
		{name: "nothing matches", res: fail("something weird happened", "", ""), want: FailureOther},
		{name: "an empty result", res: fail("", "", ""), want: FailureOther},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Classify(tt.res); got != tt.want {
				t.Errorf("Classify = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsVersionDrift(t *testing.T) {
	tests := []struct {
		name string
		res  Result
		want bool
	}{
		{name: "model does not exist", res: fail("model does not exist", "", ""), want: true},
		{name: "model not found", res: fail("model not found: xyz", "", ""), want: true},
		{name: "a transient 500 must not trigger the fallback retry", res: fail("", "500 Internal Server Error", ""), want: false},
		{name: "an empty result", res: fail("", "", ""), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsVersionDrift(tt.res); got != tt.want {
				t.Errorf("IsVersionDrift = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestClassifyIsPure(t *testing.T) {
	res := fail("", "429 rate limit", "")
	first := Classify(res)
	second := Classify(res)
	if first != second {
		t.Errorf("Classify is not deterministic: %q then %q", first, second)
	}
	if res.ErrorMessage != "" || res.Stderr != "429 rate limit" {
		t.Error("Classify must not mutate its input")
	}
}

// TestClassifyReadsOnlyTheTailOfStdout pins the 2048-character window. A
// marker further back than that is deliberately invisible: classification must
// not turn into a full scan of a multi-megabyte JSONL stream.
func TestClassifyReadsOnlyTheTailOfStdout(t *testing.T) {
	marker := "429 rate limit"
	padding := strings.Repeat("x", classifyHaystackChars)

	inWindow := fail("", "", padding[:classifyHaystackChars-len(marker)]+marker)
	if got := Classify(inWindow); got != FailureRateLimit {
		t.Errorf("marker at the very edge of the window: got %q, want %q", got, FailureRateLimit)
	}

	outOfWindow := fail("", "", marker+padding)
	if got := Classify(outOfWindow); got != FailureOther {
		t.Errorf("marker just past the window: got %q, want %q", got, FailureOther)
	}
}

// TestLastRunesCountsCharactersNotBytes guards the Python semantics of
// `stdout[-2048:]`: a multi-byte log must not be cut at a byte offset, which
// would both shorten the window and split a rune.
func TestLastRunesCountsCharactersNotBytes(t *testing.T) {
	tests := []struct {
		name string
		s    string
		n    int
		want string
	}{
		{name: "shorter than the window", s: "abc", n: 10, want: "abc"},
		{name: "exactly the window", s: "abcde", n: 5, want: "abcde"},
		{name: "longer than the window", s: "abcdef", n: 2, want: "ef"},
		{name: "multi-byte runes are counted as one each", s: "ααααα", n: 2, want: "αα"},
		{name: "a multi-byte string within the rune window is returned whole", s: "ααα", n: 3, want: "ααα"},
		{name: "empty", s: "", n: 4, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := lastRunes(tt.s, tt.n); got != tt.want {
				t.Errorf("lastRunes = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestFailureValuesAreStable pins the wire strings: they are written into
// event rows and read back by the dashboard, so renaming one is a data change,
// not a refactor.
func TestFailureValuesAreStable(t *testing.T) {
	want := map[Failure]string{
		FailureIDSpoof:      "id_spoof",
		FailureTimeout:      "timeout",
		FailureRateLimit:    "rate_limit",
		FailurePermission:   "permission",
		FailureBudget:       "budget",
		FailureVersionDrift: "version_drift",
		FailureTransient:    "transient",
		FailureParser:       "parser",
		FailureOther:        "other",
	}
	for f, s := range want {
		if string(f) != s {
			t.Errorf("Failure %v = %q, want %q", f, string(f), s)
		}
	}
}

// TestClassifyReadsStderr covers the field no provider fills today. The engine
// will, once it captures stderr separately, and this keeps that path from
// being dead on arrival.
func TestClassifyReadsStderr(t *testing.T) {
	res := Result{ExitCode: 1, Stderr: "503 service unavailable"}
	if got := Classify(res); got != FailureTransient {
		t.Errorf("Classify = %q, want %q", got, FailureTransient)
	}
}

// ---- Status codes are matched as standalone numbers ---------------------

// TestHasStatusCode is the boundary rule on its own. Python expresses it as
// `(?<![0-9.])<code>(?![0-9.])`; Go's RE2 has no lookbehind, so the rule is
// hand-rolled and this table is what keeps the two spellings in agreement.
func TestHasStatusCode(t *testing.T) {
	codes := []string{"401", "403"}

	tests := []struct {
		name string
		blob string
		want bool
	}{
		{name: "the code alone", blob: "403", want: true},
		{name: "surrounded by spaces", blob: "http 403 forbidden", want: true},
		{name: "at the start", blob: "403 forbidden", want: true},
		{name: "at the end", blob: "status was 401", want: true},
		{name: "next to punctuation", blob: `"api_error_status":403,`, want: true},
		{name: "in brackets", blob: "(401)", want: true},
		{name: "a hyphen is not a digit", blob: "err-403-x", want: true},

		{name: "glued to digits on the right", blob: `"cache_read_input_tokens":40321`},
		{name: "glued to digits on the left", blob: "duration_ms:1403"},
		{name: "glued on both sides", blob: "9403 1"},
		{name: "after a decimal point", blob: "cost 0.403"},
		{name: "before a decimal point", blob: "403.5 seconds"},
		{name: "not present", blob: "everything was fine"},
		{name: "empty", blob: ""},
		{name: "a different code", blob: "http 500"},

		// The scan advances by one rather than by len(code), so a second
		// occurrence after a rejected one is still found.
		{name: "a rejected match followed by a real one", blob: "40321 and then 403", want: true},
		{name: "two glued codes then a clean one", blob: "1401 4032 401", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasStatusCode(tt.blob, codes); got != tt.want {
				t.Errorf("hasStatusCode(%q) = %v, want %v", tt.blob, got, tt.want)
			}
		})
	}
}

// TestClassifyStatusCodesNeedABoundary walks the rule up through Classify,
// where it actually matters: a token count that merely contains a status
// code must not decide a task's retry policy.
func TestClassifyStatusCodesNeedABoundary(t *testing.T) {
	tests := []struct {
		name string
		res  Result
		want Failure
	}{
		{
			name: "a real 429 still classifies",
			res:  fail("", "http 429 returned", ""),
			want: FailureRateLimit,
		},
		{
			name: "a token count containing 429 does not",
			res:  fail("", "", `{"input_tokens":14290,"output_tokens":7}`),
			want: FailureOther,
		},
		{
			name: "a real 403 still classifies",
			res:  fail("", `{"api_error_status":403}`, ""),
			want: FailurePermission,
		},
		{
			name: "a token count containing 403 does not",
			res:  fail("", "", `{"cache_read_input_tokens":40321}`),
			want: FailureOther,
		},
		{
			name: "a real 500 still classifies",
			res:  fail("", "upstream returned 500", ""),
			want: FailureTransient,
		},
		{
			name: "a duration containing 500 does not",
			res:  fail("", "", `{"duration_ms":10500,"ttft_ms":2500}`),
			want: FailureOther,
		},
		{
			name: "a cost containing 504 does not",
			res:  fail("", "", `{"total_cost_usd":0.0504}`),
			want: FailureOther,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Classify(tt.res); got != tt.want {
				t.Errorf("Classify = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestClassifyRealAuthSpellings covers the three auth phrasings the CLIs
// actually emit, none of which is a substring of the two original markers.
func TestClassifyRealAuthSpellings(t *testing.T) {
	tests := []struct {
		name string
		blob string
	}{
		{name: "claude's error code", blob: `{"code":"authentication_error","message":"Invalid API key"}`},
		{name: "the spaced spelling", blob: "authentication error: token rejected"},
		{name: "codex's expiry", blob: "OpenAI API auth expired; run codex auth"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Classify(fail("", tt.blob, "")); got != FailurePermission {
				t.Errorf("Classify = %q, want %q", got, FailurePermission)
			}
		})
	}
}

// TestClassifyVersionDriftRealPhrasings covers the two markers added for
// claude 2.1.269 alongside the six historical ones.
func TestClassifyVersionDriftRealPhrasings(t *testing.T) {
	tests := []struct {
		name string
		blob string
	}{
		{name: "the rejection sentence", blob: "It may not exist or you may not have access to it."},
		{name: "the stderr marker", blob: "[claude-code:unrecognized_model] {\"model\":\"x\"}"},
		{name: "a historical marker still works", blob: "model not found: zhipu/glm-9.9"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Classify(fail("", tt.blob, "")); got != FailureVersionDrift {
				t.Errorf("Classify = %q, want %q", got, FailureVersionDrift)
			}
		})
	}
}
