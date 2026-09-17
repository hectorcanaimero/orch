package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/hectorcanaimero/orch/internal/cli"
	"github.com/hectorcanaimero/orch/internal/export"
	"github.com/hectorcanaimero/orch/internal/export/clickuptest"
)

// TestExportClickUp drives the real command against a fake ClickUp. The
// fixture's DB has F0.T1, F1.T1 and F2.T1 done, F2.T2 in progress and F2.T3
// blocked, so every orch status that needs a ClickUp one is exercised.
func TestExportClickUp(t *testing.T) {
	root, common := newTestProject(t)
	fake := clickuptest.New(t)
	t.Setenv(export.ClickUpAPIURLEnv, fake.URL())
	t.Setenv(export.ClickUpTokenEnv, "")
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte(clickuptest.Token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(root, "tasks.json")) // #nosec G304 -- the test's own temp project.
	if err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) int {
		return cli.Run("test", append(append([]string{"export", "clickup"}, args...), common...))
	}

	if rc := run("--token-file", tokenFile); rc != 2 {
		t.Errorf("without --folder: rc = %d, want 2", rc)
	}
	if rc := run("--folder", fake.FolderID); rc != 2 {
		t.Errorf("without a token: rc = %d, want 2", rc)
	}
	if rc := run("--folder", fake.FolderID, "--token-file", tokenFile, "--status-map", "done"); rc != 2 {
		t.Errorf("malformed --status-map: rc = %d, want 2", rc)
	}

	if rc := run("--folder", fake.FolderID, "--token-file", tokenFile, "--dry-run"); rc != 0 {
		t.Fatalf("dry run: rc = %d", rc)
	}
	if fake.Writes != 0 {
		t.Fatalf("dry run wrote %d time(s)", fake.Writes)
	}

	if rc := run("--folder", fake.FolderID, "--token-file", tokenFile, "--language", "es"); rc != 0 {
		t.Fatalf("export: rc = %d", rc)
	}
	if len(fake.Lists) != 3 || len(fake.Tasks) != 5 {
		t.Fatalf("got %d List(s) and %d task(s), want 3 and 5", len(fake.Lists), len(fake.Tasks))
	}
	want := map[string]string{"F0.T1": "complete", "F1.T1": "complete", "F2.T1": "complete", "F2.T2": "in progress", "F2.T3": "blocked"}
	for _, task := range fake.Tasks {
		id := task.Name[:len("F0.T1")]
		if task.Status != want[id] {
			t.Errorf("%s is %q in ClickUp, want %q", id, task.Status, want[id])
		}
		if !bytes.Contains([]byte(task.Markdown), []byte("orch-task: parity-project/"+id)) {
			t.Errorf("%s has no marker:\n%s", id, task.Markdown)
		}
	}

	// A re-run is a no-op, and neither run touched the project.
	writes := fake.Writes
	if rc := run("--folder", fake.FolderID, "--token-file", tokenFile); rc != 0 {
		t.Fatalf("re-run: rc = %d", rc)
	}
	if fake.Writes != writes {
		t.Errorf("re-run wrote %d time(s)", fake.Writes-writes)
	}
	after, err := os.ReadFile(filepath.Join(root, "tasks.json")) // #nosec G304 -- the test's own temp project.
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("export changed tasks.json")
	}
	if matches, _ := filepath.Glob(filepath.Join(root, "tasks.json.bak-*")); len(matches) > 0 {
		t.Errorf("export wrote backups: %v", matches)
	}
}
