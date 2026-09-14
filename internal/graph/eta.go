package graph

import (
	"math"
	"time"
)

// Projection is when the remaining work finishes at the measured pace.
type Projection struct {
	// Days is the projection in days, to one decimal.
	Days float64
	// Date is the calendar day it lands on, YYYY-MM-DD (UTC).
	Date string
	// Confidence is "high" within ProjectionTrustDays, "low" beyond.
	Confidence string
}

// ProjectionTrustDays is where a projection stops being worth trusting: beyond
// a month, a pace measured over one week is extrapolating further than it can
// see.
const ProjectionTrustDays = 30

// ProjectCompletion projects a finish date from velocity: tasks finished in
// the last windowDays, applied to the tasks that remain. It is the one
// projection every surface shows (Sprint page, stakeholder summary, published
// snapshot, PDF), so they cannot quote different finishes for the same state.
//
// nil means no projection — nothing left, or no pace measured yet — which a
// caller must show as "unknown", never as "done today".
func ProjectCompletion(remainingTasks, doneInWindow, windowDays int, now time.Time) *Projection {
	if remainingTasks <= 0 || doneInWindow <= 0 || windowDays <= 0 {
		return nil
	}
	velocity := float64(doneInWindow) / float64(windowDays)
	days := float64(remainingTasks) / velocity
	confidence := "high"
	if days > ProjectionTrustDays {
		confidence = "low"
	}
	return &Projection{
		Days:       math.Round(days*10) / 10,
		Date:       now.UTC().Add(time.Duration(days * float64(24*time.Hour))).Format("2006-01-02"),
		Confidence: confidence,
	}
}
