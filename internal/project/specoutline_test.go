package project

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hectorcanaimero/orch/internal/model"
)

// A client reads "Campaña de lanzamiento › estrategia", not "Phase 6" or
// "F6.1". tasks.json names almost no phases; the specs name every phase
// (`# F6 — …`) and every package (`## F6.1 — Package: …`).
func TestSpecOutline(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, rel), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("specs/f6.md", "---\ntype: spec\n---\n\n# F6 — Campaña de lanzamiento\n\n## F6.1 — Package: estrategia\n\n### F6.1.T1 — x\n\n## F6.2 — Piezas\r\n")
	write("specs/f2.md", "# F2 - Cobros\n")

	tasks := []model.Task{
		{ID: "F6.1.T1", Phase: 6, SpecRef: "f6.md#F6.1.T1"},
		{ID: "F6.2.T1", Phase: 6, SpecRef: "specs/f6.md#F6.2.T1"}, // pre-bug-12 prefix
		{ID: "F2.1.T1", Phase: 2, SpecRef: "f2.md"},
		{ID: "F9.1.T1", Phase: 9, SpecRef: "missing.md"},
	}
	got, err := SpecOutline(root, "specs", tasks)
	if err != nil {
		t.Fatalf("SpecOutline: %v", err)
	}
	wantPhases := map[int]string{6: "Campaña de lanzamiento", 2: "Cobros"}
	for n, name := range wantPhases {
		if got.Phases[n] != name {
			t.Errorf("phase %d = %q, want %q", n, got.Phases[n], name)
		}
	}
	if _, ok := got.Phases[9]; ok {
		t.Error("a missing spec produced a phase title")
	}
	wantPackages := map[string]string{"6.1": "estrategia", "6.2": "Piezas"}
	for k, name := range wantPackages {
		if got.Packages[k] != name {
			t.Errorf("package %s = %q, want %q", k, got.Packages[k], name)
		}
	}
	if len(got.Packages) != len(wantPackages) {
		t.Errorf("packages = %v, want only %v (a task header is not a package)", got.Packages, wantPackages)
	}
}

func TestPackageKey(t *testing.T) {
	for id, want := range map[string]string{"F6.1.T1": "6.1", "F12.3.T10": "12.3", "gh-42": "", "F6.T1": ""} {
		if got := PackageKey(id); got != want {
			t.Errorf("PackageKey(%q) = %q, want %q", id, got, want)
		}
	}
}
