// Package dashboard will host the operator dashboard's HTTP server (G5.2,
// a different lane's work — replacing orchestrator/dashboard/server.py's
// FastAPI app). Today it only exposes the built web/ SPA as an fs.FS for
// that server to mount later.
//
// The `go:embed` directive below cannot reach outside its own package
// directory, so web/vite.config.ts points build.outDir directly at
// internal/dashboard/dist/build — `pnpm build` in web/ writes straight
// here, with no go:generate copy step to keep in sync (the alternative
// considered and rejected; see docs/brainstorm/go-migration-notes/
// sonnet-2.md). dist/README.md is the one file tracked directly under
// dist/: go:embed requires at least one matching file to compile at all,
// and vite's emptyOutDir is scoped to dist/build/ only, so it never
// touches dist/README.md across rebuilds.
package dashboard

import (
	"embed"
	"io/fs"
)

//go:embed dist
var distFS embed.FS

// SPA returns the built web/ SPA, rooted at its index.html, as an fs.FS
// ready for an HTTP file server to mount. Before `pnpm build` has run in
// web/ (or `make build`), dist/build/ doesn't exist yet — SPA itself still
// succeeds (fs.Sub does no I/O), but any read against the result fails;
// see TestSPARequiresABuild.
func SPA() (fs.FS, error) {
	return fs.Sub(distFS, "dist/build")
}
