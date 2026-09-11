package atomize

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Ports TestParseEstimate from orchestrator/tests/test_atomize.py.
func TestParseEstimate(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want float64
	}{
		{"hours_int", "8h", 8.0},
		{"hours_float", "1.5h", 1.5},
		{"minutes", "30m", 0.5},
		{"days_to_workhours", "2d", 16.0},
		{"no_unit_assumes_hours", "3", 3.0},
		{"empty_returns_zero", "", 0.0},
		{"blank_returns_zero", "   ", 0.0},
		{"junk_returns_zero", "mucho", 0.0},
		{"comma_decimal", "1,5h", 1.5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseEstimate(c.in); got != c.want {
				t.Errorf("parseEstimate(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// Ports TestParseDeps.
func TestParseDeps(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"simple", "F1.1.T8, F1.1.T5", []string{"F1.1.T8", "F1.1.T5"}},
		{"no_spaces", "A,B,C", []string{"A", "B", "C"}},
		{"empty", "", []string{}},
		{"trailing_comma", "A, B,", []string{"A", "B"}},
		{"semicolon_separator", "A; B", []string{"A", "B"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseDeps(c.in)
			if diff := gotWantDiff(got, c.want); diff != "" {
				t.Errorf("parseDeps(%q) mismatch:\n%s", c.in, diff)
			}
		})
	}
}

// Ports TestParserInlineFiles.test_inline_single_file.
func TestParseInlineSingleFile(t *testing.T) {
	dir := t.TempDir()
	writeSpec(t, dir, "x.md",
		"# F2 — X\n\n## F2.1 — pkg\n\n### F2.1.T1 — inline files\n\n"+
			"- **Files**: `lib/x.dart`\n")

	r, err := ParseFile(filepath.Join(dir, "x.md"), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if diff := gotWantDiff(r.Tasks[0].Files, []string{"lib/x.dart"}); diff != "" {
		t.Errorf("files mismatch:\n%s", diff)
	}
}

// Ports TestParserUnknownField.test_unknown_label_emits_warning.
func TestParseUnknownFieldWarns(t *testing.T) {
	dir := t.TempDir()
	writeSpec(t, dir, "x.md",
		"# F1 — X\n### F1.1.T1 — Task\n"+
			"- **Foo**: bar\n"+
			"- **Modelo**: opus\n")

	r, err := ParseFile(filepath.Join(dir, "x.md"), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, w := range r.Warnings {
		if strings.Contains(w, "desconocido") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected an 'unknown field' warning, got %v", r.Warnings)
	}
	if r.Tasks[0].Model != "opus" {
		t.Errorf("model = %q, want opus", r.Tasks[0].Model)
	}
}

func writeSpec(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
