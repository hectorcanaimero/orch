package notify

import (
	"context"
	"testing"
	"time"

	"github.com/hectorcanaimero/orch/internal/budget"
)

// TestBudgetAlertText: Go only, so there is no Python capture. The numbers
// are the gate's weighted ones, the cap is threshold_pct of token_budget, and
// an estimated window says so.
func TestBudgetAlertText(t *testing.T) {
	tests := []struct {
		name  string
		alert budget.Alert
		want  string
	}{
		{
			name: "near the cap, reported",
			alert: budget.Alert{Provider: "claude", Level: budget.AlertNear,
				TokensUsed: 326400, Cap: 408000, PctOfCap: 80, WindowHours: 5},
			want: ":warning: orch: claude is at 80% of its budget cap (326,400 of 408,000 tokens in its 5h window).",
		},
		{
			name: "at the cap, estimated, with waiting tasks",
			alert: budget.Alert{Provider: "gemini", Level: budget.AlertCapped,
				TokensUsed: 410000, Cap: 408000, PctOfCap: 100.5, WindowHours: 2.5,
				ResetAt: time.Date(2026, 9, 16, 14, 5, 30, 0, time.UTC), Waiting: 3, Estimated: true},
			want: ":octagonal_sign: orch: gemini reached its budget cap (410,000 of 408,000 tokens in its 2.5h window, " +
				"part of it estimated). New gemini dispatches wait until about 2026-09-16 14:05 UTC. 3 task(s) waiting.",
		},
		{
			name:  "at the cap with no reset estimate",
			alert: budget.Alert{Provider: "codex", Level: budget.AlertCapped, TokensUsed: 10, Cap: 0, WindowHours: 1},
			want:  ":octagonal_sign: orch: codex reached its budget cap (10 of 0 tokens in its 1h window). New codex dispatches wait.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := BudgetAlertText(tt.alert); got != tt.want {
				t.Errorf("got\n  %q\nwant\n  %q", got, tt.want)
			}
		})
	}
}

func TestBudgetAlertPostsTheText(t *testing.T) {
	rec := newRecorder(t, 0)
	n := &Notifier{DiscordWebhook: rec.discordURL()}
	a := budget.Alert{Provider: "claude", Level: budget.AlertNear, TokensUsed: 1, Cap: 1, PctOfCap: 100, WindowHours: 5}
	n.BudgetAlert(context.Background(), a)

	sent := rec.got()
	if len(sent) != 1 {
		t.Fatalf("posted %d times, want 1", len(sent))
	}
	if got := messageOf(t, sent[0]); got != BudgetAlertText(a) {
		t.Errorf("sent %q, want the BudgetAlertText", got)
	}
}
