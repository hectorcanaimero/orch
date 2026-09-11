package budget

import (
	"fmt"
	"time"
)

// FormatTokensShort abbreviates a token count for a log line: 400, 1.5k,
// 400k, 2m.
//
// Ported from `_format_tokens_short`. One decimal, and none at all when the
// value is a whole number of units — `400k`, not `400.0k`. The docstring in
// Python claims `2_000_000 -> "2.0m"`; the code and its own test both say
// `2m`, and the code is what ships.
//
// A float rather than an int because the caller has a token *budget* scaled by
// a percentage, and rounding before formatting would move the number a user
// reads against the number the gate compared.
func FormatTokensShort(n float64) string {
	switch {
	case n < 1_000:
		// Truncation, not rounding — Python's `int(n)`. 999.7 is "999".
		return fmt.Sprintf("%d", int64(n))
	case n < 1_000_000:
		return unit(n/1_000, "k")
	case n < 1_000_000_000:
		return unit(n/1_000_000, "m")
	default:
		return unit(n/1_000_000_000, "b")
	}
}

func unit(v float64, suffix string) string {
	if v == float64(int64(v)) {
		return fmt.Sprintf("%d%s", int64(v), suffix)
	}
	return fmt.Sprintf("%.1f%s", v, suffix)
}

// FormatResetETA is how long until a reset, for a human: "2h 14m", "45m",
// "30s", "now", or "?" when there is no reset to wait for.
//
// A zero `resetAt` is Python's `None` — the gate has nothing to wait on, so
// the answer is "?" rather than a duration computed against the epoch.
//
// Seconds are truncated, not rounded, matching Python's `int(total_seconds())`:
// a countdown that rounds up reads as though there is more time left than
// there is.
func FormatResetETA(resetAt, now time.Time) string {
	if resetAt.IsZero() {
		return "?"
	}
	delta := int64(resetAt.Sub(now).Seconds())
	if delta <= 0 {
		return "now"
	}
	hours := delta / 3600
	minutes := (delta % 3600) / 60
	switch {
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, minutes)
	case minutes > 0:
		return fmt.Sprintf("%dm", minutes)
	default:
		return fmt.Sprintf("%ds", delta)
	}
}
