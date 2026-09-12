// Package project is what the CLI and the dashboard share about a project:
// tasks.json hydrated with the live status from the state backend, plus the
// per-task figures that can only be derived from the event log.
//
// It exists because both consumers were about to answer the same question in
// two places. `tasks.json` carries a task's status as it was when the file was
// written; the database carries what it is now. Every view that ships task
// rows has to overlay the second on the first, and Python learned that the
// hard way — `_load_tasks_hydrated` was extracted in sprint F-8 (issue #72)
// after three endpoints shipped stale statuses from the file.
//
// Imports model, graph and state; imported by cli and dashboard. Nothing here
// knows about HTTP or about a terminal.
package project

import (
	"context"
	"fmt"

	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/state"
)

// StatusReader is the slice of the backend Hydrate needs: one query for every
// task's current status.
type StatusReader interface {
	Tasks(ctx context.Context, filter state.TaskFilter) ([]state.TaskRuntime, error)
}

// Hydrate overlays live statuses on tasks.json's rows.
//
// One query, not one per task. The per-task form is a round trip each and
// answers the same question; on a project of any size it is the difference
// between a page that loads and one that does not.
//
// A task with no row in the database keeps the status tasks.json gave it. That
// is the honest answer before `Bootstrap` has seeded the project — the file is
// the only thing that knows about the task at all — and it is what Python
// does by only replacing ids the runtime map contains.
//
// Order, and everything else about the rows, is left alone: the caller's task
// order is the file's order, which is what every view renders in.
func Hydrate(ctx context.Context, backend StatusReader, tasks []model.Task) ([]model.Task, error) {
	if len(tasks) == 0 {
		return tasks, nil
	}
	runtime, err := backend.Tasks(ctx, state.TaskFilter{})
	if err != nil {
		return nil, fmt.Errorf("read runtime task status: %w", err)
	}
	if len(runtime) == 0 {
		return tasks, nil
	}

	byID := make(map[string]model.Status, len(runtime))
	for _, r := range runtime {
		byID[r.ID] = r.Status
	}

	out := make([]model.Task, len(tasks))
	copy(out, tasks)
	for i := range out {
		if status, ok := byID[out[i].ID]; ok {
			out[i].Status = status
		}
	}
	return out, nil
}

// Load reads tasks.json and hydrates it in one call.
//
// The two steps are also available separately because `orch atomize` and the
// scaffolder want the file exactly as written, with no database anywhere near
// it.
func Load(ctx context.Context, backend StatusReader, tasksJSON string) ([]model.Task, error) {
	f, err := model.LoadTasksFile(tasksJSON)
	if err != nil {
		return nil, err
	}
	return Hydrate(ctx, backend, f.Tasks)
}
