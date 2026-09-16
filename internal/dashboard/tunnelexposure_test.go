package dashboard

import (
	"net/http"
	"net/url"
	"testing"
)

// A running tunnel forwards the internet to this loopback listener, so a
// request arriving through it must carry a token even on the operator
// profile, which gates nothing for local callers.
func TestTunnelledRequestWithoutTokenIsRefused(t *testing.T) {
	s := startedTunnel(t)
	operator, portal := waitForLinks(t, s)
	u, err := url.Parse(operator)
	if err != nil {
		t.Fatal(err)
	}
	linkToken := u.Query().Get("token")

	relayed := http.Header{"Cf-Connecting-Ip": {"203.0.113.9"}}
	for _, tc := range []struct {
		name   string
		host   string
		header http.Header
		path   string
		want   int
	}{
		{"through the tunnel, no token", tunnelHost, relayed, "/api/whoami", http.StatusUnauthorized},
		// Cloudflare may not rewrite Host; its headers still give it away.
		{"relay headers on a loopback Host", "127.0.0.1:7420", relayed, "/api/whoami", http.StatusUnauthorized},
		{"through the tunnel, wrong token", tunnelHost, relayed, "/api/whoami?token=nope", http.StatusUnauthorized},
		{"the live event stream too", tunnelHost, relayed, "/api/events/stream", http.StatusUnauthorized},
		{"every data route too", tunnelHost, relayed, "/api/tasks", http.StatusUnauthorized},
		{"through the tunnel, link token", tunnelHost, relayed, "/api/whoami?token=" + linkToken, http.StatusOK},
		{"the operator at the keyboard", "127.0.0.1:7420", nil, "/api/whoami", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if rec := request(t, s, http.MethodGet, tc.path, tc.host, tc.header); rec.Code != tc.want {
				t.Errorf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}

	// The client-portal link is what gets sent to a client, and a client
	// must not get the operator's view by editing the path: its token only
	// reaches the stakeholder allow-list.
	pu, err := url.Parse(portal)
	if err != nil {
		t.Fatal(err)
	}
	portalToken := pu.Query().Get("token")
	if portalToken == linkToken {
		t.Fatal("the client-portal link carries the full dashboard's token")
	}
	for path, want := range map[string]int{
		"/api/whoami?token=" + portalToken:        http.StatusOK,
		"/api/tasks?token=" + portalToken:         http.StatusForbidden,
		"/api/tunnel/status?token=" + portalToken: http.StatusForbidden,
	} {
		if rec := request(t, s, http.MethodGet, path, tunnelHost, relayed); rec.Code != want {
			t.Errorf("%s with the portal token = %d, want %d", path, rec.Code, want)
		}
	}

	// The link dies with the tunnel: a new start mints a new token.
	if _, err := s.StopTunnel(); err != nil {
		t.Fatal(err)
	}
	if rec := request(t, s, http.MethodPost, "/api/tunnel/start", "127.0.0.1:7420", nil); rec.Code != http.StatusOK {
		t.Fatalf("restart = %d", rec.Code)
	}
	if rec := request(t, s, http.MethodGet, "/api/whoami?token="+linkToken, tunnelHost, relayed); rec.Code != http.StatusUnauthorized {
		t.Errorf("the old link still works after a restart: %d", rec.Code)
	}
}
