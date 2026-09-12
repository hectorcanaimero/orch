package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hectorcanaimero/orch/internal/budget"
	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/scaffold"
	"github.com/hectorcanaimero/orch/internal/state"
)

// The whole package is exercised through a real MCP session — an in-memory
// transport pair, a real client, a real server — rather than by calling the
// handlers directly. The handlers are the easy part; what this has to prove is
// that the seven tools are reachable *as tools*: that their schemas are
// inferable, that their arguments survive JSON-RPC, and that a refusal arrives
// as a tool error with its payload intact. Calling s.setStatus in Go would
// check none of that.

// newTestProject scaffolds a real python-api project and opens its database.
//
// The project comes from the shipped template rather than a tasks.json written
// here: it is the same two-task DAG an operator gets from `orch init
// --template python-api`, spec refs included, so a change that breaks real
// projects breaks this test too (checklist rule 28).
func newTestProject(t *testing.T) (string, state.Backend) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "proj")
	if _, err := scaffold.Run(scaffold.Options{Root: root, Template: "python-api"}); err != nil {
		t.Fatalf("scaffold the project: %v", err)
	}

	ctx := context.Background()
	db, _, err := state.Open(ctx, filepath.Join(root, "orch.db"))
	if err != nil {
		t.Fatalf("open the database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	backend := state.NewSQLite(db, "proj", root)
	tasks, err := model.LoadTasksFile(filepath.Join(root, "tasks.json"))
	if err != nil {
		t.Fatalf("load tasks.json: %v", err)
	}
	if err := backend.Bootstrap(ctx, tasks.Tasks); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	return root, backend
}

// connect wires a client to a server over the SDK's in-memory transport pair.
func connect(t *testing.T, opts Options) *mcpsdk.ClientSession {
	t.Helper()
	srv, err := NewServer(opts)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	ctx := context.Background()
	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	serverSession, err := srv.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "orch-test", Version: "v0"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func newTestSession(t *testing.T) (*mcpsdk.ClientSession, string, state.Backend) {
	t.Helper()
	root, backend := newTestProject(t)
	cs := connect(t, Options{
		Backend:     backend,
		TasksJSON:   filepath.Join(root, "tasks.json"),
		ProjectID:   "proj",
		ProjectRoot: root,
		Version:     "v-test",
	})
	return cs, root, backend
}

// call invokes a tool and decodes its structured content into out.
//
// The structured payload is decoded from the wire rather than from the Go
// value the handler returned, so a field that does not survive the round trip
// — a json.RawMessage inferred as a string, a map with no schema — fails here
// instead of passing on the strength of the in-process call.
func call(t *testing.T, cs *mcpsdk.ClientSession, name string, args any, out any) *mcpsdk.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if out != nil {
		if res.StructuredContent == nil {
			t.Fatalf("%s: no structuredContent in the result", name)
		}
		raw, err := json.Marshal(res.StructuredContent)
		if err != nil {
			t.Fatalf("%s: re-marshal the structured content: %v", name, err)
		}
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("%s: decode %s: %v", name, raw, err)
		}
	}
	return res
}

// ---- tools/list ------------------------------------------------------------

func TestToolsListIsTheSevenTools(t *testing.T) {
	cs, _, _ := newTestSession(t)

	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	var got []string
	for _, tool := range res.Tools {
		got = append(got, tool.Name)
		// A tool with no input schema cannot be called by a model that
		// validates arguments, and AddTool infers one — so an empty schema
		// here means the In type stopped being a struct.
		if tool.InputSchema == nil {
			t.Errorf("%s has no input schema", tool.Name)
		}
		if tool.Description == "" {
			t.Errorf("%s has no description", tool.Name)
		}
	}

	want := []string{
		"orch_block", "orch_budget", "orch_context", "orch_events",
		"orch_get_task", "orch_list_tasks", "orch_set_status",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tools = %v\nwant   %v", got, want)
	}
}

// ---- the start -> finish cycle --------------------------------------------

// TestStartFinishCycle walks the path an agent actually takes, and checks the
// consequence rather than the return value: after F0.T1 finishes, the task
// that depended on it becomes the ready one.
func TestStartFinishCycle(t *testing.T) {
	cs, _, _ := newTestSession(t)

	var ready listTasksOut
	call(t, cs, "orch_list_tasks", map[string]any{"ready": true}, &ready)
	if len(ready.Tasks) != 1 || ready.Tasks[0].ID != "F0.T1" {
		t.Fatalf("ready before the cycle = %+v; want just F0.T1", ready.Tasks)
	}

	var started setStatusOut
	call(t, cs, "orch_set_status", map[string]any{
		"task_id": "F0.T1", "status": "in-progress", "author": "claude/claude-sonnet-4-6",
	}, &started)
	if !started.OK || started.From != "todo" || started.To != "in-progress" {
		t.Fatalf("start = %+v; want ok todo -> in-progress", started)
	}

	var finished setStatusOut
	call(t, cs, "orch_set_status", map[string]any{
		"task_id": "F0.T1", "status": "done",
		"note": "scaffolded the layout", "author": "claude/claude-sonnet-4-6",
	}, &finished)
	if !finished.OK || finished.From != "in-progress" || finished.To != "done" {
		t.Fatalf("finish = %+v; want ok in-progress -> done", finished)
	}

	call(t, cs, "orch_list_tasks", map[string]any{"ready": true}, &ready)
	if len(ready.Tasks) != 1 || ready.Tasks[0].ID != "F1.T1" {
		t.Errorf("ready after the cycle = %+v; want just F1.T1", ready.Tasks)
	}

	// The note reached the task's trail, which is the half of a finish that
	// an operator reads back later.
	var got getTaskOut
	call(t, cs, "orch_get_task", map[string]any{"task_id": "F0.T1"}, &got)
	if got.Status != "done" {
		t.Errorf("status after finish = %q; want done", got.Status)
	}
	if len(got.Comments) == 0 {
		t.Fatal("the finish left no comment on the task")
	}
	last, ok := got.Comments[len(got.Comments)-1].(map[string]any)
	if !ok {
		t.Fatalf("comment is %T, not a JSON object", got.Comments[len(got.Comments)-1])
	}
	if last["body"] != "scaffolded the layout" || last["author"] != "claude/claude-sonnet-4-6" {
		t.Errorf("last comment = %v; want the note and the author that wrote it", last)
	}
}

// ---- the refusals ----------------------------------------------------------

func TestSetStatusRefusals(t *testing.T) {
	cases := []struct {
		name     string
		args     map[string]any
		wantCode string
		wantFrom string
		wantList []string
	}{
		{
			// `done` can only reopen to `todo` — the one transition out of
			// it. An agent that tried to restart a finished task gets the
			// pair it may actually pick from.
			name:     "illegal transition",
			args:     map[string]any{"task_id": "F0.T1", "status": "in-progress"},
			wantCode: codeIllegalTransition,
			wantFrom: "done",
			wantList: []string{"todo", "done"},
		},
		{
			name:     "unknown task",
			args:     map[string]any{"task_id": "NOPE", "status": "done"},
			wantCode: codeUnknownTask,
		},
		{
			// The underscore spelling is the plausible typo: the database
			// column, the status label map and half the docs all use
			// `in_progress` somewhere.
			name:     "invalid status",
			args:     map[string]any{"task_id": "F0.T1", "status": "in_progress"},
			wantCode: codeInvalidStatus,
		},
		{
			name:     "no task id",
			args:     map[string]any{"task_id": "", "status": "done"},
			wantCode: codeMissingRequiredArg,
		},
	}

	cs, _, backend := newTestSession(t)
	// Drive F0.T1 to done first, so the illegal-transition case is a real
	// move the table refuses rather than a status nobody could reach.
	ctx := context.Background()
	for _, to := range []model.Status{model.StatusInProgress, model.StatusDone} {
		if err := backend.Transition(ctx, "F0.T1", to, state.Note{Author: "test"}); err != nil {
			t.Fatalf("seed F0.T1 -> %s: %v", to, err)
		}
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out setStatusOut
			res := call(t, cs, "orch_set_status", tc.args, &out)

			if !res.IsError {
				t.Error("the result is not flagged as an error")
			}
			if out.OK {
				t.Error("ok is true on a refused write")
			}
			if out.Error == nil {
				t.Fatal("no structured error in the payload")
			}
			if out.Error.Code != tc.wantCode {
				t.Errorf("code = %q; want %q", out.Error.Code, tc.wantCode)
			}
			if out.Error.From != tc.wantFrom {
				t.Errorf("from = %q; want %q", out.Error.From, tc.wantFrom)
			}
			if !reflect.DeepEqual(out.Error.ValidTransitions, tc.wantList) {
				t.Errorf("valid_transitions = %v; want %v", out.Error.ValidTransitions, tc.wantList)
			}
			// The text content carries the same story, because a model that
			// reads only the content must not see an empty error.
			if len(res.Content) == 0 {
				t.Fatal("no text content on the error result")
			}
			text, ok := res.Content[0].(*mcpsdk.TextContent)
			if !ok || text.Text == "" {
				t.Errorf("content[0] = %#v; want non-empty text", res.Content[0])
			}
		})
	}

	// The refused writes changed nothing.
	got, err := backend.Task(ctx, "F0.T1")
	if err != nil {
		t.Fatalf("read F0.T1 back: %v", err)
	}
	if got.Status != model.StatusDone {
		t.Errorf("F0.T1 is %s after four refusals; want done", got.Status)
	}
}

func TestBlockRequiresAReason(t *testing.T) {
	cs, _, backend := newTestSession(t)

	var out setStatusOut
	res := call(t, cs, "orch_block", map[string]any{"task_id": "F0.T1", "reason": "  "}, &out)
	if !res.IsError || out.OK {
		t.Fatalf("a blank reason was accepted: %+v", out)
	}
	if out.Error == nil || out.Error.Code != codeMissingRequiredArg {
		t.Fatalf("error = %+v; want %s", out.Error, codeMissingRequiredArg)
	}

	got, err := backend.Task(context.Background(), "F0.T1")
	if err != nil {
		t.Fatalf("read F0.T1 back: %v", err)
	}
	if got.Status != model.StatusTodo {
		t.Errorf("F0.T1 is %s; a reasonless block must not have moved it", got.Status)
	}
}

func TestBlockRecordsTheReason(t *testing.T) {
	cs, _, _ := newTestSession(t)

	var out setStatusOut
	call(t, cs, "orch_block", map[string]any{
		"task_id": "F1.T1", "reason": "F0.T1's layout is missing app/main.py",
		"author": "claude/claude-sonnet-4-6",
	}, &out)
	if !out.OK || out.To != "blocked" {
		t.Fatalf("block = %+v; want ok -> blocked", out)
	}

	var got getTaskOut
	call(t, cs, "orch_get_task", map[string]any{"task_id": "F1.T1"}, &got)
	if got.Status != "blocked" {
		t.Fatalf("status = %q; want blocked", got.Status)
	}
	last, ok := got.Comments[len(got.Comments)-1].(map[string]any)
	if !ok {
		t.Fatalf("comment is %T, not a JSON object", got.Comments[len(got.Comments)-1])
	}
	if last["body"] != "F0.T1's layout is missing app/main.py" {
		t.Errorf("comment = %v; want the reason verbatim", last)
	}
	// blocked can go on to todo, in-progress, done or stay blocked — the
	// list an agent needs to get itself unstuck.
	if want := []string{"todo", "in-progress", "done", "blocked"}; !reflect.DeepEqual(got.LegalTransitions, want) {
		t.Errorf("legal_transitions = %v; want %v", got.LegalTransitions, want)
	}
}

// ---- the read-only tools ---------------------------------------------------

func TestGetTaskCarriesTheDispatchShape(t *testing.T) {
	cs, _, _ := newTestSession(t)

	var got getTaskOut
	call(t, cs, "orch_get_task", map[string]any{"task_id": "F0.T1"}, &got)

	if got.Title != "Scaffold FastAPI project layout" {
		t.Errorf("title = %q", got.Title)
	}
	if got.Model != "claude/claude-sonnet-4-6" {
		t.Errorf("model = %q", got.Model)
	}
	if got.EstimateHours != 0.3 {
		t.Errorf("estimate_hours = %v; want 0.3", got.EstimateHours)
	}
	if got.SpecRef != "f0-foundation.md#T1" {
		t.Errorf("spec_ref = %q", got.SpecRef)
	}
	want := []string{"pyproject.toml", "app/__init__.py", "app/main.py", "tests/__init__.py"}
	if !reflect.DeepEqual(got.Files, want) {
		t.Errorf("files = %v; want %v", got.Files, want)
	}
	if !reflect.DeepEqual(got.Dependencies, []string{}) {
		t.Errorf("dependencies = %v; want an empty list, not null", got.Dependencies)
	}
}

func TestListTasksFilters(t *testing.T) {
	cs, _, backend := newTestSession(t)
	ctx := context.Background()
	if err := backend.Transition(ctx, "F1.T1", model.StatusBlocked, state.Note{Author: "test", Body: "waiting"}); err != nil {
		t.Fatalf("seed F1.T1 blocked: %v", err)
	}

	cases := []struct {
		name string
		args map[string]any
		want []string
	}{
		{"no filter", map[string]any{}, []string{"F0.T1", "F1.T1"}},
		{"by status", map[string]any{"status": []string{"blocked"}}, []string{"F1.T1"}},
		{"by two statuses", map[string]any{"status": []string{"todo", "blocked"}}, []string{"F0.T1", "F1.T1"}},
		{"by id", map[string]any{"ids": []string{"F1.T1"}}, []string{"F1.T1"}},
		{"ready", map[string]any{"ready": true}, []string{"F0.T1"}},
		{"limit", map[string]any{"limit": 1}, []string{"F0.T1"}},
		{"no match", map[string]any{"status": []string{"done"}}, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out listTasksOut
			call(t, cs, "orch_list_tasks", tc.args, &out)
			var ids []string
			for _, row := range out.Tasks {
				ids = append(ids, row.ID)
			}
			if len(ids) == 0 {
				ids = []string{}
			}
			if !reflect.DeepEqual(ids, tc.want) {
				t.Errorf("ids = %v; want %v", ids, tc.want)
			}
			if out.Count != len(out.Tasks) {
				t.Errorf("count = %d; want %d", out.Count, len(out.Tasks))
			}
		})
	}

	// Total counts the matches, Count the rows returned — which is how a
	// caller tells a truncated page from a complete one.
	var limited listTasksOut
	call(t, cs, "orch_list_tasks", map[string]any{"limit": 1}, &limited)
	if limited.Count != 1 || limited.Total != 2 {
		t.Errorf("count/total = %d/%d; want 1/2", limited.Count, limited.Total)
	}
}

func TestListTasksRejectsAnUnknownStatus(t *testing.T) {
	cs, _, _ := newTestSession(t)

	res, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name:      "orch_list_tasks",
		Arguments: map[string]any{"status": []string{"finished"}},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("an unknown status filter was accepted — it would have matched nothing, silently")
	}
}

// TestContextPointsAtTheProjectsOwnSpecs is bug 12's guard on this side of
// the port: `spec_root` supplies the `specs/` prefix, so a template whose
// specRef carried one too would send the agent to `specs/specs/...`. The
// assertion is the whole resolved path against the file `orch init` tells the
// operator to write — not a prefix check, which is what let bug 12 through.
func TestContextPointsAtTheProjectsOwnSpecs(t *testing.T) {
	cs, root, _ := newTestSession(t)

	var out contextOut
	call(t, cs, "orch_context", map[string]any{"task_id": "F0.T1"}, &out)
	if out.Task == nil {
		t.Fatal("no task in the context")
	}
	if want := "specs/f0-foundation.md#T1"; out.Task.SpecRefPath != want {
		t.Fatalf("spec_ref_path = %q; want %q", out.Task.SpecRefPath, want)
	}
	// And the directory that path is relative to is the one the scaffolder
	// created, so the agent is not being sent somewhere that does not exist.
	if _, err := os.Stat(filepath.Join(root, out.Project.SpecRoot)); err != nil {
		t.Errorf("spec_root %q does not exist in the scaffolded project: %v", out.Project.SpecRoot, err)
	}
}

func TestContextReportsDependenciesAndCounts(t *testing.T) {
	cs, _, _ := newTestSession(t)

	var project contextOut
	call(t, cs, "orch_context", map[string]any{}, &project)
	if project.Task != nil {
		t.Error("a context with no task_id carried a task")
	}
	if project.Project.Total != 2 {
		t.Errorf("total = %d; want 2", project.Project.Total)
	}
	// Every status is a key, zeroes included: a missing key reads as "none"
	// only if the caller knows the key would have been there.
	for _, st := range []string{"backlog", "todo", "in-progress", "done", "blocked"} {
		if _, ok := project.Project.Counts[st]; !ok {
			t.Errorf("counts has no %q key", st)
		}
	}
	if project.Project.Counts["todo"] != 2 {
		t.Errorf("counts[todo] = %d; want 2", project.Project.Counts["todo"])
	}

	// F1.T1 depends on F0.T1, which is not done: pending, not a dependency
	// whose comment can be read.
	var blocked contextOut
	call(t, cs, "orch_context", map[string]any{"task_id": "F1.T1"}, &blocked)
	if !reflect.DeepEqual(blocked.Task.PendingDependencies, []string{"F0.T1"}) {
		t.Errorf("pending = %v; want [F0.T1]", blocked.Task.PendingDependencies)
	}
	if len(blocked.Task.Dependencies) != 0 {
		t.Errorf("dependencies = %v; want none while F0.T1 is unfinished", blocked.Task.Dependencies)
	}

	// Finish F0.T1 and it moves across, carrying what it reported.
	var done setStatusOut
	call(t, cs, "orch_set_status", map[string]any{"task_id": "F0.T1", "status": "in-progress"}, &done)
	call(t, cs, "orch_set_status", map[string]any{
		"task_id": "F0.T1", "status": "done", "note": "layout is in place",
	}, &done)

	call(t, cs, "orch_context", map[string]any{"task_id": "F1.T1"}, &blocked)
	if len(blocked.Task.PendingDependencies) != 0 {
		t.Errorf("pending = %v; want none", blocked.Task.PendingDependencies)
	}
	if len(blocked.Task.Dependencies) != 1 || blocked.Task.Dependencies[0].ID != "F0.T1" {
		t.Fatalf("dependencies = %+v; want F0.T1", blocked.Task.Dependencies)
	}
	if blocked.Task.Dependencies[0].LastComment != "layout is in place" {
		t.Errorf("last_comment = %q; want what F0.T1 reported",
			blocked.Task.Dependencies[0].LastComment)
	}
}

func TestBudgetIsDisabledWithoutAGate(t *testing.T) {
	cs, _, _ := newTestSession(t)

	var out budgetOut
	call(t, cs, "orch_budget", map[string]any{}, &out)
	if out.Enabled {
		t.Error("enabled with no gate configured")
	}
	if out.Providers == nil {
		t.Error("providers is null; want an empty object a caller can range over")
	}
}

// ---- orch_events against a database Python wrote ---------------------------

// pythonFixture is the real orch.db written by orch v0.11.0 (Python) — see
// internal/state/testdata/README.md. Go has never written to it.
//
// orch_events is the one tool whose answer comes entirely from rows this
// package cannot produce itself: a transition writes a comment, not an event,
// so a test that seeded its own data through Go would be reading back
// something Go had just invented. These four rows were appended by Python.
const pythonFixture = "../state/testdata/orch-py-0.11.0.db"

// copyPythonFixture returns a writable copy: opening it in WAL mode writes
// sidecars and can rewrite the header, so the file in the repo is never the
// one a test opens.
func copyPythonFixture(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile(pythonFixture)
	if err != nil {
		t.Fatalf("read the Python fixture: %v", err)
	}
	path := filepath.Join(t.TempDir(), "orch.db")
	// #nosec G703 -- the destination is t.TempDir(), not caller input.
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatalf("copy the fixture: %v", err)
	}
	return path
}

func TestEventsReadsAPythonEventLog(t *testing.T) {
	ctx := context.Background()
	db, _, err := state.Open(ctx, copyPythonFixture(t))
	if err != nil {
		t.Fatalf("open the fixture: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	cs := connect(t, Options{
		Backend:   state.NewSQLite(db, "billing-api", t.TempDir()),
		ProjectID: "billing-api",
		Version:   "v-test",
	})

	// Every event, oldest first — the four rows the fixture's README
	// enumerates, in the order Python wrote them.
	var all eventsOut
	call(t, cs, "orch_events", map[string]any{"limit": 0}, &all)
	type row struct{ typ, task, ts string }
	got := make([]row, 0, len(all.Events))
	for _, e := range all.Events {
		got = append(got, row{e.EventType, e.TaskID, e.TS})
	}
	want := []row{
		{"dispatch", "F0.T1", "2026-09-01T09:00:00+00:00"},
		{"success", "F0.T1", "2026-09-01T10:30:00+00:00"},
		{"dispatch", "F1.T1", "2026-09-02T11:15:00+00:00"},
		{"block", "F1.T2", "2026-09-03T14:45:00+00:00"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("events = %+v\nwant     %+v", got, want)
	}
	if all.Count != 4 {
		t.Errorf("count = %d; want 4", all.Count)
	}

	// Scoped to one task.
	var one eventsOut
	call(t, cs, "orch_events", map[string]any{"task_id": "F0.T1", "limit": 0}, &one)
	if len(one.Events) != 2 {
		t.Fatalf("F0.T1 events = %d; want 2", len(one.Events))
	}
	for _, e := range one.Events {
		if e.TaskID != "F0.T1" {
			t.Errorf("task_id filter leaked %s", e.TaskID)
		}
		if e.RunID != "fixture-run-0001" {
			t.Errorf("run_id = %q; want fixture-run-0001", e.RunID)
		}
		if e.Extra == nil {
			t.Error("extra is null; want an empty object")
		}
	}

	// A limit returns the newest N, still oldest first — the tail, which is
	// what `orch events --tail` means and what a caller catching up wants.
	var tail eventsOut
	call(t, cs, "orch_events", map[string]any{"limit": 2}, &tail)
	if len(tail.Events) != 2 {
		t.Fatalf("tail = %d events; want 2", len(tail.Events))
	}
	if tail.Events[0].TaskID != "F1.T1" || tail.Events[1].TaskID != "F1.T2" {
		t.Errorf("tail = %s, %s; want the last two rows",
			tail.Events[0].TaskID, tail.Events[1].TaskID)
	}
}

// ---- orch_budget against the shipped budgets.yaml ---------------------------

// TestBudgetReportsTheShippedPreset loads the budgets.yaml `orch init` writes,
// not a sample invented here (checklist rule 28), so a preset whose shape
// changed breaks this test too.
func TestBudgetReportsTheShippedPreset(t *testing.T) {
	ctx := context.Background()
	db, _, err := state.Open(ctx, copyPythonFixture(t))
	if err != nil {
		t.Fatalf("open the fixture: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	backend := state.NewSQLite(db, "billing-api", t.TempDir())

	cfg, err := budget.LoadConfig("../scaffold/defaults/budgets.yaml", "conservative")
	if err != nil {
		t.Fatalf("load the shipped budgets.yaml: %v", err)
	}
	cs := connect(t, Options{
		Backend:   backend,
		ProjectID: "billing-api",
		Budget:    budget.NewGate(backend, cfg),
		Version:   "v-test",
	})

	var out budgetOut
	call(t, cs, "orch_budget", map[string]any{}, &out)
	if !out.Enabled {
		t.Fatal("enabled is false with a gate configured")
	}
	for _, provider := range []string{"claude", "codex", "opencode", "agy"} {
		row, ok := out.Providers[provider]
		if !ok {
			t.Errorf("no row for %s", provider)
			continue
		}
		if row.TokenBudget == 0 || row.WindowHours == 0 || row.ThresholdPct == 0 {
			t.Errorf("%s = %+v; the preset's numbers did not reach the tool", provider, row)
		}
	}
	// The preset's own figures, so a silently-renamed key shows up as a zero
	// rather than as a row that merely exists.
	if got := out.Providers["claude"]; got.TokenBudget != 800000 || got.WindowHours != 5 || got.ThresholdPct != 60 {
		t.Errorf("claude = %+v; want the conservative preset's 800000/5h/60%%", got)
	}
}

// A write to a task the database does not have sends `currentStatus` to
// tasks.json, which is where a task added since the last run would be. If
// that file cannot be read, the project is broken — and saying "unknown task
// id" sends the agent looking for a typo in an id instead.
func TestAnUnreadableTasksFileIsReportedAsItself(t *testing.T) {
	root, backend := newTestProject(t)
	broken := filepath.Join(root, "tasks.json")
	if err := os.WriteFile(broken, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("corrupt tasks.json: %v", err)
	}
	cs := connect(t, Options{
		Backend: backend, TasksJSON: broken, ProjectID: "proj",
		ProjectRoot: root, Version: "v-test",
	})

	res, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name:      "orch_set_status",
		Arguments: map[string]any{"task_id": "F9.T9", "status": "done"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("a write against a broken project succeeded")
	}
	text, ok := res.Content[0].(*mcpsdk.TextContent)
	if !ok {
		t.Fatalf("content[0] = %#v; want text", res.Content[0])
	}
	if !strings.Contains(text.Text, "tasks.json") {
		t.Errorf("error text = %q; it must name the file that could not be read", text.Text)
	}
	// And a task the database DOES have is still writable — a malformed
	// manifest must not take the whole server down with it, since the
	// database is the source of truth for status.
	var out setStatusOut
	call(t, cs, "orch_set_status", map[string]any{"task_id": "F0.T1", "status": "in-progress"}, &out)
	if !out.OK {
		t.Errorf("a known task became unwritable: %+v", out)
	}
}
