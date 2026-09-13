// Package cloudfake is an in-memory orch-cloud Worker for tests.
//
// It implements the contract in docs/CLOUD.md — hash-stored tokens, the
// manifest/blob split, the digest no-op, the JSON error bodies and the
// viewer routes — so the client in internal/publish is tested over real
// HTTP against the rules the Worker follows, not against canned responses
// that agree with whatever the client happens to send. Nothing outside
// tests imports it.
package cloudfake

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"regexp"
	"strings"
	"sync"
)

const (
	maxBody  = 10 << 20
	maxFile  = 5 << 20
	maxFiles = 200
)

var projectID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

type manifest struct {
	Version int
	Digest  string
	Files   map[string]string // path -> blob hash
}

type project struct {
	publishHash string
	viewHash    string
	site        *manifest
}

// Worker is the fake. Its fields are guarded by mu; read them through the
// accessor methods.
type Worker struct {
	*httptest.Server

	// AdminToken is the Worker's ADMIN_TOKEN secret.
	AdminToken string

	mu             sync.Mutex
	adminTokenFile string
	projects       map[string]*project
	views          map[string]string // view hash -> project id
	blobs          map[string][]byte
	requests       int
}

// New starts a fake Worker with a random admin token. Close it when done.
func New() *Worker {
	w := &Worker{
		AdminToken: Token(),
		projects:   map[string]*project{},
		views:      map[string]string{},
		blobs:      map[string][]byte{},
	}
	w.Server = httptest.NewServer(http.HandlerFunc(w.serve))
	return w
}

// Token is a random 32-byte hex token, the contract's shape. Tests use it
// instead of literals, so no test file carries a string that looks like a
// credential.
func Token() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// Requests is how many HTTP requests the fake has received.
func (w *Worker) Requests() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.requests
}

// HasProject reports whether id is registered.
func (w *Worker) HasProject(id string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, ok := w.projects[id]
	return ok
}

// SiteVersion is the project's current site version, 0 before any upload.
func (w *Worker) SiteVersion(id string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	if p, ok := w.projects[id]; ok && p.site != nil {
		return p.site.Version
	}
	return 0
}

// SiteFiles is the path list of the project's current site.
func (w *Worker) SiteFiles(id string) []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	p, ok := w.projects[id]
	if !ok || p.site == nil {
		return nil
	}
	out := make([]string, 0, len(p.site.Files))
	for f := range p.site.Files {
		out = append(out, f)
	}
	return out
}

// StoresToken reports whether token appears anywhere in the fake's storage
// in the clear — it never should.
func (w *Worker) StoresToken(token string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	for id, p := range w.projects {
		if id == token || p.publishHash == token || p.viewHash == token {
			return true
		}
	}
	for h := range w.views {
		if h == token {
			return true
		}
	}
	return false
}

func writeErr(rw http.ResponseWriter, status int, code, msg string) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)
	_ = json.NewEncoder(rw).Encode(map[string]string{"error": code, "message": msg})
}

func writeJSON(rw http.ResponseWriter, status int, v any) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)
	_ = json.NewEncoder(rw).Encode(v)
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if tok, ok := strings.CutPrefix(h, "Bearer "); ok {
		return tok
	}
	return ""
}

func (w *Worker) serve(rw http.ResponseWriter, r *http.Request) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.requests++

	p := r.URL.Path
	switch {
	case p == "/robots.txt":
		rw.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(rw, "User-agent: *\nDisallow: /\n")
	case strings.HasPrefix(p, "/v/"):
		w.serveViewer(rw, r)
	case p == "/api/v1/health":
		writeJSON(rw, 200, map[string]any{"ok": true, "version": "fake", "api": 1})
	case p == "/api/v1/whoami":
		if !w.isAdmin(r) {
			writeErr(rw, 401, "unauthorized", "admin token required")
			return
		}
		writeJSON(rw, 200, map[string]string{"role": "admin"})
	case p == "/api/v1/projects":
		w.createProject(rw, r)
	case strings.HasPrefix(p, "/api/v1/projects/"):
		w.projectRoute(rw, r, strings.TrimPrefix(p, "/api/v1/projects/"))
	default:
		writeErr(rw, 404, "not_found", "no such route")
	}
}

func (w *Worker) isAdmin(r *http.Request) bool {
	if w.adminTokenFile != "" {
		// Read on every request: the file is written by a fake `wrangler secret
		// put` while the Worker is running, the way a real secret appears some
		// seconds after the command returns. No file yet means no admin.
		raw, err := os.ReadFile(w.adminTokenFile) // #nosec G304 -- a test-owned path
		if err != nil {
			return false
		}
		tok := strings.TrimSpace(string(raw))
		return tok != "" && hash(bearer(r)) == hash(tok)
	}
	return hash(bearer(r)) == hash(w.AdminToken)
}

// SetAdminTokenFile makes the Worker read its admin token from path on every
// request instead of using AdminToken — for `orch cloud setup` tests, where
// the token is generated by orch and handed to the Worker by a fake wrangler.
func (w *Worker) SetAdminTokenFile(path string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.adminTokenFile = path
}

func (w *Worker) createProject(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(rw, 405, "method_not_allowed", "POST only")
		return
	}
	if !w.isAdmin(r) {
		writeErr(rw, 401, "unauthorized", "admin token required")
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBody)).Decode(&body); err != nil || !projectID.MatchString(body.ID) {
		writeErr(rw, 400, "invalid", "invalid project id")
		return
	}
	if _, ok := w.projects[body.ID]; ok {
		writeErr(rw, 409, "conflict", "project exists")
		return
	}
	pub, view := Token(), Token()
	w.projects[body.ID] = &project{publishHash: hash(pub), viewHash: hash(view)}
	w.views[hash(view)] = body.ID
	writeJSON(rw, 201, map[string]string{"id": body.ID, "publish_token": pub, "view_token": view})
}

func (w *Worker) projectRoute(rw http.ResponseWriter, r *http.Request, rest string) {
	id, action, _ := strings.Cut(rest, "/")
	proj, ok := w.projects[id]

	switch {
	case action == "" && r.Method == http.MethodDelete:
		if !w.isAdmin(r) {
			writeErr(rw, 401, "unauthorized", "admin token required")
			return
		}
		if !ok {
			writeErr(rw, 404, "not_found", "no such project")
			return
		}
		delete(w.views, proj.viewHash)
		delete(w.projects, id)
		rw.WriteHeader(204)
	case action == "publish-token" && r.Method == http.MethodPost:
		if !w.isAdmin(r) {
			writeErr(rw, 401, "unauthorized", "admin token required")
			return
		}
		if !ok {
			writeErr(rw, 404, "not_found", "no such project")
			return
		}
		pub := Token()
		proj.publishHash = hash(pub)
		writeJSON(rw, 200, map[string]string{"publish_token": pub})
	case action == "view-token" && r.Method == http.MethodPost:
		if !ok || hash(bearer(r)) != proj.publishHash {
			writeErr(rw, 401, "unauthorized", "publish token required")
			return
		}
		view := Token()
		delete(w.views, proj.viewHash)
		proj.viewHash = hash(view)
		w.views[proj.viewHash] = id
		writeJSON(rw, 200, map[string]string{"view_token": view})
	case action == "site" && r.Method == http.MethodPut:
		if !ok || hash(bearer(r)) != proj.publishHash {
			writeErr(rw, 401, "unauthorized", "publish token required")
			return
		}
		w.putSite(rw, r, proj)
	default:
		writeErr(rw, 405, "method_not_allowed", "unsupported method or route")
	}
}

func (w *Worker) putSite(rw http.ResponseWriter, r *http.Request, proj *project) {
	data, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		writeErr(rw, 400, "invalid", "unreadable body")
		return
	}
	if len(data) > maxBody {
		writeErr(rw, 413, "too_large", "body over 10 MiB")
		return
	}
	var body struct {
		Digest string            `json:"digest"`
		Files  map[string]string `json:"files"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		writeErr(rw, 400, "invalid", "body is not JSON")
		return
	}
	if len(body.Files) > maxFiles {
		writeErr(rw, 413, "too_large", "too many files")
		return
	}
	if _, ok := body.Files["index.html"]; !ok {
		writeErr(rw, 400, "invalid", "index.html is required")
		return
	}
	if proj.site != nil && proj.site.Digest == body.Digest {
		writeJSON(rw, 200, map[string]any{"changed": false, "version": proj.site.Version})
		return
	}
	next := &manifest{Digest: body.Digest, Files: map[string]string{}}
	decoded := map[string][]byte{}
	for p, b64 := range body.Files {
		if p == "" || strings.HasPrefix(p, "/") || path.Clean(p) != p || strings.Contains(p, "..") {
			writeErr(rw, 400, "invalid", "bad path "+p)
			return
		}
		b, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			writeErr(rw, 400, "invalid", "bad base64 for "+p)
			return
		}
		if len(b) > maxFile {
			writeErr(rw, 413, "too_large", p+" over 5 MiB")
			return
		}
		decoded[p] = b
	}
	for p, b := range decoded {
		sum := sha256.Sum256(b)
		h := hex.EncodeToString(sum[:])
		w.blobs[h] = b
		next.Files[p] = h
	}
	next.Version = 1
	if proj.site != nil {
		next.Version = proj.site.Version + 1
	}
	proj.site = next
	writeJSON(rw, 200, map[string]any{"changed": true, "version": next.Version})
}

func (w *Worker) serveViewer(rw http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v/")
	token, file, hasSlash := strings.Cut(rest, "/")
	notFound := func() { http.Error(rw, "Not Found", http.StatusNotFound) }

	id, ok := w.views[hash(token)]
	if !ok {
		notFound()
		return
	}
	if !hasSlash {
		// #nosec G710 -- a same-origin path built from a token that just
		// matched a stored hash; a test fake, never deployed.
		http.Redirect(rw, r, "/v/"+token+"/", http.StatusPermanentRedirect)
		return
	}
	if file == "" {
		file = "index.html"
	}
	proj := w.projects[id]
	if proj.site == nil {
		notFound()
		return
	}
	h, ok := proj.site.Files[file]
	if !ok {
		notFound()
		return
	}
	rw.Header().Set("Referrer-Policy", "no-referrer")
	rw.Header().Set("X-Robots-Tag", "noindex, nofollow")
	_, _ = rw.Write(w.blobs[h])
}
