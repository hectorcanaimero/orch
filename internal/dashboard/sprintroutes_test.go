package dashboard

import (
	"errors"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/hectorcanaimero/orch/internal/config"
	"github.com/hectorcanaimero/orch/internal/model"
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

// A milestone is a phase. The fixture is phase 0 = T-1, T-2 and phase 1 = T-3
// (blocked); T-1 is done in the database.
func TestMilestonesEndpointReportsPhases(t *testing.T) {
	f := &fakeState{
		done7d: 7, // 1 task/day
		tasks:  []state.TaskRuntime{{ID: "T-1", Status: model.StatusDone}},
	}
	s := newReadServer(t, f)

	var got milestonesPayload
	decode(t, get(t, s, "/api/milestones"), &got)
	if len(got.Milestones) != 2 {
		t.Fatalf("got %d milestones, want one per phase (2): %+v", len(got.Milestones), got.Milestones)
	}

	p0, p1 := got.Milestones[0], got.Milestones[1]
	if p0.Phase != 0 || p0.Name != "Phase 0" || p0.Status != "active" {
		t.Errorf("phase 0 = %+v", p0)
	}
	if p0.Progress.Total != 2 || p0.Progress.Done != 1 || p0.Progress.Pct != 50 {
		t.Errorf("phase 0 progress = %+v, want 1/2 at 50%%", p0.Progress)
	}
	// One unblocked task left at 1/day.
	if p0.ETA == nil || p0.ETA.ETADays != 1 {
		t.Errorf("phase 0 eta = %+v, want 1 day", p0.ETA)
	}
	// Phase 1's only task is blocked: there is no work that can start, so
	// there is no date to promise.
	if p1.Blocked != 1 || p1.ETA != nil {
		t.Errorf("phase 1 = blocked %d, eta %+v; want 1 and null", p1.Blocked, p1.ETA)
	}
}

// A project with no tasks answers an empty list and never asks for a velocity
// it has nothing to project with.
func TestMilestonesOnAProjectWithNoTasks(t *testing.T) {
	root := writeProject(t, "spec_root: specs\n", `{"meta": {"project": "demo"}, "tasks": []}`)
	s, err := New(cfg(ProfileOperator, ""), Options{
		Static: spaHandler(t),
		State:  &fakeState{done7dErr: errors.New("must not be asked")},
		Paths:  config.Paths{Root: root, ID: "demo", ConfigYAML: filepath.Join(root, "config.yaml")},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

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
		{"/api/milestones", &fakeState{tasksErr: boom}},
		{"/api/milestones", &fakeState{done7dErr: boom}},
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
