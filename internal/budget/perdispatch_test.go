package budget

import "testing"

// `escalation_allowed = ... and (budget_cap <= 0.0 or spent_so_far <
// budget_cap)` in orch.py's retry branch, isolated.
//
// The two ends are what matter. A cap of zero is "unlimited", not "nothing" —
// it is what an operator who never set `per_dispatch_usd` gets, and reading it
// as a limit would silently remove the third attempt FR-D-8 added. And the
// comparison is strict, so a task that has spent exactly the cap does not get
// the extra attempt.
func TestAllowEscalation(t *testing.T) {
	cases := []struct {
		name  string
		spent float64
		cap   float64
		want  bool
	}{
		{"no cap configured", 100, 0, true},
		{"no cap configured, nothing spent", 0, 0, true},
		{"a negative cap is still unlimited", 100, -1, true},
		{"well under", 1.5, 5, true},
		{"nothing spent yet", 0, 5, true},
		{"a hair under", 4.99, 5, true},
		// Exactly on the cap blocks: `spent_so_far < budget_cap` is false.
		{"exactly on the cap", 5, 5, false},
		{"a cent over", 5.01, 5, false},
		{"well over", 50, 5, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := AllowEscalation(c.spent, c.cap); got != c.want {
				t.Errorf("AllowEscalation(%v, %v) = %v, want %v", c.spent, c.cap, got, c.want)
			}
		})
	}
}

// The two caps read as though they disagree and do not.
//
// `AllowEscalation` allows while `spent < cap`; the rolling window blocks once
// `used >= cap`. Written that way round they look like opposite boundaries,
// and the first draft of this file asserted that they were. They are not:
// equality blocks on both sides, and the test that said otherwise is the only
// reason anyone found out.
func TestBothCapsBlockOnEquality(t *testing.T) {
	if AllowEscalation(5, 5) {
		t.Error("the per-task cap must not allow spending that has reached it")
	}
	pb := ProviderBudget{TokenBudget: 5, ThresholdPct: 100}
	if float64(5) < pb.Cap() {
		t.Error("the rolling window must not allow usage that has reached it")
	}
}
