package budget

import (
	"testing"
	"time"
)

// Every expectation is what `orchestrator.budget._format_tokens_short`
// printed for the same input, not what this implementation happens to do.
//
// Three of them are the reason the table exists rather than a handful of
// obvious cases:
//
//   - 999,999 is "1000.0k", not "1m". The branch is chosen by comparing
//     against 1,000,000 before dividing, so a value just under it takes the
//     k branch and then rounds up past 1000.
//   - 489999.99999999994 — a real cap, 700,000 at 70% — is "490.0k", not
//     "490k": the "drop the decimal when it is whole" check runs on the
//     unrounded quotient, which is not whole.
//   - 999.7 is "999". Sub-thousand values truncate rather than round.
//
// None of those would survive a reimplementation from the description.
func TestFormatTokensShortMatchesPython(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "0"},
		{1, "1"},
		{400, "400"},
		{999, "999"},
		{999.7, "999"},
		{0.5, "0"},
		{1_000, "1k"},
		{1_500, "1.5k"},
		{1234.5, "1.2k"},
		{100_000, "100k"},
		{200_000, "200k"},
		{400_000, "400k"},
		{489999.99999999994, "490.0k"},
		{999_999, "1000.0k"},
		{1_000_000, "1m"},
		{2_000_000, "2m"},
		{2_500_000, "2.5m"},
		{1_000_000_000, "1b"},
		{2_500_000_000, "2.5b"},
	}
	for _, c := range cases {
		if got := FormatTokensShort(c.in); got != c.want {
			t.Errorf("FormatTokensShort(%v) = %q, python prints %q", c.in, got, c.want)
		}
	}
}

// Same provenance: `_format_reset_eta(now + delta, now=now)`.
//
// The zero time stands in for Python's None. "1h 0m" rather than "1h" and
// "now" for a reset already in the past are both Python's, and both are the
// kind of detail a caller ends up depending on for its log lines.
func TestFormatResetETAMatchesPython(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		delta time.Duration
		want  string
	}{
		{2*time.Hour + 14*time.Minute, "2h 14m"},
		{time.Hour, "1h 0m"},
		{25*time.Hour + time.Minute, "25h 1m"},
		{45 * time.Minute, "45m"},
		{90 * time.Second, "1m"},
		{59 * time.Second, "59s"},
		{30 * time.Second, "30s"},
		{0, "now"},
		{-5 * time.Minute, "now"},
	}
	for _, c := range cases {
		if got := FormatResetETA(now.Add(c.delta), now); got != c.want {
			t.Errorf("FormatResetETA(now%+v) = %q, python prints %q", c.delta, got, c.want)
		}
	}

	if got := FormatResetETA(time.Time{}, now); got != "?" {
		t.Errorf("FormatResetETA(zero) = %q, python prints %q for None", got, "?")
	}
}

// The formatter reads a Decision straight out of the gate, so the two have to
// agree about what "no reset" looks like. A gate that answers OK leaves
// ResetAt zero; that must print "?" and not a duration measured from year 1.
func TestFormatResetETAOnACleanDecision(t *testing.T) {
	g := gateAt(t, &fakeSpend{}, oneProvider(ProviderBudget{
		WindowHours: 5, TokenBudget: 1000, ThresholdPct: 60,
	}), "2026-09-02T12:00:00Z")

	d, err := g.CanDispatch(t.Context(), "claude")
	if err != nil {
		t.Fatal(err)
	}
	if !d.OK {
		t.Fatal("an empty window must not block")
	}
	if got := FormatResetETA(d.ResetAt, g.now()); got != "?" {
		t.Errorf("FormatResetETA on an OK decision = %q, want %q", got, "?")
	}
}
