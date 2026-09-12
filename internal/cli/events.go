package cli

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/hectorcanaimero/orch/internal/state"
)

// filterAndTail applies `--run` and `--tail` the way Python's
// `iter_events(run_id=..., task_id=..., limit=...)` does: the run filter
// narrows the set BEFORE tail takes the last N of what's left, and 0 (or
// negative) means "no cap".
func filterAndTail(evs []state.Event, runID string, tail int) []state.Event {
	filtered := evs
	if runID != "" {
		filtered = make([]state.Event, 0, len(evs))
		for _, e := range evs {
			if e.RunID == runID {
				filtered = append(filtered, e)
			}
		}
	}
	if tail > 0 && len(filtered) > tail {
		filtered = filtered[len(filtered)-tail:]
	}
	return filtered
}

func newEventsCmd(flags *projectFlags) *cobra.Command {
	var (
		tail   int
		runID  string
		asJSON bool
	)
	cmd := &cobra.Command{
		Use:   "events [TASK_ID]",
		Short: "Tail event rows for one task, or for the whole project",
		Long: "Tail the event log (default: the last 20).\n\n" +
			"With a TASK_ID, that task's events, exactly as Python's `orch\n" +
			"events TASK_ID` prints them. Without one, every task's — and the\n" +
			"run-level rows that belong to no task, which is the only way to\n" +
			"see them: `sprint_done` carries the counts a finished run ended\n" +
			"with and its task_id is empty by design.\n\n" +
			"The no-argument form is new in Go; Python's TASK_ID is required.",
		// Python's own parser makes TASK_ID required, and this stays
		// compatible with that: every invocation Python accepts prints what
		// Python prints. The zero-argument form is additional surface, not a
		// changed one — see docs/CLI.md.
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			taskID := ""
			if len(args) == 1 {
				taskID = args[0]
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

			// Fetch everything (0 = no cap) so --run can narrow the set
			// before --tail takes the last N of what's left — the Backend
			// interface has no run-id filter of its own.
			var all []state.Event
			if taskID == "" {
				all, err = backend.AllEvents(ctx, 0)
			} else {
				all, err = backend.Events(ctx, taskID, 0)
			}
			if err != nil {
				return fmt.Errorf("read events%s: %w", forTask(taskID), err)
			}
			evs := filterAndTail(all, runID, tail)

			if asJSON {
				out := make([]eventJSON, 0, len(evs))
				for _, e := range evs {
					out = append(out, toEventJSON(e, paths.ID))
				}
				return printCompactJSON(cmd.OutOrStdout(), out)
			}

			if len(evs) == 0 {
				if taskID == "" {
					_, err := fmt.Fprintln(cmd.OutOrStdout(), "(no events recorded)")
					return err
				}
				_, err := fmt.Fprintf(cmd.OutOrStdout(), "(no events for task %q)\n", taskID)
				return err
			}

			// The one-task table is Python's, column for column. The
			// project-wide one adds TASK, because without it a row says
			// nothing about which task it belongs to — and a run-level row
			// with an empty task_id would be indistinguishable from a
			// task's. Two shapes rather than one: the column is redundant
			// when you named the task, and load-bearing when you did not.
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
			header := "TS\tEVENT\tBACKEND\tRUN"
			if taskID == "" {
				header = "TS\tTASK\tEVENT\tBACKEND\tRUN"
			}
			if _, err := fmt.Fprintln(w, header); err != nil {
				return err
			}
			for _, e := range evs {
				var line string
				if taskID == "" {
					line = fmt.Sprintf("%s\t%s\t%s\t%s\t%s\n",
						e.TS, taskColumn(e.TaskID), e.EventType, e.Backend, shortRunID(e.RunID))
				} else {
					line = fmt.Sprintf("%s\t%s\t%s\t%s\n",
						e.TS, e.EventType, e.Backend, shortRunID(e.RunID))
				}
				if _, err := fmt.Fprint(w, line); err != nil {
					return err
				}
			}
			return w.Flush()
		},
	}
	cmd.Flags().IntVar(&tail, "tail", 20, "Cap at the last N events (default: 20; 0 = all)")
	cmd.Flags().StringVar(&runID, "run", "", "Restrict to one run id (default: every recorded run)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit as a JSON array (one row per event)")
	return cmd
}

func shortRunID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// forTask is the " for task \"X\"" tail an error message gets when the caller
// named one, and nothing when they did not. An error reading the whole log
// should not claim a task id it was never given.
func forTask(taskID string) string {
	if taskID == "" {
		return ""
	}
	return fmt.Sprintf(" for %q", taskID)
}

// taskColumn renders a run-level row's empty task id.
//
// `sprint_done` belongs to the run, not to a task, and an empty cell in a
// tab-aligned table reads as a rendering bug rather than as a fact. The em
// dash is the same "there is nothing here" the dashboard's own tables use for
// a missing ETA.
func taskColumn(taskID string) string {
	if taskID == "" {
		return "—"
	}
	return taskID
}
