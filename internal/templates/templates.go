// Package templates is the file tree `orch init` scaffolds a project from,
// embedded in the binary.
//
// Ported from the `templates/` directory of the Python package, plus one
// template Python never had (see expo-mobile below).
//
// # Why the files are copied rather than shared
//
// `go:embed` cannot reach outside its own package directory, so the tree lives
// here as well as under `orchestrator/templates/` until the Python tree is
// deleted. Two copies of the same data is exactly where a fix lands on one
// side only — and this data has produced two bugs already: the snake_case task
// keys, and `specRef` values carrying a `specs/` prefix that `spec_root`
// already supplies (bug 12). So `TestGoTreeMatchesPython` diffs the two file
// by file and fails on any drift. It reads `../../orchestrator/templates`
// directly; when Python goes, that test goes with it and this becomes the only
// copy.
//
// # The fifth template
//
// `expo-mobile` is new here and has no Python counterpart. It is H-1e, the
// last of the five canonical templates, outstanding since before the migration
// began — the one place in this port where "no new features in Python" and
// "finish what was started" point in different directions, resolved by
// building it only in Go.
package templates

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"
)

//go:embed all:files
var embedded embed.FS

// Files is the whole tree, rooted at what Python calls `templates/`.
//
// Callers that want one project's directory should use Project; this is for
// the shared files — `gitignore.tmpl`, `scripts/task-*.sh`, the CI workflow,
// `specs/README.md` — that every scaffold gets whatever template it used.
func Files() fs.FS {
	sub, err := fs.Sub(embedded, "files")
	if err != nil {
		// Unreachable: the path is a constant and the tree is embedded at
		// build time. Panicking beats returning an error nobody can act on.
		panic("templates: embedded tree is missing its root: " + err.Error())
	}
	return sub
}

// A Template is one shipped project scaffold.
type Template struct {
	Name string
	// Description is the first non-empty line of the template's
	// description.txt, or the name when there is none.
	Description string
}

// NotFoundError names what was asked for and what exists.
//
// The list matters more than the error: a user who typed `next-js` wants to
// see `nextjs-saas`, not to be told they were wrong.
type NotFoundError struct {
	Name      string
	Available []string
}

func (e *NotFoundError) Error() string {
	available := "<none>"
	if len(e.Available) > 0 {
		available = strings.Join(e.Available, ", ")
	}
	return fmt.Sprintf("unknown template %q. Available: %s", e.Name, available)
}

// requiredFile is what makes a directory a template.
//
// Python's rule, kept: every other file in a template is optional and only
// overlaid when present, so a template that ships only tasks.json.tmpl is
// legal and scaffolds a project using the shared defaults for everything else.
const requiredFile = "tasks.json.tmpl"

// List returns every shipped template, sorted by name.
func List() []Template {
	entries, err := fs.ReadDir(Files(), "projects")
	if err != nil {
		return nil
	}

	out := make([]Template, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := fs.Stat(Files(), "projects/"+e.Name()+"/"+requiredFile); err != nil {
			continue
		}
		out = append(out, Template{Name: e.Name(), Description: describe(e.Name())})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Names is List's names alone, for an error message or a `--template` help
// string.
func Names() []string {
	list := List()
	out := make([]string, len(list))
	for i, t := range list {
		out[i] = t.Name
	}
	return out
}

func describe(name string) string {
	raw, err := fs.ReadFile(Files(), "projects/"+name+"/description.txt")
	if err != nil {
		return name
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return name
}

// Project returns one template's directory as a filesystem.
//
// A NotFoundError carries the available names, so the caller does not have to
// call List separately to write a useful message.
func Project(name string) (fs.FS, error) {
	if _, err := fs.Stat(Files(), "projects/"+name+"/"+requiredFile); err != nil {
		return nil, &NotFoundError{Name: name, Available: Names()}
	}
	sub, err := fs.Sub(Files(), "projects/"+name)
	if err != nil {
		return nil, fmt.Errorf("open template %q: %w", name, err)
	}
	return sub, nil
}

// Render substitutes the two placeholders a template file can carry.
//
// `PROJECT_NAME` and `GENERATED_AT` are bare words rather than `{{...}}`,
// which is Python's choice and is kept because the same files are read by both
// binaries during the migration. It has a consequence worth knowing: the
// substitution is textual and unanchored, so a template containing the words
// in prose would have them replaced too. None do, and the sync test would
// notice a new one appearing on the Python side.
//
// `now` is a parameter rather than a call to time.Now so a golden can exist at
// all — GENERATED_AT is a date, and a renderer that reads the clock cannot be
// compared against a fixed expectation.
func Render(body, projectName string, now time.Time) string {
	body = strings.ReplaceAll(body, "PROJECT_NAME", projectName)
	return strings.ReplaceAll(body, "GENERATED_AT", now.UTC().Format("2006-01-02"))
}
