// Package cli wires cobra's root command and every subcommand. cmd/orch's
// main.go is a thin wrapper around Run — nothing under internal/ ever
// imports cmd/ (CHECKLIST.md rule 15), so the dependency runs the other way.
package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hectorcanaimero/orch/internal/config"
	"github.com/hectorcanaimero/orch/internal/telemetry"
)

// errNotImplemented marks a subcommand that is wired into the CLI surface
// but has no real implementation yet. Run maps it to exit code 2 so scripts
// can tell "not built yet" apart from a genuine runtime error (exit 1) or a
// usage error (cobra's own exit 2 on unknown flags/args).
var errNotImplemented = errors.New("not implemented yet")

// exitError carries a specific process exit code, for the handful of
// commands (like `orch logs`, per orch.py's _run_logs_subcommand) whose
// Python original distinguishes more than "worked" vs "errored" — e.g. exit
// 2 for "the file doesn't exist" vs exit 1 for a config/layout error.
// Everything else just returns a plain error and gets the generic exit 1.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

func withExitCode(code int, err error) error {
	return &exitError{code: code, err: err}
}

// withSilentExitCode signals a specific exit code without printing anything
// to stderr. `orch validate` needs this: a non-zero exit there means
// "the report already written to stdout found problems", not "something
// else went wrong" — Python doesn't print a second message for that case
// either, so echoing exitError's own text as an ad-hoc stderr line would be
// noise `_run_validate_subcommand` never produces.
func withSilentExitCode(code int) error {
	return &exitError{code: code, err: errors.New("")}
}

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

func newRootCmd(version string) (*cobra.Command, *projectFlags) {
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
	root.AddCommand(newEventsCmd(flags))
	root.AddCommand(newLogsCmd(flags))
	root.AddCommand(newTaskCmd(flags))
	root.AddCommand(newTaskStatusCmd(flags))
	root.AddCommand(newResetCmd(flags))
	root.AddCommand(newValidateCmd(flags))
	root.AddCommand(newGraphCmd(flags))
	root.AddCommand(newMigrateCmd(flags))
	root.AddCommand(newRouterCmd(flags))
	root.AddCommand(newInitCmd(flags))
	root.AddCommand(newConfigCmd(flags))
	root.AddCommand(newInstallSkillsCmd(flags))
	root.AddCommand(newAtomizeCmd(flags))
	root.AddCommand(newRunCmd(flags))
	root.AddCommand(newMCPCmd(flags))
	root.AddCommand(newNotifyCmd(flags))
	root.AddCommand(newDoctorCmd(flags))
	root.AddCommand(newDashboardCmd(flags))
	root.AddCommand(newExplainCmd(flags))
	root.AddCommand(newReportCmd(flags))
	return root, flags
}

// Run executes the CLI against args and returns the process exit code.
// main() calls os.Exit(Run(...)) — kept separate so tests can drive it
// in-process without os.Exit killing the test binary.
func Run(version string, args []string) int {
	root, flags := newRootCmd(version)
	root.SetArgs(args)

	start := time.Now()
	matched, err := root.ExecuteC()
	reportTelemetry(matched, flags, version, time.Since(start), err == nil)

	if err != nil {
		var ee *exitError
		if errors.As(err, &ee) {
			if ee.err != nil && ee.err.Error() != "" {
				fmt.Fprintln(os.Stderr, err)
			}
			return ee.code
		}
		fmt.Fprintln(os.Stderr, err)
		if errors.Is(err, errNotImplemented) {
			return 2
		}
		return 1
	}
	return 0
}

// reportTelemetry sends one best-effort usage ping (G8.6/F4.9) — see
// internal/telemetry's package doc for exactly what it carries. Off unless
// the project's own config.yaml sets telemetry.enabled: true, and the
// standard DO_NOT_TRACK environment variable always overrides that either
// way. A config load failure, a missing home directory, or any other
// resolution problem just means nothing is sent — telemetry is never
// allowed to change a command's own exit code or print anything of its
// own; the command already ran to completion by the time this is called.
func reportTelemetry(matched *cobra.Command, flags *projectFlags, version string, dur time.Duration, success bool) {
	if telemetry.DoNotTrack() {
		return
	}
	// Tolerant on purpose, and independent of whatever the command itself
	// did with these flags: `orch init` on a bare directory and `orch
	// status` on a fully-formed project both need an answer to "is
	// telemetry on", and neither's own config-loading path (or lack of
	// one) should gate this.
	paths, err := config.ResolvePaths(flags.root, flags.id, flags.configPath)
	if err != nil {
		return
	}
	res, err := config.Load(paths.ConfigYAML, paths.Root)
	if err != nil || !res.Config.Telemetry.Enabled {
		return
	}

	installID, err := telemetry.InstallID()
	if err != nil {
		return
	}

	command := ""
	if matched != nil {
		command = strings.TrimSpace(strings.TrimPrefix(matched.CommandPath(), matched.Root().Name()))
	}

	telemetry.Reporter{
		Enabled:   true,
		Endpoint:  res.Config.Telemetry.Endpoint,
		InstallID: installID,
		Version:   version,
	}.Report(command, dur, success)
}
