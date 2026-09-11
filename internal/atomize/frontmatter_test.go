package atomize

import (
	"path/filepath"
	"strings"
	"testing"
)

// Ports TestFrontmatterExtractor.

func TestExtractFrontmatterNoneReturnsFullText(t *testing.T) {
	text := "# F1 — X\n### F1.1.T1 — Task\n"
	fm, body := extractFrontmatter(text)
	if fm.Present {
		t.Errorf("Present = true, want false")
	}
	if body != text {
		t.Errorf("body mismatch: got %q want %q", body, text)
	}
}

func TestExtractFrontmatterValid(t *testing.T) {
	text := "---\n" +
		"type: spec\n" +
		"project_id: rupies\n" +
		"phase: 1\n" +
		"package: authentication\n" +
		"generated_by: orch-spec\n" +
		"consumed_by: [orch-atomizer]\n" +
		"---\n" +
		"# F1 — Auth\n"
	fm, body := extractFrontmatter(text)
	if !fm.Present {
		t.Fatalf("Present = false, want true")
	}
	if fm.Type != "spec" || fm.ProjectID != "rupies" || fm.Package != "authentication" || fm.GeneratedBy != "orch-spec" {
		t.Errorf("fm = %+v", fm)
	}
	if fm.Phase == nil || *fm.Phase != 1 {
		t.Errorf("Phase = %v, want 1", fm.Phase)
	}
	if diff := gotWantDiff(fm.ConsumedBy, []string{"orch-atomizer"}); diff != "" {
		t.Errorf("ConsumedBy mismatch:\n%s", diff)
	}
	if !strings.HasPrefix(body, "# F1 — Auth") {
		t.Errorf("body = %q", body)
	}
	if len(fm.Warnings) != 0 {
		t.Errorf("Warnings = %v, want none", fm.Warnings)
	}
}

func TestExtractFrontmatterMalformedYAMLTreatedAsLegacy(t *testing.T) {
	text := "---\ntype: spec\nphase: [unclosed\n---\n# F1 — X\n"
	fm, body := extractFrontmatter(text)
	if fm.Present {
		t.Errorf("Present = true, want false")
	}
	found := false
	for _, w := range fm.Warnings {
		if strings.Contains(w, "malformado") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a 'malformado' warning, got %v", fm.Warnings)
	}
	if body != text {
		t.Errorf("body must be the entire original text on a YAML parse error")
	}
}

func TestExtractFrontmatterInvalidTypeWarns(t *testing.T) {
	text := "---\ntype: banana\n---\n# F1 — X\n"
	fm, _ := extractFrontmatter(text)
	if !fm.Present || fm.Type != "banana" {
		t.Fatalf("fm = %+v", fm)
	}
	if !anyWarningContains(fm.Warnings, "no es válido") {
		t.Errorf("warnings = %v", fm.Warnings)
	}
}

func TestExtractFrontmatterNonAtomizerTypeWarns(t *testing.T) {
	text := "---\ntype: prd\n---\n# irrelevant\n"
	fm, _ := extractFrontmatter(text)
	if fm.Type != "prd" {
		t.Fatalf("Type = %q", fm.Type)
	}
	if !anyWarningContains(fm.Warnings, "no es un tipo consumible") {
		t.Errorf("warnings = %v", fm.Warnings)
	}
}

func TestExtractFrontmatterConsumedByWithoutAtomizerWarns(t *testing.T) {
	text := "---\ntype: spec\nconsumed_by: [some-other-tool]\n---\n# F1 — X\n"
	fm, _ := extractFrontmatter(text)
	if !anyWarningContains(fm.Warnings, "orch-atomizer") {
		t.Errorf("warnings = %v", fm.Warnings)
	}
}

func anyWarningContains(warnings []string, substr string) bool {
	for _, w := range warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

// Ports TestParseSpecWithFrontmatter.

func TestParseSpecFrontmatterParsedAndTasksExtracted(t *testing.T) {
	dir := t.TempDir()
	writeSpec(t, dir, "f1-auth.md",
		"---\n"+
			"type: spec\n"+
			"project_id: rupies\n"+
			"phase: 1\n"+
			"package: authentication\n"+
			"generated_by: orch-spec\n"+
			"consumed_by: [orch-atomizer]\n"+
			"---\n"+
			"# F1 — Auth\n\n"+
			"## F1.1 — auth pkg\n\n"+
			"### F1.1.T1 — Domain: Auth model\n\n"+
			"- **Modelo**: claude-sonnet-4-6\n"+
			"- **Estimación**: 4h\n")

	r, err := ParseFile(filepath.Join(dir, "f1-auth.md"), dir, "rupies")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Tasks) != 1 {
		t.Fatalf("tasks = %v", r.Tasks)
	}
	tk := r.Tasks[0]
	if tk.ID != "F1.1.T1" || tk.Phase != 1 || tk.Model != "claude-sonnet-4-6" {
		t.Errorf("task = %+v", tk)
	}
	if len(r.Warnings) != 0 {
		t.Errorf("warnings = %v, want none", r.Warnings)
	}
}

func TestParseSpecFrontmatterPhaseDefaultWhenNoHeader(t *testing.T) {
	dir := t.TempDir()
	writeSpec(t, dir, "f5-x.md",
		"---\n"+
			"type: spec\n"+
			"phase: 5\n"+
			"---\n"+
			"## F5.1 — pkg\n\n"+
			"### F5.1.T1 — Task\n"+
			"- **Modelo**: opus\n")

	r, err := ParseFile(filepath.Join(dir, "f5-x.md"), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if anyWarningContains(r.Warnings, "sin header de fase previo") {
		t.Errorf("unexpected phase warning: %v", r.Warnings)
	}
	if r.Tasks[0].Phase != 5 {
		t.Errorf("phase = %d, want 5", r.Tasks[0].Phase)
	}
}

func TestParseSpecProjectIDMismatchWarns(t *testing.T) {
	dir := t.TempDir()
	writeSpec(t, dir, "x.md",
		"---\ntype: spec\nproject_id: other-project\n---\n"+
			"# F1 — X\n### F1.1.T1 — Task\n- **Modelo**: opus\n")

	r, err := ParseFile(filepath.Join(dir, "x.md"), dir, "rupies")
	if err != nil {
		t.Fatal(err)
	}
	if !anyWarningContains(r.Warnings, "no matchea con project activo 'rupies'") {
		t.Errorf("warnings = %v", r.Warnings)
	}
	if len(r.Tasks) != 1 {
		t.Errorf("tasks = %v, want 1 (mismatch is a warning, not fatal)", r.Tasks)
	}
}

func TestParseSpecBackcompatNoFrontmatter(t *testing.T) {
	dir := t.TempDir()
	writeSpec(t, dir, "legacy.md",
		"# F0 — Bootstrap\n\n"+
			"## F0.1 — repo\n\n"+
			"### F0.1.T1 — Task\n"+
			"- **Modelo**: opus\n"+
			"- **Estimación**: 8h\n")

	r, err := ParseFile(filepath.Join(dir, "legacy.md"), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Tasks) != 1 || r.Tasks[0].ID != "F0.1.T1" || r.Tasks[0].EstimateHours != 8.0 {
		t.Errorf("tasks = %+v", r.Tasks)
	}
	if len(r.Warnings) != 0 {
		t.Errorf("warnings = %v, want none", r.Warnings)
	}
}

func TestParseSpecMalformedFrontmatterParsesBodyAsLegacy(t *testing.T) {
	dir := t.TempDir()
	writeSpec(t, dir, "broken.md",
		"---\ntype: spec\nphase: [unclosed\n---\n"+
			"# F2 — X\n### F2.1.T1 — Task\n- **Modelo**: opus\n")

	r, err := ParseFile(filepath.Join(dir, "broken.md"), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if !anyWarningContains(r.Warnings, "malformado") {
		t.Errorf("warnings = %v", r.Warnings)
	}
	if len(r.Tasks) != 1 || r.Tasks[0].ID != "F2.1.T1" {
		t.Errorf("tasks = %+v", r.Tasks)
	}
}
