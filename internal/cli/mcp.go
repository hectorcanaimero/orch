package cli

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/hectorcanaimero/orch/internal/mcp"
)

// newMCPCmd wires `orch mcp` — the stdio MCP server.
//
// No Python source: `orchestrator/` has no MCP server at all, so this is new
// capability rather than a port. The shape is the one every MCP client
// expects of a stdio server: it speaks JSON-RPC on stdin/stdout and nothing
// else, which is why there is no `--json`, no banner, and no progress output.
// **Anything this command printed to stdout would be framed as a protocol
// message and break the session**, so the only writes to stdout come from the
// SDK.
//
// It is meant to be launched by the agent, not by a human — `.mcp.json` in a
// scaffolded project names it, and `orch init` writes that file.
func newMCPCmd(flags *projectFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Serve orch's tools to an agent over stdio (MCP)",
		Long: "Serve orch's state to an MCP-capable agent over stdio.\n\n" +
			"Seven tools: orch_list_tasks, orch_get_task, orch_set_status,\n" +
			"orch_block, orch_budget, orch_events, orch_context.\n\n" +
			"Speaks JSON-RPC on stdin/stdout; run it from an MCP client, not\n" +
			"from a terminal. See docs/MCP.md.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, cfg, err := loadProjectConfig(flags)
			if err != nil {
				return err
			}
			backend, closeDB, err := openBackend(cmd.Context(), paths, cfg)
			if err != nil {
				return err
			}
			defer func() { _ = closeDB() }()

			// Seeded once at startup for the same reason `orch task-status`
			// seeds on every invocation: a project that has never been run
			// has no runtime rows, and the first tool call must not fail
			// with "unknown task" for a task tasks.json plainly contains.
			// Bootstrap is idempotent and never overwrites a status.
			if err := backend.Bootstrap(cmd.Context(), loadDAG(paths)); err != nil {
				return fmt.Errorf("bootstrap: %w", err)
			}

			opts := mcp.Options{
				Backend:     backend,
				TasksJSON:   paths.TasksJSON(),
				ProjectID:   paths.ID,
				ProjectRoot: paths.Root,
				SpecRoot:    cfg.SpecRoot,
				Version:     cmd.Root().Version,
			}
			// A nil *budget.Gate must not be stored in the interface: the
			// tool checks `Budget == nil`, and a typed nil is not nil.
			if gate := newBudgetGate(paths, cfg, backend); gate != nil {
				opts.Budget = gate
			}

			// SIGINT and SIGTERM end the session as cleanly as stdin
			// closing does — an agent that is killed takes its server with
			// it, and a half-open database is worse than a dropped
			// connection.
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			return mcp.Serve(ctx, opts)
		},
	}
}
