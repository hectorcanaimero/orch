// Package publish turns the stakeholder snapshot into something a client can
// open: a self-contained static site, written to a directory or pushed to a
// branch a static host serves (G6.3 — blueprint F2.3 and F2.4).
//
// The document itself is internal/publish/snapshot's (G6.1) and the page that
// renders it is web/'s second bundle (G6.2, web/vite.stakeholder.config.ts).
// This package is only the join: copy the bundle, write the data beside it,
// and put the result where somebody can reach it. Nothing here computes a
// figure — if a number is wrong, it is wrong in snapshot.Build, and the PDF
// and the dashboard's stakeholder route are wrong in exactly the same way.
package publish

import (
	"embed"
	"io/fs"
)

//go:embed dist
var distFS embed.FS

// Bundle returns the built stakeholder SPA, rooted where vite left it, as an
// fs.FS ready to copy. It mirrors dashboard.SPA() deliberately — same embed
// shape, same failure mode, same README under dist/ keeping `go:embed` fed on
// a fresh checkout (a pattern matching zero files does not compile).
//
// Before `pnpm build:stakeholder` has run in web/ (or `make web`), the
// directory is empty: Bundle itself still succeeds, since fs.Sub does no I/O,
// and every read against the result fails. TestBundleRequiresABuild says so in
// one sentence rather than letting `orch publish` write a site with no page in
// it.
func Bundle() (fs.FS, error) {
	return fs.Sub(distFS, "dist/stakeholder")
}

// bundleIndexName is the file vite emits, which is NOT what a static host
// serves by default.
//
// The entry point is `web/stakeholder.html` (vite names the output after its
// input), and a directory whose landing page is `stakeholder.html` is a
// directory that 404s at `/`. Export renames it on the way out — the bundle is
// an input, the export is a website, and the rename belongs to whichever of
// the two knows it is publishing one.
const bundleIndexName = "stakeholder.html"
