package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hectorcanaimero/orch/internal/config"
	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/state"
)

// fakeState is the two methods StateReader asks for. A real SQLite backend
// would test the backend; these tests are about what the handlers do with
// what it returns, including the answers a real one is hard to provoke — a
// read that fails, an event log with a malformed row.
type fakeState struct {
	tasks      []state.TaskRuntime
	events     []state.Event
	spends     []state.Spend
	tasksErr   error
	eventsErr  error
	spendErr   error
	done7d     int
	lastEvents map[string]state.Event

	latestErr error
	sinceErr  error
	// mu guards events, which the live-tail tests append to while a stream
	// goroutine reads it.
	mu            sync.Mutex
	done7dErr     error
	lastEventsErr error
	// askedForEvents records the id list the sprint handler passed, which is
	// how "it only asks about the blocked tasks" is testable.
	askedForEvents []string

	lastN int
	// lastSince records the window the handler asked for, which is how the
	// budget summary's "today" and the metrics page's "all of history" are
	// told apart.
	lastSince time.Time

	// stakeholderTokenHash/stakeholderTokenOK/stakeholderTokenErr back
	// StakeholderToken (G8.2/F3.3) — zero value (ok=false, err=nil) is "no
	// row for this project", the common case in these tests, which makes
	// the access-gate tests fall through to Config.Token/fallbackTokenHash
	// exactly like a project that has never rotated one.
	stakeholderTokenHash string
	stakeholderTokenOK   bool
	stakeholderTokenErr  error

	// runs and inFlight back /api/now; runsErr is also how a test says "never
	// ran" (state.ErrNoRuns).
	runs        []state.Run
	runsErr     error
	inFlight    []state.Dispatch
	inFlightErr error
}

func (f *fakeState) LatestRun(context.Context) (state.Run, error) {
	if f.runsErr != nil {
		return state.Run{}, f.runsErr
	}
	if len(f.runs) == 0 {
		return state.Run{}, state.ErrNoRuns
	}
	return f.runs[0], nil
}

func (f *fakeState) InFlightDispatches(context.Context) ([]state.Dispatch, error) {
	return f.inFlight, f.inFlightErr
}

func (f *fakeState) Tasks(context.Context, state.TaskFilter) ([]state.TaskRuntime, error) {
	return f.tasks, f.tasksErr
}

func (f *fakeState) AllEvents(_ context.Context, n int) ([]state.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastN = n
	if f.eventsErr != nil {
		return nil, f.eventsErr
	}
	if n > 0 && n < len(f.events) {
		return f.events[len(f.events)-n:], nil
	}
	return f.events, nil
}

func (f *fakeState) AllSpend(_ context.Context, since time.Time) ([]state.Spend, error) {
	f.lastSince = since
	if f.spendErr != nil {
		return nil, f.spendErr
	}
	out := make([]state.Spend, 0, len(f.spends))
	for _, s := range f.spends {
		ts, ok := state.ParseTS(s.TS)
		if !ok || ts.Before(since) {
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

// SpendSince is what budget.Gate calls, one provider at a time.
func (f *fakeState) SpendSince(_ context.Context, backend string, since time.Time) ([]state.Spend, error) {
	if f.spendErr != nil {
		return nil, f.spendErr
	}
	out := make([]state.Spend, 0, len(f.spends))
	for _, s := range f.spends {
		if s.Backend != backend {
			continue
		}
		ts, ok := state.ParseTS(s.TS)
		if !ok || ts.Before(since) {
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

// latestEventID and eventsSince back the live tail. The fake keeps its own
// slice so a test can append while a stream is running, which is what makes
// "the tail delivers what arrives after you connected" testable at all.
func (f *fakeState) LatestEventID(context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.latestErr != nil {
		return 0, f.latestErr
	}
	var max int64
	for _, e := range f.events {
		if e.ID > max {
			max = e.ID
		}
	}
	return max, nil
}

func (f *fakeState) EventsSince(_ context.Context, afterID int64, limit int) ([]state.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sinceErr != nil {
		return nil, f.sinceErr
	}
	out := make([]state.Event, 0, len(f.events))
	for _, e := range f.events {
		if e.ID <= afterID {
			continue
		}
		out = append(out, e)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

// appendEvent is how a test writes into the log while a stream is reading it.
func (f *fakeState) appendEvent(e state.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, e)
}

func (f *fakeState) CountDoneLastNDays(_ context.Context, _ int) (int, error) {
	return f.done7d, f.done7dErr
}

func (f *fakeState) LastEventByTask(_ context.Context, taskIDs []string) (map[string]state.Event, error) {
	f.askedForEvents = taskIDs
	if f.lastEventsErr != nil {
		return nil, f.lastEventsErr
	}
	if f.lastEvents == nil {
		return map[string]state.Event{}, nil
	}
	return f.lastEvents, nil
}

func (f *fakeState) StakeholderToken(context.Context) (string, time.Time, bool, error) {
	return f.stakeholderTokenHash, time.Time{}, f.stakeholderTokenOK, f.stakeholderTokenErr
}

const testTasksJSON = `{
  "meta": {"project": "demo"},
  "tasks": [
    {"id": "T-1", "phase": 0, "title": "Scaffold", "model": "claude/sonnet",
     "status": "todo", "dependencies": [], "estimateHours": 1.0,
     "description": "lay the ground", "specRef": "specs/f0.md#T1"},
    {"id": "T-2", "phase": 0, "title": "Database", "model": "claude/sonnet",
     "status": "todo", "dependencies": ["T-1"], "estimateHours": 2.0},
    {"id": "T-3", "phase": 1, "title": "API", "model": "codex/gpt",
     "status": "blocked", "dependencies": ["T-2"], "estimateHours": 0.5}
  ]
}`

func newReadServer(t *testing.T, st StateReader) *Server {
	t.Helper()
	root := writeProject(t, "spec_root: specs\n", testTasksJSON)
	s, err := New(cfg(ProfileOperator, ""), Options{
		Static: spaHandler(t),
		State:  st,
		Paths:  config.Paths{Root: root, ID: "demo", ConfigYAML: filepath.Join(root, "config.yaml")},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// newGatedServer is newReadServer under the stakeholder profile, for the
// tests that check a route is behind the gate.
func newGatedServer(t *testing.T, st StateReader) *Server {
	t.Helper()
	root := writeProject(t, "spec_root: specs\n", testTasksJSON)
	s, err := New(cfg(ProfileStakeholder, "test-token-stakeholder"), Options{
		Static: spaHandler(t),
		State:  st,
		Paths:  config.Paths{Root: root, ID: "demo", ConfigYAML: filepath.Join(root, "config.yaml")},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// The database wins over config.yaml/--token whenever this project has a
// row (G8.2/F3.3): a rotated token invalidates the old one immediately, on
// an already-running dashboard, with no restart — expectedTokenHash resolves
// fresh on every gated request rather than once at startup.
func TestGatedRouteUsesDatabaseTokenOverConfig(t *testing.T) {
	dbHash := HashToken("token-from-the-database")
	s := newGatedServer(t, &fakeState{stakeholderTokenHash: dbHash, stakeholderTokenOK: true})

	// The config-file token ("test-token-stakeholder", baked into
	// newGatedServer) no longer works once the database has a row.
	if resp := get(t, s, "/api/whoami?token=test-token-stakeholder"); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("old config token once the database has a row: status = %d, want 401", resp.StatusCode)
	}
	// The database's token does.
	if resp := get(t, s, "/api/whoami?token=token-from-the-database"); resp.StatusCode != http.StatusOK {
		t.Errorf("database token: status = %d, want 200", resp.StatusCode)
	}
}

// A database read error falls back to config.yaml/--token rather than
// refusing every request — a transient hiccup should not be worse than the
// server this project had before G8.2.
// A database read error fails closed — 401, not a fallback to
// config.yaml/--token. Opus's review of this PR: "no puedo saber si rotó"
// (err != nil) is not the same state as "this project never rotated"
// (ok=false, err=nil), and falling back on the former would resurrect a
// token an operator deliberately rotated away the moment the database that
// would have told them so becomes unreachable — precisely what rotating
// exists to prevent. Asserted by what it does NOT do (rule 25): the
// config-file token must NOT still work here.
func TestGatedRouteFailsClosedOnDatabaseError(t *testing.T) {
	s := newGatedServer(t, &fakeState{stakeholderTokenErr: errors.New("database is locked")})

	if resp := get(t, s, "/api/whoami?token=test-token-stakeholder"); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("config token after a database read error: status = %d, want 401 (fail closed)", resp.StatusCode)
	}
}

func decode[T any](t *testing.T, resp *http.Response, into *T) {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
		t.Fatalf("decode %T: %v", into, err)
	}
}

// The status the database holds wins over the status the file was written
// with. This is issue #72 as a test: three Python endpoints shipped stale
// statuses because only one of them hydrated.
func TestTasksShowsTheDatabaseStatusNotTheFiles(t *testing.T) {
	s := newReadServer(t, &fakeState{tasks: []state.TaskRuntime{
		{ID: "T-1", Status: model.StatusDone},
		{ID: "T-2", Status: model.StatusInProgress},
	}})

	var got tasksPayload
	decode(t, get(t, s, "/api/tasks"), &got)

	if got.Total != 3 || got.Count != 3 {
		t.Fatalf("count/total = %d/%d, want 3/3", got.Count, got.Total)
	}
	want := map[string]string{"T-1": "done", "T-2": "in-progress", "T-3": "blocked"}
	for _, row := range got.Tasks {
		if row.Status != want[row.ID] {
			t.Errorf("%s status = %q, want %q", row.ID, row.Status, want[row.ID])
		}
	}
	// T-3 has no database row, so tasks.json's own status stands.
	if got.Summary.Total != 3 || got.Summary.Done != 1 || got.Summary.Blocked != 1 {
		t.Errorf("summary = %+v", got.Summary)
	}
	if got.ProjectID != "demo" {
		t.Errorf("project_id = %q, want demo", got.ProjectID)
	}
}

// `count` narrows with the filters, `total` does not. A page showing "1 task"
// with no second number cannot tell an empty filter from an empty project.
func TestTasksFiltersNarrowCountButNotTotal(t *testing.T) {
	s := newReadServer(t, &fakeState{})

	for _, tc := range []struct {
		query string
		want  []string
		why   string
	}{
		{"", []string{"T-1", "T-2", "T-3"}, "no filter"},
		{"?phase=0", []string{"T-1", "T-2"}, "by phase"},
		{"?phase=notanumber", []string{"T-1", "T-2", "T-3"}, "a phase that is not a number is no filter, not a 422"},
		{"?status=blocked", []string{"T-3"}, "by status"},
		{"?model=codex/gpt", []string{"T-3"}, "by model"},
		{"?q=DATAB", []string{"T-2"}, "case-insensitive title match"},
		{"?q=ground", []string{"T-1"}, "the description is searched too"},
		{"?q=specs/f0", nil, "the spec ref is NOT searched"},
		{"?q=t-", []string{"T-1", "T-2", "T-3"}, "the id is searched"},
		{"?parallelizable=1", []string{"T-1"}, "only T-1 has all deps done (it has none)"},
		{"?phase=0&status=todo", []string{"T-1", "T-2"}, "filters compose"},
	} {
		t.Run(tc.why, func(t *testing.T) {
			var got tasksPayload
			decode(t, get(t, s, "/api/tasks"+tc.query), &got)
			ids := make([]string, 0, len(got.Tasks))
			for _, row := range got.Tasks {
				ids = append(ids, row.ID)
			}
			if len(ids) != len(tc.want) {
				t.Fatalf("%s: got %v, want %v", tc.query, ids, tc.want)
			}
			for i := range ids {
				if ids[i] != tc.want[i] {
					t.Fatalf("%s: got %v, want %v", tc.query, ids, tc.want)
				}
			}
			if got.Count != len(ids) {
				t.Errorf("count = %d, want %d", got.Count, len(ids))
			}
			if got.Total != 3 {
				t.Errorf("total = %d, want 3 — filters must not move it", got.Total)
			}
		})
	}
}

// The per-task figures come from the event log, and the two the log produces
// are not interchangeable: hours is derived from PAIRS of events, last
// updated from any single one.
func TestTaskRowsCarryTheEventDerivedFigures(t *testing.T) {
	s := newReadServer(t, &fakeState{events: []state.Event{
		{TaskID: "T-1", EventType: "dispatch", TS: "2026-09-01T10:00:00Z"},
		{TaskID: "T-1", EventType: "success", TS: "2026-09-01T11:30:00Z"},
		{TaskID: "T-3", EventType: "block", TS: "2026-09-01T12:00:00Z"},
	}})

	var got tasksPayload
	decode(t, get(t, s, "/api/tasks"), &got)

	byID := map[string]taskPayload{}
	for _, row := range got.Tasks {
		byID[row.ID] = row
	}
	if h := byID["T-1"].HumanHours; h != 1.5 {
		t.Errorf("T-1 human_hours = %v, want 1.5", h)
	}
	if u := byID["T-1"].LastUpdated; u != "2026-09-01T11:30:00Z" {
		t.Errorf("T-1 last_updated = %q", u)
	}
	// T-3 was blocked, never dispatched: it has a last event and no hours.
	if h := byID["T-3"].HumanHours; h != 0 {
		t.Errorf("T-3 human_hours = %v, want 0", h)
	}
	if u := byID["T-3"].LastUpdated; u != "2026-09-01T12:00:00Z" {
		t.Errorf("T-3 last_updated = %q", u)
	}
	// T-1 blocks T-2 which blocks T-3: two tasks wait on it.
	if d := byID["T-1"].Downstream; d != 2 {
		t.Errorf("T-1 downstream_impact = %d, want 2", d)
	}
}

func TestTaskDetail(t *testing.T) {
	s := newReadServer(t, &fakeState{})

	var got taskPayload
	decode(t, get(t, s, "/api/task/T-1"), &got)
	if got.ID != "T-1" || got.Title != "Scaffold" {
		t.Fatalf("got %+v", got)
	}
	if got.Parallelizable == nil || !*got.Parallelizable {
		t.Errorf("parallelizable = %v, want true — T-1 has no dependencies", got.Parallelizable)
	}
	if got.SpecRef != "specs/f0.md#T1" || got.DepCount != 0 {
		t.Errorf("spec_ref = %q, dep_count = %d", got.SpecRef, got.DepCount)
	}

	// The list endpoint omits the field rather than shipping it false: it is
	// the same answer the `parallelizable=1` filter already gives there.
	var list tasksPayload
	decode(t, get(t, s, "/api/tasks"), &list)
	for _, row := range list.Tasks {
		if row.Parallelizable != nil {
			t.Errorf("%s carries parallelizable on the list endpoint", row.ID)
		}
	}
}

func TestTaskDetailNotFound(t *testing.T) {
	s := newReadServer(t, &fakeState{})
	resp := get(t, s, "/api/task/NOPE")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	var got errorPayload
	decode(t, resp, &got)
	if got.Detail != "task not found: NOPE" {
		t.Errorf("detail = %q", got.Detail)
	}
}

func TestGraphNodesAndEdges(t *testing.T) {
	s := newReadServer(t, &fakeState{})

	var got graphPayload
	decode(t, get(t, s, "/api/graph"), &got)

	if len(got.Nodes) != 3 || len(got.Edges) != 2 {
		t.Fatalf("%d nodes, %d edges; want 3 and 2", len(got.Nodes), len(got.Edges))
	}
	// The edge points at what waits: dep → task.
	if got.Edges[0].Source != "T-1" || got.Edges[0].Target != "T-2" {
		t.Errorf("edge[0] = %+v, want T-1 → T-2", got.Edges[0])
	}
	// Every node is on the only path through this chain.
	for _, n := range got.Nodes {
		if !n.OnPath {
			t.Errorf("%s is not on the critical path of a straight chain", n.ID)
		}
	}
}

func TestEventsFormatsAndLimits(t *testing.T) {
	f := &fakeState{events: []state.Event{
		{TaskID: "T-1", EventType: "dispatch", TS: "2026-09-01T10:00:00Z", Backend: "claude",
			Extra: map[string]any{"cli_model": "claude-sonnet-4-6"}},
		{TaskID: "T-1", EventType: "success", TS: "2026-09-01T10:10:12Z", Backend: "claude",
			Extra: map[string]any{"duration_s": 612.4, "cost_usd": 0.21}},
		{TaskID: "T-2", EventType: "block", TS: "2026-09-01T10:20:00Z",
			Extra: map[string]any{"reason": "dep T-1 not done"}},
	}}
	s := newReadServer(t, f)

	var got eventsPayload
	decode(t, get(t, s, "/api/events"), &got)
	if got.Count != 3 {
		t.Fatalf("count = %d, want 3", got.Count)
	}
	if f.lastN != 200 {
		t.Errorf("asked the backend for %d events, want the default 200", f.lastN)
	}

	wantHuman := []string{
		"[10:00:00] -> T-1 dispatch -> claude-sonnet-4-6",
		"[10:10:12] OK T-1 success (10m 12s, $0.210)",
		"[10:20:00] BLOCK T-2 block (dep T-1 not done)",
	}
	wantSeverity := []string{"info", "ok", "block"}
	for i, row := range got.Events {
		if row.Human != wantHuman[i] {
			t.Errorf("human[%d] = %q, want %q", i, row.Human, wantHuman[i])
		}
		if row.Severity != wantSeverity[i] {
			t.Errorf("severity[%d] = %q, want %q", i, row.Severity, wantSeverity[i])
		}
	}

	// task_id narrows AFTER the tail is taken, so the window is "the last N
	// events", not "the last N of this task".
	var filtered eventsPayload
	decode(t, get(t, s, "/api/events?task_id=T-1&limit=2"), &filtered)
	if f.lastN != 2 {
		t.Errorf("limit did not reach the backend: asked for %d", f.lastN)
	}
	if filtered.Count != 1 {
		t.Errorf("count = %d, want 1 — the last 2 events hold one T-1 row", filtered.Count)
	}
}

func TestEventsLimitIsClampedNotRefused(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want int
	}{
		{"", 200}, {"50", 50}, {"0", 1}, {"-5", 1}, {"99999", 1000}, {"abc", 200},
	} {
		if got := clampLimit(tc.raw); got != tc.want {
			t.Errorf("clampLimit(%q) = %d, want %d", tc.raw, got, tc.want)
		}
	}
}

// An unknown event type still renders: grey, with a `*`, and the row intact.
// A log view that went blank on an event a newer orch emits would hide
// exactly the thing the operator opened it for.
func TestUnknownEventTypeStillRenders(t *testing.T) {
	got := formatEvent(state.Event{
		TaskID: "T-9", EventType: "invented_later", TS: "2026-09-01T10:00:00Z",
	})
	if got.Severity != "muted" || got.Human != "[10:00:00] * T-9 invented_later" {
		t.Errorf("got %+v", got)
	}

	// And so does an event with nothing in it at all.
	empty := formatEvent(state.Event{})
	if empty.Human != "[] * ? ?" || empty.Severity != "muted" {
		t.Errorf("empty event rendered as %q", empty.Human)
	}
}

// A read that fails is a 500 naming what failed, and the wrapped error — which
// carries absolute paths — goes to the log, not to the client.
func TestReadFailuresAre500WithoutThePath(t *testing.T) {
	boom := errors.New("database is locked")
	for _, tc := range []struct {
		path  string
		state *fakeState
	}{
		{"/api/tasks", &fakeState{tasksErr: boom}},
		{"/api/task/T-1", &fakeState{tasksErr: boom}},
		{"/api/graph", &fakeState{tasksErr: boom}},
		{"/api/events", &fakeState{eventsErr: boom}},
	} {
		resp := get(t, newReadServer(t, tc.state), tc.path)
		if resp.StatusCode != http.StatusInternalServerError {
			t.Errorf("%s: status = %d, want 500", tc.path, resp.StatusCode)
		}
		var got errorPayload
		decode(t, resp, &got)
		if got.Detail == "" || strings.Contains(got.Detail, "locked") {
			t.Errorf("%s: detail = %q — it must name the endpoint, not echo the error", tc.path, got.Detail)
		}
	}
}

// A server built with no backend answers 500 rather than an empty project. An
// empty payload would read as "this project has no tasks", which is a lie a
// page cannot recover from.
func TestReadEndpointsNeedABackend(t *testing.T) {
	root := writeProject(t, "spec_root: specs\n", testTasksJSON)
	s := newTestServer(t, cfg(ProfileOperator, ""), root)
	for _, path := range []string{"/api/tasks", "/api/graph", "/api/events"} {
		if resp := get(t, s, path); resp.StatusCode != http.StatusInternalServerError {
			t.Errorf("%s with no backend: status = %d, want 500", path, resp.StatusCode)
		}
	}
}

// The read endpoints are gated like every other data route — registered
// through `gated`, not next to the static handler.
func TestReadEndpointsAreGated(t *testing.T) {
	root := writeProject(t, "spec_root: specs\n", testTasksJSON)
	s, err := New(cfg(ProfileStakeholder, "test-token-stakeholder"), Options{
		Static: spaHandler(t),
		State:  &fakeState{},
		Paths:  config.Paths{Root: root, ID: "demo", ConfigYAML: filepath.Join(root, "config.yaml")},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/tasks", "/api/task/T-1", "/api/graph", "/api/events"} {
		if resp := get(t, s, path); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s with no token: status = %d, want 401", path, resp.StatusCode)
		}
		// With a token they are still off the stakeholder allow-list: none of
		// these four is on it, and operator data is not stakeholder data.
		if resp := get(t, s, path+"?token=test-token-stakeholder"); resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s with a token: status = %d, want 403", path, resp.StatusCode)
		}
	}
}

// The task detail serves the comments the DATABASE holds, not the empty array
// tasks.json carries. Since F-12 every `Transition` appends a note to
// `tasks_runtime.comments_json`, and a scaffolded tasks.json has none — so
// before this, a project orch had actually run showed no comments at all,
// with the notes sitting in the database the same request had opened.
func TestTaskDetailServesTheDatabasesComments(t *testing.T) {
	note := `{"at":"2026-09-12T03:27:34Z","author":"operator","body":"manual set via orch task set"}`
	s := newReadServer(t, &fakeState{tasks: []state.TaskRuntime{{
		ID: "T-1", Status: model.StatusInProgress,
		Comments: []json.RawMessage{json.RawMessage(note)},
	}}})

	var got taskPayload
	decode(t, get(t, s, "/api/task/T-1"), &got)
	if len(got.Comments) != 1 {
		t.Fatalf("comments = %v, want the one the database holds", got.Comments)
	}
	if !strings.Contains(string(got.Comments[0]), "manual set via orch task set") {
		t.Errorf("comment = %s", got.Comments[0])
	}

	// A task with no runtime row still reports an empty array rather than
	// null — the SPA maps over it.
	var other taskPayload
	decode(t, get(t, s, "/api/task/T-2"), &other)
	if other.Comments == nil {
		t.Error("comments = null for a task with no row; the SPA maps over it")
	}
}
