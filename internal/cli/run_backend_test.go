package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hectorcanaimero/orch/internal/engine"
	"github.com/hectorcanaimero/orch/internal/state"
)

// countingBackend records whether the run wrote through it.
type countingBackend struct {
	state.Backend
	started int
}

func (c *countingBackend) StartRun(ctx context.Context, runID, mode string) error {
	c.started++
	return c.Backend.StartRun(ctx, runID, mode)
}

// A run handed a backend writes through it and opens no database of its
// own: `orch bench` reads the run's spend through that same backend while
// the run goes, and a second open would be a second writer on the file
// (CHECKLIST rule 17). The passed backend's file is not the project's default
// database path, so a run that opened its own would create that file.
func TestRunProjectUsesThePassedBackend(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"tasks.json":                      `{"tasks":[{"id":"T-1","phase":1,"title":"t","model":"claude/haiku","status":"todo","dependencies":[],"estimateHours":1,"files":[],"specRef":""}]}`,
		".orchestrator/config.yaml":       "concurrency:\n  global_max: 1\n",
		".orchestrator/model_router.yaml": "claude/haiku:\n  backend: claude\n  cli_model: claude-haiku-4-5\n  tier: cheap\n",
		"fake/claude/_default.out":        `{"type":"result","subtype":"success","is_error":false,"total_cost_usd":0,"usage":{"input_tokens":1,"output_tokens":1}}`,
		"scripts/task-start.sh":           "#!/bin/sh\nexit 0\n",
		"scripts/task-finish.sh":          "#!/bin/sh\nexit 0\n",
	}
	for rel, body := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		// #nosec G306 -- the fixture's task scripts must be executable.
		if err := os.WriteFile(p, []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(engine.FakeProviderEnv, filepath.Join(root, "fake"))

	flags := &projectFlags{root: root}
	paths, cfg, err := loadProjectConfig(flags)
	if err != nil {
		t.Fatal(err)
	}
	// The passed backend lives in a different file than the project's
	// default path, so any database the run opened itself would show up.
	db, _, err := state.Open(context.Background(), filepath.Join(t.TempDir(), "passed.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	passed := &countingBackend{Backend: state.NewSQLite(db, paths.ID, paths.Root)}

	err = runProject(context.Background(), strings.NewReader(""), io.Discard, flags,
		runOptions{mode: string(engine.ModeAuto), isolated: true, backend: passed})
	if err != nil {
		t.Fatalf("runProject: %v", err)
	}
	if passed.started != 1 {
		t.Errorf("the run started %d times through the passed backend, want 1", passed.started)
	}
	if _, err := os.Stat(paths.SQLitePath(cfg)); !os.IsNotExist(err) {
		t.Errorf("the run opened its own database at %s (stat err %v); it must use the passed backend only",
			paths.SQLitePath(cfg), err)
	}
}
