package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hectorcanaimero/orch/internal/vcs"
)

// fakeFindings records what the tool asked GitHub for.
type fakeFindings struct {
	existing []vcs.Issue
	searched []string
	created  []struct {
		repo, title, body string
		labels            []string
	}
}

func (f *fakeFindings) SearchIssues(repo, label, query string, _ int) ([]vcs.Issue, error) {
	f.searched = append(f.searched, repo+"|"+label+"|"+query)
	return f.existing, nil
}

func (f *fakeFindings) CreateIssue(repo, title, body string, labels []string) (string, error) {
	f.created = append(f.created, struct {
		repo, title, body string
		labels            []string
	}{repo, title, body, labels})
	return "https://github.com/hectorcanaimero/orch/issues/999", nil
}

// orch_report_finding is how an agent in any orch project reports a problem
// with orch itself. Each case is what the tool did NOT do as much as what it
// returned: nothing may be filed while it is off, invalid, a duplicate, or
// unconfirmed next to similar reports.
func TestReportFinding(t *testing.T) {
	valid := map[string]any{
		"type": "bug", "title": "orch explain lists backlog tasks as ready",
		"summary": "explain says ready, run never dispatches them",
	}
	with := func(extra map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range valid {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	similar := []vcs.Issue{{Number: 12, Title: "explain shows the wrong ready set", State: "OPEN", URL: "u12"}}

	tests := []struct {
		name        string
		disabled    bool
		existing    []vcs.Issue
		args        map[string]any
		wantError   bool
		wantFiled   bool
		wantDup     int
		wantSimilar int
		wantMessage string
	}{
		{name: "off for the project", disabled: true, args: valid, wantError: true, wantMessage: "report_findings.enabled"},
		{name: "unknown type", args: with(map[string]any{"type": "rant"}), wantError: true, wantMessage: "bug, improvement, feature"},
		{name: "no summary", args: with(map[string]any{"summary": " "}), wantError: true, wantMessage: "required"},
		{
			name:     "same title in other words of case and punctuation",
			existing: []vcs.Issue{{Number: 235, Title: "Orch explain lists backlog tasks as ready!", State: "CLOSED", URL: "u235"}},
			args:     valid, wantDup: 235, wantMessage: "#235",
		},
		{name: "similar, not confirmed", existing: similar, args: valid, wantSimilar: 1, wantMessage: "confirm_new"},
		{name: "similar, confirmed", existing: similar, args: with(map[string]any{"confirm_new": true}), wantFiled: true, wantSimilar: 1},
		{name: "nothing like it", args: valid, wantFiled: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, backend := newTestProject(t)
			fake := &fakeFindings{existing: tt.existing}
			opts := Options{Backend: backend, ProjectRoot: root, Version: "v-test"}
			if !tt.disabled {
				opts.Findings = fake
			}
			cs := connect(t, opts)

			var out reportFindingOut
			res := call(t, cs, "orch_report_finding", tt.args, &out)
			if res.IsError != tt.wantError {
				t.Errorf("IsError = %v, want %v (%q)", res.IsError, tt.wantError, out.Message)
			}
			if out.Filed != tt.wantFiled || (len(fake.created) == 1) != tt.wantFiled {
				t.Errorf("filed = %v with %d issues created, want %v", out.Filed, len(fake.created), tt.wantFiled)
			}
			if tt.wantError && len(fake.searched) != 0 {
				t.Errorf("searched GitHub for a refused report: %v", fake.searched)
			}
			if tt.wantDup != 0 && (out.Duplicate == nil || out.Duplicate.Number != tt.wantDup) {
				t.Errorf("duplicate = %+v, want #%d", out.Duplicate, tt.wantDup)
			}
			if len(out.Similar) != tt.wantSimilar {
				t.Errorf("similar = %+v, want %d", out.Similar, tt.wantSimilar)
			}
			if !strings.Contains(out.Message, tt.wantMessage) {
				t.Errorf("message = %q, want it to mention %q", out.Message, tt.wantMessage)
			}
		})
	}
}

// What reaches GitHub: the right repo and label, the report's sections, and
// none of the operator's paths.
func TestReportFindingFilesARedactedIssue(t *testing.T) {
	root, backend := newTestProject(t)
	fake := &fakeFindings{}
	cs := connect(t, Options{Backend: backend, ProjectRoot: root, Version: "v0.12.0", Findings: fake})
	home, _ := os.UserHomeDir()

	call(t, cs, "orch_report_finding", map[string]any{
		"type": "improvement", "title": "doctor should flag an ignored .claude/",
		"summary":       "agents in " + root + " ran with no allow-list",
		"evidence":      "log at " + filepath.Join(home, "notes.txt"),
		"suggested_fix": "a check in internal/doctor",
		"confidence":    "medium",
	}, &reportFindingOut{})

	if len(fake.created) != 1 {
		t.Fatalf("created %d issues, want 1", len(fake.created))
	}
	got := fake.created[0]
	if got.repo != FindingsRepo || len(got.labels) != 1 || got.labels[0] != FindingsLabel {
		t.Errorf("repo %q labels %v, want %s with %s", got.repo, got.labels, FindingsRepo, FindingsLabel)
	}
	for _, want := range []string{"**Type**: improvement", "**Confidence**: medium", "orch `v0.12.0`",
		"## Summary", "agents in <project> ran", "## Evidence", "~/notes.txt", "## Suggested fix"} {
		if !strings.Contains(got.body, want) {
			t.Errorf("body is missing %q:\n%s", want, got.body)
		}
	}
	if strings.Contains(got.body, root) || strings.Contains(got.body, home) {
		t.Errorf("body leaks a local path:\n%s", got.body)
	}
	if strings.Contains(got.body, "## Repro") {
		t.Errorf("an empty section was rendered:\n%s", got.body)
	}
}
