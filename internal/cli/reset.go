package cli

import (
	"context"
	"fmt"
	"path"

	"github.com/spf13/cobra"

	"github.com/hectorcanaimero/orch/internal/config"
	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/state"
)

// newResetCmd ports `orch reset` (orchestrator/orch.py's
// _run_reset_subcommand) — the manual escape hatch for stuck in-progress
// tasks. Exit codes match Python: 0 success (even zero candidates), 1 I/O
// error, 2 invalid project layout.
//
// Deliberate difference from the Python source: Python reads tasks.json's
// own `status` field directly to find in-progress candidates, a leftover
// from before F-12 made SQLite the runtime source of truth — on a real
// project that field is frozen at whatever `orch init` wrote (normally
// "todo") and never reflects a live run, so a literal port would almost
// never find anything to reset. Go reads the actual runtime status via
// Backend.Tasks/TaskFilter instead, which is what "revert stuck
// in-progress tasks" needs to mean once the database, not the file, is
// the truth.
func newResetCmd(flags *projectFlags) *cobra.Command {
	var (
		requeue bool
		only    string
	)
	cmd := &cobra.Command{
		Use:   "reset",
		Short: "Revert stuck in-progress tasks to todo (dry-run by default)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			paths, err := resolveAndValidate(flags)
			if err != nil {
				return withExitCode(2, err)
			}
			res, err := config.Load(paths.ConfigYAML, paths.Root)
			if err != nil {
				return fmt.Errorf("config load failed: %w", err)
			}
			backend, closeDB, err := openBackend(ctx, paths, res.Config)
			if err != nil {
				return err
			}
			defer func() { _ = closeDB() }()

			tasks := loadDAG(paths)
			_ = backend.Bootstrap(ctx, tasks)

			candidates, err := inProgressCandidates(ctx, backend, only)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			if len(candidates) == 0 {
				_, err := fmt.Fprintln(out, "no in-progress tasks to reset.")
				return err
			}

			if !requeue {
				if _, err := fmt.Fprintf(out, "would revert %d in-progress task(s) to todo:\n", len(candidates)); err != nil {
					return err
				}
				for _, id := range candidates {
					if _, err := fmt.Fprintf(out, "  - %s\n", id); err != nil {
						return err
					}
				}
				_, err := fmt.Fprintln(out, "\nRe-run with --requeue to actually mutate state.")
				return err
			}

			note := state.Note{Author: "orch-reset", Body: "reset"}
			reverted := make([]string, 0, len(candidates))
			for _, id := range candidates {
				if err := backend.Transition(ctx, id, model.StatusTodo, note); err != nil {
					if _, werr := fmt.Fprintf(cmd.ErrOrStderr(), "reset failed for %s: %v\n", id, err); werr != nil {
						return werr
					}
					continue
				}
				reverted = append(reverted, id)
			}
			if _, err := fmt.Fprintf(out, "reverted %d task(s) to todo:\n", len(reverted)); err != nil {
				return err
			}
			for _, id := range reverted {
				if _, err := fmt.Fprintf(out, "  - %s\n", id); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&requeue, "requeue", false, "Actually revert (default is dry-run)")
	cmd.Flags().StringVar(&only, "only", "", "Restrict to task ids matching this glob")
	return cmd
}

// inProgressCandidates returns every in-progress task id (DAG order),
// narrowed by --only when set.
func inProgressCandidates(ctx context.Context, backend state.Backend, only string) ([]string, error) {
	rows, err := backend.Tasks(ctx, state.TaskFilter{Statuses: []model.Status{model.StatusInProgress}})
	if err != nil {
		return nil, fmt.Errorf("list in-progress tasks: %w", err)
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if only != "" {
			ok, err := path.Match(only, r.ID)
			if err != nil {
				return nil, fmt.Errorf("--only %q: %w", only, err)
			}
			if !ok {
				continue
			}
		}
		out = append(out, r.ID)
	}
	return out, nil
}
