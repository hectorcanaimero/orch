package dashboard

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/hectorcanaimero/orch/internal/config"
	"github.com/hectorcanaimero/orch/internal/demo"
	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/state"
)

// The fixture is T-1 and T-2 in phase 0, T-3 blocked in phase 1. Here T-1 is
// being worked on by a live run, on its second attempt.
func TestNowEndpoint(t *testing.T) {
	f := &fakeState{
		done7d: 7,
		tasks:  []state.TaskRuntime{{ID: "T-1", Status: model.StatusInProgress}},
		lastEvents: map[string]state.Event{
			"T-3": event("block", "2026-08-30T09:00:00Z", map[string]any{"reason": "waiting on T-2"}),
		},
		runs: []state.Run{{RunID: "run-1", StartedAt: "2026-09-16T10:00:00Z", Mode: "run", Status: "live", InFlight: 1}},
		inFlight: []state.Dispatch{
			{RunID: "run-1", TaskID: "T-1", Backend: "claude", StartedAt: "2026-09-16T10:05:00Z", Attempt: 2},
		},
	}
	root := writeProject(t, "spec_root: specs\nconcurrency:\n  global_max: 4\nretry:\n  max_attempts: 3\n", testTasksJSON)
	s, err := New(cfg(ProfileOperator, ""), Options{
		Static: spaHandler(t),
		State:  f,
		Paths:  config.Paths{Root: root, ID: "demo", ConfigYAML: filepath.Join(root, "config.yaml")},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var got nowPayload
	decode(t, get(t, s, "/api/now"), &got)

	if got.Run == nil || got.Run.RunID != "run-1" || got.Run.Status != "live" {
		t.Errorf("run = %+v, want the live run-1", got.Run)
	}
	if len(got.Working) != 1 {
		t.Fatalf("working = %+v, want T-1", got.Working)
	}
	w := got.Working[0]
	if w.TaskID != "T-1" || w.Title != "Scaffold" || w.Provider != "claude" || w.Model != "claude/sonnet" {
		t.Errorf("working[0] = %+v", w)
	}
	if w.Attempt != 2 || w.MaxAttempts != 3 || w.StartedAt != "2026-09-16T10:05:00Z" {
		t.Errorf("working[0] attempt = %d of %d since %q; want 2 of 3 since 10:05", w.Attempt, w.MaxAttempts, w.StartedAt)
	}
	if got.Slots.Used != 1 || got.Slots.Max != 4 {
		t.Errorf("slots = %+v, want 1 of 4", got.Slots)
	}
	if len(got.Attention) != 1 || got.Attention[0].TaskID != "T-3" || got.Attention[0].Reason != "waiting on T-2" {
		t.Errorf("attention = %+v, want T-3 with its reason", got.Attention)
	}
	// The pace figures are /api/sprint's, not a second calculation.
	if got.Pace.RemainingTasks != 2 || got.Pace.BlockedCount != 1 || got.Pace.ETADate == nil {
		t.Errorf("pace = %+v", got.Pace)
	}
	if got.Summary.Total != 3 || got.Summary.InProgress != 1 {
		t.Errorf("summary = %+v", got.Summary)
	}
}

// A project that has never run answers null and empty lists — the SPA maps
// over them — rather than an error.
func TestNowOnAProjectThatNeverRan(t *testing.T) {
	s := newReadServer(t, &fakeState{runsErr: state.ErrNoRuns, tasks: []state.TaskRuntime{{ID: "T-3", Status: model.StatusTodo}}})

	var got nowPayload
	decode(t, get(t, s, "/api/now"), &got)
	if got.Run != nil {
		t.Errorf("run = %+v, want null", got.Run)
	}
	if got.Working == nil || got.Attention == nil || got.WaitingForBudget == nil {
		t.Errorf("working/attention/waiting must be [] not null: %+v", got)
	}
	// No concurrency cap in config.yaml: the slots are the default `orch run`
	// enforces, not a blank.
	if got.Slots.Max != 6 {
		t.Errorf("slots.max = %d, want the default global_max 6", got.Slots.Max)
	}
}

func TestNowReportsReadFailures(t *testing.T) {
	boom := errors.New("database is locked")
	for _, f := range []*fakeState{
		{runsErr: boom},
		{inFlightErr: boom},
		{tasksErr: boom},
	} {
		if resp := get(t, newReadServer(t, f), "/api/now"); resp.StatusCode != http.StatusInternalServerError {
			t.Errorf("state %+v: status = %d, want 500", f, resp.StatusCode)
		}
	}
}

// /api/now is the operator's console: a stakeholder token does not reach it.
func TestNowIsGated(t *testing.T) {
	s := newGatedServer(t, &fakeState{})
	if resp := get(t, s, "/api/now"); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no token: status = %d, want 401", resp.StatusCode)
	}
	if resp := get(t, s, "/api/now?token=test-token-stakeholder"); resp.StatusCode != http.StatusForbidden {
		t.Errorf("stakeholder token: status = %d, want 403", resp.StatusCode)
	}
}

// Against the demo project, read through the real SQLite backend: three agents
// working, a live run, and the blocked tasks asking for attention.
func TestNowAgainstTheDemoProject(t *testing.T) {
	ctx := context.Background()
	paths, err := demo.Build(ctx, filepath.Join(t.TempDir(), "demo"), time.Now())
	if err != nil {
		t.Fatalf("demo.Build: %v", err)
	}
	loaded, err := config.Load(paths.ConfigYAML, paths.Root)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	db, _, err := state.Open(ctx, paths.SQLitePath(loaded.Config))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	s, err := New(cfg(ProfileOperator, ""), Options{
		Static: spaHandler(t),
		State:  state.NewSQLite(db, paths.ID, paths.Root),
		Paths:  paths,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var got nowPayload
	decode(t, get(t, s, "/api/now"), &got)
	if got.Run == nil || got.Run.Status != "live" {
		t.Errorf("run = %+v, want the demo's live run", got.Run)
	}
	if len(got.Working) != 3 {
		t.Errorf("working = %d dispatches, want the demo's 3", len(got.Working))
	}
	for _, w := range got.Working {
		if w.Title == "" || w.Title == w.TaskID || w.Provider == "" || w.StartedAt == "" {
			t.Errorf("working row missing its task or dispatch fields: %+v", w)
		}
	}
	if len(got.Attention) != 2 {
		t.Errorf("attention = %d, want the demo's 2 blocked tasks", len(got.Attention))
	}
}
