package cli

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/hectorcanaimero/orch/internal/model"
)

// tasksRow is `tasks --json`'s trimmed row shape — the subset of fields
// `_run_tasks_subcommand` (orchestrator/orch.py) keeps from the full
// snapshot, same key names and order.
type tasksRow struct {
	ID           string       `json:"id"`
	Status       model.Status `json:"status"`
	Backend      string       `json:"backend"`
	CLIModel     string       `json:"cli_model"`
	Phase        int          `json:"phase"`
	Dependencies []string     `json:"dependencies"`
}

func trimToTasksRow(r statusRow) tasksRow {
	return tasksRow{
		ID: r.ID, Status: r.Status, Backend: r.Backend,
		CLIModel: r.CLIModel, Phase: r.Phase, Dependencies: r.Dependencies,
	}
}

func newTasksCmd(flags *projectFlags) *cobra.Command {
	var (
		asJSON       bool
		only         string
		statusFilter string
	)
	cmd := &cobra.Command{
		Use:   "tasks",
		Short: "List every task with status, routing and deps",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			paths, cfg, err := loadProjectConfig(flags)
			if err != nil {
				return err
			}
			tasks := loadDAG(paths)
			backend, closeDB, err := openBackend(ctx, paths, cfg)
			if err != nil {
				return err
			}
			defer func() { _ = closeDB() }()

			snap, err := buildSnapshot(ctx, paths, backend, tasks, only, parseStatusList(statusFilter))
			if err != nil {
				return err
			}

			if asJSON {
				rows := make([]tasksRow, 0, len(snap.Tasks))
				for _, r := range snap.Tasks {
					rows = append(rows, trimToTasksRow(r))
				}
				return printCompactJSON(cmd.OutOrStdout(), rows)
			}

			out := cmd.OutOrStdout()
			if _, err := fmt.Fprintf(out, "Tasks (%d shown)\n", len(snap.Tasks)); err != nil {
				return err
			}
			w := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
			if _, err := fmt.Fprintln(w, "ID\tSTATUS\tBACKEND/MODEL\tDEPS\tPHASE"); err != nil {
				return err
			}
			for _, r := range snap.Tasks {
				deps := "—"
				if len(r.Dependencies) > 0 {
					deps = fmt.Sprint(r.Dependencies)
				}
				if _, err := fmt.Fprintf(w, "%s\t%s\t%s/%s\t%s\t%d\n",
					r.ID, r.Status, r.Backend, r.CLIModel, deps, r.Phase); err != nil {
					return err
				}
			}
			return w.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit as JSON array of rows")
	cmd.Flags().StringVar(&only, "only", "", "Restrict task rows to ids matching this glob")
	cmd.Flags().StringVar(&statusFilter, "status", "", "Comma-separated status filter (e.g. todo,in-progress)")
	return cmd
}
