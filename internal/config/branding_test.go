package config

import (
	"strings"
	"testing"
)

// A colour that is not a colour is a STARTUP error rather than a value quietly
// dropped, and that asymmetry is the point: branding that does not apply is
// invisible. An operator who wrote `accent_color: blue` and saw no blue would
// go looking at the SPA, the PDF and their browser cache before suspecting the
// spelling.
func TestBrandingAccentColourMustBeHex(t *testing.T) {
	for _, tc := range []struct {
		in string
		ok bool
	}{
		{"", true}, // absent is the common case
		{"#ff6600", true},
		{"#FF6600", true},
		{"#f60", true},
		{"  #f60  ", true}, // trimmed, not rejected
		{"blue", false},
		{"ff6600", false}, // no hash
		{"#ff66", false},
		{"#gggggg", false},
		{"rgb(255,102,0)", false},
	} {
		b := Branding{AccentColor: tc.in}
		err := validateBranding(&b)
		if tc.ok && err != nil {
			t.Errorf("accent_color %q was rejected: %v", tc.in, err)
		}
		if !tc.ok {
			if err == nil {
				t.Errorf("accent_color %q was accepted", tc.in)
				continue
			}
			// The message has to name the key and the two legal spellings,
			// because it is read by somebody who cannot see the problem.
			if !strings.Contains(err.Error(), "accent_color") ||
				!strings.Contains(err.Error(), "#rgb") {
				t.Errorf("accent_color %q: %v", tc.in, err)
			}
		}
	}
}

// The strings are trimmed, because a trailing space in a YAML value is
// invisible and would become a visible gap on the page.
func TestBrandingTrimsItsStrings(t *testing.T) {
	b := Branding{Name: "  Acme  ", Logo: " logo.png ", Footer: "  Confidential "}
	if err := validateBranding(&b); err != nil {
		t.Fatal(err)
	}
	if b.Name != "Acme" || b.Logo != "logo.png" || b.Footer != "Confidential" {
		t.Errorf("got %+v", b)
	}
}

// The zero block is the "no branding" case every surface has to recognise, so
// it has a name rather than four comparisons repeated at each site.
func TestBrandingConfigured(t *testing.T) {
	if (Branding{}).Configured() {
		t.Error("an empty block reports itself as configured")
	}
	for _, b := range []Branding{
		{Name: "Acme"}, {Logo: "a.png"}, {AccentColor: "#f60"}, {Footer: "x"},
	} {
		if !b.Configured() {
			t.Errorf("%+v does not report itself as configured", b)
		}
	}
}

// The four keys are known, so a project that sets them does not get an
// "unknown key" warning telling it the feature does not exist.
func TestBrandingKeysAreKnown(t *testing.T) {
	for _, k := range []string{
		"presentation.branding.name", "presentation.branding.logo",
		"presentation.branding.accent_color", "presentation.branding.footer",
	} {
		if !knownKeys[k] {
			t.Errorf("%s is not in knownKeys; a project setting it would be warned about", k)
		}
	}
}
