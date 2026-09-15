package snapshot

import "github.com/hectorcanaimero/orch/internal/model"

// Quality is how deliveries are checked before they count, in the terms a
// client reads: which kinds of check every pull request goes through, and how
// many delivered tasks passed them. No model names, no costs, no ids.
type Quality struct {
	// Gates are the kinds of check, in order: tests, typecheck, lint, build,
	// review (internal/ci.Gates).
	Gates []string `json:"gates"`
	// Delivered counts done tasks that went through a pull request.
	Delivered int `json:"delivered"`
	// Verified counts those whose CI passed.
	Verified int `json:"verified"`
	// FirstPass counts those that passed without a CI retry.
	FirstPass int `json:"first_pass"`
}

// TaskCI is one task's pull-request CI result, as the snapshot needs it.
type TaskCI struct {
	Status   string // pending, success, failure, skipped
	Attempts int    // CI retries it took
}

// buildQuality is nil when no check runs on pull requests: a project that
// does not review its work through CI has nothing to say here, and saying
// "0 verified" would read as a failure it is not.
func buildQuality(tasks []model.Task, gates []string, ci map[string]TaskCI) *Quality {
	if len(gates) == 0 {
		return nil
	}
	q := &Quality{Gates: gates}
	for _, t := range tasks {
		c, ok := ci[t.ID]
		if !ok || t.Status != model.StatusDone {
			continue
		}
		q.Delivered++
		if c.Status == "success" {
			q.Verified++
			if c.Attempts == 0 {
				q.FirstPass++
			}
		}
	}
	return q
}
