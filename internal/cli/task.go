package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/state"
)

// newTaskCmd is the `orch task` parent — only `set` exists, matching
// Python's `_run_task_subcommand` dispatch (`if not args or args[0] not in
// ("set",))`.
func newTaskCmd(flags *projectFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "task",
		Short: "Manual task mutations",
	}
	cmd.AddCommand(newTaskSetCmd(flags))
	return cmd
}

// newTaskSetCmd ports `orch task set` (orchestrator/orch.py's
// _run_task_set_subcommand). Only `--status` is wired to
// state.Backend.Transition today — `--model`/`--backend` would write to
// `tasks_definition`, and the Backend interface has no method for
// that yet (see go-migration-notes.md). The flags are still registered, so
// a caller gets a clear "not implemented" error instead of cobra's opaque
// "unknown flag" — the CLI surface matches Python's even where the
// implementation behind it doesn't yet.
//
// `--milestone` is gone rather than stubbed: a milestone is a phase (see
// snapshot.PhaseMilestones), which a task already carries in tasks.json, so
// there is nothing for the flag to ever write.
func newTaskSetCmd(flags *projectFlags) *cobra.Command {
	var id, status, modelFlag, backendFlag string
	cmd := &cobra.Command{
		Use:   "set",
		Short: "Set a task's status (model and backend overrides are not implemented)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if modelFlag == "" && backendFlag == "" && status == "" {
				return withExitCode(1, errors.New(
					"at least one of --model, --status, --backend is required"))
			}
			if modelFlag != "" || backendFlag != "" {
				return fmt.Errorf(
					"--model/--backend are not implemented yet — " +
						"state.Backend has no method to write tasks_definition " +
						"(see go-migration-notes.md); only --status works today")
			}

			ctx := cmd.Context()
			paths, cfg, err := loadProjectConfig(flags)
			if err != nil {
				return err
			}
			backend, closeDB, err := openBackend(ctx, paths, cfg)
			if err != nil {
				return err
			}
			defer func() { _ = closeDB() }()
			if err := backend.Bootstrap(ctx, loadDAG(paths)); err != nil {
				return fmt.Errorf("bootstrap: %w", err)
			}

			st, err := model.ParseStatus(status)
			if err != nil {
				return withExitCode(1, err)
			}
			note := state.Note{Author: "operator", Body: "manual set via orch task set"}
			err = backend.Transition(ctx, id, st, note)
			switch {
			case err == nil:
				_, ferr := fmt.Fprintf(cmd.OutOrStdout(), "task %s: status -> %s\n", id, status)
				return ferr
			case errors.Is(err, state.ErrTaskNotFound):
				return withExitCode(1, fmt.Errorf("%s: %w", id, err))
			case errors.Is(err, state.ErrIllegalTransition):
				return withExitCode(3, fmt.Errorf("illegal transition: %w", err))
			default:
				return err
			}
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "Task ID, e.g. F1.1.T3")
	cmd.Flags().StringVar(&modelFlag, "model", "", "Override the model for this task (not implemented yet)")
	cmd.Flags().StringVar(&status, "status", "", "Set the task status (e.g. done, in-progress, blocked)")
	cmd.Flags().StringVar(&backendFlag, "backend", "", "Override the backend for this task (not implemented yet)")
	_ = cmd.MarkFlagRequired("id")
	return cmd
}
