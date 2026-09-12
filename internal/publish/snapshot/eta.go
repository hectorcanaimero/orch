package snapshot

import "github.com/hectorcanaimero/orch/internal/model"

// etaHoursRemaining ports `eta_hours_remaining` (orchestrator/dashboard/
// metrics.py): the plan's remaining estimate, scaled by how the team's
// actual pace on DONE tasks compared to their own estimates.
//
// nil means "no signal" — nothing left, or nothing finished yet to measure a
// pace from — and the caller renders that as "—", never as 0h.
func etaHoursRemaining(tasks []model.Task, humanHoursByID map[string]float64) *float64 {
	var remainingEst float64
	for _, t := range tasks {
		if t.Status != model.StatusDone {
			remainingEst += t.EstimateHours
		}
	}
	if remainingEst <= 0 {
		return nil
	}

	var doneEst, doneActual float64
	for _, t := range tasks {
		if t.Status != model.StatusDone {
			continue
		}
		doneEst += t.EstimateHours
		doneActual += humanHoursByID[t.ID]
	}
	if doneEst <= 0 || doneActual <= 0 {
		return &remainingEst
	}

	eta := remainingEst * (doneActual / doneEst)
	return &eta
}
