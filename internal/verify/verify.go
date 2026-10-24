// Package verify is orch's local gate between an agent's clean exit and the
// push (verify.enabled): the repository's own checks, then a read-only review
// by another provider. What fails goes back to the agent; only work that
// passes is published.
//
// It lives outside internal/engine because it drives the review through
// internal/ci, which imports the engine; the engine sees it only as its
// Verifier interface.
package verify

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/hectorcanaimero/orch/internal/ci"
	"github.com/hectorcanaimero/orch/internal/config"
	"github.com/hectorcanaimero/orch/internal/engine"
	"github.com/hectorcanaimero/orch/internal/model"
)

// maxDiffBytes cuts the reviewed diff, file by file: `orch ci review`'s default.
const maxDiffBytes = 200_000

// outputTail is how much of a failing command's output the agent is shown.
// The end is where test runners and compilers put the failure.
const outputTail = 8_000

// Verifier is the gate. The zero Cfg verifies nothing: callers build one only
// when verify.enabled is on.
type Verifier struct {
	Cfg config.Verify
	// Base is the branch tasks start from; the review reads the diff against
	// it (origin/<Base> when there is one, as the worktree started there).
	Base string
	// RequireTests sends back a change that touches no test file
	// (dispatch.tdd).
	RequireTests bool
}

// Verify checks the work in dir, a task's worktree with the agent's work
// committed. It returns what failed, written for the agent's next attempt, or
// "" when the work passes.
//
// A check that cannot run is not the agent's fault and does not fail it: a
// detected command whose tool is not installed is skipped, so is a configured
// one sh cannot find (a warning names its config key), and a review that
// cannot complete (no other provider, a provider that is down, an answer that
// does not parse) is logged and passes, as `orch ci review` without
// --blocking does. A gate that blocks on its own breakage teaches people to
// turn it off.
func (v Verifier) Verify(ctx context.Context, req engine.VerifyRequest) engine.VerifyResult {
	timeout := time.Duration(v.Cfg.TimeoutS) * time.Second
	if v.RequireTests {
		if failure := v.requireTests(ctx, req.TaskID, req.Dir); failure != "" {
			return engine.VerifyResult{Failure: failure}
		}
	}
	var ran []engine.VerifyCommand
	for _, c := range v.commands(req.TaskID, req.Dir) {
		if out, err := timed(&ran, c, func() (string, error) { return run(ctx, req.Dir, req.Env, c, timeout) }); notFound(err) {
			skipNotFound(req.TaskID, "verify.commands", c, out)
		} else if err != nil {
			return engine.VerifyResult{Failure: commandFailed(c, err, out, ""), Commands: ran}
		}
	}
	res := v.e2e(ctx, req, timeout)
	res.Commands = append(ran, res.Commands...)
	if res.Failure != "" || !v.Cfg.Review {
		return res
	}
	res.Failure = v.review(ctx, req.TaskID, req.Writer, req.Dir, timeout, &res)
	return res
}

// timed runs f, the check command, and adds it to ran with how long it took
// unless sh could not find it.
func timed(ran *[]engine.VerifyCommand, command string, f func() (string, error)) (string, error) {
	start := time.Now()
	out, err := f()
	if !notFound(err) {
		*ran = append(*ran, engine.VerifyCommand{Command: command, DurationS: time.Since(start).Round(time.Millisecond).Seconds()})
	}
	return out, err
}

func commandFailed(command string, err error, out, context string) string {
	return fmt.Sprintf("orch's local verify ran `%s` in your working dir%s and it failed (%v):\n\n```\n%s\n```\n",
		command, context, err, tail(out))
}

// e2e brings the task's app up (verify.app), waits for it to answer, runs
// verify.e2e against it with ORCH_APP_URL set, and takes it down again,
// whatever happened. An app that does not start or never answers is sent
// back like a failing test: most often it is the change that broke it, and
// when it is the machine instead, the agent can block the task and say so.
func (v Verifier) e2e(ctx context.Context, req engine.VerifyRequest, timeout time.Duration) engine.VerifyResult {
	app := v.Cfg.App
	env := req.Env
	appURL := ""
	if app.Start != "" {
		if app.Stop != "" {
			defer func() {
				// A fresh context: the app comes down even when ctx is done.
				if out, err := run(context.Background(), req.Dir, req.Env, app.Stop, timeout); err != nil {
					slog.Warn("verify: stopping the app failed", "task", req.TaskID, "err", err, "output", tail(out))
				}
			}()
		}
		if out, err := run(ctx, req.Dir, req.Env, app.Start, timeout); notFound(err) {
			// The E2E is for the app: without it there is nothing to run.
			skipNotFound(req.TaskID, "verify.app.start", app.Start, out)
			return engine.VerifyResult{}
		} else if err != nil {
			return engine.VerifyResult{Failure: "The task's app did not start. " + commandFailed(app.Start, err, out, "")}
		}
		if app.URL != "" {
			url, err := expandURL(ctx, req.Dir, req.Env, app.URL)
			if err != nil {
				slog.Warn("verify: expanding verify.app.url failed; not waiting for the app", "task", req.TaskID, "err", err)
			} else {
				ready := time.Duration(app.ReadyTimeoutS) * time.Second
				if err := waitReady(ctx, url, ready); err != nil {
					return engine.VerifyResult{Failure: fmt.Sprintf("orch's local verify started the task's app with `%s`, and it never answered at %s (%v). "+
						"Check that the change still builds and serves.\n", app.Start, url, err)}
				}
				env = withVar(env, "ORCH_APP_URL="+url)
				appURL = url
			}
		}
	}
	report := ""
	if v.Cfg.E2EReport != "" {
		// A report left by an earlier attempt must not pass for this one's.
		report = filepath.Join(req.Dir, filepath.FromSlash(v.Cfg.E2EReport))
		_ = os.Remove(report)
	}
	var res engine.VerifyResult
	for _, c := range v.Cfg.E2E {
		if out, err := timed(&res.Commands, c, func() (string, error) { return run(ctx, req.Dir, env, c, timeout) }); notFound(err) {
			skipNotFound(req.TaskID, "verify.e2e", c, out)
		} else if err != nil {
			res.Failure = commandFailed(c, err, out, " against the running app")
			break
		}
	}
	if report == "" || len(v.Cfg.E2E) == 0 {
		return res
	}
	rep, err := collectE2E(report, req.Dir, req.ArtifactsDir, req.TaskID, appURL)
	if err != nil {
		slog.Warn("verify: the E2E report could not be read; keeping the command's verdict", "task", req.TaskID, "err", err)
		return res
	}
	res.E2E = &engine.E2ESummary{
		Passed: rep.Stats.Passed, Failed: rep.Stats.Failed, Flaky: rep.Stats.Flaky, Skipped: rep.Stats.Skipped,
		Report: filepath.Join(req.ArtifactsDir, ReportFile),
	}
	if res.Failure != "" && (rep.Stats.Failed > 0 || len(rep.Errors) > 0) {
		// The report says it better than the command's output.
		res.Failure = "orch's local verify ran the E2E tests. " + rep.failureSummary()
	}
	return res
}

// expandURL has the shell expand verify.app.url with the task's environment,
// so `$((3000 + ORCH_TASK_SLOT))` gives each task its own port.
func expandURL(ctx context.Context, dir string, env []string, url string) (string, error) {
	out, err := run(ctx, dir, env, `printf '%s' "`+strings.ReplaceAll(url, `"`, `\"`)+`"`, time.Minute)
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, tail(out))
	}
	return strings.TrimSpace(out), nil
}

// readyPoll is how often waitReady asks.
var readyPoll = time.Second

// waitReady polls url until it answers below 500, or ready runs out. A 404 is
// ready: the server is up, and the E2E tests say what is missing.
func waitReady(ctx context.Context, url string, ready time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, ready)
	defer cancel()
	client := &http.Client{Timeout: 5 * time.Second}
	var last error
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req) // #nosec G704 -- the operator's own verify.app.url
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode < 500 {
				return nil
			}
			err = fmt.Errorf("it answered %s", resp.Status)
		}
		last = err
		select {
		case <-ctx.Done():
			return fmt.Errorf("gave up after %s: %w", ready, last)
		case <-time.After(readyPoll):
		}
	}
}

// withVar is env with kv added, starting from this process's environment
// when env is nil (which exec reads as "inherit").
func withVar(env []string, kv string) []string {
	if env == nil {
		env = os.Environ()
	}
	return append(slices.Clone(env), kv)
}

// commands are the configured ones, or what `orch ci setup` detects.
func (v Verifier) commands(taskID, dir string) []string {
	if len(v.Cfg.Commands) > 0 {
		return v.Cfg.Commands
	}
	st, err := ci.Detect(dir)
	if err != nil {
		slog.Warn("verify: detecting the repository's checks failed; running none", "task", taskID, "err", err)
		return nil
	}
	var out []string
	for _, p := range st.Packages {
		for _, c := range []string{p.Install, p.Lint, p.Typecheck, p.Test, p.Build} {
			if c == "" {
				continue
			}
			if tool, _, _ := strings.Cut(c, " "); !onPath(tool) {
				slog.Warn("verify: skipping a check whose tool is not installed", "task", taskID, "command", c)
				continue
			}
			if p.Dir != "." {
				c = "cd " + shellQuote(p.Dir) + " && " + c
			}
			out = append(out, c)
		}
	}
	return out
}

// run is `sh -c command` in dir with env (nil inherits this process's),
// bounded by timeout (checklist rule 18).
func run(ctx context.Context, dir string, env []string, command string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// #nosec G204 -- the operator's own verify.commands, or commands detected from their repository.
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = dir
	cmd.Env = env
	cmd.WaitDelay = 5 * time.Second
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if ctx.Err() != nil {
		err = fmt.Errorf("timed out after %s", timeout)
	}
	return out.String(), err
}

// notFound reports whether sh could not find the command (exit 127): a typo
// or a tool missing from the machine, which the agent cannot fix (#433).
func notFound(err error) bool {
	var exit *exec.ExitError
	return errors.As(err, &exit) && exit.ExitCode() == 127
}

func skipNotFound(taskID, key, command, out string) {
	slog.Warn("verify: a configured command was not found; skipping it, check "+key+" in config.yaml",
		"task", taskID, "command", command, "output", tail(out))
}

// review asks another provider to review the diff, and returns its blocking
// findings for the agent, or "". A review that completes names its reviewer
// and all its findings in res.
func (v Verifier) review(ctx context.Context, taskID string, writer model.Backend, dir string, timeout time.Duration, res *engine.VerifyResult) string {
	reviewer := v.reviewer(writer)
	if reviewer == "" {
		slog.Warn("verify: no other provider is installed to review; skipping the review", "task", taskID, "writer", writer)
		return ""
	}
	diff, err := v.diff(ctx, dir)
	if err != nil {
		slog.Warn("verify: reading the diff failed; skipping the review", "task", taskID, "err", err)
		return ""
	}
	if strings.TrimSpace(diff) == "" {
		return ""
	}
	diff, cut := ci.TruncateDiff(diff, maxDiffBytes)
	answer, err := ci.Ask(ctx, reviewer, v.Cfg.ReviewModel, ci.BuildPrompt(checklist(dir, v.Cfg.Checklist), diff, cut), timeout)
	if err != nil {
		slog.Warn("verify: the review did not complete; passing the work", "task", taskID, "reviewer", reviewer, "err", err)
		return ""
	}
	verdict, err := ci.ParseVerdict(answer)
	if err != nil {
		slog.Warn("verify: the review's answer did not parse; passing the work", "task", taskID, "reviewer", reviewer, "err", err)
		return ""
	}
	res.Reviewer, res.ReviewModel = string(reviewer), v.Cfg.ReviewModel
	for _, f := range verdict.Findings {
		res.Findings = append(res.Findings, engine.VerifyFinding(f))
	}
	if !verdict.Blocks() {
		return ""
	}
	return "orch's local verify had your work reviewed and the review asks for changes. " +
		"Fix the blocking findings:\n\n" + verdict.Markdown(string(reviewer), nil)
}

// reviewer is verify.reviewer, or the first installed review backend that did
// not write the task: a model reviewing its own work misses what it missed.
func (v Verifier) reviewer(writer model.Backend) model.Backend {
	if v.Cfg.Reviewer != "" {
		return model.Backend(v.Cfg.Reviewer)
	}
	for _, b := range ci.ReviewBackends {
		if b != writer && onPath(string(b)) {
			return b
		}
	}
	return ""
}

// diff is what the task changed, against its base.
func (v Verifier) diff(ctx context.Context, dir string) (string, error) {
	return ci.Diff(ctx, dir, v.baseRef(ctx, dir))
}

// baseRef is origin/<Base> when the clone has it, as the worktree started
// there, and <Base> otherwise.
func (v Verifier) baseRef(ctx context.Context, dir string) string {
	base := v.Base
	if base == "" {
		base = "main"
	}
	if _, err := run(ctx, dir, nil, "git rev-parse --verify --quiet "+shellQuote("origin/"+base+"^{commit}"), time.Minute); err == nil {
		return "origin/" + base
	}
	return base
}

// requireTests sends back a change that touches no test file. An empty
// change passes here: the other checks decide what nothing means.
func (v Verifier) requireTests(ctx context.Context, taskID, dir string) string {
	out, err := run(ctx, dir, nil, "git diff --name-only --merge-base "+shellQuote(v.baseRef(ctx, dir))+" --", time.Minute)
	if err != nil {
		slog.Warn("verify: listing the changed files failed; not checking for tests", "task", taskID, "err", err)
		return ""
	}
	var changed []string
	for _, f := range strings.Split(out, "\n") {
		if f = strings.TrimSpace(f); f != "" {
			changed = append(changed, f)
		}
	}
	if len(changed) == 0 || slices.ContainsFunc(changed, IsTestFile) {
		return ""
	}
	if len(changed) > 10 {
		changed = append(changed[:10], "…")
	}
	return "dispatch.tdd is on, and your change touches no test file (it changes " + strings.Join(changed, ", ") + "). " +
		"Write a test for each acceptance criterion with the framework the repository already uses, " +
		"see it fail for the expected reason, then make it pass.\n"
}

// IsTestFile reports whether a path, as git prints it, is a test by the
// naming conventions of the ecosystems orch detects: Go's _test.go, the
// .test./.spec. of JavaScript and TypeScript, pytest's test_*.py and
// *_test.py, the *Test/*Tests classes of the JVM and .NET, and any file under
// a tests/, test/, __tests__/ or spec/ directory.
func IsTestFile(p string) bool {
	base := path.Base(p)
	stem := strings.TrimSuffix(base, path.Ext(base))
	switch {
	case strings.HasSuffix(base, "_test.go"),
		strings.Contains(base, ".test."), strings.Contains(base, ".spec."),
		strings.HasSuffix(base, ".py") && (strings.HasPrefix(base, "test_") || strings.HasSuffix(stem, "_test")),
		strings.HasSuffix(stem, "Test"), strings.HasSuffix(stem, "Tests"):
		return true
	}
	for _, dir := range strings.Split(path.Dir(p), "/") {
		switch dir {
		case "tests", "test", "__tests__", "spec":
			return true
		}
	}
	return false
}

// checklist is the configured file, else .orch/review.md, else
// .github/orch-review.md, read from the task's worktree so it is the one on
// the task's branch; else the built-in checklist.
func checklist(dir, configured string) string {
	candidates := []string{".orch/review.md", ".github/orch-review.md"}
	if configured != "" {
		candidates = []string{configured}
	}
	for _, c := range candidates {
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(path.Clean(c)))) // #nosec G304 -- a checklist path in the operator's own repository
		if err == nil && strings.TrimSpace(string(b)) != "" {
			return string(b)
		}
	}
	return ci.BuiltinChecklist
}

func onPath(tool string) bool {
	_, err := exec.LookPath(tool)
	return err == nil
}

func tail(s string) string {
	if len(s) <= outputTail {
		return strings.TrimRight(s, "\n")
	}
	return "…" + strings.TrimRight(s[len(s)-outputTail:], "\n")
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
