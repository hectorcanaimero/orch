package dashboard

import (
	"errors"
	"net/http"
	"testing"

	"github.com/hectorcanaimero/orch/internal/state"
)

func TestSprintEndpoint(t *testing.T) {
	// The fixture project is T-1 todo, T-2 todo, T-3 blocked.
	f := &fakeState{
		done7d: 7,
		lastEvents: map[string]state.Event{
			"T-3": event("block", "2026-08-30T09:00:00Z",
				map[string]any{"reason": "waiting on T-2"}),
		},
	}
	s := newReadServer(t, f)

	var got sprintPayload
	decode(t, get(t, s, "/api/sprint"), &got)

	if !got.Available {
		t.Fatal("available = false")
	}
	if got.VelocityPerDay != 1 {
		t.Errorf("velocity = %v, want 1 (7 done over a 7-day window)", got.VelocityPerDay)
	}
	if got.RemainingTasks != 2 || got.BlockedCount != 1 {
		t.Errorf("remaining = %d, blocked = %d; want 2 and 1", got.RemainingTasks, got.BlockedCount)
	}
	if len(got.Blockers) != 1 || got.Blockers[0].Reason != "waiting on T-2" {
		t.Errorf("blockers = %+v", got.Blockers)
	}

	// It asks about the blocked tasks only. Reading every task's last event to
	// render a panel that is usually empty is the version of this that gets
	// slow on a real project.
	if len(f.askedForEvents) != 1 || f.askedForEvents[0] != "T-3" {
		t.Errorf("asked for events on %v, want just [T-3]", f.askedForEvents)
	}
}

// A project with nothing blocked asks for no events at all — an empty id list
// is answered without touching the database.
func TestSprintWithNothingBlockedAsksForNoEvents(t *testing.T) {
	f := &fakeState{
		done7d: 7,
		tasks: []state.TaskRuntime{
			{ID: "T-3", Status: "todo"}, // unblock the fixture's only blocked task
		},
	}
	s := newReadServer(t, f)

	var got sprintPayload
	decode(t, get(t, s, "/api/sprint"), &got)
	if got.BlockedCount != 0 || len(got.Blockers) != 0 {
		t.Errorf("blocked = %d, blockers = %+v", got.BlockedCount, got.Blockers)
	}
	if len(f.askedForEvents) != 0 {
		t.Errorf("asked for events on %v with nothing blocked", f.askedForEvents)
	}
	if f.askedForEvents == nil {
		t.Error("passed a nil id list, which means EVERY task — it must be an empty list")
	}
}

func TestMilestonesEndpoint(t *testing.T) {
	f := &fakeState{
		done7d: 7, // 1 task/day
		milestones: []state.Milestone{
			{ID: "m1", Title: "MVP", TargetDate: "2999-01-01", Status: "open",
				CreatedAt: "2026-08-01T00:00:00Z", Total: 10, Done: 4, PercentDone: 40},
			{ID: "m2", Title: "Launch", Status: "open",
				CreatedAt: "2026-08-02T00:00:00Z", Total: 3, Done: 3, PercentDone: 100},
		},
	}
	s := newReadServer(t, f)

	var got milestonesPayload
	decode(t, get(t, s, "/api/milestones"), &got)
	if len(got.Milestones) != 2 {
		t.Fatalf("got %d milestones, want 2", len(got.Milestones))
	}

	mvp, launch := got.Milestones[0], got.Milestones[1]
	if mvp.Progress.Total != 10 || mvp.Progress.Done != 4 || mvp.Progress.Pct != 40 {
		t.Errorf("m1 progress = %+v", mvp.Progress)
	}
	// 6 remaining at 1/day, and a target date far enough away to be "high".
	if mvp.ETA == nil || mvp.ETA.ETADays != 6 || mvp.ETA.Confidence != "high" {
		t.Errorf("m1 eta = %+v, want 6 days and high confidence", mvp.ETA)
	}
	// A finished milestone has nothing to project.
	if launch.ETA != nil {
		t.Errorf("m2 eta = %+v, want null — it is done", launch.ETA)
	}
}

// A project with no milestones answers an empty list and never asks for a
// velocity it has nothing to project with.
func TestMilestonesOnAProjectWithNone(t *testing.T) {
	s := newReadServer(t, &fakeState{})

	var got milestonesPayload
	decode(t, get(t, s, "/api/milestones"), &got)
	if got.Milestones == nil {
		t.Error("milestones = null; the SPA maps over it, so it must be []")
	}
	if len(got.Milestones) != 0 {
		t.Errorf("got %d milestones", len(got.Milestones))
	}
}

func TestSprintEndpointsReportReadFailures(t *testing.T) {
	boom := errors.New("database is locked")
	for _, tc := range []struct {
		path  string
		state *fakeState
	}{
		{"/api/sprint", &fakeState{done7dErr: boom}},
		{"/api/sprint", &fakeState{lastEventsErr: boom}},
		{"/api/milestones", &fakeState{milestonesErr: boom}},
		{"/api/milestones", &fakeState{
			milestones: []state.Milestone{{ID: "m1", Total: 2}}, done7dErr: boom}},
	} {
		resp := get(t, newReadServer(t, tc.state), tc.path)
		if resp.StatusCode != http.StatusInternalServerError {
			t.Errorf("%s: status = %d, want 500", tc.path, resp.StatusCode)
		}
	}
}

func TestSprintEndpointsAreGated(t *testing.T) {
	s := newGatedServer(t, &fakeState{})
	for _, path := range []string{"/api/sprint", "/api/milestones"} {
		if resp := get(t, s, path); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s with no token: status = %d, want 401", path, resp.StatusCode)
		}
		if resp := get(t, s, path+"?token=test-token-stakeholder"); resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s with a token: status = %d, want 403", path, resp.StatusCode)
		}
	}
}
