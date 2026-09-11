package budget

import (
	"strings"
	"testing"
)

// The strings are `warn_undersized_presets`'s, character for character,
// em dash included. They are what an operator reads when a run is inexplicably
// serialising, so the wording is the point of the function.
func TestWarnUndersizedPresetsMatchesPython(t *testing.T) {
	cfg := &Config{Providers: map[string]ProviderBudget{
		"codex":    {WindowHours: 3, TokenBudget: 100_000, ThresholdPct: 60},
		"claude":   {WindowHours: 5, TokenBudget: 800_000, ThresholdPct: 60},
		"opencode": {WindowHours: 24, TokenBudget: 200_000, ThresholdPct: 70},
	}}
	got := WarnUndersizedPresets(cfg, "mixed", 200_000)
	want := []string{
		"WARN: budget preset 'mixed' provider 'codex' window (100k) is smaller than 2x typical dispatch (200k) — dispatches will serialize",
		"WARN: budget preset 'mixed' provider 'opencode' window (200k) is smaller than 2x typical dispatch (200k) — dispatches will serialize",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d warnings, want %d:\n%s", len(got), len(want), strings.Join(got, "\n"))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("warning %d =\n  %q\npython writes\n  %q", i, got[i], want[i])
		}
	}
}

// The boundary, both sides of it, and the formatting oddity that lives on it.
//
// `400,000 >= 2 * 200,000` does not warn. One token less does — and prints the
// window as "400.0k", because 399.999k is not a whole number of k so the
// decimal is kept. The message then reads as though 400.0k were smaller than
// 200k doubled, which is confusing and is exactly what Python prints. Copied,
// not corrected: this is the line `scripts/parity.sh` would diff.
func TestWarnUndersizedPresetsAtTheBoundary(t *testing.T) {
	exactly := &Config{Providers: map[string]ProviderBudget{
		"a": {WindowHours: 5, TokenBudget: 400_000, ThresholdPct: 60},
	}}
	if got := WarnUndersizedPresets(exactly, "boundary", 200_000); len(got) != 0 {
		t.Errorf("exactly 2x must not warn, got %v", got)
	}

	justUnder := &Config{Providers: map[string]ProviderBudget{
		"b": {WindowHours: 5, TokenBudget: 399_999, ThresholdPct: 60},
	}}
	got := WarnUndersizedPresets(justUnder, "b'oundary", 200_000)
	if len(got) != 1 {
		t.Fatalf("one token under 2x must warn, got %v", got)
	}
	// The preset name carries a single quote, so Python's repr flips to double
	// quotes — which is why this goes through pyfmt.Quote and not %q.
	want := `WARN: budget preset "b'oundary" provider 'b' window (400.0k) is smaller than 2x typical dispatch (200k) — dispatches will serialize`
	if got[0] != want {
		t.Errorf("warning =\n  %q\npython writes\n  %q", got[0], want)
	}
}

// Nothing to compare against means nothing to say. An estimate of zero is what
// a config without `typical_dispatch_tokens` produces, and inventing a default
// would warn about a number the operator never set.
func TestWarnUndersizedPresetsStaysQuiet(t *testing.T) {
	cfg := &Config{Providers: map[string]ProviderBudget{
		"codex": {WindowHours: 3, TokenBudget: 1, ThresholdPct: 60},
	}}
	for _, tc := range []struct {
		name    string
		cfg     *Config
		typical int
	}{
		{"nil config", nil, 200_000},
		{"no providers", &Config{}, 200_000},
		{"zero estimate", cfg, 0},
		{"negative estimate", cfg, -1},
	} {
		if got := WarnUndersizedPresets(tc.cfg, "x", tc.typical); len(got) != 0 {
			t.Errorf("%s: got %v, want nothing", tc.name, got)
		}
	}
}

// Sorted, not map order — the one place this deliberately differs from Python,
// which inherits the order the YAML was written in. Two runs of the same
// binary on the same config must print the same lines in the same order, or
// the output is not something anyone can diff.
func TestWarnUndersizedPresetsIsOrdered(t *testing.T) {
	cfg := &Config{Providers: map[string]ProviderBudget{
		"opencode": {TokenBudget: 1},
		"agy":      {TokenBudget: 1},
		"codex":    {TokenBudget: 1},
		"claude":   {TokenBudget: 1},
	}}
	first := WarnUndersizedPresets(cfg, "p", 100)
	if len(first) != 4 {
		t.Fatalf("expected four warnings, got %d", len(first))
	}
	for i, name := range []string{"agy", "claude", "codex", "opencode"} {
		if !strings.Contains(first[i], "provider '"+name+"'") {
			t.Errorf("warning %d is %q, want it to be about %q", i, first[i], name)
		}
	}
	// Go randomises map iteration, so a single run agreeing is not evidence.
	for i := 0; i < 20; i++ {
		again := WarnUndersizedPresets(cfg, "p", 100)
		for j := range first {
			if again[j] != first[j] {
				t.Fatalf("run %d differs at line %d:\n  %q\n  %q", i, j, again[j], first[j])
			}
		}
	}
}

// The packaged presets against the packaged estimate: the shipped defaults
// must not warn about themselves.
//
// `typical_dispatch_tokens: 200000` is what config.yaml ships, and codex's
// 400,000 window is exactly 2x it — sitting on the boundary this function
// tests. A preset tuned one token lower, or an estimate raised, starts warning
// every operator on startup, and this says so before they see it.
func TestShippedPresetsDoNotWarn(t *testing.T) {
	for _, preset := range []string{"conservative", "aggressive", "shared"} {
		cfg, err := LoadConfig("../../orchestrator/budgets.yaml", preset)
		if err != nil {
			t.Fatalf("LoadConfig(%q): %v", preset, err)
		}
		if got := WarnUndersizedPresets(cfg, preset, 200_000); len(got) != 0 {
			t.Errorf("the shipped %q preset warns about itself:\n%s",
				preset, strings.Join(got, "\n"))
		}
	}
}
