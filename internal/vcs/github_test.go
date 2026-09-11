package vcs

import (
	"errors"
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

func TestGitHubCIStatusSuccess(t *testing.T) {
	withFakeBin(t)
	t.Setenv("FAKE_GH_CHECKS_STDOUT", `[{"state":"completed","conclusion":"success"}]`)
	got, err := NewGitHubProvider().CIStatus("https://github.com/org/repo/pull/1")
	if err != nil {
		t.Fatal(err)
	}
	if got != CISuccess {
		t.Errorf("got %q, want success", got)
	}
}

func TestGitHubCIStatusFailure(t *testing.T) {
	withFakeBin(t)
	t.Setenv("FAKE_GH_CHECKS_STDOUT", `[{"state":"completed","conclusion":"failure"}]`)
	got, err := NewGitHubProvider().CIStatus("https://github.com/org/repo/pull/1")
	if err != nil {
		t.Fatal(err)
	}
	if got != CIFailure {
		t.Errorf("got %q, want failure", got)
	}
}

func TestGitHubCIStatusPendingWhenInProgress(t *testing.T) {
	withFakeBin(t)
	t.Setenv("FAKE_GH_CHECKS_STDOUT", `[{"state":"in_progress","conclusion":""}]`)
	got, err := NewGitHubProvider().CIStatus("https://github.com/org/repo/pull/1")
	if err != nil {
		t.Fatal(err)
	}
	if got != CIPending {
		t.Errorf("got %q, want pending", got)
	}
}

func TestGitHubCIStatusPendingOnGhError(t *testing.T) {
	withFakeBin(t)
	t.Setenv("FAKE_GH_CHECKS_EXIT", "1")
	got, err := NewGitHubProvider().CIStatus("https://github.com/org/repo/pull/1")
	if err != nil {
		t.Fatal(err)
	}
	if got != CIPending {
		t.Errorf("got %q, want pending", got)
	}
}

func TestGitHubCIStatusPendingOnEmptyChecks(t *testing.T) {
	withFakeBin(t)
	t.Setenv("FAKE_GH_CHECKS_STDOUT", `[]`)
	got, err := NewGitHubProvider().CIStatus("https://github.com/org/repo/pull/1")
	if err != nil {
		t.Fatal(err)
	}
	if got != CIPending {
		t.Errorf("got %q, want pending", got)
	}
}

func TestGitHubCIStatusSkippedConclusionMapsToSuccess(t *testing.T) {
	// Pins the documented Python behavior (see go-migration-notes.md): a
	// "skipped" conclusion is success, not a distinct third state.
	withFakeBin(t)
	t.Setenv("FAKE_GH_CHECKS_STDOUT", `[{"state":"completed","conclusion":"skipped"}]`)
	got, err := NewGitHubProvider().CIStatus("https://github.com/org/repo/pull/1")
	if err != nil {
		t.Fatal(err)
	}
	if got != CISuccess {
		t.Errorf("got %q, want success", got)
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
