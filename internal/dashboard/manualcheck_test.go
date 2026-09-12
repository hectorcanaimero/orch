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
	"time"

	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/state"
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

// The b3 endpoints against a REAL database, not a fake: the two reads they
// added are SQL, and a fake would be testing the fake. Skipped unless
// ORCH_MANUAL_CHECK=1, like the SPA check above, and it prints the payloads so
// "I ran it and read it" is something the next person can re-run.
func TestManualCheckSprintAndMilestones(t *testing.T) {
	if os.Getenv("ORCH_MANUAL_CHECK") == "" {
		t.Skip("set ORCH_MANUAL_CHECK=1")
	}
	ctx := context.Background()

	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "config.yaml"),
		[]byte("spec_root: specs\nstate:\n  backend: sqlite\nbudgets_preset: conservative\n"), 0o600)
	_ = os.WriteFile(filepath.Join(root, "tasks.json"), []byte(`{
	  "meta": {"project": "manual-check"},
	  "tasks": [
	    {"id":"T-1","phase":0,"title":"Scaffold","model":"claude/sonnet","status":"todo","estimateHours":1.0},
	    {"id":"T-2","phase":0,"title":"Database","model":"claude/sonnet","status":"todo","dependencies":["T-1"],"estimateHours":2.0},
	    {"id":"T-3","phase":1,"title":"API","model":"claude/sonnet","status":"todo","dependencies":["T-2"],"estimateHours":4.0}
	  ]
	}`), 0o600)

	db, _, err := state.Open(ctx, filepath.Join(root, "orch.db"))
	if err != nil {
		t.Fatalf("open the database: %v", err)
	}
	defer func() { _ = db.Close() }()

	backend := state.NewSQLite(db, "manual-check", root)
	f, err := model.LoadTasksFile(filepath.Join(root, "tasks.json"))
	if err != nil {
		t.Fatalf("load tasks.json: %v", err)
	}
	if err := backend.Bootstrap(ctx, f.Tasks); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	// T-1 finished; T-3 blocked, with a reason on its last event.
	if err := backend.Transition(ctx, "T-1", model.StatusDone, state.Note{}); err != nil {
		t.Fatalf("finish T-1: %v", err)
	}
	if err := backend.Transition(ctx, "T-3", model.StatusBlocked, state.Note{}); err != nil {
		t.Fatalf("block T-3: %v", err)
	}
	if err := backend.AppendEvent(ctx, "run-1", state.Event{
		EventType: "block", TaskID: "T-3", Backend: "claude",
		TS:    time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		Extra: map[string]any{"reason": "waiting on T-2", "pid": 1},
	}); err != nil {
		t.Fatalf("append event: %v", err)
	}

	c := cfg(ProfileOperator, "")
	c.Port = 0
	s, err := New(c, Options{Static: spaHandler(t), State: backend, Paths: pathsFor(root)})
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	serveErr := make(chan error, 1)
	go func() { serveErr <- s.Serve(runCtx) }()
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

	for _, path := range []string{"/api/sprint", "/api/milestones"} {
		resp, rerr := http.Get("http://" + s.BoundAddr() + path) // #nosec G107 -- the loopback listener this test started
		if rerr != nil {
			t.Fatalf("GET %s: %v", path, rerr)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, resp.StatusCode)
		}
		t.Logf("%s %d\n%s", path, resp.StatusCode, string(body))
	}
}
