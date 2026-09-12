package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// The fallback is what makes a client-side route survive a refresh: /kanban is
// not a file in the build, so a plain file server 404s every deep link.
func TestSPAHandlerServesFilesAndFallsBackToTheShell(t *testing.T) {
	h := SPAHandler(staticFS())

	for _, tc := range []struct {
		path string
		want string
		why  string
	}{
		{"/", "<!doctype html>", "the root is the shell"},
		{"/assets/index-abc123.js", "console.log", "a real file is served as itself"},
		{"/kanban", "<!doctype html>", "a client-side route falls back to the shell"},
		{"/deep/link/that/does/not/exist", "<!doctype html>", "however deep"},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200 (%s)", tc.path, rec.Code, tc.why)
			continue
		}
		if !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("%s: body = %q, want it to contain %q (%s)",
				tc.path, rec.Body.String(), tc.want, tc.why)
		}
	}

	// `/index.html` by name is the one path that is NOT a 200: http.FileServer
	// redirects it to `/` with a 301, which is its canonicalisation and not
	// something this handler decides. Asserted so the next reader does not
	// take it for a bug in the fallback.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/index.html", nil))
	if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "./" {
		t.Errorf("/index.html = %d %q, want a 301 to ./ (http.FileServer's own canonicalisation)",
			rec.Code, rec.Header().Get("Location"))
	}
}

// Only GET and HEAD fall back. A POST to a path that does not exist is a
// caller doing something wrong, and answering it with a page of HTML would
// turn a 404 into a confusing 200.
func TestSPAHandlerDoesNotFallBackForWrites(t *testing.T) {
	h := SPAHandler(staticFS())
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, "/kanban", nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s /kanban: status = %d, want 404", method, rec.Code)
		}
	}
}

// HEAD gets the headers and no body, which is what HEAD means.
func TestSPAHandlerHeadHasNoBody(t *testing.T) {
	rec := httptest.NewRecorder()
	SPAHandler(staticFS()).ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/kanban", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("HEAD returned %d bytes of body", rec.Body.Len())
	}
}

// An embedded SPA with no index.html is a build that never ran — `make web`
// not done, or done into the wrong directory. It answers 500 and says which,
// because the alternative is a blank 200 that reads like the client-side
// router losing a route, and that sends the reader looking at the wrong half
// of the system.
func TestSPAHandlerWithNoShellSaysSo(t *testing.T) {
	// A build tree with assets and no index.html: exactly what a half-copied
	// dist/ looks like.
	files := fstest.MapFS{"assets/index-abc123.js": {Data: []byte("console.log(1)")}}
	h := SPAHandler(files)

	// The file that IS there still serves — the failure is local to the
	// fallback, not to the whole handler.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/index-abc123.js", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("a real file answered %d in a tree with no shell", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/kanban", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 — a blank 200 would read as a routing bug", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "make web") {
		t.Errorf("body = %q; it must name the command that fixes it", rec.Body.String())
	}
}
