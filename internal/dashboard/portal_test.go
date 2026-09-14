package dashboard

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/hectorcanaimero/orch/internal/config"
	"github.com/hectorcanaimero/orch/internal/publish/snapshot"
)

const portalToken = "portal-token"

func newPortalServer(t *testing.T, profile Profile) *Server {
	t.Helper()
	root := t.TempDir()
	s, err := New(cfg(profile, portalToken), Options{
		Static: spaHandler(t),
		Paths:  config.Paths{Root: root, ID: "p", ConfigYAML: filepath.Join(root, "config.yaml")},
		Portal: fstest.MapFS{
			"stakeholder.html":            {Data: []byte("<!doctype html><title>portal</title>")},
			"assets/stakeholder-abc.js":   {Data: []byte("console.log('portal')")},
			"assets/source-serif-4.woff2": {Data: []byte("font")},
		},
		Snapshot: func(context.Context) (snapshot.Snapshot, error) {
			return snapshot.Snapshot{Schema: 1, ProjectName: "usebot", RefreshIntervalS: 30}, nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func body(t *testing.T, resp *http.Response) string {
	t.Helper()
	raw, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(raw)
}

// The client portal is served live at /stakeholder/: its page and assets
// with no token (they are code, like the SPA's), its data only with one.
func TestPortalIsServedAndItsDataIsGated(t *testing.T) {
	s := newPortalServer(t, ProfileStakeholder)

	for _, path := range []string{"/stakeholder/", "/stakeholder/assets/stakeholder-abc.js"} {
		resp := get(t, s, path)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want 200 without a token", path, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}
	if got := body(t, get(t, s, "/stakeholder/")); !strings.Contains(got, "<title>portal</title>") {
		t.Errorf("/stakeholder/ served %q, want the portal page", got)
	}

	if resp := get(t, s, "/stakeholder/data.json"); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("data without a token = %d, want 401", resp.StatusCode)
	}
	resp := get(t, s, "/stakeholder/data.json?token="+portalToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("data with the token = %d, want 200", resp.StatusCode)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store (live data behind a token)", cc)
	}
	var snap snapshot.Snapshot
	if err := json.Unmarshal([]byte(body(t, resp)), &snap); err != nil || snap.ProjectName != "usebot" {
		t.Errorf("data = %+v (%v), want the snapshot", snap, err)
	}

	// An unknown file under the data prefix stays a JSON 404, never a page.
	if resp := get(t, s, "/stakeholder/nope.txt"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /stakeholder/nope.txt = %d, want 404", resp.StatusCode)
	}
}

// Links already shared as /?token=… keep working: a stakeholder dashboard
// sends every page request to the portal, token included.
func TestStakeholderPagesRedirectToThePortal(t *testing.T) {
	s := newPortalServer(t, ProfileStakeholder)
	for path, want := range map[string]string{
		"/?token=" + portalToken: "/stakeholder/?token=" + portalToken,
		"/kanban":                "/stakeholder/",
	} {
		resp := get(t, s, path)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != want {
			t.Errorf("GET %s = %d → %q, want 302 → %q", path, resp.StatusCode, resp.Header.Get("Location"), want)
		}
	}

	// An operator keeps the SPA at / and can preview the portal.
	op := newPortalServer(t, ProfileOperator)
	if got := body(t, get(t, op, "/")); !strings.Contains(got, "id=root") {
		t.Errorf("operator / served %q, want the SPA", got)
	}
	if resp := get(t, op, "/stakeholder/data.json"); resp.StatusCode != http.StatusOK {
		t.Errorf("operator portal data = %d, want 200", resp.StatusCode)
	}
}

func TestPortalDataIsOnTheStakeholderAllowList(t *testing.T) {
	found := false
	for _, r := range DefaultStakeholderRoutes {
		found = found || r == "stakeholder_snapshot_json"
	}
	if !found {
		t.Error("stakeholder_snapshot_json is not on the default allow-list: a token would get 403 on the portal's data")
	}
}
