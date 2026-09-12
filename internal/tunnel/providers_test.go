package tunnel

import (
	"sort"
	"testing"
)

func TestResolveKnownProvider(t *testing.T) {
	spec, ok := Resolve(ProviderAutossh)
	if !ok {
		t.Fatal("Resolve(autossh) ok = false, want true")
	}
	if spec.Command != "autossh" {
		t.Errorf("Command = %q, want %q", spec.Command, "autossh")
	}
}

func TestResolveUnknownProvider(t *testing.T) {
	if _, ok := Resolve("cloudflared"); ok {
		t.Error("Resolve(cloudflared) ok = true, want false — not a registered provider")
	}
}

func TestKnownProvidersIsExactlyAutosshAndBore(t *testing.T) {
	got := KnownProviders()
	sort.Strings(got)
	want := []string{ProviderAutossh, ProviderBore}
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("KnownProviders() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("KnownProviders() = %v, want %v", got, want)
		}
	}
}

func TestCompileURLRegexRejectsBadPattern(t *testing.T) {
	if _, err := compileURLRegex("("); err == nil {
		t.Error("expected an error for an unbalanced regex")
	}
}

func TestCompileReconnectRegexEmptyPatternMatchesNothing(t *testing.T) {
	spec := ProviderSpec{ReconnectPattern: ""}
	re, err := compileReconnectRegex(spec)
	if err != nil {
		t.Fatal(err)
	}
	if re.MatchString("starting ssh") {
		t.Error("empty ReconnectPattern (bore) should never match")
	}
}

func TestFormatURLWholeMatchWhenNoTemplate(t *testing.T) {
	re, err := compileURLRegex(autosshURLPattern)
	if err != nil {
		t.Fatal(err)
	}
	line := "connected: https://abc123.a.pinggy.link ready"
	loc := re.FindStringSubmatchIndex(line)
	if loc == nil {
		t.Fatal("expected a match")
	}
	match := submatches(line, loc)
	got := formatURL("", re, match)
	want := "https://abc123.a.pinggy.link"
	if got != want {
		t.Errorf("formatURL() = %q, want %q", got, want)
	}
}

func TestFormatURLNamedGroupTemplateForBore(t *testing.T) {
	re, err := compileURLRegex(boreURLPattern)
	if err != nil {
		t.Fatal(err)
	}
	line := "2024-01-15T12:00:00Z INFO bore_cli::client: listening at bore.pub:41230"
	loc := re.FindStringSubmatchIndex(line)
	if loc == nil {
		t.Fatal("expected a match")
	}
	match := submatches(line, loc)
	got := formatURL(boreURLTemplate, re, match)
	want := "http://bore.pub:41230"
	if got != want {
		t.Errorf("formatURL() = %q, want %q", got, want)
	}
}

func TestAutosshDoesNotMatchBoreLine(t *testing.T) {
	re, err := compileURLRegex(autosshURLPattern)
	if err != nil {
		t.Fatal(err)
	}
	if re.MatchString("listening at bore.pub:41230") {
		t.Error("autossh's URL pattern should not match a bore line")
	}
}
