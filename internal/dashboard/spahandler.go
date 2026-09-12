package dashboard

import (
	"io/fs"
	"net/http"
	"strings"
)

// SPAHandler serves the built SPA: the file when it exists, the shell when it
// does not.
//
// The fallback is what makes client-side routing work. `/kanban` is not a file
// in the build — it is a route React resolves once the shell has loaded — so a
// plain file server answers 404 for every deep link and for every refresh on a
// page the user navigated to. Serving index.html instead hands the URL to the
// router, which is what a static host's "SPA mode" does.
//
// Only GET and HEAD fall back. A POST to a path that does not exist is a
// caller doing something wrong, and answering it with a page of HTML would
// turn that into a confusing 200 instead of a 404.
//
// This handler is deliberately NOT gated — see access.go for why the static
// surface is public by default and what bug that prevents.
func SPAHandler(files fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(files))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		if _, err := fs.Stat(files, name); err == nil {
			fileServer.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}

		body, err := fs.ReadFile(files, "index.html")
		if err != nil {
			// An embedded SPA with no index.html is a build that never ran.
			// Saying so beats a blank 200, which reads like the client-side
			// router losing a route.
			http.Error(w, "no index.html in the embedded SPA — run `make web`",
				http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write(body)
	})
}
