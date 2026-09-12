package dashboard

// MilestoneETADate is `milestoneETA`'s date, for a caller outside this
// package. Empty string when there is no projection — nothing remaining, or
// no velocity to project with — which is the same "—" the panel renders and a
// different claim from a date.
//
// Exported as a thin wrapper rather than by moving `milestoneETA` somewhere
// neutral: the arithmetic lives here because it is what `/api/milestones`
// computes, and a package holding one function would be a worse home than the
// one consumer it already has. `orch notify digest` is the second caller
// (G6.7); a third is when moving it starts to pay, and by then there will be
// three call sites to say where.
//
// `today` is a parameter, not `time.Now()`, for the reason the unexported one
// takes it: an ETA is a date, and a function that reads the clock itself can
// only be tested by the clock.
func MilestoneETADate(remaining int, velocityPerDay float64, today, targetDate string) string {
	eta := milestoneETA(remaining, velocityPerDay, today, targetDate)
	if eta == nil {
		return ""
	}
	return eta.ETADate
}

// VelocityWindowDays is the window `velocity_per_day` averages over, exposed
// so a caller computing the same figure from its own `CountDoneLastNDays`
// divides by the same number the dashboard does rather than by a 7 it typed
// itself.
const VelocityWindowDays = velocityWindowDays
