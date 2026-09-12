package project

import (
	"sort"
	"strconv"
	"time"

	"github.com/hectorcanaimero/orch/internal/state"
)

// Per-task figures derived from the event log. Ported from
// `human_hours_by_task` and `last_updated_by_task` in
// `orchestrator/dashboard/metrics.py`.
//
// They live here rather than in the dashboard because they are facts about a
// project, not about a page — `orch status` has the same column.

// terminalEvents close an interval a `dispatch` opened.
var terminalEvents = map[string]bool{
	"success": true,
	"fail":    true,
	"timeout": true,
}

// noTaskID is the placeholder a project-wide event carries in `task_id`.
// Counting it would produce a task called "-" with hours on it.
const noTaskID = "-"

// HumanHoursByTask sums wall-clock hours per task from dispatch → terminal
// pairs. Retries stack: three attempts contribute three intervals.
//
// When the terminal event records `duration_s`, that is preferred over the
// wall-clock delta — it excludes the time the task spent queued, which is the
// difference between "how long did this take" and "how long was it in the
// system". Zero or unparseable falls back to the delta.
//
// A task with no closed interval is absent from the map rather than present
// with 0.0. The distinction is real: "never dispatched" and "took no time" are
// different facts, and a column that renders 0.0 for the first one is wrong.
func HumanHoursByTask(events []state.Event) map[string]float64 {
	byTask := map[string][]state.Event{}
	for _, e := range events {
		if e.TaskID == "" || e.TaskID == noTaskID {
			continue
		}
		byTask[e.TaskID] = append(byTask[e.TaskID], e)
	}

	out := map[string]float64{}
	for id, evs := range byTask {
		// Sorted by the timestamp STRING, like Python. ISO-8601 in UTC sorts
		// lexicographically the same as chronologically, and a malformed
		// timestamp then sorts somewhere harmless instead of failing a parse.
		sort.SliceStable(evs, func(i, j int) bool { return evs[i].TS < evs[j].TS })

		var (
			totalSeconds float64
			dispatchedAt time.Time
			pending      bool
		)
		for _, e := range evs {
			switch {
			case e.EventType == "dispatch":
				dispatchedAt, pending = parseTS(e.TS)
			case terminalEvents[e.EventType]:
				if d, ok := durationSeconds(e); ok && d > 0 {
					totalSeconds += d
					pending = false
					continue
				}
				if pending {
					if end, ok := parseTS(e.TS); ok {
						if delta := end.Sub(dispatchedAt).Seconds(); delta > 0 {
							totalSeconds += delta
						}
					}
					pending = false
				}
			}
		}
		if totalSeconds > 0 {
			out[id] = round3(totalSeconds / 3600.0)
		}
	}
	return out
}

// LastUpdatedByTask is the newest event timestamp per task.
//
// Compared as strings, which is what Python does and what the storage format
// makes correct: the timestamps are ISO-8601 UTC and sort lexicographically.
// It also means a row whose timestamp is malformed does not take the whole
// map down with it.
func LastUpdatedByTask(events []state.Event) map[string]string {
	out := map[string]string{}
	for _, e := range events {
		if e.TaskID == "" || e.TaskID == noTaskID || e.TS == "" {
			continue
		}
		if e.TS > out[e.TaskID] {
			out[e.TaskID] = e.TS
		}
	}
	return out
}

// durationSeconds reads `extra.duration_s`, tolerating the number arriving as
// a JSON number or as a string — the event log has both, because `extra` is
// whatever the backend that wrote it put there.
func durationSeconds(e state.Event) (float64, bool) {
	raw, ok := e.Extra["duration_s"]
	if !ok {
		return 0, false
	}
	switch v := raw.(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case string:
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}

// parseTS accepts the timestamp forms the event log holds: a trailing Z, an
// explicit offset, or neither (read as UTC, like Python's `_parse_ts`).
func parseTS(ts string) (time.Time, bool) {
	if ts == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, ts); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// round3 reproduces Python's `round(x, 3)`.
//
// Not `math.Round(x*1000)/1000`: that rounds half away from zero and rounds
// the SCALED value, so it disagrees with Python twice over. Formatting to
// three decimals and reading back is correctly-rounded half-to-even on the
// exact binary value, which is what CPython's round does.
func round3(v float64) float64 {
	f, err := strconv.ParseFloat(strconv.FormatFloat(v, 'f', 3, 64), 64)
	if err != nil {
		return v
	}
	return f
}
