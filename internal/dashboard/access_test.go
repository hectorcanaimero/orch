package dashboard

import "testing"

// The access model as a table: profile × path × route × token, with no server,
// no recorder and no handler. The rules are much easier to get wrong than the
// plumbing is, and a table is how you see all of them at once.

func cfg(profile Profile, token string) Config {
	return Config{
		Profile: profile,
		Token:   token,
		// Mirrors what newDashboardCmd resolves at startup when the
		// database has no row yet — see config.go's own doc comment on
		// TokenHash for why Validate() (called inside New()) checks this
		// field and not Token directly.
		TokenHash:         HashToken(token),
		StakeholderRoutes: DefaultStakeholderRoutes,
		Host:              DefaultHost,
		Port:              DefaultPort,
	}
}

func TestDecide(t *testing.T) {
	const testToken = "test-token-stakeholder"

	cases := []struct {
		name    string
		cfg     Config
		path    string
		route   string
		token   string
		want    Verdict
		because string
	}{
		// --- operator: nothing is gated -----------------------------------
		{
			name: "operator serves a data route with no token", cfg: cfg(ProfileOperator, ""),
			path: "/api/tasks", route: "api_tasks", want: Allow,
			because: "the operator is assumed to be on localhost; this is the default profile and it has never had auth",
		},
		{
			name: "operator ignores a configured token", cfg: cfg(ProfileOperator, testToken),
			path: "/api/tasks", route: "api_tasks", want: Allow,
			because: "a token left in config must not start gating an operator dashboard",
		},

		// --- stakeholder: token first, then the allow-list -----------------
		{
			name: "stakeholder with no token", cfg: cfg(ProfileStakeholder, testToken),
			path: "/api/tasks", route: "api_tasks", want: Unauthorized,
		},
		{
			name: "stakeholder with the wrong token", cfg: cfg(ProfileStakeholder, testToken),
			path: "/api/tasks", route: "api_tasks", token: "nope", want: Unauthorized,
		},
		{
			name: "stakeholder with the right token, route not allowed",
			cfg:  cfg(ProfileStakeholder, testToken),
			path: "/api/tasks", route: "api_tasks", token: testToken, want: Forbidden,
			because: "authentication before authorisation: an anonymous caller gets 401 and never learns from a 403 which routes exist",
		},
		{
			name: "stakeholder with the right token, route allowed",
			cfg:  cfg(ProfileStakeholder, testToken),
			path: "/api/whoami", route: "api_whoami", token: testToken, want: Allow,
		},
		{
			name: "stakeholder, server configured with no token at all",
			cfg:  cfg(ProfileStakeholder, ""),
			path: "/api/whoami", route: "api_whoami", token: "anything", want: Unauthorized,
			because: "a misconfigured server answers like a wrong token; saying 'no token is set' would tell an anonymous caller how the deployment is built",
		},

		// --- the one exception --------------------------------------------
		{
			name: "capabilities needs no token", cfg: cfg(ProfileStakeholder, testToken),
			path: "/api/tunnel/capabilities", route: "api_tunnel_capabilities", want: Allow,
			because: "the SPA decides whether to draw the tunnel panel before it has asked anyone for a token",
		},

		// --- both: only /stakeholder/* is gated ---------------------------
		{
			name: "both leaves the operator surface open", cfg: cfg(ProfileBoth, testToken),
			path: "/api/tasks", route: "api_tasks", want: Allow,
		},
		{
			name: "both gates the stakeholder prefix", cfg: cfg(ProfileBoth, testToken),
			path: "/stakeholder/summary", route: "stakeholder_summary_json", want: Unauthorized,
		},
		{
			name: "both gates the prefix itself", cfg: cfg(ProfileBoth, testToken),
			path: "/stakeholder", route: "stakeholder_summary_json", want: Unauthorized,
		},
		{
			name: "both does not gate a path that merely starts with the word",
			cfg:  cfg(ProfileBoth, testToken),
			path: "/stakeholders-report", route: "api_tasks", want: Allow,
			because: "a prefix match without the boundary would gate any path beginning with those letters",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Table-testable per decide's own doc comment: the expected
			// hash is derived right here from the plaintext each case
			// wrote into cfg.Token, the same transform HashToken(c.token)
			// applies to what a caller supplies — decide reads neither
			// value's plaintext, only the two hashes.
			got := c.cfg.decide(c.path, c.route, c.token, HashToken(c.cfg.Token))
			if got != c.want {
				t.Errorf("decide(%q, %q, token=%q) = %v, want %v\n%s",
					c.path, c.route, c.token, got, c.want, c.because)
			}
		})
	}
}

// The comparison is against the supplied token's hash, not its plaintext —
// a caller cannot authenticate by presenting the stored hash itself.
func TestDecideComparesHashesNotPlaintext(t *testing.T) {
	c := cfg(ProfileStakeholder, "test-token-stakeholder")
	got := c.decide("/api/whoami", "api_whoami", c.TokenHash, c.TokenHash)
	if got != Unauthorized {
		t.Errorf("presenting the stored hash as the token = %v, want Unauthorized", got)
	}
}

// A bare "/" in the allow-list is an EXACT match, not a prefix.
//
// Without the exception a single "/" would allow every path there is, since
// they all start with one — and it is a plausible thing to write in a config
// meaning "the index page".
func TestAllowListBareSlashIsExact(t *testing.T) {
	c := cfg(ProfileStakeholder, "t")
	c.StakeholderRoutes = []string{"/"}

	if !c.routeAllowed("/", "spa") {
		t.Error(`"/" should allow the index`)
	}
	for _, path := range []string{"/api/tasks", "/api/config", "/anything"} {
		if c.routeAllowed(path, "api_tasks") {
			t.Errorf(`"/" allowed %q — a bare slash is not a prefix`, path)
		}
	}
}

func TestAllowListEntryForms(t *testing.T) {
	c := cfg(ProfileStakeholder, "t")
	c.StakeholderRoutes = []string{"/api/docs", "api_whoami", ""}

	cases := []struct {
		path, route string
		want        bool
		why         string
	}{
		{"/api/docs", "api_docs_list", true, "exact path"},
		{"/api/docs/content", "api_docs_content", true, "path prefix"},
		{"/api/whoami", "api_whoami", true, "route name"},
		{"/api/tasks", "api_tasks", false, "neither"},
		{"/api/docsomething", "x", true, "prefix match is textual, as in Python"},
	}
	for _, tc := range cases {
		if got := c.routeAllowed(tc.path, tc.route); got != tc.want {
			t.Errorf("routeAllowed(%q, %q) = %v, want %v (%s)",
				tc.path, tc.route, got, tc.want, tc.why)
		}
	}
}

// An entry with a trailing slash matches the path without one. A config
// saying `/api/docs/` should not stop `/api/docs` from resolving.
func TestAllowListTrailingSlash(t *testing.T) {
	c := cfg(ProfileStakeholder, "t")
	c.StakeholderRoutes = []string{"/api/docs/"}
	if !c.routeAllowed("/api/docs", "api_docs_list") {
		t.Error("a trailing slash in the entry should still match the bare path")
	}
}

// The default allow-list covers what the stakeholder profile exists to serve,
// and nothing that would leak the operator surface.
func TestDefaultAllowListShape(t *testing.T) {
	c := cfg(ProfileStakeholder, "t")

	for _, allowed := range []struct{ path, route string }{
		{"/stakeholder/summary", "stakeholder_summary_json"},
		{"/api/whoami", "api_whoami"},
		{"/api/tunnel/capabilities", "api_tunnel_capabilities"},
		{"/api/docs", "api_docs_list"},
		{"/", "spa"},
		{"/kanban", "spa"},
	} {
		if !c.routeAllowed(allowed.path, allowed.route) {
			t.Errorf("the default list should allow %s (%s)", allowed.path, allowed.route)
		}
	}
	// The operator's data routes are not on it. These are the ones that would
	// hand a client the project's cost, plan and history.
	for _, denied := range []struct{ path, route string }{
		{"/api/tasks", "api_tasks"},
		{"/api/metrics", "api_metrics"},
		{"/api/budget/summary", "api_budget_summary"},
		{"/api/config", "api_config"},
		{"/api/events", "api_events"},
	} {
		if c.routeAllowed(denied.path, denied.route) {
			t.Errorf("the default list allows %s (%s) — that is operator data",
				denied.path, denied.route)
		}
	}
}
