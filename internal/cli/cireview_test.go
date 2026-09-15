package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hectorcanaimero/orch/internal/ci"
	"github.com/hectorcanaimero/orch/internal/engine"
)

// reviewRepo is a git repository on branch feature, one commit ahead of main
// (also published as origin/main), and the test's working directory.
func reviewRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		// #nosec G204 -- git with the test's own literal arguments
		cmd := exec.Command("git", append([]string{"-c", "user.email=test@example.test", "-c", "user.name=test"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "main")
	write("store.go", "package store\n")
	git("add", "-A")
	git("commit", "-q", "-m", "init")
	git("update-ref", "refs/remotes/origin/main", "main")
	git("checkout", "-q", "-b", "feature")
	write("store.go", "package store\n\nfunc Load() {}\n")
	write("notes.md", strings.Repeat("A line of notes.\n", 200))
	git("add", "-A")
	git("commit", "-q", "-m", "feat: load")
	t.Chdir(dir)
	return dir
}

// providerTestdata is resolved when the package loads, before any test
// changes the working directory.
var providerTestdata = func() string {
	p, err := filepath.Abs(filepath.Join("..", "providers", "testdata"))
	if err != nil {
		panic(err)
	}
	return p
}()

// fakeReviewer makes claude answer with a captured output (a path under
// internal/providers/testdata) and exit code.
func fakeReviewer(t *testing.T, exit string, fixture ...string) {
	t.Helper()
	src, err := os.ReadFile(filepath.Join(append([]string{providerTestdata}, fixture...)...)) // #nosec G304 -- a fixture under testdata
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	claude := filepath.Join(dir, "claude")
	if err := os.Mkdir(claude, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claude, ci.ReviewTaskID+".out"), src, 0o600); err != nil { // #nosec G703 -- the test's own temp dir
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claude, ci.ReviewTaskID+".exit"), []byte(exit), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(engine.FakeProviderEnv, dir)
}

var realVerdict = []string{"claude", "2.1.272", "review-verdict.json"}

// runReview runs `orch ci review args...` with no GitHub Actions variables
// leaking in from the environment the tests themselves run in.
func runReview(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	for _, k := range []string{"GITHUB_BASE_REF", "GITHUB_REPOSITORY", "GITHUB_EVENT_PATH"} {
		if _, set := os.LookupEnv(k); set {
			t.Setenv(k, "")
		}
	}
	cmd := newCICmd(&projectFlags{}, "dev")
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(append([]string{"review"}, args...))
	err := cmd.Execute()
	var ee *exitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		code = ee.code
		errOut.WriteString(err.Error())
	default:
		code = 1
		errOut.WriteString(err.Error())
	}
	return out.String(), errOut.String(), code
}

func TestCIReviewPrintsTheVerdict(t *testing.T) {
	reviewRepo(t)
	fakeReviewer(t, "0", realVerdict...)

	out, errOut, code := runReview(t, "--provider", "claude", "--model", "claude-haiku-4-5-20251001")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, want := range []string{
		"## orch review (claude, claude-haiku-4-5-20251001)",
		"**Changes requested.**",
		"### Blocking",
		"- `store.go:11` **Error handling",
		"> No checklist at .github/orch-review.md: the built-in general checklist was used.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}

	// The same answer fails the job with --blocking.
	_, errOut, code = runReview(t, "--provider", "claude", "--model", "m", "--blocking")
	if code != 1 || !strings.Contains(errOut, "request_changes with --blocking") {
		t.Errorf("--blocking: exit %d, stderr %q", code, errOut)
	}
}

func TestCIReviewOptions(t *testing.T) {
	dir := reviewRepo(t)
	fakeReviewer(t, "0", realVerdict...)

	checklist := filepath.Join(dir, "rules.md")
	if err := os.WriteFile(checklist, []byte("- **Rule 1**: tests.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _, code := runReview(t, "--provider", "claude", "--model", "m", "--checklist", checklist)
	if code != 0 || strings.Contains(out, "No checklist") {
		t.Errorf("an existing checklist: exit %d\n%s", code, out)
	}

	out, _, _ = runReview(t, "--provider", "claude", "--model", "m", "--max-diff-bytes", "1000")
	if !strings.Contains(out, "> The diff is over 1000 bytes and was cut short in: notes.md.") {
		t.Errorf("a cut diff should say so:\n%s", out)
	}

	out, errOut, code := runReview(t, "--provider", "claude", "--model", "m", "--json")
	var v ci.Verdict
	if err := json.Unmarshal([]byte(out), &v); err != nil || code != 0 || v.Verdict != ci.VerdictRequestChanges {
		t.Errorf("--json: exit %d, %v, %q", code, err, out)
	}
	if !strings.Contains(errOut, "note: No checklist") {
		t.Errorf("--json keeps notes on stderr, got %q", errOut)
	}

	out, _, code = runReview(t, "--provider", "claude", "--model", "m", "--base", "feature")
	if code != 0 || out != "Nothing to review: no changes against feature.\n" {
		t.Errorf("an empty diff: exit %d, %q", code, out)
	}
}

// A review that does not complete warns and exits 0, unless --blocking.
func TestCIReviewThatDoesNotComplete(t *testing.T) {
	tests := []struct {
		name    string
		exit    string
		fixture []string
		args    []string
		want    string
	}{
		{"an answer with no verdict", "0", []string{"claude", "2.1.269", "success.json"}, nil,
			`unreadable answer from claude: no JSON object in the reviewer's answer, which begins: "ok"`},
		{"a provider that fails", "1", []string{"claude", "2.1.269", "unrecognized-model.log"}, nil, "claude failed: "},
		{"a base that does not exist", "0", realVerdict, []string{"--base", "origin/nope"}, "git diff --merge-base origin/nope"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reviewRepo(t)
			fakeReviewer(t, tt.exit, tt.fixture...)
			args := append([]string{"--provider", "claude", "--model", "m"}, tt.args...)

			out, errOut, code := runReview(t, args...)
			if code != 0 || out != "" || !strings.Contains(errOut, "warning: orch ci review did not complete") || !strings.Contains(errOut, tt.want) {
				t.Errorf("advisory: exit %d, stdout %q, stderr %q", code, out, errOut)
			}
			_, errOut, code = runReview(t, append(args, "--blocking")...)
			if code != 1 || !strings.Contains(errOut, tt.want) {
				t.Errorf("--blocking: exit %d, stderr %q", code, errOut)
			}
		})
	}
}

func TestCIReviewDefaultBase(t *testing.T) {
	reviewRepo(t)
	fakeReviewer(t, "0", realVerdict...)
	if out, errOut, code := runReview(t, "--provider", "claude", "--model", "m"); code != 0 || !strings.Contains(out, "Changes requested") {
		t.Errorf("origin/main: exit %d, %s", code, errOut)
	}
	// runReview clears GITHUB_BASE_REF, so this run builds the command itself.
	t.Setenv("GITHUB_BASE_REF", "release")
	cmd := newCICmd(&projectFlags{}, "dev")
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	var errOut bytes.Buffer
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"review", "--provider", "claude", "--model", "m"})
	if err := cmd.Execute(); err != nil || !strings.Contains(errOut.String(), "git diff --merge-base origin/release") {
		t.Errorf("GITHUB_BASE_REF=release: %v, %q", err, errOut.String())
	}
}

type postedComment struct {
	repo, marker, body string
	pr                 int
}

func stubPost(t *testing.T, err error) *[]postedComment {
	t.Helper()
	var posts []postedComment
	old := postReviewComment
	t.Cleanup(func() { postReviewComment = old })
	postReviewComment = func(repo string, pr int, marker, body string) (string, bool, error) {
		posts = append(posts, postedComment{repo, marker, body, pr})
		return "https://github.com/o/r/pull/7#issuecomment-1", true, err
	}
	return &posts
}

func TestCIReviewPost(t *testing.T) {
	dir := reviewRepo(t)
	fakeReviewer(t, "0", realVerdict...)
	event := filepath.Join(dir, "event.json")
	if err := os.WriteFile(event, []byte(`{"pull_request":{"number":7}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	posts := stubPost(t, nil)

	// runReview clears the Actions variables, which this test needs set.
	post := func(args ...string) (string, int) {
		t.Helper()
		cmd := newCICmd(&projectFlags{}, "dev")
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		var errOut bytes.Buffer
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&errOut)
		cmd.SetArgs(append([]string{"review", "--provider", "claude", "--model", "m", "--post"}, args...))
		err := cmd.Execute()
		var ee *exitError
		if errors.As(err, &ee) {
			return errOut.String() + err.Error(), ee.code
		}
		if err != nil {
			return err.Error(), 1
		}
		return errOut.String(), 0
	}
	t.Setenv("GITHUB_REPOSITORY", "o/r")
	t.Setenv("GITHUB_EVENT_PATH", event)
	t.Setenv("GITHUB_BASE_REF", "")

	errOut, code := post()
	if code != 0 || len(*posts) != 1 || !strings.Contains(errOut, "Posted the review comment: https://github.com/o/r/pull/7#issuecomment-1") {
		t.Fatalf("exit %d, %d posts, stderr %q", code, len(*posts), errOut)
	}
	p := (*posts)[0]
	if p.repo != "o/r" || p.pr != 7 || p.marker != ci.CommentMarker ||
		!strings.HasPrefix(p.body, ci.CommentMarker+"\n## orch review (claude, m)") {
		t.Errorf("posted %+v", p)
	}

	if _, code := post("--pr", "12"); code != 0 || (*posts)[1].pr != 12 {
		t.Errorf("--pr should win over the event: exit %d, %+v", code, (*posts)[1])
	}

	stubPost(t, errors.New("gh: HTTP 403"))
	if errOut, code := post(); code != 0 || !strings.Contains(errOut, "warning: orch ci review did not complete") {
		t.Errorf("a failed post without --blocking: exit %d, %q", code, errOut)
	}
	if _, code := post("--blocking"); code != 1 {
		t.Errorf("a failed post with --blocking: exit %d", code)
	}

	t.Setenv("GITHUB_EVENT_PATH", filepath.Join(dir, "push.json"))
	if err := os.WriteFile(filepath.Join(dir, "push.json"), []byte(`{"ref":"refs/heads/main"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if errOut, code := post(); code != 2 || !strings.Contains(errOut, "no pull_request.number") {
		t.Errorf("an event without a PR: exit %d, %q", code, errOut)
	}
	t.Setenv("GITHUB_EVENT_PATH", "")
	if errOut, code := post(); code != 2 || !strings.Contains(errOut, "--post needs --pr or GITHUB_EVENT_PATH") {
		t.Errorf("no PR at all: exit %d, %q", code, errOut)
	}
	t.Setenv("GITHUB_REPOSITORY", "")
	if errOut, code := post("--pr", "1"); code != 2 || !strings.Contains(errOut, "--post needs GITHUB_REPOSITORY") {
		t.Errorf("no repository: exit %d, %q", code, errOut)
	}
}

func TestCIReviewUsageErrors(t *testing.T) {
	reviewRepo(t)
	fakeReviewer(t, "0", realVerdict...)
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"no provider", []string{"--model", "m"}, `--provider "": expected claude, gemini, codex or opencode`},
		{"a provider review does not run", []string{"--provider", "agy", "--model", "m"}, `--provider "agy"`},
		{"no model", []string{"--provider", "claude"}, "--model is required"},
		{"a zero timeout", []string{"--provider", "claude", "--model", "m", "--timeout", "0s"}, "must be positive"},
		{"an unknown flag", []string{"--provider", "claude", "--model", "m", "--bogus"}, "unknown flag: --bogus"},
		{"a positional argument", []string{"--provider", "claude", "--model", "m", "extra"}, "unknown command"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, errOut, code := runReview(t, tt.args...)
			if code != 2 || !strings.Contains(errOut, tt.want) {
				t.Errorf("exit %d, stderr %q, want 2 and %q", code, errOut, tt.want)
			}
		})
	}
}
