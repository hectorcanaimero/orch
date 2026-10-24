package dashboard

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/hectorcanaimero/orch/internal/engine"
	"github.com/hectorcanaimero/orch/internal/state"
)

func e2eEvent(id int64, run, task string, attempt, passed, failed int) state.Event {
	return state.Event{ID: id, RunID: run, EventType: eventE2EReport, TaskID: task, TS: "2026-10-22T20:00:00Z",
		Extra: map[string]any{"attempt": float64(attempt), "passed": float64(passed), "failed": float64(failed),
			"flaky": float64(0), "skipped": float64(1), "report": "/ignored/report.json"}}
}

func TestTestsListsTheCollectedRunsNewestFirst(t *testing.T) {
	st := &fakeState{events: []state.Event{
		e2eEvent(1, "run-1", "C-1", 1, 1, 1),
		{ID: 2, RunID: "run-1", EventType: "verify_failed", TaskID: "C-1"},
		e2eEvent(3, "run-1", "C-1", 2, 2, 0),
	}}
	s := newReadServer(t, st)

	var got testsPayload
	decode(t, get(t, s, "/api/tests"), &got)
	want := []e2eRun{
		{RunID: "run-1", TaskID: "C-1", Attempt: 2, At: "2026-10-22T20:00:00Z", Passed: 2, Skipped: 1},
		{RunID: "run-1", TaskID: "C-1", Attempt: 1, At: "2026-10-22T20:00:00Z", Passed: 1, Failed: 1, Skipped: 1},
	}
	if len(got.Runs) != len(want) {
		t.Fatalf("runs = %+v, want %+v", got.Runs, want)
	}
	for i := range want {
		if got.Runs[i] != want[i] {
			t.Errorf("run %d = %+v, want %+v", i, got.Runs[i], want[i])
		}
	}
}

// writeAttempt leaves a report and a video where the verify keeps them.
func writeAttempt(t *testing.T, s *Server) string {
	t.Helper()
	dir := engine.E2EArtifactsDir(s.paths.StateDir(), "run-1", "C-1", 2)
	for name, body := range map[string]string{
		e2eReportFile:           `{"task_id":"C-1","tests":[]}`,
		"t002/video.webm":       "0123456789",
		"t002/error-context.md": "# context",
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// The dashboard reads the layout engine.E2EArtifactsDir writes.
func TestTestReportAndFilesAreServed(t *testing.T) {
	s := newReadServer(t, &fakeState{})
	writeAttempt(t, s)

	resp := get(t, s, "/api/tests/run-1/C-1/2")
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != `{"task_id":"C-1","tests":[]}` {
		t.Fatalf("report: %d %s", resp.StatusCode, body)
	}

	resp = get(t, s, "/api/tests/run-1/C-1/2/files/t002/video.webm")
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "video/webm" {
		t.Errorf("video: %d %q, want 200 video/webm", resp.StatusCode, resp.Header.Get("Content-Type"))
	}

	// A video seeks with Range requests.
	req := httptest.NewRequest(http.MethodGet, "/api/tests/run-1/C-1/2/files/t002/video.webm", nil)
	req.Header.Set("Range", "bytes=2-4")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "234" {
		t.Errorf("range: %d %q, want 206 \"234\"", rec.Code, rec.Body.String())
	}

	resp = get(t, s, "/api/tests/run-1/C-1/2/files/t002/error-context.md?download=1")
	if cd := resp.Header.Get("Content-Disposition"); cd != `attachment; filename="error-context.md"` {
		t.Errorf("Content-Disposition = %q", cd)
	}

	if resp := get(t, s, "/api/tests/run-1/C-1/1"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("an attempt with no report: %d, want 404", resp.StatusCode)
	}
}

// Nothing outside the attempt's directory is reachable, however the path is
// spelled.
func TestTestFilesStayInsideTheAttempt(t *testing.T) {
	s := newReadServer(t, &fakeState{})
	writeAttempt(t, s)
	secret := filepath.Join(s.paths.StateDir(), "e2e", "secret.txt")
	if err := os.WriteFile(secret, []byte("not yours"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/api/tests/run-1/C-1/2/files/..%2F..%2F..%2Fsecret.txt",
		"/api/tests/run-1/C-1/2/files/t002%2F..%2F..%2F..%2F..%2Fsecret.txt",
		"/api/tests/run-1/C-1/2/files/t002%5C..%5Creport.json",
		"/api/tests/%2E%2E/C-1/2",
		"/api/tests/run-1/C-1/0",
		"/api/tests/run-1/C-1/two",
		"/api/tests/run-1/C-1/2/files/t002",
	} {
		resp := get(t, s, path)
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode == http.StatusOK {
			t.Errorf("%s: 200 %q, want a refusal", path, body)
		}
	}
}

func TestTestsAreOperatorOnly(t *testing.T) {
	s := newGatedServer(t, &fakeState{})
	for _, path := range []string{"/api/tests", "/api/tests/run-1/C-1/1", "/api/tests/run-1/C-1/1/files/x.png"} {
		if resp := get(t, s, path+"?token=test-token-stakeholder"); resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s as a stakeholder: %d, want 403", path, resp.StatusCode)
		}
	}
}

// The log reads what the verify did, not a bare event name.
func TestVerifyEventsReadInTheLog(t *testing.T) {
	sendBack := formatEvent(state.Event{EventType: "verify_failed", TaskID: "C-1", TS: "2026-10-22T20:00:00Z",
		Extra: map[string]any{"attempt": float64(1), "reason": "x"}})
	report := formatEvent(e2eEvent(2, "run-1", "C-1", 1, 3, 1))
	if want := "[20:00:00] VERIFY C-1 verify_failed (send-back 1)"; sendBack.Human != want || sendBack.Severity != "warn" {
		t.Errorf("verify_failed = %q %q, want %q warn", sendBack.Human, sendBack.Severity, want)
	}
	// #463: the findings counted, the attempt that ran out of send-backs, a pass.
	blocked := formatEvent(state.Event{EventType: "verify_failed", TaskID: "C-1", TS: "2026-10-22T20:00:00Z",
		Extra: map[string]any{"attempt": float64(3), "sent_back": false, "findings": []any{map[string]any{}, map[string]any{}}}})
	if want := "[20:00:00] VERIFY C-1 verify_failed (attempt 3, 2 findings, blocked)"; blocked.Human != want {
		t.Errorf("verify_failed = %q, want %q", blocked.Human, want)
	}
	passed := formatEvent(state.Event{EventType: "verify_passed", TaskID: "C-1", TS: "2026-10-22T20:00:00Z",
		Extra: map[string]any{"attempt": float64(2), "reviewer": "gemini"}})
	if want := "[20:00:00] VERIFY C-1 verify_passed (attempt 2, reviewed by gemini)"; passed.Human != want || passed.Severity != "info" {
		t.Errorf("verify_passed = %q %q, want %q info", passed.Human, passed.Severity, want)
	}
	if want := "[20:00:00] E2E C-1 e2e_report (3 passed, 1 failed)"; report.Human != want {
		t.Errorf("e2e_report = %q, want %q", report.Human, want)
	}
}

// A dispatch's effort shows on its log line when the route set one (#388).
func TestDispatchLineNamesTheEffort(t *testing.T) {
	got := formatEvent(state.Event{EventType: "dispatch", TaskID: "C-1", TS: "2026-10-22T20:00:00Z",
		Extra: map[string]any{"cli_model": "gpt-5.6-codex", "effort": "low", "attempt": float64(1)}})
	if want := "[20:00:00] -> C-1 dispatch -> gpt-5.6-codex (effort low)"; got.Human != want {
		t.Errorf("dispatch = %q, want %q", got.Human, want)
	}
}
