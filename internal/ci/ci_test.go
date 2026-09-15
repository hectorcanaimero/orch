package ci

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

var update = flag.Bool("update", false, "rewrite the golden workflows under testdata/")

// repo writes a repository tree from path → content.
func repo(t *testing.T, files map[string]string) string {
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

// golden compares the workflow to testdata/<name>.yml, and checks that it is
// YAML GitHub can read with the jobs it claims.
func golden(t *testing.T, name string, got []byte, wantJobs ...string) {
	t.Helper()
	var doc struct {
		On   map[string]any `yaml:"on"`
		Jobs map[string]struct {
			Steps []map[string]any `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(got, &doc); err != nil {
		t.Fatalf("the generated workflow is not YAML: %v\n%s", err, got)
	}
	if _, ok := doc.On["pull_request"]; !ok {
		t.Errorf("the workflow does not run on pull_request:\n%s", got)
	}
	if len(doc.Jobs) != len(wantJobs) {
		t.Errorf("jobs = %d, want %v", len(doc.Jobs), wantJobs)
	}
	for _, j := range wantJobs {
		if len(doc.Jobs[j].Steps) == 0 {
			t.Errorf("job %q is missing or empty", j)
		}
	}

	path := filepath.Join("testdata", name+".yml")
	if *update {
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path) // #nosec G304 -- a golden file of this package
	if err != nil {
		t.Fatalf("%v (run go test ./internal/ci -update to create it)", err)
	}
	if string(got) != string(want) {
		t.Errorf("workflow differs from %s (go test ./internal/ci -update to accept):\n%s", path, got)
	}
}

func render(t *testing.T, root string, checks Checks) (Stack, []byte) {
	t.Helper()
	st, err := Detect(root)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	out, err := Render(Jobs(st, checks), Options{BaseBranch: "dev", Checks: checks})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	return st, out
}

// orch's own shape: Go at the root with a golangci config, a pnpm web app in
// a subdirectory with its own lockfile.
func TestGoWithAWebApp(t *testing.T) {
	root := repo(t, map[string]string{
		"go.mod":                       "module example.com/x\n\ngo 1.25.0\n",
		".golangci.yml":                "version: \"2\"\n",
		"web/package.json":             `{"packageManager":"pnpm@9.15.0","scripts":{"lint":"oxlint","typecheck":"tsc -b","test":"vitest run","build":"vite build"}}`,
		"web/pnpm-lock.yaml":           "lockfileVersion: '9.0'\n",
		"web/.nvmrc":                   "20\n",
		"docs/package.json":            `{"scripts":{"build":"never a package"}}`,
		".github/workflows/review.yml": "on: pull_request\n",
	})
	st, out := render(t, root, AllChecks)
	if strings.Join(st.Workflows, ",") != "review.yml" {
		t.Errorf("workflows = %v", st.Workflows)
	}
	golden(t, "go-and-web", out, "go", "web")
}

// A pnpm workspace runs its members through the root's scripts, so a member
// gets no job of its own; a Python service beside it does.
func TestPnpmWorkspaceWithAPythonService(t *testing.T) {
	root := repo(t, map[string]string{
		"package.json":                `{"scripts":{"lint":"turbo lint","test":"turbo test","build":"turbo build"}}`,
		"pnpm-workspace.yaml":         "packages: ['apps/*']\n",
		"pnpm-lock.yaml":              "lockfileVersion: '9.0'\n",
		"apps/web/package.json":       `{"scripts":{"test":"vitest"}}`,
		"services/api/pyproject.toml": "[project]\nname = \"api\"\n\n[tool.ruff]\nline-length = 100\n\n[tool.pytest.ini_options]\ntestpaths = [\"tests\"]\n",
		"services/api/uv.lock":        "version = 1\n",
	})
	_, out := render(t, root, AllChecks)
	golden(t, "pnpm-workspace-python", out, "node", "services-api")
}

// npm with no lockfile: no dependency cache (setup-node refuses one without
// a lockfile), and npm init's failing placeholder test is not a test.
func TestNpmWithoutALockfile(t *testing.T) {
	root := repo(t, map[string]string{
		"package.json": `{"scripts":{"lint":"eslint .","test":"echo \"Error: no test specified\" && exit 1"}}`,
	})
	st, out := render(t, root, AllChecks)
	if p := st.Packages[0]; p.Test != "" || p.Install != "npm install" || p.Lockfile != "" {
		t.Errorf("package = %+v", p)
	}
	golden(t, "npm-no-lockfile", out, "node")
}

func TestPoetryWithMypy(t *testing.T) {
	root := repo(t, map[string]string{
		"pyproject.toml":  "[tool.poetry]\nname = \"x\"\n\n[tool.mypy]\nstrict = true\n",
		"poetry.lock":     "",
		".python-version": "3.11\n",
		"tests/test_x.py": "def test_x(): pass\n",
	})
	_, out := render(t, root, AllChecks)
	golden(t, "poetry-mypy", out, "python")
}

func TestRustAndChoosingChecks(t *testing.T) {
	root := repo(t, map[string]string{"Cargo.toml": "[package]\nname = \"x\"\n"})
	_, out := render(t, root, Checks{Test: true})
	golden(t, "rust-tests-only", out, "rust")
	if strings.Contains(string(out), "clippy --all-targets") {
		t.Errorf("lint was not chosen but is in the workflow:\n%s", out)
	}
}

func TestMakefileOnly(t *testing.T) {
	root := repo(t, map[string]string{"Makefile": ".PHONY: test\ntest:\n\t./run-tests.sh\n\ninstall:\n\tcp x y\n"})
	st, out := render(t, root, AllChecks)
	if p := st.Packages[0]; p.Language != Make || p.Test != "make test" || p.Build != "" {
		t.Errorf("package = %+v", p)
	}
	golden(t, "makefile", out, "make")
}

// Nothing to check is an error, not an empty workflow that passes every PR.
func TestNothingToCheck(t *testing.T) {
	root := repo(t, map[string]string{"README.md": "hi\n", "package.json": `{"name":"x"}`})
	st, err := Detect(root)
	if err != nil {
		t.Fatal(err)
	}
	if st.HasChecks() {
		t.Errorf("HasChecks = true for %+v", st.Packages)
	}
	if _, err := Render(Jobs(st, AllChecks), Options{}); err == nil || !strings.Contains(err.Error(), "nothing to check") {
		t.Errorf("Render err = %v, want nothing to check", err)
	}
}

func TestDetectReportsAnUnreadablePackageJSON(t *testing.T) {
	root := repo(t, map[string]string{"package.json": "{not json"})
	if _, err := Detect(root); err == nil || !strings.Contains(err.Error(), "package.json") {
		t.Errorf("err = %v, want one naming package.json", err)
	}
}

func TestScalarQuotesWhatYAMLWouldParse(t *testing.T) {
	cases := map[string]string{
		"go test ./...":           "go test ./...",
		`pip install -e ".[dev]"`: `"pip install -e \".[dev]\""`,
		"echo a: b":               `"echo a: b"`,
		"-x":                      `"-x"`,
		"make test # all":         `"make test # all"`,
	}
	for in, want := range cases {
		if got := scalar(in); got != want {
			t.Errorf("scalar(%q) = %s, want %s", in, got, want)
		}
		var back string
		if err := yaml.Unmarshal([]byte("v: "+scalar(in)), &struct {
			V *string `yaml:"v"`
		}{&back}); err != nil || back != in {
			t.Errorf("%q does not read back: %q, %v", in, back, err)
		}
	}
}
