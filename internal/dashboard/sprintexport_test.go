package dashboard

import "testing"

// The wrapper's own contract is the nil case: `milestoneETA` returns a
// *etaPayload and nil means "no projection", which callers outside this
// package cannot see. Flattening that to "" is new behaviour and this is
// what pins it — an empty string is the "—" the panel renders, and a
// different claim from a date.
//
// The arithmetic itself is held to Python by TestMilestoneETAMatchesThePythonGolden;
// this only checks the translation at the boundary.
func TestMilestoneETADate(t *testing.T) {
	cases := []struct {
		name       string
		remaining  int
		velocity   float64
		today      string
		targetDate string
		want       string
	}{
		{"nothing left", 0, 2, "2026-09-12", "", ""},
		{"no velocity to project with", 5, 0, "2026-09-12", "", ""},
		{"an unparseable today", 5, 1, "not-a-date", "", ""},
		// 5 tasks at 2/day is 2.5 days, ceiled to 3.
		{"a real projection", 5, 2, "2026-09-12", "", "2026-09-15"},
		// The target date changes the confidence, never the date itself.
		{"a target date does not move it", 5, 2, "2026-09-12", "2026-10-01", "2026-09-15"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MilestoneETADate(tc.remaining, tc.velocity, tc.today, tc.targetDate); got != tc.want {
				t.Errorf("MilestoneETADate(%d, %v, %q, %q) = %q, want %q",
					tc.remaining, tc.velocity, tc.today, tc.targetDate, got, tc.want)
			}
		})
	}
}

// VelocityWindowDays is exported so a caller computing velocity from its own
// CountDoneLastNDays divides by the same number the dashboard does. If the two
// ever differ, `orch notify digest` and /api/sprint project different dates
// from the same rows.
func TestVelocityWindowDaysIsTheOneTheDashboardUses(t *testing.T) {
	if VelocityWindowDays != velocityWindowDays {
		t.Errorf("VelocityWindowDays = %d, velocityWindowDays = %d",
			VelocityWindowDays, velocityWindowDays)
	}
}
