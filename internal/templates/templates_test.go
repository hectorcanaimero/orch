package templates

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hectorcanaimero/orch/internal/graph"
	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/router"
)

// The five canonical templates. Named rather than derived, so that a template
// silently disappearing from the embedded tree — a bad merge, a missing
// `all:` prefix on the embed directive — fails here instead of making List()
// quietly return four.
var canonical = []string{
	"chatbot-whatsapp",
	"data-pipeline",
	"expo-mobile",
	"nextjs-saas",
	"python-api",
}

func TestListReturnsTheCanonicalFive(t *testing.T) {
	got := Names()
	if len(got) != len(canonical) {
		t.Fatalf("List() = %v, want the five canonical templates %v", got, canonical)
	}
	for i := range canonical {
		if got[i] != canonical[i] {
			t.Errorf("template %d = %q, want %q (List is sorted)", i, got[i], canonical[i])
		}
	}
	for _, tmpl := range List() {
		if tmpl.Description == "" {
			t.Errorf("%s has no description", tmpl.Name)
		}
		if tmpl.Description == tmpl.Name {
			t.Errorf("%s falls back to its name — description.txt is missing or blank", tmpl.Name)
		}
	}
}

func TestProjectNotFoundNamesWhatExists(t *testing.T) {
	_, err := Project("next-js")
	if err == nil {
		t.Fatal("expected an error for an unknown template")
	}
	var notFound *NotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("got %T, want *NotFoundError", err)
	}
	// The list is the useful half: someone who typed `next-js` wants to see
	// `nextjs-saas`, not to be told they were wrong.
	msg := err.Error()
	for _, name := range canonical {
		if !strings.Contains(msg, name) {
			t.Errorf("error %q does not mention %q", msg, name)
		}
	}
}

// Every template's tasks.json has to survive the same loader `orch run` uses,
// and then pass the same validation.
//
// This is the test the brief asked for and the one bug 12 argues for: template
// data is where three of the migration's bugs have come from, and none of them
// were visible by reading the file.
func TestEveryTemplateValidates(t *testing.T) {
	for _, name := range canonical {
		t.Run(name, func(t *testing.T) {
			tasks := loadTemplateTasks(t, name)
			if len(tasks) == 0 {
				t.Fatal("template scaffolds no tasks")
			}

			// Routes exactly as `orch init` builds them: inferred from the
			// `backend/cli_model` key, which is what makes a scaffolded
			// project dispatchable without hand-editing the router.
			var keys []string
			for _, task := range tasks {
				keys = append(keys, task.Model)
			}
			problems := graph.Validate(tasks, keys)
			for _, p := range problems {
				id := "<no task>"
				if p.TaskID != nil {
					id = *p.TaskID
				}
				t.Errorf("%s: %s (%s): %s", id, p.Kind, p.Field, p.Message)
			}

			if cycles := graph.FindCycles(tasks); len(cycles) > 0 {
				t.Errorf("dependency cycles: %v", cycles)
			}
			if _, err := graph.TopoOrder(tasks); err != nil {
				t.Errorf("tasks cannot be ordered: %v", err)
			}
		})
	}
}

// Every model a template names must be inferable into a route.
//
// `orch init` populates the router by inference, so a model key that
// `router.InferEntry` cannot read leaves a task that cannot dispatch — the
// shape of bug 14, which is why this is asserted per model rather than left
// to the validation above.
func TestEveryTemplateModelIsInferable(t *testing.T) {
	for _, name := range canonical {
		t.Run(name, func(t *testing.T) {
			seen := map[string]bool{}
			for _, task := range loadTemplateTasks(t, name) {
				if seen[task.Model] {
					continue
				}
				seen[task.Model] = true
				entry, err := router.InferEntry(task.Model, model.TierStandard)
				if err != nil {
					t.Errorf("%s: cannot infer a route for %q — `orch init` would "+
						"leave it unrouted: %v", task.ID, task.Model, err)
					continue
				}
				if got := string(entry.Backend) + "/" + entry.CLIModel; got != task.Model {
					t.Errorf("%s: inferred %q from %q", task.ID, got, task.Model)
				}
			}
		})
	}
}

// specRef values are relative to spec_root, which already supplies `specs/`.
//
// Bug 12: every template carried the prefix, so every prompt sent its agent to
// `specs/specs/<file>` — one directory below where `orch init` puts them.
func TestNoTemplateRepeatsTheSpecRoot(t *testing.T) {
	for _, name := range canonical {
		t.Run(name, func(t *testing.T) {
			for _, task := range loadTemplateTasks(t, name) {
				if task.SpecRef == "" {
					t.Errorf("%s has no specRef", task.ID)
					continue
				}
				if strings.HasPrefix(task.SpecRef, "specs/") {
					t.Errorf("%s: specRef %q repeats the spec root — it is joined "+
						"onto spec_root, which is already `specs`", task.ID, task.SpecRef)
				}
				if !strings.Contains(task.SpecRef, "#") {
					t.Errorf("%s: specRef %q has no section anchor", task.ID, task.SpecRef)
				}
			}
		})
	}
}

// Every template ships the three files `orch init` reads from it. Only
// tasks.json.tmpl is required by the loader; the other two being optional is
// what would let one go missing without anyone noticing.
func TestEveryTemplateShipsItsFiles(t *testing.T) {
	for _, name := range canonical {
		t.Run(name, func(t *testing.T) {
			dir, err := Project(name)
			if err != nil {
				t.Fatalf("Project: %v", err)
			}
			for _, f := range []string{"tasks.json.tmpl", "config.yaml.tmpl", "AGENTS.md.tmpl", "description.txt"} {
				if _, err := fs.Stat(dir, f); err != nil {
					t.Errorf("missing %s", f)
				}
			}
		})
	}
}

func TestRenderSubstitutesBothPlaceholders(t *testing.T) {
	now := time.Date(2026, 9, 12, 15, 4, 5, 0, time.UTC)
	got := Render("project=PROJECT_NAME date=GENERATED_AT again=PROJECT_NAME", "demo", now)
	want := "project=demo date=2026-09-12 again=demo"
	if got != want {
		t.Errorf("Render() = %q, want %q", got, want)
	}

	// The clock is a parameter, so a golden can exist. A renderer that read
	// time.Now could not be compared against a fixed expectation at all.
	later := Render("GENERATED_AT", "demo", now.Add(48*time.Hour))
	if later != "2026-09-14" {
		t.Errorf("Render() = %q, want the date it was given", later)
	}

	// UTC, not local: two machines scaffolding the same project on the same
	// day must write the same file.
	tokyo, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Skipf("no tzdata: %v", err)
	}
	// 2026-09-12T23:00Z is already the 13th in Tokyo.
	late := time.Date(2026, 9, 12, 23, 0, 0, 0, time.UTC).In(tokyo)
	if got := Render("GENERATED_AT", "demo", late); got != "2026-09-12" {
		t.Errorf("Render() = %q in Asia/Tokyo, want the UTC date", got)
	}
}

// Every template renders to valid JSON after substitution — the placeholders
// sit inside string values, so a template that moved one outside the quotes
// would produce a file `orch init` writes and nothing can read.
func TestEveryTemplateRendersToValidJSON(t *testing.T) {
	for _, name := range canonical {
		t.Run(name, func(t *testing.T) {
			raw := readTemplateFile(t, name, "tasks.json.tmpl")
			rendered := Render(raw, "demo-project", time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC))
			if strings.Contains(rendered, "PROJECT_NAME") || strings.Contains(rendered, "GENERATED_AT") {
				t.Error("a placeholder survived rendering")
			}
			var out map[string]any
			if err := json.Unmarshal([]byte(rendered), &out); err != nil {
				t.Fatalf("rendered tasks.json is not valid JSON: %v", err)
			}
			meta, _ := out["meta"].(map[string]any)
			if meta["project"] != "demo-project" {
				t.Errorf("meta.project = %v, want the project name", meta["project"])
			}
			if meta["template"] != name {
				t.Errorf("meta.template = %v, want %q", meta["template"], name)
			}
		})
	}
}

// ---- the copy, held to the Python tree ------------------------------------

// goOnly are the files that exist here and not in the Python package.
//
// One entry, and it is the point of the list existing: expo-mobile is H-1e,
// the fifth canonical template, outstanding since before the migration and
// built only in Go because Python is frozen. Anything else appearing here is
// drift, not a decision.
var goOnly = map[string]bool{
	"projects/expo-mobile/AGENTS.md.tmpl":   true,
	"projects/expo-mobile/config.yaml.tmpl": true,
	"projects/expo-mobile/description.txt":  true,
	"projects/expo-mobile/tasks.json.tmpl":  true,
}

// TestGoTreeMatchesPython is the guard on having two copies of this data.
//
// `go:embed` cannot reach outside its package directory, so the tree lives
// here as well as under `orchestrator/templates/` until Python is deleted. Two
// copies is where a fix lands on one side only — and this data has produced
// bug 12 (specRef prefixes) and half of bug 14 already.
//
// It reads the Python tree directly, so it stops working the day that tree is
// removed. That is correct: at that point this becomes the only copy and the
// test has nothing left to check. Delete it then, do not weaken it now.
func TestGoTreeMatchesPython(t *testing.T) {
	pythonRoot := filepath.Join("..", "..", "orchestrator", "templates")
	if _, err := os.Stat(pythonRoot); errors.Is(err, fs.ErrNotExist) {
		t.Skip("the Python tree is gone; this test goes with it")
	}

	goFiles := map[string][]byte{}
	err := fs.WalkDir(Files(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, err := fs.ReadFile(Files(), path)
		if err != nil {
			return err
		}
		goFiles[path] = body
		return nil
	})
	if err != nil {
		t.Fatalf("walk the embedded tree: %v", err)
	}

	pyFiles := map[string][]byte{}
	err = filepath.WalkDir(pythonRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(pythonRoot, path)
		if err != nil {
			return err
		}
		// #nosec G304,G122 -- a fixed tree inside the repository, walked by a
		// test. The TOCTOU G122 warns about needs an attacker who can already
		// write to the working copy.
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		pyFiles[filepath.ToSlash(rel)] = body
		return nil
	})
	if err != nil {
		t.Fatalf("walk the Python tree: %v", err)
	}

	var missingHere, extraHere, differs []string
	for path := range pyFiles {
		if _, ok := goFiles[path]; !ok {
			missingHere = append(missingHere, path)
		}
	}
	for path := range goFiles {
		if _, ok := pyFiles[path]; !ok && !goOnly[path] {
			extraHere = append(extraHere, path)
		}
	}
	for path, want := range pyFiles {
		if got, ok := goFiles[path]; ok && string(got) != string(want) {
			differs = append(differs, path)
		}
	}
	sort.Strings(missingHere)
	sort.Strings(extraHere)
	sort.Strings(differs)

	if len(missingHere) > 0 {
		t.Errorf("in the Python tree and not here: %v\n"+
			"copy them across — a scaffold that differs between the two binaries "+
			"is the bug this test exists for", missingHere)
	}
	if len(extraHere) > 0 {
		t.Errorf("here and not in the Python tree: %v\n"+
			"add to goOnly with a reason, or remove", extraHere)
	}
	if len(differs) > 0 {
		t.Errorf("present in both and different: %v\n"+
			"a fix landed on one side only", differs)
	}

	// And the allowlist does not outlive what it allows.
	for path := range goOnly {
		if _, ok := goFiles[path]; !ok {
			t.Errorf("goOnly lists %q, which is not in the tree", path)
		}
	}
}

// ---- helpers ---------------------------------------------------------------

func readTemplateFile(t *testing.T, template, name string) string {
	t.Helper()
	dir, err := Project(template)
	if err != nil {
		t.Fatalf("Project(%q): %v", template, err)
	}
	raw, err := fs.ReadFile(dir, name)
	if err != nil {
		t.Fatalf("read %s/%s: %v", template, name, err)
	}
	return string(raw)
}

// loadTemplateTasks renders a template's tasks.json and parses it with the
// same loader `orch run` uses — not with a test-local struct, which would
// accept a shape the real loader rejects.
func loadTemplateTasks(t *testing.T, template string) []model.Task {
	t.Helper()
	rendered := Render(readTemplateFile(t, template, "tasks.json.tmpl"),
		"demo-project", time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC))

	path := filepath.Join(t.TempDir(), "tasks.json")
	if err := os.WriteFile(path, []byte(rendered), 0o600); err != nil {
		t.Fatalf("write rendered tasks.json: %v", err)
	}
	file, err := model.LoadTasksFile(path)
	if err != nil {
		t.Fatalf("%s: the rendered tasks.json does not load: %v", template, err)
	}
	return file.Tasks
}
