package cli

import (
	"reflect"
	"testing"
)

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
