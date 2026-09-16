package scaffold

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hectorcanaimero/orch/internal/budget"
	"github.com/hectorcanaimero/orch/internal/config"
	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/router"
	"github.com/hectorcanaimero/orch/internal/templates"
	"gopkg.in/yaml.v3"
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

// #267: the config `orch init` writes documented the Python line's `findings:`
// block and its `orch findings publish` command, which the Go binary does not
// have — so the first `orch run` on a fresh project warned about the file init
// had just written. A scaffolded config must carry no key the loader reports
// as dropped.
func TestScaffoldedConfigHasNoDroppedFindingsKey(t *testing.T) {
	for _, name := range append(templates.Names(), "") {
		label := name
		if label == "" {
			label = "blank"
		}
		t.Run(label, func(t *testing.T) {
			res := scaffolded(t, name)
			path := filepath.Join(res.Root, ".orchestrator", "config.yaml")

			var raw map[string]any
			if err := yaml.Unmarshal([]byte(readFile(t, res, ".orchestrator/config.yaml")), &raw); err != nil {
				t.Fatalf("parse the scaffolded config: %v", err)
			}
			if _, ok := raw["findings"]; ok {
				t.Error("the scaffolded config has a `findings:` block; that feature was removed")
			}

			loaded, err := config.Load(path, res.Root)
			if err != nil {
				t.Fatalf("the scaffolded config does not load: %v", err)
			}
			for _, w := range loaded.Warnings {
				if strings.Contains(w, "`findings`") {
					t.Errorf("loading the scaffolded config warns about itself: %s", w)
				}
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
		".mcp.json",
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

// A new project gets its budget guardrail. `orch init` used to write no
// budgets.yaml, so the gate was off on every scaffolded project — silently,
// while the wizard asked which preset to use. The file is the packaged one,
// and the preset chosen in config.yaml loads from it where `orch run` looks.
func TestScaffoldWritesTheBudgetGuardrail(t *testing.T) {
	for _, template := range []string{"", "python-api"} {
		t.Run("template="+template, func(t *testing.T) {
			res, err := Run(Options{
				Root: filepath.Join(t.TempDir(), "p"), Template: template,
				BudgetPreset: "aggressive", Now: fixedNow,
			})
			if err != nil {
				t.Fatal(err)
			}
			configYAML := filepath.Join(res.Root, ".orchestrator", "config.yaml")
			loaded, err := config.Load(configYAML, res.Root)
			if err != nil {
				t.Fatal(err)
			}
			path := budget.ResolvePath(res.Root, configYAML, loaded.Config.BudgetsConfig)
			if path != filepath.Join(res.Root, ".orchestrator", "budgets.yaml") {
				t.Fatalf("budgets.yaml resolves to %q", path)
			}
			cfg, err := budget.LoadConfig(path, loaded.Config.BudgetsPreset)
			if err != nil || cfg == nil || len(cfg.Providers) == 0 {
				t.Fatalf("preset %q from %s: cfg=%v err=%v", loaded.Config.BudgetsPreset, path, cfg, err)
			}
			if cfg.Providers["claude"].ThresholdPct != 90 {
				t.Errorf("claude threshold = %v, want the aggressive preset's 90", cfg.Providers["claude"].ThresholdPct)
			}
			// The packaged config leaves per_dispatch_usd to its default, so a
			// new project does not send claude a --max-budget-usd it never chose.
			if b := loaded.Config.Budget; b.PerDispatchExplicit || b.PerDispatchUSD != 5.0 {
				t.Errorf("budget = %+v, want the implicit default 5.0", b)
			}
		})
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

// defaultsDivergedFromPython lists packaged defaults that are deliberately no
// longer identical to the last Python release's, each with the reason.
// Changing a default is allowed; doing it without saying so here is not,
// because a project scaffolded by the last Python release and one scaffolded
// by this binary must not differ by accident.
var defaultsDivergedFromPython = map[string]string{
	"config.yaml": "#267: the `findings:` block documented `orch findings publish`, which the Go binary " +
		"does not have, and made config.Load warn about the file init had just written; " +
		"it is replaced by `report_findings:`, the dogfooding opt-in that exists. " +
		"#249: the dispatch comment named .worktrees/<task-id>/ inside the project; " +
		"worktrees moved next to it, to ../<project>.worktrees/<task-id>/. " +
		"fix/budget-accounting: the budgets comment promised a --budgets-preset flag and an " +
		"ORCH_BUDGETS_PRESET variable `orch run` never read; it now says where budgets.yaml is looked " +
		"for and what typical_dispatch_tokens weighs; `budget.per_dispatch_usd` is commented out, because " +
		"claude now receives --max-budget-usd only when the key is written, and a new project should not " +
		"send a cap nobody chose (the value is still the 5.0 default, limiting escalation). " +
		"The tunnel block sat under `dashboard:`, where the Go loader ignores it, and chose autossh; " +
		"the tunnel is a Cloudflare quick tunnel and its block is a top-level `tunnel: enabled:`",
	"budgets.yaml": "fix/budget-accounting: the header's selection order named a CLI flag and an environment " +
		"variable nothing reads, and the calibration note now says that cache tokens count",
}

// The three packaged defaults started as copies of Python's, so they get the
// same guard `internal/templates` has: silent drift fails here.
//
// The comparison is against testdata/python-frozen/, pinned from the Python
// tree, not the tree itself. It used to read orchestrator/ and skip when that
// was absent, which would have deleted the guard together with Python.
func TestPackagedDefaultsMatchPython(t *testing.T) {
	for _, name := range []string{"config.yaml", "budgets.yaml", "model_router.yaml"} {
		t.Run(name, func(t *testing.T) {
			pythonPath := filepath.Join("testdata", "python-frozen", name)
			want, err := os.ReadFile(pythonPath) // #nosec G304 -- a fixed path in the repo
			if err != nil {
				t.Fatalf("read %s: %v", pythonPath, err)
			}
			got, err := packagedDefault(name)
			if err != nil {
				t.Fatalf("read the embedded %s: %v", name, err)
			}
			reason, diverged := defaultsDivergedFromPython[name]
			switch {
			case !diverged && string(got) != string(want):
				t.Errorf("the embedded %s has drifted from the last Python release's copy (%s); "+
					"if that is intended, add it to defaultsDivergedFromPython with the reason", name, pythonPath)
			case diverged && string(got) == string(want):
				t.Errorf("defaultsDivergedFromPython lists %s (%q) but it is identical again; remove the entry",
					name, reason)
			}
		})
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

// ---- the wizard's answers have to land somewhere ------------------------------

// Three of the wizard's questions are not decided by the template, and their
// answers are written into config.yaml after scaffolding.
//
// This is the assertion the wizard tests did not make. They checked the
// summary displayed each answer; nothing checked the answer took effect, and
// for a while it did not — Wizard collected `state backend`, `budget preset`
// and `spec root` into locals, showed them, and returned Options that had
// never heard of them.
func TestWizardChoicesAreWrittenIntoTheConfig(t *testing.T) {
	res, err := Run(Options{
		Root:         filepath.Join(t.TempDir(), "p"),
		Template:     "python-api",
		StateBackend: "file",
		BudgetPreset: "aggressive",
		SpecRoot:     "docs/specs",
		Now:          fixedNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	body := readFile(t, res, ".orchestrator/config.yaml")

	for _, want := range []string{"backend: file", "budgets_preset: aggressive", "spec_root: docs/specs"} {
		if !strings.Contains(body, want) {
			t.Errorf("the config does not contain %q:\n%s", want, body)
		}
	}
	// The template's own values are gone, not merely accompanied.
	for _, gone := range []string{"backend: sqlite", "spec_root: specs\n"} {
		if strings.Contains(body, gone) {
			t.Errorf("the config still contains %q", gone)
		}
	}
	// And the comments survived — a YAML round-trip would have eaten them,
	// and they are the guidance an operator reads when they open the file.
	if !strings.Contains(body, "#") {
		t.Error("the comments were lost")
	}
	// `backend:` is nested under `state:`; the replacement must keep its
	// indent or the key moves to the top level and means something else.
	if !strings.Contains(body, "\n  backend: file") {
		t.Error("backend lost its indentation and is no longer under state:")
	}
}

// No answers, no edits. Batch mode passes none of the three, and a flag
// nobody set must not overwrite what the template chose.
func TestNoChoicesLeavesTheConfigAlone(t *testing.T) {
	root := filepath.Join(t.TempDir(), "p")
	withChoices, err := Run(Options{Root: root, Template: "python-api", Now: fixedNow})
	if err != nil {
		t.Fatal(err)
	}
	body := readFile(t, withChoices, ".orchestrator/config.yaml")
	if !strings.Contains(body, "spec_root: specs") {
		t.Error("the template's spec_root was not preserved")
	}
	if !strings.Contains(body, "backend: sqlite") {
		t.Error("the template's state backend was not preserved")
	}
}

// The tier picks are stamped into tasks.json's meta block.
//
// Informational — nothing dispatches on them — but they are the only record
// of which models the project was set up around.
func TestTierDefaultsAreStampedIntoTaskMeta(t *testing.T) {
	res, err := Run(Options{
		Root: filepath.Join(t.TempDir(), "p"), Template: "python-api", Now: fixedNow,
		TierDefaults: map[string]string{
			"premium":  "claude/claude-opus-4-7",
			"standard": "claude/claude-sonnet-4-6",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var doc struct {
		Meta map[string]any `json:"meta"`
	}
	if err := json.Unmarshal([]byte(readFile(t, res, "tasks.json")), &doc); err != nil {
		t.Fatalf("the stamped tasks.json does not parse: %v", err)
	}
	if doc.Meta["default_premium_model"] != "claude/claude-opus-4-7" {
		t.Errorf("default_premium_model = %v", doc.Meta["default_premium_model"])
	}
	if doc.Meta["default_standard_model"] != "claude/claude-sonnet-4-6" {
		t.Errorf("default_standard_model = %v", doc.Meta["default_standard_model"])
	}
	// A tier nobody picked gets no key, rather than an empty one.
	if _, ok := doc.Meta["default_cheap_model"]; ok {
		t.Error("a tier with no pick was stamped anyway")
	}
	// The template's own meta survives the rewrite.
	if doc.Meta["template"] != "python-api" {
		t.Errorf("meta.template = %v, want python-api", doc.Meta["template"])
	}
	// And the file still loads — a stamp that broke the schema would be
	// worse than no stamp.
	if _, err := model.LoadTasksFile(filepath.Join(res.Root, "tasks.json")); err != nil {
		t.Errorf("the stamped tasks.json no longer loads: %v", err)
	}
}

// Bug 17: a key the template omits is appended, not dropped.
//
// None of the four shipped templates contains `budgets_preset`. Python's
// regex has no fallback, so the wizard asks for a budget preset, shows it in
// the confirm summary, takes the operator's "yes", and discards it — the
// project loads with the packaged default instead. A confirm gate that
// displays a choice with no effect is worse than not asking, because the
// operator has been told it took.
func TestAMissingKeyIsAppendedNotDropped(t *testing.T) {
	res, err := Run(Options{
		Root: filepath.Join(t.TempDir(), "p"), Template: "python-api",
		BudgetPreset: "aggressive", Now: fixedNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	body := readFile(t, res, ".orchestrator/config.yaml")
	if !strings.Contains(body, "budgets_preset: aggressive") {
		t.Errorf("the chosen preset was dropped:\n%s", body)
	}
	if !strings.Contains(body, "Added by `orch init`") {
		t.Error("the appended key does not say where it came from")
	}
	// And the file is still the YAML it was.
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("appending broke the config: %v", err)
	}
	if doc["budgets_preset"] != "aggressive" {
		t.Errorf("budgets_preset parses as %v", doc["budgets_preset"])
	}
	if doc["spec_root"] != "specs" {
		t.Errorf("the template's spec_root was disturbed: %v", doc["spec_root"])
	}
}

// A nested key is never appended. A bare `backend:` at the end of the file
// would be a top-level setting with a different meaning, so a config with no
// `state:` block keeps the default rather than gaining a wrong key.
func TestANestedKeyIsNotAppended(t *testing.T) {
	body := replaceOrAppend("concurrency:\n  global_max: 4\n", reStateBackend, "", "file", false)
	if strings.Contains(body, "backend") {
		t.Errorf("a nested key was appended:\n%s", body)
	}
}

// `.mcp.json` is what points an MCP-capable agent at `orch mcp` (G6.4). Two
// properties, and the second is the one that matters: it names the server the
// binary actually serves, and it never replaces a file that is already there.
func TestMCPConfigNamesTheServer(t *testing.T) {
	res := scaffolded(t, "python-api")

	var cfg struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(readFile(t, res, ".mcp.json")), &cfg); err != nil {
		t.Fatalf("parse .mcp.json: %v", err)
	}
	server, ok := cfg.MCPServers["orch"]
	if !ok {
		t.Fatalf("no `orch` server in .mcp.json: %+v", cfg.MCPServers)
	}
	if server.Command != "orch" {
		t.Errorf("command = %q; want orch", server.Command)
	}
	if len(server.Args) != 1 || server.Args[0] != "mcp" {
		t.Errorf("args = %v; want [mcp]", server.Args)
	}
}

// Soft even under --force. A project's `.mcp.json` is shared with every other
// MCP server it uses; overwriting it would silently disconnect them.
func TestMCPConfigIsNeverOverwritten(t *testing.T) {
	root := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	const theirs = `{"mcpServers":{"something-else":{"command":"other"}}}`
	if err := os.WriteFile(filepath.Join(root, ".mcp.json"), []byte(theirs), 0o600); err != nil {
		t.Fatalf("seed .mcp.json: %v", err)
	}

	for _, force := range []bool{false, true} {
		if _, err := Run(Options{Root: root, Template: "python-api", Force: force, Now: fixedNow}); err != nil {
			t.Fatalf("Run(force=%v): %v", force, err)
		}
		got, err := os.ReadFile(filepath.Join(root, ".mcp.json")) // #nosec G304 -- a temp dir this test created
		if err != nil {
			t.Fatalf("read .mcp.json back: %v", err)
		}
		if string(got) != theirs {
			t.Fatalf("force=%v replaced the project's own .mcp.json:\n%s", force, got)
		}
	}
}

// #233: `orch init` wrote a Python workflow (`setup-python`, `pip install`,
// `pytest`) into every repo, so a Node or Go project's CI failed, or never
// ran the tests at all. The workflow and `github.test_command` follow what
// the repo is made of.
func TestWorkflowFollowsTheRepoStack(t *testing.T) {
	cases := []struct {
		name  string
		files []string
		want  []string
		test  string
	}{
		{"pnpm", []string{"package.json", "pnpm-lock.yaml"}, []string{"actions/setup-node", "pnpm install --frozen-lockfile"}, "pnpm test"},
		{"npm", []string{"package.json"}, []string{"actions/setup-node", "npm install"}, "npm test"},
		{"yarn", []string{"package.json", "yarn.lock"}, []string{"actions/setup-node", "yarn install --frozen-lockfile"}, "yarn test"},
		{"go", []string{"go.mod"}, []string{"actions/setup-go", "go-version-file: go.mod"}, "go test ./..."},
		{"python", []string{"pyproject.toml"}, []string{"actions/setup-python", "pip install -e .[dev]"}, "pytest"},
		{"empty", nil, []string{"actions/setup-python"}, "pytest"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "proj")
			if err := os.MkdirAll(root, 0o750); err != nil {
				t.Fatal(err)
			}
			for _, f := range tc.files {
				if err := os.WriteFile(filepath.Join(root, f), []byte("{}\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			res, err := Run(Options{Root: root, Now: fixedNow})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}

			wf := readFile(t, res, ".github/workflows/orch-ci.yml")
			for _, want := range append(tc.want, "run: "+tc.test) {
				if !strings.Contains(wf, want) {
					t.Errorf("workflow is missing %q:\n%s", want, wf)
				}
			}
			if tc.name != "python" && tc.name != "empty" && strings.Contains(wf, "python") {
				t.Errorf("a %s repo got a Python workflow:\n%s", tc.name, wf)
			}
			if cfg := readFile(t, res, ".orchestrator/config.yaml"); !strings.Contains(cfg, "test_command: "+tc.test) {
				t.Errorf("config.yaml does not say test_command: %s", tc.test)
			}
		})
	}
}

// An existing repository gets the pipeline its files call for, not the
// template's single test job: here a typecheck and a build it really has.
func TestWorkflowIsDetectedForAnExistingRepo(t *testing.T) {
	root := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"package.json":   `{"scripts":{"type-check":"tsc --noEmit","test":"vitest run","build":"next build"}}`,
		"pnpm-lock.yaml": "lockfileVersion: '9.0'\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	res, err := Run(Options{Root: root, Now: fixedNow})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	wf := readFile(t, res, ".github/workflows/orch-ci.yml")
	for _, want := range []string{"Generated by `orch ci setup`", "run: pnpm run type-check", "run: pnpm run test", "run: pnpm run build", "cache: pnpm"} {
		if !strings.Contains(wf, want) {
			t.Errorf("workflow is missing %q:\n%s", want, wf)
		}
	}
}

// A template already names its test command; the workflow has to run that
// command with the toolchain it needs, not pytest.
func TestWorkflowFollowsTheTemplateTestCommand(t *testing.T) {
	res := scaffolded(t, "nextjs-saas")
	wf := readFile(t, res, ".github/workflows/orch-ci.yml")
	for _, want := range []string{"actions/setup-node", "run: pnpm test"} {
		if !strings.Contains(wf, want) {
			t.Errorf("workflow is missing %q:\n%s", want, wf)
		}
	}
	if strings.Contains(wf, "python") {
		t.Errorf("the nextjs-saas template got a Python workflow:\n%s", wf)
	}
}

// #234: every AGENTS.md told an agent with no orch on PATH to run `pipx
// install orch` — the archived Python package, which is not this binary.
func TestAgentsMDInstallsTheGoBinary(t *testing.T) {
	for _, tmpl := range append([]string{""}, templates.Names()...) {
		t.Run("template="+tmpl, func(t *testing.T) {
			got := readFile(t, scaffolded(t, tmpl), "AGENTS.md")
			if strings.Contains(got, "pipx") {
				t.Errorf("AGENTS.md still installs the Python package:\n%s", got)
			}
			if !strings.Contains(got, "scripts/install.sh") {
				t.Errorf("AGENTS.md does not say how to install orch:\n%s", got)
			}
		})
	}
}
