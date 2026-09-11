package budget

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeYAML(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "budgets.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write budgets.yaml: %v", err)
	}
	return path
}

// The file orch ships. Loading it here rather than a hand-written sample means
// a preset renamed or a field dropped in the packaged YAML fails in Go, not
// only in whatever Python test happens to cover it.
func TestLoadConfigReadsTheShippedPresets(t *testing.T) {
	path := filepath.Join("..", "..", "orchestrator", "budgets.yaml")
	for _, preset := range []string{"conservative", "aggressive", "shared"} {
		cfg, err := LoadConfig(path, preset)
		if err != nil {
			t.Fatalf("LoadConfig(%q): %v", preset, err)
		}
		if cfg == nil {
			t.Fatalf("LoadConfig(%q) returned no config", preset)
		}
		want := []string{"agy", "claude", "codex", "opencode"}
		got := cfg.Names()
		if len(got) != len(want) {
			t.Fatalf("preset %q has providers %v, want %v", preset, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("preset %q provider %d = %q, want %q", preset, i, got[i], want[i])
			}
		}
		for name, pb := range cfg.Providers {
			if pb.WindowHours <= 0 {
				t.Errorf("%s/%s has window_hours %v", preset, name, pb.WindowHours)
			}
			if pb.TokenBudget <= 0 {
				t.Errorf("%s/%s has token_budget %d", preset, name, pb.TokenBudget)
			}
			if pb.ThresholdPct <= 0 || pb.ThresholdPct > 100 {
				t.Errorf("%s/%s has threshold_pct %v", preset, name, pb.ThresholdPct)
			}
		}
	}

	// The presets are meant to differ by aggressiveness, not by numbers
	// nobody checked. shared < conservative < aggressive on the threshold.
	thresholds := map[string]float64{}
	for _, preset := range []string{"shared", "conservative", "aggressive"} {
		cfg, err := LoadConfig(path, preset)
		if err != nil {
			t.Fatal(err)
		}
		thresholds[preset] = cfg.Providers["claude"].ThresholdPct
	}
	if !(thresholds["shared"] < thresholds["conservative"] &&
		thresholds["conservative"] < thresholds["aggressive"]) {
		t.Errorf("claude thresholds are not ordered shared < conservative < aggressive: %v", thresholds)
	}
}

// A missing budgets.yaml is not an error: it is how every project from before
// Sprint 7 keeps running. The caller tells it apart by the nil config.
func TestLoadConfigMissingFileDisablesTheGate(t *testing.T) {
	cfg, err := LoadConfig(filepath.Join(t.TempDir(), "nope.yaml"), "conservative")
	if err != nil {
		t.Fatalf("a missing file must not be an error: %v", err)
	}
	if cfg != nil {
		t.Fatalf("expected no config, got %+v", cfg)
	}
	if !NewGate(nil, cfg).Disabled() {
		t.Error("a nil config must disable the gate")
	}
}

// A file that exists but cannot be read is an error, not a disabled gate.
//
// The distinction matters because "no budgets.yaml" is the disabled case, and
// a path that turns out to be a directory, or that the user cannot open, is a
// mistake to report rather than a guardrail to silently drop.
func TestLoadConfigUnreadableFileIsAnError(t *testing.T) {
	// A directory: open succeeds, read fails with EISDIR.
	_, err := LoadConfig(t.TempDir(), "conservative")
	if err == nil {
		t.Fatal("expected an error for a path that is not a readable file")
	}
	if !strings.HasPrefix(err.Error(), "read ") {
		t.Errorf("message = %q, want it to say which step failed", err.Error())
	}
}

func TestLoadConfigUnknownPreset(t *testing.T) {
	path := writeYAML(t, `
presets:
  conservative:
    claude: {window_hours: 5, token_budget: 100, threshold_pct: 50}
  aggressive:
    claude: {window_hours: 5, token_budget: 100, threshold_pct: 90}
`)
	_, err := LoadConfig(path, "doesnotexist")
	if err == nil {
		t.Fatal("expected an error for an unknown preset")
	}
	var notFound *PresetNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected a *PresetNotFoundError, got %T", err)
	}
	// Python: f"budgets preset {preset!r} not found in {path}. Available: {available}"
	want := "budgets preset 'doesnotexist' not found in " + path +
		". Available: aggressive, conservative"
	if err.Error() != want {
		t.Errorf("message =\n  %q\npython writes\n  %q", err.Error(), want)
	}
}

// A file with no `presets:` key at all still reports what is available, which
// is nothing — the operator's real problem is the empty file, and "Available:
// <none>" says so where a bare "not found" would send them hunting for a typo.
func TestLoadConfigNoPresetsAtAll(t *testing.T) {
	path := writeYAML(t, "something_else: true\n")
	_, err := LoadConfig(path, "conservative")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.HasSuffix(err.Error(), "Available: <none>") {
		t.Errorf("message = %q, want it to end with \"Available: <none>\"", err.Error())
	}
}

// A preset naming no providers is legal: it disables the gate. That is how an
// operator turns the guardrail off without deleting the file, and it must not
// be confused with a preset that is missing.
func TestLoadConfigEmptyPresetDisablesTheGate(t *testing.T) {
	path := writeYAML(t, "presets:\n  conservative:\n")
	cfg, err := LoadConfig(path, "conservative")
	if err != nil {
		t.Fatalf("an empty preset must load: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected a config, got nil")
	}
	if len(cfg.Providers) != 0 {
		t.Errorf("expected no providers, got %v", cfg.Names())
	}
	if !NewGate(nil, cfg).Disabled() {
		t.Error("a preset with no providers must disable the gate")
	}
}

// Every field is required. Python raises KeyError, which names the field in a
// traceback; this names it in a sentence, with the preset and provider it
// belongs to, because the operator is looking at a YAML file and not a stack.
func TestLoadConfigMissingFieldNamesIt(t *testing.T) {
	for _, field := range []string{"window_hours", "token_budget", "threshold_pct"} {
		t.Run(field, func(t *testing.T) {
			fields := map[string]string{
				"window_hours":  "window_hours: 5",
				"token_budget":  "token_budget: 100",
				"threshold_pct": "threshold_pct: 50",
			}
			delete(fields, field)
			body := "presets:\n  conservative:\n    claude:\n"
			for _, f := range fields {
				body += "      " + f + "\n"
			}
			_, err := LoadConfig(writeYAML(t, body), "conservative")
			if err == nil {
				t.Fatalf("a missing %s must be an error, not a zero", field)
			}
			if !strings.Contains(err.Error(), field) {
				t.Errorf("message %q does not name the missing field", err.Error())
			}
			if !strings.Contains(err.Error(), "'claude'") ||
				!strings.Contains(err.Error(), "'conservative'") {
				t.Errorf("message %q does not say which preset and provider", err.Error())
			}
		})
	}
}

// `token_budget: 2.0e6` is an int in Python via `int(spec[...])`. Written as a
// float, YAML gives a float, and refusing it here would reject a file Python
// loads.
func TestLoadConfigTruncatesAFloatBudget(t *testing.T) {
	path := writeYAML(t, `
presets:
  conservative:
    opencode: {window_hours: 24, token_budget: 2.0e6, threshold_pct: 70}
`)
	cfg, err := LoadConfig(path, "conservative")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got := cfg.Providers["opencode"].TokenBudget; got != 2_000_000 {
		t.Errorf("token_budget = %d, want 2000000", got)
	}
}

// The divergence from Python, asserted so it is a decision rather than an
// accident of which YAML library was picked. PyYAML keeps the last of two
// identical keys silently; this refuses the file and names the line.
//
// See LoadConfig's doc comment for why budgets.yaml gets the strict treatment
// and config.yaml does not.
func TestLoadConfigRejectsDuplicateProviderKeys(t *testing.T) {
	path := writeYAML(t, `
presets:
  conservative:
    claude: {window_hours: 5, token_budget: 800000, threshold_pct: 60}
    claude: {window_hours: 5, token_budget: 1, threshold_pct: 60}
`)
	cfg, err := LoadConfig(path, "conservative")
	if err == nil {
		t.Fatalf("a duplicate key must be refused; loaded %v instead", cfg.Providers["claude"])
	}
	if !strings.Contains(err.Error(), "already defined") {
		t.Errorf("message %q should say which key was defined twice", err.Error())
	}
}

func TestCapScalesTheBudgetByTheThreshold(t *testing.T) {
	cases := []struct {
		budget    int
		threshold float64
		want      float64
	}{
		{100_000, 80, 80_000},
		{800_000, 60, 480_000},
		{400_000, 0, 0},
		{1000, 100, 1000},
		// The reason Cap is a float. 700,000 at 70% is 489999.99999999994,
		// not 490,000, in both languages — so `used < cap` and `used <
		// int(cap)` disagree for exactly one token, and the block reason
		// prints 489,999 while the comparison uses the longer number. Python
		// does both of those things and so does this.
		{700_000, 70, 489999.99999999994},
		{999_999, 99, 989999.01},
	}
	for _, c := range cases {
		pb := ProviderBudget{TokenBudget: c.budget, ThresholdPct: c.threshold}
		if got := pb.Cap(); got != c.want {
			t.Errorf("ProviderBudget{%d, %v}.Cap() = %v, want %v",
				c.budget, c.threshold, got, c.want)
		}
	}
}

func TestNamesIsSortedAndNilSafe(t *testing.T) {
	var nilCfg *Config
	if got := nilCfg.Names(); got != nil {
		t.Errorf("(*Config)(nil).Names() = %v, want nil", got)
	}
	cfg := &Config{Providers: map[string]ProviderBudget{
		"opencode": {}, "agy": {}, "claude": {}, "codex": {},
	}}
	want := []string{"agy", "claude", "codex", "opencode"}
	got := cfg.Names()
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Names() = %v, want %v", got, want)
		}
	}
}
