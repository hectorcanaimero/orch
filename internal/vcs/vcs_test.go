package vcs

import (
	"errors"
	"strings"
	"testing"
)

func TestNewProviderDefaultsToGitHub(t *testing.T) {
	p := NewProvider(Config{})
	if _, ok := p.(*GitHubProvider); !ok {
		t.Errorf("got %T, want *GitHubProvider", p)
	}
}

func TestNewProviderGitHubExplicit(t *testing.T) {
	p := NewProvider(Config{Provider: "github"})
	if _, ok := p.(*GitHubProvider); !ok {
		t.Errorf("got %T, want *GitHubProvider", p)
	}
}

func TestNewProviderGitLabWithHost(t *testing.T) {
	p := NewProvider(Config{Provider: "gitlab", Host: "gitlab.example.com"})
	gl, ok := p.(*GitLabProvider)
	if !ok {
		t.Fatalf("got %T, want *GitLabProvider", p)
	}
	if gl.host != "gitlab.example.com" {
		t.Errorf("host = %q", gl.host)
	}
}

func TestNewProviderGitLabDefaultsHost(t *testing.T) {
	p := NewProvider(Config{Provider: "gitlab"})
	gl, ok := p.(*GitLabProvider)
	if !ok {
		t.Fatalf("got %T, want *GitLabProvider", p)
	}
	if gl.host != "gitlab.com" {
		t.Errorf("host = %q, want gitlab.com", gl.host)
	}
}

func TestCLIErrorMessageWithUnderlyingError(t *testing.T) {
	err := &CLIError{Binary: "gh", Reason: "not authenticated", Err: errors.New("exit status 1")}
	msg := err.Error()
	for _, want := range []string{"gh", "not authenticated", "exit status 1"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Error() = %q, missing %q", msg, want)
		}
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("Unwrap() = nil, want the underlying error")
	}
}

func TestCLIErrorMessageWithoutUnderlyingError(t *testing.T) {
	err := &CLIError{Binary: "glab", Reason: "not found in PATH"}
	msg := err.Error()
	if !strings.Contains(msg, "glab") || !strings.Contains(msg, "not found in PATH") {
		t.Errorf("Error() = %q", msg)
	}
}

func TestRunErrorMessageFormats(t *testing.T) {
	withRun := &runError{binary: "gh", args: []string{"pr", "create"}, stderr: "boom"}
	if msg := withRun.Error(); !strings.Contains(msg, "boom") {
		t.Errorf("Error() = %q", msg)
	}
	withErr := &runError{binary: "gh", args: []string{"pr"}, err: errors.New("no such file")}
	if msg := withErr.Error(); !strings.Contains(msg, "no such file") {
		t.Errorf("Error() = %q", msg)
	}
}

// ---- Binary-missing coverage across the remaining methods ---------------

func TestCheckAuthOKWhenAuthenticated(t *testing.T) {
	withFakeBin(t)
	if err := CheckAuth("gh", ""); err != nil {
		t.Fatalf("CheckAuth = %v, want nil", err)
	}
}

func TestCheckAuthReturnsCLIErrorWhenNotAuthenticated(t *testing.T) {
	withFakeBin(t)
	t.Setenv("FAKE_GLAB_AUTH_EXIT", "1")
	err := CheckAuth("glab", "gitlab.example.com")
	var cliErr *CLIError
	if !errors.As(err, &cliErr) || cliErr.Reason != "not authenticated" {
		t.Fatalf("err = %v, want *CLIError{Reason: not authenticated}", err)
	}
}

func TestCheckAuthReturnsCLIErrorWhenBinaryMissing(t *testing.T) {
	withNoBin(t)
	if !isCLIError(CheckAuth("gh", "")) {
		t.Fatalf("expected *CLIError for a missing binary")
	}
}

func TestGitHubCILogsReturnsCLIErrorWhenBinaryMissing(t *testing.T) {
	withNoBin(t)
	if _, err := NewGitHubProvider().CILogs("https://github.com/org/repo/pull/1"); !isCLIError(err) {
		t.Fatalf("err = %v, want *CLIError", err)
	}
}

func TestGitHubMergePRReturnsCLIErrorWhenBinaryMissing(t *testing.T) {
	withNoBin(t)
	if err := NewGitHubProvider().MergePR("https://github.com/org/repo/pull/1", true, false); !isCLIError(err) {
		t.Fatalf("err = %v, want *CLIError", err)
	}
}

func TestGitLabCIStatusReturnsCLIErrorWhenBinaryMissing(t *testing.T) {
	withNoBin(t)
	if _, err := NewGitLabProvider("gitlab.com").CIStatus("https://gitlab.com/org/repo/-/merge_requests/1"); !isCLIError(err) {
		t.Fatalf("err = %v, want *CLIError", err)
	}
}

func TestGitLabCILogsReturnsCLIErrorWhenBinaryMissing(t *testing.T) {
	withNoBin(t)
	if _, err := NewGitLabProvider("gitlab.com").CILogs("https://gitlab.com/org/repo/-/merge_requests/1"); !isCLIError(err) {
		t.Fatalf("err = %v, want *CLIError", err)
	}
}

func TestGitLabMergePRReturnsCLIErrorWhenBinaryMissing(t *testing.T) {
	withNoBin(t)
	if err := NewGitLabProvider("gitlab.com").MergePR("https://gitlab.com/org/repo/-/merge_requests/1", true, false); !isCLIError(err) {
		t.Fatalf("err = %v, want *CLIError", err)
	}
}

func isCLIError(err error) bool {
	var cliErr *CLIError
	return errors.As(err, &cliErr)
}

// ---- runIDFromURL / isAllDigits edge cases -------------------------------

func TestRunIDFromURLWithJobSuffix(t *testing.T) {
	got := runIDFromURL("https://github.com/org/repo/actions/runs/99999/jobs/123")
	if got != "123" {
		t.Errorf("got %q, want the rightmost numeric segment (123)", got)
	}
}

func TestRunIDFromURLReturnsEmptyWithNoDigits(t *testing.T) {
	if got := runIDFromURL("https://github.com/org/repo/actions/runs/abc"); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

// ---- Malformed JSON is treated as pending/empty, matching Python's
// json.JSONDecodeError handling. ------------------------------------------

func TestGitHubCIStatusPendingOnMalformedJSON(t *testing.T) {
	withFakeBin(t)
	t.Setenv("FAKE_GH_CHECKS_STDOUT", "not json")
	got, err := NewGitHubProvider().CIStatus("https://github.com/org/repo/pull/1")
	if err != nil {
		t.Fatal(err)
	}
	if got != CIPending {
		t.Errorf("got %q, want pending", got)
	}
}

func TestGitLabCIStatusPendingOnMalformedJSON(t *testing.T) {
	withFakeBin(t)
	t.Setenv("FAKE_GLAB_VIEW_STDOUT", "not json")
	got, err := NewGitLabProvider("gitlab.com").CIStatus("https://gitlab.com/org/repo/-/merge_requests/1")
	if err != nil {
		t.Fatal(err)
	}
	if got != CIPending {
		t.Errorf("got %q, want pending", got)
	}
}
