package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/hectorcanaimero/orch/internal/budget"
	"github.com/hectorcanaimero/orch/internal/config"
	"github.com/hectorcanaimero/orch/internal/engine"
	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/notify"
	"github.com/hectorcanaimero/orch/internal/providers"
	"github.com/hectorcanaimero/orch/internal/state"
	"github.com/hectorcanaimero/orch/internal/vcs"
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
			return runProject(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), flags, opts)
		},
	}

	cmd.Flags().StringVar(&opts.mode, "mode", string(engine.ModeAuto),
		"auto dispatches everything ready; semi asks before each critical task")
	cmd.Flags().IntVar(&opts.maxTasks, "max-tasks", 0,
		"Stop after dispatching this many tasks; 0 means no limit")
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

	backend, closeBackend, err := openBackend(ctx, paths, cfg)
	if err != nil {
		return withExitCode(1, err)
	}
	defer func() {
		if cerr := closeBackend(); cerr != nil {
			fmt.Fprintf(os.Stderr, "closing the state backend: %v\n", cerr)
		}
	}()

	tasks := loadDAG(paths)
	if len(tasks) == 0 {
		return withExitCode(1, fmt.Errorf("no tasks in %s", paths.TasksJSON()))
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

	rtr := loadRouterTolerant(paths)
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
		Only:              opts.only,
		MaxTasks:          opts.maxTasks,
		StateDir:          paths.StateDir(),
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
		wtManager = engine.NewWorktreeManager(worktree.NewManager(paths.Root, !opts.noPush))
		scheduler.Worktree = wtManager
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

	// The CI poller only has something to watch when a PR can exist.
	if provider != nil {
		runner.CI = &engine.CIPoller{
			Provider:     provider,
			Backend:      backend,
			Worktree:     wtManager,
			PollInterval: time.Duration(cfg.VCS.CIPollIntervalS) * time.Second,
			MaxRetries:   cfg.VCS.CIMaxRetries,
			AutoMerge:    cfg.GitHub.AutoMerge,
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
	if code != 0 {
		// The loop already said what it was doing on its way out;
		// a second line here would be noise.
		return withSilentExitCode(code)
	}
	return nil
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
