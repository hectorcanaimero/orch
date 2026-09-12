package dashboard

import (
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/hectorcanaimero/orch/internal/config"
)

// staticFS mirrors what a real Vite build puts in dist: the shell, the hashed
// bundle under assets/, AND the files copied from web/public straight to the
// root.
//
// That last group is the point. sonnet-2 ran a real build to find them, and
// they are why this package has no allow-list of static paths: `favicon.svg`,
// `manifest.json` and the PWA icons sit beside index.html, nowhere near
// `/assets/`. Any list of "the static paths" is a list somebody has to keep in
// step with whatever web/public happens to hold.
func staticFS() fs.FS {
	return fstest.MapFS{
		"index.html":              {Data: []byte("<!doctype html><div id=root>")},
		"assets/index-abc123.js":  {Data: []byte("console.log(1)")},
		"assets/index-abc123.css": {Data: []byte("body{}")},
		"favicon.svg":             {Data: []byte("<svg/>")},
		"manifest.json":           {Data: []byte(`{"name":"orch"}`)},
		"icon-192.png":            {Data: []byte("\x89PNG")},
		"icon-512.png":            {Data: []byte("\x89PNG")},
		"icons.svg":               {Data: []byte("<svg/>")},
	}
}

// spaHandler is a stand-in for what spa.go will expose: serve the file when it
// exists, fall back to the shell on GET/HEAD when it does not.
func spaHandler(t *testing.T) http.Handler {
	t.Helper()
	files := staticFS()
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
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		body, _ := fs.ReadFile(files, "index.html")
		_, _ = w.Write(body)
	})
}

func newTestServer(t *testing.T, c Config, projectRoot string) *Server {
	t.Helper()
	s, err := New(c, Options{
		Static: spaHandler(t),
		Paths:  config.Paths{Root: projectRoot, ID: "p", ConfigYAML: filepath.Join(projectRoot, "config.yaml")},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func get(t *testing.T, s *Server, path string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec.Result()
}

// The test orch-98 asked for: with the SPA handler mounted, a stakeholder
// session reaches every static file with no token.
//
// This is #97 as a regression test. The Python middleware exempted the shell
// and `/assets/*`; a browser loaded the HTML, its `<script src>` 401'd, and
// the page rendered blank with a 200 already on the wire. The files at the
// root of the build are the half an allow-list would have missed.
func TestStaticFilesNeedNoToken(t *testing.T) {
	s := newTestServer(t, cfg(ProfileStakeholder, "test-token-stakeholder"), t.TempDir())

	for _, path := range []string{
		"/", "/index.html",
		"/assets/index-abc123.js", "/assets/index-abc123.css",
		"/favicon.svg", "/manifest.json", "/icon-192.png", "/icon-512.png", "/icons.svg",
		// A client-side route: no such file, so the shell comes back.
		"/kanban", "/metrics/detail",
	} {
		resp := get(t, s, path)
		// The property is "the gate did not refuse it", not "200". Go's
		// FileServer answers /index.html with a 301 to /, which a browser
		// follows and which says nothing about auth — asserting 200 would
		// make this test about http.FileServer's redirect policy instead.
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			t.Errorf("GET %s = %d with no token — a browser cannot put a token "+
				"on its own asset requests", path, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}
}

// And the data routes are still gated in the same server.
func TestDataRoutesAreGatedInTheSameServer(t *testing.T) {
	s := newTestServer(t, cfg(ProfileStakeholder, "test-token-stakeholder"), t.TempDir())

	resp := get(t, s, "/api/config/status")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET /api/config/status = %d with no token, want 401", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

// The static handler is reached by a path that looks like an API route but is
// not registered. It must serve the shell rather than 401 — a client-side
// route named /api-docs is a page, not an endpoint.
func TestUnregisteredPathFallsToTheSPA(t *testing.T) {
	s := newTestServer(t, cfg(ProfileStakeholder, "test-token-stakeholder"), t.TempDir())
	resp := get(t, s, "/api-docs")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /api-docs = %d, want the SPA shell", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

func TestWhoamiReportsTheProfile(t *testing.T) {
	for _, profile := range []Profile{ProfileOperator, ProfileBoth} {
		s := newTestServer(t, cfg(profile, ""), t.TempDir())
		resp := get(t, s, "/api/whoami")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /api/whoami = %d", resp.StatusCode)
		}
		var body whoamiPayload
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		_ = resp.Body.Close()
		if body.Profile != string(profile) {
			t.Errorf("profile = %q, want %q", body.Profile, profile)
		}
	}
}

// whoami is on the stakeholder allow-list, so a token gets you it and nothing
// more. The SPA reads it to hide operator-only navigation; the profile is not
// a secret, the token is.
func TestWhoamiIsReachableByAStakeholder(t *testing.T) {
	s := newTestServer(t, cfg(ProfileStakeholder, "test-token-stakeholder"), t.TempDir())

	req := httptest.NewRequest(http.MethodGet, "/api/whoami", nil)
	req.Header.Set("Authorization", "Bearer test-token-stakeholder")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("GET /api/whoami with a token = %d, want 200", rec.Code)
	}

	// config/status is not on the list: authenticated, still forbidden.
	req = httptest.NewRequest(http.MethodGet, "/api/config/status?token=test-token-stakeholder", nil)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("GET /api/config/status with a valid token = %d, want 403", rec.Code)
	}
}

// The token may arrive in a query parameter, because the stakeholder profile
// exists to make a shareable URL and a pasted link carries no header.
func TestTokenFromQueryParameter(t *testing.T) {
	s := newTestServer(t, cfg(ProfileStakeholder, "test-token-stakeholder"), t.TempDir())
	resp := get(t, s, "/api/whoami?token=test-token-stakeholder")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET with ?token= = %d, want 200", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

// A rejection says nothing but the word.
func TestRejectionsLeakNothing(t *testing.T) {
	s := newTestServer(t, cfg(ProfileStakeholder, "test-token-stakeholder"), t.TempDir())
	resp := get(t, s, "/api/config/status")
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if strings.TrimSpace(string(body)) != "unauthorized" {
		t.Errorf("401 body = %q, want just the word", body)
	}
	if strings.Contains(string(body), "config") {
		t.Error("the rejection echoes the path back")
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store — a proxy must not serve "+
			"somebody else this rejection", got)
	}
}

// ---- config/status ----------------------------------------------------------

func writeProject(t *testing.T, configYAML, tasksJSON string) string {
	t.Helper()
	root := t.TempDir()
	if configYAML != "" {
		if err := os.WriteFile(filepath.Join(root, "config.yaml"), []byte(configYAML), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if tasksJSON != "" {
		if err := os.WriteFile(filepath.Join(root, "tasks.json"), []byte(tasksJSON), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// All four conditions, and the AND is the point: a project with a config but
// no `meta.project`, or a spec root and no backend, is half-scaffolded and the
// wizard is the right answer.
func TestConfigStatus(t *testing.T) {
	const fullConfig = "spec_root: specs\nstate:\n  backend: sqlite\nbudgets_preset: conservative\n"
	const fullTasks = `{"meta":{"project":"billing-api"},"tasks":[]}`

	cases := []struct {
		name       string
		configYAML string
		tasksJSON  string
		wantSetup  bool
	}{
		{"everything present", fullConfig, fullTasks, true},
		{"no project id in tasks.json meta", fullConfig, `{"meta":{},"tasks":[]}`, false},
		{"no tasks.json at all", fullConfig, "", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := writeProject(t, c.configYAML, c.tasksJSON)
			s := newTestServer(t, cfg(ProfileOperator, ""), root)

			resp := get(t, s, "/api/config/status")
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d", resp.StatusCode)
			}
			var body configStatusPayload
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			_ = resp.Body.Close()

			if body.IsSetup != c.wantSetup {
				t.Errorf("is_setup = %v, want %v (%+v)", body.IsSetup, c.wantSetup, body)
			}
		})
	}
}

// A project too broken to read answers 200 with is_setup false, matching
// Python. The SPA renders the wizard either way, and a project that will not
// load is the strongest case for showing it — a 500 would give the SPA
// nothing to act on.
func TestConfigStatusOnAnUnreadableProjectStillAnswers(t *testing.T) {
	root := writeProject(t, "spec_root: specs\n", "{not json")
	s := newTestServer(t, cfg(ProfileOperator, ""), root)

	resp := get(t, s, "/api/config/status")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 even on a broken project", resp.StatusCode)
	}
	var body configStatusPayload
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if body.IsSetup {
		t.Error("is_setup = true on a project whose tasks.json does not parse")
	}
	if body.Error == "" {
		t.Error("the payload does not say what went wrong")
	}
}

// ---- construction and lifecycle ---------------------------------------------

func TestNewRejectsAServerThatCannotWork(t *testing.T) {
	// A stakeholder profile with no token 401s every request including its
	// own token form. Better said at startup than discovered as a blank page.
	if _, err := New(cfg(ProfileStakeholder, ""), Options{Static: spaHandler(t)}); err == nil {
		t.Error("stakeholder with no token was accepted")
	}
	// No SPA handler would 404 the shell while every API route worked — a
	// failure that reads as a broken build rather than a missing argument.
	if _, err := New(cfg(ProfileOperator, ""), Options{}); err == nil {
		t.Error("a nil static handler was accepted")
	}
}

func TestFromConfigRejectsAnUnknownProfile(t *testing.T) {
	var c config.Config
	c.Dashboard.Profile = "stakholder" // the typo that matters
	if _, err := FromConfig(c); err == nil {
		t.Error("a misspelled profile was accepted — it would have opened the dashboard")
	}
	// An empty profile is the operator default, not an error.
	c.Dashboard.Profile = ""
	got, err := FromConfig(c)
	if err != nil {
		t.Fatalf("empty profile: %v", err)
	}
	if got.Profile != ProfileOperator {
		t.Errorf("profile = %q, want %q", got.Profile, ProfileOperator)
	}
}

// Serve listens before it blocks, so a caller that prints the URL prints one
// that already answers — and a cancelled context shuts it down rather than
// leaving the port held.
func TestServeStartsAndStops(t *testing.T) {
	c := cfg(ProfileOperator, "")
	c.Port = 0 // any free port
	s := newTestServer(t, c, writeProject(t, "spec_root: specs\n", `{"meta":{}}`))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()

	// Ready closes when the listener is open, and BoundAddr is then the port
	// the kernel picked. Asking for the address and getting one that answers
	// is the whole claim in the doc comment, so the test makes a real request
	// over TCP rather than trusting the log line.
	<-s.Ready()
	if s.BoundAddr() == "" {
		t.Fatalf("no listener: %v", <-done)
	}
	resp, err := http.Get("http://" + s.BoundAddr() + "/api/whoami") // #nosec G107 -- the loopback listener this test just started
	if err != nil {
		t.Fatalf("GET /api/whoami on the live listener: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("live listener answered %d, want 200", resp.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return after its context was cancelled")
	}
}

// A listen that fails must release everyone waiting on Ready. A channel that
// only closed on success would turn a taken port into a hung caller, which is
// a worse failure than the error it is hiding.
func TestServeReleasesReadyWhenTheListenFails(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = taken.Close() }()

	c := cfg(ProfileOperator, "")
	host, port, err := net.SplitHostPort(taken.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	c.Host = host
	c.Port, err = strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	s := newTestServer(t, c, writeProject(t, "spec_root: specs\n", `{"meta":{}}`))

	done := make(chan error, 1)
	go func() { done <- s.Serve(context.Background()) }()

	select {
	case <-s.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("Ready never closed after a failed listen")
	}
	if got := s.BoundAddr(); got != "" {
		t.Errorf("BoundAddr = %q after a failed listen, want empty", got)
	}
	if err := <-done; err == nil {
		t.Error("Serve returned nil on a port already held")
	}
}

func pathsFor(root string) config.Paths {
	return config.Paths{Root: root, ID: "p", ConfigYAML: filepath.Join(root, "config.yaml")}
}
