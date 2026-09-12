package telemetry

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallIDPersistsAcrossCalls(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	first, err := InstallID()
	if err != nil {
		t.Fatalf("InstallID: %v", err)
	}
	if first == "" {
		t.Fatal("InstallID returned an empty string")
	}

	second, err := InstallID()
	if err != nil {
		t.Fatalf("InstallID (second call): %v", err)
	}
	if second != first {
		t.Errorf("InstallID changed between calls: %q then %q", first, second)
	}
}

func TestInstallIDIsUniquePerHome(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	a, err := InstallID()
	if err != nil {
		t.Fatalf("InstallID: %v", err)
	}

	t.Setenv("HOME", t.TempDir())
	b, err := InstallID()
	if err != nil {
		t.Fatalf("InstallID: %v", err)
	}

	if a == b {
		t.Error("two different home directories produced the same install id")
	}
}

func TestInstallIDFilePermissions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if _, err := InstallID(); err != nil {
		t.Fatalf("InstallID: %v", err)
	}

	path := filepath.Join(home, ".orch", "install_id")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("install_id perm = %o, want 0600", perm)
	}
}
