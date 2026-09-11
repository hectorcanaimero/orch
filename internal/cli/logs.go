package cli

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// newLogsCmd ports `_run_logs_subcommand` (orchestrator/orch.py). Unlike the
// other three read commands it has NO --json flag — checked directly in the
// Python source, not assumed — and reads a plain per-task log file rather
// than the database, since log content is written by the dispatched CLI
// subprocess, not the state backend.
func newLogsCmd(flags *projectFlags) *cobra.Command {
	var (
		tail int
		all  bool
	)
	cmd := &cobra.Command{
		Use:   "logs TASK_ID",
		Short: "Tail the per-task log file (default: last 200 lines)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			taskID := args[0]
			paths, err := resolveAndValidate(flags)
			if err != nil {
				return err
			}

			logPath := filepath.Join(paths.StateDir(), "logs", taskID+".log")
			if _, statErr := os.Stat(logPath); statErr != nil {
				return withExitCode(2, fmt.Errorf(
					"no log file for task %q at %s", taskID, logPath))
			}

			// #nosec G304 -- logPath is built from the resolved project's own
			// state dir plus the task id argument, not an arbitrary path.
			f, err := os.Open(logPath)
			if err != nil {
				return fmt.Errorf("could not read %s: %w", logPath, err)
			}
			defer func() { _ = f.Close() }()

			lines := make([]string, 0, 256)
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
			for sc.Scan() {
				lines = append(lines, sc.Text())
			}
			if err := sc.Err(); err != nil {
				return fmt.Errorf("could not read %s: %w", logPath, err)
			}

			out := lines
			if !all && tail > 0 && len(lines) > tail {
				out = lines[len(lines)-tail:]
			}
			w := cmd.OutOrStdout()
			for _, line := range out {
				if _, err := fmt.Fprintln(w, line); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&tail, "tail", 200, "Cap at the last N lines (default: 200)")
	cmd.Flags().BoolVar(&all, "all", false, "Print the entire log (ignores --tail)")
	return cmd
}
