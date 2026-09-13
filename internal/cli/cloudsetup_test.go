package cli_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hectorcanaimero/orch/internal/cli"
)

// fakeNPXDir is internal/publish's fake npx, which plays wrangler and logs
// every call to $FAKE_WRANGLER_STATE/argv.log.
func fakeNPXDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "publish", "testdata", "fakebin"))
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// --dry-run is the command a cautious operator runs first. It must not run
// wrangler at all — not even whoami, which talks to Cloudflare — and must not
// touch the credentials file.
func TestCloudSetupDryRunRunsNothing(t *testing.T) {
	state := t.TempDir()
	home := t.TempDir()
	t.Setenv("FAKE_WRANGLER_STATE", state)
	t.Setenv("PATH", fakeNPXDir(t)+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
	t.Setenv("HOME", home)

	if rc := cli.Run("test", []string{"cloud", "setup", "--dry-run"}); rc != 0 {
		t.Fatalf("orch cloud setup --dry-run: rc = %d, want 0", rc)
	}
	if _, err := os.Stat(filepath.Join(state, "argv.log")); err == nil {
		t.Error("--dry-run ran npx")
	}
	if _, err := os.Stat(filepath.Join(home, ".orch", "credentials")); err == nil {
		t.Error("--dry-run wrote credentials")
	}
}

// No npx is a missing prerequisite, reported like every other usage-level
// problem in this CLI: exit 2, before anything else runs.
func TestCloudSetupWithoutNPXExitsTwo(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	if rc := cli.Run("test", []string{"cloud", "setup", "--yes"}); rc != 2 {
		t.Errorf("orch cloud setup without npx: rc = %d, want 2", rc)
	}
}

func TestCloudSetupRejectsAnInvalidWorkerName(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if rc := cli.Run("test", []string{"cloud", "setup", "--dry-run", "--name", "Not_A_Name"}); rc != 2 {
		t.Errorf("--name Not_A_Name: rc = %d, want 2", rc)
	}
}
