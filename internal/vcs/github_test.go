package vcs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---- CreatePR ---------------------------------------------------------

func TestGitHubCreatePRReturnsURL(t *testing.T) {
	withFakeBin(t)
	log := ghLog(t)
	t.Setenv("FAKE_GH_CREATE_STDOUT", "https://github.com/org/repo/pull/42\n")

	url, err := NewGitHubProvider().CreatePR("orch/t1", "main", "My PR", "body text")
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://github.com/org/repo/pull/42" {
		t.Errorf("url = %q", url)
	}

	got := readLog(t, log)
	for _, want := range []string{"pr create", "--title My PR", "--head orch/t1", "--base main"} {
		if !strings.Contains(got, want) {
			t.Errorf("gh invocation missing %q, got: %s", want, got)
		}
	}
}

func TestGitHubCreatePRReturnsEmptyOnOrdinaryFailure(t *testing.T) {
	withFakeBin(t)
	t.Setenv("FAKE_GH_CREATE_EXIT", "1")
	t.Setenv("FAKE_GH_AUTH_EXIT", "0") // authenticated — this is an ordinary failure

	url, err := NewGitHubProvider().CreatePR("t1", "main", "title", "body")
	if err != nil {
		t.Fatalf("expected no error for an ordinary (non-auth) failure, got %v", err)
	}
	if url != "" {
		t.Errorf("url = %q, want empty", url)
	}
}

func TestGitHubCreatePRReturnsCLIErrorWhenNotAuthenticated(t *testing.T) {
	withFakeBin(t)
	t.Setenv("FAKE_GH_CREATE_EXIT", "1")
	t.Setenv("FAKE_GH_AUTH_EXIT", "1")

	_, err := NewGitHubProvider().CreatePR("t1", "main", "title", "body")
	var cliErr *CLIError
	if !errors.As(err, &cliErr) {
		t.Fatalf("err = %v (%T), want *CLIError", err, err)
	}
	if cliErr.Reason != "not authenticated" {
		t.Errorf("Reason = %q", cliErr.Reason)
	}
}

func TestGitHubCreatePRReturnsCLIErrorWhenBinaryMissing(t *testing.T) {
	withNoBin(t)
	_, err := NewGitHubProvider().CreatePR("t1", "main", "title", "body")
	var cliErr *CLIError
	if !errors.As(err, &cliErr) {
		t.Fatalf("err = %v (%T), want *CLIError", err, err)
	}
	if cliErr.Reason != "not found in PATH" {
		t.Errorf("Reason = %q", cliErr.Reason)
	}
}

// ---- CIStatus -----------------------------------------------------------
//
// These used to feed shapes real `gh` never emits —
// `{"state":"completed","conclusion":"success"}` — written from what the
// Python code reads rather than captured from the CLI. They passed while the
// command itself could not run: `gh pr checks` has no `conclusion` field, so
// asking for one is a usage error and gh exits non-zero. The fixtures below
// are real, from `gh 2.100.0` against merged PRs of this repository, in
// testdata/gh/2.100.0/.

// realChecks loads one of the captured payloads.
func realChecks(t *testing.T, name string) string {
	t.Helper()
	// #nosec G304 -- a constant name from this file, under testdata.
	raw, err := os.ReadFile(filepath.Join("testdata", "gh", "2.100.0", name))
	if err != nil {
		t.Fatalf("read the captured fixture: %v", err)
	}
	return string(raw)
}

// TestGitHubCIStatusSuccess uses a real all-green PR. Note the UPPERCASE
// `state`: the old lowercase comparison would have missed it even if the
// command had worked.
func TestGitHubCIStatusSuccess(t *testing.T) {
	withFakeBin(t)
	t.Setenv("FAKE_GH_CHECKS_STDOUT", realChecks(t, "pr-checks-all-pass.json"))
	got, err := NewGitHubProvider().CIStatus("https://github.com/org/repo/pull/1")
	if err != nil {
		t.Fatal(err)
	}
	if got != CISuccess {
		t.Errorf("got %q, want success", got)
	}
}

// A real PR carrying a NEUTRAL check alongside eight SUCCESSes. Python maps
// `neutral` to success, so the whole PR is success — and this is the fixture
// that proves the mapping is reached at all, since every check on it is
// spelled in capitals.
func TestGitHubCIStatusNeutralAmongSuccessesIsSuccess(t *testing.T) {
	withFakeBin(t)
	body := realChecks(t, "pr-checks-with-neutral.json")
	if !strings.Contains(body, `"NEUTRAL"`) {
		t.Fatalf("the fixture no longer contains a NEUTRAL check; it is the point of this test")
	}
	t.Setenv("FAKE_GH_CHECKS_STDOUT", body)
	got, err := NewGitHubProvider().CIStatus("https://github.com/org/repo/pull/1")
	if err != nil {
		t.Fatal(err)
	}
	if got != CISuccess {
		t.Errorf("got %q, want success", got)
	}
}

// The uppercase vocabulary, one state at a time.
//
// Each row is a state `gh pr checks` can put in that field — GitHub's own
// conclusion names once a check finishes, its status names before that. The
// lowercase spellings are here too because the maps are keyed that way and
// normalising must not depend on which case arrives.
func TestGitHubCIStatusMapsGitHubsUppercaseStates(t *testing.T) {
	cases := []struct {
		state string
		want  CIState
	}{
		{"SUCCESS", CISuccess},
		{"NEUTRAL", CISuccess},
		{"SKIPPED", CISuccess},
		{"FAILURE", CIFailure},
		{"TIMED_OUT", CIFailure},
		{"ACTION_REQUIRED", CIFailure},
		// A cancelled or stale check is not a code failure: cancelling a slow
		// job to re-run it (or a run superseded by
		// `concurrency: cancel-in-progress`) must not read as CI red and
		// spend a retry while the re-run is still on its way (#252).
		{"CANCELLED", CIPending},
		{"STALE", CIPending},
		{"IN_PROGRESS", CIPending},
		{"QUEUED", CIPending},
		{"WAITING", CIPending},
		{"REQUESTED", CIPending},
		{"PENDING", CIPending},
		{"EXPECTED", CIPending},
		// A state nobody has seen is pending, not a guess in either
		// direction: orch waits rather than declaring a run green or red on
		// a word it does not know.
		{"SOMETHING_NEW", CIPending},
		// Case does not decide the answer.
		{"success", CISuccess},
		{"failure", CIFailure},
	}
	for _, tc := range cases {
		t.Run(tc.state, func(t *testing.T) {
			withFakeBin(t)
			t.Setenv("FAKE_GH_CHECKS_STDOUT",
				`[{"name":"build","state":"`+tc.state+`","bucket":"pass"}]`)
			got, err := NewGitHubProvider().CIStatus("https://github.com/org/repo/pull/1")
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("state %q -> %q, want %q", tc.state, got, tc.want)
			}
		})
	}
}

// TestGitHubCIStatusAsksForFieldsThatExist is the regression that matters, and
// it asserts on the ARGV rather than on the answer.
//
// The bug was not a wrong mapping — it was asking gh for a field it does not
// have, so the command failed and every answer became "pending" forever. A
// test that only checked the returned state could not tell that apart from a
// run still in progress, which is exactly why it went unnoticed in both
// binaries.
func TestGitHubCIStatusAsksForFieldsThatExist(t *testing.T) {
	withFakeBin(t)
	logPath := ghLog(t)
	t.Setenv("FAKE_GH_CHECKS_STDOUT", realChecks(t, "pr-checks-all-pass.json"))

	if _, err := NewGitHubProvider().CIStatus("https://github.com/org/repo/pull/1"); err != nil {
		t.Fatal(err)
	}
	argv, err := os.ReadFile(logPath) // #nosec G304 -- a path ghLog made in t.TempDir()
	if err != nil {
		t.Fatalf("read the argv log: %v", err)
	}
	if strings.Contains(string(argv), "conclusion") {
		t.Errorf("still asking gh for a `conclusion` field it does not have:\n%s", argv)
	}
	if !strings.Contains(string(argv), "state") {
		t.Errorf("not asking for `state`, which is the field that answers:\n%s", argv)
	}
}

// A gh that fails now REPORTS, where it used to answer "pending" and say
// nothing. Swallowing it is what hid a command that could never succeed: a
// run whose CI never resolves looked exactly like one still waiting.
func TestGitHubCIStatusReportsAGhFailure(t *testing.T) {
	withFakeBin(t)
	t.Setenv("FAKE_GH_CHECKS_EXIT", "1")
	got, err := NewGitHubProvider().CIStatus("https://github.com/org/repo/pull/1")
	if err == nil {
		t.Error("a failing gh was reported as a clean pending")
	}
	// Still pending as the state, so a caller that ignores the error keeps
	// the safe answer rather than a zero value.
	if got != CIPending {
		t.Errorf("got %q, want pending alongside the error", got)
	}
}

func TestGitHubCIStatusReportsMalformedJSON(t *testing.T) {
	withFakeBin(t)
	t.Setenv("FAKE_GH_CHECKS_STDOUT", `not json`)
	got, err := NewGitHubProvider().CIStatus("https://github.com/org/repo/pull/1")
	if err == nil {
		t.Error("unparseable output was reported as a clean pending")
	}
	if got != CIPending {
		t.Errorf("got %q, want pending alongside the error", got)
	}
}

// No checks is NOT an error, and it is not pending either: a PR opened a
// second ago has none, but so does one on a repo with no workflow, and only
// the second never changes (#233). CINone lets the poller tell them apart by
// how long it lasts.
func TestGitHubCIStatusNoneOnEmptyChecks(t *testing.T) {
	withFakeBin(t)
	t.Setenv("FAKE_GH_CHECKS_STDOUT", `[]`)
	t.Setenv("FAKE_GH_VIEW_STDOUT", `{"mergeable":"MERGEABLE"}`)
	got, err := NewGitHubProvider().CIStatus("https://github.com/org/repo/pull/1")
	if err != nil {
		t.Fatal(err)
	}
	if got != CINone {
		t.Errorf("got %q, want none", got)
	}
}

// What real gh does with no checks: exit 1 and say so on stderr (#233's log:
// `no checks reported on the 'orch/F3.3.T1' branch`). That is an answer, not
// a failure to read one.
func TestGitHubCIStatusNoChecksReportedIsNone(t *testing.T) {
	withFakeBin(t)
	t.Setenv("FAKE_GH_CHECKS_EXIT", "1")
	t.Setenv("FAKE_GH_CHECKS_STDERR", "no checks reported on the 'orch/T-1' branch\n")
	t.Setenv("FAKE_GH_VIEW_STDOUT", `{"mergeable":"UNKNOWN"}`)
	got, err := NewGitHubProvider().CIStatus("https://github.com/org/repo/pull/1")
	if err != nil {
		t.Fatal(err)
	}
	if got != CINone {
		t.Errorf("got %q, want none", got)
	}
}

// GitHub runs no workflow on a conflicting PR, so its missing checks will
// never arrive (#236). Saying so is what keeps it from being finished as a
// repo without CI.
func TestGitHubCIStatusConflictWhenNoChecksAndConflicting(t *testing.T) {
	withFakeBin(t)
	t.Setenv("FAKE_GH_CHECKS_STDOUT", `[]`)
	t.Setenv("FAKE_GH_VIEW_STDOUT", `{"mergeable":"CONFLICTING"}`)
	got, err := NewGitHubProvider().CIStatus("https://github.com/org/repo/pull/1")
	if err != nil {
		t.Fatal(err)
	}
	if got != CIConflict {
		t.Errorf("got %q, want conflict", got)
	}
}

// If the mergeable state cannot be read, "none" would let the poller finish a
// PR that may be conflicting. Pending with the error is the safe answer.
func TestGitHubCIStatusNoChecksAndUnreadableMergeableIsPending(t *testing.T) {
	withFakeBin(t)
	t.Setenv("FAKE_GH_CHECKS_STDOUT", `[]`)
	t.Setenv("FAKE_GH_VIEW_EXIT", "1")
	got, err := NewGitHubProvider().CIStatus("https://github.com/org/repo/pull/1")
	if err == nil {
		t.Error("an unreadable mergeable state was not reported")
	}
	if got != CIPending {
		t.Errorf("got %q, want pending alongside the error", got)
	}
}

func TestGitHubCIStatusReturnsCLIErrorWhenBinaryMissing(t *testing.T) {
	withNoBin(t)
	_, err := NewGitHubProvider().CIStatus("https://github.com/org/repo/pull/1")
	var cliErr *CLIError
	if !errors.As(err, &cliErr) {
		t.Fatalf("err = %v (%T), want *CLIError", err, err)
	}
}

// ---- PRState ------------------------------------------------------------

// TestGitHubPRStateReadsRealCaptures: one real PR of this repository per
// state, from gh 2.100.0 (#255).
func TestGitHubPRStateReadsRealCaptures(t *testing.T) {
	cases := []struct {
		file string
		want PRState
	}{
		{"pr-view-state-open.json", PROpen},
		{"pr-view-state-merged.json", PRMerged},
		{"pr-view-state-closed.json", PRClosed},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			withFakeBin(t)
			log := ghLog(t)
			t.Setenv("FAKE_GH_VIEW_STDOUT", realChecks(t, tc.file))

			got, err := NewGitHubProvider().PRState("https://github.com/org/repo/pull/1")
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
			if argv := readLog(t, log); !strings.Contains(argv, "pr view https://github.com/org/repo/pull/1 --json state") {
				t.Errorf("gh invocation = %q", argv)
			}
		})
	}
}

func TestGitHubPRStateReportsFailures(t *testing.T) {
	cases := map[string]map[string]string{
		"gh fails":           {"FAKE_GH_VIEW_EXIT": "1"},
		"malformed JSON":     {"FAKE_GH_VIEW_STDOUT": "not json"},
		"an unknown state":   {"FAKE_GH_VIEW_STDOUT": `{"state":"DRAFT"}`},
		"no state in answer": {"FAKE_GH_VIEW_STDOUT": `{}`},
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			withFakeBin(t)
			for k, v := range env {
				t.Setenv(k, v)
			}
			if got, err := NewGitHubProvider().PRState("https://github.com/org/repo/pull/1"); err == nil {
				t.Errorf("got %q with no error", got)
			}
		})
	}
}

func TestGitHubPRStateReturnsCLIErrorWhenBinaryMissing(t *testing.T) {
	withNoBin(t)
	_, err := NewGitHubProvider().PRState("https://github.com/org/repo/pull/1")
	var cliErr *CLIError
	if !errors.As(err, &cliErr) {
		t.Fatalf("err = %v (%T), want *CLIError", err, err)
	}
}

// ---- CILogs ---------------------------------------------------------------

func TestGitHubCILogsReturnsFailedRunOutput(t *testing.T) {
	withFakeBin(t)
	t.Setenv("FAKE_GH_VIEW_STDOUT", `{"statusCheckRollup":[{"conclusion":"failure","detailsUrl":"https://github.com/org/repo/actions/runs/99999/jobs/1"}]}`)
	t.Setenv("FAKE_GH_RUNVIEW_STDOUT", strings.Repeat("ERROR: test failed\n", 10))

	logs, err := NewGitHubProvider().CILogs("https://github.com/org/repo/pull/1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs, "ERROR") {
		t.Errorf("logs = %q", logs)
	}
}

func TestGitHubCILogsTruncatesTo8000Chars(t *testing.T) {
	withFakeBin(t)
	t.Setenv("FAKE_GH_VIEW_STDOUT", `{"statusCheckRollup":[{"conclusion":"failure","detailsUrl":"https://github.com/org/repo/actions/runs/1/jobs/1"}]}`)
	t.Setenv("FAKE_GH_RUNVIEW_STDOUT", strings.Repeat("x", 20000))

	logs, err := NewGitHubProvider().CILogs("https://github.com/org/repo/pull/1")
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) > 8000 {
		t.Errorf("len(logs) = %d, want <= 8000", len(logs))
	}
}

func TestGitHubCILogsEmptyOnNoFailures(t *testing.T) {
	withFakeBin(t)
	t.Setenv("FAKE_GH_VIEW_STDOUT", `{"statusCheckRollup":[{"conclusion":"success","detailsUrl":""}]}`)

	logs, err := NewGitHubProvider().CILogs("https://github.com/org/repo/pull/1")
	if err != nil {
		t.Fatal(err)
	}
	if logs != "" {
		t.Errorf("logs = %q, want empty", logs)
	}
}

// ---- MergePR (Sprint G-1) ---------------------------------------------

func TestGitHubMergePRPassesSquashAndAuto(t *testing.T) {
	withFakeBin(t)
	log := ghLog(t)

	if err := NewGitHubProvider().MergePR("https://github.com/org/repo/pull/1", true, true); err != nil {
		t.Fatal(err)
	}
	got := readLog(t, log)
	for _, want := range []string{"pr merge", "--squash", "--auto", "https://github.com/org/repo/pull/1"} {
		if !strings.Contains(got, want) {
			t.Errorf("gh invocation missing %q, got: %s", want, got)
		}
	}
}

func TestGitHubMergePROmitsFlagsWhenFalse(t *testing.T) {
	withFakeBin(t)
	log := ghLog(t)

	if err := NewGitHubProvider().MergePR("https://github.com/org/repo/pull/1", false, false); err != nil {
		t.Fatal(err)
	}
	got := readLog(t, log)
	if strings.Contains(got, "--squash") || strings.Contains(got, "--auto") {
		t.Errorf("expected no --squash/--auto, got: %s", got)
	}
}

func TestGitHubMergePRReturnsErrMergeFailedOnOrdinaryFailure(t *testing.T) {
	withFakeBin(t)
	t.Setenv("FAKE_GH_MERGE_EXIT", "1")
	t.Setenv("FAKE_GH_AUTH_EXIT", "0")

	err := NewGitHubProvider().MergePR("https://github.com/org/repo/pull/1", true, false)
	if !errors.Is(err, ErrMergeFailed) {
		t.Fatalf("err = %v, want ErrMergeFailed", err)
	}
}

func TestGitHubMergePRReturnsCLIErrorWhenNotAuthenticated(t *testing.T) {
	withFakeBin(t)
	t.Setenv("FAKE_GH_MERGE_EXIT", "1")
	t.Setenv("FAKE_GH_AUTH_EXIT", "1")

	err := NewGitHubProvider().MergePR("https://github.com/org/repo/pull/1", true, false)
	var cliErr *CLIError
	if !errors.As(err, &cliErr) {
		t.Fatalf("err = %v (%T), want *CLIError", err, err)
	}
}
