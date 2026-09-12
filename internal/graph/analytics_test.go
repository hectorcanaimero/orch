package graph

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"testing"

	"github.com/hectorcanaimero/orch/internal/model"
)

// The golden is what PYTHON answers for the same eleven graphs — see
// testdata/make-analytics-golden.py. The scenarios are built there rather
// than here so there is exactly one definition of each shape, and this test
// rebuilds them from the same descriptions.
type analyticsGolden struct {
	Summary struct {
		Total              int     `json:"total"`
		Done               int     `json:"done"`
		InProgress         int     `json:"in_progress"`
		Blocked            int     `json:"blocked"`
		Backlog            int     `json:"backlog"`
		PercentDone        float64 `json:"percent_done"`
		EstimateHoursTotal float64 `json:"estimate_hours_total"`
	} `json:"summary"`
	PhaseCounts []struct {
		Phase      int `json:"phase"`
		Total      int `json:"total"`
		Done       int `json:"done"`
		InProgress int `json:"in_progress"`
		Blocked    int `json:"blocked"`
	} `json:"phase_counts"`
	Parallelizable   []string       `json:"parallelizable"`
	DownstreamImpact map[string]int `json:"downstream_impact"`
	CriticalPath     []string       `json:"critical_path"`
	Orphans          []struct {
		TaskID       string `json:"task_id"`
		MissingDepID string `json:"missing_dep_id"`
	} `json:"orphans"`
}

func mkTask(id string, phase int, status model.Status, deps []string, hours float64) model.Task {
	return model.Task{
		ID: id, Phase: phase, Title: id, Model: "claude/claude-sonnet-4-6",
		Status: status, Dependencies: deps, EstimateHours: hours,
	}
}

// analyticsScenarios mirrors SCENARIOS in make-analytics-golden.py, name for
// name. A name here with no entry there (or the reverse) fails the test rather
// than quietly testing ten of eleven.
func analyticsScenarios(t *testing.T) map[string][]model.Task {
	t.Helper()
	return map[string][]model.Task{
		"empty":              {},
		"single-unestimated": {mkTask("A", 0, model.StatusTodo, nil, 0)},
		"chain": {
			mkTask("A", 0, model.StatusDone, nil, 1.0),
			mkTask("B", 0, model.StatusTodo, []string{"A"}, 2.0),
			mkTask("C", 0, model.StatusTodo, []string{"B"}, 3.0),
		},
		"tie": {
			mkTask("root", 0, model.StatusTodo, nil, 1.0),
			mkTask("left", 0, model.StatusTodo, []string{"root"}, 2.0),
			mkTask("right", 0, model.StatusTodo, []string{"root"}, 2.0),
		},
		"tie-reversed": {
			mkTask("root", 0, model.StatusTodo, nil, 1.0),
			mkTask("right", 0, model.StatusTodo, []string{"root"}, 2.0),
			mkTask("left", 0, model.StatusTodo, []string{"root"}, 2.0),
		},
		"done-in-the-middle": {
			mkTask("A", 0, model.StatusDone, nil, 0),
			mkTask("B", 0, model.StatusDone, []string{"A"}, 0),
			mkTask("C", 0, model.StatusTodo, []string{"B"}, 0),
			mkTask("D", 0, model.StatusTodo, []string{"C"}, 0),
		},
		"orphan-dep": {
			mkTask("A", 0, model.StatusTodo, []string{"GHOST"}, 0),
			mkTask("B", 0, model.StatusTodo, nil, 5.0),
		},
		"cycle": {
			mkTask("A", 0, model.StatusTodo, []string{"B"}, 0),
			mkTask("B", 0, model.StatusTodo, []string{"A"}, 0),
			mkTask("C", 0, model.StatusTodo, nil, 9.0),
		},
		"mixed-statuses": {
			mkTask("p0-done", 0, model.StatusDone, nil, 1.5),
			mkTask("p0-blocked", 0, model.StatusBlocked, nil, 2.0),
			mkTask("p1-todo", 1, model.StatusTodo, []string{"p0-done"}, 0.5),
			mkTask("p1-wip", 1, model.StatusInProgress, nil, 3.0),
			mkTask("p1-backlog", 1, model.StatusBacklog, []string{"p0-blocked"}, 0),
			mkTask("p5-todo", 5, model.StatusTodo, nil, 0),
		},
		"parallelizable": {
			mkTask("dep", 0, model.StatusDone, nil, 0),
			mkTask("ready-todo", 2, model.StatusTodo, []string{"dep"}, 0),
			mkTask("ready-backlog", 1, model.StatusBacklog, []string{"dep"}, 0),
			mkTask("not-ready-dep-open", 0, model.StatusTodo, []string{"ready-todo"}, 0),
			mkTask("not-ready-wip", 0, model.StatusInProgress, []string{"dep"}, 0),
			mkTask("not-ready-blocked", 0, model.StatusBlocked, []string{"dep"}, 0),
			mkTask("not-ready-done", 0, model.StatusDone, []string{"dep"}, 0),
		},
		"parity-project": loadParityTasks(t),
	}
}

func loadParityTasks(t *testing.T) []model.Task {
	t.Helper()
	f, err := model.LoadTasksFile(filepath.Join("testdata", "parity-project", "tasks.json"))
	if err != nil {
		t.Fatalf("load parity tasks.json: %v", err)
	}
	return f.Tasks
}

func TestAnalyticsMatchesThePythonGolden(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "analytics.golden.json"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var golden map[string]analyticsGolden
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatalf("parse golden: %v", err)
	}

	scenarios := analyticsScenarios(t)
	if len(scenarios) != len(golden) {
		t.Fatalf("%d scenarios in Go, %d in the golden — one side grew alone",
			len(scenarios), len(golden))
	}

	for name, tasks := range scenarios {
		want, ok := golden[name]
		if !ok {
			t.Fatalf("scenario %q has no golden — regenerate with make-analytics-golden.py", name)
		}
		t.Run(name, func(t *testing.T) {
			got := Summarize(tasks)
			if got.Total != want.Summary.Total || got.Done != want.Summary.Done ||
				got.InProgress != want.Summary.InProgress ||
				got.Blocked != want.Summary.Blocked || got.Backlog != want.Summary.Backlog {
				t.Errorf("Summarize counts = %+v, want %+v", got, want.Summary)
			}
			// Python rounds both floats to one decimal for the wire. Comparing
			// the rounded values is comparing what a caller actually sees.
			if round1(got.PercentDone) != want.Summary.PercentDone {
				t.Errorf("PercentDone = %v, want %v", round1(got.PercentDone), want.Summary.PercentDone)
			}
			if round1(got.EstimateHoursTotal) != want.Summary.EstimateHoursTotal {
				t.Errorf("EstimateHoursTotal = %v, want %v",
					round1(got.EstimateHoursTotal), want.Summary.EstimateHoursTotal)
			}

			gotPhases := PhaseCounts(tasks)
			if len(gotPhases) != len(want.PhaseCounts) {
				t.Fatalf("PhaseCounts = %d rows, want %d", len(gotPhases), len(want.PhaseCounts))
			}
			for i, w := range want.PhaseCounts {
				g := gotPhases[i]
				if g.Phase != w.Phase || g.Total != w.Total || g.Done != w.Done ||
					g.InProgress != w.InProgress || g.Blocked != w.Blocked {
					t.Errorf("PhaseCounts[%d] = %+v, want %+v", i, g, w)
				}
			}

			gotPara := make([]string, 0, len(tasks))
			for _, x := range Parallelizable(tasks) {
				gotPara = append(gotPara, x.ID)
			}
			if !equalStrings(gotPara, want.Parallelizable) {
				t.Errorf("Parallelizable = %v, want %v", gotPara, want.Parallelizable)
			}

			gotImpact := DownstreamImpact(tasks)
			if len(want.DownstreamImpact) == 0 && len(gotImpact) == 0 {
				// both empty; reflect.DeepEqual on nil vs {} would disagree
			} else if !reflect.DeepEqual(gotImpact, want.DownstreamImpact) {
				t.Errorf("DownstreamImpact = %v, want %v", gotImpact, want.DownstreamImpact)
			}

			gotPath := sortedKeys(CriticalPath(tasks))
			if !equalStrings(gotPath, want.CriticalPath) {
				t.Errorf("CriticalPath = %v, want %v", gotPath, want.CriticalPath)
			}

			gotOrphans := OrphanDependencies(tasks)
			if len(gotOrphans) != len(want.Orphans) {
				t.Fatalf("OrphanDependencies = %v, want %v", gotOrphans, want.Orphans)
			}
			for i, w := range want.Orphans {
				if gotOrphans[i].TaskID != w.TaskID || gotOrphans[i].MissingDepID != w.MissingDepID {
					t.Errorf("OrphanDependencies[%d] = %+v, want %+v", i, gotOrphans[i], w)
				}
			}
		})
	}
}

// The tie scenarios are the reason CriticalPath reproduces Python's traversal
// order instead of just its arithmetic. Both branches weigh the same; which
// one is "the" critical path is decided by declaration order. A port that
// iterated a Go map would pass this test about half the time, which is worse
// than failing it.
func TestCriticalPathBreaksTiesByDeclarationOrder(t *testing.T) {
	scenarios := analyticsScenarios(t)
	if got := sortedKeys(CriticalPath(scenarios["tie"])); !equalStrings(got, []string{"right", "root"}) {
		t.Errorf("tie: got %v, want [right root]", got)
	}
	if got := sortedKeys(CriticalPath(scenarios["tie-reversed"])); !equalStrings(got, []string{"left", "root"}) {
		t.Errorf("tie-reversed: got %v, want [left root]", got)
	}
}

// A duplicate id is a broken tasks.json — `Validate` says so — but these
// functions still have to answer something rather than panic, and the
// something has to match Python's, where the last row with an id wins the
// lookup table.
func TestDuplicateIDsResolveToTheLastOne(t *testing.T) {
	tasks := []model.Task{
		mkTask("A", 0, model.StatusTodo, nil, 0),
		mkTask("A", 0, model.StatusDone, nil, 0),
		mkTask("B", 0, model.StatusTodo, []string{"A"}, 0),
	}
	// Two things follow from the index keeping only the LAST A, and they pull
	// in opposite directions: B's dependency is satisfied (that A is done), and
	// A itself is not offered (a done task is not ready to launch). The first
	// A — todo, and by row order the one a reader would expect to see — is not
	// considered at all. Verified against Python, which answers ["B"] too.
	var ids []string
	for _, x := range Parallelizable(tasks) {
		ids = append(ids, x.ID)
	}
	if !equalStrings(ids, []string{"B"}) {
		t.Errorf("Parallelizable = %v, want [B] — only the last A is indexed, and it is done", ids)
	}
	// Summarize counts ROWS, not ids: both A rows are there.
	if got := Summarize(tasks); got.Total != 3 {
		t.Errorf("Summarize total = %d, want 3 — it counts rows, not distinct ids", got.Total)
	}
}

func round1(v float64) float64 {
	return float64(int64(v*10+0.5)) / 10
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func equalStrings(a, b []string) bool { return slices.Equal(a, b) }
