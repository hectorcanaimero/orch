package graph

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hectorcanaimero/orch/internal/model"
	"gopkg.in/yaml.v3"
)

const parityProject = "testdata/parity-project"

func loadParity(t *testing.T) ([]model.Task, []string) {
	t.Helper()
	f, err := model.LoadTasksFile(filepath.Join(parityProject, "tasks.json"))
	if err != nil {
		t.Fatalf("load the parity project: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(parityProject, "model_router.yaml")) // #nosec G304 -- fixed testdata path
	if err != nil {
		t.Fatalf("read the router: %v", err)
	}
	var router map[string]any
	if err := yaml.Unmarshal(raw, &router); err != nil {
		t.Fatalf("parse the router: %v", err)
	}
	routes := make([]string, 0, len(router))
	for k := range router {
		routes = append(routes, k)
	}
	return f.Tasks, routes
}

// ---------------------------------------------------------------------------
// Parity with Python
// ---------------------------------------------------------------------------

// The expectations are the output of `preflight.validate_graph` on this exact
// fixture, captured by testdata/make-goldens.py. `orch validate` prints these
// messages and `scripts/parity.sh` diffs them between the two binaries, so the
// text is a contract — including the quoting style, which is why `pyQuote`
// exists.
func TestValidateMatchesThePythonGolden(t *testing.T) {
	tasks, routes := loadParity(t)

	raw, err := os.ReadFile("testdata/validate.golden.json") // #nosec G304 -- fixed testdata path
	if err != nil {
		t.Fatalf("read the golden: %v", err)
	}
	var want []struct {
		TaskID      *string `json:"task_id"`
		Field       string  `json:"field"`
		Kind        string  `json:"kind"`
		Message     string  `json:"message"`
		Remediation *string `json:"remediation"`
		Severity    string  `json:"severity"`
	}
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatalf("parse the golden: %v", err)
	}

	got := Validate(tasks, routes)
	if len(got) != len(want) {
		t.Fatalf("got %d problems, Python found %d:\ngot:  %s\nwant: %s",
			len(got), len(want), render(got), raw)
	}
	for i, w := range want {
		g := got[i]
		if g.Kind != w.Kind {
			t.Errorf("problem %d: kind = %q, Python says %q", i, g.Kind, w.Kind)
		}
		if g.Message != w.Message {
			t.Errorf("problem %d: message =\n  %q\nPython says\n  %q", i, g.Message, w.Message)
		}
		if g.Field != w.Field {
			t.Errorf("problem %d: field = %q, Python says %q", i, g.Field, w.Field)
		}
		if g.TaskID != deref(w.TaskID) {
			t.Errorf("problem %d: task_id = %q, Python says %q", i, g.TaskID, deref(w.TaskID))
		}
		if g.Remediation != deref(w.Remediation) {
			t.Errorf("problem %d: remediation = %q, Python says %q",
				i, g.Remediation, deref(w.Remediation))
		}
		if string(g.Severity) != w.Severity {
			t.Errorf("problem %d: severity = %q, Python says %q", i, g.Severity, w.Severity)
		}
	}
}

// The cycle Python reports is `CYC.A -> CYC.C -> CYC.B -> CYC.A`, not any of
// the other two rotations or the reverse. Which one gets printed depends on
// the DFS entry point and the canonicalisation, so this pins the port of both.
func TestFindCyclesMatchesThePythonGolden(t *testing.T) {
	tasks, _ := loadParity(t)

	raw, err := os.ReadFile("testdata/cycles.golden.json") // #nosec G304 -- fixed testdata path
	if err != nil {
		t.Fatalf("read the golden: %v", err)
	}
	var want [][]string
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatalf("parse the golden: %v", err)
	}

	got := FindCycles(tasks)
	if len(got) != len(want) {
		t.Fatalf("found %d cycles, Python found %d: %v vs %v", len(got), len(want), got, want)
	}
	for i := range want {
		if strings.Join(got[i], ",") != strings.Join(want[i], ",") {
			t.Errorf("cycle %d:\n  got    %v\n  Python %v", i, got[i], want[i])
		}
	}
}

func TestDisplayOrderMatchesThePythonGolden(t *testing.T) {
	tasks, _ := loadParity(t)

	raw, err := os.ReadFile("testdata/display-order.golden.json") // #nosec G304 -- fixed testdata path
	if err != nil {
		t.Fatalf("read the golden: %v", err)
	}
	var want []string
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatalf("parse the golden: %v", err)
	}

	got := DisplayOrder(tasks)
	ids := make([]string, 0, len(got))
	for _, t := range got {
		ids = append(ids, t.ID)
	}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Errorf("display order:\n  got    %v\n  Python %v", ids, want)
	}
}

// ---------------------------------------------------------------------------
// Validate, case by case
// ---------------------------------------------------------------------------

func TestValidateCases(t *testing.T) {
	cases := []struct {
		name      string
		tasks     []model.Task
		routes    []string
		wantKinds []string
	}{
		{
			name: "a clean graph has nothing to say",
			tasks: []model.Task{
				{ID: "A", Phase: 0, Model: "m"},
				{ID: "B", Phase: 1, Model: "m", Dependencies: []string{"A"}},
			},
			routes: []string{"m"},
		},
		{
			name:      "a task with no id",
			tasks:     []model.Task{{Phase: 0, Model: "m"}},
			routes:    []string{"m"},
			wantKinds: []string{KindSchemaTasks},
		},
		{
			// The missing id short-circuits the rest of the SCHEMA checks —
			// the phase and model findings are suppressed — but
			// validateDependencies is a separate pass and still runs. Verified
			// against Python, which reports exactly these two for this task.
			// (I expected one and was wrong; the code was right.)
			name: "a missing id suppresses later schema checks, not later passes",
			tasks: []model.Task{
				{Phase: -3, Dependencies: []string{"ghost"}},
			},
			routes:    []string{"m"},
			wantKinds: []string{KindSchemaTasks, KindDepMissing},
		},
		{
			name:      "a negative phase",
			tasks:     []model.Task{{ID: "A", Phase: -1, Model: "m"}},
			routes:    []string{"m"},
			wantKinds: []string{KindSchemaTasks},
		},
		{
			name:      "an empty model",
			tasks:     []model.Task{{ID: "A", Phase: 0}},
			routes:    []string{"m"},
			wantKinds: []string{KindSchemaTasks},
		},
		{
			// An empty model is one mistake. Reporting it as both a schema
			// error and an unroutable model would be two findings for it.
			name:      "an empty model is not also reported as unroutable",
			tasks:     []model.Task{{ID: "A", Phase: 0}},
			routes:    []string{"m"},
			wantKinds: []string{KindSchemaTasks},
		},
		{
			name: "a dependency that does not exist",
			tasks: []model.Task{
				{ID: "A", Phase: 0, Model: "m", Dependencies: []string{"ghost"}},
			},
			routes:    []string{"m"},
			wantKinds: []string{KindDepMissing},
		},
		{
			name: "a self-dependency is a cycle, not a missing dep",
			tasks: []model.Task{
				{ID: "A", Phase: 0, Model: "m", Dependencies: []string{"A"}},
			},
			routes:    []string{"m"},
			wantKinds: []string{KindDepCycle},
		},
		{
			name: "an unrouted model",
			tasks: []model.Task{
				{ID: "A", Phase: 0, Model: "nope"},
			},
			routes:    []string{"m"},
			wantKinds: []string{KindRouteUnresolved},
		},
		{
			// nil routes means "the router has not been loaded", and must
			// not be read as "the router is empty, so nothing resolves".
			name: "nil routes skips route checking entirely",
			tasks: []model.Task{
				{ID: "A", Phase: 0, Model: "anything-at-all"},
			},
			routes: nil,
		},
		{
			name: "an empty route set reports every model",
			tasks: []model.Task{
				{ID: "A", Phase: 0, Model: "m"},
			},
			routes:    []string{},
			wantKinds: []string{KindRouteUnresolved},
		},
		{
			name: "a two-task cycle",
			tasks: []model.Task{
				{ID: "A", Phase: 0, Model: "m", Dependencies: []string{"B"}},
				{ID: "B", Phase: 0, Model: "m", Dependencies: []string{"A"}},
			},
			routes:    []string{"m"},
			wantKinds: []string{KindDepCycle},
		},
		{
			name:   "no tasks at all is valid",
			tasks:  nil,
			routes: []string{"m"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Validate(c.tasks, c.routes)
			kinds := make([]string, 0, len(got))
			for _, p := range got {
				kinds = append(kinds, p.Kind)
			}
			if strings.Join(kinds, ",") != strings.Join(c.wantKinds, ",") {
				t.Errorf("kinds = %v, want %v\n%s", kinds, c.wantKinds, render(got))
			}
		})
	}
}

// The quoting is why `orch validate`'s output can be diffed against Python's
// at all — Go's %q would write double quotes on every line.
func TestMessagesUsePythonQuoting(t *testing.T) {
	got := Validate([]model.Task{
		{ID: "A", Phase: 0, Model: "m", Dependencies: []string{"ghost"}},
	}, []string{"m"})
	if len(got) != 1 {
		t.Fatalf("got %d problems, want 1", len(got))
	}
	if got[0].Message != "depends on unknown task 'ghost'" {
		t.Errorf("message = %q, want Python's single-quoted form", got[0].Message)
	}
}

func TestPyQuote(t *testing.T) {
	cases := []struct{ in, want string }{
		{"F0.T1", "'F0.T1'"},
		{"claude/claude-sonnet-4-6", "'claude/claude-sonnet-4-6'"},
		{"", "''"},
		// Python's repr switches to double quotes only when the value has a
		// single quote and no double quote.
		{"it's", `"it's"`},
		{`say "hi"`, `'say "hi"'`},
		{`both ' and "`, `'both \' and "'`},
		{`back\slash`, `'back\\slash'`},
		{"line\nbreak", `'line\nbreak'`},
	}
	for _, c := range cases {
		if got := pyQuote(c.in); got != c.want {
			t.Errorf("pyQuote(%q) = %s, want %s", c.in, got, c.want)
		}
	}
}

func TestHasErrors(t *testing.T) {
	if HasErrors(nil) {
		t.Error("no problems reported as errors")
	}
	if HasErrors([]Problem{{Severity: SeverityWarn}}) {
		t.Error("a warning alone must not fail a build")
	}
	if !HasErrors([]Problem{{Severity: SeverityWarn}, {Severity: SeverityError}}) {
		t.Error("an error among warnings was missed")
	}
}

// ---------------------------------------------------------------------------
// Ordering
// ---------------------------------------------------------------------------

func TestTopoOrder(t *testing.T) {
	cases := []struct {
		name  string
		tasks []model.Task
		want  []string
	}{
		{
			name: "a chain",
			tasks: []model.Task{
				{ID: "C", Phase: 2, Dependencies: []string{"B"}},
				{ID: "A", Phase: 0},
				{ID: "B", Phase: 1, Dependencies: []string{"A"}},
			},
			want: []string{"A", "B", "C"},
		},
		{
			// Ties break by (phase, id), so the answer does not depend on
			// how tasks.json happened to be written.
			name: "independent tasks come out in display order",
			tasks: []model.Task{
				{ID: "Z", Phase: 0},
				{ID: "A", Phase: 1},
				{ID: "M", Phase: 0},
			},
			want: []string{"M", "Z", "A"},
		},
		{
			name: "a diamond",
			tasks: []model.Task{
				{ID: "D", Phase: 3, Dependencies: []string{"B", "C"}},
				{ID: "B", Phase: 1, Dependencies: []string{"A"}},
				{ID: "C", Phase: 1, Dependencies: []string{"A"}},
				{ID: "A", Phase: 0},
			},
			want: []string{"A", "B", "C", "D"},
		},
		{
			// A dependency can point at a LATER phase. The ordering must
			// follow the edges, not the phase numbers.
			name: "dependencies win over phase numbers",
			tasks: []model.Task{
				{ID: "EARLY", Phase: 0, Dependencies: []string{"LATE"}},
				{ID: "LATE", Phase: 9},
			},
			want: []string{"LATE", "EARLY"},
		},
		{name: "no tasks", tasks: nil, want: []string{}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := TopoOrder(c.tasks)
			if err != nil {
				t.Fatalf("TopoOrder: %v", err)
			}
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestTopoOrderRefusesWhatItCannotOrder(t *testing.T) {
	cases := []struct {
		name     string
		tasks    []model.Task
		wantText string
	}{
		{
			name: "a cycle",
			tasks: []model.Task{
				{ID: "A", Dependencies: []string{"B"}},
				{ID: "B", Dependencies: []string{"A"}},
			},
			wantText: "cycle",
		},
		{
			name:     "a self-dependency",
			tasks:    []model.Task{{ID: "A", Dependencies: []string{"A"}}},
			wantText: "depends on itself",
		},
		{
			name:     "a dependency that does not exist",
			tasks:    []model.Task{{ID: "A", Dependencies: []string{"ghost"}}},
			wantText: "unknown task",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := TopoOrder(c.tasks)
			if err == nil {
				t.Fatalf("TopoOrder returned %v with no error", got)
			}
			if !strings.Contains(err.Error(), c.wantText) {
				t.Errorf("error %q does not mention %q", err, c.wantText)
			}
			// A partial order is worse than none: a caller that ignored the
			// error would dispatch a truncated plan.
			if got != nil {
				t.Errorf("an order was returned alongside the error: %v", got)
			}
		})
	}
}

// The parity fixture has a cycle in it, so a topological order is impossible —
// which is itself the behaviour worth checking on real input.
func TestTopoOrderRefusesTheParityFixture(t *testing.T) {
	tasks, _ := loadParity(t)
	if _, err := TopoOrder(tasks); err == nil {
		t.Error("the fixture contains a cycle and a missing dependency; " +
			"TopoOrder should refuse it")
	}
}

func TestDisplayOrderIsStableAndDoesNotMutate(t *testing.T) {
	tasks := []model.Task{
		{ID: "B", Phase: 1}, {ID: "A", Phase: 1}, {ID: "C", Phase: 0},
	}
	first := DisplayOrder(tasks)
	second := DisplayOrder(tasks)
	if first[0].ID != "C" || first[1].ID != "A" || first[2].ID != "B" {
		t.Errorf("order = %s, want C A B", ids(first))
	}
	if ids(first) != ids(second) {
		t.Errorf("not stable: %s then %s", ids(first), ids(second))
	}
	// The caller's slice is theirs.
	if tasks[0].ID != "B" {
		t.Errorf("DisplayOrder reordered the caller's slice: %v", ids(tasks))
	}
}

// ---------------------------------------------------------------------------
// DOT
// ---------------------------------------------------------------------------

func TestDOTGolden(t *testing.T) {
	tasks, _ := loadParity(t)
	got := DOT(tasks)

	golden := "testdata/graph.golden.dot"
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(golden, []byte(got), 0o600); err != nil {
			t.Fatalf("write the golden: %v", err)
		}
		t.Log("golden updated")
		return
	}
	want, err := os.ReadFile(golden) // #nosec G304 -- fixed testdata path
	if err != nil {
		t.Fatalf("read the golden (regenerate with UPDATE_GOLDEN=1): %v", err)
	}
	if got != string(want) {
		t.Errorf("DOT output changed.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestDOTIsDeterministic(t *testing.T) {
	tasks, _ := loadParity(t)
	// Reversing the input must not change the output: a re-render should
	// never show up in a diff just because tasks.json was re-ordered.
	reversed := make([]model.Task, len(tasks))
	for i, task := range tasks {
		reversed[len(tasks)-1-i] = task
	}
	if DOT(tasks) != DOT(reversed) {
		t.Error("DOT depends on the order of its input")
	}
}

func TestDOTStructure(t *testing.T) {
	tasks := []model.Task{
		{ID: "A", Phase: 0, Title: "First", Status: model.StatusDone},
		{ID: "B", Phase: 1, Title: "Second", Status: model.StatusBlocked,
			Dependencies: []string{"A"}},
		{ID: "SELF", Phase: 1, Dependencies: []string{"SELF"}},
	}
	got := DOT(tasks)

	mustContain := []struct{ what, needle string }{
		{"the digraph header", "digraph orch {"},
		{"a cluster per phase", "subgraph cluster_phase_0 {"},
		{"the second cluster", "subgraph cluster_phase_1 {"},
		{"id and title on two lines", `label="A\nFirst"`},
		{"the dependency edge", `"A" -> "B";`},
		{"done is coloured", `fillcolor="#dcf5e5"`},
		{"blocked is coloured", `fillcolor="#fde2e2"`},
	}
	for _, c := range mustContain {
		if !strings.Contains(got, c.needle) {
			t.Errorf("%s is missing (%q):\n%s", c.what, c.needle, got)
		}
	}

	// A self-loop is a validation error, not a drawing. Rendering it would
	// produce a graph that looks authoritative and is wrong.
	if strings.Contains(got, `"SELF" -> "SELF"`) {
		t.Error("a self-dependency was drawn as an edge")
	}
	// Edges must sit outside the clusters, or dot pulls the target into the
	// wrong phase box.
	edgeAt := strings.Index(got, `"A" -> "B";`)
	lastClusterEnd := strings.LastIndex(got, "  }\n")
	if edgeAt < lastClusterEnd {
		t.Error("an edge was emitted inside a cluster")
	}
}

func TestDOTQuoting(t *testing.T) {
	// Ids come from a user's tasks.json, so a quote or a backslash is
	// possible; unescaped it produces a file `dot` refuses to parse.
	got := DOT([]model.Task{{ID: `we"ird\one`, Phase: 0}})
	if !strings.Contains(got, `"we\"ird\\one"`) {
		t.Errorf("special characters were not escaped:\n%s", got)
	}
}

func TestDOTEmptyGraph(t *testing.T) {
	got := DOT(nil)
	if !strings.HasPrefix(got, "digraph orch {") || !strings.HasSuffix(got, "}\n") {
		t.Errorf("an empty graph should still be valid DOT:\n%s", got)
	}
	if strings.Contains(got, "->") {
		t.Errorf("an empty graph has edges:\n%s", got)
	}
}

// ---------------------------------------------------------------------------

func render(ps []Problem) string {
	b, _ := json.MarshalIndent(ps, "", "  ")
	return string(b)
}

func ids(tasks []model.Task) string {
	out := make([]string, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, t.ID)
	}
	return strings.Join(out, " ")
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// A golden file proves the output has not CHANGED, not that it is valid. This
// checks the shape independently, so a change that keeps the golden in step
// with a broken generator still fails.
//
// It is a structural check, not a Graphviz parse — `dot` is not available on
// this machine and adding a binary dependency to `go test` for one assertion
// is a bad trade. The CI job could run `dot -Tsvg` if that ever stops being
// true; the invariants below are the ones that actually break when a string
// is built by hand.
func TestDOTIsWellFormed(t *testing.T) {
	tasks, _ := loadParity(t)
	out := DOT(tasks)

	depth := 0
	inQuotes := false
	escaped := false
	statements := 0

	for i := 0; i < len(out); i++ {
		c := out[i]
		if inQuotes {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inQuotes = false
			}
			continue
		}
		switch c {
		case '"':
			inQuotes = true
		case '{':
			depth++
		case '}':
			depth--
			if depth < 0 {
				t.Fatalf("a closing brace with nothing open, at byte %d", i)
			}
		case ';':
			statements++
		}
	}

	if inQuotes {
		t.Error("an unterminated quoted string")
	}
	if depth != 0 {
		t.Errorf("braces do not balance: %d still open", depth)
	}
	if statements < len(tasks) {
		t.Errorf("%d terminated statements for %d tasks — some line is missing its `;`",
			statements, len(tasks))
	}

	// Every node id that appears in an edge must have been declared, or dot
	// invents an unlabelled node and the picture quietly gains a box.
	declared := map[string]bool{}
	for _, task := range tasks {
		declared[task.ID] = true
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, "->") {
			continue
		}
		parts := strings.SplitN(strings.TrimSuffix(line, ";"), "->", 2)
		if len(parts) != 2 {
			t.Errorf("malformed edge line: %q", line)
			continue
		}
		for _, side := range parts {
			id := strings.Trim(strings.TrimSpace(side), `"`)
			if !declared[id] {
				t.Errorf("edge %q references %q, which is never declared", line, id)
			}
		}
	}
}
