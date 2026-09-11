package doctor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/router"
)

func withFakeBin(t *testing.T) {
	t.Helper()
	dir, err := filepath.Abs("testdata/fakebin")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func withNoBin(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

// ---- ReferencedBackends -----------------------------------------------

func TestReferencedBackendsFromRouter(t *testing.T) {
	tasks := []model.Task{{ID: "T1", Model: "claude/opus"}, {ID: "T2", Model: "codex/gpt"}}
	r := router.Router{
		"claude/opus": model.RouteEntry{Backend: model.BackendClaude},
		"codex/gpt":   model.RouteEntry{Backend: model.BackendCodex},
	}
	got := ReferencedBackends(tasks, r)
	if want := []string{"claude", "codex"}; !equalStrSlices(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestReferencedBackendsFallsBackWhenNoneResolve(t *testing.T) {
	tasks := []model.Task{{ID: "T1", Model: "unknown/model"}}
	got := ReferencedBackends(tasks, router.Router{})
	if want := []string{"claude", "codex", "opencode"}; !equalStrSlices(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func equalStrSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---- CheckBackends ------------------------------------------------------

func TestCheckBackendsAllPresentAndAuthed(t *testing.T) {
	withFakeBin(t)
	checks := CheckBackends([]string{"claude", "codex", "opencode"})

	byName := map[string]Check{}
	for _, c := range checks {
		byName[c.Name] = c
	}
	if byName["backend.claude"].Status != StatusOK {
		t.Errorf("backend.claude = %+v", byName["backend.claude"])
	}
	if byName["backend.claude.auth"].Status != StatusSkip {
		t.Errorf("backend.claude.auth = %+v, want skip (no cheap probe)", byName["backend.claude.auth"])
	}
	if byName["backend.opencode"].Status != StatusOK {
		t.Errorf("backend.opencode = %+v", byName["backend.opencode"])
	}
	if byName["backend.opencode.auth"].Status != StatusOK {
		t.Errorf("backend.opencode.auth = %+v", byName["backend.opencode.auth"])
	}
}

func TestCheckBackendsMissingBinaryIsError(t *testing.T) {
	withNoBin(t)
	checks := CheckBackends([]string{"claude"})
	if checks[0].Status != StatusError {
		t.Errorf("backend.claude = %+v, want error", checks[0])
	}
	if checks[1].Status != StatusSkip {
		t.Errorf("backend.claude.auth = %+v, want skip", checks[1])
	}
}

func TestCheckBackendsVersionFailureIsError(t *testing.T) {
	withFakeBin(t)
	t.Setenv("FAKE_CLAUDE_VERSION_EXIT", "1")
	checks := CheckBackends([]string{"claude"})
	if checks[0].Status != StatusError {
		t.Errorf("backend.claude = %+v, want error", checks[0])
	}
}

func TestCheckBackendsOpencodeAuthFailureIsError(t *testing.T) {
	withFakeBin(t)
	t.Setenv("FAKE_OPENCODE_AUTH_EXIT", "1")
	t.Setenv("FAKE_OPENCODE_AUTH_STDERR", "not logged in")
	checks := CheckBackends([]string{"opencode"})

	var auth Check
	for _, c := range checks {
		if c.Name == "backend.opencode.auth" {
			auth = c
		}
	}
	if auth.Status != StatusError {
		t.Errorf("backend.opencode.auth = %+v, want error", auth)
	}
	if auth.Remediation == "" {
		t.Errorf("expected a remediation hint")
	}
}

func TestCheckBackendsGeminiOkWhenPresentNoAuthProbe(t *testing.T) {
	withFakeBin(t)
	checks := CheckBackends([]string{"gemini"})
	var auth Check
	for _, c := range checks {
		if c.Name == "backend.gemini.auth" {
			auth = c
		}
	}
	if auth.Status != StatusOK {
		t.Errorf("backend.gemini.auth = %+v, want ok (presence-only signal)", auth)
	}
}
