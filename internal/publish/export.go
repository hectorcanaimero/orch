package publish

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/hectorcanaimero/orch/internal/publish/snapshot"
)

// robotsTXT keeps the export out of search results.
//
// A published snapshot is not secret, but it is not an advertisement either:
// it carries a client's project name, its progress and — when the operator
// turned that on — what it has cost. Indexing that is nobody's intent, and a
// static host has no other place to say so.
const robotsTXT = "User-agent: *\nDisallow: /\n"

// dataJSONName and dataJSName are the two spellings of the same document.
//
// web/src/stakeholder/App.tsx reads `window.__ORCH_SNAPSHOT__` first and falls
// back to `fetch('./data.json')`. Both are written, always, because they cover
// different ways of opening the same folder: the fetch works over http, and
// under `file://` it is blocked by the browser's cross-origin rule — where a
// `<script src>` is not, because that is script execution and not a network
// read. A client who was sent a zip opens index.html and it renders.
const (
	dataJSONName = "data.json"
	dataJSName   = "data.js"
)

// Options controls one export.
type Options struct {
	// Token, when non-empty, puts the site in a subdirectory of that name:
	// <dir>/<token>/index.html rather than <dir>/index.html.
	//
	// This is OBSCURITY, not authentication, and the difference matters
	// enough to say twice (see docs/CLI.md). Anyone holding the URL holds
	// the data, the path is in the host's access log, in the client's
	// browser history and in whatever chat window it was pasted into, and
	// nothing here can revoke it — rotating it means exporting again to a
	// new path and the old one keeps serving until it is deleted. It raises
	// the cost of a guess against a known host; it does not make the export
	// private. A snapshot that must be private belongs behind the
	// dashboard's stakeholder token, which is a real gate.
	Token string

	// Index overrides the landing page's filename. Empty means index.html,
	// which is what every static host serves for a bare directory.
	Index string
}

// Result describes what an Export wrote.
type Result struct {
	// Dir is the directory the page itself landed in — the same as the dir
	// passed to Export, unless a Token moved it into a subdirectory.
	Dir string
	// URLPath is what to append to the host's base URL to reach the page:
	// "/" normally, "/<token>/" with a token. Printed, so the operator can
	// paste it rather than reconstruct it.
	URLPath string
	// Digest identifies the DATA, not the export: see Digest.
	Digest string
	// Files is how many files were written, the bundle included.
	Files int
}

// Digest is the fingerprint of a snapshot's content, ignoring when it was
// generated.
//
// `--watch` re-exports only when this changes, and `generated_at` moves on
// every build by definition — hashing the document whole would make every
// tick a change, which for `--to git` means a commit per interval on a branch
// whose whole history would then be timestamps. What a viewer would see is
// what decides whether to publish.
func Digest(snap snapshot.Snapshot) string {
	snap.GeneratedAt = ""
	b, err := json.Marshal(snap)
	if err != nil {
		// Snapshot is a plain data struct with no channels, funcs or NaNs —
		// json.Marshal cannot fail on it. Returning a value that never
		// matches is still better than a panic in a watch loop.
		return "unmarshalable"
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Export writes the static stakeholder site into dir, creating it if needed.
//
// The result is self-contained: the page, its assets, the data in both
// spellings, and a robots.txt. Nothing it renders is fetched from anywhere —
// the logo is already a data: URI by the time the snapshot carries it — so the
// directory can be zipped, copied to a bucket, or committed to a branch and it
// still works.
//
// Existing files are overwritten and unknown ones are left alone. Export does
// not empty the directory: `--to dir` pointed at somebody's home folder by a
// missing argument should not be a delete, and the git destination does its
// own cleaning where the tree is one it controls.
func Export(dir string, snap snapshot.Snapshot, opts Options) (Result, error) {
	bundle, err := Bundle()
	if err != nil {
		return Result{}, fmt.Errorf("reading the embedded stakeholder bundle: %w", err)
	}
	return exportFrom(bundle, dir, snap, opts)
}

// exportFrom is Export with the bundle passed in.
//
// Split for the tests, and only for the tests: the embedded bundle does not
// exist until `pnpm build:stakeholder` has run, and an exporter whose every
// test needs a Node toolchain is an exporter nobody runs the tests of. The
// integration test still uses the real bundle — that is the point of it — but
// the rules about tokens, robots.txt and the injected tag are checked against
// a fixture that is readable in the test file.
func exportFrom(bundle fs.FS, dir string, snap snapshot.Snapshot, opts Options) (Result, error) {
	if _, err := fs.Stat(bundle, bundleIndexName); err != nil {
		return Result{}, fmt.Errorf(
			"the stakeholder bundle has not been built — run `make web` "+
				"(or `pnpm build:stakeholder` in web/) before publishing: %w", err)
	}

	site := dir
	urlPath := "/"
	if opts.Token != "" {
		if err := validToken(opts.Token); err != nil {
			return Result{}, err
		}
		site = filepath.Join(dir, opts.Token)
		urlPath = "/" + opts.Token + "/"
	}
	// #nosec G301 -- a public static site: the directory has to be traversable
	// by whatever user the web server runs as, which 0750 does not allow.
	if err := os.MkdirAll(site, 0o755); err != nil {
		return Result{}, fmt.Errorf("creating %s: %w", site, err)
	}

	indexName := opts.Index
	if indexName == "" {
		indexName = "index.html"
	}

	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return Result{}, fmt.Errorf("encoding the snapshot: %w", err)
	}

	res := Result{Dir: site, URLPath: urlPath, Digest: Digest(snap)}

	// robots.txt goes at the EXPORT root, not beside the page: a crawler
	// only ever reads /robots.txt at the origin root, so a copy inside the
	// token directory would be a file nothing requests. With a token the
	// root holds this one file and nothing else — which is also the only
	// thing a visitor to the bare URL finds.
	if err := writeFile(filepath.Join(dir, "robots.txt"), []byte(robotsTXT)); err != nil {
		return Result{}, err
	}
	res.Files++

	// The bundle goes down FIRST and the data on top of it. The order is not
	// cosmetic: `web/src/stakeholder/public/` holds a data.json of example
	// figures so `pnpm dev:stakeholder` has something to render, vite copies
	// publicDir into the build, and a copy that ran last would hand a client
	// the demo project instead of their own. copyBundle also refuses to
	// carry those two names at all — belt and braces, because what they
	// guard against is silent and entirely plausible.
	n, err := copyBundle(bundle, site, indexName)
	if err != nil {
		return Result{}, err
	}
	res.Files += n

	if err := writeFile(filepath.Join(site, dataJSONName), data); err != nil {
		return Result{}, err
	}
	res.Files++

	// A plain assignment, not JSON.parse of a string: the payload is already
	// valid JavaScript, and a `<script>` body is not HTML-escaped by the
	// parser, so the one thing that could break out of it is the literal
	// "</script>" — which cannot appear, because json.Marshal escapes `<` as
	// the six characters \\u003c inside a string.
	js := append([]byte("window.__ORCH_SNAPSHOT__ = "), data...)
	js = append(js, ";\n"...)
	if err := writeFile(filepath.Join(site, dataJSName), js); err != nil {
		return Result{}, err
	}
	res.Files++

	return res, nil
}

// copyBundle copies every file of the built SPA into site, renaming the entry
// page to indexName and giving it the data.js tag it does not ship with.
func copyBundle(bundle fs.FS, site, indexName string) (int, error) {
	var n int
	err := fs.WalkDir(bundle, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p == "." {
				return nil
			}
			// #nosec G301 -- see the site directory in exportFrom.
			return os.MkdirAll(filepath.Join(site, filepath.FromSlash(p)), 0o755)
		}

		// The dev fixture never leaves the bundle. It is there so the page
		// renders under `pnpm dev:stakeholder`; in an export it would be a
		// stranger's figures on a client's page.
		if p == dataJSONName || p == dataJSName {
			return nil
		}

		b, err := fs.ReadFile(bundle, p)
		if err != nil {
			return fmt.Errorf("reading %s from the bundle: %w", p, err)
		}

		dst := filepath.Join(site, filepath.FromSlash(p))
		if p == bundleIndexName {
			b, err = injectDataScript(b)
			if err != nil {
				return err
			}
			dst = filepath.Join(site, indexName)
		}
		if err := writeFile(dst, b); err != nil {
			return err
		}
		n++
		return nil
	})
	return n, err
}

// dataScriptTag is what injectDataScript inserts.
var dataScriptTag = []byte(`<script src="./` + dataJSName + `"></script>`)

// injectDataScript puts the data.js tag into the built HTML.
//
// The bundle cannot ship the tag itself: `web/stakeholder.html` is also what
// `pnpm dev:stakeholder` serves, where data.js does not exist and the page
// reads its example data through the fetch path instead. So the tag is added
// by whoever produces the artefact that has a data.js next to it, which is
// this function.
//
// Placed before the first <script>, which in a vite build is the module tag in
// <head>. Order is belt and braces — a module script is deferred, so a classic
// one anywhere in the document runs first either way — but "the data is there
// before the app looks for it" should be visible in the HTML rather than be a
// fact about module semantics somebody has to know.
func injectDataScript(html []byte) ([]byte, error) {
	at := strings.Index(string(html), "<script")
	if at < 0 {
		at = strings.Index(string(html), "</head>")
	}
	if at < 0 {
		// Not a page we recognise. Writing it anyway would produce a site
		// that renders the loading skeleton forever, which is a worse
		// failure than this one.
		return nil, fmt.Errorf(
			"the stakeholder bundle's %s has neither a <script> tag nor a </head> "+
				"to put the snapshot before — the bundle looks wrong, not the export",
			bundleIndexName)
	}
	out := make([]byte, 0, len(html)+len(dataScriptTag)+1)
	out = append(out, html[:at]...)
	out = append(out, dataScriptTag...)
	out = append(out, '\n')
	out = append(out, html[at:]...)
	return out, nil
}

func writeFile(dst string, b []byte) error {
	// #nosec G301 -- see the site directory in exportFrom.
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(dst), err)
	}
	if err := os.WriteFile(dst, b, 0o644); err != nil { // #nosec G306 -- a public static site
		return fmt.Errorf("writing %s: %w", dst, err)
	}
	return nil
}

// validToken rejects anything that would not be one path segment.
//
// The token becomes a directory name and a URL path, so a `../` in it is a
// write outside the export directory. Checked rather than sanitised: a token
// silently rewritten is a URL the operator does not have.
func validToken(tok string) error {
	if tok != path.Clean(tok) || strings.ContainsAny(tok, `/\`) ||
		tok == "." || tok == ".." || strings.HasPrefix(tok, ".") {
		return fmt.Errorf("--token must be a single path segment with no slashes: %q", tok)
	}
	return nil
}

// copyTree copies a directory recursively. Used by the git destination, which
// stages an already-written export into a checkout.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		in, err := os.Open(p) // #nosec G304 G122 -- walking a directory this process just wrote, inside a temporary checkout it owns
		if err != nil {
			return err
		}
		defer func() { _ = in.Close() }()
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return err
		}
		out, err := os.Create(target) // #nosec G304 -- inside a temporary checkout
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			_ = out.Close()
			return err
		}
		return out.Close()
	})
}
