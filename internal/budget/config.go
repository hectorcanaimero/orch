// Package budget is the rolling-window guardrail that stops an unattended
// run from burning a provider subscription.
//
// Ported from `orchestrator/budget.py`. The shape is the same — a window per
// provider, a percentage threshold, a reset estimate, a dashboard snapshot —
// but one thing about it is deliberately different, and it is the reason this
// package matters.
//
// # One source of spend
//
// Python reads spend from two places: `state/spend-<day>.jsonl` and the
// `spend` table in `state/orch.db`. It did not always: until PR #115 it read
// only the JSONL files, so on a project using the sqlite backend — the
// default since v0.11 — the gate saw zero spend and never fired. 750,000
// tokens against a 600-token cap answered "go ahead". A guardrail that
// silently stops guarding is worse than no guardrail, because the operator
// believes they have one.
//
// Go has exactly one place spend can live: `state.Backend.SpendSince`. That
// is not a simplification for its own sake, it is the class of bug being
// designed out. There is nowhere for a spend row to hide, so no
// de-duplication between sources is needed and no second reader can fall
// behind the writer.
//
// # The clock
//
// Every method takes its `now` from an injectable clock rather than calling
// `time.Now` inline. Python calls `datetime.now()` in five places, which is
// why its tests all write spend rows relative to the real wall clock and hope
// the assertion lands before the window slides. The window vector in
// `testdata/` is generated at a frozen instant for the same reason.
package budget

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/hectorcanaimero/orch/internal/pyfmt"
	"gopkg.in/yaml.v3"
)

// ProviderBudget is one provider's rolling window.
//
// WindowHours is not a knob to tune: it is the window the provider itself
// enforces for rate limiting, so it mirrors their published numbers.
// TokenBudget and ThresholdPct are the parts an operator calibrates.
type ProviderBudget struct {
	WindowHours  float64
	TokenBudget  int
	ThresholdPct float64
}

// Cap is the token count at which dispatching stops: the budget scaled by the
// threshold percentage.
//
// A float, not an integer, because Python compares `tokens_used < cap`
// without rounding and prints `int(cap)` separately in the block reason.
// Rounding here would move the boundary by up to a token relative to Python.
func (p ProviderBudget) Cap() float64 {
	return float64(p.TokenBudget) * (p.ThresholdPct / 100.0)
}

// Config is one preset: the providers it covers and their windows.
//
// A preset naming no providers is legal and disables the gate, which is how
// an operator turns the guardrail off without deleting the file.
type Config struct {
	Providers map[string]ProviderBudget
}

// Names returns the configured provider names in sorted order.
//
// Python iterates a dict and inherits the order the YAML was written in. Go
// maps have no order at all, so anything a caller can observe — the warning
// list, the order providers are evaluated in — has to pick one. Sorted is the
// only choice that is stable across runs, which is what a test and a diff
// need. The resulting order differs from Python's for a file whose providers
// are not written alphabetically; nothing downstream depends on it, because
// the snapshot crosses the wire as a JSON object and `encoding/json` sorts
// map keys anyway.
func (c *Config) Names() []string {
	if c == nil {
		return nil
	}
	out := make([]string, 0, len(c.Providers))
	for name := range c.Providers {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// PresetNotFoundError is a budgets.yaml that exists but does not define the
// requested preset.
//
// A typo in a preset name is a config error, not a degradation case: falling
// back to some other preset would silently apply a budget the operator never
// chose. Python raises here and `orch.py` turns it into a startup failure;
// this keeps that shape, and the message word for word, because it is the
// last thing a user sees before orch exits.
type PresetNotFoundError struct {
	Path      string
	Preset    string
	Available []string
}

func (e *PresetNotFoundError) Error() string {
	available := "<none>"
	if len(e.Available) > 0 {
		available = strings.Join(e.Available, ", ")
	}
	return fmt.Sprintf("budgets preset %s not found in %s. Available: %s",
		pyfmt.Quote(e.Preset), e.Path, available)
}

// LoadConfig reads one preset out of budgets.yaml.
//
// Returns `(nil, nil)` when the file does not exist — no budgets.yaml means
// no gate, which is how every project that predates the feature keeps
// running. The caller tells the two apart by the nil config, not by an error.
//
// # Divergence: duplicate keys
//
// PyYAML silently keeps the last of two identical keys, so a budgets.yaml
// that defines `claude:` twice under one preset loads in Python with the
// second definition winning and no warning. This refuses it, naming the line.
//
// `internal/config` goes the other way for config.yaml, and the difference is
// not an inconsistency. orch itself ships a config.yaml with a duplicated
// `dashboard:` block, and `orch init` copied it into every project scaffolded
// without a template, so rejecting it would break the promise that swapping
// the binary keeps your project working. No such file was ever shipped for
// budgets.yaml. With no bad artifact in the wild to stay compatible with, the
// better behaviour wins: a budget silently taken from the wrong block is the
// same failure mode as a guardrail that does not fire.
func LoadConfig(path, preset string) (*Config, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- the budgets path comes from the operator's own config
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var file struct {
		Presets map[string]map[string]rawProvider `yaml:"presets"`
	}
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	spec, ok := file.Presets[preset]
	if !ok {
		names := make([]string, 0, len(file.Presets))
		for name := range file.Presets {
			names = append(names, name)
		}
		sort.Strings(names)
		return nil, &PresetNotFoundError{Path: path, Preset: preset, Available: names}
	}

	providers := make(map[string]ProviderBudget, len(spec))
	for name, rp := range spec {
		pb, err := rp.resolve(name, preset, path)
		if err != nil {
			return nil, err
		}
		providers[name] = pb
	}
	return &Config{Providers: providers}, nil
}

// rawProvider is the on-disk shape, with every field a pointer so a missing
// one is reported by name instead of silently defaulting to zero. A missing
// `token_budget` defaulting to 0 would read as a budget of nothing and block
// every dispatch — the loudest possible way to get a config typo wrong.
type rawProvider struct {
	WindowHours  *float64 `yaml:"window_hours"`
	TokenBudget  *float64 `yaml:"token_budget"`
	ThresholdPct *float64 `yaml:"threshold_pct"`
}

func (r rawProvider) resolve(provider, preset, path string) (ProviderBudget, error) {
	missing := func(field string) error {
		return fmt.Errorf("%s: preset %s provider %s is missing %s",
			path, pyfmt.Quote(preset), pyfmt.Quote(provider), field)
	}
	if r.WindowHours == nil {
		return ProviderBudget{}, missing("window_hours")
	}
	if r.TokenBudget == nil {
		return ProviderBudget{}, missing("token_budget")
	}
	if r.ThresholdPct == nil {
		return ProviderBudget{}, missing("threshold_pct")
	}
	return ProviderBudget{
		WindowHours: *r.WindowHours,
		// Read as a float and truncated, matching Python's
		// `int(spec["token_budget"])`, so a budget written `2.0e6` means the
		// same number in both.
		TokenBudget:  int(*r.TokenBudget),
		ThresholdPct: *r.ThresholdPct,
	}, nil
}
