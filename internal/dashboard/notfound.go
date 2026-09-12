package dashboard

import "net/http"

// notFoundPrefixes are the path prefixes under which an unclaimed route is a
// 404 rather than the SPA.
//
// Both are data prefixes: everything under them is fetched by code, never
// typed by a person, so the answer has to be machine-readable. `/stakeholder/`
// is here as well as `/api/` because the landing page's own endpoint lives
// there — see `stakeholder_summary_json` in the Python server, which the Go
// port has not wired yet.
//
// Deliberately NOT "/": the SPA owns every other path, and client-side routes
// like `/kanban` must keep reaching it on a hard refresh.
var notFoundPrefixes = []string{"/api/", "/stakeholder/"}

// apiNotFoundPayload is the body. `path` is echoed because this is the
// operator's own machine and the likely cause is a route the port has not
// reached yet — the reticence `writePlain` shows on the gated routes is about
// not confirming which routes exist to an unauthenticated caller, and there is
// nothing to confirm about one that exists nowhere.
type apiNotFoundPayload struct {
	Error string `json:"error"`
	Path  string `json:"path"`
}

func handleAPINotFound(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusNotFound, apiNotFoundPayload{
		Error: "not found",
		Path:  r.URL.Path,
	})
}
