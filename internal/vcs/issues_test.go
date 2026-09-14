package vcs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// capturedIssueList is the real `gh issue list` payload — see
// testdata/gh/README.md for the exact command it came from. The tests read
// the file rather than a literal: a struct tested against JSON written from
// that same struct proves only that the struct agrees with itself, which is
// how bug 27 (a field gh never had) survived a green suite.
const capturedIssueList = "testdata/gh/2.100.0/issue-list.json"

func TestListIssuesReadsCapturedPayload(t *testing.T) {
	withFakeBin(t)
	abs, err := filepath.Abs(capturedIssueList)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_GH_ISSUES_FILE", abs)

	issues, err := NewGitHubProvider().ListIssues(IssueQuery{Label: "auto-reported", State: "all"})
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	if len(issues) != 3 {
		t.Fatalf("got %d issues, want 3", len(issues))
	}

	first := issues[0]
	if first.Number != 88 {
		t.Errorf("Number = %d, want 88", first.Number)
	}
	if first.State != "CLOSED" {
		// UPPERCASE, like `gh pr checks --json state`. Asserted so a future
		// reader does not lowercase-compare it the way bug 27 did.
		t.Errorf("State = %q, want CLOSED (gh returns it uppercase)", first.State)
	}
	if first.URL != "https://github.com/hectorcanaimero/orch/issues/88" {
		t.Errorf("URL = %q", first.URL)
	}
	if first.CreatedAt != "2026-08-29T23:38:42Z" {
		t.Errorf("CreatedAt = %q", first.CreatedAt)
	}
	if !strings.Contains(first.Title, "AgyBackend") {
		t.Errorf("Title = %q", first.Title)
	}
	if !strings.Contains(first.Body, "## Feature request") {
		t.Errorf("Body did not survive the round trip: %.60q", first.Body)
	}
	if got := first.LabelNames(); len(got) != 1 || got[0] != "auto-reported" {
		t.Errorf("LabelNames() = %v, want [auto-reported]", got)
	}
}

func TestListIssuesAsksGHForFieldsItHas(t *testing.T) {
	withFakeBin(t)
	log := ghLog(t)
	t.Setenv("FAKE_GH_ISSUES_STDOUT", "[]")

	if _, err := NewGitHubProvider().ListIssues(IssueQuery{Label: "orch:task"}); err != nil {
		t.Fatalf("ListIssues: %v", err)
	}

	argv := strings.TrimSpace(readLog(t, log))
	for _, want := range []string{
		"issue list",
		"--label orch:task",
		"--state open", // the default: a sync must not reopen closed work
		"--limit 500",  // gh's own default of 30 truncates silently
		"--json number,title,body,state,url,createdAt,labels",
	} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv %q does not contain %q", argv, want)
		}
	}
}

// TestIssueListFieldsAllExistInTheCapture is bug 27's OTHER half: the fix
// there was not just reporting the failure but asking gh only for fields it
// has. Asserting the constant against a real payload is the check that would
// have caught `conclusion` before it shipped — a hand-written literal proves
// nothing, since the same wrong belief writes both sides.
func TestIssueListFieldsAllExistInTheCapture(t *testing.T) {
	b, err := os.ReadFile(capturedIssueList)
	if err != nil {
		t.Fatal(err)
	}
	var raw []map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 {
		t.Fatal("capture is empty")
	}
	for _, field := range strings.Split(issueListFields, ",") {
		if _, ok := raw[0][field]; !ok {
			t.Errorf("gh did not return %q; asking for it makes the whole command a usage error", field)
		}
	}
}

func TestListIssuesPassesStateAndLimitThrough(t *testing.T) {
	withFakeBin(t)
	log := ghLog(t)
	t.Setenv("FAKE_GH_ISSUES_STDOUT", "[]")

	_, err := NewGitHubProvider().ListIssues(IssueQuery{Label: "orch:task", State: "all", Limit: 7})
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	argv := readLog(t, log)
	if !strings.Contains(argv, "--state all") || !strings.Contains(argv, "--limit 7") {
		t.Errorf("argv did not carry the query: %s", argv)
	}
}

func TestListIssuesRequiresALabel(t *testing.T) {
	withFakeBin(t)
	if _, err := NewGitHubProvider().ListIssues(IssueQuery{}); err == nil {
		t.Fatal("listing with no label succeeded; it would ingest a whole tracker")
	}
}

// TestListIssuesReportsAFailedCommand is bug 27's shape, asserted so it
// cannot come back here: `gh pr checks` failed on every call for months
// because the failure was swallowed into a plausible-looking value.
func TestListIssuesReportsAFailedCommand(t *testing.T) {
	withFakeBin(t)
	t.Setenv("FAKE_GH_ISSUES_EXIT", "1")
	t.Setenv("FAKE_GH_ISSUES_STDOUT", "")

	issues, err := NewGitHubProvider().ListIssues(IssueQuery{Label: "orch:task"})
	if err == nil {
		t.Fatalf("a failing gh returned no error (issues=%v); an empty sync would look like an up-to-date one", issues)
	}
	if !strings.Contains(err.Error(), "orch:task") {
		t.Errorf("error does not say what was asked for: %v", err)
	}
}

// An unmatched label is `[]`, not an error — checked against the real gh
// (`gh issue list --label nonexistent` on this repo prints `[]` and exits 0),
// which is why nothing here treats "no issues" as a problem.
func TestListIssuesEmptyResultIsNotAnError(t *testing.T) {
	withFakeBin(t)
	t.Setenv("FAKE_GH_ISSUES_STDOUT", "[]")

	issues, err := NewGitHubProvider().ListIssues(IssueQuery{Label: "no-such-label"})
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	if len(issues) != 0 {
		t.Errorf("got %d issues, want none", len(issues))
	}
}

func TestListIssuesNeedsGH(t *testing.T) {
	withNoBin(t)
	if _, err := NewGitHubProvider().ListIssues(IssueQuery{Label: "orch:task"}); err == nil {
		t.Fatal("ListIssues succeeded with no gh on PATH")
	}
}

// SearchIssues reads the same captured payload shape, and asks gh for the
// repo, the label, every state and a title search.
func TestSearchIssuesAsksForTheRepoLabelAndTitle(t *testing.T) {
	withFakeBin(t)
	log := ghLog(t)
	abs, err := filepath.Abs(capturedIssueList)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_GH_ISSUES_FILE", abs)

	issues, err := NewGitHubProvider().SearchIssues("o/r", "auto-reported", "CI poller waits", 10)
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	if len(issues) != 3 {
		t.Errorf("got %d issues, want the 3 in the capture", len(issues))
	}
	got := readLog(t, log)
	for _, want := range []string{"issue list", "--repo o/r", "--label auto-reported", "--state all", "--search CI poller waits in:title", "--limit 10"} {
		if !strings.Contains(got, want) {
			t.Errorf("gh invocation missing %q, got: %s", want, got)
		}
	}
}

func TestCreateIssueReturnsTheURL(t *testing.T) {
	withFakeBin(t)
	log := ghLog(t)
	t.Setenv("FAKE_GH_ISSUE_CREATE_STDOUT", "https://github.com/o/r/issues/7\n")

	url, err := NewGitHubProvider().CreateIssue("o/r", "A title", "A body", []string{"auto-reported"})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if url != "https://github.com/o/r/issues/7" {
		t.Errorf("url = %q", url)
	}
	got := readLog(t, log)
	for _, want := range []string{"issue create", "--repo o/r", "--title A title", "--body A body", "--label auto-reported"} {
		if !strings.Contains(got, want) {
			t.Errorf("gh invocation missing %q, got: %s", want, got)
		}
	}
}

func TestCreateIssueReportsAFailure(t *testing.T) {
	withFakeBin(t)
	t.Setenv("FAKE_GH_ISSUE_CREATE_EXIT", "1")
	t.Setenv("FAKE_GH_AUTH_EXIT", "0")
	if _, err := NewGitHubProvider().CreateIssue("o/r", "t", "b", nil); err == nil {
		t.Error("a failing gh issue create was reported as filed")
	}
}
