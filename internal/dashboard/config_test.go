package dashboard

import (
	"net"
	"strings"
	"testing"
)

// An unknown profile must never produce a working server, whichever path set
// it. FromConfig catches the config.yaml spelling; this catches the one a flag
// can set directly — `orch dashboard --profile stakholder`.
//
// It is not an inert setting. `decide` reads a profile it does not recognise
// as "no stakeholder context", which is ALLOW EVERYTHING: a misspelled flag
// meant to restrict access would open it, and the symptom is a page that
// works. Found by a testscript that hung instead of failing, because the
// server started and served.
func TestValidateRejectsAnUnknownProfile(t *testing.T) {
	c := Config{Profile: Profile("stakholder"), Token: "test-token-stakeholder",
		TokenHash: HashToken("test-token-stakeholder"), Host: DefaultHost, Port: DefaultPort}
	err := c.Validate()
	if err == nil {
		t.Fatal("a misspelled profile validated; it would serve everything unguarded")
	}
	if !strings.Contains(err.Error(), "not one of") {
		t.Errorf("err = %q, want it to name the three legal profiles", err)
	}

	// And the three real ones still pass.
	for _, p := range []Profile{ProfileOperator, ProfileStakeholder, ProfileBoth} {
		ok := Config{Profile: p, Token: "test-token-stakeholder",
			TokenHash: HashToken("test-token-stakeholder"), Host: DefaultHost, Port: DefaultPort}
		if err := ok.Validate(); err != nil {
			t.Errorf("profile %q: %v", p, err)
		}
	}

	// An empty profile is not "the default" at this layer — FromConfig is
	// where the default is applied, and a Config built by hand with no
	// profile is a caller that forgot one.
	empty := Config{Token: "t", TokenHash: HashToken("t"), Host: DefaultHost, Port: DefaultPort}
	if err := empty.Validate(); err == nil {
		t.Error("an empty profile validated; FromConfig is what defaults it")
	}
}

// TokenHash is what Validate() actually checks — G8.2 moved the effective
// token into the database, resolved before Validate() runs (see
// newDashboardCmd), so a non-empty Token with no TokenHash is exactly the
// shape a caller gets wrong by forgetting that resolution step.
func TestValidateChecksTokenHashNotToken(t *testing.T) {
	c := Config{Profile: ProfileStakeholder, Token: "test-token-stakeholder",
		Host: DefaultHost, Port: DefaultPort}
	err := c.Validate()
	if err == nil {
		t.Fatal("Token set but TokenHash empty validated; every request would 401")
	}
	if !strings.Contains(err.Error(), "no stakeholder token") {
		t.Errorf("err = %q, want it to say no token is set", err)
	}

	// A database-sourced token has no plaintext Token at all, and that is
	// exactly the case this whole feature exists for.
	c.Token = ""
	c.TokenHash = HashToken("whatever-was-in-the-database")
	if err := c.Validate(); err != nil {
		t.Errorf("TokenHash set from the database, empty Token: %v", err)
	}
}

// TestAddrBracketsAnIPv6Literal is the case `fmt.Sprintf("%s:%d")` got wrong.
//
// It produced ":::7420" for `--host ::`, which net.Listen refuses with "too
// many colons in address". Unreachable while every host anyone passed was v4;
// `--allow-remote` (G8.5) is what made binding `::` something an operator can
// ask for, so the bug became reachable at the same moment.
//
// The v4 and empty cases are here because JoinHostPort must not change them:
// an empty host is how "every interface" is spelled today, and rewriting it
// would move a listener nobody asked to move.
func TestAddrBracketsAnIPv6Literal(t *testing.T) {
	cases := []struct {
		host string
		port int
		want string
	}{
		{"127.0.0.1", 7420, "127.0.0.1:7420"},
		{"0.0.0.0", 7420, "0.0.0.0:7420"},
		{"", 7420, ":7420"},
		{"localhost", 7420, "localhost:7420"},
		{"::", 7420, "[::]:7420"},
		{"::1", 7420, "[::1]:7420"},
		{"fe80::1", 0, "[fe80::1]:0"},
	}
	for _, tc := range cases {
		got := Config{Host: tc.host, Port: tc.port}.Addr()
		if got != tc.want {
			t.Errorf("Config{Host: %q, Port: %d}.Addr() = %q, want %q",
				tc.host, tc.port, got, tc.want)
		}
		// The point is not the string, it is that a listener accepts it.
		if _, _, err := net.SplitHostPort(got); err != nil {
			t.Errorf("Addr() = %q, which net cannot parse: %v", got, err)
		}
	}
}
