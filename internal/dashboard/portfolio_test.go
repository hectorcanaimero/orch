package dashboard

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hectorcanaimero/orch/internal/config"
	"github.com/hectorcanaimero/orch/internal/state"
)

// testToken is the token the gated project expects.
//
// The same spelling every other test in this package uses. Not a
// leetspeak-looking string: rule 5 makes anything that reads like a
// credential blocking even when it is invented, and a reviewer should not
// have to decide whether a fixture is real.
const testToken = "test-token-stakeholder"

// errBoom is the failure the fakes raise — the same shape readroutes_test
// uses, because "the database would not answer" is the only read failure
// these paths have to survive.
var errBoom = errors.New("database is locked")

// frozen is the clock the portfolio projects from, so an ETA is a value a
// test can write down.
var frozen = time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)

// portfolioServer builds one project's Server over a fake backend.
func portfolioServer(t *testing.T, id string, profile Profile, token string, st StateReader) *Server {
	t.Helper()
	root := writeProject(t, "spec_root: specs\n", testTasksJSON)
	s, err := New(cfg(profile, token), Options{
		Static: spaHandler(t),
		State:  st,
		Paths:  config.Paths{Root: root, ID: id, ConfigYAML: filepath.Join(root, "config.yaml")},
	})
	if err != nil {
		t.Fatalf("New(%s): %v", id, err)
	}
	return s
}

func newPortfolio(t *testing.T, opts PortfolioOptions) *Portfolio {
	t.Helper()
	if opts.Config.Profile == "" {
		opts.Config = cfg(ProfileOperator, "")
	}
	if opts.Static == nil {
		opts.Static = spaHandler(t)
	}
	if opts.Now == nil {
		opts.Now = func() time.Time { return frozen }
	}
	p, err := NewPortfolio(opts)
	if err != nil {
		t.Fatalf("NewPortfolio: %v", err)
	}
	return p
}

func getPortfolio(t *testing.T, p *Portfolio) portfolioPayload {
	t.Helper()
	rec := httptest.NewRecorder()
	p.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/portfolio", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/portfolio = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var out portfolioPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode /api/portfolio: %v\n%s", err, rec.Body.String())
	}
	return out
}

// ---- the payload -----------------------------------------------------------

// TestPortfolioRowUsesTheSameNumbersAsTheProject is the property the whole
// design rests on: a row is computed by the same `sprintHealth` and
// `graph.Summarize` the single-project routes call, so the headline and the
// panel behind it cannot disagree.
//
// Asserted by comparing the row against `/p/<id>/api/sprint`'s own body rather
// than against numbers typed here — a hand-written expectation would pass
// while both drifted together.
func TestPortfolioRowUsesTheSameNumbersAsTheProject(t *testing.T) {
	st := &fakeState{
		tasks: []state.TaskRuntime{
			{ID: "T-1", Status: "done"},
			{ID: "T-2", Status: "in-progress"},
			{ID: "T-3", Status: "blocked"},
		},
		done7d:     7,
		lastEvents: map[string]state.Event{"T-3": {EventType: "block", TS: "2026-09-11T09:00:00Z", Extra: map[string]any{"reason": "no API key"}}},
	}
	p := newPortfolio(t, PortfolioOptions{
		Projects: []PortfolioProject{{ID: "demo", Server: portfolioServer(t, "demo", ProfileOperator, "", st)}},
	})

	row := getPortfolio(t, p).Projects[0]

	rec := httptest.NewRecorder()
	p.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/p/demo/api/sprint", nil))
	var sprint sprintPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &sprint); err != nil {
		t.Fatalf("decode /p/demo/api/sprint: %v\n%s", err, rec.Body.String())
	}

	if row.VelocityPerDay != sprint.VelocityPerDay {
		t.Errorf("velocity: portfolio %v, project %v", row.VelocityPerDay, sprint.VelocityPerDay)
	}
	if row.Confidence != sprint.Confidence {
		t.Errorf("confidence: portfolio %q, project %q", row.Confidence, sprint.Confidence)
	}
	if row.Blocked != sprint.BlockedCount {
		t.Errorf("blocked: portfolio %d, project %d", row.Blocked, sprint.BlockedCount)
	}
	if len(row.Blockers) == 0 || row.Blockers[0].Reason != "no API key" {
		t.Errorf("blockers = %+v; want the project's own reason", row.Blockers)
	}
	if row.Done != 1 || row.InProgress != 1 || row.Total != 3 {
		t.Errorf("counters = %d done / %d in progress / %d total", row.Done, row.InProgress, row.Total)
	}
}

// A project whose state cannot be read is a greyed-out card, not a 500.
//
// Rule 25 in spirit: the assertion is that the OTHER project is still there
// and still right. "Returns 200" would not distinguish a portfolio that
// degraded from one that happened not to touch the broken project at all.
func TestOneBrokenProjectDoesNotBlankThePage(t *testing.T) {
	broken := &fakeState{tasksErr: errBoom}
	healthy := &fakeState{tasks: []state.TaskRuntime{{ID: "T-1", Status: "done"}}}

	p := newPortfolio(t, PortfolioOptions{
		Projects: []PortfolioProject{
			{ID: "broken", Server: portfolioServer(t, "broken", ProfileOperator, "", broken)},
			{ID: "healthy", Server: portfolioServer(t, "healthy", ProfileOperator, "", healthy)},
		},
	})

	got := getPortfolio(t, p)
	if len(got.Projects) != 2 {
		t.Fatalf("%d rows, want 2 — a broken project must still have a card", len(got.Projects))
	}
	if got.Projects[0].Available {
		t.Error("the broken project reports available")
	}
	if got.Projects[0].Reason == "" {
		t.Error("the broken project gives no reason")
	}
	if !got.Projects[1].Available || got.Projects[1].Done != 1 {
		t.Errorf("the healthy project is wrong: %+v", got.Projects[1])
	}
}

// The counters survive a failure in a LATER read. Losing the summary because
// the event log would not answer throws away the answer to keep the question
// tidy.
func TestPartialReadKeepsTheCounters(t *testing.T) {
	st := &fakeState{
		tasks:     []state.TaskRuntime{{ID: "T-1", Status: "done"}},
		done7dErr: errBoom,
		spendErr:  errBoom,
	}
	p := newPortfolio(t, PortfolioOptions{
		Projects: []PortfolioProject{{ID: "demo", Server: portfolioServer(t, "demo", ProfileOperator, "", st)}},
	})

	row := getPortfolio(t, p).Projects[0]
	if !row.Available {
		t.Fatal("a project whose counters read fine reports unavailable")
	}
	if row.Done != 1 || row.Total != 3 {
		t.Errorf("counters lost: %d/%d", row.Done, row.Total)
	}
	if row.Spend.Available {
		t.Error("spend reports available after a failed read")
	}
	if row.VelocityPerDay != 0 {
		t.Errorf("velocity = %v after a failed read, want 0", row.VelocityPerDay)
	}
}

func TestBlockersAreCappedAtThree(t *testing.T) {
	rows := []blockerRow{{TaskID: "a"}, {TaskID: "b"}, {TaskID: "c"}, {TaskID: "d"}}
	if got := capBlockers(rows); len(got) != portfolioBlockerLimit {
		t.Errorf("capBlockers kept %d, want %d", len(got), portfolioBlockerLimit)
	}
	// nil becomes an empty list, never null: the page ranges over it.
	if got := capBlockers(nil); got == nil || len(got) != 0 {
		t.Errorf("capBlockers(nil) = %#v, want an empty slice", got)
	}
}

func TestUnavailableProjectsAreListedNotDropped(t *testing.T) {
	p := newPortfolio(t, PortfolioOptions{
		Projects:    []PortfolioProject{{ID: "demo", Server: portfolioServer(t, "demo", ProfileOperator, "", &fakeState{})}},
		Unavailable: []UnavailableProject{{Root: "/tmp/junk", Reason: "no tasks.json"}},
	})
	got := getPortfolio(t, p)
	if len(got.Unavailable) != 1 || got.Unavailable[0].Root != "/tmp/junk" {
		t.Fatalf("unavailable = %+v", got.Unavailable)
	}
	if got.Unavailable[0].Reason != "no tasks.json" {
		t.Errorf("reason = %q", got.Unavailable[0].Reason)
	}
}

// ---- delegation ------------------------------------------------------------

// TestEachProjectKeepsItsOwnAccessModel is why the portfolio delegates to each
// project's Server instead of re-serving its routes: the gate that answers is
// that project's, with that project's token.
//
// The operator project answers without one and the stakeholder project does
// not, in the same process, through the same front door.
func TestEachProjectKeepsItsOwnAccessModel(t *testing.T) {
	p := newPortfolio(t, PortfolioOptions{
		Projects: []PortfolioProject{
			{ID: "open", Server: portfolioServer(t, "open", ProfileOperator, "", &fakeState{})},
			{ID: "gated", Server: portfolioServer(t, "gated", ProfileStakeholder, testToken, &fakeState{})},
		},
	})

	cases := []struct {
		name  string
		path  string
		token string
		want  int
	}{
		{"operator project, no token", "/p/open/api/whoami", "", http.StatusOK},
		{"stakeholder project, no token", "/p/gated/api/whoami", "", http.StatusUnauthorized},
		{"stakeholder project, wrong token", "/p/gated/api/whoami", "not-the-token", http.StatusUnauthorized},
		{"stakeholder project, right token", "/p/gated/api/whoami", testToken, http.StatusOK},
		// The token is valid and the route is still not on that project's
		// allow-list: 403, not 200. The portfolio must not widen it.
		{"stakeholder project, route not allowed", "/p/gated/api/metrics", testToken, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			rec := httptest.NewRecorder()
			p.Handler().ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Errorf("%s = %d, want %d (%s)", tc.path, rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestUnknownProjectIs404NotTheSPA(t *testing.T) {
	p := newPortfolio(t, PortfolioOptions{
		Projects: []PortfolioProject{{ID: "demo", Server: portfolioServer(t, "demo", ProfileOperator, "", &fakeState{})}},
	})
	rec := httptest.NewRecorder()
	p.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/p/ghost/api/tasks", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown project = %d, want 404", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "ghost") {
		t.Errorf("the 404 does not name the project: %q", body)
	}
}

// /p/<id> without the trailing slash must reach the project, not the
// portfolio page — a redirect rather than a silent fall-through to "/".
func TestProjectRootRedirectsToItsSlash(t *testing.T) {
	p := newPortfolio(t, PortfolioOptions{
		Projects: []PortfolioProject{{ID: "demo", Server: portfolioServer(t, "demo", ProfileOperator, "", &fakeState{})}},
	})
	rec := httptest.NewRecorder()
	p.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/p/demo", nil))
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("/p/demo = %d, want 301", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/p/demo/" {
		t.Errorf("Location = %q, want /p/demo/", got)
	}
}

// ---- construction ----------------------------------------------------------

func TestNewPortfolioRefusals(t *testing.T) {
	server := func(id string) *Server { return portfolioServer(t, id, ProfileOperator, "", &fakeState{}) }

	cases := []struct {
		name string
		opts PortfolioOptions
	}{
		{
			// The one that matters: a stakeholder portfolio would show every
			// project's counters to a token holder scoped to one of them.
			name: "a stakeholder profile",
			opts: PortfolioOptions{Config: cfg(ProfileStakeholder, testToken), Projects: []PortfolioProject{{ID: "a", Server: server("a")}}},
		},
		{
			name: "both is not operator either",
			opts: PortfolioOptions{Config: cfg(ProfileBoth, testToken), Projects: []PortfolioProject{{ID: "a", Server: server("a")}}},
		},
		{
			// Two directories can resolve to one id, and the second
			// registration would panic in ServeMux or shadow the first.
			name: "a duplicate project id",
			opts: PortfolioOptions{Projects: []PortfolioProject{
				{ID: "a", Server: server("a")}, {ID: "a", Server: server("a2")},
			}},
		},
		{name: "no projects at all", opts: PortfolioOptions{}},
		{name: "a project with no id", opts: PortfolioOptions{Projects: []PortfolioProject{{Server: server("a")}}}},
		{name: "a project with no server", opts: PortfolioOptions{Projects: []PortfolioProject{{ID: "a"}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := tc.opts
			if opts.Config.Profile == "" {
				opts.Config = cfg(ProfileOperator, "")
			}
			opts.Static = spaHandler(t)
			if _, err := NewPortfolio(opts); err == nil {
				t.Error("NewPortfolio accepted it")
			}
		})
	}
}

// ---- the seam with the portfolio page (G8.6) -------------------------------

// TestSingleProjectDashboardRefusesPortfolio is the one finding in this file
// that is not about the portfolio at all: it is about the dashboard that is
// NOT one.
//
// `/api/portfolio` is registered only under `--portfolio`, so without the
// prefix rule it falls through to the SPA at "/" and answers **200 with
// HTML**. Since the rule landed this is one case of it rather than a handler
// of its own — the same 404 any unimplemented data route gets. The page's
// "is this process a portfolio?" check reads a 404; a page of HTML parsed as
// a payload is truthy, so the nav item appears on every single-project
// dashboard and the page then indexes a string. Found by orch-opus reviewing
// G8.5 against the G8.6 client, in a browser neither side owns.
func TestSingleProjectDashboardRefusesPortfolio(t *testing.T) {
	s := portfolioServer(t, "demo", ProfileOperator, "", &fakeState{})

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/portfolio", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("/api/portfolio on a single-project dashboard = %d, want 404 "+
			"(the page's whole gate reads this status)", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q; HTML here is what the bug looked like", ct)
	}
	var body apiNotFoundPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode the refusal: %v\n%s", err, rec.Body.String())
	}
	if body.Error == "" {
		t.Errorf("the refusal says nothing: %+v", body)
	}
	// The path is echoed so a reader of a log or a curl knows which route
	// was missing, not merely that one was.
	if body.Path != "/api/portfolio" {
		t.Errorf("path = %q, want the path that was asked for", body.Path)
	}
}

// And it stays a 404 on a stakeholder dashboard rather than becoming a 401.
//
// This is why the route is ungated — the second exception to "every data route
// goes through `gated`", after the SPA. It carries no data; it is the absence
// of a feature, and 401 for something that does not exist here is the same
// unusable answer as the 200 was.
func TestPortfolioRefusalIsNotGated(t *testing.T) {
	s := portfolioServer(t, "demo", ProfileStakeholder, testToken, &fakeState{})

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/portfolio", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("/api/portfolio with no token = %d, want 404 — the absence of a "+
			"feature is not a thing to authenticate", rec.Code)
	}
}

// ---- degrading really is per section ---------------------------------------

// TestAFailedVelocityReadKeepsTheBlockers is the bug orch-opus found: the
// blocker lookup was nested inside the velocity read's `else`, so one failure
// took both and the row said "N blocked" while listing none.
//
// The assertion is the pair — the count AND the reasons — because the count
// alone is what made the old behaviour look fine.
func TestAFailedVelocityReadKeepsTheBlockers(t *testing.T) {
	st := &fakeState{
		tasks: []state.TaskRuntime{
			{ID: "T-1", Status: "blocked"},
			{ID: "T-3", Status: "blocked"},
		},
		done7dErr: errBoom,
		lastEvents: map[string]state.Event{
			"T-3": {EventType: "block", Extra: map[string]any{"reason": "no API key"}},
		},
	}
	p := newPortfolio(t, PortfolioOptions{
		Projects: []PortfolioProject{{ID: "demo", Server: portfolioServer(t, "demo", ProfileOperator, "", st)}},
	})

	row := getPortfolio(t, p).Projects[0]
	if row.Blocked == 0 {
		t.Fatal("no blocked count, so this test proves nothing")
	}
	if len(row.Blockers) == 0 {
		t.Fatalf("row says %d blocked and lists none — the reasons went out with "+
			"the velocity read", row.Blocked)
	}
	var sawReason bool
	for _, b := range row.Blockers {
		if b.Reason == "no API key" {
			sawReason = true
		}
	}
	if !sawReason {
		t.Errorf("blockers = %+v; want T-3's own reason", row.Blockers)
	}
	// And the failed read still shows as no projection, not as a confident
	// zero: `sprintHealth` renders velocity 0 as confidence "none".
	if row.VelocityPerDay != 0 || row.ETADate != nil {
		t.Errorf("velocity = %v, eta = %v; a failed count must not project",
			row.VelocityPerDay, row.ETADate)
	}
}

// ---- the name is a name ----------------------------------------------------

// project_name was the id a second time. Two keys carrying one value invite
// the page to render the wrong one, and the value they carried was the
// directory's base name rather than anything a human chose.
func TestProjectNameComesFromTasksJSON(t *testing.T) {
	p := newPortfolio(t, PortfolioOptions{
		// testTasksJSON's meta.project is "demo"; the id here is not.
		Projects: []PortfolioProject{{ID: "billing-api", Server: portfolioServer(t, "billing-api", ProfileOperator, "", &fakeState{})}},
	})

	row := getPortfolio(t, p).Projects[0]
	if row.ProjectID != "billing-api" {
		t.Errorf("project_id = %q", row.ProjectID)
	}
	if row.ProjectName != "demo" {
		t.Errorf("project_name = %q, want %q from tasks.json's meta.project",
			row.ProjectName, "demo")
	}
}

// A project whose tasks.json names none falls back to the id, because an
// empty name on a card is worse than a repeated one.
func TestProjectNameFallsBackToTheID(t *testing.T) {
	root := writeProject(t, "spec_root: specs\n",
		`{"tasks":[{"id":"T-1","phase":0,"title":"One","model":"claude/sonnet","status":"todo","dependencies":[],"estimateHours":1.0}]}`)
	s, err := New(cfg(ProfileOperator, ""), Options{
		Static: spaHandler(t),
		State:  &fakeState{},
		Paths:  config.Paths{Root: root, ID: "nameless", ConfigYAML: filepath.Join(root, "config.yaml")},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p := newPortfolio(t, PortfolioOptions{Projects: []PortfolioProject{{ID: "nameless", Server: s}}})

	if got := getPortfolio(t, p).Projects[0].ProjectName; got != "nameless" {
		t.Errorf("project_name = %q, want the id as the fallback", got)
	}
}
