package dashboard

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

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

// getWithHost drives a route with a chosen Host header — the third gate reads
// it, so it is not something these tests can leave to the default.
func getWithHost(t *testing.T, s *Server, path, host string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Host = host
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// The gate order is the contract: config → profile → host → binary, and the
// reported reason is the FIRST failure, never a list. Each row here fails a
// different gate with everything after it also failing, so a port that checked
// them in another order would report the wrong one.
func TestTunnelCapabilitiesReportsTheFirstFailingGate(t *testing.T) {
	const notLoopback = "orch.example.com"

	for _, tc := range []struct {
		name    string
		profile Profile
		opts    TunnelOptions
		host    string
		reason  string
		short   string
	}{
		{
			name: "disabled beats everything", profile: ProfileStakeholder,
			opts: TunnelOptions{Enabled: false}, host: notLoopback,
			reason: tunnel.ReasonConfigDisabled, short: "disabled",
		},
		{
			name: "profile beats host", profile: ProfileStakeholder,
			opts:   TunnelOptions{Enabled: true, Provider: "autossh", Command: "autossh"},
			host:   notLoopback,
			reason: tunnel.ReasonProfileGate, short: "not_operator",
		},
		{
			name: "host is last of the three", profile: ProfileOperator,
			opts:   TunnelOptions{Enabled: true, Provider: "autossh", Command: "autossh"},
			host:   notLoopback,
			reason: tunnel.ReasonHostGate, short: "not_loopback",
		},
		{
			// Every gate passes and the binary is absent. A command that
			// cannot exist is the cheapest way to be sure of that.
			name: "the binary is checked only at the end", profile: ProfileOperator,
			opts: TunnelOptions{Enabled: true, Provider: "autossh",
				Command: "orch-no-such-binary-exists"},
			host:   "127.0.0.1:7420",
			reason: tunnel.ReasonAutosshMissing, short: tunnel.ReasonAutosshMissing,
		},
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

			if got.CanControl {
				t.Error("can_control = true")
			}
			if got.Reason != tc.reason {
				t.Errorf("reason = %q, want %q", got.Reason, tc.reason)
			}
			if len(got.Reasons) != 1 || got.Reasons[0] != tc.short {
				t.Errorf("reasons = %v, want [%s]", got.Reasons, tc.short)
			}
		})
	}
}

// The one route on the stakeholder allow-list that also skips the token check.
// The SPA decides whether to draw the tunnel panel BEFORE it has asked anybody
// for a token, so a 401 here would mean the panel never appears — not even for
// someone holding a valid one.
func TestTunnelCapabilitiesNeedsNoToken(t *testing.T) {
	s := newTunnelServer(t, ProfileStakeholder, "test-token-stakeholder",
		TunnelOptions{Enabled: true, Provider: "autossh", Command: "autossh"})

	rec := getWithHost(t, s, "/api/tunnel/capabilities", "127.0.0.1:7420")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d with no token, want 200", rec.Code)
	}
	var got capabilitiesPayload
	decodeBody(t, rec, &got)

	// It still refuses control — it just says so instead of refusing to talk.
	if got.CanControl || got.Reason != tunnel.ReasonProfileGate {
		t.Errorf("got %+v; a stakeholder may ASK and may not control", got)
	}
	// And what it discloses is bounded: the provider name is only reported
	// when the tunnel is enabled, and nothing about state or URL is here.
	if got.Provider == nil || *got.Provider != "autossh" {
		t.Errorf("provider = %v", got.Provider)
	}
}

// A disabled tunnel reports no provider. The field is not "which provider
// would we use" — it is "which provider is configured", and a name for a
// tunnel that cannot run is a name the panel would draw.
func TestTunnelCapabilitiesHidesTheProviderWhenDisabled(t *testing.T) {
	s := newTunnelServer(t, ProfileOperator, "",
		TunnelOptions{Enabled: false, Provider: "autossh", Command: "autossh"})

	rec := getWithHost(t, s, "/api/tunnel/capabilities", "127.0.0.1:7420")
	var got capabilitiesPayload
	decodeBody(t, rec, &got)

	if got.Enabled {
		t.Error("enabled = true")
	}
	if got.Provider != nil {
		t.Errorf("provider = %q, want null while disabled", *got.Provider)
	}
	if got.Reason != tunnel.ReasonConfigDisabled {
		t.Errorf("reason = %q", got.Reason)
	}
}

// `/status` is the gated one, and each status code says something different:
// 404 when the tunnel is not configured (the route may as well not exist),
// 403 for a caller who may not ask.
func TestTunnelStatusGates(t *testing.T) {
	enabled := TunnelOptions{Enabled: true, Provider: "autossh", Command: "autossh"}

	for _, tc := range []struct {
		name    string
		profile Profile
		opts    TunnelOptions
		host    string
		want    int
	}{
		{"not configured", ProfileOperator, TunnelOptions{}, "127.0.0.1:7420", http.StatusNotFound},
		{"not operator", ProfileBoth, enabled, "127.0.0.1:7420", http.StatusForbidden},
		{"not loopback", ProfileOperator, enabled, "orch.example.com", http.StatusForbidden},
		// Enabled with no manager is a wiring bug, not a config state —
		// answering "idle" would be a lie the operator cannot act on.
		{"enabled with no manager", ProfileOperator, enabled, "127.0.0.1:7420",
			http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token := ""
			if tc.profile != ProfileOperator {
				token = "test-token-stakeholder"
			}
			s := newTunnelServer(t, tc.profile, token, tc.opts)
			if rec := getWithHost(t, s, "/api/tunnel/status", tc.host); rec.Code != tc.want {
				t.Errorf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

// With a manager, the payload is the manager's own State — the shape the SPA
// reads, unchanged by this layer.
func TestTunnelStatusReportsTheManagersState(t *testing.T) {
	mgr := tunnel.NewManager(t.TempDir(), nil, 100)
	s := newTunnelServer(t, ProfileOperator, "", TunnelOptions{
		Enabled: true, Provider: "autossh", Command: "autossh", Manager: mgr,
	})

	rec := getWithHost(t, s, "/api/tunnel/status", "127.0.0.1:7420")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got tunnel.State
	decodeBody(t, rec, &got)
	if got.State != "idle" {
		t.Errorf("state = %q, want idle for a manager that has never started", got.State)
	}
	// Nothing was started, so there is no PID and no URL — and they are null
	// rather than zero, which is what lets the panel tell "not running" from
	// "running on port 0".
	if got.PID != nil || got.URL != nil {
		t.Errorf("pid = %v, url = %v; both must be null", got.PID, got.URL)
	}
}

// A stakeholder holding a valid token still cannot read tunnel state. The
// route is not on the allow-list, so the gate refuses before the handler's own
// profile check would.
func TestTunnelStatusIsNotOnTheStakeholderAllowList(t *testing.T) {
	s := newTunnelServer(t, ProfileStakeholder, "test-token-stakeholder",
		TunnelOptions{Enabled: true, Provider: "autossh", Command: "autossh"})

	rec := getWithHost(t, s, "/api/tunnel/status?token=test-token-stakeholder", "127.0.0.1:7420")
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func decodeBody[T any](t *testing.T, rec *httptest.ResponseRecorder, into *T) {
	t.Helper()
	decode(t, rec.Result(), into)
}
