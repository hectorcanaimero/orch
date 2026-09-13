package export

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hectorcanaimero/orch/internal/model"
)

func task(id string, phase int, deps ...string) model.Task {
	return model.Task{ID: id, Phase: phase, Title: "title of " + id, Status: model.StatusTodo, Dependencies: deps}
}

// ---- stages ------------------------------------------------------------

func TestStagesFor(t *testing.T) {
	cases := []struct {
		name  string
		tasks []model.Task
		want  map[string]int
	}{
		{
			name:  "independent tasks share stage 1",
			tasks: []model.Task{task("A", 1), task("B", 1), task("C", 1)},
			want:  map[string]int{"A": 1, "B": 1, "C": 1},
		},
		{
			name:  "a chain climbs one stage per link",
			tasks: []model.Task{task("D", 1, "C"), task("C", 1, "B"), task("B", 1, "A"), task("A", 1)},
			want:  map[string]int{"A": 1, "B": 2, "C": 3, "D": 4},
		},
		{
			// The longest path decides, not the first dependency listed.
			name: "a diamond takes the longest path",
			tasks: []model.Task{
				task("A", 1), task("B", 1, "A"), task("C", 1, "A"),
				task("D", 1, "B", "C"), task("E", 1, "A", "D"),
			},
			want: map[string]int{"A": 1, "B": 2, "C": 2, "D": 3, "E": 4},
		},
		{
			// X is not in the set (another phase, done, filtered out or
			// unknown): nothing under this parent waits for it.
			name:  "a dependency outside the set does not raise the stage",
			tasks: []model.Task{task("A", 1, "X"), task("B", 1, "A", "Y")},
			want:  map[string]int{"A": 1, "B": 2},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stagesFor(tc.tasks); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("stagesFor = %v, want %v", got, tc.want)
			}
		})
	}
}

// ---- planning ----------------------------------------------------------

func TestPlanMulticaGroupsByPhaseOrdersByStageAndSkipsDone(t *testing.T) {
	done := task("F1.T0", 1)
	done.Status = model.StatusDone
	tasks := []model.Task{
		task("F2.T1", 2, "F1.T2"), // cross-phase dep: stage 1 in its own parent
		task("F1.T2", 1, "F1.T1", "F1.T0"),
		done,
		task("F1.T1", 1),
		task("F1.T3", 1),
	}
	plan, err := PlanMultica("proj", "specs", tasks, Selection{}, map[int]string{1: "Auth core"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan.SkippedDone, []string{"F1.T0"}) {
		t.Errorf("SkippedDone = %v, want [F1.T0]", plan.SkippedDone)
	}
	var got []string
	for _, ph := range plan.Phases {
		got = append(got, "parent "+ph.Title)
		for _, tk := range ph.Tasks {
			got = append(got, fmt.Sprintf("%s@%d", tk.Task.ID, tk.Stage))
		}
	}
	// F1.T2 depends on F1.T1 (stage 1) and on a DONE task, which does not
	// count, so it is stage 2 and comes after both stage-1 tasks; within a
	// stage, declaration order. Phase 2 has no title, so it is just "F2".
	want := []string{"parent F1 — Auth core", "F1.T1@1", "F1.T3@1", "F1.T2@2", "parent F2", "F2.T1@1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("plan =\n  %v\nwant\n  %v", got, want)
	}
}

func TestPlanMulticaSelection(t *testing.T) {
	tasks := []model.Task{task("F1.T1", 1), task("F1.T2", 1, "F1.T1"), task("F2.T1", 2), task("F3.T1", 3)}
	ids := func(p MulticaPlan) []string {
		var out []string
		for _, ph := range p.Phases {
			for _, tk := range ph.Tasks {
				out = append(out, tk.Task.ID)
			}
		}
		return out
	}

	plan, err := PlanMultica("proj", "", tasks, Selection{Phases: []int{1, 3}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(plan); !reflect.DeepEqual(got, []string{"F1.T1", "F1.T2", "F3.T1"}) {
		t.Errorf("--phase 1,3 kept %v", got)
	}

	plan, err = PlanMultica("proj", "", tasks, Selection{Only: "F1.T2"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(plan); !reflect.DeepEqual(got, []string{"F1.T2"}) {
		t.Errorf("--only F1.T2 kept %v", got)
	}
	// Its dependency was filtered out, so it is not waiting on anything in
	// this parent: stage 1.
	if s := plan.Phases[0].Tasks[0].Stage; s != 1 {
		t.Errorf("F1.T2 alone got stage %d, want 1", s)
	}

	plan, err = PlanMultica("proj", "", tasks, Selection{Only: "["}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Empty() {
		t.Errorf("a malformed glob should match nothing, kept %v", ids(plan))
	}
}

func TestPlanMulticaRefusesCycles(t *testing.T) {
	_, err := PlanMultica("proj", "", []model.Task{task("A", 1, "B"), task("B", 1, "A")}, Selection{}, nil)
	if err == nil || !strings.Contains(err.Error(), "cycle") || !strings.Contains(err.Error(), "orch validate") {
		t.Fatalf("PlanMultica on a cycle = %v, want an error naming the cycle and orch validate", err)
	}
}

// ---- descriptions ------------------------------------------------------

func TestTaskDescriptionCarriesWhatMulticaHasNoFieldFor(t *testing.T) {
	tk := task("F1.1.T2", 1, "F1.1.T1", "F0.T9")
	tk.Description = "Build the session endpoint.\n\nDone when: tests pass."
	tk.SpecRef = "f1-auth.md#F1.1.T2"
	tk.Model = "claude/claude-sonnet-4-6"
	tk.EstimateHours = 1.5
	plan := MulticaPlan{ProjectID: "proj", SpecRoot: "specs", Keys: map[string]string{"F1.1.T1": "MUL-7"}}

	got := plan.TaskDescription(MulticaTask{Task: tk, Stage: 2})
	for _, want := range []string{
		"Build the session endpoint.\n\nDone when: tests pass.\n\n---\n",
		"Depends on: F1.1.T1 (MUL-7), F0.T9 (not in Multica yet)\n",
		"Spec: `specs/f1-auth.md#F1.1.T2`\n",
		"Model: `claude/claude-sonnet-4-6`\n",
		"Estimate: 1.5h\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("description lacks %q:\n%s", want, got)
		}
	}
	// The marker is the LAST line, alone, so Resolve's regex finds it.
	if !strings.HasSuffix(got, "\norch-task: proj/F1.1.T2\n") {
		t.Errorf("description does not end with its marker line:\n%s", got)
	}
}

// ---- applying, with an in-process fake ---------------------------------

// fakeMultica is a stateful stand-in for the CLI: issues it creates show up
// in the next list, which is what an idempotency test needs and a static
// fixture cannot give.
type fakeMultica struct {
	calls  [][]string
	stdins []string
	issues []map[string]string
	failOn string // fail a create whose title contains this
}

func (f *fakeMultica) run(_ context.Context, stdin string, args ...string) (string, error) {
	f.calls = append(f.calls, args)
	f.stdins = append(f.stdins, stdin)
	switch strings.Join(args[:2], " ") {
	case "issue list":
		page := map[string]any{"issues": f.issues, "has_more": false}
		b, _ := json.Marshal(page)
		return string(b), nil
	case "issue create":
		title := flagValue(args, "--title")
		if f.failOn != "" && strings.Contains(title, f.failOn) {
			return "", &MulticaError{Reason: reasonExit, Args: args, Stderr: "server said no"}
		}
		n := len(f.issues) + 1
		is := map[string]string{"id": fmt.Sprintf("uuid-%d", n), "identifier": fmt.Sprintf("MUL-%d", n), "description": stdin}
		f.issues = append(f.issues, is)
		b, _ := json.Marshal(is)
		return string(b), nil
	}
	return "", fmt.Errorf("unexpected %v", args)
}

func (f *fakeMultica) creates() [][]string {
	var out [][]string
	for _, c := range f.calls {
		if c[1] == "create" {
			out = append(out, c)
		}
	}
	return out
}

func flagValue(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func diamondPlan(t *testing.T) MulticaPlan {
	t.Helper()
	tasks := []model.Task{task("F1.T1", 1), task("F1.T2", 1, "F1.T1"), task("F1.T3", 1, "F1.T1"), task("F1.T4", 1, "F1.T2", "F1.T3"), task("F2.T1", 2, "F1.T4")}
	plan, err := PlanMultica("proj", "specs", tasks, Selection{}, map[int]string{1: "Core"})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestApplyCreatesParentsBeforeChildrenWithTheirIDsAndStages(t *testing.T) {
	fake := &fakeMultica{}
	dest := Multica{Project: "PRJ-1", Run: fake.run}
	plan := diamondPlan(t)
	if err := dest.Resolve(context.Background(), &plan); err != nil {
		t.Fatal(err)
	}
	var reported []string
	if err := dest.Apply(context.Background(), &plan, func(c Created) { reported = append(reported, c.Ref.Key) }); err != nil {
		t.Fatal(err)
	}

	type call struct{ title, parent, stage string }
	var got []call
	for _, c := range fake.creates() {
		if s := flagValue(c, "--status"); s != "backlog" {
			t.Errorf("%q created with status %q, want backlog", flagValue(c, "--title"), s)
		}
		if p := flagValue(c, "--project"); p != "PRJ-1" {
			t.Errorf("%q created in project %q, want PRJ-1", flagValue(c, "--title"), p)
		}
		got = append(got, call{flagValue(c, "--title"), flagValue(c, "--parent"), flagValue(c, "--stage")})
	}
	want := []call{
		{"F1 — Core", "", ""},
		{"F1.T1 — title of F1.T1", "uuid-1", "1"},
		{"F1.T2 — title of F1.T2", "uuid-1", "2"},
		{"F1.T3 — title of F1.T3", "uuid-1", "2"},
		{"F1.T4 — title of F1.T4", "uuid-1", "3"},
		{"F2", "", ""},
		{"F2.T1 — title of F2.T1", "uuid-6", "1"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("create calls =\n  %v\nwant\n  %v", got, want)
	}
	if len(reported) != len(want) {
		t.Errorf("onCreate reported %d issues, want %d", len(reported), len(want))
	}

	// A dependency created earlier in the run is named by its new key, and a
	// cross-phase one too, because phase 1 finished before phase 2 began.
	f4 := fake.issues[4]["description"]
	if !strings.Contains(f4, "Depends on: F1.T2 (MUL-3), F1.T3 (MUL-4)") {
		t.Errorf("F1.T4's description does not name its dependencies' keys:\n%s", f4)
	}
	if f21 := fake.issues[6]["description"]; !strings.Contains(f21, "Depends on: F1.T4 (MUL-5)") {
		t.Errorf("F2.T1's description does not name its cross-phase dependency's key:\n%s", f21)
	}
}

func TestSecondExportCreatesNothing(t *testing.T) {
	fake := &fakeMultica{}
	dest := Multica{Run: fake.run}
	first := diamondPlan(t)
	if err := dest.Resolve(context.Background(), &first); err != nil {
		t.Fatal(err)
	}
	if err := dest.Apply(context.Background(), &first, func(Created) {}); err != nil {
		t.Fatal(err)
	}
	before := len(fake.creates())

	second := diamondPlan(t)
	if err := dest.Resolve(context.Background(), &second); err != nil {
		t.Fatal(err)
	}
	for _, ph := range second.Phases {
		if ph.Existing == nil {
			t.Errorf("phase F%d not recognised as existing", ph.Number)
		}
		for _, tk := range ph.Tasks {
			if tk.Existing == nil {
				t.Errorf("%s not recognised as existing", tk.Task.ID)
			}
		}
	}
	if err := dest.Apply(context.Background(), &second, func(c Created) { t.Errorf("re-run created %s", c.Title) }); err != nil {
		t.Fatal(err)
	}
	if after := len(fake.creates()); after != before {
		t.Errorf("re-run made %d create calls", after-before)
	}
}

// Two orch projects exporting into one Multica workspace both have an F1.T1.
// The second export must create its own issues, not take the first project's
// for its own and skip everything.
func TestAnotherProjectsIssuesAreNotThisProjects(t *testing.T) {
	fake := &fakeMultica{}
	dest := Multica{Run: fake.run}
	tasks := []model.Task{task("F1.T1", 1), task("F1.T2", 1, "F1.T1")}
	for _, projectID := range []string{"shop", "blog"} {
		plan, err := PlanMultica(projectID, "", tasks, Selection{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := dest.Resolve(context.Background(), &plan); err != nil {
			t.Fatal(err)
		}
		if err := dest.Apply(context.Background(), &plan, func(Created) {}); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(fake.issues); n != 6 {
		t.Errorf("two projects exported %d issues into the shared workspace, want 6 (3 each)", n)
	}
	// And blog's F1.T2 names blog's F1.T1, not shop's.
	if d := fake.issues[5]["description"]; !strings.Contains(d, "F1.T1 (MUL-5)") {
		t.Errorf("blog's F1.T2 points at the wrong F1.T1:\n%s", d)
	}
}

// A task done in orch is left out of the export, but if an earlier run
// created its issue, what depends on it still names that issue's key.
func TestADoneDependencyExportedEarlierIsNamedByItsKey(t *testing.T) {
	fake := &fakeMultica{}
	dest := Multica{Run: fake.run}
	first, err := PlanMultica("proj", "", []model.Task{task("F1.T1", 1)}, Selection{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := dest.Resolve(context.Background(), &first); err != nil {
		t.Fatal(err)
	}
	if err := dest.Apply(context.Background(), &first, func(Created) {}); err != nil {
		t.Fatal(err)
	}

	doneT1 := task("F1.T1", 1)
	doneT1.Status = model.StatusDone
	second, err := PlanMultica("proj", "", []model.Task{doneT1, task("F1.T2", 1, "F1.T1")}, Selection{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := dest.Resolve(context.Background(), &second); err != nil {
		t.Fatal(err)
	}
	if err := dest.Apply(context.Background(), &second, func(Created) {}); err != nil {
		t.Fatal(err)
	}
	if d := fake.issues[len(fake.issues)-1]["description"]; !strings.Contains(d, "Depends on: F1.T1 (MUL-2)") {
		t.Errorf("F1.T2 does not name its done, already-exported dependency by key:\n%s", d)
	}
}

func TestApplyStopsAtTheFirstFailureAndARerunContinues(t *testing.T) {
	fake := &fakeMultica{failOn: "F1.T3"}
	dest := Multica{Run: fake.run}
	plan := diamondPlan(t)
	if err := dest.Resolve(context.Background(), &plan); err != nil {
		t.Fatal(err)
	}
	err := dest.Apply(context.Background(), &plan, func(Created) {})
	if err == nil || !strings.Contains(err.Error(), "F1.T3") || !strings.Contains(err.Error(), "server said no") {
		t.Fatalf("Apply = %v, want the failing task and Multica's message", err)
	}
	if n := len(fake.issues); n != 3 { // parent, F1.T1, F1.T2
		t.Fatalf("created %d issues before the failure, want 3", n)
	}

	fake.failOn = ""
	rerun := diamondPlan(t)
	if err := dest.Resolve(context.Background(), &rerun); err != nil {
		t.Fatal(err)
	}
	if err := dest.Apply(context.Background(), &rerun, func(Created) {}); err != nil {
		t.Fatal(err)
	}
	if n := len(fake.issues); n != 7 {
		t.Errorf("after the re-run there are %d issues, want 7 (no duplicates)", n)
	}
}

func TestResolveFollowsPages(t *testing.T) {
	pages := []string{
		`{"issues":[{"id":"a","identifier":"MUL-1","description":"orch-task: proj/F1.T1"}],"has_more":true}`,
		`{"issues":[{"id":"b","identifier":"MUL-2","description":"orch-phase: proj/1"}],"has_more":false}`,
	}
	var offsets []string
	run := func(_ context.Context, _ string, args ...string) (string, error) {
		offsets = append(offsets, flagValue(args, "--offset"))
		return pages[len(offsets)-1], nil
	}
	plan := diamondPlan(t)
	if err := (Multica{Run: run}).Resolve(context.Background(), &plan); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(offsets, []string{"0", "1"}) {
		t.Errorf("listed at offsets %v, want [0 1]", offsets)
	}
	if plan.Phases[0].Existing == nil || plan.Phases[0].Existing.Key != "MUL-2" {
		t.Error("the parent on the second page was not found")
	}
	if plan.Phases[0].Tasks[0].Existing == nil {
		t.Error("the task on the first page was not found")
	}
}

// ---- the real exec path, against a fake binary -------------------------

func withFakeMultica(t *testing.T) (logPath string) {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("testdata", "fakebin"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	logPath = filepath.Join(t.TempDir(), "multica.log")
	t.Setenv("FAKE_MULTICA_LOG", logPath)
	t.Setenv("FAKE_MULTICA_COUNTER", filepath.Join(t.TempDir(), "counter"))
	return logPath
}

func TestExecRunnerPassesArgvAndStdinThrough(t *testing.T) {
	logPath := withFakeMultica(t)
	dest := Multica{Run: NewExecRunner(t.TempDir())}
	plan := diamondPlan(t)
	if err := dest.Resolve(context.Background(), &plan); err != nil {
		t.Fatal(err)
	}
	if err := dest.Apply(context.Background(), &plan, func(Created) {}); err != nil {
		t.Fatal(err)
	}
	log, err := os.ReadFile(logPath) // #nosec G304 -- t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"argv: [issue] [list] [--output] [json] [--limit] [100] [--offset] [0] [--fields] [id,identifier,description]\n",
		// A title with spaces and an em dash arrives as ONE argument.
		"argv: [issue] [create] [--title] [F1.T4 — title of F1.T4] [--description-stdin] [--status] [backlog] [--output] [json] [--parent] [uuid-1] [--stage] [3]\n",
		"orch-task: proj/F1.T4",
	} {
		if !strings.Contains(string(log), want) {
			t.Errorf("fake multica log lacks %q:\n%s", want, log)
		}
	}
}

func TestMissingCLIIsATypedErrorWithTheWayOut(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	plan := diamondPlan(t)
	err := (Multica{Run: NewExecRunner("")}).Resolve(context.Background(), &plan)
	if !IsNotFound(err) {
		t.Fatalf("Resolve without multica = %v, want a not-found MulticaError", err)
	}
	for _, want := range []string{"not on PATH", "multica login", "workspace_id"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}

func TestNonZeroExitSurfacesStderr(t *testing.T) {
	withFakeMultica(t)
	t.Setenv("FAKE_MULTICA_EXIT", "1")
	t.Setenv("FAKE_MULTICA_STDERR", "workspace_id is required: use --workspace-id flag")
	plan := diamondPlan(t)
	err := (Multica{Run: NewExecRunner("")}).Resolve(context.Background(), &plan)
	var me *MulticaError
	if !errors.As(err, &me) || IsNotFound(err) {
		t.Fatalf("Resolve = %v, want an exit MulticaError", err)
	}
	if !strings.Contains(err.Error(), "workspace_id is required") || !strings.Contains(err.Error(), "multica issue list") {
		t.Errorf("error %q does not carry the command and Multica's own message", err)
	}
}

// ---- phase titles ------------------------------------------------------

func TestPhaseTitlesReadTheSpecHeaders(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "specs"), 0o750); err != nil {
		t.Fatal(err)
	}
	write := func(rel, body string) {
		if err := os.WriteFile(filepath.Join(root, rel), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("specs/f1.md", "---\ntype: spec\n---\n\n# F1 — Auth core\n\n## F1.1 — Package: x\n")
	write("specs/f2.md", "# F2 - Payments\r\n")

	a := task("F1.1.T1", 1)
	a.SpecRef = "f1.md#F1.1.T1"
	b := task("F2.T1", 2)
	b.SpecRef = "specs/f2.md#T1" // the pre-bug-12 form, prefix included
	c := task("F3.T1", 3)
	c.SpecRef = "missing.md#T1"

	got, err := PhaseTitles(root, "specs", []model.Task{a, b, c, task("F4.T1", 4)})
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]string{1: "Auth core", 2: "Payments"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("PhaseTitles = %v, want %v", got, want)
	}
}
