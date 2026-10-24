package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/hectorcanaimero/orch/internal/config"
	"github.com/hectorcanaimero/orch/internal/engine"
	"github.com/hectorcanaimero/orch/internal/knowledge"
	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/router"
	"github.com/hectorcanaimero/orch/internal/state"
)

// curatePrefix starts every curation task's id: knowledge-curate-<utc time>.
const curatePrefix = "knowledge-curate-"

// curateLogMax caps the commit list a curation prompt carries.
const curateLogMax = 200

func newKnowledgeCurateCmd(flags *projectFlags) *cobra.Command {
	var (
		backend string
		yes     bool
	)
	cmd := &cobra.Command{
		Use:   "curate [--backend X] [--yes]",
		Short: "Dispatch one task that merges, fixes and prunes the lessons, as a PR touching only .orch/knowledge/",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			req := curateRequest{Backend: backend, Out: cmd.OutOrStdout()}
			if !yes {
				in := bufio.NewReader(cmd.InOrStdin())
				req.Confirm = func(string) (bool, error) { return askYesNo(in, cmd.OutOrStdout(), "Dispatch it?", false) }
			}
			return curateKnowledge(cmd.Context(), flags, req)
		},
	}
	cmd.Flags().StringVar(&backend, "backend", "", "Run it on this CLI's cheapest route (default: the cheapest route of any installed CLI)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Dispatch without asking after the estimate")
	return cmd
}

// curateRequest is one curation, as curateKnowledge takes it.
type curateRequest struct {
	// Backend limits the route to this CLI; "" is the cheapest route of any
	// installed one.
	Backend string
	// Confirm is asked with the estimate before anything is dispatched; nil
	// dispatches without asking.
	Confirm func(estimate string) (bool, error)
	// Out gets the estimate and what was dispatched.
	Out io.Writer
	// State, when set, is the database the curation writes through; the
	// caller closes it. The dashboard passes its own (CHECKLIST rule 17).
	State state.Backend
	// NoPush is `orch run --no-push`'s.
	NoPush bool
}

// curateKnowledge is `orch knowledge curate` (#452) minus cobra — the one
// entry point, which the dashboard's Knowledge page calls too. It builds one
// task whose prompt is the current lessons and the commits since the last
// curation, routes it to the cheapest route, shows its estimate, and
// dispatches it through runProject — `orch run`'s own path, with its budget,
// spend records, worktree and PR (or local merge). Worktree mode is forced, so
// the result is always a branch to review and never a rewrite in place, and
// its only gate is curateScope: work touching anything outside
// .orch/knowledge/ is sent back to the agent and never published.
func curateKnowledge(ctx context.Context, flags *projectFlags, req curateRequest) error {
	out := req.Out
	if out == nil {
		out = io.Discard
	}
	paths, cfg, err := loadProjectConfig(flags)
	if err != nil {
		return withExitCode(1, err)
	}
	backend := req.State
	if backend == nil {
		opened, closeDB, err := openBackend(ctx, paths, cfg)
		if err != nil {
			return withExitCode(1, err)
		}
		defer func() { _ = closeDB() }()
		backend = opened
	}
	lessons, err := knowledge.Load(paths.Root)
	if err != nil {
		return withExitCode(1, err)
	}
	if len(lessons) == 0 {
		_, err := fmt.Fprintf(out, "no lessons to curate (%s is empty)\n", knowledge.Dir)
		return err
	}
	_, fake := engine.FakeProviderDir()
	installed := router.OnPath
	if fake {
		installed = func(model.Backend) bool { return true }
	}
	route, err := curateRoute(loadRouterTolerant(paths), req.Backend, installed)
	if err != nil {
		return withExitCode(2, err)
	}
	lastDone, _, err := lastCurations(ctx, backend)
	if err != nil {
		return withExitCode(1, err)
	}
	base := baseBranch(cfg)
	task := curateTask(route, lessons, gitLogSince(paths.Root, base, lastDone), lastDone)
	// A fresh id: a blocked curation's row keeps its own.
	stem := curatePrefix + time.Now().UTC().Format("20060102-150405")
	task.ID = stem
	for n := 2; ; n++ {
		rows, err := backend.Tasks(ctx, state.TaskFilter{IDs: []string{task.ID}})
		if err != nil {
			return withExitCode(1, err)
		}
		if len(rows) == 0 {
			break
		}
		task.ID = fmt.Sprintf("%s-%d", stem, n)
	}

	e, err := projectEstimate(ctx, backend, paths, cfg, []model.Task{task}, "", 0)
	if err != nil {
		return withExitCode(1, err)
	}
	_, _ = fmt.Fprintf(out, "curating %d lessons on %s as %s; the work goes to branch orch/%s\n", len(lessons), route, task.ID, task.ID)
	printEstimate(out, e)
	if req.Confirm != nil {
		ok, err := req.Confirm(e.Lines()[0])
		if err != nil {
			return withExitCode(1, err)
		}
		if !ok {
			_, _ = fmt.Fprintln(out, "aborted: nothing dispatched")
			return withSilentExitCode(1)
		}
	}
	return runProject(ctx, nil, out, flags, runOptions{
		mode:         string(engine.ModeAuto),
		worktreeMode: true,
		noPush:       req.NoPush,
		backend:      backend,
		tasks:        []model.Task{task},
		verifier:     curateScope{base: base},
	})
}

// curateRoute is the route key a curation runs on: the cheapest tier with a
// route on an installed CLI — only backend's, when one is named.
func curateRoute(rtr router.Router, backend string, installed func(model.Backend) bool) (string, error) {
	ok := func(b model.Backend) bool { return (backend == "" || string(b) == backend) && installed(b) }
	for _, tier := range []model.Tier{model.TierCheap, model.TierStandard, model.TierPremium} {
		if k := rtr.ForTier(tier, ok); k != "" {
			return k, nil
		}
	}
	if backend != "" {
		return "", fmt.Errorf("no route in model_router.yaml runs on %s, or %s is not installed", backend, backend)
	}
	return "", errors.New("no route in model_router.yaml runs on an installed CLI")
}

// lastCurations reads the curation tasks in the database: when the last one
// finished (zero for none), and when the last one of any status started.
func lastCurations(ctx context.Context, backend state.Backend) (done, started time.Time, err error) {
	rows, err := backend.Tasks(ctx, state.TaskFilter{})
	if err != nil {
		return done, started, fmt.Errorf("reading the tasks: %w", err)
	}
	for _, r := range rows {
		if !strings.HasPrefix(r.ID, curatePrefix) {
			continue
		}
		if t, ok := state.ParseTS(r.FinishedAt); ok && r.Status == model.StatusDone && t.After(done) {
			done = t
		}
		for _, ts := range []string{r.StartedAt, r.UpdatedAt} {
			if t, ok := state.ParseTS(ts); ok && t.After(started) {
				started = t
			}
		}
	}
	return done, started, nil
}

// gitLogSince lists the base branch's commits since t (all of them for the
// zero time), newest first, at most curateLogMax, with the files each touched.
func gitLogSince(root, base string, since time.Time) string {
	args := []string{"-C", root, "log", "--format=%h %ad %s", "--date=short", "--name-status", fmt.Sprintf("-n%d", curateLogMax)}
	if !since.IsZero() {
		args = append(args, "--since="+since.Format(time.RFC3339))
	}
	out, err := exec.Command("git", append(args, base, "--")...).CombinedOutput() // #nosec G204 -- fixed git arguments; root and base are the project's
	if err != nil {
		return fmt.Sprintf("(git log failed: %v: %s)", err, strings.TrimSpace(string(out)))
	}
	if strings.TrimSpace(string(out)) == "" {
		return "(none)"
	}
	return strings.TrimRight(string(out), "\n")
}

// curateTask is the curation's task, but for its id: its prompt is its
// description.
func curateTask(route string, lessons []knowledge.Lesson, log string, since time.Time) model.Task {
	var b strings.Builder
	fmt.Fprintf(&b, `Curate this project's lessons in %[1]s/: one markdown file each, YAML front
matter (id, title, area, tags, source, task, created) then a short body. Later
tasks get them in their prompt, so they must stay few, short and true.

- Merge duplicates: keep one file, fold in what the others add, delete the others.
- Two lessons that contradict each other: rewrite them into one that says what
  holds now. Read the code to decide.
- Delete a lesson the code no longer supports: read the files and areas it
  names. The commits below show what changed.
- Keep each body within %[2]d lines and %[3]d characters, and every file's front
  matter valid.
- Change nothing outside %[1]s/: work that touches any other file is sent back
  and never published.

Current lessons (%[4]d):
`, knowledge.Dir, knowledge.MaxLines, knowledge.MaxChars, len(lessons))
	for _, l := range lessons {
		path := l.Path
		l.Path = ""
		fmt.Fprintf(&b, "\n--- %s\n%s", path, knowledge.Marshal(l))
	}
	if since.IsZero() {
		fmt.Fprintf(&b, "\nCommits (no curation yet; the latest %d):\n", curateLogMax)
	} else {
		fmt.Fprintf(&b, "\nCommits since the last curation (%s):\n", since.Format(time.RFC3339))
	}
	b.WriteString(log)
	return model.Task{
		Title:         "Curate the project's lessons (" + knowledge.Dir + "/)",
		Description:   b.String(),
		Model:         route,
		Reason:        "orch knowledge curate",
		Status:        model.StatusTodo,
		EstimateHours: 0.5,
		Files:         []string{knowledge.Dir + "/"},
	}
}

func baseBranch(cfg config.Config) string {
	if cfg.Dispatch.BaseBranch == "" {
		return "main"
	}
	return cfg.Dispatch.BaseBranch
}

// curateScope is a curation's only gate before its work is published: every
// changed file is under knowledge.Dir, every lesson still parses and fits the
// prompt's cap. Unlike verify.enabled's checks it fails closed — a scope it
// cannot read is not a scope it can vouch for.
type curateScope struct{ base string }

func (s curateScope) Verify(ctx context.Context, req engine.VerifyRequest) engine.VerifyResult {
	ref := s.base
	if exec.CommandContext(ctx, "git", "-C", req.Dir, "rev-parse", "--verify", "--quiet", "origin/"+ref+"^{commit}").Run() == nil { // #nosec G204 -- fixed git arguments
		ref = "origin/" + ref
	}
	out, err := exec.CommandContext(ctx, "git", "-C", req.Dir, "diff", "--name-only", "--merge-base", ref, "--").Output() // #nosec G204 -- fixed git arguments
	if err != nil {
		return engine.VerifyResult{Failure: fmt.Sprintf("orch could not list the files you changed against %s (%v), so it cannot publish the curation.\n", ref, err)}
	}
	var problems, outside []string
	for _, f := range strings.Split(string(out), "\n") {
		if f = strings.TrimSpace(f); f != "" && !strings.HasPrefix(f, knowledge.Dir+"/") {
			outside = append(outside, f)
		}
	}
	if len(outside) > 0 {
		problems = append(problems, fmt.Sprintf("You changed files outside %s/: %s. A curation changes only the lessons: "+
			"restore them with `git checkout %s -- <file>` and keep your lesson edits.", knowledge.Dir, strings.Join(outside, ", "), ref))
	}
	lessons, err := knowledge.Load(req.Dir)
	if err != nil {
		problems = append(problems, "A lesson no longer parses: "+err.Error())
	}
	for _, l := range lessons {
		if n := strings.Count(l.Body, "\n") + 1; n > knowledge.MaxLines || utf8.RuneCountInString(l.Body) > knowledge.MaxChars {
			problems = append(problems, fmt.Sprintf("%s is over the cap (%d lines, %d characters; at most %d and %d).",
				l.Path, n, utf8.RuneCountInString(l.Body), knowledge.MaxLines, knowledge.MaxChars))
		}
	}
	if len(problems) == 0 {
		return engine.VerifyResult{}
	}
	return engine.VerifyResult{Failure: strings.Join(problems, "\n") + "\n"}
}

// curateIfDue runs a curation at the end of `orch run` once
// knowledge.curate_every lessons were added since the last one started (or
// ever, before the first). Lessons carry only a date, so one added the day a
// curation ran counts from the next day. 0 never curates.
func curateIfDue(ctx context.Context, w io.Writer, flags *projectFlags, noPush bool) error {
	paths, cfg, err := loadProjectConfig(flags)
	if err != nil || cfg.Knowledge.CurateEvery <= 0 {
		return nil
	}
	lessons, err := knowledge.Load(paths.Root)
	if err != nil {
		return nil
	}
	backend, closeDB, err := openBackend(ctx, paths, cfg)
	if err != nil {
		return nil
	}
	_, last, err := lastCurations(ctx, backend)
	_ = closeDB()
	if err != nil {
		return nil
	}
	n := newLessons(lessons, last)
	if n < cfg.Knowledge.CurateEvery {
		return nil
	}
	_, _ = fmt.Fprintf(os.Stderr, "note: lessons added since the last curation: %d (knowledge.curate_every: %d) — curating them\n", n, cfg.Knowledge.CurateEvery)
	return curateKnowledge(ctx, flags, curateRequest{Out: w, NoPush: noPush})
}

// newLessons counts the lessons created after since's day; all of them for
// the zero time.
func newLessons(lessons []knowledge.Lesson, since time.Time) int {
	n := 0
	for _, l := range lessons {
		t, err := time.Parse("2006-01-02", l.Created)
		if since.IsZero() || (err == nil && t.After(since.UTC().Truncate(24*time.Hour))) {
			n++
		}
	}
	return n
}
