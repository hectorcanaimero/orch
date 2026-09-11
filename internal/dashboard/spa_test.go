package dashboard

import (
	"io/fs"
	"testing"
)

// TestSPARequiresABuild is the "clear message instead of a cryptic
// failure" the G5.1 brief asks for: a fresh checkout (or a CI job) that
// hasn't run `pnpm build` in web/ yet gets exactly one failing test that
// says what to do, rather than a silently empty dashboard once G5.2 wires
// an HTTP server around SPA().
func TestSPARequiresABuild(t *testing.T) {
	spa, err := SPA()
	if err != nil {
		t.Fatalf("SPA(): %v", err)
	}
	if _, err := fs.Stat(spa, "index.html"); err != nil {
		t.Fatalf("web/ has not been built — run `pnpm build` in web/ (or `make build`) "+
			"before internal/dashboard has a real SPA to embed: %v", err)
	}
}

// TestSPAIsRootedAtIndexHTML pins that SPA() strips the dist/build
// prefix, so a caller can request "index.html" directly rather than
// needing to know vite's outDir layout.
func TestSPAIsRootedAtIndexHTML(t *testing.T) {
	spa, err := SPA()
	if err != nil {
		t.Fatalf("SPA(): %v", err)
	}
	if _, err := fs.Stat(spa, "dist/build/index.html"); err == nil {
		t.Error("SPA() should already be rooted at dist/build — dist/build/index.html should not resolve")
	}
}
