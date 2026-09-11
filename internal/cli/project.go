package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hectorcanaimero/orch/internal/config"
	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/router"
	"github.com/hectorcanaimero/orch/internal/state"
)

// resolveAndValidate resolves the project's paths and checks it looks like
// a real orch project — ported from `ProjectPaths.ensure_valid()`
// (orchestrator/paths.py): both tasks.json AND scripts/task-start.sh must
// exist. Every project-scoped command calls this first, status/tasks/
// events/logs included.
func resolveAndValidate(f *projectFlags) (config.Paths, error) {
	paths, err := config.ResolvePaths(f.root, f.id, f.configPath)
	if err != nil {
		return config.Paths{}, err
	}
	_, tasksErr := os.Stat(paths.TasksJSON())
	_, scriptErr := os.Stat(filepath.Join(paths.Root, "scripts", "task-start.sh"))
	if tasksErr != nil || scriptErr != nil {
		return config.Paths{}, fmt.Errorf(
			"orchestrator project_root=%s is missing tasks.json or scripts/task-start.sh",
			paths.Root)
	}
	return paths, nil
}

// loadProjectConfig resolves paths, validates the project, and loads
// config.yaml — the subset `orch events` needs (it never touches
// tasks.json).
func loadProjectConfig(f *projectFlags) (config.Paths, config.Config, error) {
	paths, err := resolveAndValidate(f)
	if err != nil {
		return config.Paths{}, config.Config{}, err
	}
	res, err := config.Load(paths.ConfigYAML, paths.Root)
	if err != nil {
		return config.Paths{}, config.Config{}, fmt.Errorf("config load failed: %w", err)
	}
	return paths, res.Config, nil
}

// openBackend opens the project's database and returns a Backend, plus a
// close func the caller must run once done. Kept separate from
// loadProjectConfig so `orch logs` — which never touches the database — can
// skip it entirely.
func openBackend(ctx context.Context, paths config.Paths, cfg config.Config) (state.Backend, func() error, error) {
	dbPath := paths.SQLitePath(cfg)
	db, _, err := state.Open(ctx, dbPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open %s: %w", dbPath, err)
	}
	return state.NewSQLite(db, paths.ID, paths.Root), db.Close, nil
}

// loadDAG reads tasks.json for its shape (phase, title, model,
// dependencies…) tolerantly: a malformed file yields zero tasks rather than
// an error, matching `build_status_snapshot`'s "malformed tasks.json is not
// our problem" (orchestrator/observability.py). Runtime status lives in the
// database, not here, since F-12.
func loadDAG(paths config.Paths) []model.Task {
	tf, err := model.LoadTasksFile(paths.TasksJSON())
	if err != nil {
		return nil
	}
	return tf.Tasks
}

// loadRouterTolerant loads model_router.yaml, falling back to an empty
// router on any error — matching `build_status_snapshot`'s own
// `try: router_map = load_router(...) except: router_map = {}`
// (orchestrator/observability.py): a broken or missing router config
// shouldn't hide the rest of `status`/`tasks`, only degrade the
// backend/cli_model/tier columns to the "route not found" fallback.
func loadRouterTolerant(paths config.Paths) router.Router {
	rtr, err := router.Load(paths.RouterYAML())
	if err != nil {
		return router.Router{}
	}
	return rtr
}

// knownBackends enumerates model.Backend's five values — Backend.SpendSince
// takes one backend at a time (it's shaped for the budget gate's rolling
// window, not a project-wide report), so computing a per-task cost map
// across every provider means calling it once per known value. There is no
// bulk "every backend" query to fall back to.
var knownBackends = []model.Backend{
	model.BackendClaude, model.BackendCodex, model.BackendOpencode,
	model.BackendGemini, model.BackendAgy,
}

// computeCostByTask sums cost_usd per task id across every backend and all
// of history (since = the zero Time), mirroring Python's
// `for row in backend.iter_all_spend(): cost_by_task[tid] += cost`
// (orchestrator/observability.py). The project-wide total `orch status`
// reports is this map's own values summed — not a second, separately
// computed number — so the two can never disagree.
func computeCostByTask(ctx context.Context, backend state.Backend) (map[string]float64, error) {
	out := map[string]float64{}
	for _, b := range knownBackends {
		spend, err := backend.SpendSince(ctx, string(b), time.Time{})
		if err != nil {
			return nil, fmt.Errorf("read spend for %q: %w", b, err)
		}
		for _, s := range spend {
			out[s.TaskID] += s.CostUSD
		}
	}
	return out, nil
}

// round4 matches Python's `round(x, 4)`.
func round4(v float64) float64 {
	return float64(int64(v*10000+sign(v)*0.5)) / 10000
}

func sign(v float64) float64 {
	if v < 0 {
		return -1
	}
	return 1
}

// formatPyFloat renders v the way Python's json.dumps renders a float: a
// decimal point always present, even at a whole number ("0.0", not "0").
// Go's encoding/json drops the point for a whole-number float64, and this
// package's cost fields need to look like Python's regardless of value —
// not just at zero, now that real spend can flow through. Plain 'f'
// formatting (no exponent) is enough here: cost_usd is always a small,
// four-decimal dollar amount, never large or tiny enough to need one.
func formatPyFloat(v float64) json.RawMessage {
	s := strconv.FormatFloat(v, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return json.RawMessage(s)
}

// statusRow is one task as `status`/`tasks --json` render it — the shape
// `build_status_snapshot` builds in Python, field for field and in the same
// order (json.dumps preserves dict insertion order, so this order is part
// of the compatibility contract, not cosmetic).
type statusRow struct {
	ID             string          `json:"id"`
	Phase          int             `json:"phase"`
	Title          string          `json:"title"`
	Status         model.Status    `json:"status"`
	Backend        string          `json:"backend"`
	CLIModel       string          `json:"cli_model"`
	Model          string          `json:"model"`
	Tier           *string         `json:"tier"`
	Dependencies   []string        `json:"dependencies"`
	CostUSD        json.RawMessage `json:"cost_usd"`
	LastEvent      *eventJSON      `json:"last_event"`
	LastEventHuman *string         `json:"last_event_human"`
	DeferReason    *string         `json:"defer_reason"`

	// costUSDValue is the same value as CostUSD, kept as a real float64 for
	// buildSnapshot's filtered_total_usd sum — parsing CostUSD's
	// json.RawMessage back out just to add it up would be a strange way to
	// avoid storing the number twice.
	costUSDValue float64
}

// buildStatusRows joins tasks.json's DAG shape with the database's runtime
// status, mirroring `build_status_snapshot`. Order follows tasks.json's own
// task order (declaration order), NOT Backend.Tasks' `ORDER BY task_id` —
// that's a different consumer's convention; Python's row order comes from
// iterating tasks.json directly.
func buildStatusRows(ctx context.Context, backend state.Backend, projectID string, tasks []model.Task, rtr router.Router, costByTask map[string]float64) ([]statusRow, error) {
	rows := make([]statusRow, 0, len(tasks))
	for _, t := range tasks {
		status := t.Status
		rt, err := backend.Task(ctx, t.ID)
		switch {
		case err == nil:
			status = rt.Status
		case errors.Is(err, state.ErrTaskNotFound):
			// tasks.json's own status stands until Bootstrap seeds a row.
		default:
			return nil, fmt.Errorf("read task %q: %w", t.ID, err)
		}

		lastEvent, lastEventHuman, err := lastEventFor(ctx, backend, t.ID, projectID)
		if err != nil {
			return nil, err
		}

		backendName, cliModel, tier := "?", t.Model, (*string)(nil)
		if route, ok := rtr[t.Model]; ok {
			backendName = string(route.Backend)
			cliModel = route.CLIModel
			tierStr := string(route.Tier)
			tier = &tierStr
		}

		cost := round4(costByTask[t.ID])

		rows = append(rows, statusRow{
			ID:             t.ID,
			Phase:          t.Phase,
			Title:          t.Title,
			Status:         status,
			Backend:        backendName,
			CLIModel:       cliModel,
			Model:          t.Model,
			Tier:           tier,
			Dependencies:   nonNilStrings(t.Dependencies),
			CostUSD:        formatPyFloat(cost),
			LastEvent:      lastEvent,
			LastEventHuman: lastEventHuman,
			DeferReason:    nil,
			costUSDValue:   cost,
		})
	}
	return rows, nil
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
