package publish

import (
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/hectorcanaimero/orch/internal/publish/snapshot"
)

// fakeBundle is shaped like what vite actually emits: the entry page named
// after its input, a hashed asset under assets/, and a file copied from
// publicDir at the root.
func fakeBundle() fs.FS {
	return fstest.MapFS{
		bundleIndexName: &fstest.MapFile{Data: []byte(
			`<!doctype html><html><head><meta charset="UTF-8" />` +
				`<script type="module" crossorigin src="./assets/stakeholder-abc123.js"></script>` +
				`</head><body><div id="root"></div></body></html>`)},
		"assets/stakeholder-abc123.js": &fstest.MapFile{Data: []byte("console.log(1)\n")},
		"favicon.svg":                  &fstest.MapFile{Data: []byte("<svg/>")},
		// vite copies publicDir into the build, and publicDir holds the
		// example snapshot `pnpm dev:stakeholder` renders. A real bundle
		// therefore ships a data.json of somebody else's numbers.
		dataJSONName: &fstest.MapFile{Data: []byte(`{"project_name":"EJEMPLO DE DESARROLLO"}`)},
	}
}

func testSnapshot() snapshot.Snapshot {
	return snapshot.Snapshot{
		Schema:      snapshot.Schema,
		GeneratedAt: "2026-09-12T10:00:00Z",
		ProjectName: "Cliente Ejemplo",
		Summary:     snapshot.Summary{Total: 10, Done: 4, PercentDone: 40},
	}
}

func TestExportWritesASelfContainedSite(t *testing.T) {
	dir := t.TempDir()
	res, err := exportFrom(fakeBundle(), dir, testSnapshot(), Options{})
	if err != nil {
		t.Fatalf("exportFrom: %v", err)
	}
	if res.Dir != dir || res.URLPath != "/" {
		t.Errorf("Dir/URLPath = %q/%q, want %q//", res.Dir, res.URLPath, dir)
	}

	// The landing page is index.html, NOT the bundle's stakeholder.html: a
	// directory whose entry point is stakeholder.html 404s at "/".
	for _, want := range []string{
		"index.html", "data.json", "data.js", "robots.txt",
		filepath.Join("assets", "stakeholder-abc123.js"), "favicon.svg",
	} {
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Errorf("missing from the export: %s (%v)", want, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, bundleIndexName)); err == nil {
		t.Errorf("%s should have been renamed to index.html, not copied alongside it", bundleIndexName)
	}
}

func TestExportInjectsTheDataScriptBeforeTheBundle(t *testing.T) {
	dir := t.TempDir()
	if _, err := exportFrom(fakeBundle(), dir, testSnapshot(), Options{}); err != nil {
		t.Fatalf("exportFrom: %v", err)
	}
	html := read(t, filepath.Join(dir, "index.html"))

	dataAt := strings.Index(html, `src="./data.js"`)
	appAt := strings.Index(html, "stakeholder-abc123.js")
	if dataAt < 0 {
		t.Fatalf("index.html has no data.js tag:\n%s", html)
	}
	if appAt >= 0 && dataAt > appAt {
		t.Errorf("data.js must come before the app bundle so the snapshot is there when it looks:\n%s", html)
	}
}

// The two spellings must carry the same document — App.tsx reads whichever is
// available and a viewer must not see a different project depending on how the
// page was opened.
func TestExportWritesTheSameDataBothWays(t *testing.T) {
	dir := t.TempDir()
	snap := testSnapshot()
	if _, err := exportFrom(fakeBundle(), dir, snap, Options{}); err != nil {
		t.Fatalf("exportFrom: %v", err)
	}

	var fromJSON snapshot.Snapshot
	if err := json.Unmarshal([]byte(read(t, filepath.Join(dir, "data.json"))), &fromJSON); err != nil {
		t.Fatalf("data.json does not parse: %v", err)
	}
	if fromJSON.ProjectName != snap.ProjectName {
		t.Errorf("data.json project = %q, want %q", fromJSON.ProjectName, snap.ProjectName)
	}

	js := read(t, filepath.Join(dir, "data.js"))
	const prefix = "window.__ORCH_SNAPSHOT__ = "
	if !strings.HasPrefix(js, prefix) {
		t.Fatalf("data.js does not assign the global App.tsx reads:\n%s", js)
	}
	var fromJS snapshot.Snapshot
	body := strings.TrimSuffix(strings.TrimSpace(strings.TrimPrefix(js, prefix)), ";")
	if err := json.Unmarshal([]byte(body), &fromJS); err != nil {
		t.Fatalf("data.js payload does not parse: %v", err)
	}
	if Digest(fromJS) != Digest(fromJSON) {
		t.Error("data.js and data.json carry different documents")
	}
}

func TestExportRobotsTxtDisallowsEverything(t *testing.T) {
	dir := t.TempDir()
	if _, err := exportFrom(fakeBundle(), dir, testSnapshot(), Options{}); err != nil {
		t.Fatalf("exportFrom: %v", err)
	}
	got := read(t, filepath.Join(dir, "robots.txt"))
	if !strings.Contains(got, "Disallow: /") {
		t.Errorf("robots.txt = %q, want a blanket Disallow", got)
	}
}

func TestExportTokenMovesTheSiteIntoASubdirectory(t *testing.T) {
	dir := t.TempDir()
	res, err := exportFrom(fakeBundle(), dir, testSnapshot(), Options{Token: "hidden-dir"})
	if err != nil {
		t.Fatalf("exportFrom: %v", err)
	}
	if res.URLPath != "/hidden-dir/" {
		t.Errorf("URLPath = %q, want /hidden-dir/", res.URLPath)
	}
	if _, err := os.Stat(filepath.Join(dir, "hidden-dir", "index.html")); err != nil {
		t.Errorf("the page is not under the token directory: %v", err)
	}
	// Nothing at the bare root but robots.txt — a visitor who guesses the
	// host and not the path finds no page and no data.
	if _, err := os.Stat(filepath.Join(dir, "index.html")); err == nil {
		t.Error("a token export must not also leave a page at the root")
	}
	if _, err := os.Stat(filepath.Join(dir, "data.json")); err == nil {
		t.Error("a token export must not leave the data at the root")
	}
	if _, err := os.Stat(filepath.Join(dir, "robots.txt")); err != nil {
		t.Errorf("robots.txt belongs at the origin root, where a crawler reads it: %v", err)
	}
}

// A token is a path segment. Checked rather than sanitised, because a token
// silently rewritten is a URL the operator does not have — and `../..` would
// write outside the export directory entirely.
func TestExportRejectsATokenThatIsNotOneSegment(t *testing.T) {
	for _, tok := range []string{"../escape", "a/b", `a\b`, ".", "..", ".hidden"} {
		if _, err := exportFrom(fakeBundle(), t.TempDir(), testSnapshot(), Options{Token: tok}); err == nil {
			t.Errorf("token %q was accepted", tok)
		}
	}
}

func TestExportRefusesAnUnbuiltBundle(t *testing.T) {
	_, err := exportFrom(fstest.MapFS{}, t.TempDir(), testSnapshot(), Options{})
	if err == nil {
		t.Fatal("an empty bundle should not export")
	}
	if !strings.Contains(err.Error(), "make web") {
		t.Errorf("the error should say how to fix it, got: %v", err)
	}
}

// TestDigestIgnoresGeneratedAt is what keeps `--watch --to git` from making a
// commit every interval.
func TestDigestIgnoresGeneratedAt(t *testing.T) {
	a := testSnapshot()
	b := testSnapshot()
	b.GeneratedAt = "2099-01-01T00:00:00Z"
	if Digest(a) != Digest(b) {
		t.Error("two snapshots that say the same thing at different times must have the same digest")
	}

	c := testSnapshot()
	c.Summary.Done = 5
	if Digest(a) == Digest(c) {
		t.Error("a snapshot with different content must have a different digest")
	}
}

// TestExportedSiteServesOverHTTP is the acceptance criterion: export, serve the
// directory the way a static host would, and check what a browser receives.
//
// It uses the REAL embedded bundle, not the fixture — the point is that what
// `pnpm build:stakeholder` produces and what Export does to it add up to a
// working page. It skips rather than fails when the bundle has not been built,
// because TestBundleRequiresABuild already says that in one clear sentence and
// five copies of it is noise.
func TestExportedSiteServesOverHTTP(t *testing.T) {
	bundle, err := Bundle()
	if err != nil {
		t.Fatalf("Bundle(): %v", err)
	}
	if _, err := fs.Stat(bundle, bundleIndexName); err != nil {
		t.Skip("stakeholder bundle not built; see TestBundleRequiresABuild")
	}

	dir := t.TempDir()
	snap := testSnapshot()
	if _, err := Export(dir, snap, Options{}); err != nil {
		t.Fatalf("Export: %v", err)
	}

	srv := httptest.NewServer(http.FileServer(http.Dir(dir)))
	defer srv.Close()

	// "/" must serve the page. This is the assertion the stakeholder.html
	// rename exists for: without it a bare directory URL is a 404 or a file
	// listing.
	html := get(t, srv.URL+"/")
	if !strings.Contains(html, `id="root"`) {
		t.Errorf("GET / did not serve the stakeholder page:\n%s", truncate(html))
	}
	if !strings.Contains(html, `src="./data.js"`) {
		t.Errorf("GET / served a page with no snapshot attached:\n%s", truncate(html))
	}

	// Every asset the page references must resolve against the same
	// directory — that is what `base: './'` in the stakeholder vite config
	// buys, and a regression there produces a blank page with a 404 in the
	// console rather than a failing build.
	for _, src := range assetSrcs(html) {
		res, err := http.Get(srv.URL + "/" + strings.TrimPrefix(src, "./"))
		if err != nil {
			t.Fatalf("GET %s: %v", src, err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Errorf("the page references %s, which the export does not serve (%d)", src, res.StatusCode)
		}
	}

	var served snapshot.Snapshot
	if err := json.Unmarshal([]byte(get(t, srv.URL+"/data.json")), &served); err != nil {
		t.Fatalf("GET /data.json does not parse: %v", err)
	}
	if served.ProjectName != snap.ProjectName {
		t.Errorf("served project = %q, want %q", served.ProjectName, snap.ProjectName)
	}
	if got := get(t, srv.URL+"/robots.txt"); !strings.Contains(got, "Disallow: /") {
		t.Errorf("GET /robots.txt = %q", got)
	}
}

// assetSrcs pulls the src="..." values out of the served HTML.
func assetSrcs(html string) []string {
	var out []string
	rest := html
	for {
		at := strings.Index(rest, `src="`)
		if at < 0 {
			return out
		}
		rest = rest[at+len(`src="`):]
		end := strings.Index(rest, `"`)
		if end < 0 {
			return out
		}
		if v := rest[:end]; strings.HasPrefix(v, "./") || !strings.Contains(v, "//") {
			out = append(out, v)
		}
		rest = rest[end:]
	}
}

func get(t *testing.T, url string) string {
	t.Helper()
	res, err := http.Get(url) // #nosec G107 -- httptest's own URL
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", url, res.StatusCode)
	}
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("reading %s: %v", url, err)
	}
	return string(b)
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) // #nosec G304 -- a path this test just wrote
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(b)
}

func truncate(s string) string {
	if len(s) > 400 {
		return s[:400] + "…"
	}
	return s
}

// The bundle ships a dev-fixture data.json (vite copies publicDir into the
// build). An export that carried it would show a client the demo project's
// figures — a wrong page that looks entirely right, which is the worst shape a
// bug can have here.
func TestExportNeverServesTheDevFixture(t *testing.T) {
	dir := t.TempDir()
	if _, err := exportFrom(fakeBundle(), dir, testSnapshot(), Options{}); err != nil {
		t.Fatalf("exportFrom: %v", err)
	}
	got := read(t, filepath.Join(dir, "data.json"))
	if strings.Contains(got, "EJEMPLO DE DESARROLLO") {
		t.Errorf("the export serves the bundle's development fixture:\n%s", got)
	}
	if !strings.Contains(got, "Cliente Ejemplo") {
		t.Errorf("the export does not carry the real snapshot:\n%s", got)
	}
}
