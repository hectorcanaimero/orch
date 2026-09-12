package dashboard

import (
	"context"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A real server, the real embedded SPA, real HTTP requests. Not a unit test —
// it is checklist rule 29 run as code so the result is reproducible rather
// than a screenshot in a PR body. Kept out of the normal run by a build tag
// would hide it; it is fast and it is the only thing that proves #97 is shut.
func TestManualCheckRealSPA(t *testing.T) {
	if os.Getenv("ORCH_MANUAL_CHECK") == "" {
		t.Skip("set ORCH_MANUAL_CHECK=1")
	}
	spa, err := SPA()
	if err != nil {
		t.Fatalf("SPA(): %v", err)
	}
	fileServer := http.FileServer(http.FS(spa))
	static := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		if _, serr := fs.Stat(spa, name); serr == nil {
			fileServer.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		body, rerr := fs.ReadFile(spa, "index.html")
		if rerr != nil {
			// An embedded SPA with no index.html is a broken build, and
			// answering 500 says so where a silent empty 200 would look
			// like the client-side router losing a route.
			http.Error(w, "no index.html in the embedded SPA", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(body)
	})

	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "config.yaml"),
		[]byte("spec_root: specs\nstate:\n  backend: sqlite\nbudgets_preset: conservative\n"), 0o600)
	_ = os.WriteFile(filepath.Join(root, "tasks.json"),
		[]byte(`{"meta":{"project":"manual-check"},"tasks":[]}`), 0o600)

	c := cfg(ProfileStakeholder, "test-token-stakeholder")
	// Port 0 and then ask the server what it got: a fixed port races whatever
	// else is on this machine, and sleeping until it is "probably up" is the
	// other way this test could be flaky. Ready closes when the listener is
	// open, so neither.
	c.Port = 0
	s, err := New(c, Options{Static: static, Paths: pathsFor(root)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() { serveErr <- s.Serve(ctx) }()
	defer func() {
		cancel()
		if err := <-serveErr; err != nil {
			t.Errorf("Serve: %v", err)
		}
	}()
	<-s.Ready()
	if s.BoundAddr() == "" {
		t.Fatalf("the listener never came up: %v", <-serveErr)
	}

	// Find the real hashed bundle name rather than hardcoding it.
	var jsPath string
	_ = fs.WalkDir(spa, "assets", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".js") {
			jsPath = "/" + p
		}
		return nil
	})
	if jsPath == "" {
		t.Fatal("no bundle in the embedded assets/")
	}

	base := "http://" + s.BoundAddr()
	for _, tc := range []struct {
		path string
		want int
		why  string
	}{
		{"/", 200, "the shell, with no token — a browser has none yet"},
		{jsPath, 200, "the bundle, with no token — this is the #97 failure"},
		{"/favicon.svg", 200, "a public/ file at the dist root"},
		{"/manifest.json", 200, "another one an allow-list would miss"},
		{"/icon-192.png", 200, "and another"},
		{"/kanban", 200, "a client-side route falls back to the shell"},
		{"/api/config/status", 401, "a data route with no token"},
		{"/api/whoami?token=test-token-stakeholder", 200, "on the allow-list, with a token"},
		{"/api/config/status?token=test-token-stakeholder", 403, "authenticated, not allow-listed"},
		{"/api/whoami?token=wrong", 401, "the wrong token"},
	} {
		resp, rerr := http.Get(base + tc.path) // #nosec G107 -- a fixed loopback URL in a test
		if rerr != nil {
			t.Fatalf("GET %s: %v", tc.path, rerr)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 120))
		_ = resp.Body.Close()
		mark := "ok "
		if resp.StatusCode != tc.want {
			mark = "BAD"
			t.Errorf("GET %s = %d, want %d (%s)", tc.path, resp.StatusCode, tc.want, tc.why)
		}
		t.Logf("%s %-36s %d  %s", mark, tc.path, resp.StatusCode,
			strings.ReplaceAll(strings.TrimSpace(string(body))[:min(60, len(strings.TrimSpace(string(body))))], "\n", " "))
	}
}
