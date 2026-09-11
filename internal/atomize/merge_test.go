package atomize

import (
	"encoding/json"
	"testing"

	"github.com/hectorcanaimero/orch/internal/model"
)

// defaultParsedTask ports test_atomize.py's _parsed() helper.
func defaultParsedTask(overrides func(*ParsedTask)) ParsedTask {
	pt := ParsedTask{
		ID: "F1.1.T1", Phase: 1, Package: 1, TaskNum: 1,
		Title: "Auth domain", Description: "desc",
		Model: "claude-sonnet-4-6", Reason: "typed",
		EstimateHours: 4.0,
		Dependencies:  []string{},
		Files:         []string{},
		SpecRef:       "x.md#F1.1.T1",
	}
	if overrides != nil {
		overrides(&pt)
	}
	return pt
}

func mustTask(t *testing.T, jsonRow string) model.Task {
	t.Helper()
	var tk model.Task
	if err := json.Unmarshal([]byte(jsonRow), &tk); err != nil {
		t.Fatalf("unmarshal task fixture: %v", err)
	}
	return tk
}

// Ports TestMergePreservesRuntime.

func TestMergeStatusPreserved(t *testing.T) {
	existing := model.TasksFile{Tasks: []model.Task{mustTask(t, `{
		"id": "F1.1.T1", "phase": 1, "title": "old title", "description": "old",
		"model": "old-model", "reason": "old", "status": "done",
		"dependencies": [], "estimateHours": 1.0, "files": ["real/path.dart"],
		"specRef": "old.md#F1.1.T1", "comments": [{"note": "existing comment"}]
	}`)}}

	result, diff := MergeTasks(existing, []ParsedTask{defaultParsedTask(nil)})
	row := result.Tasks[0]

	if row.Status != model.StatusDone {
		t.Errorf("status = %q, want preserved 'done'", row.Status)
	}
	if diff2 := gotWantDiff(row.Files, []string{"real/path.dart"}); diff2 != "" {
		t.Errorf("files not preserved:\n%s", diff2)
	}
	if len(row.Comments) != 1 {
		t.Errorf("comments not preserved: %v", row.Comments)
	}
	if row.Title != "Auth domain" || row.Model != "claude-sonnet-4-6" || row.EstimateHours != 4.0 {
		t.Errorf("declarative fields not updated: %+v", row)
	}
	if len(diff.Updated) != 1 {
		t.Fatalf("Updated = %v", diff.Updated)
	}
	changed := diff.Updated[0].Changed
	if !containsStr(changed, "title") || !containsStr(changed, "model") {
		t.Errorf("Changed = %v, want title and model", changed)
	}
}

func TestMergeNewTaskUsesBacklogDefault(t *testing.T) {
	result, diff := MergeTasks(model.TasksFile{}, []ParsedTask{defaultParsedTask(nil)})
	if len(diff.NewTasks) != 1 {
		t.Fatalf("NewTasks = %v", diff.NewTasks)
	}
	row := result.Tasks[0]
	if row.Status != model.StatusBacklog {
		t.Errorf("status = %q, want backlog", row.Status)
	}
	if len(row.Comments) != 0 {
		t.Errorf("comments = %v, want empty", row.Comments)
	}
}

// Ports TestMergeNoDifference.test_unchanged_bucket.

func TestMergeUnchangedBucket(t *testing.T) {
	existing := model.TasksFile{Tasks: []model.Task{mustTask(t, `{
		"id": "F1.1.T1", "phase": 1, "title": "Auth domain", "description": "desc",
		"model": "claude-sonnet-4-6", "reason": "typed", "status": "in-progress",
		"dependencies": [], "estimateHours": 4.0, "files": [],
		"specRef": "x.md#F1.1.T1", "comments": []
	}`)}}

	result, diff := MergeTasks(existing, []ParsedTask{defaultParsedTask(nil)})
	if len(diff.Updated) != 0 {
		t.Errorf("Updated = %v, want none", diff.Updated)
	}
	if len(diff.Unchanged) != 1 {
		t.Fatalf("Unchanged = %v", diff.Unchanged)
	}
	if result.Tasks[0].Status != model.StatusInProgress {
		t.Errorf("status = %q, want in-progress preserved", result.Tasks[0].Status)
	}
}

// Ports TestMergeOrphans.

func TestMergeOrphanNotRemoved(t *testing.T) {
	existing := model.TasksFile{
		Meta:   model.Meta{Extra: map[string]json.RawMessage{"foo": json.RawMessage(`"bar"`)}},
		Phases: []model.Phase{{ID: 1, Name: ""}},
		Tasks: []model.Task{mustTask(t, `{
			"id": "F9.9.T99", "phase": 9, "title": "orphan", "model": "x", "status": "done"
		}`)},
	}
	result, diff := MergeTasks(existing, []ParsedTask{defaultParsedTask(nil)})

	foundInResult := false
	for _, r := range result.Tasks {
		if r.ID == "F9.9.T99" {
			foundInResult = true
		}
	}
	if !foundInResult {
		t.Errorf("orphan removed from result: %+v", result.Tasks)
	}
	foundInDiff := false
	for _, o := range diff.Orphans {
		if o.ID == "F9.9.T99" {
			foundInDiff = true
		}
	}
	if !foundInDiff {
		t.Errorf("orphan not listed in diff.Orphans: %+v", diff.Orphans)
	}
}

func TestMergeLegacyIDNotTouched(t *testing.T) {
	existing := model.TasksFile{Tasks: []model.Task{mustTask(t, `{
		"id": "R-001", "phase": 0, "title": "Monorepo bootstrap", "status": "done",
		"model": "legacy", "dependencies": [], "estimateHours": 0, "files": [],
		"specRef": "", "comments": [], "description": "", "reason": ""
	}`)}}

	result, _ := MergeTasks(existing, []ParsedTask{defaultParsedTask(nil)})

	var r001 *model.Task
	for i := range result.Tasks {
		if result.Tasks[i].ID == "R-001" {
			r001 = &result.Tasks[i]
		}
	}
	if r001 == nil {
		t.Fatalf("R-001 missing from result")
	}
	if r001.Status != model.StatusDone || r001.Title != "Monorepo bootstrap" {
		t.Errorf("R-001 mutated: %+v", r001)
	}
	if result.Tasks[len(result.Tasks)-1].ID != "F1.1.T1" {
		t.Errorf("new task not appended last: %+v", result.Tasks)
	}
}

func TestMergeMetaAndPhasesPreserved(t *testing.T) {
	existing := model.TasksFile{
		Meta:   model.Meta{Note: "custom"},
		Phases: []model.Phase{{ID: 1, Name: "test"}},
	}
	result, _ := MergeTasks(existing, []ParsedTask{defaultParsedTask(nil)})
	if result.Meta.Note != "custom" {
		t.Errorf("Meta.Note = %q, want custom", result.Meta.Note)
	}
	if len(result.Phases) != 1 || result.Phases[0].Name != "test" {
		t.Errorf("Phases = %+v", result.Phases)
	}
}

// Ports TestMergeDepValidation.

func TestMergeDepWarningWhenMissing(t *testing.T) {
	p := defaultParsedTask(func(pt *ParsedTask) { pt.Dependencies = []string{"NON.EXISTENT.ID"} })
	_, diff := MergeTasks(model.TasksFile{}, []ParsedTask{p})
	if !anyWarningContains(diff.DepWarnings, "NON.EXISTENT.ID") {
		t.Errorf("DepWarnings = %v", diff.DepWarnings)
	}
}

func TestMergeNoWarningWhenDepExists(t *testing.T) {
	p1 := defaultParsedTask(func(pt *ParsedTask) { pt.ID = "F1.1.T1" })
	p2 := defaultParsedTask(func(pt *ParsedTask) {
		pt.ID = "F1.1.T2"
		pt.Dependencies = []string{"F1.1.T1"}
	})
	_, diff := MergeTasks(model.TasksFile{}, []ParsedTask{p1, p2})
	if len(diff.DepWarnings) != 0 {
		t.Errorf("DepWarnings = %v, want none", diff.DepWarnings)
	}
}
