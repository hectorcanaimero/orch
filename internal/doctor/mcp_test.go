package doctor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckMCPConfigWarnsWhenAbsent(t *testing.T) {
	c := CheckMCPConfig(t.TempDir())
	if c.Status != StatusWarn {
		t.Errorf("c = %+v, want warn", c)
	}
}

func TestCheckMCPConfigOKWhenPresent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := CheckMCPConfig(dir)
	if c.Status != StatusOK {
		t.Errorf("c = %+v, want ok", c)
	}
}
