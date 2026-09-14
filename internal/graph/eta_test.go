package graph

import (
	"testing"
	"time"
)

// One projection for every surface that shows a finish date: the Sprint page,
// the stakeholder summary, the published snapshot and the PDF. They used to
// disagree on the same screen (1.7h vs "29h" vs "15 de septiembre").
func TestProjectCompletion(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		remaining  int
		done       int
		wantNil    bool
		wantDays   float64
		wantDate   string
		wantConfid string
	}{
		{name: "nothing left", remaining: 0, done: 7, wantNil: true},
		{name: "no velocity yet", remaining: 5, done: 0, wantNil: true},
		{name: "a week of pace", remaining: 7, done: 14, wantDays: 3.5, wantDate: "2026-09-18", wantConfid: "high"},
		{name: "slow enough to stop trusting", remaining: 40, done: 7, wantDays: 40, wantDate: "2026-10-24", wantConfid: "low"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ProjectCompletion(tt.remaining, tt.done, 7, now)
			if tt.wantNil {
				if got != nil {
					t.Errorf("got %+v, want no projection", *got)
				}
				return
			}
			if got == nil {
				t.Fatal("got no projection")
			}
			if got.Days != tt.wantDays || got.Date != tt.wantDate || got.Confidence != tt.wantConfid {
				t.Errorf("got %+v, want days %v date %s confidence %s", *got, tt.wantDays, tt.wantDate, tt.wantConfid)
			}
		})
	}
}
