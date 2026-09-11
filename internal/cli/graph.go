package cli

import (
	"fmt"
	"os"
	"path"

	"github.com/spf13/cobra"

	"github.com/hectorcanaimero/orch/internal/graph"
	"github.com/hectorcanaimero/orch/internal/model"
)

// newGraphCmd ports the `orch graph` subcommand (orchestrator/orch.py's
// _run_graph_subcommand), but not its output: Python renders a
// self-contained HTML page with inline SVG (`orchestrator/graph.py`'s
// build_html) with no DOT anywhere in the tree. Go replaces that with
// Graphviz DOT — pipeable into `dot`, greppable, diffable in review — since
// the visual DAG now lives in the dashboard (GraphPage + /api/graph). This
// is a deliberate, documented contract change, not a gap: see
// docs/brainstorm/go-migration-notes.md.
//
// `--out` defaults to stdout rather than Python's `plan.html` in cwd, since
// there is no HTML file to default-name; passing it writes to that path
// instead and prints a one-line confirmation, matching Python's own "wrote
// {path} ({n} nodes)" shape. `--open` is dropped along with the browser
// launch it triggered — nothing to open once the output is DOT text.
//
// Unlike Python (which goes through the full `build_status_snapshot`
// aggregator just to reuse its `--only` filtering and reads the DB via
// Bootstrap), DOT is a pure function of tasks.json's shape — id, phase,
// dependencies — with no runtime status in it, so this filters tasks.json
// directly and never opens a Backend.
func newGraphCmd(flags *projectFlags) *cobra.Command {
	var out, only string
	cmd := &cobra.Command{
		Use:   "graph",
		Short: "Render the project DAG as Graphviz DOT",
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, err := resolveAndValidate(flags)
			if err != nil {
				return withExitCode(1, err)
			}
			tasks := loadDAG(paths)

			if only != "" {
				filtered := make([]model.Task, 0, len(tasks))
				for _, t := range tasks {
					ok, matchErr := path.Match(only, t.ID)
					if matchErr != nil {
						return withExitCode(1, fmt.Errorf("--only %q: %w", only, matchErr))
					}
					if ok {
						filtered = append(filtered, t)
					}
				}
				tasks = filtered
			}

			dot := graph.DOT(tasks)

			if out == "" {
				_, err := fmt.Fprint(cmd.OutOrStdout(), dot)
				return err
			}
			// #nosec G306 -- a DOT file is meant to be shared/piped, not secret.
			if err := os.WriteFile(out, []byte(dot), 0o644); err != nil {
				return withExitCode(1, fmt.Errorf("could not write %s: %w", out, err))
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "wrote %s (%d nodes)\n", out, len(tasks))
			return err
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "Write DOT to this file instead of stdout")
	cmd.Flags().StringVar(&only, "only", "", "Restrict nodes rendered to ids matching this glob")
	return cmd
}
