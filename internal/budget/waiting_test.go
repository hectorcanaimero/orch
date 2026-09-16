package budget

import (
	"testing"
	"time"

	"github.com/hectorcanaimero/orch/internal/state"
)

// A task is waiting on the budget while its newest event is the gate's
// deferral and the reset it names is still ahead. Anything newer — a
// dispatch — or a reset already past means it is not waiting any more, which
// is what keeps a crashed run from leaving "blocked by budget" up forever.
func TestWaitingOn(t *testing.T) {
	now := time.Date(2026, 9, 12, 14, 0, 0, 0, time.UTC)
	skip := func(reset string) state.Event {
		return state.Event{EventType: "budget_skip", Backend: "claude", TaskID: "C-1",
			Extra: map[string]any{"reset_at": reset, "reason": "claude over threshold"}}
	}

	tests := []struct {
		name string
		ev   state.Event
		want bool
	}{
		{"deferred, reset ahead", skip("2026-09-12T15:00:00Z"), true},
		{"deferred, reset already past", skip("2026-09-12T13:59:59Z"), false},
		{"deferred with no readable reset", skip("soon"), false},
		{"dispatched since", state.Event{EventType: "dispatch", Backend: "claude"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, ok := WaitingOn(tt.ev, now)
			if ok != tt.want {
				t.Fatalf("ok = %v, want %v", ok, tt.want)
			}
			if ok && (w.Provider != "claude" || w.ResetAt != "2026-09-12T15:00:00Z" || w.Reason != "claude over threshold") {
				t.Errorf("w = %+v", w)
			}
		})
	}
}
