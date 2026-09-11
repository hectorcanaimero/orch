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
		Use:   "events TASK_ID",
		Short: "Tail event rows for one task (default: last 20)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			taskID := args[0]
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
			all, err := backend.Events(ctx, taskID, 0)
			if err != nil {
				return fmt.Errorf("read events for %q: %w", taskID, err)
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
				_, err := fmt.Fprintf(cmd.OutOrStdout(), "(no events for task %q)\n", taskID)
				return err
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
			if _, err := fmt.Fprintln(w, "TS\tEVENT\tBACKEND\tRUN"); err != nil {
				return err
			}
			for _, e := range evs {
				if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
					e.TS, e.EventType, e.Backend, shortRunID(e.RunID)); err != nil {
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
