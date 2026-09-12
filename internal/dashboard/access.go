package dashboard

import (
	"net/http"
	"strings"
)

// The dashboard's access model, ported from `TokenAuthMiddleware` and
// `ProfileGuardMiddleware` in `orchestrator/dashboard/middleware.py`.
//
// # Why this is not middleware
//
// Python wraps the whole app and then works out, per request, whether the path
// would have reached a data route — matching prefixes, consulting the resolved
// route name, and special-casing the SPA mount. That shape produced the bug
// this port exists not to repeat: the gate covered `/assets/*`, the browser
// asked for the SPA's JS with no token, got 401, and the page rendered blank
// with a 200 on the HTML (#97).
//
// Here the gate is a decorator on the handlers that need it, and the static
// handler is simply not decorated. Public is the default for what the SPA
// serves, so there is no classification to get wrong — and a new API route
// registered without `gated` is a visible omission at the registration site
// rather than an invisible hole in a prefix list somewhere else.
//
// The rule that makes this safe is the one sonnet-2's build turned up: a Vite
// build puts `favicon.svg`, `manifest.json` and the PWA icons at the ROOT of
// dist, beside `index.html` and nowhere near `/assets/`. Any allow-list of
// static paths is a list somebody has to keep in step with whatever `web/public`
// happens to contain. This design never needs one.

// Profile decides how much of the dashboard a request may see.
type Profile string

const (
	// ProfileOperator is the default: no auth, everything visible. The
	// operator is assumed to be on localhost.
	ProfileOperator Profile = "operator"
	// ProfileStakeholder gates every data route behind a token and an
	// allow-list. This is what a shared tunnel URL runs as.
	ProfileStakeholder Profile = "stakeholder"
	// ProfileBoth serves the operator surface unguarded and applies the
	// stakeholder rules only under /stakeholder/. Mixed-mode local use.
	ProfileBoth Profile = "both"
)

// StakeholderPathPrefix is where `both` mode switches into stakeholder rules.
const StakeholderPathPrefix = "/stakeholder"

// DefaultStakeholderRoutes is the allow-list a stakeholder session gets when
// config.yaml does not override it, by route name.
//
// Ported from `DEFAULT_STAKEHOLDER_ROUTES`, with each entry's reason kept
// because every one of them is a decision about what a client may see:
//
//   - stakeholder_summary_json — the summary the whole profile exists for.
//   - api_tunnel_capabilities — the SPA has to know whether to draw the tunnel
//     panel BEFORE it has a token, so this one also skips the token check.
//   - api_whoami — the SPA hides operator-only nav from the profile. The
//     profile is not a secret; the token is.
//   - api_docs_list / api_docs_content — PRD and spec markdown are project
//     artefacts a stakeholder is meant to read.
//   - spa — the static shell and every client-side route under it.
var DefaultStakeholderRoutes = []string{
	"stakeholder_summary_json",
	"api_tunnel_capabilities",
	"api_whoami",
	"api_docs_list",
	"api_docs_content",
	"spa",
}

// noTokenRoutes are the route names a stakeholder may reach without one.
//
// Exactly one, and it is not a convenience: the SPA decides whether to render
// the tunnel panel before it has asked anybody for a token, so a 401 here
// would mean the panel could never appear even for someone holding a valid
// one.
var noTokenRoutes = map[string]bool{
	"api_tunnel_capabilities": true,
}

// Verdict is what the gate decided about one request.
type Verdict int

const (
	// Allow — serve it.
	Allow Verdict = iota
	// Unauthorized — 401. No token, or the wrong one.
	Unauthorized
	// Forbidden — 403. Authenticated, but this route is not on the
	// stakeholder allow-list.
	Forbidden
)

// decide answers whether one request may proceed, given the hash the
// supplied token is compared against.
//
// expectedHash is resolved by the caller (Server.expectedTokenHash) rather
// than looked up in here on purpose — G8.2 (F3.3) moved the token itself
// into the database, keyed by project, and a live lookup inside a function
// this deliberately pure would turn the whole-model-as-a-table test below
// into a test that needs a server, a database and a clock. Passing the
// already-resolved hash in keeps it a table: profile × path × route name ×
// token × expected hash, with no server, no recorder, no handler, no
// database. See tokenhash.go for HashToken, the one place a token is turned
// into what gets compared.
//
// Order matters and matches Python's: authentication before authorisation, so
// an unauthenticated caller gets 401 and never learns from a 403 which routes
// exist.
func (c Config) decide(path, routeName, suppliedToken, expectedHash string) Verdict {
	if !c.stakeholderContext(path) {
		return Allow
	}
	if noTokenRoutes[routeName] {
		return Allow
	}

	// A stakeholder profile with no token configured (in config.yaml, via
	// --token, or in the database) is a misconfigured server, and it
	// answers 401 like a wrong token would. Saying "the server has no
	// token" would tell an anonymous caller how to think about the
	// deployment; saying nothing costs the operator one look at their setup.
	if expectedHash == "" {
		return Unauthorized
	}
	if !constantTimeEqual(HashToken(suppliedToken), expectedHash) {
		return Unauthorized
	}

	if !c.routeAllowed(path, routeName) {
		return Forbidden
	}
	return Allow
}

// stakeholderContext reports whether the stakeholder rules apply at all.
//
// In `both` mode only `/stakeholder/*` is gated: the operator surface stays
// open because mixed mode is a local convenience and the operator is on
// localhost. A deployment that shares its URL runs as `stakeholder`, where
// this is true everywhere.
func (c Config) stakeholderContext(path string) bool {
	switch c.Profile {
	case ProfileStakeholder:
		return true
	case ProfileBoth:
		return path == StakeholderPathPrefix || strings.HasPrefix(path, StakeholderPathPrefix+"/")
	default:
		return false
	}
}

// routeAllowed matches one request against the allow-list.
//
// Two entry forms, both Python's:
//
//   - starting with "/" — a path prefix, except a bare "/" which is an EXACT
//     match. Without that exception a single "/" in the list would allow every
//     path there is, since they all start with one.
//   - anything else — a route name, compared exactly.
//
// Order in the list does not matter; the first match wins and they are all
// permissive.
func (c Config) routeAllowed(path, routeName string) bool {
	for _, entry := range c.StakeholderRoutes {
		if entry == "" {
			continue
		}
		if strings.HasPrefix(entry, "/") {
			if entry == "/" {
				if path == "/" {
					return true
				}
				continue
			}
			if path == strings.TrimSuffix(entry, "/") || strings.HasPrefix(path, entry) {
				return true
			}
			continue
		}
		if routeName != "" && entry == routeName {
			return true
		}
	}
	return false
}

// tokenFrom reads the token a caller supplied.
//
// Two places, both Python's: the `Authorization: Bearer` header, and a
// `?token=` query parameter. The query form exists because a shared URL is the
// whole point of the stakeholder profile — a link someone pastes into a chat
// cannot carry a header.
//
// The header wins when both are present. A caller who sets a header is using
// the API deliberately; a stale query parameter left in a bookmark should not
// override it.
func tokenFrom(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		if rest, ok := strings.CutPrefix(h, "Bearer "); ok {
			return strings.TrimSpace(rest)
		}
	}
	return r.URL.Query().Get("token")
}
