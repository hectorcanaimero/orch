package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/hectorcanaimero/orch/internal/budget"
	"github.com/hectorcanaimero/orch/internal/config"
	"github.com/hectorcanaimero/orch/internal/engine"
	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/providers"
	"github.com/hectorcanaimero/orch/internal/state"
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
	var (
		mode      string
		maxTasks  int
		only      string
		taskLocks bool
	)

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Walk the DAG, dispatching ready tasks to their CLI agents",
		RunE: func(cmd *cobra.Command, args []string) error {
			switch engine.Mode(mode) {
			case engine.ModeAuto, engine.ModeSemi:
			default:
				return withExitCode(1, fmt.Errorf(
					"--mode %q: expected %q or %q", mode, engine.ModeAuto, engine.ModeSemi))
			}

			paths, cfg, err := loadProjectConfig(flags)
			if err != nil {
				return withExitCode(1, err)
			}

			ctx := cmd.Context()
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

			rtr := loadRouterTolerant(paths)
			runID := providers.NewSessionID()
			if err := backend.StartRun(ctx, runID, mode); err != nil {
				return withExitCode(1, fmt.Errorf("starting the run: %w", err))
			}

			scheduler := engine.NewScheduler(queue, rtr, engine.SchedulerOptions{
				Mode:              engine.Mode(mode),
				GlobalMax:         cfg.Concurrency.GlobalMax,
				PerProvider:       cfg.Concurrency.PerProvider,
				TimeoutMultiplier: cfg.DefaultTimeoutMult,
				Cfg:               cfg,
				UseTaskLocks:      taskLocks,
				Only:              only,
				MaxTasks:          maxTasks,
				StateDir:          paths.StateDir(),
				Cwd:               paths.Root,
				RunID:             runID,
			})
			scheduler.Backend = engine.NewStateRecorder(backend)
			if engine.Mode(mode) == engine.ModeSemi {
				scheduler.Gate = engine.TerminalGate{In: cmd.InOrStdin(), Out: cmd.OutOrStdout()}
			}
			runner := engine.NewRunner(scheduler)
			// Resume: the same backend answers which dispatches a previous
			// run left behind, so a crashed orch's rows do not hold tasks
			// in-progress forever.
			runner.Dispatches = backend
			// One gate, shared: the scheduler asks it per dispatch and the
			// loop asks it whether every provider is capped. Building two
			// would read the same window twice and let them disagree inside
			// a single tick.
			if gate := newBudgetGate(paths, cfg, backend); gate != nil {
				scheduler.Budget = gate
				runner.Budget = gate
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
		},
	}

	cmd.Flags().StringVar(&mode, "mode", string(engine.ModeAuto),
		"auto dispatches everything ready; semi asks before each critical task")
	cmd.Flags().IntVar(&maxTasks, "max-tasks", 0,
		"Stop after dispatching this many tasks; 0 means no limit")
	cmd.Flags().StringVar(&only, "only", "",
		"Only dispatch tasks whose id matches this glob (dependencies still resolve across the whole DAG)")
	cmd.Flags().BoolVar(&taskLocks, "task-locks", false,
		"Take a per-task lock, so several orch instances can share one project")

	return cmd
}

// newBudgetGate loads the provider guardrails, or returns nil when the
// project has none — which is every project that never opted in, and is why
// a missing budgets.yaml is not an error.
//
// Read failures are reported and treated as "no gate" rather than failing the
// run: a guardrail that cannot be loaded must not be able to stop work, the
// same fail-open the per-dispatch check takes when the window is unreadable.
func newBudgetGate(paths config.Paths, cfg config.Config, backend state.Backend) *budget.Gate {
	path := cfg.BudgetsConfig
	if path == "" {
		return nil
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(paths.Root, path)
	}
	bcfg, err := budget.LoadConfig(path, cfg.BudgetsPreset)
	if err != nil {
		fmt.Fprintf(os.Stderr, "budget guardrails disabled: %v\n", err)
		return nil
	}
	if bcfg == nil {
		return nil
	}
	return budget.NewGate(backend, bcfg)
}
