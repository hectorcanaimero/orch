package vcs

import (
	"errors"
	"strings"
	"testing"
)

// ---- CreatePR ---------------------------------------------------------

func TestGitLabCreatePRReturnsURL(t *testing.T) {
	withFakeBin(t)
	log := glabLog(t)
	t.Setenv("FAKE_GLAB_CREATE_STDOUT", "https://gitlab.com/org/repo/-/merge_requests/7\n")

	url, err := NewGitLabProvider("gitlab.com").CreatePR("orch/t1", "main", "My MR", "body")
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://gitlab.com/org/repo/-/merge_requests/7" {
		t.Errorf("url = %q", url)
	}
	got := readLog(t, log)
	for _, want := range []string{"mr create", "--source-branch orch/t1", "--target-branch main"} {
		if !strings.Contains(got, want) {
			t.Errorf("glab invocation missing %q, got: %s", want, got)
		}
	}
}

func TestGitLabCreatePRSetsGitlabHostEnv(t *testing.T) {
	withFakeBin(t)
	// The fake glab doesn't echo its env, so this drives the real behavior
	// through auth: pointing GITLAB_HOST at a host whose auth check we
	// control tells us the env var reached the child process.
	t.Setenv("FAKE_GLAB_CREATE_STDOUT", "https://gl.example.com/org/repo/-/merge_requests/1\n")

	url, err := NewGitLabProvider("gl.example.com").CreatePR("t1", "main", "title", "body")
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://gl.example.com/org/repo/-/merge_requests/1" {
		t.Errorf("url = %q", url)
	}
}

func TestGitLabCreatePRReturnsEmptyOnOrdinaryFailure(t *testing.T) {
	withFakeBin(t)
	t.Setenv("FAKE_GLAB_CREATE_EXIT", "1")
	t.Setenv("FAKE_GLAB_AUTH_EXIT", "0")

	url, err := NewGitLabProvider("gitlab.com").CreatePR("t1", "t", "b", "h")
	if err != nil {
		t.Fatalf("expected no error for an ordinary failure, got %v", err)
	}
	if url != "" {
		t.Errorf("url = %q, want empty", url)
	}
}

func TestGitLabCreatePRReturnsCLIErrorWhenNotAuthenticated(t *testing.T) {
	withFakeBin(t)
	t.Setenv("FAKE_GLAB_CREATE_EXIT", "1")
	t.Setenv("FAKE_GLAB_AUTH_EXIT", "1")

	_, err := NewGitLabProvider("gitlab.com").CreatePR("t1", "t", "b", "h")
	var cliErr *CLIError
	if !errors.As(err, &cliErr) {
		t.Fatalf("err = %v (%T), want *CLIError", err, err)
	}
	if cliErr.Binary != "glab" || cliErr.Reason != "not authenticated" {
		t.Errorf("cliErr = %+v", cliErr)
	}
}

// ---- CIStatus -----------------------------------------------------------

func TestGitLabCIStatusSuccess(t *testing.T) {
	withFakeBin(t)
	t.Setenv("FAKE_GLAB_VIEW_STDOUT", `{"head_pipeline":{"id":1,"status":"success"}}`)
	got, err := NewGitLabProvider("gitlab.com").CIStatus("https://gitlab.com/org/repo/-/merge_requests/7")
	if err != nil {
		t.Fatal(err)
	}
	if got != CISuccess {
		t.Errorf("got %q, want success", got)
	}
}

func TestGitLabCIStatusFailure(t *testing.T) {
	withFakeBin(t)
	t.Setenv("FAKE_GLAB_VIEW_STDOUT", `{"head_pipeline":{"id":1,"status":"failed"}}`)
	got, err := NewGitLabProvider("gitlab.com").CIStatus("https://gitlab.com/org/repo/-/merge_requests/7")
	if err != nil {
		t.Fatal(err)
	}
	if got != CIFailure {
		t.Errorf("got %q, want failure", got)
	}
}

func TestGitLabCIStatusPendingWhenRunning(t *testing.T) {
	withFakeBin(t)
	t.Setenv("FAKE_GLAB_VIEW_STDOUT", `{"head_pipeline":{"id":1,"status":"running"}}`)
	got, err := NewGitLabProvider("gitlab.com").CIStatus("https://gitlab.com/org/repo/-/merge_requests/7")
	if err != nil {
		t.Fatal(err)
	}
	if got != CIPending {
		t.Errorf("got %q, want pending", got)
	}
}

func TestGitLabCIStatusPendingOnGlabError(t *testing.T) {
	withFakeBin(t)
	t.Setenv("FAKE_GLAB_VIEW_EXIT", "1")
	got, err := NewGitLabProvider("gitlab.com").CIStatus("https://gitlab.com/org/repo/-/merge_requests/7")
	if err != nil {
		t.Fatal(err)
	}
	if got != CIPending {
		t.Errorf("got %q, want pending", got)
	}
}

func TestGitLabCIStatusPendingOnInvalidURL(t *testing.T) {
	withFakeBin(t)
	got, err := NewGitLabProvider("gitlab.com").CIStatus("https://github.com/org/repo/pull/1")
	if err != nil {
		t.Fatal(err)
	}
	if got != CIPending {
		t.Errorf("got %q, want pending", got)
	}
}

func TestGitLabCIStatusSkippedMapsToSuccess(t *testing.T) {
	withFakeBin(t)
	t.Setenv("FAKE_GLAB_VIEW_STDOUT", `{"head_pipeline":{"id":1,"status":"skipped"}}`)
	got, err := NewGitLabProvider("gitlab.com").CIStatus("https://gitlab.com/org/repo/-/merge_requests/7")
	if err != nil {
		t.Fatal(err)
	}
	if got != CISuccess {
		t.Errorf("got %q, want success", got)
	}
}

// ---- iidFromURL -----------------------------------------------------------

func TestIIDFromURLExtractsIID(t *testing.T) {
	if got := iidFromURL("https://gitlab.com/org/repo/-/merge_requests/42"); got != "42" {
		t.Errorf("got %q, want 42", got)
	}
}

func TestIIDFromURLReturnsEmptyOnInvalid(t *testing.T) {
	if got := iidFromURL("https://github.com/org/repo/pull/1"); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

// ---- CILogs ---------------------------------------------------------------

func TestGitLabCILogsCombinesFailedJobTraces(t *testing.T) {
	withFakeBin(t)
	t.Setenv("FAKE_GLAB_VIEW_STDOUT", `{"head_pipeline":{"id":55,"status":"failed"}}`)
	t.Setenv("FAKE_GLAB_JOBS_STDOUT", `[{"id":1,"status":"failed"},{"id":2,"status":"success"},{"id":3,"status":"failed"}]`)
	t.Setenv("FAKE_GLAB_TRACE_STDOUT", "trace output\n")

	logs, err := NewGitLabProvider("gitlab.com").CILogs("https://gitlab.com/org/repo/-/merge_requests/7")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs, "trace output") {
		t.Errorf("logs = %q", logs)
	}
	if !strings.Contains(logs, "---") {
		t.Errorf("expected multiple job traces joined with '---', got %q", logs)
	}
}

func TestGitLabCILogsEmptyWhenNoPipeline(t *testing.T) {
	withFakeBin(t)
	t.Setenv("FAKE_GLAB_VIEW_STDOUT", `{}`)
	logs, err := NewGitLabProvider("gitlab.com").CILogs("https://gitlab.com/org/repo/-/merge_requests/7")
	if err != nil {
		t.Fatal(err)
	}
	if logs != "" {
		t.Errorf("logs = %q, want empty", logs)
	}
}

// ---- MergePR (Sprint G-1) ---------------------------------------------

func TestGitLabMergePRPassesSquash(t *testing.T) {
	withFakeBin(t)
	log := glabLog(t)

	err := NewGitLabProvider("gitlab.com").MergePR("https://gitlab.com/org/repo/-/merge_requests/7", true, false)
	if err != nil {
		t.Fatal(err)
	}
	got := readLog(t, log)
	for _, want := range []string{"mr merge 7", "--squash"} {
		if !strings.Contains(got, want) {
			t.Errorf("glab invocation missing %q, got: %s", want, got)
		}
	}
}

func TestGitLabMergePRAutoUsesWhenPipelineSucceeds(t *testing.T) {
	withFakeBin(t)
	log := glabLog(t)

	err := NewGitLabProvider("gitlab.com").MergePR("https://gitlab.com/org/repo/-/merge_requests/7", true, true)
	if err != nil {
		t.Fatal(err)
	}
	got := readLog(t, log)
	if !strings.Contains(got, "--when-pipeline-succeeds") {
		t.Errorf("expected --when-pipeline-succeeds for auto=true, got: %s", got)
	}
}

func TestGitLabMergePRReturnsErrMergeFailedOnOrdinaryFailure(t *testing.T) {
	withFakeBin(t)
	t.Setenv("FAKE_GLAB_MERGE_EXIT", "1")
	t.Setenv("FAKE_GLAB_AUTH_EXIT", "0")

	err := NewGitLabProvider("gitlab.com").MergePR("https://gitlab.com/org/repo/-/merge_requests/7", true, false)
	if !errors.Is(err, ErrMergeFailed) {
		t.Fatalf("err = %v, want ErrMergeFailed", err)
	}
}

func TestGitLabMergePRReturnsErrMergeFailedOnInvalidURL(t *testing.T) {
	withFakeBin(t)
	err := NewGitLabProvider("gitlab.com").MergePR("https://github.com/org/repo/pull/1", true, false)
	if !errors.Is(err, ErrMergeFailed) {
		t.Fatalf("err = %v, want ErrMergeFailed", err)
	}
}
