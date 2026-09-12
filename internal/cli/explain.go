package cli

import (
	"github.com/spf13/cobra"

	"github.com/hectorcanaimero/orch/internal/config"
	"github.com/hectorcanaimero/orch/internal/explain"
	"github.com/hectorcanaimero/orch/internal/project"
	"github.com/hectorcanaimero/orch/internal/state"
)

// newExplainCmd is `orch explain` — one screen that says where a project is
// and what is safe to do next.
//
// New in Go; Python has no equivalent. It exists because the answer was
// available and scattered: the counts come from `orch status`, what could run
// now from `orch tasks`, the guardrail from `orch budget`, and a person
// arriving at a project had to know all three commands to assemble a picture
// none of them draws.
//
// `--json` is the same structure the MCP server's `orch_context` returns,
// built by the same function. One gather, two renderings — the way that story
// drifts is by being assembled twice.
func newExplainCmd(flags *projectFlags) *cobra.Command {
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "explain",
		Short: "Where this project stands, and what is safe to do next",
		Long: "Print a one-screen summary of the project: task counts, what " +
			"could be dispatched right now, the budget window, and the " +
			"read-only commands worth typing next.\n\n" +
			"With --json, prints the same structure the MCP server's " +
			"`orch_context` tool returns.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()

			paths, cfg, err := loadProjectConfig(flags)
			if err != nil {
				return err
			}
			backend, closeDB, err := openBackend(ctx, paths, cfg)
			if err != nil {
				return err
			}
			// Discarded like every other command's: the process is exiting and
			// the handle is read-only here.
			defer func() { _ = closeDB() }()

			tasks, err := project.Load(ctx, backend, paths.TasksJSON())
			if err != nil {
				return err
			}

			overview, err := explain.Gather(ctx, explain.Options{
				ProjectID:   paths.ID,
				ProjectRoot: paths.Root,
				SpecRoot:    cfg.SpecRoot,
				Tasks:       tasks,
				Budget:      budgetReporter(paths, cfg, backend),
			})
			if err != nil {
				return err
			}

			if asJSON {
				return printCompactJSON(cmd.OutOrStdout(), overview)
			}
			return overview.Text(cmd.OutOrStdout())
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false,
		"Print the same structure `orch_context` returns, instead of text")
	return cmd
}

// budgetReporter is `newBudgetGate` with the typed-nil trap closed.
//
// `newBudgetGate` returns a *budget.Gate, and nil means "no budgets.yaml".
// Assigning that nil POINTER to an interface field produces a non-nil
// INTERFACE holding a nil pointer, so `opts.Budget != nil` passes and the
// first method call panics — `Gate.Disabled` dereferences its config. One of
// Go's oldest sharp edges, and the reason this conversion is three lines with
// a name rather than an inline assignment.
func budgetReporter(paths config.Paths, cfg config.Config, backend state.Backend) explain.BudgetReporter {
	gate := newBudgetGate(paths, cfg, backend)
	if gate == nil {
		return nil
	}
	return gate
}
