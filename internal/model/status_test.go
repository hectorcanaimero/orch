package model

import (
	"encoding/json"
	"os"
	"testing"
)

// TestCanTransitionMatchesPython checks CanTransition against every
// (from, to) pair Python's real _STATUS_TRANSITIONS table produces — see
// testdata/README.md for exactly how testdata/transitions.json was
// generated. This is the "coincide 1:1 con la de Python" requirement from
// the G1.3 brief, verified against actual Python output rather than a
// hand-copied table that could silently drift.
func TestCanTransitionMatchesPython(t *testing.T) {
	data, err := os.ReadFile("testdata/transitions.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Statuses    []string `json:"statuses"`
		Transitions []struct {
			From  string `json:"from"`
			To    string `json:"to"`
			Legal bool   `json:"legal"`
		} `json:"transitions"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Transitions) != 25 {
		t.Fatalf("fixture has %d transition rows, want 25 (5x5 status matrix)", len(fixture.Transitions))
	}
	for _, row := range fixture.Transitions {
		got := CanTransition(Status(row.From), Status(row.To))
		if got != row.Legal {
			t.Errorf("CanTransition(%q, %q) = %v, want %v (per Python's _STATUS_TRANSITIONS)",
				row.From, row.To, got, row.Legal)
		}
	}
}

// TestTransitionsIsCanTransitionsInverse checks Transitions(from) lists
// exactly the statuses CanTransition(from, *) allows — no more, no less.
func TestTransitionsIsCanTransitionsInverse(t *testing.T) {
	for _, from := range allStatuses {
		listed := Transitions(from)
		listedSet := map[Status]bool{}
		for _, to := range listed {
			listedSet[to] = true
			if !CanTransition(from, to) {
				t.Errorf("Transitions(%q) lists %q, but CanTransition rejects it", from, to)
			}
		}
		for _, to := range allStatuses {
			if CanTransition(from, to) && !listedSet[to] {
				t.Errorf("CanTransition(%q, %q) = true, but Transitions(%q) omits it", from, to, from)
			}
		}
	}
}

func TestParseStatus(t *testing.T) {
	cases := []struct {
		in      string
		wantErr bool
	}{
		{"backlog", false},
		{"todo", false},
		{"in-progress", false},
		{"done", false},
		{"blocked", false},
		{"skipped", true}, // valid CI status, NOT a valid task Status
		{"", true},
		{"TODO", true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			_, err := ParseStatus(tc.in)
			if (err != nil) != tc.wantErr {
				t.Errorf("ParseStatus(%q) error = %v, wantErr %v", tc.in, err, tc.wantErr)
			}
		})
	}
}
