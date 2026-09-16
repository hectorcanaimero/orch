package sitegen

import (
	"strings"
	"testing"
)

func TestBuildSampleSpec(t *testing.T) {
	res := Build(SampleSpec)
	if len(res.Tasks) != 6 {
		t.Fatalf("tasks = %d, want 6 (warnings: %v)", len(res.Tasks), res.Warnings)
	}
	if len(res.Problems) != 0 || len(res.Warnings) != 0 {
		t.Fatalf("sample should be clean, got problems %v warnings %v", res.Problems, res.Warnings)
	}
	if res.Summary != "0 error(s) · 0 warning(s) · 0 total" {
		t.Errorf("summary = %q", res.Summary)
	}
	if got := strings.Join(res.CriticalPath, ">"); got != "F0.1.T1>F2.1.T1>F2.1.T2" {
		t.Errorf("critical path = %s", got)
	}
	if !strings.Contains(res.SVG, `class="dag-node is-critical"`) {
		t.Errorf("svg has no critical node trace")
	}
}

func TestBuildReportsValidateProblems(t *testing.T) {
	spec := "# F0 — X\n\n## F0.1 — Package: p\n\n### F0.1.T1 — A\n\n- **Model**: m\n- **Dependencies**: F0.1.T2\n\n" +
		"### F0.1.T2 — B\n\n- **Model**: m\n- **Dependencies**: F0.1.T1, F9.1.T9\n"
	res := Build(spec)
	kinds := map[string]bool{}
	for _, p := range res.Problems {
		kinds[p.Kind] = true
	}
	if !kinds["dep.missing"] || !kinds["dep.cycle"] {
		t.Fatalf("want dep.missing and dep.cycle, got %+v", res.Problems)
	}
	if res.SVG != "" {
		t.Errorf("a cyclic graph must not be drawn")
	}
	if !strings.Contains(res.Summary, "error(s)") || strings.HasPrefix(res.Summary, "0 error(s)") {
		t.Errorf("summary = %q", res.Summary)
	}
}

func TestDAGSVGEscapesTitles(t *testing.T) {
	res := Build("# F0 — X\n\n## F0.1 — Package: p\n\n### F0.1.T1 — <script>alert(1)</script>\n\n- **Model**: m\n")
	if len(res.Tasks) != 1 {
		t.Fatalf("tasks = %d", len(res.Tasks))
	}
	if strings.Contains(res.SVG, "<script>") {
		t.Fatalf("title not escaped: %s", res.SVG)
	}
}

func TestDAGSVGTracesCriticalPathOnlyWithBranches(t *testing.T) {
	line := Build("# F0 — X\n\n## F0.1 — Package: p\n\n### F0.1.T1 — A\n\n- **Model**: m\n\n### F0.1.T2 — B\n\n- **Model**: m\n- **Dependencies**: F0.1.T1\n")
	if strings.Contains(line.SVG, "is-critical") {
		t.Errorf("a straight line must not trace a critical path")
	}
}
