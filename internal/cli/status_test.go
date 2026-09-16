package cli

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/router"
	"github.com/hectorcanaimero/orch/internal/state"
)

// A todo task the budget gate deferred says so in `defer_reason`, which had
// been hard-coded to null: the reason lived only in the run's memory, so
// `orch status` showed a ready task sitting still with no explanation. A
// reset already past is not waiting any more.
func TestStatusShowsTheBudgetDeferral(t *testing.T) {
	ctx := context.Background()
	db, _, err := state.Open(ctx, filepath.Join(t.TempDir(), "orch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	backend := state.NewSQLite(db, "p", t.TempDir())

	tasks := []model.Task{
		{ID: "A", Title: "waits", Status: model.StatusTodo, Model: "claude/opus"},
		{ID: "B", Title: "waited", Status: model.StatusTodo, Model: "claude/opus"},
	}
	if err := backend.Bootstrap(ctx, tasks); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour).UTC().Format("2006-01-02T15:04:05Z")
	past := time.Now().Add(-time.Hour).UTC().Format("2006-01-02T15:04:05Z")
	for id, reset := range map[string]string{"A": future, "B": past} {
		if err := backend.AppendEvent(ctx, "run-1", state.Event{
			EventType: "budget_skip", TaskID: id, Backend: "claude",
			Extra: map[string]any{"reset_at": reset, "reason": "claude over threshold"},
		}); err != nil {
			t.Fatal(err)
		}
	}

	rows, err := buildStatusRows(ctx, backend, "p", tasks, router.Router{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "blocked-by-budget:claude until " + future
	if rows[0].DeferReason == nil || *rows[0].DeferReason != want {
		t.Errorf("A defer_reason = %v, want %q", rows[0].DeferReason, want)
	}
	if rows[1].DeferReason != nil {
		t.Errorf("B defer_reason = %q, want null once the reset has passed", *rows[1].DeferReason)
	}
}

func TestParseStatusList(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want map[string]bool
	}{
		{"empty", "", nil},
		{"single", "todo", map[string]bool{"todo": true}},
		{"multiple", "todo,in-progress", map[string]bool{"todo": true, "in-progress": true}},
		{"whitespace and blanks collapse", " todo , , done ", map[string]bool{"todo": true, "done": true}},
		{"only blanks", " , ,", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseStatusList(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseStatusList(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestCostForMatchesPythonsSumTypeQuirk(t *testing.T) {
	// Python: sum(cost_by_task.values()) over an empty dict is the int 0
	// (json "0"); sum(r["cost_usd"] for r in filtered) over a non-empty
	// list of floats is a float (json "0.0"), even when every term is
	// zero — but over an EMPTY filtered list, sum() of an empty generator
	// is int 0 again. See the costJSON doc comment in status.go.
	cases := []struct {
		name          string
		costByTask    map[string]float64
		filteredSum   float64
		filteredCount int
		wantProject   string
		wantFiltered  string
	}{
		{"no spend at all", nil, 0, 0, "0", "0"},
		{"spend exists but filtered set is empty", map[string]float64{"F0.T1": 0.42}, 0, 0, "0.42", "0"},
		{"single zero-cost task still filtered in", map[string]float64{"F0.T1": 0}, 0, 1, "0.0", "0.0"},
		{"real spend, all filtered in", map[string]float64{"F0.T1": 0.42, "F1.T1": 1.0}, 1.42, 5, "1.42", "1.42"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := costFor(tc.costByTask, tc.filteredSum, tc.filteredCount)
			if string(got.ProjectTotalUSD) != tc.wantProject {
				t.Errorf("ProjectTotalUSD = %s, want %s", got.ProjectTotalUSD, tc.wantProject)
			}
			if string(got.FilteredTotalUSD) != tc.wantFiltered {
				t.Errorf("FilteredTotalUSD = %s, want %s", got.FilteredTotalUSD, tc.wantFiltered)
			}
		})
	}
}
