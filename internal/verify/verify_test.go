package verify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hectorcanaimero/orch/internal/ci"
	"github.com/hectorcanaimero/orch/internal/config"
	"github.com/hectorcanaimero/orch/internal/engine"
	"github.com/hectorcanaimero/orch/internal/model"
)

func TestVerifyPassesWhenTheCommandsPass(t *testing.T) {
	v := Verifier{Cfg: config.Verify{Commands: []string{"true", "test -d .", "no-such-tool-463"}, TimeoutS: 10}}
	res := v.Verify(context.Background(), engine.VerifyRequest{TaskID: "T-1", Writer: model.BackendClaude, Dir: t.TempDir()})
	if res.Failure != "" {
		t.Errorf("Verify = %q, want a pass", res.Failure)
	}
	// #463: the pass says what ran, and the command sh could not find did not.
	if len(res.Commands) != 2 || res.Commands[0].Command != "true" || res.Commands[1].Command != "test -d ." || res.Commands[0].DurationS < 0 {
		t.Errorf("Commands = %+v, want true and test -d . with their durations", res.Commands)
	}
}

// The agent is shown the command and the end of its output: that is where a
// test runner or a compiler says what broke.
func TestVerifyReportsTheFailingCommandWithItsOutput(t *testing.T) {
	v := Verifier{Cfg: config.Verify{Commands: []string{"true", "echo FAIL TestLogin: want 401; exit 3", "touch never-ran"}, TimeoutS: 10}}
	dir := t.TempDir()
	got := v.Verify(context.Background(), engine.VerifyRequest{TaskID: "T-1", Writer: model.BackendClaude, Dir: dir}).Failure
	for _, want := range []string{"`echo FAIL TestLogin: want 401; exit 3`", "exit status 3", "FAIL TestLogin: want 401"} {
		if !strings.Contains(got, want) {
			t.Errorf("Verify = %q, want it to contain %q", got, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "never-ran")); err == nil {
		t.Error("a command after the failing one ran")
	}
}

// A configured command that is not found (sh's exit 127, a typo such as
// `tue`) is the operator's config, not the agent's work: it is skipped, and
// so is the E2E an app that cannot be started was for (#433).
func TestVerifySkipsAConfiguredCommandThatIsNotFound(t *testing.T) {
	v := Verifier{Cfg: config.Verify{
		Commands: []string{"orch-no-such-command-tue", "touch commands-ran"},
		App:      config.VerifyApp{Start: "orch-no-such-command-tue"},
		E2E:      []string{"orch-no-such-command-tue", "touch e2e-ran"},
		TimeoutS: 10,
	}}
	dir := t.TempDir()
	if got := v.Verify(context.Background(), engine.VerifyRequest{TaskID: "T-1", Writer: model.BackendClaude, Dir: dir}).Failure; got != "" {
		t.Errorf("Verify = %q, want a pass", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "commands-ran")); err != nil {
		t.Errorf("the commands after the missing one did not run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "e2e-ran")); err == nil {
		t.Error("the E2E ran without its app")
	}

	v.Cfg.App.Start = ""
	if got := v.Verify(context.Background(), engine.VerifyRequest{TaskID: "T-1", Writer: model.BackendClaude, Dir: dir}).Failure; got != "" {
		t.Errorf("Verify with a missing E2E command = %q, want a pass", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "e2e-ran")); err != nil {
		t.Errorf("the E2E commands after the missing one did not run: %v", err)
	}
}

func TestVerifyBoundsEachCommand(t *testing.T) {
	v := Verifier{Cfg: config.Verify{Commands: []string{"sleep 30"}, TimeoutS: 1}}
	if got := v.Verify(context.Background(), engine.VerifyRequest{TaskID: "T-1", Writer: model.BackendClaude, Dir: t.TempDir()}).Failure; !strings.Contains(got, "timed out after 1s") {
		t.Errorf("Verify = %q, want a timeout", got)
	}
}

// Detected checks run per package, from the package's directory, and one
// whose tool is not installed is skipped rather than failing the agent.
func TestVerifyRunsTheDetectedChecks(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "Makefile"), "test:\n\t@echo detected-make-test; exit 4\n")
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make is not installed")
	}
	v := Verifier{Cfg: config.Verify{TimeoutS: 10}}
	if got := v.Verify(context.Background(), engine.VerifyRequest{TaskID: "T-1", Writer: model.BackendClaude, Dir: dir}).Failure; !strings.Contains(got, "detected-make-test") {
		t.Errorf("Verify = %q, want the detected make test to have run and failed", got)
	}
}

func TestVerifySendsBackABlockingReview(t *testing.T) {
	dir := branchWithWork(t)
	fakeReviewer(t, "0", readFixture(t, "claude", "2.1.272", "review-verdict.json"))
	v := Verifier{Cfg: config.Verify{Review: true, Reviewer: "claude", TimeoutS: 30}, Base: "main"}

	got := v.Verify(context.Background(), engine.VerifyRequest{TaskID: "T-1", Writer: model.BackendCodex, Dir: dir}).Failure
	for _, want := range []string{"the review asks for changes", "Tests: new behaviour comes with a test that exercises it"} {
		if !strings.Contains(got, want) {
			t.Errorf("Verify = %q, want it to contain %q", got, want)
		}
	}
}

// #463: every finding comes back structured, however long the reason the
// agent reads gets, with the reviewer that found them.
func TestVerifyReturnsEveryFindingStructured(t *testing.T) {
	dir := branchWithWork(t)
	var findings []ci.Finding
	for i := 1; i <= 12; i++ {
		findings = append(findings, ci.Finding{Severity: ci.SeverityBlocking, File: "store.go", Line: i,
			Title: "finding " + strconv.Itoa(i), Detail: strings.TrimSpace(strings.Repeat("why it matters ", 10))})
	}
	verdict, _ := json.Marshal(ci.Verdict{Verdict: ci.VerdictRequestChanges, Summary: "twelve problems", Findings: findings})
	answer, _ := json.Marshal(map[string]any{"type": "result", "subtype": "success", "is_error": false, "result": string(verdict)})
	fakeReviewer(t, "0", answer)
	v := Verifier{Cfg: config.Verify{Review: true, Reviewer: "claude", ReviewModel: "opus", TimeoutS: 30}, Base: "main"}

	res := v.Verify(context.Background(), engine.VerifyRequest{TaskID: "T-1", Writer: model.BackendCodex, Dir: dir})
	if !strings.Contains(res.Failure, "the review asks for changes") {
		t.Errorf("Failure = %q, want the review's request", res.Failure)
	}
	if len(res.Findings) != 12 || res.Findings[11] != engine.VerifyFinding(findings[11]) {
		t.Errorf("Findings = %+v, want all 12", res.Findings)
	}
	if res.Reviewer != "claude" || res.ReviewModel != "opus" {
		t.Errorf("reviewer = %q/%q, want claude/opus", res.Reviewer, res.ReviewModel)
	}
}

func TestVerifyPassesAnApprovingReview(t *testing.T) {
	dir := branchWithWork(t)
	fakeReviewer(t, "0", []byte(`{"type":"result","subtype":"success","is_error":false,"result":"{\"verdict\":\"approve\",\"summary\":\"fine\",\"findings\":[]}"}`))
	v := Verifier{Cfg: config.Verify{Review: true, Reviewer: "claude", TimeoutS: 30}, Base: "main"}

	if got := v.Verify(context.Background(), engine.VerifyRequest{TaskID: "T-1", Writer: model.BackendCodex, Dir: dir}).Failure; got != "" {
		t.Errorf("Verify = %q, want a pass", got)
	}
}

// A reviewer that is down is not the agent's fault: the work passes.
func TestVerifyPassesWhenTheReviewCannotComplete(t *testing.T) {
	dir := branchWithWork(t)
	fakeReviewer(t, "1", []byte(`{"type":"result","subtype":"error","is_error":true,"terminal_reason":"api_error"}`))
	v := Verifier{Cfg: config.Verify{Review: true, Reviewer: "claude", TimeoutS: 30}, Base: "main"}

	if got := v.Verify(context.Background(), engine.VerifyRequest{TaskID: "T-1", Writer: model.BackendCodex, Dir: dir}).Failure; got != "" {
		t.Errorf("Verify = %q, want a pass", got)
	}
}

func TestReviewerIsNeverTheWriterByDefault(t *testing.T) {
	bin := t.TempDir()
	for _, b := range []string{"claude", "gemini"} {
		write(t, filepath.Join(bin, b), "#!/bin/sh\n")
		if err := os.Chmod(filepath.Join(bin, b), 0o700); err != nil { // #nosec G302 -- an executable stub in the test's temp dir
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	v := Verifier{}
	if got := v.reviewer(model.BackendClaude); got != model.BackendGemini {
		t.Errorf("reviewer for claude's work = %q, want gemini", got)
	}
	if got := v.reviewer(model.BackendCodex); got != model.BackendClaude {
		t.Errorf("reviewer for codex's work = %q, want claude", got)
	}
	v.Cfg.Reviewer = "claude"
	if got := v.reviewer(model.BackendClaude); got != model.BackendClaude {
		t.Errorf("configured reviewer = %q, want claude", got)
	}
}

func TestChecklistPrefersTheProjectsOwn(t *testing.T) {
	dir := t.TempDir()
	if got := checklist(dir, ""); got != ci.BuiltinChecklist {
		t.Errorf("no checklist file: got %q, want the built-in one", got)
	}
	write(t, filepath.Join(dir, ".github", "orch-review.md"), "- github one\n")
	if got := checklist(dir, ""); got != "- github one\n" {
		t.Errorf("got %q, want .github/orch-review.md", got)
	}
	write(t, filepath.Join(dir, ".orch", "review.md"), "- the domain imports no adapter\n")
	if got := checklist(dir, ""); got != "- the domain imports no adapter\n" {
		t.Errorf("got %q, want .orch/review.md first", got)
	}
	write(t, filepath.Join(dir, "docs", "rules.md"), "- configured\n")
	if got := checklist(dir, "docs/rules.md"); got != "- configured\n" {
		t.Errorf("got %q, want verify.checklist", got)
	}
}

// branchWithWork is a repository whose HEAD is one commit ahead of main.
func branchWithWork(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		// #nosec G204 -- this test's own fixed git subcommands.
		cmd := exec.Command("git", append([]string{"-c", "user.email=t@example.test", "-c", "user.name=t"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("symbolic-ref", "HEAD", "refs/heads/main")
	write(t, filepath.Join(dir, "README.md"), "x\n")
	git("add", "-A")
	git("commit", "-q", "-m", "init")
	git("checkout", "-q", "-b", "orch/T-1")
	write(t, filepath.Join(dir, "store.go"), "package store\n\nfunc Load() {}\n")
	git("add", "-A")
	git("commit", "-q", "-m", "work")
	return dir
}

// fakeReviewer makes claude answer with output and exit code, through the
// engine's fake provider.
func fakeReviewer(t *testing.T, exit string, output []byte) {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "claude", ci.ReviewTaskID+".out"), string(output))
	write(t, filepath.Join(dir, "claude", ci.ReviewTaskID+".exit"), exit)
	t.Setenv(engine.FakeProviderEnv, dir)
}

func readFixture(t *testing.T, parts ...string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(append([]string{"..", "providers", "testdata"}, parts...)...)) // #nosec G304 -- a fixture under testdata
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil { // #nosec G703 -- paths under the test's own temp dirs
		t.Fatal(err)
	}
}

func TestIsTestFile(t *testing.T) {
	tests := map[string]bool{
		"internal/auth/login_test.go":  true,
		"src/auth/login.test.ts":       true,
		"web/src/Button.spec.tsx":      true,
		"tests/test_login.py":          true,
		"app/login_test.py":            true,
		"src/test/java/LoginTest.java": true,
		"Auth.Tests/LoginTests.cs":     true,
		"src/__tests__/login.js":       true,
		"crates/auth/tests/login.rs":   true,
		"spec/login_spec.rb":           true,
		"internal/auth/login.go":       false,
		"src/auth/login.ts":            false,
		"app/testing_utils.py":         false,
		"docs/testing.md":              false,
		"src/contest/entry.ts":         false,
	}
	for path, want := range tests {
		if got := IsTestFile(path); got != want {
			t.Errorf("IsTestFile(%q) = %v, want %v", path, got, want)
		}
	}
}

// With dispatch.tdd on, a change with no test in it goes back to the agent,
// before any command runs; one with a test goes on to the commands.
func TestVerifyRequiresATestWhenTDDIsOn(t *testing.T) {
	dir := branchWithWork(t) // adds store.go only
	v := Verifier{Cfg: config.Verify{Commands: []string{"touch commands-ran"}, TimeoutS: 10}, Base: "main", RequireTests: true}

	got := v.Verify(context.Background(), engine.VerifyRequest{TaskID: "T-1", Writer: model.BackendClaude, Dir: dir}).Failure
	if !strings.Contains(got, "touches no test file") || !strings.Contains(got, "store.go") {
		t.Errorf("Verify = %q, want the missing test reported with the changed files", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "commands-ran")); err == nil {
		t.Error("the commands ran on a change that was already sent back")
	}

	write(t, filepath.Join(dir, "store_test.go"), "package store\n")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "test")
	if got := v.Verify(context.Background(), engine.VerifyRequest{TaskID: "T-1", Writer: model.BackendClaude, Dir: dir}).Failure; got != "" {
		t.Errorf("Verify = %q, want a pass once a test is in the change", got)
	}

	v.RequireTests = false
	gitIn(t, dir, "rm", "-q", "store_test.go")
	gitIn(t, dir, "commit", "-q", "-m", "no test")
	if got := v.Verify(context.Background(), engine.VerifyRequest{TaskID: "T-1", Writer: model.BackendClaude, Dir: dir}).Failure; got != "" {
		t.Errorf("Verify = %q, want no test requirement with dispatch.tdd off", got)
	}
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	// #nosec G204 -- this test's own fixed git subcommands.
	cmd := exec.Command("git", append([]string{"-c", "user.email=t@example.test", "-c", "user.name=t"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// appFixture is a server standing in for the task's app, already up, and a
// worktree whose start/stop commands leave a mark. The URL is written the
// way a project writes it, from the task's slot.
func appFixture(t *testing.T, status int) (Verifier, engine.VerifyRequest, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	old := readyPoll
	readyPoll = 10 * time.Millisecond
	t.Cleanup(func() { readyPoll = old })

	v := Verifier{Cfg: config.Verify{TimeoutS: 10, App: config.VerifyApp{
		Start:         "touch started",
		URL:           "http://127.0.0.1:$((BASE_PORT + ORCH_TASK_SLOT))",
		Stop:          "touch stopped",
		ReadyTimeoutS: 30, // room for a loaded machine; only a test that wants the timeout shortens it
	}}}
	req := engine.VerifyRequest{TaskID: "T-1", Writer: model.BackendClaude, Dir: t.TempDir(),
		Env: append(os.Environ(), "ORCH_TASK_SLOT=2", "BASE_PORT="+strconv.Itoa(port-2))}
	return v, req, srv.URL
}

func TestVerifyRunsE2EAgainstTheTasksApp(t *testing.T) {
	v, req, want := appFixture(t, http.StatusNotFound) // up, if not serving / yet
	v.Cfg.E2E = []string{`test "$ORCH_APP_URL" = "` + want + `" && touch e2e-ran`}

	if got := v.Verify(context.Background(), req).Failure; got != "" {
		t.Fatalf("Verify = %q, want a pass", got)
	}
	for _, mark := range []string{"started", "e2e-ran", "stopped"} {
		if _, err := os.Stat(filepath.Join(req.Dir, mark)); err != nil {
			t.Errorf("%s: %v", mark, err)
		}
	}
}

func TestVerifySendsBackAnAppThatNeverAnswers(t *testing.T) {
	v, req, want := appFixture(t, http.StatusInternalServerError)
	v.Cfg.App.ReadyTimeoutS = 1
	v.Cfg.E2E = []string{"touch e2e-ran"}

	got := v.Verify(context.Background(), req).Failure
	// The last error is the 500, or, on a loaded machine, the deadline of a
	// request still waiting: either way the app was never ready, which is
	// what this checks. A 500 taken as ready would have passed the work.
	if !strings.Contains(got, "never answered at "+want) {
		t.Errorf("Verify = %q, want the app reported as never ready", got)
	}
	if _, err := os.Stat(filepath.Join(req.Dir, "e2e-ran")); err == nil {
		t.Error("the E2E ran against an app that was not ready")
	}
	if _, err := os.Stat(filepath.Join(req.Dir, "stopped")); err != nil {
		t.Errorf("the app was not stopped: %v", err)
	}
}

func TestVerifySendsBackAnAppThatDoesNotStart(t *testing.T) {
	v, req, _ := appFixture(t, http.StatusOK)
	v.Cfg.App.Start = "echo 'service web: build failed'; exit 1"

	got := v.Verify(context.Background(), req).Failure
	if !strings.Contains(got, "did not start") || !strings.Contains(got, "service web: build failed") {
		t.Errorf("Verify = %q, want the start failure and its output", got)
	}
	if _, err := os.Stat(filepath.Join(req.Dir, "stopped")); err != nil {
		t.Errorf("a half-started app was not stopped: %v", err)
	}
}

func TestVerifySendsBackAFailingE2E(t *testing.T) {
	v, req, _ := appFixture(t, http.StatusOK)
	v.Cfg.E2E = []string{"echo '1 failed: checkout pays with a card'; exit 1"}

	got := v.Verify(context.Background(), req).Failure
	if !strings.Contains(got, "against the running app") || !strings.Contains(got, "checkout pays with a card") {
		t.Errorf("Verify = %q, want the E2E failure", got)
	}
	if _, err := os.Stat(filepath.Join(req.Dir, "stopped")); err != nil {
		t.Errorf("the app was not stopped: %v", err)
	}
}

// TestVerifyCollectsARealPlaywrightReport feeds the captured report through
// an E2E command that writes it and fails, as `playwright test` does.
func TestVerifyCollectsARealPlaywrightReport(t *testing.T) {
	dir := t.TempDir()
	raw, err := os.ReadFile(filepath.Join("testdata", "playwright", "1.64.1", "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "captured.json"), strings.ReplaceAll(string(raw), "/capture", dir))
	shots := filepath.Join(dir, "test-results", "shop-checkout-pays-with-a-card")
	for _, f := range []string{"test-failed-1.png", "video.webm", "error-context.md", "trace.zip"} {
		write(t, filepath.Join(shots, f), "bytes of "+f)
	}
	artifacts := filepath.Join(t.TempDir(), "attempt-1")
	v := Verifier{Cfg: config.Verify{TimeoutS: 10, E2E: []string{"cp captured.json e2e-report.json; exit 1"}, E2EReport: "e2e-report.json"}}

	res := v.Verify(context.Background(), engine.VerifyRequest{TaskID: "T-1", Writer: model.BackendClaude, Dir: dir, ArtifactsDir: artifacts})

	for _, want := range []string{
		"1 of 2 E2E tests failed",
		"- checkout › pays with a card (tests/shop.spec.ts:13)",
		"last step: click Pagar",
		`Expected: "Pago aprobado"`,
		`Received: "Error 500"`,
		"screenshot: test-results/shop-checkout-pays-with-a-card/test-failed-1.png",
	} {
		if !strings.Contains(res.Failure, want) {
			t.Errorf("failure is missing %q:\n%s", want, res.Failure)
		}
	}
	if strings.Contains(res.Failure, "\x1b[") {
		t.Errorf("the failure kept terminal colours:\n%q", res.Failure)
	}
	if res.E2E == nil || res.E2E.Passed != 1 || res.E2E.Failed != 1 || res.E2E.Report != filepath.Join(artifacts, ReportFile) {
		t.Fatalf("E2E = %+v, want 1 passed, 1 failed, the report under the artifacts dir", res.E2E)
	}

	b, err := os.ReadFile(res.E2E.Report) // #nosec G304 -- a temp dir this test made
	if err != nil {
		t.Fatal(err)
	}
	var rep E2EReport
	if err := json.Unmarshal(b, &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Tests) != 2 || rep.Tests[0].Status != "passed" || rep.Tests[1].Status != "failed" {
		t.Fatalf("tests = %+v", rep.Tests)
	}
	failed := rep.Tests[1]
	if len(failed.Attachments) != 4 {
		t.Fatalf("attachments = %+v, want the four the test left", failed.Attachments)
	}
	for _, a := range failed.Attachments {
		if a.Path == "" {
			t.Errorf("%s was not copied", a.Name)
			continue
		}
		if _, err := os.Stat(filepath.Join(artifacts, filepath.FromSlash(a.Path))); err != nil {
			t.Errorf("%s: %v", a.Name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "e2e-report.json")); err == nil {
		t.Error("the Playwright report was left in the worktree, where the commit would pick it up")
	}
}
