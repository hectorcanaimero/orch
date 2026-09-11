package doctor

import "testing"

func TestSummaryCountsByStatus(t *testing.T) {
	checks := []Check{
		{Status: StatusOK}, {Status: StatusOK}, {Status: StatusWarn},
		{Status: StatusError}, {Status: StatusSkip}, {Status: StatusSkip},
	}
	got := Summary(checks)
	want := map[Status]int{StatusOK: 2, StatusWarn: 1, StatusError: 1, StatusSkip: 2}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("Summary()[%s] = %d, want %d", k, got[k], v)
		}
	}
}

func TestExitCodeErrorWins(t *testing.T) {
	checks := []Check{{Status: StatusWarn}, {Status: StatusError}, {Status: StatusOK}}
	if got := ExitCode(checks); got != 2 {
		t.Errorf("ExitCode = %d, want 2", got)
	}
}

func TestExitCodeWarnWhenNoError(t *testing.T) {
	checks := []Check{{Status: StatusOK}, {Status: StatusWarn}}
	if got := ExitCode(checks); got != 1 {
		t.Errorf("ExitCode = %d, want 1", got)
	}
}

func TestExitCodeZeroWhenClean(t *testing.T) {
	checks := []Check{{Status: StatusOK}, {Status: StatusSkip}}
	if got := ExitCode(checks); got != 0 {
		t.Errorf("ExitCode = %d, want 0", got)
	}
}

func TestSortByName(t *testing.T) {
	checks := []Check{{Name: "z"}, {Name: "a"}, {Name: "m"}}
	SortByName(checks)
	if checks[0].Name != "a" || checks[1].Name != "m" || checks[2].Name != "z" {
		t.Errorf("checks = %+v", checks)
	}
}
