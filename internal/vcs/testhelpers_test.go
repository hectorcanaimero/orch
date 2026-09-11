package vcs

import (
	"os"
	"path/filepath"
	"testing"
)

// withFakeBin prepends testdata/fakebin (the fake gh/glab scripts) to PATH
// for the duration of the test, so exec.LookPath and the actual exec.Command
// calls this package makes resolve to them instead of any real CLI.
func withFakeBin(t *testing.T) {
	t.Helper()
	dir, err := filepath.Abs("testdata/fakebin")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// withNoBin points PATH somewhere with neither gh nor glab, for the
// binary-missing test cases.
func withNoBin(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

// ghLog returns a fresh temp file path and points FAKE_GH_LOG at it, so a
// test can assert on the exact argv the fake gh received.
func ghLog(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gh.log")
	t.Setenv("FAKE_GH_LOG", path)
	return path
}

func glabLog(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "glab.log")
	t.Setenv("FAKE_GLAB_LOG", path)
	return path
}

func readLog(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 -- path is this test's own t.TempDir() file.
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
