package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hectorcanaimero/orch/internal/ci"
)

func ciRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

var pnpmApp = map[string]string{
	"package.json":   `{"scripts":{"type-check":"tsc --noEmit","test":"vitest run"}}`,
	"pnpm-lock.yaml": "lockfileVersion: '9.0'\n",
}

func TestCIDetectJSON(t *testing.T) {
	root := ciRepo(t, pnpmApp)
	out, err := runRoot(t, "dev", "ci", "detect", "--json", "--project-root", root)
	if err != nil {
		t.Fatalf("ci detect: %v\n%s", err, out)
	}
	var got struct {
		Packages []ciPackageJSON `json:"packages"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if len(got.Packages) != 1 || got.Packages[0].Manager != "pnpm" || got.Packages[0].Typecheck != "pnpm run type-check" || got.Packages[0].Lint != "" {
		t.Errorf("packages = %+v", got.Packages)
	}
}

func TestCISetupDryRunWritesNothing(t *testing.T) {
	root := ciRepo(t, pnpmApp)
	out, err := runRoot(t, "dev", "ci", "setup", "--dry-run", "--no-test", "--project-root", root)
	if err != nil {
		t.Fatalf("setup --dry-run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "run: pnpm run type-check") || strings.Contains(out, "vitest") || strings.Contains(out, "pnpm run test") {
		t.Errorf("dry run output:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(root, workflowPath)); err == nil {
		t.Errorf("--dry-run wrote the workflow")
	}
}

func TestCISetupWritesAndProtectsTheWorkflow(t *testing.T) {
	root := ciRepo(t, pnpmApp)
	target := filepath.Join(root, workflowPath)

	// Without a terminal it will not write unasked.
	if _, err := runRoot(t, "dev", "ci", "setup", "--project-root", root); err == nil || !strings.Contains(err.Error(), "pass --yes") {
		t.Errorf("no terminal, no --yes: err = %v", err)
	}

	out, err := runRoot(t, "dev", "ci", "setup", "--yes", "--base", "dev", "--project-root", root)
	if err != nil {
		t.Fatalf("setup --yes: %v\n%s", err, out)
	}
	written, err := os.ReadFile(target) // #nosec G304 -- a temp dir this test made
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(written), "branches: [dev]") || !strings.Contains(string(written), "run: pnpm run test") {
		t.Errorf("workflow:\n%s", written)
	}

	out, err = runRoot(t, "dev", "ci", "setup", "--yes", "--base", "dev", "--project-root", root)
	if err != nil || !strings.Contains(out, "already up to date") {
		t.Errorf("second run: %v\n%s", err, out)
	}

	// A hand-edited workflow is not replaced without --force.
	if err := os.WriteFile(target, []byte("name: mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runRoot(t, "dev", "ci", "setup", "--yes", "--project-root", root); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Errorf("existing workflow without --force: err = %v", err)
	}
	if b, _ := os.ReadFile(target); string(b) != "name: mine\n" { // #nosec G304 -- a temp dir this test made
		t.Errorf("the hand-edited workflow was replaced: %q", b)
	}
	if _, err := runRoot(t, "dev", "ci", "setup", "--force", "--project-root", root); err != nil {
		t.Fatalf("--force: %v", err)
	}
	if b, _ := os.ReadFile(target); !strings.Contains(string(b), "name: orch-ci") { // #nosec G304 -- a temp dir this test made
		t.Errorf("--force did not replace it: %q", b)
	}
}

// The push branch follows the orch project's dispatch.base_branch.
func TestCISetupUsesTheProjectBaseBranch(t *testing.T) {
	files := map[string]string{".orchestrator/config.yaml": "dispatch:\n  base_branch: develop\n"}
	for k, v := range pnpmApp {
		files[k] = v
	}
	root := ciRepo(t, files)
	out, err := runRoot(t, "dev", "ci", "setup", "--dry-run", "--project-root", root)
	if err != nil || !strings.Contains(out, "branches: [develop]") {
		t.Errorf("setup: %v\n%s", err, out)
	}
}

func TestCISetupWithNothingToCheck(t *testing.T) {
	root := ciRepo(t, map[string]string{"README.md": "hi\n"})
	out, err := runRoot(t, "dev", "ci", "setup", "--yes", "--project-root", root)
	if err == nil || !strings.Contains(err.Error(), "found nothing to check") {
		t.Errorf("err = %v\n%s", err, out)
	}
}

func TestAskChecksOnlyAsksAboutWhatExists(t *testing.T) {
	st := ci.Stack{Packages: []ci.Package{{Dir: ".", Typecheck: "pnpm run type-check", Test: "pnpm run test"}}}
	var out bytes.Buffer
	// typecheck: no; tests: default (yes).
	got, err := askChecks(bufio.NewReader(strings.NewReader("n\n\n")), &out, st, ci.AllChecks)
	if err != nil {
		t.Fatal(err)
	}
	if got.Typecheck || !got.Test || !got.Lint || !got.Build {
		t.Errorf("checks = %+v, want typecheck off and the rest untouched", got)
	}
	if strings.Contains(out.String(), "lint") || strings.Contains(out.String(), "build") {
		t.Errorf("asked about checks the repo does not have:\n%s", out.String())
	}
}

func TestAskYesNo(t *testing.T) {
	cases := []struct {
		in   string
		def  bool
		want bool
	}{
		{"\n", true, true}, {"\n", false, false}, {"sí\n", false, true}, {"no\n", true, false}, {"", true, true},
	}
	for _, c := range cases {
		got, err := askYesNo(bufio.NewReader(strings.NewReader(c.in)), &bytes.Buffer{}, "?", c.def)
		if err != nil || got != c.want {
			t.Errorf("askYesNo(%q, def %v) = %v, %v; want %v", c.in, c.def, got, err, c.want)
		}
	}
}
