package cli

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/vcs"
)

// capturedIssues reads the same real `gh issue list` payload internal/vcs
// tests against, so the mapping below is exercised on GitHub's own shapes
// (multi-paragraph markdown bodies, an UPPERCASE state, label objects)
// rather than on three-word literals that never look like an issue.
func capturedIssues(t *testing.T) []vcs.Issue {
	t.Helper()
	b, err := os.ReadFile("../vcs/testdata/gh/2.100.0/issue-list.json")
	if err != nil {
		t.Fatal(err)
	}
	var issues []vcs.Issue
	if err := json.Unmarshal(b, &issues); err != nil {
		t.Fatal(err)
	}
	return issues
}

func TestTaskFromIssuePutsTheURLFirst(t *testing.T) {
	issue := capturedIssues(t)[0]
	task := taskFromIssue(issue, "claude-sonnet-4-6")

	if task.ID != "gh-88" {
		t.Errorf("ID = %q, want gh-88", task.ID)
	}
	if task.Title != issue.Title {
		t.Errorf("Title = %q, want the issue's own", task.Title)
	}
	first, _, _ := strings.Cut(task.Description, "\n")
	if first != issue.URL {
		t.Errorf("first line of Description = %q, want the issue URL %q", first, issue.URL)
	}
	if !strings.Contains(task.Description, "## Feature request") {
		t.Error("the issue body did not make it into the description")
	}
	if task.SpecRef != "" {
		t.Errorf("SpecRef = %q; a URL is not a spec file, and specRef is joined to spec_root", task.SpecRef)
	}
	if task.Model != "claude-sonnet-4-6" {
		t.Errorf("Model = %q", task.Model)
	}
	if task.Status != model.StatusTodo {
		t.Errorf("Status = %q, want todo", task.Status)
	}
	if task.Phase != 0 || len(task.Dependencies) != 0 || task.EstimateHours != 0 {
		t.Errorf("invented structure an issue tracker does not have: phase=%d deps=%v est=%v",
			task.Phase, task.Dependencies, task.EstimateHours)
	}
}

// An issue with an empty body must not produce a description that is a URL
// followed by two blank lines — the description is read by a human and by the
// dispatch prompt.
func TestTaskFromIssueWithNoBody(t *testing.T) {
	task := taskFromIssue(vcs.Issue{Number: 4, Title: "t", URL: "https://example.test/4"}, "m")
	if task.Description != "https://example.test/4" {
		t.Errorf("Description = %q", task.Description)
	}
}

func TestPlanSyncOrdersByNumberAndSkipsWhatExists(t *testing.T) {
	issues := capturedIssues(t) // 88, 87, 86 — gh returns newest first
	existing := []model.Task{{ID: "gh-87", Title: "edited by hand"}}

	plan := planSync(issues, existing, "m")

	var ids []string
	for _, task := range plan.Add {
		ids = append(ids, task.ID)
	}
	want := []string{"gh-86", "gh-88"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Errorf("Add ids = %v, want %v (ascending, minus what exists)", ids, want)
	}
	if len(plan.SkippedExisting) != 1 || plan.SkippedExisting[0] != "gh-87" {
		t.Errorf("SkippedExisting = %v, want [gh-87]", plan.SkippedExisting)
	}
	if plan.empty() {
		t.Error("plan reports empty with two tasks to add")
	}
}

// The second run of a sync is the one that matters: it must find nothing to
// do, not append the same issues again under the same ids.
func TestPlanSyncIsIdempotent(t *testing.T) {
	issues := capturedIssues(t)
	first := planSync(issues, nil, "m")
	second := planSync(issues, first.Add, "m")

	if !second.empty() {
		t.Fatalf("a second sync would add %d task(s) again", len(second.Add))
	}
	if len(second.SkippedExisting) != len(issues) {
		t.Errorf("SkippedExisting = %v, want all %d", second.SkippedExisting, len(issues))
	}
}

func TestCheckSyncModel(t *testing.T) {
	known := []string{"gpt-5-codex", "claude-sonnet-4-6"}

	if err := checkSyncModel("claude-sonnet-4-6", known); err != nil {
		t.Errorf("a routed model was refused: %v", err)
	}

	err := checkSyncModel("", known)
	if err == nil {
		t.Fatal("no --model was accepted")
	}
	// The list is the point of the message: an operator copies a name out of
	// it instead of opening model_router.yaml to find out what is there.
	for _, want := range []string{"claude-sonnet-4-6", "gpt-5-codex"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not offer %q to copy: %v", want, err)
		}
	}
	// Sorted, so the same project prints the same message twice running.
	if i, j := strings.Index(err.Error(), "claude"), strings.Index(err.Error(), "gpt"); i > j {
		t.Errorf("models are not listed in sorted order: %v", err)
	}

	err = checkSyncModel("gpt-9-imaginary", known)
	if err == nil {
		t.Fatal("an unroutable model was accepted; the tasks would break `orch validate` (bug 14)")
	}
	if !strings.Contains(err.Error(), "gpt-9-imaginary") {
		t.Errorf("error does not name the model that failed: %v", err)
	}
}

// A project whose router resolves nothing gets told how to populate it,
// rather than a list of zero models to choose from.
func TestCheckSyncModelWithAnEmptyRouter(t *testing.T) {
	err := checkSyncModel("anything", nil)
	if err == nil {
		t.Fatal("an empty router accepted a model")
	}
	if !strings.Contains(err.Error(), "router add-missing") {
		t.Errorf("error does not say how to fix an empty router: %v", err)
	}
}
