package dashboard

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hectorcanaimero/orch/internal/config"
	"github.com/hectorcanaimero/orch/internal/tunnel"
)

// newTunnelServer builds a server with a given profile and tunnel wiring.
func newTunnelServer(t *testing.T, profile Profile, token string, opts TunnelOptions) *Server {
	t.Helper()
	root := writeProject(t, "spec_root: specs\n", testTasksJSON)
	s, err := New(cfg(profile, token), Options{
		Static: spaHandler(t),
		State:  &fakeState{},
		Tunnel: opts,
		Paths:  config.Paths{Root: root, ID: "demo", ConfigYAML: filepath.Join(root, "config.yaml")},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// fakeCloudflared is the fake provider script, which answers --version and
// then idles like a tunnel that is up.
func fakeCloudflared(t *testing.T) string {
	t.Helper()
	script, err := filepath.Abs("../tunnel/testdata/fakebin/fake-tunnel.sh")
	if err != nil {
		t.Fatal(err)
	}
	// ConfigBlocker reads the home directory; a developer's own
	// ~/.cloudflared/config.yml must not decide these tests.
	t.Setenv("HOME", t.TempDir())
	return script
}

const tunnelHost = "quiet-river-sea-bird.trycloudflare.com"

// request drives a route as a caller at host, from a loopback peer — which is
// what both a local browser and cloudflared are.
func request(t *testing.T, s *Server, method, path, host string, header http.Header) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Host = host
	req.RemoteAddr = "127.0.0.1:50000"
	for k, v := range header {
		req.Header[k] = v
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func getWithHost(t *testing.T, s *Server, path, host string) *httptest.ResponseRecorder {
	t.Helper()
	return request(t, s, http.MethodGet, path, host, nil)
}

// startedTunnel returns an operator server whose tunnel was started through
// the route, with the fake provider printing a quick-tunnel URL.
func startedTunnel(t *testing.T) *Server {
	t.Helper()
	script := fakeCloudflared(t)
	t.Setenv("FAKE_TUNNEL_LINES", "|  https://quiet-river-sea-bird.trycloudflare.com  |\n")
	s := newTunnelServer(t, ProfileOperator, "", TunnelOptions{
		Enabled: true, Command: script, Manager: tunnel.NewManager(t.TempDir(), nil, 10),
	})
	if rec := request(t, s, http.MethodPost, "/api/tunnel/start", "127.0.0.1:7420", nil); rec.Code != http.StatusOK {
		t.Fatalf("start = %d: %s", rec.Code, rec.Body)
	}
	t.Cleanup(func() { _, _ = s.StopTunnel() })
	return s
}

func waitForLinks(t *testing.T, s *Server) (operator, portal string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if operator, portal = s.TunnelLinks(); operator != "" {
			return operator, portal
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the tunnel never reported a URL")
	return "", ""
}

// The gate order is the contract: config → profile → local caller → binary,
// and the reported reason is the FIRST failure.
func TestTunnelCapabilitiesReportsTheFirstFailingGate(t *testing.T) {
	missing := TunnelOptions{Enabled: true, Command: "orch-no-such-binary-exists"}
	for _, tc := range []struct {
		name    string
		profile Profile
		opts    TunnelOptions
		host    string
		reason  string
		short   string
	}{
		{"disabled beats everything", ProfileStakeholder, TunnelOptions{}, tunnelHost,
			tunnel.ReasonConfigDisabled, "disabled"},
		{"profile beats host", ProfileStakeholder, missing, tunnelHost, tunnel.ReasonProfileGate, "not_operator"},
		{"host is last of the three", ProfileOperator, missing, tunnelHost, tunnel.ReasonHostGate, "not_loopback"},
		{"the binary is checked only at the end", ProfileOperator, missing, "127.0.0.1:7420",
			tunnel.ReasonBinaryMissing, tunnel.ReasonBinaryMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token := ""
			if tc.profile != ProfileOperator {
				token = "test-token-stakeholder"
			}
			s := newTunnelServer(t, tc.profile, token, tc.opts)
			rec := getWithHost(t, s, "/api/tunnel/capabilities", tc.host)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 — this route always answers", rec.Code)
			}
			var got capabilitiesPayload
			decodeBody(t, rec, &got)
			if got.CanControl || got.Reason != tc.reason || len(got.Reasons) != 1 || got.Reasons[0] != tc.short {
				t.Errorf("got can_control=%v reason=%q reasons=%v, want reason %q [%s]",
					got.CanControl, got.Reason, got.Reasons, tc.reason, tc.short)
			}
			// What the machine has installed is only for the local operator.
			if tc.reason != tunnel.ReasonBinaryMissing && (got.Binary != nil || got.Guides != nil) {
				t.Errorf("binary/guides disclosed past a failed gate: %+v", got)
			}
		})
	}
}

// A missing binary comes with the install guides and this machine's pick, so
// the page can show the steps instead of an error.
func TestTunnelCapabilitiesCarryTheInstallGuide(t *testing.T) {
	s := newTunnelServer(t, ProfileOperator, "",
		TunnelOptions{Enabled: true, Command: "orch-no-such-binary-exists"})
	var got capabilitiesPayload
	decodeBody(t, getWithHost(t, s, "/api/tunnel/capabilities", "localhost:7420"), &got)
	if got.Binary == nil || got.Binary.Found {
		t.Fatalf("binary = %+v, want not found", got.Binary)
	}
	if got.Host == nil || len(got.Guides) == 0 || got.DocsURL == "" {
		t.Fatalf("no install guide in %+v", got)
	}
}

func TestTunnelCapabilitiesReportTheBinaryWhenInstalled(t *testing.T) {
	s := newTunnelServer(t, ProfileOperator, "",
		TunnelOptions{Enabled: true, Command: fakeCloudflared(t)})
	var got capabilitiesPayload
	decodeBody(t, getWithHost(t, s, "/api/tunnel/capabilities", "127.0.0.1:7420"), &got)
	if !got.CanControl || got.Reason != tunnel.ReasonOK {
		t.Fatalf("got %+v, want can_control", got)
	}
	if got.Binary == nil || !strings.Contains(got.Binary.Version, "fake") {
		t.Errorf("binary = %+v, want the version line", got.Binary)
	}
}

// For a stakeholder the route needs no token: the SPA decides whether to draw
// the tunnel page before it has asked anybody for one.
func TestTunnelCapabilitiesNeedsNoToken(t *testing.T) {
	s := newTunnelServer(t, ProfileStakeholder, "test-token-stakeholder",
		TunnelOptions{Enabled: true})
	rec := getWithHost(t, s, "/api/tunnel/capabilities", "127.0.0.1:7420")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d with no token, want 200", rec.Code)
	}
	var got capabilitiesPayload
	decodeBody(t, rec, &got)
	if got.CanControl || got.Reason != tunnel.ReasonProfileGate {
		t.Errorf("got %+v; a stakeholder may ASK and may not control", got)
	}
	if got.Provider == nil || *got.Provider != tunnel.Provider {
		t.Errorf("provider = %v", got.Provider)
	}
}

func TestTunnelCapabilitiesHidesTheProviderWhenDisabled(t *testing.T) {
	s := newTunnelServer(t, ProfileOperator, "", TunnelOptions{Enabled: false})
	var got capabilitiesPayload
	decodeBody(t, getWithHost(t, s, "/api/tunnel/capabilities", "127.0.0.1:7420"), &got)
	if got.Enabled || got.Provider != nil || got.Reason != tunnel.ReasonConfigDisabled {
		t.Errorf("got %+v, want disabled with no provider", got)
	}
}

// status, start and stop share one gate, and each status code says something
// different: 404 when not configured, 403 for a caller who may not ask.
func TestTunnelControlGates(t *testing.T) {
	enabled := TunnelOptions{Enabled: true}
	relayed := http.Header{"Cf-Connecting-Ip": {"203.0.113.9"}}
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/tunnel/status"},
		{http.MethodPost, "/api/tunnel/start"},
		{http.MethodPost, "/api/tunnel/stop"},
	} {
		for _, tc := range []struct {
			name    string
			profile Profile
			opts    TunnelOptions
			host    string
			header  http.Header
			want    int
		}{
			{"not configured", ProfileOperator, TunnelOptions{}, "127.0.0.1:7420", nil, http.StatusNotFound},
			{"not operator", ProfileBoth, enabled, "127.0.0.1:7420", nil, http.StatusForbidden},
			{"not loopback", ProfileOperator, enabled, "orch.example.com", nil, http.StatusForbidden},
			// cloudflared's peer address is loopback; its headers are not.
			{"relayed", ProfileOperator, enabled, "127.0.0.1:7420", relayed, http.StatusForbidden},
			{"enabled with no manager", ProfileOperator, enabled, "127.0.0.1:7420", nil,
				http.StatusInternalServerError},
		} {
			t.Run(route.path+"/"+tc.name, func(t *testing.T) {
				token := ""
				if tc.profile != ProfileOperator {
					token = "test-token-stakeholder"
				}
				s := newTunnelServer(t, tc.profile, token, tc.opts)
				if rec := request(t, s, route.method, route.path, tc.host, tc.header); rec.Code != tc.want {
					t.Errorf("status = %d, want %d", rec.Code, tc.want)
				}
			})
		}
	}
}

// A page on another site must not be able to press start through the
// operator's browser.
func TestTunnelStartRefusesACrossSiteOrigin(t *testing.T) {
	s := newTunnelServer(t, ProfileOperator, "", TunnelOptions{
		Enabled: true, Command: fakeCloudflared(t), Manager: tunnel.NewManager(t.TempDir(), nil, 10),
	})
	rec := request(t, s, http.MethodPost, "/api/tunnel/start", "127.0.0.1:7420",
		http.Header{"Origin": {"https://evil.example"}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if st := s.tunnel.Manager.Status().State; st != tunnel.StateIdle {
		t.Errorf("state = %q, the tunnel started anyway", st)
	}
}

func TestTunnelStartWithoutTheBinaryIsAConflict(t *testing.T) {
	s := newTunnelServer(t, ProfileOperator, "", TunnelOptions{
		Enabled: true, Command: "orch-no-such-binary-exists", Manager: tunnel.NewManager(t.TempDir(), nil, 10),
	})
	rec := request(t, s, http.MethodPost, "/api/tunnel/start", "127.0.0.1:7420",
		http.Header{"Origin": {"http://127.0.0.1:7420"}})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), tunnel.ReasonBinaryMissing) {
		t.Fatalf("status = %d %s, want 409 binary_missing", rec.Code, rec.Body)
	}
}

// Start hands the local operator a link carrying a token minted for this
// tunnel; stop takes both away.
func TestTunnelStartAndStopFromThePage(t *testing.T) {
	s := startedTunnel(t)
	operator, portal := waitForLinks(t, s)
	if !strings.HasPrefix(operator, "https://quiet-river-sea-bird.trycloudflare.com/?token=") ||
		!strings.HasPrefix(portal, "https://quiet-river-sea-bird.trycloudflare.com/stakeholder/?token=") {
		t.Fatalf("links = %q, %q", operator, portal)
	}

	var status tunnelStatusPayload
	decodeBody(t, getWithHost(t, s, "/api/tunnel/status", "127.0.0.1:7420"), &status)
	if status.State.State != tunnel.StateRunning || status.ShareURL == nil || *status.ShareURL != operator {
		t.Errorf("status = %+v, want running with the share link", status)
	}

	if rec := request(t, s, http.MethodPost, "/api/tunnel/stop", "127.0.0.1:7420", nil); rec.Code != http.StatusOK {
		t.Fatalf("stop = %d: %s", rec.Code, rec.Body)
	}
	if operator, _ := s.TunnelLinks(); operator != "" {
		t.Errorf("the link outlived the tunnel: %q", operator)
	}
}

// With a manager, the payload is the manager's own State — null pid and url
// for one that never started, so "not running" is not "running on port 0".
func TestTunnelStatusReportsTheManagersState(t *testing.T) {
	s := newTunnelServer(t, ProfileOperator, "", TunnelOptions{
		Enabled: true, Manager: tunnel.NewManager(t.TempDir(), nil, 100),
	})
	rec := getWithHost(t, s, "/api/tunnel/status", "127.0.0.1:7420")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got tunnelStatusPayload
	decodeBody(t, rec, &got)
	if got.State.State != "idle" || got.PID != nil || got.URL != nil || got.ShareURL != nil {
		t.Errorf("got %+v, want idle with null pid, url and share_url", got)
	}
}

// A stakeholder holding a valid token still cannot read tunnel state: the
// route is not on the allow-list.
func TestTunnelStatusIsNotOnTheStakeholderAllowList(t *testing.T) {
	s := newTunnelServer(t, ProfileStakeholder, "test-token-stakeholder", TunnelOptions{Enabled: true})
	rec := getWithHost(t, s, "/api/tunnel/status?token=test-token-stakeholder", "127.0.0.1:7420")
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func decodeBody[T any](t *testing.T, rec *httptest.ResponseRecorder, into *T) {
	t.Helper()
	decode(t, rec.Result(), into)
}
