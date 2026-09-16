package dashboard

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hectorcanaimero/orch/internal/config"
	"github.com/hectorcanaimero/orch/internal/demo"
	"github.com/hectorcanaimero/orch/internal/doctor"
	"github.com/hectorcanaimero/orch/internal/receipt"
	"github.com/hectorcanaimero/orch/internal/state"
)

func demoServer(t *testing.T) *Server {
	t.Helper()
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
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close the demo database: %v", err)
		}
	})
	s, err := New(cfg(ProfileOperator, ""), Options{
		Static: spaHandler(t),
		State:  state.NewSQLite(db, paths.ID, paths.Root),
		Paths:  paths,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// The demo's finished run, with its markdown and the footer kept apart.
func TestReceiptAgainstTheDemoProject(t *testing.T) {
	s := demoServer(t)

	var got receiptPayload
	decode(t, get(t, s, "/api/receipt"), &got)
	if got.Receipt == nil || got.Receipt.RunID != demo.HistoryRunID || len(got.Receipt.Done) != 16 {
		t.Fatalf("receipt = %+v, want the demo's finished run with 16 done", got.Receipt)
	}
	if !strings.HasPrefix(got.Markdown, "## orch run `"+demo.HistoryRunID+"` — finished") {
		t.Errorf("markdown = %q", got.Markdown)
	}
	if got.BuiltWith != receipt.BuiltWith || strings.Contains(got.Markdown, receipt.BuiltWith) {
		t.Errorf("built_with = %q; markdown must not already carry it", got.BuiltWith)
	}

	// The live run by id: unfinished, with its failed attempts.
	var live receiptPayload
	decode(t, get(t, s, "/api/receipt?run="+demo.RunID), &live)
	if live.Receipt == nil || live.Receipt.Finished || live.Receipt.FailedAttempts == 0 {
		t.Errorf("live run receipt = %+v", live.Receipt)
	}

	if resp := get(t, s, "/api/receipt?run=nope"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown run: status = %d, want 404", resp.StatusCode)
	}
}

// A project that never finished a run answers a null receipt, not an error.
func TestReceiptBeforeAnyRun(t *testing.T) {
	resp := get(t, newReadServer(t, &fakeState{}), "/api/receipt")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got receiptPayload
	decode(t, resp, &got)
	if got.Receipt != nil || got.Markdown != "" || got.BuiltWith == "" {
		t.Errorf("got %+v, want a null receipt", got)
	}
}

func TestReceiptAndOnboardingAreGated(t *testing.T) {
	s := newGatedServer(t, &fakeState{})
	for _, path := range []string{"/api/receipt", "/api/onboarding"} {
		if resp := get(t, s, path); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s no token: status = %d, want 401", path, resp.StatusCode)
		}
		if resp := get(t, s, path+"?token=test-token-stakeholder"); resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s stakeholder token: status = %d, want 403", path, resp.StatusCode)
		}
	}
}

func TestOnboarding(t *testing.T) {
	saved := checkBackends
	t.Cleanup(func() { checkBackends = saved })
	checkBackends = func(names []string) []doctor.Check {
		return []doctor.Check{{Name: "backend.claude", Status: doctor.StatusError, Detail: "claude not on PATH"}}
	}

	// A project that never dispatched: providers and first run are unmet.
	var fresh onboardingPayload
	decode(t, get(t, newReadServer(t, &fakeState{}), "/api/onboarding"), &fresh)
	if fresh.Complete {
		t.Error("a project with no finished run is not complete")
	}
	items := map[string]onboardingItem{}
	for _, it := range fresh.Items {
		items[it.ID] = it
	}
	for _, id := range []string{"providers", "budget", "tasks", "vcs", "tunnel", "first_run"} {
		if _, ok := items[id]; !ok {
			t.Errorf("no %q item in %+v", id, fresh.Items)
		}
	}
	if p := items["providers"]; p.Done || !strings.Contains(p.Detail, "claude not on PATH") || p.Command != "orch doctor" {
		t.Errorf("providers = %+v", p)
	}
	if f := items["first_run"]; f.Done || f.Command != "orch run" {
		t.Errorf("first_run = %+v", f)
	}
	if !items["vcs"].Optional || !items["tunnel"].Optional || items["tasks"].Optional {
		t.Errorf("optional flags wrong: %+v", fresh.Items)
	}

	// A config.yaml that does not load is the budget item's to report.
	root := writeProject(t, "budget: [not, a, map\n", testTasksJSON)
	broken, err := New(cfg(ProfileOperator, ""), Options{
		Static: spaHandler(t),
		State:  &fakeState{},
		Paths:  config.Paths{Root: root, ID: "demo", ConfigYAML: filepath.Join(root, "config.yaml")},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var bad onboardingPayload
	decode(t, get(t, broken, "/api/onboarding"), &bad)
	for _, it := range bad.Items {
		if it.ID == "budget" && (it.Done || !strings.Contains(it.Detail, "config.yaml")) {
			t.Errorf("budget with a broken config.yaml = %+v, want not done naming config.yaml", it)
		}
	}

	// Once a run has dispatched and finished, the card is done.
	f := &fakeState{events: []state.Event{
		{RunID: "r", EventType: "dispatch", TaskID: "T-1", TS: "2026-09-10T10:00:00Z"},
		{RunID: "r", EventType: "sprint_done", TS: "2026-09-10T11:00:00Z"},
	}}
	var ran onboardingPayload
	decode(t, get(t, newReadServer(t, f), "/api/onboarding"), &ran)
	if !ran.Complete {
		t.Error("a finished run should complete the onboarding")
	}
	for _, it := range ran.Items {
		if it.ID == "first_run" && !it.Done {
			t.Errorf("first_run = %+v, want done", it)
		}
	}
}
