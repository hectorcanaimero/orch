package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/state"
)

// newTaskStatusCmd ports `orch task-status` (orchestrator/orch.py's
// _run_task_status_subcommand) — the single-writer helper
// scripts/task-{start,finish,block,reset}.sh shell into. Exit codes match
// Python exactly: 0 success, 1 config/layout error, 2 unknown task id, 3
// illegal transition.
func newTaskStatusCmd(flags *projectFlags) *cobra.Command {
	var author, note string
	cmd := &cobra.Command{
		Use:   "task-status TASK_ID STATUS",
		Short: "Single-writer helper — updates task status via the active backend",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			taskID, rawStatus := args[0], args[1]
			ctx := cmd.Context()

			// argparse's `choices=[...]` rejects an unrecognized status
			// before the handler runs, at its own usage-error exit (2) —
			// there's no cobra equivalent for a positional arg, so this
			// is checked here instead, at the same exit code.
			status, err := model.ParseStatus(rawStatus)
			if err != nil {
				return withExitCode(2, err)
			}

			paths, cfg, err := loadProjectConfig(flags)
			if err != nil {
				return err
			}
			backend, closeDB, err := openBackend(ctx, paths, cfg)
			if err != nil {
				return err
			}
			defer func() { _ = closeDB() }()

			// A task-status call against a project that was never
			// `orch status`'d yet still has to find its row — bootstrap is
			// idempotent (INSERT OR IGNORE), so the only way this fails is
			// a real database problem, which is exactly what the caller
			// needs to see rather than a confusing "task not found" from
			// the Transition call right below.
			if err := backend.Bootstrap(ctx, loadDAG(paths)); err != nil {
				return fmt.Errorf("bootstrap: %w", err)
			}

			err = backend.Transition(ctx, taskID, status, state.Note{Author: author, Body: note})
			switch {
			case err == nil:
				return nil
			case errors.Is(err, state.ErrTaskNotFound):
				return withExitCode(2, fmt.Errorf("unknown task id: %q", taskID))
			case errors.Is(err, state.ErrIllegalTransition):
				return withExitCode(3, fmt.Errorf("illegal transition: %w", err))
			default:
				return err
			}
		},
	}
	cmd.Flags().StringVar(&author, "author", "orch", "Comment author")
	cmd.Flags().StringVar(&note, "note", "", "Free-form comment appended to the task")
	return cmd
}
