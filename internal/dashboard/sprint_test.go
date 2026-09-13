package dashboard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/state"
)

// The clock the golden was generated at. Both sides take it as a parameter,
// which is the only reason a projected DATE can be in a golden at all.
var frozenNow = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

const frozenToday = "2026-09-01"

type sprintGolden struct {
	Sprint map[string]struct {
		VelocityPerDay float64      `json:"velocity_per_day"`
		DoneCount      int          `json:"done_count"`
		RemainingTasks int          `json:"remaining_tasks"`
		RemainingHours float64      `json:"remaining_hours"`
		BlockedCount   int          `json:"blocked_count"`
		ETADays        *float64     `json:"eta_days"`
		ETADate        *string      `json:"eta_date"`
		Confidence     string       `json:"confidence"`
		Blockers       []blockerRow `json:"blockers"`
	} `json:"sprint"`
	MilestoneETA []struct {
		Remaining      int         `json:"remaining"`
		VelocityPerDay float64     `json:"velocity_per_day"`
		TargetDate     *string     `json:"target_date"`
		Today          string      `json:"today"`
		ETA            *etaPayload `json:"eta"`
	} `json:"milestone_eta"`
}

func task(id string, phase int, status model.Status, hours float64, title string) model.Task {
	return model.Task{ID: id, Phase: phase, Status: status, EstimateHours: hours, Title: title}
}

func event(eventType, ts string, extra map[string]any) state.Event {
	return state.Event{EventType: eventType, TS: ts, Extra: extra}
}

type sprintScenario struct {
	tasks      []model.Task
	done7d     int
	lastEvents map[string]state.Event
}

// sprintScenarios mirrors SPRINT_SCENARIOS in testdata/make-sprint-golden.py.
func sprintScenarios() map[string]sprintScenario {
	many := make([]model.Task, 0, 41)
	many = append(many, task("D", 0, model.StatusDone, 0, ""))
	for i := 0; i < 40; i++ {
		many = append(many, task("T"+string(rune('a'+i%26))+string(rune('0'+i/26)), 0, model.StatusTodo, 1.0, ""))
	}

	return map[string]sprintScenario{
		"empty": {},
		"no-velocity": {
			tasks: []model.Task{task("A", 0, model.StatusTodo, 2.0, ""), task("B", 0, model.StatusTodo, 3.0, "")},
		},
		"nothing-remaining": {
			tasks:  []model.Task{task("A", 0, model.StatusDone, 0, ""), task("B", 0, model.StatusDone, 0, "")},
			done7d: 2,
		},
		"steady-pace": {
			tasks: []model.Task{
				task("A", 0, model.StatusDone, 0, ""), task("B", 0, model.StatusDone, 0, ""),
				task("C", 0, model.StatusTodo, 1.0, ""), task("D", 0, model.StatusTodo, 2.0, ""),
				task("E", 0, model.StatusTodo, 3.0, ""), task("F", 0, model.StatusTodo, 4.0, ""),
			},
			done7d: 7,
		},
		"slow-pace-low-confidence": {tasks: many, done7d: 1},
		"blockers": {
			tasks: []model.Task{
				task("A", 0, model.StatusDone, 0, ""),
				task("B", 2, model.StatusBlocked, 5.0, "Payments"),
				task("C", 1, model.StatusBlocked, 1.0, ""),
				task("D", 0, model.StatusTodo, 2.0, ""),
			},
			done7d: 7,
			lastEvents: map[string]state.Event{
				"B": event("block", "2026-08-30T09:00:00Z", map[string]any{"reason": "waiting on the bank"}),
				"C": event("fail", "2026-08-31T09:00:00Z", nil),
			},
		},
		"blocker-with-no-event": {
			tasks: []model.Task{task("A", 0, model.StatusBlocked, 1.0, "")},
		},
		"long-reason": {
			tasks: []model.Task{task("A", 0, model.StatusBlocked, 0, "")},
			lastEvents: map[string]state.Event{
				"A": event("block", "2026-08-30T09:00:00Z",
					map[string]any{"reason": longString(500)}),
			},
		},
	}
}

func longString(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'x'
	}
	return string(b)
}

func loadSprintGolden(t *testing.T) sprintGolden {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "sprint.golden.json"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var g sprintGolden
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatalf("parse golden: %v", err)
	}
	return g
}

func TestSprintHealthMatchesThePythonGolden(t *testing.T) {
	golden := loadSprintGolden(t)
	scenarios := sprintScenarios()
	if len(scenarios) != len(golden.Sprint) {
		t.Fatalf("%d scenarios in Go, %d in the golden — one side grew alone",
			len(scenarios), len(golden.Sprint))
	}

	for name, sc := range scenarios {
		want, ok := golden.Sprint[name]
		if !ok {
			t.Fatalf("scenario %q has no golden — the goldens are frozen (see testdata/README.md): add the scenario's golden by hand, in the PR that adds the scenario", name)
		}
		t.Run(name, func(t *testing.T) {
			got := sprintHealth(sc.tasks, sc.done7d, sc.lastEvents, frozenNow)

			if got.VelocityPerDay != want.VelocityPerDay || got.DoneCount != want.DoneCount ||
				got.RemainingTasks != want.RemainingTasks ||
				got.RemainingHours != want.RemainingHours ||
				got.BlockedCount != want.BlockedCount {
				t.Errorf("figures = %+v, want %+v", got, want)
			}
			if got.Confidence != want.Confidence {
				t.Errorf("confidence = %q, want %q", got.Confidence, want.Confidence)
			}
			if !eqFloatPtr(got.ETADays, want.ETADays) {
				t.Errorf("eta_days = %v, want %v", deref(got.ETADays), deref(want.ETADays))
			}
			if !eqStringPtr(got.ETADate, want.ETADate) {
				t.Errorf("eta_date = %v, want %v", derefStr(got.ETADate), derefStr(want.ETADate))
			}

			if len(got.Blockers) != len(want.Blockers) {
				t.Fatalf("blockers = %+v, want %+v", got.Blockers, want.Blockers)
			}
			for i, w := range want.Blockers {
				g := got.Blockers[i]
				if g.TaskID != w.TaskID || g.Title != w.Title || g.Phase != w.Phase ||
					g.Reason != w.Reason || g.EstimateHours != w.EstimateHours ||
					!eqStringPtr(g.BlockedAt, w.BlockedAt) {
					t.Errorf("blockers[%d] = %+v, want %+v", i, g, w)
				}
			}
		})
	}
}

func TestMilestoneETAMatchesThePythonGolden(t *testing.T) {
	for _, tc := range loadSprintGolden(t).MilestoneETA {
		target := ""
		if tc.TargetDate != nil {
			target = *tc.TargetDate
		}
		got := milestoneETA(tc.Remaining, tc.VelocityPerDay, tc.Today, target)
		switch {
		case got == nil && tc.ETA == nil:
		case got == nil || tc.ETA == nil:
			t.Errorf("milestoneETA(%d, %v, %q) = %v, want %v",
				tc.Remaining, tc.VelocityPerDay, target, got, tc.ETA)
		case *got != *tc.ETA:
			t.Errorf("milestoneETA(%d, %v, %q) = %+v, want %+v",
				tc.Remaining, tc.VelocityPerDay, target, *got, *tc.ETA)
		}
	}
}

// No velocity, or nothing left to do, is NULL — not zero days and not today.
// "Finishing today" and "no idea when" are opposite claims, and a UI that
// renders 0 for the second one is lying with a number.
func TestNoProjectionIsNullNotZero(t *testing.T) {
	got := sprintHealth([]model.Task{task("A", 0, model.StatusTodo, 1, "")}, 0, nil, frozenNow)
	if got.ETADays != nil || got.ETADate != nil {
		t.Errorf("eta = %v / %v with no velocity; both must be null",
			deref(got.ETADays), derefStr(got.ETADate))
	}
	if got.Confidence != "none" {
		t.Errorf("confidence = %q, want none", got.Confidence)
	}
	if milestoneETA(5, 0, frozenToday, "") != nil {
		t.Error("milestoneETA with no velocity must be nil")
	}
	if milestoneETA(0, 5, frozenToday, "") != nil {
		t.Error("milestoneETA with nothing remaining must be nil")
	}
}

// A milestone needing 1.2 days of work is not finishing today: the projection
// takes the CEILING of the division, not the rounding.
func TestMilestoneETACeilsThePartialDay(t *testing.T) {
	got := milestoneETA(6, 5.0, frozenToday, "")
	if got == nil || got.ETADays != 2 {
		t.Fatalf("got %+v, want 2 days (6/5 = 1.2, ceiled)", got)
	}
	if got.ETADate != "2026-09-03" {
		t.Errorf("eta_date = %q, want 2026-09-03", got.ETADate)
	}
}

// Blocked tasks are their own figure, not part of the remaining work. Counting
// them as remaining would make the ETA promise a date for work that cannot
// start, which is the number an operator most needs to be true.
func TestBlockedTasksAreNotRemainingWork(t *testing.T) {
	got := sprintHealth([]model.Task{
		task("A", 0, model.StatusTodo, 2.0, ""),
		task("B", 0, model.StatusBlocked, 40.0, ""),
	}, 7, nil, frozenNow)

	if got.RemainingTasks != 1 || got.RemainingHours != 2.0 {
		t.Errorf("remaining = %d tasks / %v hours, want 1 and 2 — the blocked one is separate",
			got.RemainingTasks, got.RemainingHours)
	}
	if got.BlockedCount != 1 {
		t.Errorf("blocked_count = %d, want 1", got.BlockedCount)
	}
}

func eqFloatPtr(a, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func eqStringPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func deref(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

func derefStr(v *string) any {
	if v == nil {
		return nil
	}
	return *v
}
