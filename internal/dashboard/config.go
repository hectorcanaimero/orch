package dashboard

import (
	"crypto/subtle"
	"fmt"
	"strings"

	"github.com/hectorcanaimero/orch/internal/config"
)

// Config is what the dashboard needs to decide who sees what, and where to
// listen.
//
// Built from config.yaml's `dashboard:` block. The stakeholder allow-list is
// the one field with no home in `internal/config` yet: it is a dashboard
// concern and nothing else reads it, so it is resolved here from the same
// block rather than widening the shared config struct for one consumer.
type Config struct {
	Profile Profile
	// Token is the shared secret from config.yaml/--token, in plaintext.
	// Empty under the operator profile, where nothing is gated. Never
	// itself compared against a supplied token — see TokenHash — and kept
	// only as the source `orch dashboard` hashes when the database has no
	// row for this project yet (see cli's newDashboardCmd).
	Token string
	// TokenHash is the resolved effective hash AT STARTUP: from the
	// database (state.Backend.StakeholderToken) if this project has ever
	// rotated one, else HashToken(Token). Consumed by Validate() and the
	// startup banner, which both need an answer before a listener opens.
	// NOT consulted by decide() at request time — Server resolves it fresh
	// on every gated request instead (see server.go's expectedTokenHash),
	// so a rotation takes effect on an already-running dashboard with no
	// restart. This field would go stale the moment that happens, which is
	// exactly why request handling does not read it.
	TokenHash string
	// TokenSource is "database" or "config.yaml" — which one produced
	// TokenHash at startup. Empty when TokenHash is empty. Informational
	// only: the startup banner and `orch dashboard token show` print it,
	// nothing gates on it.
	TokenSource string
	// StakeholderRoutes is the allow-list, by route name or path prefix.
	StakeholderRoutes []string
	// Host and Port are where the server listens.
	Host string
	Port int
}

// Defaults for the listener, matching Python's.
const (
	DefaultHost = "127.0.0.1"
	DefaultPort = 7420
)

// FromConfig builds a dashboard Config from a loaded project config.
//
// An unknown profile is an error rather than a silent fallback to operator.
// Falling back would turn `profile: stakholder` — a typo in a file someone
// edited to *restrict* access — into a dashboard with no auth at all, and the
// symptom is a page that works, which is exactly the wrong feedback.
func FromConfig(cfg config.Config) (Config, error) {
	profile := Profile(strings.TrimSpace(cfg.Dashboard.Profile))
	if profile == "" {
		profile = ProfileOperator
	}
	switch profile {
	case ProfileOperator, ProfileStakeholder, ProfileBoth:
	default:
		return Config{}, fmt.Errorf(
			"dashboard.profile %q is not one of %s, %s, %s",
			profile, ProfileOperator, ProfileStakeholder, ProfileBoth)
	}

	out := Config{
		Profile:           profile,
		Token:             strings.TrimSpace(cfg.Dashboard.Token),
		StakeholderRoutes: DefaultStakeholderRoutes,
		Host:              DefaultHost,
		Port:              DefaultPort,
	}
	return out, nil
}

// Validate reports a configuration that cannot serve what it claims to.
//
// Separate from FromConfig because the two answer different questions: that
// one asks "is this file well-formed", this one asks "will this deployment
// work". A stakeholder profile with no token parses fine and then 401s every
// request including its own token form, which is a server nobody can use — and
// the operator finds out from a blank page rather than from startup.
func (c Config) Validate() error {
	// The profile first, and here rather than only in FromConfig: a caller
	// that sets it directly — `orch dashboard --profile stakholder` — would
	// otherwise skip the one check that catches a typo, and an unknown
	// profile is not inert. `decide` reads anything it does not recognise as
	// "no stakeholder context", which means ALLOW EVERYTHING. A misspelled
	// flag meant to restrict access would open it, and the symptom is a page
	// that works.
	switch c.Profile {
	case ProfileOperator, ProfileStakeholder, ProfileBoth:
	default:
		return fmt.Errorf(
			"dashboard profile %q is not one of %s, %s, %s",
			c.Profile, ProfileOperator, ProfileStakeholder, ProfileBoth)
	}
	if c.Profile == ProfileStakeholder && c.TokenHash == "" {
		return fmt.Errorf(
			"dashboard.profile is %q but no stakeholder token is set (checked "+
				"the database and dashboard.token/--token) — every request "+
				"would be refused, including the SPA's own token form",
			ProfileStakeholder)
	}
	// Port 0 is legal and means "any free one" — `net.Listen` picks it, and
	// it is how a test or an ephemeral instance binds without racing another
	// process for a fixed number.
	if c.Port < 0 || c.Port > 65535 {
		return fmt.Errorf("dashboard port %d is not a port", c.Port)
	}
	return nil
}

// Addr is the host:port to listen on.
func (c Config) Addr() string {
	return fmt.Sprintf("%s:%d", c.Host, c.Port)
}

// constantTimeEqual compares two secrets without leaking their length through
// timing, the way Python's `hmac.compare_digest` does.
//
// `subtle.ConstantTimeCompare` returns 0 for different lengths without
// comparing, so the length is still observable — that is true of
// `compare_digest` too, and it is the standard trade. What both prevent is the
// byte-by-byte early exit that would let a caller discover a token one
// character at a time.
func constantTimeEqual(supplied, expected string) bool {
	return subtle.ConstantTimeCompare([]byte(supplied), []byte(expected)) == 1
}
