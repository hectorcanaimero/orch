package providers

import (
	"regexp"
	"strings"
)

// Failure is the coarse category of a failed dispatch, and is what the reap
// loop's retry policy keys on. Ported from dispatcher.py's FailureClass; the
// string values are the same lower-snake ones, because they are written into
// event rows and read by the dashboard.
type Failure string

const (
	FailureIDSpoof      Failure = "id_spoof"      // agent called task-finish.sh with the wrong id
	FailureTimeout      Failure = "timeout"       // exceeded estimateHours * multiplier
	FailureRateLimit    Failure = "rate_limit"    // 429 / "rate limit" / "too many requests"
	FailurePermission   Failure = "permission"    // auth denied — retrying will not help
	FailureBudget       Failure = "budget"        // spend cap hit — retrying burns more
	FailureVersionDrift Failure = "version_drift" // backend rejected the requested cli_model
	FailureTransient    Failure = "transient"     // 5xx / UnknownError / err_xxxxxxxx
	FailureParser       Failure = "parser"        // our own Parse could not read the output
	FailureOther        Failure = "other"         // unknown — conservative default, retry once
)

// The marker tables below are copied verbatim from dispatcher.py. Do not
// "tidy" them: a dropped substring silently reclassifies a real failure, and
// the only thing standing between that and production is classify_test.go.

// versionDriftMarkers is evidence the backend rejected the requested
// cli_model. Kept broad on purpose — a false positive costs one cheap
// fallback retry, a false negative leaves a task blocked.
var versionDriftMarkers = []string{
	"model not found",
	"unknown model",
	"no such model",
	"invalid model",
	"model does not exist",
	"unsupported model",
}

// These two live in Result.ErrorMessage only — never in CLI output — so they
// are matched against that field alone, before the haystack is even built.
const (
	idSpoofMarker = "id spoofing detected"
	timeoutMarker = "orchestrator timeout"
)

var rateLimitMarkers = []string{
	"429",
	"rate limit",
	"too many requests",
}

var permissionMarkers = []string{
	"permission denied",
	"not authorized",
	"unauthorized",
	"auth error",
	"authentication failed",
	"401",
	"403",
}

var budgetMarkers = []string{
	"max budget",
	"budget exceeded",
	"over budget",
}

var transientMarkers = []string{
	"500",
	"502",
	"503",
	"504",
	"internal server error",
	"bad gateway",
	"service unavailable",
	"gateway timeout",
	"unknownerror",
	"connection reset",
	"connection dropped",
	"econnreset",
}

// errCodeRe catches opencode/opencodego's `err_xxxxxxxx` correlation ids.
var errCodeRe = regexp.MustCompile(`(?i)\berr_[0-9a-f]{6,}\b`)

var parserMarkers = []string{
	"could not parse",
	"produced no jsonl events",
	"produced no events",
	"parse_result raised",
}

// classifyHaystackChars caps how much of Stdout feeds classification. Python
// slices `result.stdout[-2048:]`, which on a Python str is 2048 *characters*,
// so this counts runes rather than bytes.
const classifyHaystackChars = 2048

// classifyHaystack concatenates ErrorMessage, Stderr and the tail of Stdout,
// lowercased, as one string so the marker scans are a single pass.
func classifyHaystack(res Result) string {
	parts := make([]string, 0, 3)
	if res.ErrorMessage != "" {
		parts = append(parts, res.ErrorMessage)
	}
	if res.Stderr != "" {
		parts = append(parts, res.Stderr)
	}
	if res.Stdout != "" {
		parts = append(parts, lastRunes(res.Stdout, classifyHaystackChars))
	}
	return strings.ToLower(strings.Join(parts, "\n"))
}

// lastRunes returns the final n runes of s, mirroring Python's s[-n:].
func lastRunes(s string, n int) string {
	// Fast path: an all-ASCII string that is already short enough, which is
	// every log this is called on that has not blown past the cap.
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}

// Classify maps a failed Result to a Failure. Pure: no I/O, no mutation.
//
// Order matters and is the Python's order. Checks keyed on markers orch sets
// itself (ErrorMessage) run first, then specific CLI-output markers, then
// generic ones, then the conservative default. Callers pass any Result;
// successful ones are not special-cased, because the reap loop only asks about
// failures.
//
// Divergence from the G2.4 brief, which sketched this as a per-provider
// `Classify(exit int, stderr []byte)` method: in Python it is one module-level
// function shared by every backend, and it reads ErrorMessage and the stdout
// tail, not just stderr. Splitting it per provider would invite five copies
// that drift, and dropping ErrorMessage would break the two highest-priority
// classes outright — both ID_SPOOF and TIMEOUT are set by orch, never printed
// by a CLI.
func Classify(res Result) Failure {
	// 1) ID_SPOOF — set by the post-run checks; beats anything the CLI said.
	if res.ErrorMessage != "" && strings.Contains(strings.ToLower(res.ErrorMessage), idSpoofMarker) {
		return FailureIDSpoof
	}

	// 2) TIMEOUT — set by the engine's wait path.
	if res.ErrorMessage != "" && strings.Contains(strings.ToLower(res.ErrorMessage), timeoutMarker) {
		return FailureTimeout
	}

	blob := classifyHaystack(res)

	switch {
	// 3) RATE_LIMIT — specific, and checked before the generic 5xx below.
	case containsAny(blob, rateLimitMarkers):
		return FailureRateLimit
	// 4) PERMISSION — auth errors are terminal; no retry.
	case containsAny(blob, permissionMarkers):
		return FailurePermission
	// 5) BUDGET — spend cap hit; no retry.
	case containsAny(blob, budgetMarkers):
		return FailureBudget
	// 6) VERSION_DRIFT — triggers the one fallback-model retry.
	case containsAny(blob, versionDriftMarkers):
		return FailureVersionDrift
	// 7) TRANSIENT — server-side hiccups a plain retry usually clears.
	case containsAny(blob, transientMarkers), errCodeRe.MatchString(blob):
		return FailureTransient
	// 8) PARSER — our own give-up strings. After TRANSIENT on purpose, so a
	//    "parse failed after 500" leans transient.
	case containsAny(blob, parserMarkers):
		return FailureParser
	}

	// 9) Conservative default — retry once, same model, standard backoff.
	return FailureOther
}

// IsVersionDrift reports whether res looks like the backend rejected the
// cli_model, i.e. whether the reap loop should retry with
// RouteEntry.FallbackCLIModel. Port of is_version_drift_error.
func IsVersionDrift(res Result) bool { return Classify(res) == FailureVersionDrift }

func containsAny(haystack string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(haystack, n) {
			return true
		}
	}
	return false
}
