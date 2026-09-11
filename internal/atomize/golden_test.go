package atomize

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/hectorcanaimero/orch/internal/model"
)

// goldenParsedTask mirrors the field names asdict(ParsedTask) produces in
// Python (snake_case), used only to decode the golden JSON files generated
// by testdata/make-goldens.py.
type goldenParsedTask struct {
	ID            string   `json:"id"`
	Phase         int      `json:"phase"`
	Package       int      `json:"package"`
	TaskNum       int      `json:"task_num"`
	Title         string   `json:"title"`
	Description   string   `json:"description"`
	Model         string   `json:"model"`
	Reason        string   `json:"reason"`
	EstimateHours float64  `json:"estimate_hours"`
	Dependencies  []string `json:"dependencies"`
	Files         []string `json:"files"`
	SpecRef       string   `json:"spec_ref"`
	SourceFile    string   `json:"source_file"`
	Line          int      `json:"line"`
}

type goldenParseResult struct {
	Tasks        []goldenParsedTask `json:"tasks"`
	FilesScanned []string           `json:"filesScanned"`
	Warnings     []string           `json:"warnings"`
}

// toGolden converts a ParsedTask into the shape the golden JSON uses,
// trimming SourceFile to a basename the way testdata/make-goldens.py's
// _portable_task does — the golden is checked out at whatever path a
// clone lands at, so the full path can't be part of the comparison.
func toGolden(t ParsedTask) goldenParsedTask {
	return goldenParsedTask{
		ID: t.ID, Phase: t.Phase, Package: t.Package, TaskNum: t.TaskNum,
		Title: t.Title, Description: t.Description, Model: t.Model, Reason: t.Reason,
		EstimateHours: t.EstimateHours, Dependencies: t.Dependencies, Files: t.Files,
		SpecRef: t.SpecRef, SourceFile: filepath.Base(t.SourceFile), Line: t.Line,
	}
}

func toGoldenResult(r ParseResult) goldenParseResult {
	out := goldenParseResult{Warnings: r.Warnings}
	if out.Warnings == nil {
		out.Warnings = []string{}
	}
	for _, p := range r.FilesScanned {
		out.FilesScanned = append(out.FilesScanned, filepath.Base(p))
	}
	for _, t := range r.Tasks {
		out.Tasks = append(out.Tasks, toGolden(t))
	}
	return out
}

func loadGolden(t *testing.T, name string, v any) {
	t.Helper()
	// #nosec G304 -- name is always a literal golden filename passed by this test file.
	data, err := os.ReadFile(filepath.Join("testdata", "golden", name))
	if err != nil {
		t.Fatalf("read golden %s: %v", name, err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("unmarshal golden %s: %v", name, err)
	}
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatalf("abs %s: %v", p, err)
	}
	return abs
}

func TestParseSampleSpecMatchesPythonGolden(t *testing.T) {
	specsDir := mustAbs(t, filepath.Join("testdata", "specs"))
	got, err := ParseFile(filepath.Join(specsDir, "sample_spec.md"), specsDir, "")
	if err != nil {
		t.Fatal(err)
	}

	var want goldenParseResult
	loadGolden(t, "parse-sample.golden.json", &want)

	if diff := gotWantDiff(toGoldenResult(got), want); diff != "" {
		t.Errorf("parse result mismatch:\n%s", diff)
	}
}

func TestParseOrchSpecOutputMatchesPythonGolden(t *testing.T) {
	specsDir := mustAbs(t, filepath.Join("testdata", "specs"))
	got, err := ParseFile(filepath.Join(specsDir, "orch_spec_output.md"), specsDir, "sample-project")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Warnings) != 0 {
		t.Errorf("expected zero warnings on a clean orch-spec fixture, got %v", got.Warnings)
	}

	var want goldenParseResult
	loadGolden(t, "parse-orch-spec.golden.json", &want)

	if diff := gotWantDiff(toGoldenResult(got), want); diff != "" {
		t.Errorf("parse result mismatch:\n%s", diff)
	}
}

// goldenMerge mirrors the shape testdata/make-goldens.py writes for the
// merge scenario: parse output, the merged tasks.json, and the diff
// buckets by task ID (order-independent — see the note on Changed below).
type goldenMerge struct {
	Parse  goldenParseResult `json:"parse"`
	Merged model.TasksFile   `json:"merged"`
	Diff   struct {
		NewTasks []string `json:"newTasks"`
		Updated  []struct {
			ID      string   `json:"id"`
			Changed []string `json:"changed"`
		} `json:"updated"`
		Unchanged   []string `json:"unchanged"`
		Orphans     []string `json:"orphans"`
		DepWarnings []string `json:"depWarnings"`
	} `json:"diff"`
}

func TestMergeScenarioMatchesPythonGolden(t *testing.T) {
	specsDir := mustAbs(t, filepath.Join("testdata", "specs"))
	files, err := WalkSpecFiles(specsDir)
	if err != nil {
		t.Fatal(err)
	}
	var mergeFiles []string
	for _, f := range files {
		base := filepath.Base(f)
		if base == "merge_a.md" || base == "merge_b.md" {
			mergeFiles = append(mergeFiles, f)
		}
	}
	if len(mergeFiles) != 2 {
		t.Fatalf("expected merge_a.md + merge_b.md, got %v", mergeFiles)
	}

	parsed, err := ParseFiles(mergeFiles, specsDir, "")
	if err != nil {
		t.Fatal(err)
	}

	existing, err := model.LoadTasksFile(filepath.Join("testdata", "existing", "merge_existing.json"))
	if err != nil {
		t.Fatal(err)
	}

	merged, diff := MergeTasks(existing, parsed.Tasks)

	var want goldenMerge
	loadGolden(t, "merge.golden.json", &want)

	// The parse-warnings golden bakes in an absolute testdata/specs path in
	// the duplicate-ID message (Python includes it verbatim); trim the
	// same prefix Go's WalkSpecFiles produced before comparing.
	gotParse := toGoldenResult(parsed)
	for i, w := range gotParse.Warnings {
		gotParse.Warnings[i] = stripSpecsDirPrefix(w, specsDir)
	}
	if diff := gotWantDiff(gotParse, want.Parse); diff != "" {
		t.Errorf("parse result mismatch:\n%s", diff)
	}

	mergedJSON, err := json.Marshal(merged)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err := json.Marshal(want.Merged)
	if err != nil {
		t.Fatal(err)
	}
	if diff := gotWantDiff(json.RawMessage(mergedJSON), json.RawMessage(wantJSON)); diff != "" {
		t.Errorf("merged tasks.json mismatch:\n%s", diff)
	}

	if diff2 := gotWantDiff(idsOf(diff.NewTasks), want.Diff.NewTasks); diff2 != "" {
		t.Errorf("new tasks mismatch:\n%s", diff2)
	}
	if diff2 := gotWantDiff(idsOf(diff.Unchanged), want.Diff.Unchanged); diff2 != "" {
		t.Errorf("unchanged mismatch:\n%s", diff2)
	}
	if diff2 := gotWantDiff(idsOf(diff.Orphans), want.Diff.Orphans); diff2 != "" {
		t.Errorf("orphans mismatch:\n%s", diff2)
	}
	if diff2 := gotWantDiff(diff.DepWarnings, want.Diff.DepWarnings); diff2 != "" {
		t.Errorf("dep warnings mismatch:\n%s", diff2)
	}
	if len(diff.Updated) != len(want.Diff.Updated) {
		t.Fatalf("updated count mismatch: got %d want %d", len(diff.Updated), len(want.Diff.Updated))
	}
	for i, u := range diff.Updated {
		if u.New.ID != want.Diff.Updated[i].ID {
			t.Errorf("updated[%d].ID = %q, want %q", i, u.New.ID, want.Diff.Updated[i].ID)
		}
		gotChanged := append([]string(nil), u.Changed...)
		sort.Strings(gotChanged)
		wantChanged := append([]string(nil), want.Diff.Updated[i].Changed...)
		sort.Strings(wantChanged)
		if diff2 := gotWantDiff(gotChanged, wantChanged); diff2 != "" {
			t.Errorf("updated[%d].Changed mismatch:\n%s", i, diff2)
		}
	}
}

func idsOf(tasks []model.Task) []string {
	out := make([]string, len(tasks))
	for i, t := range tasks {
		out[i] = t.ID
	}
	return out
}

func stripSpecsDirPrefix(s, dir string) string {
	return strings.ReplaceAll(s, dir+string(filepath.Separator), "")
}

func gotWantDiff(got, want any) string {
	if reflect.DeepEqual(got, want) {
		return ""
	}
	gj, _ := json.MarshalIndent(got, "", "  ")
	wj, _ := json.MarshalIndent(want, "", "  ")
	return "got:\n" + string(gj) + "\nwant:\n" + string(wj)
}
