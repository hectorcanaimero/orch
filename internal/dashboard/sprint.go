package dashboard

import (
	"math"
	"sort"
	"time"

	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/state"
)

// Sprint health and milestone projection, ported from the F-5 and G-3 half of
// `orchestrator/dashboard/metrics.py`.
//
// Two ETAs live here and they are different calculations with similar names.
// This one projects from VELOCITY: tasks finished per day over a rolling
// window, applied to the task count that remains. The other — Python's
// `eta_hours_remaining` — projects from HOURS, scaling the plan's estimates by
// how far past them the finished work ran. Only the first is on any endpoint
// this package serves; the second belongs to the stakeholder snapshot.

// velocityWindowDays is the rolling window velocity is measured over. Seven
// days, so a week with a public holiday in it reads as a slower week rather
// than as a stalled project.
const velocityWindowDays = 7

// etaConfidenceDays is where a projection stops being worth trusting. Beyond a
// month, a velocity measured over one week is extrapolating further than it
// can see.
const etaConfidenceDays = 30

// sprintPayload is `/api/sprint`'s body.
//
// `available: false` with a reason is a first-class answer, not an error: the
// file backend has no query for any of this, and the SPA says so rather than
// drawing a panel of zeroes.
type sprintPayload struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`

	VelocityPerDay float64 `json:"velocity_per_day"`
	DoneCount      int     `json:"done_count"`
	RemainingTasks int     `json:"remaining_tasks"`
	RemainingHours float64 `json:"remaining_hours"`
	BlockedCount   int     `json:"blocked_count"`

	ETADays    *float64 `json:"eta_days"`
	ETADate    *string  `json:"eta_date"`
	Confidence string   `json:"confidence"`

	Blockers []blockerRow `json:"blockers"`
}

// blockerRow is one blocked task with the reason it stopped.
type blockerRow struct {
	TaskID        string  `json:"task_id"`
	Title         string  `json:"title"`
	Phase         int     `json:"phase"`
	Reason        string  `json:"reason"`
	BlockedAt     *string `json:"blocked_at"`
	EstimateHours float64 `json:"estimate_hours"`
}

// blockerReasonLimit is Python's `reason[:300]`. A reason is a sentence; a
// stack trace pasted into one is not, and the panel is a list, not a log.
const blockerReasonLimit = 300

// sprintHealth computes the payload. Pure: the caller supplies the two figures
// only the database can answer, which is what lets the whole thing be a table
// test with no database in it.
//
// `now` is a parameter for the same reason: an ETA is a date, and a function
// that reads the clock itself can only be tested by the clock.
func sprintHealth(tasks []model.Task, done7d int, lastEvents map[string]state.Event, now time.Time) sprintPayload {
	var (
		doneCount      int
		remainingTasks int
		remainingHours float64
		blocked        []model.Task
	)
	for _, t := range tasks {
		switch t.Status {
		case model.StatusDone:
			doneCount++
		case model.StatusBlocked:
			blocked = append(blocked, t)
		default:
			// Everything that is neither done nor blocked is work still to
			// do. Python also excludes a "skipped" status here; it is not in
			// the Go status set, so there is nothing to exclude.
			remainingTasks++
			remainingHours += t.EstimateHours
		}
	}

	velocity := 0.0
	if velocityWindowDays > 0 {
		velocity = float64(done7d) / float64(velocityWindowDays)
	}

	out := sprintPayload{
		Available:      true,
		VelocityPerDay: round2(velocity),
		DoneCount:      doneCount,
		RemainingTasks: remainingTasks,
		RemainingHours: round1(remainingHours),
		BlockedCount:   len(blocked),
		Confidence:     "none",
		Blockers:       blockers(blocked, lastEvents),
	}

	// No velocity or nothing left means no projection — and no projection is
	// reported as null rather than as zero days, because "done today" and "no
	// idea" are opposite claims.
	if velocity > 0 && remainingTasks > 0 {
		etaDays := float64(remainingTasks) / velocity
		etaDate := now.UTC().Add(time.Duration(etaDays * float64(24*time.Hour))).Format("2006-01-02")
		rounded := round1(etaDays)
		out.ETADays = &rounded
		out.ETADate = &etaDate
		out.Confidence = confidenceFor(etaDays)
	}
	return out
}

func confidenceFor(etaDays float64) string {
	if etaDays <= etaConfidenceDays {
		return "high"
	}
	return "low"
}

// blockers renders the blocked tasks, ordered by phase.
//
// The reason comes from the task's last event: its `extra.reason` if it has
// one, otherwise the event type, otherwise "unknown". A task blocked with no
// event at all still appears — it is blocked, and saying so with "unknown" is
// more use than leaving it off the list.
func blockers(blocked []model.Task, lastEvents map[string]state.Event) []blockerRow {
	sort.SliceStable(blocked, func(i, j int) bool { return blocked[i].Phase < blocked[j].Phase })

	out := make([]blockerRow, 0, len(blocked))
	for _, t := range blocked {
		ev, hasEvent := lastEvents[t.ID]
		reason := extraString(ev.Extra, "reason")
		if reason == "" {
			reason = ev.EventType
		}
		if reason == "" {
			reason = "unknown"
		}
		if len(reason) > blockerReasonLimit {
			reason = reason[:blockerReasonLimit]
		}
		var blockedAt *string
		if hasEvent && ev.TS != "" {
			ts := ev.TS
			blockedAt = &ts
		}
		out = append(out, blockerRow{
			TaskID: t.ID, Title: titleOr(t), Phase: t.Phase,
			Reason: reason, BlockedAt: blockedAt, EstimateHours: t.EstimateHours,
		})
	}
	return out
}

func titleOr(t model.Task) string {
	if t.Title != "" {
		return t.Title
	}
	return t.ID
}

// milestoneETA projects a completion date for one milestone.
//
// `today` is a parameter rather than a clock read, so the projection is
// testable, and `eta_days` is a CEILING: a milestone needing 1.2 days of work
// is not finishing today.
//
// Confidence is "high" when the projection lands on or before the target date,
// and when there is no target, when it lands within 30 days. A target date
// that does not parse falls back to the 30-day rule rather than failing — a
// typo in a date should cost a comparison, not a panel.
//
// nil means no projection: nothing remaining, or no velocity to project with.
// The UI renders "—", which is honest, where a date would not be.
func milestoneETA(remaining int, velocityPerDay float64, today string, targetDate string) *etaPayload {
	if remaining <= 0 || velocityPerDay <= 0 {
		return nil
	}
	etaDays := int(math.Ceil(float64(remaining) / velocityPerDay))

	start, err := time.Parse("2006-01-02", today)
	if err != nil {
		return nil
	}
	etaDate := start.AddDate(0, 0, etaDays)

	confidence := "low"
	if targetDate != "" {
		target, terr := time.Parse("2006-01-02", targetDate)
		switch {
		case terr != nil:
			confidence = confidenceFor(float64(etaDays))
		case !etaDate.After(target):
			confidence = "high"
		}
	} else {
		confidence = confidenceFor(float64(etaDays))
	}

	return &etaPayload{
		ETADate:    etaDate.Format("2006-01-02"),
		ETADays:    etaDays,
		Confidence: confidence,
	}
}

type etaPayload struct {
	ETADate    string `json:"eta_date"`
	ETADays    int    `json:"eta_days"`
	Confidence string `json:"confidence"`
}

// round2 is Python's `round(x, 2)`. See project.round3 for why this spelling.
func round2(v float64) float64 {
	return roundDecimals(v, 2)
}
