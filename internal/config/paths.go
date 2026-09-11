package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Paths is where one project keeps its files. Ported from
// `orchestrator/paths.py`, which every Python entry point routes through, so
// the two implementations look in the same places.
type Paths struct {
	// Root is the project directory: the one holding tasks.json, scripts/
	// and .orchestrator/.
	Root string
	// ID identifies the project in the database and in logs. Several
	// projects can share one orch.db, keyed by this.
	ID string
	// ConfigYAML is the config file, normally .orchestrator/config.yaml.
	ConfigYAML string
	// Layout decides the shape of StateDir. See ResolvePaths.
	Layout StateLayout
}

// StateLayout is where state lives under `.orchestrator/state/`.
type StateLayout string

const (
	// LayoutLegacy puts state directly in `.orchestrator/state/`. This is
	// what a project gets when orch is run from inside it with no explicit
	// root — the historical shape, and the common one.
	LayoutLegacy StateLayout = "legacy"
	// LayoutNamespaced puts it in `.orchestrator/state/<project-id>/`.
	// Chosen when the root was given explicitly, which is the multi-project
	// case: without the namespace, two projects would collide on run files,
	// event logs and the lock.
	LayoutNamespaced StateLayout = "namespaced"
)

// genericBasenames are directory names too vague to identify a project. When
// the root ends in one, the parent's name is used instead — `~/work/orch/v2`
// is the orch project, not the "v2" project.
var genericBasenames = map[string]bool{
	"v2": true, "v3": true, "app": true, "apps": true,
	"src": true, "workspace": true, "repo": true, "root": true,
}

// ResolvePaths works out where everything is, mirroring
// `paths.resolve_project_paths`.
//
// Precedence for the root: the argument, then ORCH_PROJECT_ROOT, then the
// working directory. Same for the id via ORCH_PROJECT_ID, falling back to the
// root's basename.
//
// Whether the root was given EXPLICITLY (by argument or env) is not just
// bookkeeping: it selects the state layout. Running orch from inside a
// project keeps the legacy flat layout; pointing orch at a project from
// elsewhere gets a namespaced one. State is never migrated between the two —
// a project that started flat stays flat, because moving it would strand the
// run files an interrupted run needs to resume.
func ResolvePaths(rootArg, idArg, configArg string) (Paths, error) {
	root := rootArg
	explicit := root != ""
	if root == "" {
		if env := os.Getenv("ORCH_PROJECT_ROOT"); env != "" {
			root, explicit = env, true
		}
	}
	if root == "" {
		wd, err := os.Getwd()
		if err != nil {
			return Paths{}, fmt.Errorf("determine the working directory: %w", err)
		}
		root = wd
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return Paths{}, fmt.Errorf("resolve project root %q: %w", root, err)
	}

	id := idArg
	if id == "" {
		id = os.Getenv("ORCH_PROJECT_ID")
	}
	if id == "" {
		id = DefaultProjectID(abs)
	}

	if configArg == "" {
		configArg = filepath.Join(".orchestrator", "config.yaml")
	}
	cfgPath := configArg
	if !filepath.IsAbs(cfgPath) {
		cfgPath = filepath.Join(abs, cfgPath)
	}

	layout := LayoutLegacy
	if explicit {
		layout = LayoutNamespaced
	}

	p := Paths{Root: abs, ID: id, ConfigYAML: cfgPath, Layout: layout}

	// A project previously run with an explicit root already has a
	// namespaced directory. Detect it, or `orch dashboard` started from
	// inside the project would read an empty legacy database while the CLI
	// writes to the namespaced one — issue #84, which cost a day.
	if layout == LayoutLegacy && hasNamespacedState(abs, id) {
		p.Layout = LayoutNamespaced
	}
	return p, nil
}

// DefaultProjectID derives an id from the root's basename, skipping the
// generic names. Returns "unknown" at the filesystem root, because something
// has to be loggable.
func DefaultProjectID(root string) string {
	clean := filepath.Clean(root)
	name := filepath.Base(clean)
	if genericBasenames[name] {
		parent := filepath.Dir(clean)
		if parent != clean {
			if pn := filepath.Base(parent); pn != "" && pn != "." && pn != string(filepath.Separator) {
				return pn
			}
		}
	}
	if name == "" || name == "." || name == string(filepath.Separator) {
		return "unknown"
	}
	return name
}

// StateDir is where run files, logs and orch.db live.
func (p Paths) StateDir() string {
	base := filepath.Join(p.Root, ".orchestrator", "state")
	if p.Layout == LayoutNamespaced {
		return filepath.Join(base, p.ID)
	}
	return base
}

// TasksJSON is the DAG file at the project root.
func (p Paths) TasksJSON() string { return filepath.Join(p.Root, "tasks.json") }

// RouterYAML is the model routing table.
func (p Paths) RouterYAML() string {
	return filepath.Join(p.Root, ".orchestrator", "model_router.yaml")
}

// SQLitePath resolves `state.sqlite_path` against the state dir, as Python's
// `_resolve_sqlite_path` does: absolute wins, relative hangs off the state
// dir, empty means `<state dir>/orch.db`.
func (p Paths) SQLitePath(cfg Config) string {
	raw := strings.TrimSpace(cfg.State.SQLitePath)
	if raw == "" {
		return filepath.Join(p.StateDir(), "orch.db")
	}
	if filepath.IsAbs(raw) {
		return filepath.Clean(raw)
	}
	return filepath.Join(p.StateDir(), raw)
}

// hasNamespacedState reports whether `.orchestrator/state/<id>/` already
// holds real state — a database or the JSONL files a pre-SQLite project left
// behind. An empty directory does not count: it would flip the layout on the
// strength of a `mkdir`.
func hasNamespacedState(root, id string) bool {
	dir := filepath.Join(root, ".orchestrator", "state", id)
	// #nosec G703 -- root and id come from the caller's own project layout;
	// this only stats a path, it never opens or writes one.
	if _, err := os.Stat(filepath.Join(dir, "orch.db")); err == nil {
		return true
	}
	for _, pattern := range []string{"spend-*.jsonl", "events-*.jsonl"} {
		if m, _ := filepath.Glob(filepath.Join(dir, pattern)); len(m) > 0 {
			return true
		}
	}
	return false
}

// DivergentDatabases reports a legacy and a namespaced orch.db coexisting
// under one project. Issue #84: the CLI wrote one and the dashboard read the
// other, so the operator saw two different truths with no error anywhere.
// Returns the two paths, or empty strings when there is no divergence.
func (p Paths) DivergentDatabases() (legacy, namespaced string) {
	l := filepath.Join(p.Root, ".orchestrator", "state", "orch.db")
	n := filepath.Join(p.Root, ".orchestrator", "state", p.ID, "orch.db")
	if _, err := os.Stat(l); err != nil {
		return "", ""
	}
	if _, err := os.Stat(n); err != nil {
		return "", ""
	}
	return l, n
}
