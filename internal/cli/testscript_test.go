package cli_test

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/rogpeppe/go-internal/testscript"

	"github.com/hectorcanaimero/orch/internal/cli"
)

// TestMain lets testscript scripts `exec orch ...` in-process — cheaper and
// more portable than shelling out to a separately-built binary, and it's
// the same code path scripts/parity.sh's Go side exercises via `make
// build`.
func TestMain(m *testing.M) {
	testscript.Main(m, map[string]func(){
		"orch": func() { os.Exit(cli.Run("test", os.Args[1:])) },
	})
}

// fixtureDir is testdata/parity-project at the repo root — the same real
// project scripts/parity.sh uses. See its README.md for how it was built.
func fixtureDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "parity-project"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("parity fixture not found at %s: %v", dir, err)
	}
	return dir
}

func TestCLICommands(t *testing.T) {
	src := fixtureDir(t)
	testscript.Run(t, testscript.Params{
		Dir: "testdata/script",
		Setup: func(env *testscript.Env) error {
			// Copy the fixture into $WORK/proj, skipping the checked-in
			// goldens/README — they aren't part of a real project tree
			// (same exclusion scripts/parity.sh applies).
			if err := copyProjectFixture(src, filepath.Join(env.WorkDir, "proj")); err != nil {
				return err
			}
			// A second project, backed by the real Python-written
			// internal/state/testdata/orch-py-0.11.0.db (4 real events
			// across 3 tasks — see its README.md) — testdata/parity-project
			// has none, so the non-empty `events` path (tabwriter, --json,
			// --run, shortRunID) has nothing to exercise without it.
			return setUpEventsFixture(filepath.Join(env.WorkDir, "billing-api"))
		},
	})
}

// billingAPITasksJSON mirrors the TASKS list make-fixture.py used to write
// internal/state/testdata/orch-py-0.11.0.db (see that file) — id/phase/
// title/model/dependencies only, since that's all buildStatusRows needs;
// runtime status/cost/events come from the database, not this file.
const billingAPITasksJSON = `{"meta":{},"phases":[],"tasks":[
  {"id":"F0.T1","phase":0,"title":"Scaffold FastAPI project layout","model":"claude/claude-sonnet-4-6","dependencies":[]},
  {"id":"F0.T2","phase":0,"title":"Postgres connection pool + settings","model":"claude/claude-sonnet-4-6","dependencies":["F0.T1"]},
  {"id":"F1.T1","phase":1,"title":"Health endpoint + integration test","model":"claude/claude-sonnet-4-6","dependencies":["F0.T2"]},
  {"id":"F1.T2","phase":1,"title":"POST /customers with validation","model":"claude/claude-opus-4-6","dependencies":["F1.T1"]},
  {"id":"F2.T1","phase":2,"title":"Invoice state machine","model":"claude/claude-opus-4-6","dependencies":["F1.T2"]}
]}`

// billingAPIRouterYAML routes both models the fixture DB's spend/tasks use,
// so buildStatusRows resolves real backend/cli_model/tier instead of falling
// back to "route not found" for every row.
const billingAPIRouterYAML = `claude/claude-sonnet-4-6:
  backend: claude
  cli_model: claude-sonnet-4-6
  tier: standard
claude/claude-opus-4-6:
  backend: claude
  cli_model: claude-opus-4-6
  tier: premium
`

// setUpEventsFixture scaffolds a project for `orch events`/`orch status`
// backed by a real database (internal/state/testdata/orch-py-0.11.0.db):
// tasks.json/model_router.yaml mirror what make-fixture.py wrote into it, so
// status/tasks --json exercise real cost/latest_run/route resolution, not
// just the empty defaults testdata/parity-project has.
func setUpEventsFixture(root string) error {
	if err := os.MkdirAll(filepath.Join(root, "scripts"), 0o750); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(root, "tasks.json"),
		[]byte(billingAPITasksJSON), 0o600); err != nil {
		return err
	}
	// #nosec G306 -- a shell script needs the executable bit; this is a
	// throwaway test fixture, not a real project's script.
	if err := os.WriteFile(filepath.Join(root, "scripts", "task-start.sh"),
		[]byte("#!/usr/bin/env bash\nexit 0\n"), 0o755); err != nil {
		return err
	}
	cfgDir := filepath.Join(root, ".orchestrator")
	if err := os.MkdirAll(cfgDir, 0o750); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"),
		[]byte("state:\n  backend: sqlite\n"), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "model_router.yaml"),
		[]byte(billingAPIRouterYAML), 0o600); err != nil {
		return err
	}
	// project-root is passed explicitly in the scripts below, which
	// selects the NAMESPACED state layout: .orchestrator/state/<id>/orch.db.
	stateDir := filepath.Join(cfgDir, "state", "billing-api")
	if err := os.MkdirAll(stateDir, 0o750); err != nil {
		return err
	}
	pyDB, err := filepath.Abs(filepath.Join("..", "state", "testdata", "orch-py-0.11.0.db"))
	if err != nil {
		return err
	}
	return copyFile(pyDB, filepath.Join(stateDir, "orch.db"))
}

func copyProjectFixture(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "goldens" || rel == "README.md" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		return copyFile(path, target)
	})
}

func copyFile(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	// #nosec G304 -- src walks the fixture directory this test controls.
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	// #nosec G304 -- dst is built from the test's own $WORK, not caller input.
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	_, err = io.Copy(out, in)
	return err
}
