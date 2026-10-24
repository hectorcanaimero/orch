package cli

import (
	"context"
	"errors"
	"fmt"
	"github.com/hectorcanaimero/orch/internal/ci"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hectorcanaimero/orch/internal/budget"
	"github.com/hectorcanaimero/orch/internal/config"
	"github.com/hectorcanaimero/orch/internal/doctor"
	"github.com/hectorcanaimero/orch/internal/engine"
	"github.com/hectorcanaimero/orch/internal/knowledge"
	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/notify"
	"github.com/hectorcanaimero/orch/internal/pricing"
	"github.com/hectorcanaimero/orch/internal/providers"
	"github.com/hectorcanaimero/orch/internal/receipt"
	"github.com/hectorcanaimero/orch/internal/scaffold"
	"github.com/hectorcanaimero/orch/internal/state"
	"github.com/hectorcanaimero/orch/internal/vcs"
	"github.com/hectorcanaimero/orch/internal/verify"
	"github.com/hectorcanaimero/orch/internal/worktree"
)

// newRunCmd ports `orch run` (orchestrator/orch.py's main loop). The loop
// itself lives in internal/engine; this resolves the project, builds the
// scheduler, and translates the outcome into an exit code.
//
// Flags are the subset the engine actually honours today. Python's `run` also
// takes `--budgets-preset`, `--dry-run` and a few more; they are deliberately
// not registered rather than accepted and ignored, which is the failure mode
// this project has now found four times (`concurrency.per_file` among them) —
// a flag or key that promises a behaviour nothing implements. They arrive in
// the PR that makes them do something. `docs/CLI.md` records the gap.
func newRunCmd(flags *projectFlags) *cobra.Command {
	var opts runOptions

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Walk the DAG, dispatching ready tasks to their CLI agents",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := runProject(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), flags, opts); err != nil {
				return err
			}
			// knowledge.curate_every (#452), once the run's lock is released.
			return curateIfDue(cmd.Context(), cmd.OutOrStdout(), flags, opts.noPush)
		},
	}

	cmd.Flags().StringVar(&opts.mode, "mode", string(engine.ModeAuto),
		"auto dispatches everything ready; semi asks before each critical task")
	cmd.Flags().IntVar(&opts.maxTasks, "max-tasks", 0,
		"Start at most this many tasks (their retries do not count); 0 means no limit")
	cmd.Flags().StringVar(&opts.only, "only", "",
		"Only dispatch tasks whose id matches this glob (dependencies still resolve across the whole DAG)")
	cmd.Flags().BoolVar(&opts.taskLocks, "task-locks", false,
		"Take a per-task lock, so several orch instances can share one project")
	cmd.Flags().BoolVar(&opts.worktreeMode, "worktree-mode", false,
		"Give each task its own git worktree and branch (also settable as dispatch.worktree_mode)")
	cmd.Flags().BoolVar(&opts.noPush, "no-push", false,
		"Skip pushing task branches — for a project with no remote")

	return cmd
}

// runOptions are `orch run`'s flags, as runProject takes them.
type runOptions struct {
	mode         string
	maxTasks     int
	only         string
	taskLocks    bool
	worktreeMode bool
	noPush       bool
	// runID names the run; empty means a fresh session id. `orch bench`
	// picks it up front so it can read the run's receipt while it is going.
	runID string
	// isolated is `orch bench`'s run: no worktrees, no pushes, no PRs, no CI
	// polling and no notifications, whatever the project's config says. A
	// benchmark copy has no git history of its own, must never open a PR on
	// the real remote, and is not news for the project's Slack channel.
	isolated bool
	// backend, when set, is the database the run writes through instead of
	// opening its own; the caller closes it. `orch bench` reads the run's
	// spend while it goes, and doing that through the run's own backend keeps
	// one writer on the file (CHECKLIST rule 17).
	backend state.Backend
	// tasks, when set, is the whole DAG this run dispatches instead of
	// tasks.json, which is then neither read nor reloaded: `orch knowledge
	// curate`'s one task (#452).
	tasks []model.Task
	// verifier, when set, is the only gate a task's work passes before it is
	// published, whatever verify.enabled says: curate's scope check.
	verifier engine.Verifier
}

// runProject is `orch run` minus cobra: the one code path that walks a
// project's DAG. `orch bench` calls it too, so a benchmark measures exactly
// what `orch run` does.
func runProject(ctx context.Context, in io.Reader, out io.Writer, flags *projectFlags, opts runOptions) error {
	switch engine.Mode(opts.mode) {
	case engine.ModeAuto, engine.ModeSemi:
	default:
		return withExitCode(1, fmt.Errorf(
			"--mode %q: expected %q or %q", opts.mode, engine.ModeAuto, engine.ModeSemi))
	}

	paths, cfg, err := loadProjectConfig(flags)
	if err != nil {
		return withExitCode(1, err)
	}

	runLock, err := takeRunLock(opts, paths.StateDir())
	if err != nil {
		return withExitCode(1, err)
	}
	defer runLock.Release()

	backend := opts.backend
	if backend == nil {
		opened, closeBackend, err := openBackend(ctx, paths, cfg)
		if err != nil {
			return withExitCode(1, err)
		}
		defer func() {
			if cerr := closeBackend(); cerr != nil {
				fmt.Fprintf(os.Stderr, "closing the state backend: %v\n", cerr)
			}
		}()
		backend = opened
	}

	tasks := opts.tasks
	if tasks == nil {
		tasks = loadDAG(paths)
	}
	if len(tasks) == 0 {
		return withExitCode(1, fmt.Errorf("no tasks in %s — plan them with: %s", paths.TasksJSON(), strings.Join(planSteps(), "  then  ")))
	}
	if err := backend.Bootstrap(ctx, tasks); err != nil {
		return withExitCode(1, fmt.Errorf("bootstrap: %w", err))
	}

	queue, err := engine.NewTaskQueue(tasks)
	if err != nil {
		return withExitCode(1, err)
	}
	// The database owns runtime status since F-12, so the queue
	// starts from what it says rather than from tasks.json's seed.
	if rows, rerr := backend.Tasks(ctx, state.TaskFilter{}); rerr != nil {
		fmt.Fprintf(os.Stderr, "reading task status; starting from tasks.json: %v\n", rerr)
	} else {
		runtime := make(map[string]model.Status, len(rows))
		for _, r := range rows {
			runtime[r.ID] = r.Status
		}
		queue.Hydrate(runtime)
	}

	worktreeMode := (opts.worktreeMode || cfg.Dispatch.WorktreeMode) && !opts.isolated
	autoPR := cfg.VCS.AutoPR && !opts.isolated
	// vcs.provider: local (#455): each finished task is squash-merged into
	// the base here; nothing is pushed and no PR is opened.
	localMerge := worktreeMode && cfg.VCS.Provider == config.VCSProviderLocal
	if worktreeMode {
		if problem := baseProblem(paths.Root, cfg.Dispatch.BaseBranch); problem != "" {
			return withExitCode(1, errors.New("cannot start: "+problem))
		}
		if localMerge {
			opts.noPush, autoPR = true, false
		}
		// A repository with no remote has nowhere to push or open a PR
		// (#376): the work stays on local branches, as doctor's vcs.remote
		// says, instead of every task blocking on a push that cannot work.
		if !hasGitRemote(paths.Root) {
			opts.noPush = true
			if autoPR {
				autoPR = false
				_, _ = fmt.Fprintln(os.Stderr, "note: this repository has no git remote — each task's work stays on its local orch/<task-id> branch, with no push and no pull request.")
			}
		}
	}

	// A typo in a configured command is found here, not mid-task as an exit
	// 127 verify skips (#440). A warning: the run goes ahead.
	for _, m := range doctor.UnresolvedCommands(paths.Root, cfg) {
		_, _ = fmt.Fprintln(os.Stderr, "warning: "+m.Detail+" — fix it in .orchestrator/config.yaml; until then verify skips it")
	}
	noteEstimate(ctx, os.Stderr, backend, paths, cfg, queue, opts)

	rtr := loadRouterTolerant(paths)
	warnMissingPermissions(os.Stderr, paths.Root, usedBackends(tasks, rtr, paths.TasksJSON()))
	if !opts.isolated {
		noteOutdatedCLIs(ctx, os.Stderr, usedBackends(tasks, rtr, paths.TasksJSON()), cliLatest(cfg))
	}
	runID := opts.runID
	if runID == "" {
		runID = providers.NewSessionID()
	}
	if err := backend.StartRun(ctx, runID, opts.mode); err != nil {
		return withExitCode(1, fmt.Errorf("starting the run: %w", err))
	}

	scheduler := engine.NewScheduler(queue, rtr, engine.SchedulerOptions{
		Mode:              engine.Mode(opts.mode),
		GlobalMax:         cfg.Concurrency.GlobalMax,
		PerProvider:       cfg.Concurrency.PerProvider,
		TimeoutMultiplier: cfg.DefaultTimeoutMult,
		Cfg:               cfg,
		UseTaskLocks:      opts.taskLocks,
		WorktreeMode:      worktreeMode,
		BaseBranch:        cfg.Dispatch.BaseBranch,
		AutoPR:            autoPR,
		LocalMerge:        localMerge,
		ConflictRetries:   conflictRetries(cfg),
		Only:              opts.only,
		MaxTasks:          opts.maxTasks,
		StateDir:          paths.StateDir(),
		DBDir:             filepath.Dir(paths.SQLitePath(cfg)),
		Cwd:               paths.Root,
		ProjectID:         paths.ID,
		RunID:             runID,
		BudgetUSD:         perDispatchCap(cfg),
	})
	scheduler.Backend = engine.NewStateRecorder(backend)
	// The same adapter, named separately because it answers a
	// different question: what a finished dependency reported, for
	// the prompt's context block.
	scheduler.Comments = engine.NewStateRecorder(backend)
	// And a dependency's PR, so one already merged is not merged into its
	// dependent's worktree a second time (#345).
	scheduler.PRs = engine.NewStateRecorder(backend)
	// What `orch provider disable` turned off, read every tick so a switch
	// flipped mid-run takes effect without a restart.
	scheduler.Providers = backend
	// The search index the prompt's project knowledge is ranked with (#451).
	if r, ok := backend.(knowledge.Ranker); ok && cfg.Knowledge.Enabled {
		scheduler.Knowledge = r
	}
	// Built unconditionally: with no webhook configured it is a
	// working no-op, so the engine never has to ask whether the
	// operator wanted notifications.
	notifyCfg := cfg
	if opts.isolated {
		notifyCfg.Notifications.SlackWebhook, notifyCfg.Notifications.DiscordWebhook = "", ""
	}
	notifier := newNotifier(notifyCfg)
	scheduler.Notify = notifier
	if engine.Mode(opts.mode) == engine.ModeSemi {
		scheduler.Gate = engine.TerminalGate{In: in, Out: out}
	}
	// Worktree isolation, and the auto-PR + CI chain that rides on
	// it. All three are off unless the project asked: a project with
	// worktree_mode off runs exactly as it did before, in its own
	// root.
	var wtManager engine.WorktreeManager
	if worktreeMode {
		wm := worktree.NewManager(paths.Root, !opts.noPush)
		wm.Setup = cfg.Dispatch.WorktreeSetup
		wtManager = engine.NewWorktreeManager(wm)
		scheduler.Worktree = wtManager
		if opts.verifier != nil {
			scheduler.Verifier = opts.verifier
		} else if cfg.Verify.Enabled {
			scheduler.Verifier = verify.Verifier{Cfg: cfg.Verify, Base: cfg.Dispatch.BaseBranch, RequireTests: cfg.Dispatch.TDD}
		}
	} else if cfg.Verify.Enabled {
		_, _ = fmt.Fprintln(os.Stderr, "note: verify.enabled needs dispatch.worktree_mode; the work is not verified before it is published")
	}

	var provider vcs.Provider
	if autoPR {
		if wtManager == nil {
			// Python's main() builds the provider only when BOTH are
			// on, for the good reason that there is no branch to open
			// a PR from without a worktree.
			return withExitCode(1, errors.New(
				"vcs.auto_pr needs dispatch.worktree_mode: there is no branch to open a PR from"))
		}
		provider = vcs.NewProvider(vcs.Config{
			Provider: cfg.VCS.Provider,
			Host:     cfg.VCS.Host,
		})
		scheduler.VCS = provider
		scheduler.CIRecorder = backend
	}

	runner := engine.NewRunner(scheduler)
	// Resume: the same backend answers which dispatches a previous
	// run left behind, so a crashed orch's rows do not hold tasks
	// in-progress forever.
	runner.Dispatches = backend
	// And adopts statuses a person or a failed write changed in the
	// database while this run held another view (#255).
	runner.Statuses = engine.StateRecorder{Backend: backend}
	// And acts on the operator's stop requests (#356).
	runner.Requests = backend
	// And takes tasks.json again when it changes (v0.22): a task added or
	// edited — by `orch task add|edit`, the dashboard or a hand edit — joins
	// this run. Checked by modification time, so an unchanged file costs a
	// stat per tick.
	tasksMod := fileModTime(paths.TasksJSON())
	if opts.tasks == nil {
		runner.ReloadTasks = func(ctx context.Context) ([]model.Task, bool, error) {
			mod := fileModTime(paths.TasksJSON())
			if mod.Equal(tasksMod) {
				return nil, false, nil
			}
			// Before reading: a file broken mid-edit is reported once, and read
			// again when it next changes.
			tasksMod = mod
			tf, err := model.LoadTasksFile(paths.TasksJSON())
			if err != nil {
				return nil, false, err
			}
			if err := backend.Bootstrap(ctx, tf.Tasks); err != nil {
				return nil, false, err
			}
			return tf.Tasks, true, nil
		}
	}

	// The CI poller only has something to watch when a PR can exist.
	if provider != nil {
		runner.CI = &engine.CIPoller{
			Provider:        provider,
			Backend:         backend,
			Worktree:        wtManager,
			PollInterval:    time.Duration(cfg.VCS.CIPollIntervalS) * time.Second,
			MaxRetries:      cfg.VCS.CIMaxRetries,
			ConflictRetries: conflictRetries(cfg),
			AutoMerge:       cfg.GitHub.AutoMerge,
			// Python's `gh pr merge --squash --auto`. Without a
			// method gh refuses to merge non-interactively (#232).
			Squash:           true,
			MergeWhenPassing: true,
		}
	}
	// One gate, shared: the scheduler asks it per dispatch and the
	// loop asks it whether every provider is capped. Building two
	// would read the same window twice and let them disagree inside
	// a single tick.
	if gate := newBudgetGate(paths, cfg, backend); gate != nil {
		scheduler.Budget = gate
		runner.Budget = gate
		runner.Alerts = newBudgetAlerts(notifyCfg, notifier, gate, backend, runID)
	}

	code, err := runner.Run(ctx)
	if err != nil {
		return withExitCode(1, err)
	}
	reportRunSummary(ctx, os.Stderr, backend, paths, cfg, notifier, runID)
	noteOutgrownCI(os.Stderr, paths.Root)
	offerReportFindings(ctx, os.Stderr, backend, paths.Root)
	if code != 0 {
		// The loop already said what it was doing on its way out;
		// a second line here would be noise.
		return withSilentExitCode(code)
	}
	return nil
}

// reportRunSummary prints the summary of the run that just ended, and posts
// it to the webhooks when notifications are on (#456). Only a finished run
// has one: a run stopped before its loop ended wrote no sprint_done.
func reportRunSummary(ctx context.Context, w io.Writer, backend state.Backend, paths config.Paths, cfg config.Config,
	notifier *notify.Notifier, runID string) {
	tf, _ := model.LoadTasksFile(paths.TasksJSON())
	rec, err := receipt.Load(ctx, backend, runID, receipt.Titles(tf.Tasks), pricing.Load(paths.Root))
	if err != nil || rec == nil || !rec.Finished {
		return
	}
	text := rec.Summary(cfg.Dashboard.SummaryLanguage, cfg.GitHub.AutoMerge).Text()
	_, _ = fmt.Fprint(w, "\n"+text)
	notifier.RunSummary(ctx, text)
}

// noteEstimate prints `orch estimate`'s first line before anything is
// dispatched (#357). An estimate that cannot be made is not a reason to stop.
func noteEstimate(ctx context.Context, w io.Writer, backend state.Backend, paths config.Paths, cfg config.Config,
	queue *engine.TaskQueue, opts runOptions) {
	tasks := queue.AllTasks()
	for i := range tasks {
		tasks[i].Status, _ = queue.Status(tasks[i].ID)
	}
	e, err := projectEstimate(ctx, backend, paths, cfg, tasks, opts.only, opts.maxTasks)
	if err != nil {
		return
	}
	lines := e.Lines()
	_, _ = fmt.Fprintf(w, "estimate: %s\n", lines[0])
}

// offerReportFindings is report_findings asked at the right moment (#376):
// once, after a run in which a project `orch init` left the offer marker in
// has a finished task. Anyone who has seen orch work can decide whether its
// agents may file public issues about it.
func offerReportFindings(ctx context.Context, w io.Writer, backend state.Backend, root string) {
	marker := filepath.Join(root, ".orchestrator", scaffold.FindingsOfferName)
	if _, err := os.Stat(marker); err != nil {
		return
	}
	done, err := backend.Tasks(ctx, state.TaskFilter{Statuses: []model.Status{model.StatusDone}})
	if err != nil || len(done) == 0 {
		return
	}
	_, _ = fmt.Fprintln(w, "note: your agents can report problems with orch itself as public GitHub issues, under your gh login, "+
		"to help fix them — set report_findings.enabled: true in .orchestrator/config.yaml to allow it. Asked once.")
	_ = os.Remove(marker)
}

// hasGitRemote says whether root's repository has any remote to push to.
func hasGitRemote(root string) bool {
	out, err := exec.Command("git", "-C", root, "remote").Output() // #nosec G204 -- fixed git arguments; root is the project root
	return err == nil && strings.TrimSpace(string(out)) != ""
}

// noteOutgrownCI says, once a run ends, that the tasks may have just built
// the code init's placeholder CI was waiting for (#363).
func noteOutgrownCI(w io.Writer, root string) {
	if ci.PlaceholderOutgrown(root) {
		_, _ = fmt.Fprintf(w, "note: %s still checks nothing, and the repository now has a stack — `orch ci setup --force` proposes its real CI.\n", ci.WorkflowPath)
	}
}

// takeRunLock is the one-run-per-project lock, taken before anything is
// opened or dispatched: two runs on a project adopt each other's writes and
// dispatch the same tasks (#344).
//
// Not with --task-locks, whose whole promise is several orch instances
// sharing one project, each taking a distinct set of tasks; the per-task
// locks are what keeps those apart. A nil lock is safe to Release.
func takeRunLock(opts runOptions, stateDir string) (*engine.RunLock, error) {
	if opts.taskLocks {
		return nil, nil
	}
	return engine.AcquireRunLock(stateDir)
}

// perDispatchCap is budget.per_dispatch_usd as the per-attempt cap claude
// receives as `--max-budget-usd` — only when the project wrote the key. The
// default 5.0 keeps its older meaning (it limits the attempt-3 escalation) and
// caps nothing by itself. nil (no flag) when unwritten, zero or less.
func perDispatchCap(cfg config.Config) *float64 {
	if !cfg.Budget.PerDispatchExplicit || cfg.Budget.PerDispatchUSD <= 0 {
		return nil
	}
	v := cfg.Budget.PerDispatchUSD
	return &v
}

// newBudgetAlerts builds the budget-window alerts, or nil when they would
// have nowhere to go: notifications.budget_alerts off, or no webhook set.
func newBudgetAlerts(cfg config.Config, notifier *notify.Notifier, usage engine.BudgetUsage,
	events engine.AlertEvents, runID string) *engine.BudgetAlerts {
	if !cfg.Notifications.BudgetAlerts || !notifier.Enabled() {
		return nil
	}
	return &engine.BudgetAlerts{
		Usage:  usage,
		Notify: notifier,
		Events: events,
		RunID:  runID,
		Pct:    cfg.Notifications.BudgetAlertPct,
	}
}

// newBudgetGate loads the provider guardrails, or returns nil when the
// project has none — which is every project that never opted in, and is why
// a missing budgets.yaml is not an error.
//
// Read failures are reported and treated as "no gate" rather than failing the
// run: a guardrail that cannot be loaded must not be able to stop work, the
// same fail-open the per-dispatch check takes when the window is unreadable.
// The path is budget.ResolvePath's, the one doctor and the dashboard read too.
func newBudgetGate(paths config.Paths, cfg config.Config, backend state.Backend) *budget.Gate {
	path := budget.ResolvePath(paths.Root, paths.ConfigYAML, cfg.BudgetsConfig)
	if path == "" {
		return nil
	}
	bcfg, err := budget.LoadConfig(path, cfg.BudgetsPreset)
	if err != nil {
		fmt.Fprintf(os.Stderr, "budget guardrails disabled: %v\n", err)
		return nil
	}
	if bcfg == nil {
		return nil
	}
	bcfg.UnreportedDispatchTokens = cfg.TypicalDispatchToken
	return budget.NewGate(backend, bcfg)
}

// fileModTime is path's modification time, zero when it cannot be read.
func fileModTime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// conflictRetries is retry.conflict.max_attempts as written (#438). A config
// without it is off, so upgrading never starts pushing to PRs and paying for
// agents unasked; `orch init` and `orch start` write 1 into a new project.
func conflictRetries(cfg config.Config) int {
	return cfg.Retry.Conflict.Attempts(0)
}
