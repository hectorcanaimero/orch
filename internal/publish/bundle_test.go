package publish

import (
	"io/fs"
	"testing"
)

// TestBundleRequiresABuild is internal/dashboard's TestSPARequiresABuild for
// the second bundle: a fresh checkout or a CI job that has not run
// `pnpm build:stakeholder` gets one failing test that says what to do, rather
// than an `orch publish` that writes a directory with data in it and no page.
func TestBundleRequiresABuild(t *testing.T) {
	b, err := Bundle()
	if err != nil {
		t.Fatalf("Bundle(): %v", err)
	}
	if _, err := fs.Stat(b, bundleIndexName); err != nil {
		t.Fatalf("the stakeholder bundle has not been built — run `make web` "+
			"(or `pnpm build:stakeholder` in web/) before internal/publish has a real "+
			"page to embed: %v", err)
	}
}

// TestBundleIsRootedAtTheOutDir pins that Bundle() strips the dist/stakeholder
// prefix, so a caller asks for the page by name without knowing vite's layout.
func TestBundleIsRootedAtTheOutDir(t *testing.T) {
	b, err := Bundle()
	if err != nil {
		t.Fatalf("Bundle(): %v", err)
	}
	if _, err := fs.Stat(b, "dist/stakeholder/"+bundleIndexName); err == nil {
		t.Error("Bundle() should already be rooted at dist/stakeholder")
	}
}
