package atomize

import (
	"encoding/json"
	"fmt"

	"github.com/hectorcanaimero/orch/internal/model"
)

// declarativeFields is the ordered list of fields a spec owns and merge
// updates on an existing task; it fixes the order UpdatedTask.Changed is
// built in. Ports _DECLARATIVE_FIELDS.
var declarativeFields = []string{
	"title", "description", "model", "reason", "dependencies",
	"estimateHours", "specRef", "phase",
}

// UpdatedTask is one entry in MergeDiff.Updated: the merged row, the row it
// replaced, and which declarative fields actually changed (in
// declarativeFields order).
type UpdatedTask struct {
	New     model.Task
	Old     model.Task
	Changed []string
}

// MergeDiff buckets the result of MergeTasks for diff rendering and for
// tests. Ports MergeDiff.
type MergeDiff struct {
	NewTasks    []model.Task
	Updated     []UpdatedTask
	Unchanged   []model.Task
	Orphans     []model.Task
	DepWarnings []string
}

// toCandidate builds the row a NEW task gets: status=backlog, whatever
// files/dependencies the spec declared, and no comments. Ports to_json_row.
func toCandidate(pt ParsedTask) model.Task {
	deps := pt.Dependencies
	if deps == nil {
		deps = []string{}
	}
	files := pt.Files
	if files == nil {
		files = []string{}
	}
	return model.Task{
		ID:            pt.ID,
		Phase:         pt.Phase,
		Title:         pt.Title,
		Description:   pt.Description,
		Model:         pt.Model,
		Reason:        pt.Reason,
		Status:        model.StatusBacklog,
		Dependencies:  deps,
		EstimateHours: pt.EstimateHours,
		Files:         files,
		SpecRef:       pt.SpecRef,
		Comments:      []json.RawMessage{},
	}
}

// mergeRow updates an EXISTING task's declarative fields from pt while
// preserving its runtime fields (status/comments/files) and any unknown
// wire keys (Extra) untouched. It always returns a struct literal (not a
// copy of old) so the merged row's MarshalJSON emits every optional field —
// see the package doc comment on that choice.
func mergeRow(old model.Task, pt ParsedTask) (model.Task, []string) {
	cand := toCandidate(pt)

	changed := map[string]bool{
		"title":         old.Title != cand.Title,
		"description":   old.Description != cand.Description,
		"model":         old.Model != cand.Model,
		"reason":        old.Reason != cand.Reason,
		"dependencies":  !equalStrings(old.Dependencies, cand.Dependencies),
		"estimateHours": old.EstimateHours != cand.EstimateHours,
		"specRef":       old.SpecRef != cand.SpecRef,
		"phase":         old.Phase != cand.Phase,
	}

	var changedList []string
	for _, f := range declarativeFields {
		if changed[f] {
			changedList = append(changedList, f)
		}
	}

	merged := model.Task{
		ID:            old.ID,
		Phase:         cand.Phase,
		Title:         cand.Title,
		Model:         cand.Model,
		Description:   cand.Description,
		Reason:        cand.Reason,
		Dependencies:  cand.Dependencies,
		EstimateHours: cand.EstimateHours,
		SpecRef:       cand.SpecRef,
		Status:        old.Status,
		Files:         old.Files,
		Comments:      old.Comments,
		Extra:         old.Extra,
	}
	return merged, changedList
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// MergeTasks applies the atomizer's non-destructive merge and returns the
// merged TasksFile plus a bucketed diff. Ports merge_tasks:
//
//   - a task ID not already in existing is appended (NewTasks);
//   - a task ID already present has its declarative fields updated and its
//     runtime fields (status/comments/files) preserved (Updated, or
//     Unchanged when nothing declarative changed);
//   - an existing row whose ID is not in parsed is never removed, only
//     reported (Orphans) — this is what makes the merge non-destructive,
//     and it applies equally to legacy pre-atomizer IDs (R-001, ...), which
//     the parser never generates and therefore never matches;
//   - meta and phases are carried over from existing untouched;
//   - every dependency in the final task set is checked against the final
//     ID set, independent of whether the task carrying it was touched by
//     this merge (DepWarnings).
func MergeTasks(existing model.TasksFile, parsed []ParsedTask) (model.TasksFile, MergeDiff) {
	var diff MergeDiff

	rows := make([]model.Task, len(existing.Tasks))
	copy(rows, existing.Tasks)

	byID := make(map[string]int, len(rows))
	for i, r := range rows {
		byID[r.ID] = i
	}
	parsedIDs := make(map[string]bool, len(parsed))
	for _, pt := range parsed {
		parsedIDs[pt.ID] = true
	}

	for _, pt := range parsed {
		if idx, ok := byID[pt.ID]; ok {
			old := rows[idx]
			merged, changed := mergeRow(old, pt)
			rows[idx] = merged
			if len(changed) > 0 {
				diff.Updated = append(diff.Updated, UpdatedTask{New: merged, Old: old, Changed: changed})
			} else {
				diff.Unchanged = append(diff.Unchanged, merged)
			}
		} else {
			cand := toCandidate(pt)
			rows = append(rows, cand)
			diff.NewTasks = append(diff.NewTasks, cand)
		}
	}

	for _, row := range rows {
		if row.ID != "" && !parsedIDs[row.ID] {
			diff.Orphans = append(diff.Orphans, row)
		}
	}

	allIDs := make(map[string]bool, len(rows))
	for _, row := range rows {
		if row.ID != "" {
			allIDs[row.ID] = true
		}
	}
	for _, row := range rows {
		for _, dep := range row.Dependencies {
			if !allIDs[dep] {
				diff.DepWarnings = append(diff.DepWarnings, fmt.Sprintf(
					"task %s depende de '%s' que no existe", row.ID, dep))
			}
		}
	}

	result := existing
	result.Tasks = rows
	return result, diff
}
