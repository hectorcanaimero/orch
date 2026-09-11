package atomize

import (
	"strings"
	"testing"

	"github.com/hectorcanaimero/orch/internal/model"
)

// Ports TestRenderDiff. Python's own test for this function only checks
// substrings of the rendered output (it's a human-readable report, not a
// wire format), so parity here means the same sections and IDs appear —
// not byte-identical text. See RenderDiff's doc comment.
func TestRenderDiffAllBucketsPopulated(t *testing.T) {
	p := defaultParsedTask(nil)
	existing := model.TasksFile{Tasks: []model.Task{mustTask(t, `{
		"id": "F0.0.T0", "phase": 0, "title": "orphan", "status": "done",
		"model": "x", "dependencies": [], "estimateHours": 0, "files": [],
		"specRef": "", "comments": [], "description": "", "reason": ""
	}`)}}

	_, diff := MergeTasks(existing, []ParsedTask{p})
	parse := ParseResult{Tasks: []ParsedTask{p}, FilesScanned: []string{"x.md"}}

	out := RenderDiff(diff, parse)
	for _, want := range []string{"NUEVAS", "HUÉRFANAS", "F0.0.T0", "F1.1.T1"} {
		if !strings.Contains(out, want) {
			t.Errorf("RenderDiff output missing %q:\n%s", want, out)
		}
	}
}

func TestRenderDiffNothingToApply(t *testing.T) {
	out := RenderDiff(MergeDiff{}, ParseResult{})
	if !strings.Contains(out, "Nada que aplicar") {
		t.Errorf("expected 'Nada que aplicar' hint, got:\n%s", out)
	}
}
