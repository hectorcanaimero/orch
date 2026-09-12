package dashboard

import (
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
