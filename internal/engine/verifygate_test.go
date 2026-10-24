package engine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/worktree"
)

// scriptedVerifier answers each call with the next failure in its script,
// "" for a pass, and repeats the last answer once the script runs out. It
// records the worktree of every call.
type scriptedVerifier struct {
	mu      sync.Mutex
	answers []string
	dirs    []string
	envs    [][]string
	// e2e is returned with every answer; artifacts records each call's
	// ArtifactsDir.
	e2e       *E2ESummary
	artifacts []string
	// findings come back with every failing answer.
	findings []VerifyFinding
}

func (v *scriptedVerifier) Verify(_ context.Context, req VerifyRequest) VerifyResult {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.dirs = append(v.dirs, req.Dir)
	v.envs = append(v.envs, req.Env)
	v.artifacts = append(v.artifacts, req.ArtifactsDir)
	i := min(len(v.dirs), len(v.answers)) - 1
	res := VerifyResult{Failure: v.answers[i], E2E: v.e2e, Commands: []VerifyCommand{{Command: "go test ./...", DurationS: 1.5}}, Reviewer: "gemini"}
	if res.Failure != "" {
		res.Findings = v.findings
	}
	return res
}

// verifyFixture is a reap fixture over a real repository with a bare
// origin, in worktree mode, with v as the verifier.
func verifyFixture(t *testing.T, v Verifier, maxAttempts int) (*reapFixture, string) {
	t.Helper()
	// A newline after the output, as a real CLI prints: every attempt of a
	// run appends to the same log, and the retry's line must parse.
	ok := okResponse()
	ok.out += "\n"
	f := newReapFixture(t,
		[]model.Task{task("C-1", 1, "claude/opus")},
		map[string]fakeResponse{"C-1": ok},
		SchedulerOptions{WorktreeMode: true, BaseBranch: "main"})
	root := f.s.Opts.Cwd
	remote := filepath.Join(t.TempDir(), "remote.git")
	gitRun(t, "", "init", "-q", "--bare", remote)
	gitRun(t, root, "init", "-q")
	gitRun(t, root, "symbolic-ref", "HEAD", "refs/heads/main")
	write(t, filepath.Join(root, "README.md"), "initial\n")
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "initial")
	gitRun(t, root, "remote", "add", "origin", remote)
	gitRun(t, root, "push", "-q", "origin", "main")
	f.s.Worktree = NewWorktreeManager(worktree.NewManager(root, true))
	f.s.Verifier = v
	f.s.Opts.Cfg.Verify.MaxAttempts = maxAttempts
	return f, remote
}

func remoteBranchFiles(t *testing.T, remote, branch string) (string, bool) {
	t.Helper()
	// #nosec G204 -- fixed git subcommand on a temp repo this test made.
	out, err := exec.Command("git", "-C", remote, "ls-tree", "--name-only", branch).CombinedOutput()
	if err != nil {
		return "", false
	}
	return string(out), true
}

// TestVerifyFailureSendsTheTaskBackToItsWorktree: work the local verify
// fails is not pushed; the task goes back to its agent in the same worktree
// with the failure to read, and the work is pushed once it passes.
func TestVerifyFailureSendsTheTaskBackToItsWorktree(t *testing.T) {
	v := &scriptedVerifier{answers: []string{"`go test ./...` failed:\nFAIL TestLogin", ""}}
	f, remote := verifyFixture(t, v, 2)
	ctx := context.Background()

	f.runOnce(t)

	if _, pushed := remoteBranchFiles(t, remote, "orch/C-1"); pushed {
		t.Error("work that failed the verify was pushed")
	}
	if got := f.backend.eventTypes("C-1"); !contains(got, EventVerifyFailed) {
		t.Errorf("events = %v, want a verify_failed", got)
	}
	if got, _ := f.s.Queue.Status("C-1"); got != model.StatusTodo {
		t.Errorf("status = %q, want todo for the send-back", got)
	}
	wt := v.dirs[0]
	feedback, err := os.ReadFile(filepath.Join(wt, CIFeedbackFile)) // #nosec G304 -- a temp dir this test made
	if err != nil {
		t.Fatalf("the kept worktree has no feedback file: %v", err)
	}
	if !strings.Contains(string(feedback), "FAIL TestLogin") {
		t.Errorf("feedback = %q, want the failure", feedback)
	}

	if _, err := f.s.Refill(ctx); err != nil {
		t.Fatalf("Refill: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(f.s.Opts.StateDir, "prompts", f.s.Opts.RunID, "C-1.txt")) // #nosec G304 -- a temp dir this test made
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "Working dir: "+wt+"\n") || !strings.Contains(string(body), "READ FIRST "+CIFeedbackFile) {
		t.Errorf("the retry prompt does not send the agent back to %s with the feedback:\n%s", wt, body)
	}
	if _, err := f.s.DrainWait(ctx, 30*time.Second); err != nil {
		t.Fatalf("DrainWait: %v", err)
	}

	if len(v.dirs) != 2 || v.dirs[1] != wt {
		t.Errorf("verified in %v, want the same worktree twice", v.dirs)
	}
	// The checks run as the task: its slot is what keeps two tasks' apps
	// off the same port.
	for i, env := range v.envs {
		if !slices.Contains(env, "ORCH_TASK_ID=C-1") || !slices.ContainsFunc(env, func(kv string) bool { return strings.HasPrefix(kv, "ORCH_TASK_SLOT=") }) {
			t.Errorf("verify %d ran without the task's environment: %v", i+1, env)
		}
	}
	files, pushed := remoteBranchFiles(t, remote, "orch/C-1")
	if !pushed {
		t.Fatal("work that passed the verify was not pushed")
	}
	if strings.Contains(files, CIFeedbackFile) {
		t.Errorf("the pushed branch carries the feedback file: %q", files)
	}
	if got, _ := f.s.Queue.Status("C-1"); got != model.StatusDone {
		t.Errorf("status = %q, want done", got)
	}
}

// TestVerifyBlocksOnceTheSendBacksAreUsedUp: an agent that cannot pass is
// blocked with the failure, and nothing is pushed.
func TestVerifyBlocksOnceTheSendBacksAreUsedUp(t *testing.T) {
	v := &scriptedVerifier{answers: []string{"`make test` failed:\nboom"}}
	f, remote := verifyFixture(t, v, 1)

	f.runOnce(t)
	f.runOnce(t)

	if got, _ := f.s.Queue.Status("C-1"); got != model.StatusBlocked {
		t.Fatalf("status = %q, want blocked", got)
	}
	if len(v.dirs) != 2 {
		t.Errorf("verified %d times, want the attempt and its one send-back", len(v.dirs))
	}
	if _, pushed := remoteBranchFiles(t, remote, "orch/C-1"); pushed {
		t.Error("work that never passed the verify was pushed")
	}
	reason, _ := f.backend.eventExtra("C-1", EventFail)["reason"].(string)
	if !strings.Contains(reason, "local verify still failing") || !strings.Contains(reason, "`make test` failed") {
		t.Errorf("block reason = %q, want the verify failure", reason)
	}
}

// TestVerifyRecordsItsVerdicts is #463: a failing verify records the
// review's findings uncut next to the reason it cut, sent back or blocking;
// a passing one records verify_passed with what ran and who reviewed.
func TestVerifyRecordsItsVerdicts(t *testing.T) {
	var findings []VerifyFinding
	for i := 1; i <= 12; i++ {
		findings = append(findings, VerifyFinding{Severity: "blocking", File: "app.go", Line: i, Title: fmt.Sprintf("finding %d", i), Detail: strings.Repeat("x", 100)})
	}
	v := &scriptedVerifier{answers: []string{"the review asks for changes", ""}, findings: findings}
	f, _ := verifyFixture(t, v, 2)

	f.runOnce(t)
	f.runOnce(t)

	failed := f.backend.eventExtra("C-1", EventVerifyFailed)
	if got, _ := failed["findings"].([]VerifyFinding); len(got) != 12 || got[11] != findings[11] {
		t.Errorf("verify_failed findings = %v, want all 12", failed["findings"])
	}
	if failed["reason"] != "the review asks for changes" || failed["attempt"] != 1 || failed["sent_back"] != true {
		t.Errorf("verify_failed = %v", failed)
	}
	passed := f.backend.eventExtra("C-1", EventVerifyPassed)
	if passed == nil {
		t.Fatalf("events = %v, want a verify_passed", f.backend.eventTypes("C-1"))
	}
	if cmds, _ := passed["commands"].([]VerifyCommand); passed["attempt"] != 2 || passed["reviewer"] != "gemini" || len(cmds) != 1 || cmds[0].DurationS != 1.5 {
		t.Errorf("verify_passed = %v", passed)
	}

	// The attempt that runs out of send-backs records its findings too.
	blocked := &scriptedVerifier{answers: []string{"still wrong"}, findings: findings[:1]}
	f, _ = verifyFixture(t, blocked, 0)
	f.s.Opts.Cfg.Verify.MaxAttempts = 1
	f.runOnce(t)
	f.runOnce(t)
	var last map[string]any
	f.backend.mu.Lock()
	for _, e := range f.backend.events {
		if e.eventType == EventVerifyFailed {
			last = e.extra
		}
	}
	f.backend.mu.Unlock()
	if last["sent_back"] != false || last["attempt"] != 2 || last["findings"] == nil {
		t.Errorf("the blocking verify_failed = %v, want attempt 2, not sent back, with its findings", last)
	}
}

// TestNoVerifierPushesAsBefore: without verify.enabled a clean exit is
// pushed straight away, as it always was.
func TestNoVerifierPushesAsBefore(t *testing.T) {
	f, remote := verifyFixture(t, nil, 0)
	f.s.Verifier = nil

	f.runOnce(t)

	if _, pushed := remoteBranchFiles(t, remote, "orch/C-1"); !pushed {
		t.Error("a clean exit was not pushed")
	}
	if got, _ := f.s.Queue.Status("C-1"); got != model.StatusDone {
		t.Errorf("status = %q, want done", got)
	}
}

// TestE2EResultsAreRecordedPerAttempt: each verified attempt's E2E run is an
// e2e_report event with its counts and report, the one the dashboard lists,
// and every attempt keeps its files in a directory of its own.
func TestE2EResultsAreRecordedPerAttempt(t *testing.T) {
	v := &scriptedVerifier{answers: []string{"1 of 3 E2E tests failed", ""}, e2e: &E2ESummary{Passed: 2, Failed: 1, Report: "/state/report.json"}}
	f, _ := verifyFixture(t, v, 2)

	f.runOnce(t)
	f.runOnce(t)

	var reports []map[string]any
	f.backend.mu.Lock()
	for _, e := range f.backend.events {
		if e.taskID == "C-1" && e.eventType == EventE2EReport {
			reports = append(reports, e.extra)
		}
	}
	f.backend.mu.Unlock()
	if len(reports) != 2 {
		t.Fatalf("e2e_report events = %v, want one per attempt", reports)
	}
	for i, r := range reports {
		if r["attempt"] != i+1 || r["passed"] != 2 || r["failed"] != 1 || r["report"] != "/state/report.json" {
			t.Errorf("e2e_report %d = %v", i+1, r)
		}
	}
	want := []string{
		E2EArtifactsDir(f.s.Opts.StateDir, f.s.Opts.RunID, "C-1", 1),
		E2EArtifactsDir(f.s.Opts.StateDir, f.s.Opts.RunID, "C-1", 2),
	}
	if !slices.Equal(v.artifacts, want) {
		t.Errorf("artifacts dirs = %v, want %v", v.artifacts, want)
	}
}

// TestDispatchGrantsTheDatabaseDir is #387's engine half: every dispatch asks
// its provider to let the agent write the directory that holds orch.db, which
// is outside a worktree and, with state.sqlite_path, may be outside the state
// dir too. Without DBDir it is the state dir, where the database lives by
// default.
func TestDispatchGrantsTheDatabaseDir(t *testing.T) {
	for _, tc := range []struct {
		name, dbDir string
		want        func(stateDir string) string
	}{
		{"database elsewhere", "/srv/orch-db", func(string) string { return "/srv/orch-db" }},
		{"default layout", "", func(stateDir string) string { return stateDir }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newReapFixture(t,
				[]model.Task{task("C-1", 1, "claude/opus")},
				map[string]fakeResponse{"C-1": okResponse()},
				SchedulerOptions{DBDir: tc.dbDir})
			if _, err := f.s.Refill(context.Background()); err != nil {
				t.Fatalf("Refill: %v", err)
			}
			t.Cleanup(func() { _, _ = f.s.DrainWait(context.Background(), 30*time.Second) })
			var got []string
			for _, e := range f.s.inFlight {
				got = e.Dispatch.Req.WritableDirs
			}
			if want := []string{tc.want(f.s.Opts.StateDir)}; !slices.Equal(got, want) {
				t.Errorf("WritableDirs = %v, want %v", got, want)
			}
		})
	}
}
