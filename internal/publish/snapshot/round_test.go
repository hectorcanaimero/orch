package snapshot

import "testing"

// RoundUpToStep is exported for `internal/dashboard`'s `/stakeholder/summary`,
// which has to quote the same figure the executive summary's spend sentence
// does. Tested directly rather than only through a payload, because a caller
// outside this package can now reach it and the rounding direction is the
// whole point: a displayed spend must never UNDER-report to a stakeholder.
func TestRoundUpToStepIsExportedAndRoundsUp(t *testing.T) {
	cases := []struct {
		value float64
		want  float64
	}{
		{0, 0},
		// Anything above zero reaches the first step — never rounds down to
		// nothing, which would report a cost as free.
		{0.01, 0.5},
		{0.49, 0.5},
		{0.50, 0.5}, // already on a step, left alone
		{0.51, 1.0},
		{1.20, 1.5},
		{12.34, 12.5},
		// Non-positive collapses to 0 rather than to a negative multiple.
		{-3, 0},
	}
	for _, c := range cases {
		if got := RoundUpToStep(c.value, SpendStep); got != c.want {
			t.Errorf("RoundUpToStep(%v, %v) = %v, want %v", c.value, SpendStep, got, c.want)
		}
	}
	// The exported wrapper and the internal function are the same rule, not
	// two spellings of it.
	if RoundUpToStep(1.2, SpendStep) != roundUpToStep(1.2, SpendStep) {
		t.Error("the exported wrapper disagrees with the function it wraps")
	}
	if SpendStep != 0.50 {
		t.Errorf("SpendStep = %v; the payload and the summary sentence both assume $0.50", SpendStep)
	}
}
