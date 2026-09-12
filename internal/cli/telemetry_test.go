package cli_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hectorcanaimero/orch/internal/cli"
)

// appendTelemetryConfig points the fixture project's config.yaml at a test
// collector, enabled or not — G8.6 (F4.9)'s own config.yaml block.
func appendTelemetryConfig(t *testing.T, root string, enabled bool, endpoint string) {
	t.Helper()
	path := filepath.Join(root, ".orchestrator", "config.yaml")
	existing, err := os.ReadFile(path) // #nosec G304 -- t.TempDir()-rooted fixture copy
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	block := "\ntelemetry:\n  enabled: " + boolYAML(enabled) + "\n  endpoint: " + endpoint + "\n"
	// #nosec G703 -- path is t.TempDir()-rooted (newTestProject's fixture copy), not caller input.
	if err := os.WriteFile(path, append(existing, []byte(block)...), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func boolYAML(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// The test G8.6 exists to pass: with telemetry off, no request ever
// reaches the collector, whatever else is configured. transport, not
// handler, is what fails the test — a RoundTripper that fires means
// Reporter.Report attempted a send at all, before any question of what
// the collector does with it.
func TestTelemetryDoesNotFireWhenDisabled(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		atomic.AddInt32(&hits, 1)
	}))
	defer srv.Close()

	root, common := newTestProject(t)
	t.Setenv("HOME", t.TempDir())
	appendTelemetryConfig(t, root, false, srv.URL)

	if rc := cli.Run("test", append([]string{"status"}, common...)); rc != 0 {
		t.Fatalf("orch status exit = %d, want 0", rc)
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Errorf("telemetry.enabled: false but the collector saw %d request(s)", got)
	}
}

// DO_NOT_TRACK overrides telemetry.enabled: true unconditionally — the
// standard's whole point, and the one config value a project's own
// config.yaml is not allowed to win against.
func TestTelemetryDoesNotFireUnderDoNotTrack(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		atomic.AddInt32(&hits, 1)
	}))
	defer srv.Close()

	root, common := newTestProject(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DO_NOT_TRACK", "1")
	appendTelemetryConfig(t, root, true, srv.URL)

	if rc := cli.Run("test", append([]string{"status"}, common...)); rc != 0 {
		t.Fatalf("orch status exit = %d, want 0", rc)
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Errorf("DO_NOT_TRACK=1 but the collector saw %d request(s)", got)
	}
}

// The other half: enabled sends exactly one event, and with the fields
// the package doc promises — nothing more (no path, no project id).
func TestTelemetrySendsExactlyOneEventWhenEnabled(t *testing.T) {
	var (
		hits int32
		mu   sync.Mutex
		body []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		body = b
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	root, common := newTestProject(t)
	t.Setenv("HOME", t.TempDir())
	appendTelemetryConfig(t, root, true, srv.URL)

	if rc := cli.Run("test", append([]string{"status"}, common...)); rc != 0 {
		t.Fatalf("orch status exit = %d, want 0", rc)
	}

	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("collector saw %d request(s), want exactly 1", got)
	}

	mu.Lock()
	defer mu.Unlock()
	var event map[string]any
	if err := json.Unmarshal(body, &event); err != nil {
		t.Fatalf("decode event %s: %v", body, err)
	}

	if event["command"] != "status" {
		t.Errorf("command = %v, want %q", event["command"], "status")
	}
	if success, ok := event["success"].(bool); !ok || !success {
		t.Errorf("success = %v, want true", event["success"])
	}
	if id, ok := event["install_id"].(string); !ok || id == "" {
		t.Errorf("install_id = %q, want a non-empty string", event["install_id"])
	}
	if v, ok := event["version"].(string); !ok || v != "test" {
		t.Errorf("version = %v, want %q (Run's own arg)", event["version"], "test")
	}
	if _, ok := event["os"].(string); !ok {
		t.Errorf("os missing: %v", event)
	}
	if _, ok := event["arch"].(string); !ok {
		t.Errorf("arch missing: %v", event)
	}
	if _, ok := event["duration_ms"].(float64); !ok {
		t.Errorf("duration_ms missing or not a number: %v", event["duration_ms"])
	}

	// The list IS the contract: a path, a project id, or a task id showing
	// up here would be exactly the leak this package's doc comment
	// promises never happens.
	allowed := map[string]bool{
		"install_id": true, "version": true, "os": true, "arch": true,
		"command": true, "duration_ms": true, "success": true,
	}
	for k := range event {
		if !allowed[k] {
			t.Errorf("event carries unexpected field %q: %v", k, event[k])
		}
	}
}
