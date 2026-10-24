package receipt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/pricing"
	"github.com/hectorcanaimero/orch/internal/state"
)

// summaryRun is #456's scenario, written the way the engine writes it: A is
// done with a PR whose CI passed; B passed its agent twice and was rejected
// by the local verify both times (verify_failed, then the terminal fail with
// no failure class); C hit the provider's rate limit, was retried, and
// blocked; D's agent blocked it and exited cleanly, so its outcome event says
// success and only the task's status and note say why.
func summaryRun() *Receipt {
	ev := func(typ, task, ts string, extra map[string]any) state.Event {
		return state.Event{RunID: "r", EventType: typ, TaskID: task, Backend: "claude", TS: "2026-09-10T" + ts + "Z", Extra: extra}
	}
	verify := "The task's app did not start. orch's local verify ran `npm start` in your working dir and it failed (exit status 1):"
	events := []state.Event{
		ev("dispatch", "A", "10:00:00", nil),
		ev("dispatch", "B", "10:00:00", nil),
		ev("dispatch", "C", "10:00:00", nil),
		ev("dispatch", "D", "10:00:00", nil),
		ev("fail", "C", "10:05:00", map[string]any{"failure_class": "rate_limit", "reason": "429 Too Many Requests"}),
		ev("retry", "C", "10:06:00", map[string]any{"failure_class": "rate_limit"}),
		ev("success", "B", "10:15:00", nil),
		ev("verify_failed", "B", "10:16:00", map[string]any{"reason": verify}),
		ev("dispatch", "B", "10:17:00", nil),
		ev("dispatch", "C", "10:10:00", nil),
		ev("fail", "C", "10:12:00", map[string]any{"failure_class": "rate_limit", "reason": "429 Too Many Requests"}),
		ev("success", "A", "10:20:00", nil),
		ev("pr_created", "A", "10:21:00", map[string]any{"pr_url": "https://github.com/x/y/pull/7"}),
		ev("success", "D", "10:25:00", nil),
		ev("success", "B", "10:30:00", nil),
		ev("fail", "B", "10:31:00", map[string]any{"failure_class": "", "reason": "local verify still failing after the agent's retries: " + verify}),
		ev("sprint_done", "", "11:00:00", nil),
	}
	note, _ := json.Marshal(map[string]string{"author": "claude", "body": "needs a Stripe test key in .env"})
	tasks := []state.TaskRuntime{
		{ID: "A", Status: model.StatusDone, CIStatus: "success", PRURL: "https://github.com/x/y/pull/7"},
		{ID: "B", Status: model.StatusBlocked, UpdatedAt: "2026-09-10T10:31:00Z"},
		{ID: "C", Status: model.StatusBlocked, UpdatedAt: "2026-09-10T10:12:00Z"},
		{ID: "D", Status: model.StatusBlocked, UpdatedAt: "2026-09-10T10:25:00Z", Comments: []json.RawMessage{note}},
	}
	titles := map[string]string{"A": "Login form", "B": "Checkout page", "C": "Search index", "D": "Payments webhook"}
	return Build(events, nil, tasks, titles, pricing.Table{}, "")
}

func TestSummaryGolden(t *testing.T) {
	r := summaryRun()
	if r == nil {
		t.Fatal("no receipt")
	}
	for _, lang := range []string{"en", "es", "pt"} {
		got := r.Summary(lang, false).Text()
		path := filepath.Join("testdata", "summary-"+lang+".txt")
		if *update {
			if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(path) // #nosec G304 -- fixed test path
		if err != nil {
			t.Fatalf("read golden (go test ./internal/receipt -run TestSummaryGolden -update): %v", err)
		}
		if got != string(want) {
			t.Errorf("%s summary differs from %s:\n--- got\n%s--- want\n%s", lang, path, got, want)
		}
	}
}

// The facts the golden pins, asserted by name so a reworded golden cannot
// quietly drop one: each blocked task names its cause and gets the command
// that retries it.
func TestSummaryNamesEachCause(t *testing.T) {
	r := summaryRun()
	if len(r.Done) != 1 || len(r.Blocked) != 3 {
		t.Fatalf("done=%+v blocked=%+v, want A done and B, C, D blocked", r.Done, r.Blocked)
	}
	causes := map[string]string{}
	for _, b := range r.Blocked {
		causes[b.TaskID] = b.Cause + "/" + b.FailureClass
	}
	if causes["B"] != "verify/" || causes["C"] != "failure/rate_limit" || causes["D"] != "agent/" {
		t.Errorf("causes = %v", causes)
	}
	s := r.Summary("en", false)
	if len(s.Actions) != 3 {
		t.Fatalf("actions = %+v, want 3", s.Actions)
	}
	for i, id := range []string{"C", "B", "D"} {
		if want := "orch task set --id " + id + " --status todo && orch run"; s.Actions[i].Command != want {
			t.Errorf("action %d command = %q, want %q", i, s.Actions[i].Command, want)
		}
	}
	if !strings.Contains(s.Actions[1].Text, "verify.app.start") {
		t.Errorf("the verify action should name verify.app.start: %q", s.Actions[1].Text)
	}
}

// With nothing blocked, a PR whose CI passed is the next thing to do — unless
// the project merges on its own.
func TestSummaryMergeAction(t *testing.T) {
	r := &Receipt{RunID: "r", Finished: true, Done: []TaskRef{{TaskID: "A", Title: "Alpha"}}, Blocked: []TaskRef{},
		PRs: []PR{{TaskID: "A", URL: "https://github.com/x/y/pull/7", CIStatus: "success"}}}
	if a := r.Summary("en", false).Actions; len(a) != 1 || !strings.Contains(a[0].Text, "#7") {
		t.Errorf("actions = %+v, want merge #7", a)
	}
	if a := r.Summary("en", true).Actions; len(a) != 0 {
		t.Errorf("auto-merge on: actions = %+v, want none", a)
	}
	if !strings.Contains(r.Summary("xx", true).Text(), "Nothing is waiting on you") {
		t.Error("an unknown language should fall back to English")
	}
}

// #463: a review's rejection is summed up by its structured findings, read
// back from the event as the database returns them, not by the reason's first
// line, which only says a review asked for changes.
func TestSummaryNamesTheReviewFindings(t *testing.T) {
	reason := "orch's local verify had your work reviewed and the review asks for changes. Fix the blocking findings:\n\n## orch review (gemini)"
	var findings []any
	if err := json.Unmarshal([]byte(`[{"severity":"blocking","file":"app.go","line":3,"title":"B is never tested"},{"severity":"blocking","title":"Missing changelog"}]`), &findings); err != nil {
		t.Fatal(err)
	}
	ev := func(typ, ts string, extra map[string]any) state.Event {
		return state.Event{RunID: "r", EventType: typ, TaskID: "B", Backend: "claude", TS: "2026-09-10T" + ts + "Z", Extra: extra}
	}
	r := Build([]state.Event{
		ev("dispatch", "10:00:00", nil),
		ev("success", "10:10:00", nil),
		ev("verify_failed", "10:11:00", map[string]any{"reason": reason, "sent_back": false, "findings": findings}),
		ev("fail", "10:11:00", map[string]any{"reason": "local verify still failing after the agent's retries: " + reason}),
	}, nil, []state.TaskRuntime{{ID: "B", Status: model.StatusBlocked}}, nil, pricing.Table{}, "r")
	got := r.Summary("en", false).Text()
	if want := "2 review findings: app.go:3 B is never tested; Missing changelog"; !strings.Contains(got, want) {
		t.Errorf("summary = %q, want it to contain %q", got, want)
	}
}

// A reason cut with "…" is not followed by the sentence's own ".".
func TestSummaryCutReasonEndsOnce(t *testing.T) {
	r := &Receipt{RunID: "r", Finished: true, Done: []TaskRef{}, Blocked: []TaskRef{{TaskID: "B", Cause: "verify", Reason: strings.Repeat("long reason ", 30)}}}
	for _, lang := range []string{"en", "es", "pt"} {
		line := r.Summary(lang, false).Lines[1]
		if !strings.HasSuffix(line, "…") || strings.HasSuffix(line, "….") {
			t.Errorf("%s: %q, want it to end on the cut's …", lang, line)
		}
	}
}
