// Command orch walks a tasks.json DAG and dispatches each task to a local
// AI CLI (claude | codex | opencode | gemini | agy). This is the Go
// rewrite's skeleton — subcommands land incrementally; see CLAUDE.md's
// "Migración a Go" section for the target layout.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// version is overridden at build time via -ldflags "-X main.version=...".
// The Makefile fills it in from `git describe --tags --always`; a plain
// `go build` (no ldflags) keeps the "dev" fallback.
var version = "dev"

// errNotImplemented marks a subcommand that is wired into the CLI surface
// but has no real implementation yet. main() maps it to exit code 2 so
// scripts can tell "not built yet" apart from a genuine runtime error
// (exit 1) or a usage error (cobra's own exit 2 on unknown flags/args).
var errNotImplemented = errors.New("not implemented yet")

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "orch",
		Short:         "Task orchestrator that walks a tasks.json DAG and dispatches to CLI coding agents",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true, // run() prints the error itself, once
	}
	root.AddCommand(newStatusCmd())
	return root
}

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the current task board",
		RunE: func(cmd *cobra.Command, args []string) error {
			return errNotImplemented
		},
	}
}

func main() {
	os.Exit(run(os.Args[1:]))
}

// run executes the root command against args and returns the process exit
// code, without calling os.Exit itself — kept separate from main so tests
// can drive it in-process.
func run(args []string) int {
	root := newRootCmd()
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
