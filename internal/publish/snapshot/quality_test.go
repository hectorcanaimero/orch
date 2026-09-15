package snapshot

import (
	"strings"
	"testing"
	"time"

	"github.com/hectorcanaimero/orch/internal/model"
)

func TestQualityCountsDeliveriesThroughCI(t *testing.T) {
	in := fixtureInput(time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC))
	in.Tasks = []model.Task{
		{ID: "A", Status: model.StatusDone},       // passed first time
		{ID: "B", Status: model.StatusDone},       // passed after a retry
		{ID: "C", Status: model.StatusDone},       // merged with a red last reading
		{ID: "D", Status: model.StatusInProgress}, // still under review: not delivered
		{ID: "E", Status: model.StatusDone},       // done without a PR
	}
	in.Gates = []string{"tests", "review"}
	in.CI = map[string]TaskCI{
		"A": {Status: "success"},
		"B": {Status: "success", Attempts: 1},
		"C": {Status: "failure", Attempts: 2},
		"D": {Status: "pending"},
	}

	q := Build(in).Quality
	if q == nil {
		t.Fatal("quality is absent with gates configured")
	}
	if strings.Join(q.Gates, ",") != "tests,review" || q.Delivered != 3 || q.Verified != 2 || q.FirstPass != 1 {
		t.Errorf("quality = %+v, want gates tests,review and 3 delivered, 2 verified, 1 first pass", *q)
	}
}

// A project whose pull requests go through no check says nothing about
// quality, rather than "0 verified".
func TestQualityAbsentWithoutGates(t *testing.T) {
	in := fixtureInput(time.Now())
	in.CI = map[string]TaskCI{"F0.1.T1": {Status: "success"}}
	if q := Build(in).Quality; q != nil {
		t.Errorf("quality = %+v, want absent", *q)
	}
}
