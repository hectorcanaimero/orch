package budget

import (
	"fmt"

	"github.com/hectorcanaimero/orch/internal/pyfmt"
)

// WarnUndersizedPresets returns one warning per provider whose window cannot
// fit two dispatches back to back.
//
// A provider with `token_budget < 2 * typicalDispatchTokens` serialises work:
// the second dispatch sits in the budget queue until the first falls out of
// the rolling window. That is almost always a misconfiguration — stale preset
// numbers, or a `typical_dispatch_tokens` estimate in config.yaml that has not
// kept up with how large prompts have got — and it presents as "orch is slow",
// which is the hardest thing to trace back to a config file.
//
// Returns rather than logs. Python does both, and the log line is the part
// that actually reaches an operator; here the caller owns output, so a CLI can
// print to stderr and a test can assert on the strings without a log hook.
//
// A nil config, no providers, or a non-positive estimate all yield nothing:
// with no estimate there is nothing to compare against, and inventing a
// default would produce warnings about a number the operator never set.
func WarnUndersizedPresets(cfg *Config, presetName string, typicalDispatchTokens int) []string {
	if cfg == nil || len(cfg.Providers) == 0 || typicalDispatchTokens <= 0 {
		return nil
	}

	twoX := 2 * typicalDispatchTokens
	var out []string
	// Sorted, so the warnings come out in the same order every run — Python
	// inherits YAML order here, which is stable per file but not comparable
	// between one config and another.
	for _, provider := range cfg.Names() {
		pb := cfg.Providers[provider]
		if pb.TokenBudget >= twoX {
			continue
		}
		out = append(out, fmt.Sprintf(
			"WARN: budget preset %s provider %s window (%s) is smaller than "+
				"2x typical dispatch (%s) — dispatches will serialize",
			pyfmt.Quote(presetName),
			pyfmt.Quote(provider),
			FormatTokensShort(float64(pb.TokenBudget)),
			FormatTokensShort(float64(typicalDispatchTokens)),
		))
	}
	return out
}
