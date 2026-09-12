package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestUnimplementedDataRoutesAre404 is the rule, and the table is the three
// real instances that produced it plus a route nobody has asked for yet.
//
// Each of these answered **200 with the SPA's HTML** before the prefix rule,
// because the path had no handler and `"/"` takes everything left over. A
// client fetching JSON then parses a page of markup: `/api/portfolio` broke
// the portfolio page's feature check, and `/stakeholder/summary` crashed the
// landing page outright with `Cannot read properties of undefined (reading
// 'done')` — axios passes the HTML string through and `if (!data)` finds a
// non-empty string truthy. `/stakeholder/summary` is served since the route
// landed, so the prefix case below stands in for it.
func TestUnimplementedDataRoutesAre404(t *testing.T) {
	s := portfolioServer(t, "demo", ProfileOperator, "", &fakeState{})

	for _, path := range []string{
		"/api/portfolio",             // only under --portfolio (G8.5)
		"/stakeholder/anything-else", // the prefix itself, not one named route
		"/api/tunnel/logs",           // no consumer since the tunnel page was trimmed
		"/api/a-route-nobody-wrote",
		"/stakeholder/anything",
	} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

			if rec.Code != http.StatusNotFound {
				t.Fatalf("%s = %d, want 404 (200 here means it fell through to the SPA)", path, rec.Code)
			}
			ct := rec.Header().Get("Content-Type")
			if !strings.Contains(ct, "application/json") {
				t.Fatalf("%s Content-Type = %q; HTML is what the bug looked like", path, ct)
			}
			var body apiNotFoundPayload
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("%s: decode: %v\n%s", path, err, rec.Body.String())
			}
			if body.Error == "" {
				t.Errorf("%s: no error text", path)
			}
			if body.Path != path {
				t.Errorf("%s: path = %q, want the path asked for", path, body.Path)
			}
		})
	}
}

// The rule must not swallow the routes that DO exist. `http.ServeMux` resolves
// by specificity rather than registration order, so this is checking that
// belief rather than restating it — a prefix that shadowed real routes would
// take the whole dashboard down, which is a worse failure than the one being
// fixed.
func TestTheRuleDoesNotShadowRealRoutes(t *testing.T) {
	s := portfolioServer(t, "demo", ProfileOperator, "", &fakeState{})

	for _, path := range []string{
		"/api/whoami",
		"/api/config/status",
		"/api/tasks",
		"/api/graph",
		"/api/events",
		"/api/sprint",
		"/api/milestones",
		"/api/metrics",
		"/api/budget/summary",
		"/api/tunnel/capabilities",
	} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if rec.Code == http.StatusNotFound {
				var body apiNotFoundPayload
				if json.Unmarshal(rec.Body.Bytes(), &body) == nil && body.Error == "not found" {
					t.Fatalf("%s was swallowed by the prefix rule", path)
				}
			}
		})
	}
}

// The SPA still owns everything else, which is what keeps a hard refresh on a
// client-side route working. A rule registered at "/" instead of the two data
// prefixes would 404 the app itself.
func TestTheRuleLeavesTheSPAAlone(t *testing.T) {
	s := portfolioServer(t, "demo", ProfileOperator, "", &fakeState{})

	for _, path := range []string{"/", "/kanban", "/list", "/portfolio"} {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code == http.StatusNotFound {
			t.Errorf("%s = 404; the SPA must still serve its own routes", path)
		}
	}
}

// The 404 is ungated: on a stakeholder dashboard it stays a 404 and does not
// become a 401.
//
// The distinction is the whole point of the route existing. A client asking
// "does this server have X?" gets an answer either way; turning "no such
// route" into "authenticate first" gives it a different wrong answer and
// breaks the same check one status code along.
func TestTheRuleIsNotGated(t *testing.T) {
	s := portfolioServer(t, "demo", ProfileStakeholder, testToken, &fakeState{})

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/nothing-here", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unimplemented route with no token = %d, want 404", rec.Code)
	}
}
