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
// task's current runtime row.
type StatusReader interface {
	Tasks(ctx context.Context, filter state.TaskFilter) ([]state.TaskRuntime, error)
}

// Hydrate overlays the live status AND comments on tasks.json's rows.
//
// One query, not one per task. The per-task form is a round trip each and
// answers the same question; on a project of any size it is the difference
// between a page that loads and one that does not.
//
// # Why comments and not only status
//
// Since F-12 the comments a task accumulates live in
// `tasks_runtime.comments_json` — every `Transition` appends one — while
// tasks.json keeps whatever it was seeded with, which for a scaffolded project
// is nothing. Overlaying only the status meant the dashboard's task detail
// served an empty `comments` array for a project orch had actually run, with
// the notes sitting in the database the same request had already opened.
//
// Nothing is lost by overlaying: `Bootstrap` seeds `comments_json` FROM the
// file, so a runtime row's comments start as the file's and only grow.
//
// (Python has the same hole for the same reason — `_load_tasks_hydrated`
// replaces `status` and nothing else. It is the second half of bug 24, and
// opus-2's notes carry the first.)
//
// A task with no row in the database keeps what tasks.json gave it. That is
// the honest answer before `Bootstrap` has seeded the project — the file is
// the only thing that knows about the task at all.
//
// Order, and everything else about the rows, is left alone: the caller's task
// order is the file's order, which is what every view renders in.
//
// One caveat for a future caller: a hydrated Task is for READING. `model.Task`
// remembers which optional keys its source file had, so saving one back to
// tasks.json would not write comments the file never declared — this overlay
// is not a way to migrate them into the file, and nothing should treat it as
// one.
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

	byID := make(map[string]state.TaskRuntime, len(runtime))
	for _, r := range runtime {
		byID[r.ID] = r
	}

	out := make([]model.Task, len(tasks))
	copy(out, tasks)
	for i := range out {
		row, ok := byID[out[i].ID]
		if !ok {
			continue
		}
		out[i].Status = row.Status
		out[i].Comments = row.Comments
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
