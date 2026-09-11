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
			return copyProjectFixture(src, filepath.Join(env.WorkDir, "proj"))
		},
	})
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
