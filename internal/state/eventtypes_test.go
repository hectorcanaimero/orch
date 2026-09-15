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
		if !want[name] && !goOnlyEventTypes[name] {
			extra = append(extra, name)
		}
	}
	// A name in the carve-out that Python has since declared is no longer
	// Go-only, and leaving it in the list would hide the day the two agree.
	for name := range goOnlyEventTypes {
		if want[name] {
			t.Errorf("%q is in goOnlyEventTypes but Python now declares it; drop the carve-out", name)
		}
		if !eventTypes[name] {
			t.Errorf("%q is carved out of the comparison but Go does not accept it", name)
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

// goOnlyEventTypes are types the Go binary emits and Python does not declare.
//
// The comparison above exists because a type ONE binary emits and the OTHER
// rejects leaves a run history with a hole in it (bug 9). The direction is
// what makes this carve-out safe rather than a hole of its own: Python's
// validator refuses an unknown type only on WRITE, and `iter_events` filters
// on project, run, task and id — never on the type — so a row Go wrote reads
// back in Python intact, in `orch events` and in the dashboard alike. Checked
// by reading `sqlite_backend.iter_events`'s WHERE clause, not assumed.
//
// The list is explicit, and each entry has to earn its place here rather than
// be discovered as drift:
//
//   - `sprint_done` (F4.7) — emitted once when a run's queue empties. It is a
//     Go-side plan item; the Python line is frozen, so adding it to
//     `EVENT_TYPES` there would be a new feature in a codebase that is not
//     taking any.
//
// Same shape as `TestGoTreeMatchesPython`'s four-entry `goOnly` allowlist for
// the `expo-mobile` template: a deliberate Go-only addition, named, with the
// reason next to it.
var goOnlyEventTypes = map[string]bool{
	"sprint_done": true,
	// A PR finished with no CI check ever reported (#233).
	"ci_no_checks": true,
	// A PR merged outside orch while its CI was watched (#255).
	"pr_merged": true,
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
