package cli

import (
	"testing"

	"github.com/hectorcanaimero/orch/internal/state"
)

func evs(runIDs ...string) []state.Event {
	out := make([]state.Event, len(runIDs))
	for i, r := range runIDs {
		out[i] = state.Event{ID: int64(i), RunID: r, EventType: "dispatch"}
	}
	return out
}

func ids(evs []state.Event) []int64 {
	out := make([]int64, len(evs))
	for i, e := range evs {
		out[i] = e.ID
	}
	return out
}

func TestShortRunID(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"short", "short"},
		{"exactly8", "exactly8"},
		{"fixture-run-0001", "fixture-"},
		{"a-very-long-run-id-indeed", "a-very-l"},
	}
	for _, tc := range cases {
		if got := shortRunID(tc.in); got != tc.want {
			t.Errorf("shortRunID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFilterAndTail(t *testing.T) {
	all := evs("run-a", "run-a", "run-b", "run-a", "run-b")
	// ids: 0,1,2,3,4 with runs a,a,b,a,b

	cases := []struct {
		name  string
		runID string
		tail  int
		want  []int64
	}{
		{"no filter no tail", "", 0, []int64{0, 1, 2, 3, 4}},
		{"tail 2 no filter", "", 2, []int64{3, 4}},
		{"tail bigger than set", "", 100, []int64{0, 1, 2, 3, 4}},
		{"run filter only", "run-b", 0, []int64{2, 4}},
		{"run filter then tail", "run-a", 1, []int64{3}},
		{"unknown run filters to empty", "run-z", 0, []int64{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ids(filterAndTail(all, tc.runID, tc.tail))
			if len(got) != len(tc.want) {
				t.Fatalf("filterAndTail(...) = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("filterAndTail(...) = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// The run-level row's task column.
//
// No Python-written database contains one — `sprint_done` is Go's own event
// type (F4.7) — so `testdata/orch-py-0.11.0.db` cannot exercise this and the
// testscript over it does not. Tested here instead of being left to a fixture
// that structurally cannot reach it.
//
// The em dash rather than an empty cell: in a tab-aligned table a blank reads
// as a rendering bug, where "—" says there is nothing to put there. It is the
// same mark the dashboard uses for a milestone with no ETA.
func TestTaskColumn(t *testing.T) {
	cases := []struct{ in, want string }{
		{"F1.T3", "F1.T3"},
		{"", "—"},
	}
	for _, c := range cases {
		if got := taskColumn(c.in); got != c.want {
			t.Errorf("taskColumn(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// An error reading the whole log must not name a task id it was never given.
func TestForTask(t *testing.T) {
	if got := forTask(""); got != "" {
		t.Errorf("forTask(\"\") = %q, want empty", got)
	}
	if got, want := forTask("F1.T3"), ` for "F1.T3"`; got != want {
		t.Errorf("forTask = %q, want %q", got, want)
	}
}
