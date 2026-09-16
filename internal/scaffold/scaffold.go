// Package scaffold creates a new orch project on disk — what `orch init`
// does once the flags or the wizard have decided what to build.
//
// Ported from `orch_init` in `orchestrator/init_cmd.py`.
//
// The package is named for the work rather than for the command, like
// `budget`, `prompt` and `router`. `internal/init` was the name in the brief;
// a Go package called `init` compiles but cannot be referenced without an
// import alias at every call site, which is a papercut charged to everyone who
// ever imports it.
//
// # What "scaffolded correctly" means
//
// Not "the files exist". Bug 14 was a scaffolder whose output could not run:
// every file it promised was on disk, and the first command its own banner
// suggested exited 1, because the router it wrote had no routes for the tasks
// it wrote. The test that catches that runs the project; the tests that assert
// on files did not. So Result carries what was produced, and the caller can
// check the project rather than the file list.
package scaffold

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/hectorcanaimero/orch/internal/ci"
	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/router"
	"github.com/hectorcanaimero/orch/internal/templates"
)

// Options are the decisions a scaffold needs. The zero value scaffolds a blank
// project into the current directory under its own name.
type Options struct {
	// Root is where the project goes. Created if absent.
	Root string
	// Name fills PROJECT_NAME. Empty means the base name of Root.
	Name string
	// Template is one of templates.Names(), or empty for a blank project.
	Template string
	// Force overwrites the files the conflict check guards. It does not
	// overwrite `.gitignore`, which is a soft target either way.
	Force bool
	// SDD adds the openspec/ layout.
	SDD bool
	// Now stamps GENERATED_AT. Zero means time.Now — a parameter so a test
	// can have a fixed expectation.
	Now time.Time

	// The three answers the wizard collects that are not decided by the
	// template. Empty means "leave the config as the template or the
	// packaged default wrote it", which is what batch mode wants: a flag
	// nobody passed must not overwrite a value the template chose.
	StateBackend string
	BudgetPreset string
	SpecRoot     string
	// TierDefaults is the wizard's per-tier model pick, stamped into
	// tasks.json's meta block for downstream tools. Keys: premium, standard,
	// cheap.
	TierDefaults map[string]string
}

// Result is what a scaffold produced, for the caller's banner and for tests
// that want to check the project rather than a list of paths.
type Result struct {
	Root string
	Name string
	// RoutesAdded names the router entries inferred from the template's
	// tasks, sorted. Empty for a blank project, which has no tasks.
	RoutesAdded []string
	// RoutesUninferable names models whose key has no `backend/` prefix, so
	// no route could be guessed. A human has to add these.
	RoutesUninferable []string
	// Warnings are things the operator should see but that did not stop the
	// scaffold.
	Warnings []string
}

// ConflictError is a destination that already holds a file `orch init` writes.
//
// Checked before ANY write, so a refused scaffold leaves the directory exactly
// as it found it — a half-scaffolded project is worse than none, because the
// files that did land make it look finished.
type ConflictError struct {
	Root  string
	Paths []string
}

func (e *ConflictError) Error() string {
	var b strings.Builder
	b.WriteString("destination has conflicting files (use --force to overwrite):")
	for _, p := range e.Paths {
		fmt.Fprintf(&b, "\n  - %s", filepath.Join(e.Root, p))
	}
	return b.String()
}

// conflictMarkers are the files whose presence means "there is already a
// project here".
//
// `.gitignore` is deliberately absent: it is a soft target, written only when
// missing and never overwritten, so its presence says nothing about whether
// this directory is an orch project.
var conflictMarkers = []string{
	"tasks.json",
	"scripts/task-start.sh",
	"scripts/task-finish.sh",
	"scripts/task-block.sh",
	".orchestrator/config.yaml",
	".orchestrator/model_router.yaml",
}

// Run scaffolds a project and returns what it produced.
func Run(opts Options) (Result, error) {
	root, err := filepath.Abs(os.ExpandEnv(opts.Root))
	if err != nil {
		return Result{}, fmt.Errorf("resolve %s: %w", opts.Root, err)
	}
	name := opts.Name
	if name == "" {
		name = filepath.Base(root)
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}

	var project fs.FS
	if opts.Template != "" {
		project, err = templates.Project(opts.Template)
		if err != nil {
			return Result{}, err
		}
	}

	if !opts.Force {
		var clashes []string
		for _, m := range conflictMarkers {
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(m))); err == nil {
				clashes = append(clashes, m)
			}
		}
		if len(clashes) > 0 {
			return Result{}, &ConflictError{Root: root, Paths: clashes}
		}
	}

	if err := os.MkdirAll(root, 0o750); err != nil {
		return Result{}, fmt.Errorf("create %s: %w", root, err)
	}

	res := Result{Root: root, Name: name}
	w := &writer{root: root, name: name, now: now, project: project, force: opts.Force}

	w.templated("tasks.json", "tasks.json.tmpl")
	w.scripts()
	w.copyShared(filepath.Join("specs", "README.md"), "specs/README.md")
	w.mkdir(filepath.Join(".orchestrator", "state"))
	w.touch(filepath.Join(".orchestrator", "state", ".gitkeep"))
	w.config()
	w.budgets()
	w.routerStub()
	w.softCopyShared(".gitignore", "gitignore.tmpl")
	w.agents()
	w.workflow()
	w.mcpConfig()
	if opts.SDD {
		w.mkdir(filepath.Join("openspec", "changes"))
		w.mkdir(filepath.Join("openspec", "specs"))
		w.copyShared(filepath.Join("openspec", "README.md"), "openspec/README.md")
	}
	w.applyChoices(opts)
	w.stampTaskMeta(opts)
	if w.err != nil {
		return res, w.err
	}

	added, uninferable, warnings := populateRouter(root)
	res.RoutesAdded = added
	res.RoutesUninferable = uninferable
	res.Warnings = warnings
	return res, nil
}

// populateRouter gives the project's own tasks a route each.
//
// The stub written above is right for a blank project: no tasks, no routes. A
// template ships tasks and every task names a model, so leaving it empty means
// the first command anyone runs fails with "model_router.yaml is missing
// entries" — bug 14, which shipped because every test asserted on files and
// none ran the project.
//
// Never fails the scaffold. A project that got this far is usable, and an
// error here costs the operator one command (`orch router add-missing --yes`),
// which the warning names.
func populateRouter(root string) (added, uninferable []string, warnings []string) {
	routerPath := filepath.Join(root, ".orchestrator", "model_router.yaml")

	tasksFile, err := model.LoadTasksFile(filepath.Join(root, "tasks.json"))
	if err != nil {
		return nil, nil, []string{fmt.Sprintf(
			"could not read tasks.json to build the router (%v) — "+
				"run `orch router add-missing --yes` once the project is set up", err)}
	}

	seen := map[string]bool{}
	var keys []string
	for _, t := range tasksFile.Tasks {
		if t.Model == "" || seen[t.Model] {
			continue
		}
		seen[t.Model] = true
		keys = append(keys, t.Model)
	}
	if len(keys) == 0 {
		return nil, nil, nil
	}
	sort.Strings(keys)

	added, uninferable, err = router.AddMissing(routerPath, keys, model.TierStandard)
	if err != nil {
		return nil, nil, []string{fmt.Sprintf(
			"could not write the router (%v) — "+
				"run `orch router add-missing --yes` to add the routes", err)}
	}
	return added, uninferable, nil
}

// ---- the writer ------------------------------------------------------------

// writer collects the first error instead of returning one per call, so Run
// reads as a list of what a project contains rather than as error handling.
// The first failure stops the work; nothing after it is attempted.
type writer struct {
	root    string
	name    string
	now     time.Time
	project fs.FS
	force   bool
	err     error
}

// fail records the first error and ignores the rest.
//
// Every caller passes a format ending in `: %w` with the underlying error, so
// the wrapping rule is satisfied at the call sites rather than here — this
// function cannot add a verb it does not know the shape of.
func (w *writer) fail(format string, args ...any) {
	if w.err == nil {
		w.err = fmt.Errorf(format, args...) //nolint:govet // callers own the verbs
	}
}

func (w *writer) path(rel string) string { return filepath.Join(w.root, rel) }

func (w *writer) mkdir(rel string) {
	if w.err != nil {
		return
	}
	if err := os.MkdirAll(w.path(rel), 0o750); err != nil {
		w.fail("create %s: %w", rel, err)
	}
}

func (w *writer) touch(rel string) {
	if w.err != nil {
		return
	}
	if _, err := os.Stat(w.path(rel)); err == nil {
		return
	}
	if err := os.WriteFile(w.path(rel), nil, 0o600); err != nil {
		w.fail("create %s: %w", rel, err)
	}
}

func (w *writer) write(rel string, body []byte, mode os.FileMode) {
	if w.err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(w.path(rel)), 0o750); err != nil {
		w.fail("create the directory for %s: %w", rel, err)
		return
	}
	// #nosec G703 -- rel is a constant in this file, never operator input;
	// the root it joins onto is the project the operator asked to scaffold.
	if err := os.WriteFile(w.path(rel), body, mode); err != nil {
		w.fail("write %s: %w", rel, err)
	}
}

// read pulls a file from the chosen template, falling back to the shared tree.
//
// Every file in a template is optional except tasks.json.tmpl — a template
// that ships only that one is legal and gets the shared defaults for
// everything else.
func (w *writer) read(templateRel, sharedRel string) ([]byte, bool) {
	if w.project != nil {
		if body, err := fs.ReadFile(w.project, templateRel); err == nil {
			return body, true
		}
	}
	body, err := fs.ReadFile(templates.Files(), sharedRel)
	if err != nil {
		return nil, false
	}
	return body, true
}

// templated writes a file the template may override, rendering placeholders.
func (w *writer) templated(rel, tmplName string) {
	body, ok := w.read(tmplName, tmplName)
	if !ok {
		w.fail("the embedded tree has no %s", tmplName)
		return
	}
	w.write(rel, []byte(templates.Render(string(body), w.name, w.now)), 0o600)
}

func (w *writer) copyShared(rel, sharedRel string) {
	body, err := fs.ReadFile(templates.Files(), sharedRel)
	if err != nil {
		w.fail("the embedded tree has no %s: %w", sharedRel, err)
		return
	}
	w.write(rel, body, 0o600)
}

// softCopyShared writes only when the file is absent — never overwritten, not
// even with --force, because it is the user's to edit.
func (w *writer) softCopyShared(rel, sharedRel string) {
	if w.err != nil {
		return
	}
	if _, err := os.Stat(w.path(rel)); err == nil {
		return
	}
	w.copyShared(rel, sharedRel)
}

// scripts copies the task-*.sh helpers and makes them executable.
//
// The mode is the point: these are invoked by the agent, and a script without
// +x fails at dispatch time with a message about permissions rather than
// about orch.
func (w *writer) scripts() {
	if w.err != nil {
		return
	}
	entries, err := fs.ReadDir(templates.Files(), "scripts")
	if err != nil {
		w.fail("the embedded tree has no scripts/: %w", err)
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sh") {
			continue
		}
		body, err := fs.ReadFile(templates.Files(), "scripts/"+e.Name())
		if err != nil {
			w.fail("read scripts/%s: %w", e.Name(), err)
			return
		}
		w.write(filepath.Join("scripts", e.Name()), body, 0o750)
	}
}

// defaults holds the three YAML files the Python package ships beside its
// templates directory: `config.yaml`, `budgets.yaml` and `model_router.yaml`.
//
// Deliberately NOT part of `internal/templates`. That package mirrors one
// directory — `orchestrator/templates/` — and its sync test says exactly
// that. These three live beside that directory in the Python package, not
// inside it, and keeping the same distinction means neither test needs an
// exception. `TestPackagedDefaultsMatchPython` holds all three to their
// originals.
//
// config.yaml is the file and not `config.Defaults()` because the comments are
// most of its value: it is the first file an operator edits, and a marshalled
// struct would write the same keys with none of the explanations. The other
// two are read for their contents — the preset names and the tier groupings
// the wizard offers — so they have to be the shipped files rather than a
// restatement of what is in them.
//
//go:embed defaults
var defaults embed.FS

// packagedDefault reads one of the three by name.
func packagedDefault(name string) ([]byte, error) {
	return defaults.ReadFile("defaults/" + name)
}

// config writes `.orchestrator/config.yaml` from the template, or the
// packaged default.
func (w *writer) config() {
	rel := filepath.Join(".orchestrator", "config.yaml")
	if w.project != nil {
		if body, err := fs.ReadFile(w.project, "config.yaml.tmpl"); err == nil {
			w.write(rel, []byte(templates.Render(string(body), w.name, w.now)), 0o600)
			return
		}
	}
	body, err := packagedDefault("config.yaml")
	if err != nil {
		w.fail("the packaged default config is missing: %w", err)
		return
	}
	w.write(rel, []byte(templates.Render(string(body), w.name, w.now)), 0o600)
}

// budgets writes `.orchestrator/budgets.yaml`, the packaged presets.
//
// Go only: Python's init never wrote it, so the guardrail was off on every
// scaffolded project while the wizard asked which preset to use. The preset
// itself is config.yaml's `budgets_preset`; this file holds all of them.
// Like the router, an existing file is kept unless --force.
func (w *writer) budgets() {
	rel := filepath.Join(".orchestrator", "budgets.yaml")
	if !w.force {
		if _, err := os.Stat(w.path(rel)); err == nil {
			return
		}
	}
	body, err := packagedDefault("budgets.yaml")
	if err != nil {
		w.fail("the packaged default budgets.yaml is missing: %w", err)
		return
	}
	w.write(rel, body, 0o600)
}

// routerStub writes the empty router.
//
// Comments only, with no `{}`. An explicit empty flow mapping is a COMPLETE
// YAML document, so appending a block mapping after it produces a file neither
// PyYAML nor router.Load will read — which made `orch router add-missing`
// break the one file whose own comment tells you to run it. Both halves of
// that are fixed (#141, #144); writing the stub this way is what keeps it
// fixed here.
func (w *writer) routerStub() {
	rel := filepath.Join(".orchestrator", "model_router.yaml")
	if !w.force {
		if _, err := os.Stat(w.path(rel)); err == nil {
			return
		}
	}
	w.write(rel, []byte(routerStubBody), 0o600)
}

const routerStubBody = `# model_router.yaml — maps tasks.json model strings to CLI
# invocations. This stub was generated by ` + "`orch init`" + `.
#
# Routes for the tasks this project was scaffolded with are added below
# automatically. When you add tasks that reference NEW models, run:
#     orch router add-missing --yes
# to append inferred entries.
`

// agents writes AGENTS.md — the template's, or the generic one.
//
// Soft unless --force: it is committed and hand-edited, and clobbering a
// project's agent context on a re-init would lose work nothing else records.
func (w *writer) agents() {
	if w.err != nil {
		return
	}
	if !w.force {
		if _, err := os.Stat(w.path("AGENTS.md")); err == nil {
			return
		}
	}
	if w.project != nil {
		if body, err := fs.ReadFile(w.project, "AGENTS.md.tmpl"); err == nil {
			w.write("AGENTS.md", []byte(templates.Render(string(body), w.name, w.now)), 0o600)
			return
		}
	}
	w.write("AGENTS.md", []byte(genericAgentsMD(w.name)), 0o600)
}

// workflow writes the CI workflow the auto-PR loop watches.
//
// Soft unless --force: user-editable once it exists.
func (w *writer) workflow() {
	if w.err != nil {
		return
	}
	rel := filepath.Join(".github", "workflows", "orch-ci.yml")
	if !w.force {
		if _, err := os.Stat(w.path(rel)); err == nil {
			return
		}
	}
	// config.yaml's github.test_command follows the repo either way.
	testCommand := w.testCommand()

	// A repository that already has code gets the pipeline its files call for
	// (lint, typecheck, test, build per package; internal/ci). A new one has
	// nothing to read yet, and gets the template's single test job.
	if st, err := ci.Detect(w.root); err == nil && st.HasChecks() {
		body, err := ci.Render(ci.Jobs(st, ci.AllChecks), ci.Options{BaseBranch: w.baseBranch(), Checks: ci.AllChecks})
		if err != nil {
			w.fail("rendering the detected CI pipeline: %w", err)
			return
		}
		w.write(rel, body, 0o600)
		return
	} else if err != nil {
		w.fail("reading the repository to propose its CI: %w", err)
		return
	}
	body, err := fs.ReadFile(templates.Files(), "github/orch-ci.yml.tmpl")
	if err != nil {
		w.fail("the embedded tree has no CI workflow: %w", err)
		return
	}
	// Python baked `pytest` and a Python toolchain into every repo (#233).
	// The command is config.yaml's `github.test_command` — the template's, or
	// the one the repo's files imply — and the setup steps follow it.
	wf := strings.ReplaceAll(string(body), "SETUP_STEPS", setupSteps(w.root, testCommand))
	w.write(rel, []byte(strings.ReplaceAll(wf, "TEST_COMMAND", testCommand)), 0o600)
}

// ErrTemplateNotFound is re-exported so a caller can tell a bad --template
// from a filesystem problem without importing templates as well.
var ErrTemplateNotFound = errors.New("unknown template")

// applyChoices rewrites the three config keys the wizard asks about.
//
// A regex edit rather than a YAML round-trip, which is Python's choice and is
// kept for its reason: the shipped config.yaml is mostly comments, and they
// are the guidance an operator reads when they open the file later. Marshalling
// the parsed document back out would produce the same keys and delete every
// line explaining them.
//
// An empty answer leaves the key alone. That matters for batch mode: a flag
// nobody passed must not overwrite what the template chose, and `orch init
// --template python-api` should get python-api's `spec_root`, not a blank.
//
// # Divergence: a missing key is appended, not dropped
//
// Python's regexes have `count=1` and no fallback, so a key the file does not
// contain is silently left out — and NONE of the four shipped templates
// contains `budgets_preset`. The wizard therefore asks for a budget preset,
// shows the answer in the confirm summary, takes the operator's "yes", and
// discards it: the project loads with the packaged default instead. A confirm
// gate that displays a choice which then has no effect is worse than not
// asking, because the operator has been told it took.
//
// Here a key that is not in the file is appended at the top level with a
// comment saying where it came from. Reported as bug 17.
func (w *writer) applyChoices(opts Options) {
	if w.err != nil {
		return
	}
	if opts.StateBackend == "" && opts.BudgetPreset == "" && opts.SpecRoot == "" {
		return
	}

	rel := filepath.Join(".orchestrator", "config.yaml")
	raw, err := os.ReadFile(w.path(rel)) // #nosec G304 -- the file this scaffold just wrote
	if err != nil {
		w.fail("read %s to apply the chosen settings: %w", rel, err)
		return
	}

	body := string(raw)
	// `backend:` is nested under `state:`, so it is matched with its leading
	// whitespace and the whitespace is preserved — a replacement that
	// unindented it would move the key to the top level and change what it
	// means. It is also the one of the three that cannot be appended: a bare
	// `backend:` at the end of the file would be a different setting, so a
	// config with no `state:` block keeps the default.
	body = replaceOrAppend(body, reStateBackend, "", opts.StateBackend, false)
	body = replaceOrAppend(body, reBudgetsPreset, "budgets_preset", opts.BudgetPreset, true)
	body = replaceOrAppend(body, reSpecRoot, "spec_root", opts.SpecRoot, true)

	w.write(rel, []byte(body), 0o600)
}

var (
	reStateBackend  = regexp.MustCompile(`(?m)^(\s*backend:\s*)\S+`)
	reBudgetsPreset = regexp.MustCompile(`(?m)^(budgets_preset:\s*)\S+`)
	reSpecRoot      = regexp.MustCompile(`(?m)^(spec_root:\s*)\S+`)
)

// replaceOrAppend swaps the value of the first line the pattern matches,
// keeping whatever prefix (indent, key, spacing) the file already had — or
// appends `key: value` when the file has no such line and appending is safe.
//
// `canAppend` is false for a nested key: writing a bare `backend:` at the end
// of the file would be a top-level setting with a different meaning, so a
// config without a `state:` block is left as it is.
func replaceOrAppend(body string, re *regexp.Regexp, key, value string, canAppend bool) string {
	if value == "" {
		return body
	}
	if loc := re.FindStringSubmatchIndex(body); loc != nil {
		prefix := body[loc[2]:loc[3]]
		return body[:loc[0]] + prefix + value + body[loc[1]:]
	}
	if !canAppend || key == "" {
		return body
	}
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return body + "\n# Added by `orch init`.\n" + key + ": " + value + "\n"
}

// stampTaskMeta records the wizard's per-tier model picks in tasks.json.
//
// Informational: nothing dispatches on them. They are there so a later tool —
// or a person reading the file — can see which models the project was set up
// around, which is otherwise recorded nowhere.
//
// Failing to stamp them does not fail the scaffold. The project is complete
// without the meta keys, and losing a whole init over three informational
// fields would be the wrong trade.
func (w *writer) stampTaskMeta(opts Options) {
	if w.err != nil || len(opts.TierDefaults) == 0 {
		return
	}
	rel := "tasks.json"
	raw, err := os.ReadFile(w.path(rel)) // #nosec G304 -- the file this scaffold just wrote
	if err != nil {
		return
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return
	}
	meta := map[string]any{}
	if rawMeta, ok := doc["meta"]; ok {
		if err := json.Unmarshal(rawMeta, &meta); err != nil {
			return
		}
	}
	for _, tier := range []string{"premium", "standard", "cheap"} {
		if m := opts.TierDefaults[tier]; m != "" {
			meta["default_"+tier+"_model"] = m
		}
	}
	encoded, err := json.Marshal(meta)
	if err != nil {
		return
	}
	doc["meta"] = encoded

	// Re-encoded with the same two-space indent the templates use, so a
	// scaffolded tasks.json still reads as something a person wrote.
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return
	}
	w.write(rel, append(out, '\n'), 0o600)
}
