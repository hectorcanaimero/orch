package scaffold

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/router"
	"github.com/hectorcanaimero/orch/internal/templates"
)

var fixedNow = time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)

func scaffolded(t *testing.T, template string) Result {
	t.Helper()
	res, err := Run(Options{
		Root: filepath.Join(t.TempDir(), "proj"), Template: template, Now: fixedNow,
	})
	if err != nil {
		t.Fatalf("Run(%q): %v", template, err)
	}
	return res
}

func readFile(t *testing.T, res Result, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(res.Root, filepath.FromSlash(rel))) // #nosec G304 -- a temp dir this test created
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(raw)
}

// The test bug 14 was missing.
//
// Every init test asserted on files the scaffolder writes; none checked that
// the project it wrote is one orch can run. That is not a property of any
// single file — it is a property of tasks.json, the router and config.yaml
// agreeing with each other.
func TestScaffoldedProjectIsRunnable(t *testing.T) {
	for _, name := range append(templates.Names(), "") {
		label := name
		if label == "" {
			label = "blank"
		}
		t.Run(label, func(t *testing.T) {
			res := scaffolded(t, name)

			tasksFile, err := model.LoadTasksFile(filepath.Join(res.Root, "tasks.json"))
			if err != nil {
				t.Fatalf("the scaffolded tasks.json does not load: %v", err)
			}
			routes, err := router.Load(filepath.Join(res.Root, ".orchestrator", "model_router.yaml"))
			if err != nil {
				t.Fatalf("the scaffolded router does not load: %v", err)
			}

			// The assertion bug 14 needed: every task has a route.
			var unrouted []string
			for _, task := range tasksFile.Tasks {
				if _, ok := routes[task.Model]; !ok {
					unrouted = append(unrouted, task.ID+" → "+task.Model)
				}
			}
			if len(unrouted) > 0 {
				t.Errorf("tasks with no route after scaffolding: %v", unrouted)
			}

			// A blank project has no tasks, so its empty router is correct —
			// and stays empty rather than being filled with guesses.
			if name == "" {
				if len(tasksFile.Tasks) != 0 {
					t.Errorf("a blank project scaffolded %d tasks", len(tasksFile.Tasks))
				}
				if len(routes) != 0 {
					t.Errorf("a blank project got %d routes", len(routes))
				}
				if len(res.RoutesAdded) != 0 {
					t.Errorf("RoutesAdded = %v for a blank project", res.RoutesAdded)
				}
			} else if len(res.RoutesAdded) == 0 {
				t.Error("a templated project reported no routes added")
			}
			if len(res.RoutesUninferable) != 0 {
				t.Errorf("models no route could be inferred for: %v", res.RoutesUninferable)
			}
			if len(res.Warnings) != 0 {
				t.Errorf("warnings: %v", res.Warnings)
			}
		})
	}
}

// The router stub must stay appendable. It ended with `{}` until #141, which
// is a complete YAML document — so `orch router add-missing` wrote a file
// nothing could read, breaking the command the stub itself recommends.
func TestRouterStubStaysAppendable(t *testing.T) {
	res := scaffolded(t, "")
	path := filepath.Join(res.Root, ".orchestrator", "model_router.yaml")

	body := readFile(t, res, ".orchestrator/model_router.yaml")
	if strings.Contains(body, "{}") {
		t.Error("the stub closes its own YAML document")
	}
	if !strings.Contains(body, "orch router add-missing") {
		t.Error("the stub does not name the command that extends it")
	}

	added, skipped, err := router.AddMissing(path, []string{"claude/sonnet"}, model.TierStandard)
	if err != nil {
		t.Fatalf("AddMissing on a fresh stub: %v", err)
	}
	if len(added) != 1 || len(skipped) != 0 {
		t.Fatalf("added=%v skipped=%v", added, skipped)
	}
	back, err := router.Load(path)
	if err != nil {
		t.Fatalf("the extended stub does not load: %v", err)
	}
	if _, ok := back["claude/sonnet"]; !ok {
		t.Error("AddMissing reported success and the route is not readable")
	}
}

func TestScaffoldWritesTheLayout(t *testing.T) {
	res := scaffolded(t, "python-api")

	for _, rel := range []string{
		"tasks.json",
		"AGENTS.md",
		".gitignore",
		"specs/README.md",
		"scripts/task-start.sh",
		"scripts/task-finish.sh",
		"scripts/task-block.sh",
		"scripts/task-reset.sh",
		".orchestrator/config.yaml",
		".orchestrator/model_router.yaml",
		".orchestrator/state/.gitkeep",
		".github/workflows/orch-ci.yml",
	} {
		if _, err := os.Stat(filepath.Join(res.Root, filepath.FromSlash(rel))); err != nil {
			t.Errorf("missing %s", rel)
		}
	}
	// openspec/ only with --sdd.
	if _, err := os.Stat(filepath.Join(res.Root, "openspec")); err == nil {
		t.Error("openspec/ exists without --sdd")
	}
}

// The task scripts have to be executable. An agent invokes them, and a script
// without +x fails at dispatch time with a message about permissions rather
// than about orch — a report that sends the reader to the wrong place.
func TestTaskScriptsAreExecutable(t *testing.T) {
	res := scaffolded(t, "")
	entries, err := os.ReadDir(filepath.Join(res.Root, "scripts"))
	if err != nil {
		t.Fatalf("read scripts/: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no scripts were written")
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&0o100 == 0 {
			t.Errorf("%s is not executable (mode %v)", e.Name(), info.Mode())
		}
	}
}

func TestPlaceholdersAreSubstituted(t *testing.T) {
	root := filepath.Join(t.TempDir(), "anything")
	res, err := Run(Options{Root: root, Name: "my-project", Template: "python-api", Now: fixedNow})
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"tasks.json", "AGENTS.md", ".orchestrator/config.yaml"} {
		body := readFile(t, res, rel)
		if strings.Contains(body, "PROJECT_NAME") || strings.Contains(body, "GENERATED_AT") {
			t.Errorf("%s still carries a placeholder", rel)
		}
	}
	if !strings.Contains(readFile(t, res, "tasks.json"), "my-project") {
		t.Error("tasks.json does not carry the project name")
	}
	if !strings.Contains(readFile(t, res, "tasks.json"), "2026-09-12") {
		t.Error("tasks.json does not carry the generated date")
	}
}

// The name defaults to the directory's, not to the template's.
func TestNameDefaultsToTheDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "billing-api")
	res, err := Run(Options{Root: root, Template: "python-api", Now: fixedNow})
	if err != nil {
		t.Fatal(err)
	}
	if res.Name != "billing-api" {
		t.Errorf("Name = %q, want billing-api", res.Name)
	}
	if !strings.Contains(readFile(t, res, "tasks.json"), "billing-api") {
		t.Error("tasks.json does not carry the directory name")
	}
}

// ---- the conflict gate -------------------------------------------------------

// Checked before ANY write, so a refused scaffold leaves the directory exactly
// as it found it. A half-scaffolded project is worse than none: the files that
// did land make it look finished.
func TestConflictGateRefusesBeforeWritingAnything(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "tasks.json"), []byte(`{"mine":true}`), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Run(Options{Root: root, Template: "python-api", Now: fixedNow})
	if err == nil {
		t.Fatal("expected a conflict error")
	}
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("got %T, want *ConflictError", err)
	}
	if len(conflict.Paths) != 1 || conflict.Paths[0] != "tasks.json" {
		t.Errorf("Paths = %v, want [tasks.json]", conflict.Paths)
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Error("the error does not name the flag that overrides it")
	}

	// Nothing else was written, and the existing file is untouched.
	// #nosec G304 -- a temp dir this test created
	if body, err := os.ReadFile(filepath.Join(root, "tasks.json")); err != nil || string(body) != `{"mine":true}` {
		t.Errorf("tasks.json = %q (err %v), want it untouched", body, err)
	}
	for _, rel := range []string{"scripts", ".orchestrator", "AGENTS.md", "specs"} {
		if _, err := os.Stat(filepath.Join(root, rel)); err == nil {
			t.Errorf("%s was written despite the conflict", rel)
		}
	}
}

func TestForceOverwritesTheGuardedFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "tasks.json"), []byte(`{"mine":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Run(Options{Root: root, Template: "python-api", Force: true, Now: fixedNow})
	if err != nil {
		t.Fatalf("Run with Force: %v", err)
	}
	if strings.Contains(readFile(t, res, "tasks.json"), "mine") {
		t.Error("--force did not replace tasks.json")
	}
}

// `.gitignore` is never overwritten, not even with --force: it is a soft
// target that projects hand-edit, and it is not a marker of "there is a
// project here" either.
func TestGitignoreIsNeverOverwritten(t *testing.T) {
	root := t.TempDir()
	const mine = "# mine\nnode_modules/\n"
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(mine), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Run(Options{Root: root, Force: true, Now: fixedNow})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := readFile(t, res, ".gitignore"); got != mine {
		t.Errorf(".gitignore = %q, want it untouched", got)
	}
}

// AGENTS.md is committed and hand-edited; a re-init must not eat it.
func TestAgentsMDIsSoftUnlessForced(t *testing.T) {
	root := t.TempDir()
	const mine = "# my own context\n"
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(mine), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Run(Options{Root: root, Template: "python-api", Now: fixedNow})
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, res, "AGENTS.md"); got != mine {
		t.Error("a re-init overwrote a hand-edited AGENTS.md")
	}

	res2, err := Run(Options{Root: root, Template: "python-api", Force: true, Now: fixedNow})
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, res2, "AGENTS.md"); got == mine {
		t.Error("--force did not replace AGENTS.md")
	}
}

// ---- AGENTS.md content -------------------------------------------------------

// A template's own AGENTS.md wins; a blank project gets the generic one. The
// difference matters: the template's carries the stack, and an agent reading
// the generic one in an Expo project would be told nothing useful.
func TestAgentsMDComesFromTheTemplateWhenThereIsOne(t *testing.T) {
	res := scaffolded(t, "python-api")
	body := readFile(t, res, "AGENTS.md")
	if !strings.Contains(body, "FastAPI") {
		t.Error("the python-api AGENTS.md does not mention the stack")
	}

	blank := scaffolded(t, "")
	generic := readFile(t, blank, "AGENTS.md")
	if !strings.Contains(generic, "# Orch Project Context") {
		t.Error("a blank project did not get the generic AGENTS.md")
	}
	// Both say the thing that costs real work when missed.
	for _, body := range []string{body, generic} {
		if !strings.Contains(body, "Never edit `tasks.json` status directly") {
			t.Error("AGENTS.md omits the rule about runtime status")
		}
	}
}

func TestGenericAgentsMDNamesTheProjectsOwnDatabase(t *testing.T) {
	body := genericAgentsMD("billing-api")
	want := ".orchestrator/state/billing-api/orch.db"
	if strings.Count(body, want) < 2 {
		t.Errorf("the db path %q should appear in both the prose and the sqlite3 command", want)
	}
	if strings.Contains(body, "PROJECT_NAME") {
		t.Error("the generic AGENTS.md carries a placeholder")
	}
}

// ---- templates ---------------------------------------------------------------

func TestUnknownTemplateNamesWhatExists(t *testing.T) {
	_, err := Run(Options{Root: filepath.Join(t.TempDir(), "p"), Template: "next-js", Now: fixedNow})
	if err == nil {
		t.Fatal("expected an error")
	}
	var notFound *templates.NotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("got %T, want *templates.NotFoundError", err)
	}
	if !strings.Contains(err.Error(), "nextjs-saas") {
		t.Errorf("the error does not suggest what exists: %v", err)
	}
	// And nothing was created — the template is resolved before any write.
	if entries, err := os.ReadDir(filepath.Dir(filepath.Join(t.TempDir(), "p"))); err == nil && len(entries) > 0 {
		t.Errorf("a failed scaffold left %d entries behind", len(entries))
	}
}

func TestSDDAddsTheOpenspecLayout(t *testing.T) {
	res, err := Run(Options{Root: filepath.Join(t.TempDir(), "p"), SDD: true, Now: fixedNow})
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"openspec/README.md", "openspec/changes", "openspec/specs"} {
		if _, err := os.Stat(filepath.Join(res.Root, filepath.FromSlash(rel))); err != nil {
			t.Errorf("missing %s", rel)
		}
	}
}

// The packaged config default is a copy of `orchestrator/config.yaml`, so it
// gets the same guard `internal/templates` has: drift fails here.
//
// Same rule as that one — it reads the Python file by relative path, so it
// stops working the day the Python tree is deleted. Delete the test then; do
// not weaken it now.
func TestDefaultConfigMatchesPython(t *testing.T) {
	pythonPath := filepath.Join("..", "..", "orchestrator", "config.yaml")
	want, err := os.ReadFile(pythonPath) // #nosec G304 -- a fixed path in the repo
	if errors.Is(err, os.ErrNotExist) {
		t.Skip("the Python tree is gone; this test goes with it")
	}
	if err != nil {
		t.Fatalf("read %s: %v", pythonPath, err)
	}
	got, err := defaultConfig.ReadFile("defaults/config.yaml")
	if err != nil {
		t.Fatalf("read the embedded default: %v", err)
	}
	if string(got) != string(want) {
		t.Error("the embedded config.yaml has drifted from orchestrator/config.yaml")
	}
}

// A blank project's config is the packaged default, comments and all. The
// comments are most of its value — this is the file an operator edits first,
// and a marshalled struct would write the same keys with none of the
// explanations.
func TestBlankProjectGetsTheAnnotatedDefaultConfig(t *testing.T) {
	body := readFile(t, scaffolded(t, ""), ".orchestrator/config.yaml")
	if !strings.Contains(body, "#") {
		t.Error("the default config has no comments — it was marshalled, not copied")
	}
	for _, key := range []string{"concurrency:", "spec_root:", "state:", "budgets_preset:"} {
		if !strings.Contains(body, key) {
			t.Errorf("the default config is missing %s", key)
		}
	}
}

// A template's config replaces the default rather than merging with it. Keys
// the template omits are still covered, because `config.Load` applies defaults
// at load time — which is why a template can ship four blocks instead of
// twenty.
func TestTemplateConfigReplacesTheDefault(t *testing.T) {
	body := readFile(t, scaffolded(t, "python-api"), ".orchestrator/config.yaml")
	if !strings.Contains(body, "python-api") {
		t.Error("the python-api config does not identify its template")
	}
	if !strings.Contains(body, "spec_root: specs") {
		t.Error("the template config does not pin spec_root")
	}
}

// A write that fails stops the scaffold and says which file.
//
// The writer collects the first error and skips everything after it, so this
// also checks the skipping works: a scaffold that kept going after a failure
// would report the LAST error, which is rarely the one that matters.
func TestScaffoldReportsTheFirstFailure(t *testing.T) {
	// A file standing where the project root has to be. This fails for root
	// too, unlike a read-only parent — CI runs as root.
	parent := t.TempDir()
	blocked := filepath.Join(parent, "proj")
	if err := os.WriteFile(blocked, []byte("I am a file"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Run(Options{Root: blocked, Template: "python-api", Now: fixedNow})
	if err == nil {
		t.Fatal("expected an error scaffolding onto a file")
	}
	if !strings.Contains(err.Error(), blocked) {
		t.Errorf("error %q does not name the path", err)
	}
}

// The clock is a parameter. Two machines scaffolding the same project on the
// same UTC day must write the same bytes — and a test cannot have a fixed
// expectation against a renderer that reads the wall clock.
func TestGeneratedAtIsTheClockItWasGiven(t *testing.T) {
	res, err := Run(Options{
		Root: filepath.Join(t.TempDir(), "p"),
		Now:  time.Date(2026, 12, 25, 23, 30, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readFile(t, res, "tasks.json"), "2026-12-25") {
		t.Error("tasks.json does not carry the date it was given")
	}
}
