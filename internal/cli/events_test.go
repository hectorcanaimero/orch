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
