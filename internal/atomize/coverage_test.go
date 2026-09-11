package atomize

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hectorcanaimero/orch/internal/model"
)

// Atomize is the single-file convenience entry point; AtomizeDir's tests
// (roundtrip_test.go) already cover the multi-file walk this delegates to.
func TestAtomizeSingleFile(t *testing.T) {
	specMD := "# F1 — X\n### F1.1.T1 — Task\n- **Modelo**: opus\n- **Estimación**: 2h\n"
	merged, diff, err := Atomize("spec.md", ".", specMD, model.TasksFile{})
	if err != nil {
		t.Fatal(err)
	}
	if len(diff.NewTasks) != 1 || diff.NewTasks[0].ID != "F1.1.T1" {
		t.Fatalf("NewTasks = %+v", diff.NewTasks)
	}
	if len(merged.Tasks) != 1 || merged.Tasks[0].Model != "opus" {
		t.Fatalf("merged = %+v", merged.Tasks)
	}
}

// RenderDiff's ACTUALIZADAS section is the only path that exercises
// fieldValue/shortDiffValue/shortDiffList — cover every declarative field
// kind (string, list, float, int) in one pass.
func TestRenderDiffUpdatedSectionCoversEveryFieldKind(t *testing.T) {
	existing := model.TasksFile{Tasks: []model.Task{mustTask(t, `{
		"id": "F1.1.T1", "phase": 1, "title": "old", "description": "",
		"model": "old-model", "reason": "old reason", "status": "todo",
		"dependencies": [], "estimateHours": 1.0, "files": [],
		"specRef": "old.md#F1.1.T1", "comments": []
	}`)}}
	p := defaultParsedTask(func(pt *ParsedTask) {
		pt.Dependencies = []string{"A", "B", "C", "D", "E"} // >4 entries exercises the "...]" truncation
		pt.Phase = 2
	})
	_, diff := MergeTasks(existing, []ParsedTask{p})
	if len(diff.Updated) != 1 {
		t.Fatalf("Updated = %v", diff.Updated)
	}

	out := RenderDiff(diff, ParseResult{Tasks: []ParsedTask{p}, FilesScanned: []string{"x.md"}})
	for _, want := range []string{"ACTUALIZADAS", "title:", "dependencies:", "estimateHours:", "phase:", "...]"} {
		if !strings.Contains(out, want) {
			t.Errorf("RenderDiff missing %q:\n%s", want, out)
		}
	}
}

func TestRenderDiffEmptyDescriptionRendersAsEmptySet(t *testing.T) {
	if got := shortDiffValue(""); got != `""` {
		t.Errorf("shortDiffValue(\"\") = %q, want empty-set marker", got)
	}
	long := strings.Repeat("x", 80)
	if got := shortDiffValue(long); len(got) != 60 || !strings.HasSuffix(got, "...") {
		t.Errorf("shortDiffValue long = %q", got)
	}
	if got := shortDiffList(nil); got != "[]" {
		t.Errorf("shortDiffList(nil) = %q, want []", got)
	}
}

// ---- Frontmatter type-mismatch branches (pyTypeName, pyStr, asStringMap) --

func TestExtractFrontmatterTopLevelNotAMapIsDiscardedSilently(t *testing.T) {
	// Ports the (buggy-but-intentionally-preserved) Python behavior: a
	// non-dict top-level YAML value produces present=false with NO
	// warnings — see extractFrontmatter's doc comment.
	text := "---\n- a\n- b\n---\n# F1 — X\n"
	fm, body := extractFrontmatter(text)
	if fm.Present {
		t.Errorf("Present = true, want false for a non-mapping frontmatter")
	}
	if len(fm.Warnings) != 0 {
		t.Errorf("Warnings = %v, want none (matches the Python quirk)", fm.Warnings)
	}
	if !strings.HasPrefix(body, "# F1 — X") {
		t.Errorf("body = %q", body)
	}
}

func TestExtractFrontmatterWrongScalarTypesWarn(t *testing.T) {
	text := "---\n" +
		"type: 5\n" +
		"project_id: 5\n" +
		"phase: not-an-int\n" +
		"package: 5\n" +
		"depends_on: not-a-list\n" +
		"consumed_by: not-a-list\n" +
		"---\n# F1 — X\n"
	fm, _ := extractFrontmatter(text)
	wantSubstrings := []string{
		"type debe ser string",
		"project_id debe ser string",
		"phase debe ser int",
		"package debe ser string",
		"depends_on debe ser lista",
		"consumed_by debe ser lista",
	}
	for _, w := range wantSubstrings {
		if !anyWarningContains(fm.Warnings, w) {
			t.Errorf("missing warning containing %q, got %v", w, fm.Warnings)
		}
	}
}

func TestExtractFrontmatterVersionAndGeneratedAtStringify(t *testing.T) {
	text := "---\nversion: 2\ngenerated_at: 2026-01-01\n---\n# F1 — X\n"
	fm, _ := extractFrontmatter(text)
	if fm.Version != "2" {
		t.Errorf("Version = %q, want \"2\"", fm.Version)
	}
	if fm.GeneratedAt == "" {
		t.Errorf("GeneratedAt is empty")
	}
}

func TestExtractFrontmatterEmptyBlockTreatedAsAbsent(t *testing.T) {
	// A blank line between the fences is required for this to match as a
	// (empty) frontmatter block at all — "---\n---\n" with no blank line
	// in between has no closing "\n---" left to match against and so
	// isn't recognized as frontmatter in the first place (falls through
	// to the "no match" path instead, covered by
	// TestExtractFrontmatterNoneReturnsFullText's sibling cases).
	text := "---\n\n---\n# F1 — X\n"
	fm, body := extractFrontmatter(text)
	if fm.Present {
		t.Errorf("Present = true, want false for an empty frontmatter block")
	}
	if !strings.HasPrefix(body, "# F1 — X") {
		t.Errorf("body = %q", body)
	}
}

// ---- WalkSpecFiles / ParseFile error paths --------------------------------

func TestWalkSpecFilesMissingRootReturnsNoFilesNoError(t *testing.T) {
	files, err := WalkSpecFiles(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Errorf("files = %v, want none", files)
	}
}

func TestWalkSpecFilesRootIsAFileErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "not-a-dir.md")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := WalkSpecFiles(path); err == nil {
		t.Errorf("expected an error when root is a file")
	}
}

func TestParseFileMissingReturnsError(t *testing.T) {
	if _, err := ParseFile(filepath.Join(t.TempDir(), "missing.md"), ".", ""); err == nil {
		t.Errorf("expected an error for a missing spec file")
	}
}

func TestAtomizeDirPropagatesWalkError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := AtomizeDir(path, "", model.TasksFile{}); err == nil {
		t.Errorf("expected an error when docsRoot is a file")
	}
}

// ---- equalStrings ----------------------------------------------------------

func TestEqualStringsLengthMismatch(t *testing.T) {
	if equalStrings([]string{"a"}, []string{"a", "b"}) {
		t.Errorf("equalStrings should report false for different lengths")
	}
}
