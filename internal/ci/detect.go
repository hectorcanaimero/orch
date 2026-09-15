// Package ci reads a repository to find what it is built from, and proposes
// the GitHub Actions pipeline that checks it: install, lint, typecheck, test
// and build, each only where the repo actually has one. `orch ci setup` and
// `orch init` write what it proposes.
//
// Detection reads files and never runs anything: a lockfile says which
// package manager, a script or a tool's config section says which check
// exists. A check that is not there is left out rather than guessed, because
// a generated step that fails on day one teaches people to ignore CI.
package ci

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Language is a toolchain family a package is built with.
type Language string

const (
	Go     Language = "go"
	Node   Language = "node"
	Python Language = "python"
	Rust   Language = "rust"
	Make   Language = "make"
)

// Package is one buildable unit of the repository.
type Package struct {
	// Dir is the package's directory relative to the repo root, "." for the
	// root itself, always with forward slashes.
	Dir      string
	Language Language
	// Manager is how dependencies are installed: go, pnpm, yarn, npm, bun,
	// uv, poetry, pip, cargo, make.
	Manager string
	// Version is the toolchain version the repo pins (.nvmrc,
	// .python-version, ...), "" when it pins none.
	Version string
	// Install installs the dependencies; "" when nothing needs installing.
	Install string
	// The checks the repo has, as the command that runs each; "" when absent.
	Lint, Typecheck, Test, Build string
	// Workspace is true for a node package whose scripts cover its
	// workspace members (pnpm-workspace.yaml or package.json workspaces).
	Workspace bool
	// PnpmVersion is the pnpm major the repo declares in packageManager, ""
	// when it declares none.
	PnpmVersion string
	// Lockfile is the node lockfile's path from the repo root, "" when there
	// is none. setup-node's dependency cache refuses to run without one.
	Lockfile string
}

// Stack is what Detect found.
type Stack struct {
	Packages []Package
	// Workflows are the files already under .github/workflows, sorted.
	Workflows []string
}

// skipDirs are never packages of their own: dependencies, build output,
// fixtures and orch's own state.
var skipDirs = map[string]bool{
	"node_modules": true, "vendor": true, "dist": true, "build": true, "target": true,
	"testdata": true, "coverage": true, "tmp": true, "docs": true,
}

// memberParents hold the packages of a monorepo one level further down.
var memberParents = []string{"apps", "packages", "services", "libs"}

// Detect reads the repository at root.
func Detect(root string) (Stack, error) {
	var st Stack
	dirs, err := candidateDirs(root)
	if err != nil {
		return st, err
	}

	nodeWorkspace := false
	for _, dir := range dirs {
		pkgs, err := detectDir(root, dir)
		if err != nil {
			return st, err
		}
		for _, p := range pkgs {
			// A node package under a node workspace root is run by the
			// root's scripts; a job of its own would run it twice.
			if p.Language == Node && nodeWorkspace && dir != "." {
				continue
			}
			st.Packages = append(st.Packages, p)
			nodeWorkspace = nodeWorkspace || (p.Language == Node && p.Workspace && dir == ".")
		}
	}

	wf, err := filepath.Glob(filepath.Join(root, ".github", "workflows", "*.y*ml"))
	if err != nil {
		return st, fmt.Errorf("listing the existing workflows: %w", err)
	}
	for _, f := range wf {
		st.Workflows = append(st.Workflows, filepath.Base(f))
	}
	sort.Strings(st.Workflows)
	return st, nil
}

// candidateDirs are the root, its direct children, and the children of the
// usual monorepo parents, in that order.
func candidateDirs(root string) ([]string, error) {
	out := []string{"."}
	children, err := subdirs(root, "")
	if err != nil {
		return nil, err
	}
	for _, c := range children {
		isParent := false
		for _, p := range memberParents {
			isParent = isParent || c == p
		}
		if !isParent {
			out = append(out, c)
			continue
		}
		members, err := subdirs(root, c)
		if err != nil {
			return nil, err
		}
		out = append(out, members...)
	}
	return out, nil
}

func subdirs(root, rel string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(root, rel))
	if err != nil {
		if rel != "" && errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", filepath.Join(root, rel), err)
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || strings.HasPrefix(name, ".") || skipDirs[name] {
			continue
		}
		out = append(out, filepath.ToSlash(filepath.Join(rel, name)))
	}
	sort.Strings(out)
	return out, nil
}

func detectDir(root, dir string) ([]Package, error) {
	abs := filepath.Join(root, dir)
	var out []Package
	add := func(p Package, err error) error {
		if err != nil {
			return err
		}
		if p.Language != "" {
			p.Dir = dir
			out = append(out, p)
		}
		return nil
	}
	if err := add(detectGo(abs)); err != nil {
		return nil, err
	}
	if err := add(detectNode(root, abs)); err != nil {
		return nil, err
	}
	if err := add(detectPython(abs)); err != nil {
		return nil, err
	}
	if err := add(detectRust(abs)); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		if err := add(detectMake(abs)); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func exists(dir string, names ...string) bool {
	for _, n := range names {
		if _, err := os.Stat(filepath.Join(dir, n)); err == nil {
			return true
		}
	}
	return false
}

// readOptional returns a file's content, or "" when it does not exist.
func readOptional(dir, name string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dir, name)) // #nosec G304 -- a file of the repository being read
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", filepath.Join(dir, name), err)
	}
	return string(b), nil
}

func detectGo(dir string) (Package, error) {
	if !exists(dir, "go.mod") {
		return Package{}, nil
	}
	p := Package{Language: Go, Manager: "go", Install: "go mod download",
		Test: "go test ./...", Build: "go build ./..."}
	if exists(dir, ".golangci.yml", ".golangci.yaml", ".golangci.toml", ".golangci.json") {
		p.Lint = golangciLint
	} else {
		p.Lint = "go vet ./..."
	}
	return p, nil
}

// npmDefaultTest is the `test` script `npm init` writes, which fails.
const npmDefaultTest = `echo "Error: no test specified" && exit 1`

var typecheckScripts = []string{"typecheck", "type-check", "check-types", "tsc"}

func detectNode(root, dir string) (Package, error) {
	raw, err := readOptional(dir, "package.json")
	if err != nil || raw == "" {
		return Package{}, err
	}
	var pkg struct {
		Scripts        map[string]string `json:"scripts"`
		Workspaces     json.RawMessage   `json:"workspaces"`
		PackageManager string            `json:"packageManager"`
		Engines        struct {
			Node string `json:"node"`
		} `json:"engines"`
	}
	if err := json.Unmarshal([]byte(raw), &pkg); err != nil {
		return Package{}, fmt.Errorf("reading %s: %w", filepath.Join(dir, "package.json"), err)
	}

	p := Package{Language: Node}
	// A member of a workspace has its lockfile at the repo root.
	lockDir := dir
	if !exists(dir, "pnpm-lock.yaml", "yarn.lock", "package-lock.json", "bun.lockb", "bun.lock") {
		lockDir = root
	}
	switch {
	case exists(lockDir, "pnpm-lock.yaml") || strings.HasPrefix(pkg.PackageManager, "pnpm@"):
		p.Manager, p.Install = "pnpm", "pnpm install --frozen-lockfile"
		if v := strings.TrimPrefix(pkg.PackageManager, "pnpm@"); v != pkg.PackageManager {
			p.PnpmVersion = v
		}
	case exists(lockDir, "yarn.lock") || strings.HasPrefix(pkg.PackageManager, "yarn@"):
		p.Manager, p.Install = "yarn", "yarn install --frozen-lockfile"
		if exists(lockDir, ".yarnrc.yml") {
			p.Install = "yarn install --immutable"
		}
	case exists(lockDir, "bun.lockb", "bun.lock"):
		p.Manager, p.Install = "bun", "bun install --frozen-lockfile"
	case exists(lockDir, "package-lock.json"):
		p.Manager, p.Install = "npm", "npm ci"
	default:
		p.Manager, p.Install = "npm", "npm install"
	}
	lockName := map[string][]string{
		"pnpm": {"pnpm-lock.yaml"}, "yarn": {"yarn.lock"}, "npm": {"package-lock.json"}, "bun": {"bun.lock", "bun.lockb"},
	}[p.Manager]
	for _, name := range lockName {
		if exists(lockDir, name) {
			rel, err := filepath.Rel(root, filepath.Join(lockDir, name))
			if err != nil {
				return Package{}, fmt.Errorf("locating %s: %w", name, err)
			}
			p.Lockfile = filepath.ToSlash(rel)
			break
		}
	}
	p.Workspace = exists(dir, "pnpm-workspace.yaml") || (len(pkg.Workspaces) > 0 && string(pkg.Workspaces) != "null")

	if v, err := readOptional(dir, ".nvmrc"); err != nil {
		return Package{}, err
	} else if v = strings.TrimSpace(v); v != "" {
		p.Version = v
	} else if v, err := readOptional(dir, ".node-version"); err != nil {
		return Package{}, err
	} else if v = strings.TrimSpace(v); v != "" {
		p.Version = v
	} else if m := regexp.MustCompile(`\d+`).FindString(pkg.Engines.Node); m != "" {
		p.Version = m
	}

	run := func(script string) string {
		if p.Manager == "npm" {
			if script == "test" {
				return "npm test"
			}
			return "npm run " + script
		}
		return p.Manager + " run " + script
	}
	if _, ok := pkg.Scripts["lint"]; ok {
		p.Lint = run("lint")
	}
	for _, s := range typecheckScripts {
		if _, ok := pkg.Scripts[s]; ok {
			p.Typecheck = run(s)
			break
		}
	}
	if t, ok := pkg.Scripts["test"]; ok && strings.TrimSpace(t) != npmDefaultTest {
		p.Test = run("test")
	}
	if _, ok := pkg.Scripts["build"]; ok {
		p.Build = run("build")
	}
	return p, nil
}

func detectPython(dir string) (Package, error) {
	pyproject, err := readOptional(dir, "pyproject.toml")
	if err != nil {
		return Package{}, err
	}
	if pyproject == "" && !exists(dir, "requirements.txt", "setup.py") {
		return Package{}, nil
	}
	p := Package{Language: Python}
	prefix := ""
	switch {
	case exists(dir, "uv.lock"):
		p.Manager, p.Install, prefix = "uv", "uv sync --frozen", "uv run "
	case exists(dir, "poetry.lock"):
		p.Manager, p.Install, prefix = "poetry", "pipx install poetry && poetry install", "poetry run "
	case exists(dir, "requirements.txt"):
		p.Manager, p.Install = "pip", "pip install -r requirements.txt"
		if exists(dir, "requirements-dev.txt") {
			p.Install += " -r requirements-dev.txt"
		}
	default:
		p.Manager, p.Install = "pip", "pip install -e ."
		if strings.Contains(pyproject, "\ndev = [") || strings.Contains(pyproject, "\ndev=[") {
			p.Install = `pip install -e ".[dev]"`
		}
	}
	if v, err := readOptional(dir, ".python-version"); err != nil {
		return Package{}, err
	} else {
		p.Version = strings.TrimSpace(v)
	}

	if strings.Contains(pyproject, "[tool.ruff") || exists(dir, "ruff.toml", ".ruff.toml") {
		p.Lint = prefix + "ruff check ."
	}
	if strings.Contains(pyproject, "[tool.mypy") || exists(dir, "mypy.ini") {
		p.Typecheck = prefix + "mypy ."
	}
	if strings.Contains(pyproject, "[tool.pytest") || exists(dir, "pytest.ini", "conftest.py", "tests") {
		p.Test = prefix + "pytest"
	}
	return p, nil
}

func detectRust(dir string) (Package, error) {
	if !exists(dir, "Cargo.toml") {
		return Package{}, nil
	}
	return Package{Language: Rust, Manager: "cargo",
		Lint: "cargo clippy --all-targets -- -D warnings", Test: "cargo test", Build: "cargo build"}, nil
}

var makeTarget = regexp.MustCompile(`(?m)^(lint|typecheck|test|build):`)

// detectMake covers a repo whose only entry point is its Makefile.
func detectMake(dir string) (Package, error) {
	raw, err := readOptional(dir, "Makefile")
	if err != nil || raw == "" {
		return Package{}, err
	}
	p := Package{Language: Make, Manager: "make"}
	for _, m := range makeTarget.FindAllStringSubmatch(raw, -1) {
		switch m[1] {
		case "lint":
			p.Lint = "make lint"
		case "typecheck":
			p.Typecheck = "make typecheck"
		case "test":
			p.Test = "make test"
		case "build":
			p.Build = "make build"
		}
	}
	if p.Lint == "" && p.Typecheck == "" && p.Test == "" && p.Build == "" {
		return Package{}, nil
	}
	return p, nil
}

// HasChecks reports whether the stack has anything a pipeline could run.
func (s Stack) HasChecks() bool {
	for _, p := range s.Packages {
		if p.Lint != "" || p.Typecheck != "" || p.Test != "" || p.Build != "" {
			return true
		}
	}
	return false
}
