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
		filteredCount int
		wantFiltered  string
	}{
		{0, "0"},
		{1, "0.0"},
		{5, "0.0"},
	}
	for _, tc := range cases {
		got := costFor(tc.filteredCount)
		if string(got.ProjectTotalUSD) != "0" {
			t.Errorf("costFor(%d).ProjectTotalUSD = %s, want 0", tc.filteredCount, got.ProjectTotalUSD)
		}
		if string(got.FilteredTotalUSD) != tc.wantFiltered {
			t.Errorf("costFor(%d).FilteredTotalUSD = %s, want %s",
				tc.filteredCount, got.FilteredTotalUSD, tc.wantFiltered)
		}
	}
}
