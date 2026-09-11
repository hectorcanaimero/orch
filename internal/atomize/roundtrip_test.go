package atomize

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hectorcanaimero/orch/internal/model"
)

// Ports TestRoundtrip.test_parse_merge_write_load: parse -> merge -> write
// -> load must not explode, and runtime defaults/declarative fields must
// survive the full trip.
func TestRoundtripParseMergeWriteLoad(t *testing.T) {
	dir := t.TempDir()
	docs := filepath.Join(dir, "docs")
	if err := copyFile(t, "testdata/specs/sample_spec.md", filepath.Join(docs, "sample.md")); err != nil {
		t.Fatal(err)
	}

	tasksJSON := filepath.Join(dir, "tasks.json")
	existing, err := LoadExisting(tasksJSON)
	if err != nil {
		t.Fatal(err)
	}

	merged, diff, parse, err := AtomizeDir(docs, "", existing)
	if err != nil {
		t.Fatal(err)
	}
	if len(parse.Tasks) != 3 {
		t.Fatalf("parsed %d tasks, want 3", len(parse.Tasks))
	}
	if len(diff.NewTasks) != 3 {
		t.Fatalf("NewTasks = %v, want 3", diff.NewTasks)
	}

	if _, err := Apply(tasksJSON, merged, false); err != nil {
		t.Fatal(err)
	}

	loaded, err := model.LoadTasksFile(tasksJSON)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Tasks) != 3 {
		t.Fatalf("loaded %d tasks, want 3", len(loaded.Tasks))
	}
	byID := map[string]model.Task{}
	for _, tk := range loaded.Tasks {
		byID[tk.ID] = tk
	}
	t9 := byID["F1.1.T9"]
	if diff2 := gotWantDiff(t9.Dependencies, []string{"F1.1.T1", "F0.1.T1"}); diff2 != "" {
		t.Errorf("T9 dependencies mismatch:\n%s", diff2)
	}
	if t9.EstimateHours != 8.0 {
		t.Errorf("T9 estimateHours = %v, want 8.0", t9.EstimateHours)
	}
	if t9.Status != model.StatusBacklog {
		t.Errorf("T9 status = %q, want backlog", t9.Status)
	}
	if diff2 := gotWantDiff(t9.Files, []string{
		"lib/features/auth/presentation/screens/auth_sign_in_screen.dart",
		"lib/features/auth/presentation/widgets/social_login_row.dart",
	}); diff2 != "" {
		t.Errorf("T9 files mismatch:\n%s", diff2)
	}
}

// Ports TestOrchSpecOutputFormat.test_full_roundtrip_with_frontmatter.
func TestRoundtripWithFrontmatter(t *testing.T) {
	dir := t.TempDir()
	docs := filepath.Join(dir, "docs")
	if err := copyFile(t, "testdata/specs/orch_spec_output.md", filepath.Join(docs, "spec.md")); err != nil {
		t.Fatal(err)
	}

	merged, diff, parse, err := AtomizeDir(docs, "sample-project", model.TasksFile{})
	if err != nil {
		t.Fatal(err)
	}
	if len(parse.Tasks) != 3 || len(parse.Warnings) != 0 {
		t.Fatalf("parse = %+v", parse)
	}
	if len(diff.NewTasks) != 3 {
		t.Fatalf("NewTasks = %v", diff.NewTasks)
	}

	tasksJSON := filepath.Join(dir, "tasks.json")
	if _, err := Apply(tasksJSON, merged, false); err != nil {
		t.Fatal(err)
	}

	loaded, err := model.LoadTasksFile(tasksJSON)
	if err != nil {
		t.Fatal(err)
	}
	var t3 model.Task
	for _, tk := range loaded.Tasks {
		if tk.ID == "F1.1.T3" {
			t3 = tk
		}
	}
	if t3.EstimateHours != 16.0 {
		t.Errorf("T3 estimateHours = %v, want 16.0", t3.EstimateHours)
	}
	if diff2 := gotWantDiff(t3.Dependencies, []string{"F1.1.T2", "F1.1.T1"}); diff2 != "" {
		t.Errorf("T3 dependencies mismatch:\n%s", diff2)
	}
	if t3.Status != model.StatusBacklog {
		t.Errorf("T3 status = %q, want backlog", t3.Status)
	}
}

func copyFile(t *testing.T, src, dst string) error {
	t.Helper()
	// #nosec G304 -- src is a hardcoded testdata path passed by this test file.
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	// #nosec G703 -- dst is a path under t.TempDir(), built by this test file.
	return os.WriteFile(dst, data, 0o600)
}
