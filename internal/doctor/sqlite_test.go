package doctor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckSQLiteOpensFreshDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "orch.db")
	c := CheckSQLite(context.Background(), path)
	if c.Status != StatusOK {
		t.Errorf("c = %+v, want ok", c)
	}
}

func TestCheckSQLiteErrorsOnUnwritablePath(t *testing.T) {
	// A path whose parent is a file (not a directory) can never be created.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := CheckSQLite(context.Background(), filepath.Join(blocker, "orch.db"))
	if c.Status != StatusError {
		t.Errorf("c = %+v, want error", c)
	}
}
