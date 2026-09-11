package atomize

import (
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/hectorcanaimero/orch/internal/templates"
)

// Bug 19: a fenced code block is documentation ABOUT the spec format, never
// spec content. Before this fix the header/task regexes matched line by
// line with no idea a fence exists, so an example inside a ```markdown
// fence parsed as real tasks.

func TestParseSkipsHeadersInsideAFencedBlock(t *testing.T) {
	dir := t.TempDir()
	writeSpec(t, dir, "x.md",
		"# F0 — Real\n\n## F0.1 — pkg\n\n### F0.1.T1 — Real task\n\n"+
			"- **Modelo**: claude-sonnet-4-6\n\n"+
			"Here is an example of the format:\n\n"+
			"```markdown\n"+
			"# F9 — Example\n\n"+
			"## F9.1 — Package: example\n\n"+
			"### F9.1.T1 — Fake task from the example\n\n"+
			"- **Modelo**: claude-opus-4-7\n"+
			"```\n\n"+
			"That's the format.\n")

	r, err := ParseFile(filepath.Join(dir, "x.md"), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Tasks) != 1 {
		t.Fatalf("got %d tasks, want 1 (the fenced example must not parse): %+v", len(r.Tasks), r.Tasks)
	}
	if r.Tasks[0].ID != "F0.1.T1" {
		t.Errorf("task id = %q, want F0.1.T1", r.Tasks[0].ID)
	}
}

// The real specs/README.md orch init writes ships its own ```markdown
// example (F0.1.T1 "Setup monorepo", F0.1.T2 "Root README") inside a
// fence — the exact content bug 19 was found through, once bug 18 made
// `specs/` a root atomize actually scans. Reading it from
// internal/templates (rather than a hand-copied string here) means this
// test tracks the shipped file instead of a snapshot of it.
func TestParseSkipsTheShippedSpecsReadmeExample(t *testing.T) {
	body, err := fs.ReadFile(templates.Files(), "specs/README.md")
	if err != nil {
		t.Fatalf("read embedded specs/README.md: %v", err)
	}

	dir := t.TempDir()
	writeSpec(t, dir, "README.md", string(body))

	r, err := ParseFile(filepath.Join(dir, "README.md"), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Tasks) != 0 {
		t.Errorf("got %d tasks from specs/README.md, want 0 (its example is inside a fence): %+v",
			len(r.Tasks), r.Tasks)
	}
}
