package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hectorcanaimero/orch/internal/config"
	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/state"
)

// costUSDZero is `cost_usd`'s always-current value — `state.Backend` has no
// way to read spend rows back yet, so every task reports zero. It has to be
// the literal bytes "0.0", not a plain float64(0): Python's
// `round(cost_by_task.get(t.id, 0.0), 4)` always starts from a float, so it
// always renders WITH a decimal point, and Go's encoding/json renders a
// float64 zero as the bare integer "0" with no way to force the point. See
// go-migration-notes.md.
var costUSDZero = json.RawMessage("0.0")

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
}

// buildStatusRows joins tasks.json's DAG shape with the database's runtime
// status, mirroring `build_status_snapshot`. Order follows tasks.json's own
// task order (declaration order), NOT Backend.Tasks' `ORDER BY task_id` —
// that's a different consumer's convention; Python's row order comes from
// iterating tasks.json directly.
//
// `backend`/`cli_model`/`tier` are always the "route not found" fallback
// ("?", the task's own model string, nil) — internal/router doesn't exist
// yet. Once it does, resolve real routes here instead of hardcoding a miss;
// until then this matches Python's own behavior on a project whose
// model_router.yaml has no entry for a task's model, which is what
// testdata/parity-project exercises (see go-migration-notes.md).
//
// `cost_usd` is always 0 for a similar reason: `state.Backend` has no way
// to read spend rows back yet (RecordSpend has no reading counterpart).
// Flagged in go-migration-notes.md — not something this command can fix on
// its own.
func buildStatusRows(ctx context.Context, backend state.Backend, projectID string, tasks []model.Task) ([]statusRow, error) {
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

		rows = append(rows, statusRow{
			ID:             t.ID,
			Phase:          t.Phase,
			Title:          t.Title,
			Status:         status,
			Backend:        "?",
			CLIModel:       t.Model,
			Model:          t.Model,
			Tier:           nil,
			Dependencies:   nonNilStrings(t.Dependencies),
			CostUSD:        costUSDZero,
			LastEvent:      lastEvent,
			LastEventHuman: lastEventHuman,
			DeferReason:    nil,
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
