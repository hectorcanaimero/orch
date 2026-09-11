package state

import (
	"encoding/json"
	"os"
	"sort"
	"testing"
)

// TestEventTypesMatchPython holds the Go set to the one Python declares.
//
// The failure this catches is quiet by construction: both binaries write to
// the same orch.db, and a type Go rejects but Python emits leaves a run
// history with a hole in it rather than an error anyone sees. That is not
// hypothetical — it is bug 9, where `orch.py` emitted seven types its own
// validator refused and the CI poller died silently for months.
//
// The set is exported from the Python tree by `testdata/make-event-types.py`
// rather than transcribed, so the two cannot drift without this failing. If it
// fails after a Python change, regenerate the JSON before touching the map:
// editing the map to match a hand-edited JSON makes the comparison circular.
func TestEventTypesMatchPython(t *testing.T) {
	raw, err := os.ReadFile("testdata/event-types.json")
	if err != nil {
		t.Fatalf("read event types vector: %v", err)
	}
	var vector struct {
		All []string `json:"all"`
	}
	if err := json.Unmarshal(raw, &vector); err != nil {
		t.Fatalf("decode event types vector: %v", err)
	}
	if len(vector.All) == 0 {
		t.Fatal("the vector lists no event types at all")
	}

	want := make(map[string]bool, len(vector.All))
	for _, name := range vector.All {
		want[name] = true
	}

	var missing, extra []string
	for name := range want {
		if !eventTypes[name] {
			missing = append(missing, name)
		}
	}
	for name := range eventTypes {
		if !want[name] {
			extra = append(extra, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)

	if len(missing) > 0 {
		t.Errorf("Python declares these and Go rejects them: %v", missing)
	}
	if len(extra) > 0 {
		t.Errorf("Go accepts these and Python does not declare them: %v", extra)
	}
}

// The four names the plan's FR-STATE-7 listed are not event types.
// `docs/history/spec.md:107` records that they were superseded on 2026-08-19,
// before any of this was written; an earlier version of eventTypes had them
// anyway, which would have let a Go binary accept rows nothing can read and
// reject the four that replaced them.
//
// Named explicitly rather than left to the vector comparison because a
// regression here comes from someone reading the same stale spec section, and
// a list of names they can search for is more use to them than a diff.
func TestSupersededEventTypesAreRejected(t *testing.T) {
	for _, name := range []string{"exit_ok", "exit_err", "resume_reset", "dry_run_planned"} {
		if eventTypes[name] {
			t.Errorf("%q is from the superseded FR-STATE-7 list; Python has no such event type", name)
		}
	}
	// And the names that replaced them are present.
	for _, name := range []string{"success", "fail", "resume_revert"} {
		if !eventTypes[name] {
			t.Errorf("%q replaced one of the superseded names and must be accepted", name)
		}
	}
}
