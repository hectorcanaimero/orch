package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/hectorcanaimero/orch/internal/config"
	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/state"
)

// projectJSON is `status --json`'s `project` object.
type projectJSON struct {
	ProjectID   string `json:"project_id"`
	ProjectRoot string `json:"project_root"`
	Backend     string `json:"backend"`
	StateDir    string `json:"state_dir"`
}

// costJSON is `status --json`'s `cost` object.
//
// Both totals keep Python's exact int-vs-float shape, a real CPython quirk
// worth spelling out since it looks like a bug: `project_total_usd` is
// `round(sum(cost_by_task.values()), 4)`, and Python's `sum()` over an
// EMPTY sequence is the int `0` — but a non-empty list of floats sums to a
// float even when every term is zero. Same story for `filtered_total_usd`,
// summed over the filtered rows. So each field is "0" (no decimal point)
// exactly when there is nothing to sum (no spend at all / an empty
// filtered set), and a real decimal otherwise — never "0" once there is
// at least one dollar amount behind it, even a $0.00 one.
type costJSON struct {
	ProjectTotalUSD  json.RawMessage `json:"project_total_usd"`
	FilteredTotalUSD json.RawMessage `json:"filtered_total_usd"`
}

func costFor(costByTask map[string]float64, filteredSum float64, filteredCount int) costJSON {
	project := json.RawMessage("0")
	if len(costByTask) > 0 {
		total := 0.0
		for _, v := range costByTask {
			total += v
		}
		project = formatPyFloat(round4(total))
	}
	filtered := json.RawMessage("0")
	if filteredCount > 0 {
		filtered = formatPyFloat(round4(filteredSum))
	}
	return costJSON{ProjectTotalUSD: project, FilteredTotalUSD: filtered}
}

// runJSON is `status --json`'s `latest_run` object.
//
// Partial parity: Python's dict (`list_runs`, orchestrator/state/
// sqlite_backend.py) also has run_file/events_file — paths into the
// file-backend layout, which doesn't exist in Go (ADR-G4), so they are
// deliberately dropped rather than faked. Everything else Backend.Run
// carries is included, in Python's field order — completed_count/
// blocked_count/deferred_count landed in state.Run via #130. See
// go-migration-notes.md.
type runJSON struct {
	RunID          string `json:"run_id"`
	StartedAt      string `json:"started_at"`
	UpdatedAt      string `json:"updated_at"`
	Mode           string `json:"mode"`
	Status         string `json:"status"`
	ParentPID      int    `json:"parent_pid"`
	InFlightCount  int    `json:"in_flight_count"`
	CompletedCount int    `json:"completed_count"`
	BlockedCount   int    `json:"blocked_count"`
	DeferredCount  int    `json:"deferred_count"`
}

func toRunJSON(r state.Run) runJSON {
	return runJSON{
		RunID: r.RunID, StartedAt: r.StartedAt, UpdatedAt: r.UpdatedAt,
		Mode: r.Mode, Status: r.Status, ParentPID: r.ParentPID,
		InFlightCount:  r.InFlight,
		CompletedCount: r.CompletedCount,
		BlockedCount:   r.BlockedCount,
		DeferredCount:  r.DeferredCount,
	}
}

type filtersJSON struct {
	Only   *string  `json:"only"`
	Status []string `json:"status"`
}

// statusSnapshot is the full `status --json` payload — `build_status_
// snapshot`'s return dict (orchestrator/observability.py), field for field.
type statusSnapshot struct {
	Project   projectJSON   `json:"project"`
	Totals    *orderedCount `json:"totals"`
	Cost      costJSON      `json:"cost"`
	LatestRun any           `json:"latest_run"`
	Tasks     []statusRow   `json:"tasks"`
	Filters   filtersJSON   `json:"filters"`
}

// buildSnapshot is shared by `status` and `tasks` (Python: the latter reuses
// build_status_snapshot then trims columns). only/statusFilter are applied
// AFTER totals are computed, so `--only`/`--status` narrow the tasks shown
// without lying about the project's real totals.
func buildSnapshot(ctx context.Context, paths config.Paths, backend state.Backend, tasks []model.Task, only string, statusFilter map[string]bool) (statusSnapshot, error) {
	if err := backend.Bootstrap(ctx, tasks); err != nil {
		return statusSnapshot{}, fmt.Errorf("bootstrap: %w", err)
	}

	rtr := loadRouterTolerant(paths)
	costByTask, err := computeCostByTask(ctx, backend)
	if err != nil {
		return statusSnapshot{}, err
	}

	all, err := buildStatusRows(ctx, backend, paths.ID, tasks, rtr, costByTask)
	if err != nil {
		return statusSnapshot{}, err
	}

	totals := newOrderedCount()
	for _, r := range all {
		totals.Add(string(r.Status), 1)
	}
	totals.Add("_total", len(all))

	filtered := make([]statusRow, 0, len(all))
	filteredSum := 0.0
	for _, r := range all {
		if only != "" {
			ok, err := path.Match(only, r.ID)
			if err != nil {
				return statusSnapshot{}, fmt.Errorf("--only %q: %w", only, err)
			}
			if !ok {
				continue
			}
		}
		if len(statusFilter) > 0 && !statusFilter[string(r.Status)] {
			continue
		}
		filtered = append(filtered, r)
		filteredSum += r.costUSDValue
	}

	var latestRun any
	run, err := backend.LatestRun(ctx)
	switch {
	case err == nil:
		latestRun = toRunJSON(run)
	case errors.Is(err, state.ErrNoRuns):
		latestRun = nil
	default:
		return statusSnapshot{}, fmt.Errorf("read latest run: %w", err)
	}

	var onlyPtr *string
	if only != "" {
		onlyPtr = &only
	}
	var statusList []string
	if len(statusFilter) > 0 {
		statusList = make([]string, 0, len(statusFilter))
		for s := range statusFilter {
			statusList = append(statusList, s)
		}
		sort.Strings(statusList)
	}

	return statusSnapshot{
		Project: projectJSON{
			ProjectID:   paths.ID,
			ProjectRoot: paths.Root,
			Backend:     "sqlite",
			StateDir:    paths.StateDir(),
		},
		Totals:    totals,
		LatestRun: latestRun,
		Cost:      costFor(costByTask, filteredSum, len(filtered)),
		Tasks:     filtered,
		Filters:   filtersJSON{Only: onlyPtr, Status: statusList},
	}, nil
}

// parseStatusList turns "todo,done" into {"todo":true,"done":true}. Ported
// from `_parse_status_list` (orchestrator/orch.py) — no validation, an
// unknown status name just matches nothing.
func parseStatusList(raw string) map[string]bool {
	if raw == "" {
		return nil
	}
	out := map[string]bool{}
	for _, p := range strings.Split(raw, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out[p] = true
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func newStatusCmd(flags *projectFlags) *cobra.Command {
	var (
		asJSON       bool
		only         string
		statusFilter string
	)
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Project status: tasks, costs, last events, run summary",
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
				return printCompactJSON(cmd.OutOrStdout(), snap)
			}
			return renderStatusTable(cmd, snap)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit the raw snapshot as JSON")
	cmd.Flags().StringVar(&only, "only", "", "Restrict task rows to ids matching this glob")
	cmd.Flags().StringVar(&statusFilter, "status", "", "Comma-separated status filter (e.g. todo,in-progress)")
	return cmd
}

func renderStatusTable(cmd *cobra.Command, snap statusSnapshot) error {
	out := cmd.OutOrStdout()
	if _, err := fmt.Fprintf(out, "Project %s · backend=%s · %d tasks\n",
		snap.Project.ProjectID, snap.Project.Backend, len(snap.Tasks)); err != nil {
		return err
	}
	w := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "ID\tSTATUS\tBACKEND/MODEL\tLAST EVENT\tCOST\tPHASE"); err != nil {
		return err
	}
	for _, r := range snap.Tasks {
		lastEvent := "—"
		if r.LastEventHuman != nil {
			lastEvent = *r.LastEventHuman
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s/%s\t%s\t$%s\t%d\n",
			r.ID, r.Status, r.Backend, r.CLIModel, lastEvent, r.CostUSD, r.Phase); err != nil {
			return err
		}
	}
	return w.Flush()
}
