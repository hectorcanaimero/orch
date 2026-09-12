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
	"github.com/hectorcanaimero/orch/internal/templates"
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
	writeFile(t, filepath.Join(root, "config.yaml"),
		"spec_root: specs\nstate:\n  backend: sqlite\nbudgets_preset: conservative\n")
	writeFile(t, filepath.Join(root, "tasks.json"),
		`{"meta":{"project":"manual-check"},"tasks":[]}`)

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
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 120))
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("close the body of %s: %v", tc.path, cerr)
		}
		if readErr != nil {
			t.Fatalf("read the body of %s: %v", tc.path, readErr)
		}
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

	// The DAG is the one `orch init --template nextjs-saas` writes, rendered
	// from the SHIPPED template rather than hand-written here: a fixture the
	// project ships is a fixture that stays true (CHECKLIST rule 28).
	//
	// Not internal/graph/testdata/parity-project — that one is deliberately
	// broken (a self-dependency, a cycle, a missing dep, a task with no id) so
	// the validators have every error kind to report. It is the right fixture
	// for a validator and the wrong one for a panel that is supposed to show
	// what a healthy project looks like.
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "config.yaml"),
		"spec_root: specs\nstate:\n  backend: sqlite\nbudgets_preset: conservative\n")

	// nextjs-saas rather than python-api because it ships six tasks: this
	// check wants one done, one blocked AND something still to do, so that
	// the ETA and the remaining figures are not all zero.
	tmpl, err := templates.Project("nextjs-saas")
	if err != nil {
		t.Fatalf("read the nextjs-saas template: %v", err)
	}
	body, err := fs.ReadFile(tmpl, "tasks.json.tmpl")
	if err != nil {
		t.Fatalf("read tasks.json.tmpl: %v", err)
	}
	writeFile(t, filepath.Join(root, "tasks.json"),
		templates.Render(string(body), "manual-check", time.Now().UTC()))

	db, _, err := state.Open(ctx, filepath.Join(root, "orch.db"))
	if err != nil {
		t.Fatalf("open the database: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close the database: %v", err)
		}
	})

	backend := state.NewSQLite(db, "manual-check", root)
	f, err := model.LoadTasksFile(filepath.Join(root, "tasks.json"))
	if err != nil {
		t.Fatalf("load tasks.json: %v", err)
	}
	if err := backend.Bootstrap(ctx, f.Tasks); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if len(f.Tasks) < 3 {
		t.Fatalf("the nextjs-saas template has %d tasks; this check needs at least 3", len(f.Tasks))
	}
	// The first task finished, the last blocked with a reason on its last
	// event — enough for every field of the sprint payload to have something
	// in it.
	done, blocked := f.Tasks[0].ID, f.Tasks[len(f.Tasks)-1].ID
	if err := backend.Transition(ctx, done, model.StatusDone, state.Note{}); err != nil {
		t.Fatalf("finish %s: %v", done, err)
	}
	if err := backend.Transition(ctx, blocked, model.StatusBlocked, state.Note{}); err != nil {
		t.Fatalf("block %s: %v", blocked, err)
	}
	if err := backend.AppendEvent(ctx, "run-1", state.Event{
		EventType: "block", TaskID: blocked, Backend: "claude",
		TS:    time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		Extra: map[string]any{"reason": "waiting on " + done, "pid": 1},
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
		payload, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("close the body of %s: %v", path, cerr)
		}
		if readErr != nil {
			t.Fatalf("read the body of %s: %v", path, readErr)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, resp.StatusCode)
		}
		t.Logf("%s %d\n%s", path, resp.StatusCode, string(payload))
	}
}

// writeFile is os.WriteFile with the error handled, so a setup step that fails
// stops the test where it failed instead of two assertions later.
func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
