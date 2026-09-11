// Package cli wires cobra's root command and every subcommand. cmd/orch's
// main.go is a thin wrapper around Run — nothing under internal/ ever
// imports cmd/ (CHECKLIST.md rule 15), so the dependency runs the other way.
package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// errNotImplemented marks a subcommand that is wired into the CLI surface
// but has no real implementation yet. Run maps it to exit code 2 so scripts
// can tell "not built yet" apart from a genuine runtime error (exit 1) or a
// usage error (cobra's own exit 2 on unknown flags/args).
var errNotImplemented = errors.New("not implemented yet")

// projectFlags are the three flags every project-scoped command accepts,
// ported from Python's `_add_common_project_flags` (orchestrator/orch.py).
// Registered once as persistent flags on the root command rather than
// repeated per subcommand — same effect for the user, one definition here.
type projectFlags struct {
	root       string
	id         string
	configPath string
}

func registerProjectFlags(root *cobra.Command) *projectFlags {
	f := &projectFlags{}
	root.PersistentFlags().StringVar(&f.root, "project-root", "",
		"Project root; default = cwd. Env fallback: ORCH_PROJECT_ROOT.")
	root.PersistentFlags().StringVar(&f.id, "project-id", "",
		"Project id override. Env fallback: ORCH_PROJECT_ID.")
	root.PersistentFlags().StringVar(&f.configPath, "config", "",
		"Path to config.yaml (default: .orchestrator/config.yaml)")
	return f
}

func newRootCmd(version string) *cobra.Command {
	root := &cobra.Command{
		Use:           "orch",
		Short:         "Task orchestrator that walks a tasks.json DAG and dispatches to CLI coding agents",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true, // Run prints the error itself, once
	}
	flags := registerProjectFlags(root)
	root.AddCommand(newStatusCmd(flags))
	root.AddCommand(newTasksCmd(flags))
	// events and logs land in the next PR — see CLAUDE.md's Go tree note.
	return root
}

// Run executes the CLI against args and returns the process exit code.
// main() calls os.Exit(Run(...)) — kept separate so tests can drive it
// in-process without os.Exit killing the test binary.
func Run(version string, args []string) int {
	root := newRootCmd(version)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if errors.Is(err, errNotImplemented) {
			return 2
		}
		return 1
	}
	return 0
}
