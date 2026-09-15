package ci

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// usebot's hand-written workflow, in the shape it has in that repository: a
// test job, a review job whose first step is named "Build review input", and
// a merge job.
const reviewedWorkflow = `name: orch-ci
on:
  pull_request:
  push:
    branches: [dev]
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Install dependencies
        run: pnpm install --frozen-lockfile
      - name: Type-check
        run: pnpm run type-check
      - name: Tests
        run: pnpm run test
  gemini-review:
    needs: test
    steps:
      - name: Build review input
        run: git diff origin/dev > diff.txt
      - name: Gemini review
        uses: google-github-actions/run-gemini-cli@v0
  merge:
    needs: [test, gemini-review]
    steps:
      - name: Squash-merge into dev
        run: gh pr merge --squash
`

func writeWorkflows(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".github", "workflows")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestGatesFromAReviewedWorkflow(t *testing.T) {
	wfs, err := ReadWorkflows(writeWorkflows(t, map[string]string{"orch-ci.yml": reviewedWorkflow}))
	if err != nil {
		t.Fatal(err)
	}
	// "Build review input" belongs to a review job, so it is not a build.
	if got := strings.Join(Gates(wfs), ","); got != "tests,typecheck,review" {
		t.Errorf("Gates = %s, want tests,typecheck,review", got)
	}
}

// What orch ci setup generates reads as the checks it runs.
func TestGatesFromAGeneratedWorkflow(t *testing.T) {
	st := Stack{Packages: []Package{{Dir: ".", Language: Go, Manager: "go", Lint: golangciLint, Test: "go test ./...", Build: "go build ./..."}}}
	body, err := Render(Jobs(st, AllChecks), Options{})
	if err != nil {
		t.Fatal(err)
	}
	wfs, err := ReadWorkflows(writeWorkflows(t, map[string]string{"orch-ci.yml": string(body)}))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(Gates(wfs), ","); got != "tests,lint,build" {
		t.Errorf("Gates = %s, want tests,lint,build", got)
	}
}

// Only workflows a pull request goes through count, and a broken file counts
// for nothing.
func TestGatesIgnoreWhatPRsDoNotRun(t *testing.T) {
	wfs, err := ReadWorkflows(writeWorkflows(t, map[string]string{
		"release.yml": "on: [push]\njobs:\n  build:\n    steps:\n      - run: make build\n",
		"broken.yml":  "on: pull_request\njobs: [unclosed",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got := Gates(wfs); len(got) != 0 {
		t.Errorf("Gates = %v, want none", got)
	}
}

func TestReadWorkflowsWithNoDirectory(t *testing.T) {
	wfs, err := ReadWorkflows(filepath.Join(t.TempDir(), "missing"))
	if err != nil || len(wfs) != 0 {
		t.Errorf("ReadWorkflows = %v, %v; want none and no error", wfs, err)
	}
}
