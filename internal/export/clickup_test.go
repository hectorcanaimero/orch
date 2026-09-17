package export_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hectorcanaimero/orch/internal/export"
	"github.com/hectorcanaimero/orch/internal/export/clickuptest"
	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/project"
)

func cuTask(id string, phase int, status model.Status, deps ...string) model.Task {
	return model.Task{
		ID: id, Phase: phase, Title: "Title of " + id, Status: status, Dependencies: deps,
		Model: "claude/claude-sonnet-5", Reason: "Standard work.", EstimateHours: 2,
		Description: "Build the thing for FR-4 and NFR-9.\n\nDone when: `make test` passes.",
		Files:       []string{"src/" + id + ".go"}, SpecRef: "f0-base.md",
	}
}

var outline = project.Outline{
	Phases:   map[int]string{0: "Plataforma base", 1: "Contactos"},
	Packages: map[string]string{"0.1": "monorepo", "1.1": "contracts"},
}

func newPlan(t *testing.T, tasks []model.Task, sel export.Selection) export.ClickUpPlan {
	t.Helper()
	plan, err := export.PlanClickUp("demo", "specs", tasks, sel, outline)
	if err != nil {
		t.Fatal(err)
	}
	plan.Language = "es"
	plan.TimeoutMultiplier = 1.5
	return plan
}

func fields() []clickuptest.Field {
	return []clickuptest.Field{
		{ID: "f-orch", Name: "orch ID", Type: "short_text"},
		{ID: "f-model", Name: "Modelo", Type: "drop_down", Options: []string{"Opus 5", "Sonnet 5", "Haiku 4.5"}},
		{ID: "f-pkg", Name: "Paquete", Type: "short_text"},
		{ID: "f-cost", Name: "Costo USD", Type: "currency"},
	}
}

func dest(fake *clickuptest.Server, dryRun bool) export.ClickUp {
	c := export.NewClickUpClient(fake.URL(), clickuptest.Token)
	c.Sleep = func(context.Context, time.Duration) error { return nil }
	return export.ClickUp{FolderID: fake.FolderID, Client: c, Statuses: export.DefaultClickUpStatuses, DryRun: dryRun}
}

func sync(t *testing.T, d export.ClickUp, plan export.ClickUpPlan) (export.Summary, []export.Action) {
	t.Helper()
	var actions []export.Action
	sum, err := d.Sync(context.Background(), plan, func(a export.Action) { actions = append(actions, a) })
	if err != nil {
		t.Fatal(err)
	}
	return sum, actions
}

func TestTaskDescriptionIsReadableProse(t *testing.T) {
	tasks := []model.Task{cuTask("F0.1.T1", 0, model.StatusTodo), cuTask("F0.1.T2", 0, model.StatusBacklog, "F0.1.T1", "F9.9.T9")}
	plan := newPlan(t, tasks, export.Selection{})
	got := plan.TaskDescription(tasks[1])

	for _, want := range []string{
		"## Qué hay que hacer\n\nBuild the thing for FR-4 and NFR-9.",
		"## Cómo sabemos que está terminada\n\n`make test` passes.",
		"## Qué requisitos cubre\n\nFR-4, NFR-9",
		"- **F0.1.T1** — Title of F0.1.T1",
		"- **F9.9.T9** — no está en tasks.json",
		"Modelo asignado: **claude/claude-sonnet-5**. Standard work.",
		"Estimación: **2 h** (si un intento pasa de 3 h, orch lo corta y reintenta).",
		"- `src/F0.1.T2.go`",
		"📄 Spec: `specs/f0-base.md` · Paquete **F0.1 — monorepo**",
		"orch-task: demo/F0.1.T2",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("description lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Done when") {
		t.Errorf("the Done when line was not turned into its own section:\n%s", got)
	}
}

func TestStatusCommentRendersOrchNotesNotJSON(t *testing.T) {
	task := cuTask("F0.1.T1", 0, model.StatusDone)
	task.Comments = []json.RawMessage{
		json.RawMessage(`{"author":"claude/claude-sonnet-5","body":"started","at":"2026-09-17T14:00:00Z"}`),
		json.RawMessage(`{"author":"claude/claude-sonnet-5","body":"PR #14 merged, tests green","at":"2026-09-17T16:10:00+00:00"}`),
	}
	plan := newPlan(t, []model.Task{task}, export.Selection{})
	plan.Spend = map[string]export.TaskSpend{"F0.1.T1": {CostUSD: 1.8, TokensOut: 412000, DurationS: 7800, Attempts: 2}}

	got := plan.StatusComment(task, "in progress", "complete")
	want := "✅ orch terminó esta tarea.\n" +
		"Estado en ClickUp: in progress → complete.\n" +
		"Último registro de orch (claude/claude-sonnet-5, 2026-09-17 16:10 UTC): PR #14 merged, tests green\n" +
		"Consumo acumulado: US$ 1,80 en 2 intento(s), 412.000 tokens de salida, 2 h 10 min de trabajo."
	if got != want {
		t.Errorf("comment =\n%s\nwant\n%s", got, want)
	}
	if strings.ContainsAny(got, "{}") {
		t.Errorf("comment carries raw JSON: %s", got)
	}
}

func TestSyncCreatesThenMirrorsIdempotently(t *testing.T) {
	fake := clickuptest.New(t)
	fake.Fields = fields()
	f0 := fake.AddList("F0 — Plataforma base")

	tasks := []model.Task{
		cuTask("F0.1.T1", 0, model.StatusDone),
		cuTask("F0.1.T2", 0, model.StatusTodo, "F0.1.T1"),
		cuTask("F1.1.T1", 1, model.StatusBacklog, "F0.1.T2"),
	}
	plan := newPlan(t, tasks, export.Selection{})
	plan.Spend = map[string]export.TaskSpend{"F0.1.T1": {CostUSD: 0.42, Attempts: 1}}

	// A dry run reads and reports, and writes nothing.
	sum, _ := sync(t, dest(fake, true), plan)
	if fake.Writes != 0 || sum.TasksCreated != 3 || sum.ListsCreated != 1 {
		t.Fatalf("dry run: writes=%d summary=%+v", fake.Writes, sum)
	}

	sum, _ = sync(t, dest(fake, false), plan)
	if sum.ListsCreated != 1 || sum.TasksCreated != 3 || sum.DependenciesAdded != 2 {
		t.Fatalf("first run summary = %+v", sum)
	}
	done := fake.TaskByName("F0.1.T1 — Title of F0.1.T1")
	if done == nil || done.ListID != f0.ID || done.Status != "complete" {
		t.Fatalf("F0.1.T1 = %+v", done)
	}
	if done.TimeEstimate != 2*3600*1000 {
		t.Errorf("time_estimate = %d ms, want 2h", done.TimeEstimate)
	}
	wantFields := map[string]any{"f-orch": "F0.1.T1", "f-pkg": "F0.1 — monorepo", "f-model": 1, "f-cost": 0.42}
	for id, want := range wantFields {
		if got := done.Fields[id]; got != want {
			t.Errorf("field %s = %#v, want %#v", id, got, want)
		}
	}
	second := fake.TaskByName("F0.1.T2 — Title of F0.1.T2")
	if len(second.DependsOn) != 1 || second.DependsOn[0] != done.ID {
		t.Errorf("F0.1.T2 depends on %v, want [%s]", second.DependsOn, done.ID)
	}
	if fake.TaskByName("F1.1.T1 — Title of F1.1.T1").ListID == f0.ID {
		t.Errorf("F1.1.T1 landed in F0's List")
	}

	// A second run over the same state changes nothing.
	writes := fake.Writes
	sum, _ = sync(t, dest(fake, false), plan)
	if fake.Writes != writes || sum.Unchanged != 3 {
		t.Fatalf("re-run wrote %d time(s), summary %+v", fake.Writes-writes, sum)
	}

	// orch moves a task: the mirror moves it and says so in a comment.
	plan.Phases[0].Tasks[1].Status = model.StatusInProgress
	plan.Phases[0].Tasks[1].Comments = []json.RawMessage{json.RawMessage(`{"author":"claude/claude-opus-5","body":"started","at":"2026-09-17T14:00:00Z"}`)}
	sum, actions := sync(t, dest(fake, false), plan)
	if sum.StatusesMoved != 1 || second.Status != "in progress" {
		t.Fatalf("status not mirrored: %+v, ClickUp status %q", sum, second.Status)
	}
	if len(second.Comments) != 1 || !strings.HasPrefix(second.Comments[0], "🚀 orch empezó esta tarea.\nEstado en ClickUp: to do → in progress.") {
		t.Errorf("comment = %q", second.Comments)
	}
	if actions[len(actions)-1].Kind == export.ActionDependency {
		t.Errorf("a dependency was re-added: %+v", actions)
	}
}

func TestSyncAdoptsTasksFoundByTheOrchIDField(t *testing.T) {
	// Tasks made before the marker existed (by hand, or through ClickUp's MCP)
	// carry only the orch ID field; the mirror must adopt them, not duplicate.
	fake := clickuptest.New(t)
	fake.Fields = fields()
	l := fake.AddList("F0 — Plataforma base")
	old := fake.AddTask(l.ID, "F0.1.T1 — hand made", "no marker here", "backlog", map[string]any{"f-orch": "F0.1.T1"})

	plan := newPlan(t, []model.Task{cuTask("F0.1.T1", 0, model.StatusTodo)}, export.Selection{})
	sum, _ := sync(t, dest(fake, false), plan)
	if sum.TasksCreated != 0 || len(fake.Tasks) != 1 {
		t.Fatalf("duplicated the task: %+v, %d task(s)", sum, len(fake.Tasks))
	}
	if old.Status != "to do" {
		t.Errorf("status = %q, want to do", old.Status)
	}
}

func TestSyncCarriesOnWhenClickUpRefusesCustomFields(t *testing.T) {
	// ClickUp's Free plan caps custom-field usages; past it every field write
	// is a 400 FIELD_033. Statuses, comments and dependencies must still sync.
	fake := clickuptest.New(t)
	fake.Fields = fields()
	l := fake.AddList("F0 — Plataforma base")
	old := fake.AddTask(l.ID, "F0.1.T1 — old", "orch-task: demo/F0.1.T1", "backlog", nil)
	fake.FieldQuotaExceeded = true

	plan := newPlan(t, []model.Task{cuTask("F0.1.T1", 0, model.StatusTodo), cuTask("F0.1.T2", 0, model.StatusTodo, "F0.1.T1")}, export.Selection{})
	plan.Spend = map[string]export.TaskSpend{"F0.1.T1": {CostUSD: 1.2, Attempts: 1}}
	sum, actions := sync(t, dest(fake, false), plan)

	if old.Status != "to do" || sum.StatusesMoved != 1 {
		t.Errorf("status not mirrored past the refused field: %q, %+v", old.Status, sum)
	}
	created := fake.TaskByName("F0.1.T2 — Title of F0.1.T2")
	if created == nil || len(created.DependsOn) != 1 {
		t.Fatalf("F0.1.T2 not created without its fields, or lost its dependency: %+v", created)
	}
	if sum.FieldsSkipped == 0 || sum.FieldsSet != 0 {
		t.Errorf("summary = %+v", sum)
	}
	skipped := 0
	for _, a := range actions {
		if a.Kind == export.ActionFieldSkipped {
			skipped++
		}
	}
	if skipped != sum.FieldsSkipped {
		t.Errorf("%d skip action(s) reported, summary says %d", skipped, sum.FieldsSkipped)
	}
}

func TestSyncNamesStatusesTheListLacks(t *testing.T) {
	fake := clickuptest.New(t)
	fake.AddList("F0", "to do", "in progress", "complete")
	plan := newPlan(t, []model.Task{cuTask("F0.1.T1", 0, model.StatusBlocked), cuTask("F0.1.T2", 0, model.StatusBacklog)}, export.Selection{})

	_, err := dest(fake, false).Sync(context.Background(), plan, func(export.Action) {})
	if err == nil || !strings.Contains(err.Error(), `no status "backlog", "blocked"`) || !strings.Contains(err.Error(), "--status-map") {
		t.Fatalf("err = %v", err)
	}
	if fake.Writes != 0 {
		t.Errorf("wrote %d time(s) before refusing", fake.Writes)
	}
}

func TestClientWaitsOutARateLimit(t *testing.T) {
	fake := clickuptest.New(t)
	fake.AddList("F0")
	fake.RateLimitOnce = true
	d := dest(fake, false)
	waited := 0
	d.Client.Sleep = func(context.Context, time.Duration) error { waited++; return nil }

	if _, err := d.Sync(context.Background(), newPlan(t, []model.Task{cuTask("F0.1.T1", 0, model.StatusTodo)}, export.Selection{}), func(export.Action) {}); err != nil {
		t.Fatal(err)
	}
	if waited != 1 {
		t.Errorf("waited %d time(s), want 1", waited)
	}
}

func TestClientSlowsDownBeforeTheLimit(t *testing.T) {
	fake := clickuptest.New(t)
	fake.AddList("F0")
	fake.Remaining = "2"
	d := dest(fake, true)
	waited := 0
	d.Client.Sleep = func(context.Context, time.Duration) error { waited++; return nil }
	if _, err := d.Sync(context.Background(), newPlan(t, []model.Task{cuTask("F0.1.T1", 0, model.StatusTodo)}, export.Selection{}), func(export.Action) {}); err != nil {
		t.Fatal(err)
	}
	if waited == 0 {
		t.Error("never waited with 2 requests left in the window")
	}
}

func TestSyncReportsABadToken(t *testing.T) {
	fake := clickuptest.New(t)
	d := dest(fake, false)
	d.Client.Token = "pk_wrong"
	_, err := d.Sync(context.Background(), newPlan(t, []model.Task{cuTask("F0.1.T1", 0, model.StatusTodo)}, export.Selection{}), func(export.Action) {})
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "API Token") {
		t.Fatalf("err = %v", err)
	}
}

func TestPlanClickUpKeepsDoneTasksAndRefusesCycles(t *testing.T) {
	plan := newPlan(t, []model.Task{cuTask("F1.1.T1", 1, model.StatusDone), cuTask("F0.1.T1", 0, model.StatusTodo)}, export.Selection{})
	if len(plan.Phases) != 2 || plan.Phases[0].Title != "F0 — Plataforma base" || len(plan.Phases[1].Tasks) != 1 {
		t.Errorf("phases = %+v", plan.Phases)
	}
	if _, err := export.PlanClickUp("demo", "specs", []model.Task{cuTask("A", 0, model.StatusTodo, "B"), cuTask("B", 0, model.StatusTodo, "A")}, export.Selection{}, outline); err == nil {
		t.Error("a cycle was accepted")
	}
}
