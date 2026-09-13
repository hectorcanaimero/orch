package project

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/hectorcanaimero/orch/internal/state"
)

func ev(taskID, eventType, ts string, extra map[string]any) state.Event {
	return state.Event{TaskID: taskID, EventType: eventType, TS: ts, Backend: "claude", Extra: extra}
}

// eventScenarios mirrors SCENARIOS in testdata/make-events-golden.py, name for
// name. The golden next to it is what Python answers for each.
func eventScenarios() map[string][]state.Event {
	return map[string][]state.Event{
		"empty": {},
		"wall-clock": {
			ev("A", "dispatch", "2026-09-01T10:00:00Z", nil),
			ev("A", "success", "2026-09-01T11:30:00Z", nil),
		},
		"duration-beats-wall-clock": {
			ev("A", "dispatch", "2026-09-01T10:00:00Z", nil),
			ev("A", "success", "2026-09-01T12:00:00Z", map[string]any{"duration_s": 600.0}),
		},
		"retries-stack": {
			ev("A", "dispatch", "2026-09-01T10:00:00Z", nil),
			ev("A", "fail", "2026-09-01T10:30:00Z", nil),
			ev("A", "dispatch", "2026-09-01T11:00:00Z", nil),
			ev("A", "timeout", "2026-09-01T11:15:00Z", nil),
			ev("A", "dispatch", "2026-09-01T12:00:00Z", nil),
			ev("A", "success", "2026-09-01T12:45:00Z", nil),
		},
		"unpaired": {
			ev("A", "success", "2026-09-01T10:00:00Z", nil),
			ev("B", "dispatch", "2026-09-01T10:00:00Z", nil),
		},
		"out-of-order": {
			ev("A", "success", "2026-09-01T11:00:00Z", nil),
			ev("A", "dispatch", "2026-09-01T10:00:00Z", nil),
		},
		"bad-durations": {
			ev("A", "dispatch", "2026-09-01T10:00:00Z", nil),
			ev("A", "success", "2026-09-01T10:30:00Z", map[string]any{"duration_s": 0.0}),
			ev("B", "dispatch", "2026-09-01T10:00:00Z", nil),
			ev("B", "success", "2026-09-01T10:30:00Z", map[string]any{"duration_s": "not a number"}),
			ev("C", "dispatch", "2026-09-01T10:00:00Z", nil),
			ev("C", "success", "2026-09-01T10:30:00Z", map[string]any{"duration_s": "1800"}),
		},
		"negative-delta": {
			ev("A", "dispatch", "2026-09-01T11:00:00Z", nil),
			ev("A", "fail", "2026-09-01T11:00:00Z", nil),
		},
		"project-wide-events": {
			ev("-", "run_start", "2026-09-01T09:00:00Z", nil),
			ev("-", "run_end", "2026-09-01T13:00:00Z", nil),
			ev("A", "dispatch", "2026-09-01T10:00:00Z", nil),
			ev("A", "success", "2026-09-01T10:06:00Z", nil),
		},
		"several-tasks": {
			ev("A", "dispatch", "2026-09-01T10:00:00Z", nil),
			ev("B", "dispatch", "2026-09-01T10:05:00Z", nil),
			ev("A", "success", "2026-09-01T10:10:00Z", map[string]any{"duration_s": 600.0}),
			ev("B", "fail", "2026-09-01T10:20:00Z", map[string]any{"duration_s": 900.0}),
			ev("C", "block", "2026-09-01T10:30:00Z", nil),
		},
		"rounding": {
			ev("A", "dispatch", "2026-09-01T10:00:00Z", nil),
			ev("A", "success", "2026-09-01T10:30:00Z", map[string]any{"duration_s": 1234.5678}),
		},
	}
}

func TestEventDerivedFiguresMatchThePythonGolden(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "events.golden.json"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var golden map[string]struct {
		HumanHours  map[string]float64 `json:"human_hours"`
		LastUpdated map[string]string  `json:"last_updated"`
	}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatalf("parse golden: %v", err)
	}

	scenarios := eventScenarios()
	if len(scenarios) != len(golden) {
		t.Fatalf("%d scenarios in Go, %d in the golden — one side grew alone",
			len(scenarios), len(golden))
	}

	for name, events := range scenarios {
		want, ok := golden[name]
		if !ok {
			t.Fatalf("scenario %q has no golden — the goldens are frozen (see testdata/README.md): add the scenario's golden by hand, in the PR that adds the scenario", name)
		}
		t.Run(name, func(t *testing.T) {
			gotHours := HumanHoursByTask(events)
			if len(gotHours) == 0 && len(want.HumanHours) == 0 {
				// nothing to compare; an empty map and a nil one are the same fact
			} else if !reflect.DeepEqual(gotHours, want.HumanHours) {
				t.Errorf("HumanHoursByTask = %v, want %v", gotHours, want.HumanHours)
			}

			gotLast := LastUpdatedByTask(events)
			if len(gotLast) == 0 && len(want.LastUpdated) == 0 {
				// same
			} else if !reflect.DeepEqual(gotLast, want.LastUpdated) {
				t.Errorf("LastUpdatedByTask = %v, want %v", gotLast, want.LastUpdated)
			}
		})
	}
}

// A task that was never dispatched has no entry, rather than an entry of 0.
// "Never started" and "took no measurable time" are different facts and the
// column that renders them has no way to tell them apart once both are 0.0.
func TestNeverDispatchedIsAbsentNotZero(t *testing.T) {
	events := []state.Event{ev("A", "block", "2026-09-01T10:00:00Z", nil)}
	hours := HumanHoursByTask(events)
	if _, present := hours["A"]; present {
		t.Errorf("A is present with %v; a task with no closed interval must be absent", hours["A"])
	}
	if got := LastUpdatedByTask(events)["A"]; got != "2026-09-01T10:00:00Z" {
		t.Errorf("last_updated = %q — a task with no dispatch still has a last event", got)
	}
}

// Python raises TypeError here — subtracting a naive datetime from an aware
// one — and the traceback escapes `human_hours_by_task` into the endpoint.
// Go treats the offsetless timestamp as UTC, which is what `_parse_ts`
// intends, and answers instead of failing. A deliberate divergence.
func TestMixedTimestampFormsDoNotBlowUp(t *testing.T) {
	events := []state.Event{
		ev("A", "dispatch", "2026-09-01T10:00:00", nil),
		ev("A", "success", "2026-09-01T11:00:00Z", nil),
	}
	if got := HumanHoursByTask(events)["A"]; got != 1.0 {
		t.Errorf("hours = %v, want 1 — the offsetless form is read as UTC", got)
	}
}

// The rounding is Python's `round(x, 3)`: correctly-rounded half-to-even on
// the exact binary value. Scaling by 1000 and using math.Round would be half
// away from zero, on an already-scaled value — wrong twice.
func TestRound3IsHalfToEven(t *testing.T) {
	for _, tc := range []struct {
		in   float64
		want float64
	}{
		{0.0625, 0.062}, // exact binary tie → down to even
		{0.1875, 0.188}, // exact binary tie → up to even
		{0.3429355, 0.343},
		{2.0005, 2.001}, // 2.0005 is just ABOVE the tie in binary, so it goes up
	} {
		if got := round3(tc.in); got != tc.want {
			t.Errorf("round3(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
