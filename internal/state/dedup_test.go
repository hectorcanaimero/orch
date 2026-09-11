package state

import "testing"

// The six vectors below are not invented: they are the exact preimages and
// hashes the Python backend wrote into testdata/orch-py-0.11.0.db. They are
// reproduced in testdata/README.md alongside the query that extracts them.
//
// If one of these fails, a Go orch and a Python orch pointed at the same
// orch.db will write duplicate rows. That is the whole reason this file
// exists.

func TestEventDedupHashMatchesPython(t *testing.T) {
	const (
		project = "billing-api"
		run     = "fixture-run-0001"
	)
	cases := []struct {
		name      string
		ts        string
		taskID    string
		eventType string
		pidHint   string
		want      string
	}{
		{
			name: "with a pid in extra",
			ts:   "2026-09-01T09:00:00+00:00", taskID: "F0.T1",
			eventType: "dispatch", pidHint: "4240",
			want: "772af28dead398ce110c22a5fe8ec1c0dca102bc2a2b9b6aab385e29c350e09b",
		},
		{
			// No pid: the separator is still written, so the preimage ends
			// in "|". Dropping the empty field changes every hash.
			name: "without a pid",
			ts:   "2026-09-01T10:30:00+00:00", taskID: "F0.T1",
			eventType: "success", pidHint: "",
			want: "5e6aafc4f8a20f0ad6c781cae04c3c36dcea9e95f30339857de0bac8207f03c3",
		},
		{
			name: "second task, second pid",
			ts:   "2026-09-02T11:15:00+00:00", taskID: "F1.T1",
			eventType: "dispatch", pidHint: "4242",
			want: "6afe96a086be445cbcbc98740c6bbe055ac89a124daa5962da1ef9037ba57497",
		},
		{
			name: "block carries no pid",
			ts:   "2026-09-03T14:45:00+00:00", taskID: "F1.T2",
			eventType: "block", pidHint: "",
			want: "fe6ec73c044b44dc591f33729d2782a5ec3eb7cdbb0620056d249ab251f384cd",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := eventDedupHash(project, c.ts, c.taskID, c.eventType, run, c.pidHint)
			if got != c.want {
				t.Errorf("eventDedupHash = %s\nPython wrote      %s", got, c.want)
			}
		})
	}
}

func TestSpendDedupHashMatchesPython(t *testing.T) {
	const project = "billing-api"
	cases := []struct {
		name      string
		ts        string
		taskID    string
		backend   string
		model     string
		costUSD   float64
		durationS float64
		want      string
	}{
		{
			// duration_s = 5400.0 — Python writes "5400.0", Go's default
			// formatting writes "5400".
			name: "integral duration",
			ts:   "2026-09-01T10:30:00+00:00", taskID: "F0.T1",
			backend: "claude", model: "claude/claude-sonnet-4-6",
			costUSD: 0.42, durationS: 5400.0,
			want: "623f4c698b191a2ffa77dc11ce55628be8ef46775985bdde51c2b083f628a2c9",
		},
		{
			// cost_usd = 1.0 — same trap on the other field.
			name: "integral cost",
			ts:   "2026-09-02T11:15:00+00:00", taskID: "F0.T2",
			backend: "claude", model: "claude/claude-opus-4-6",
			costUSD: 1.0, durationS: 1.5,
			want: "a7cd159f3edd7730502bbb81ddf5224ebefba00a0843d4016c6734f4f7204ca2",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := spendDedupHash(project, c.ts, c.taskID, c.backend, c.model, c.costUSD, c.durationS)
			if got != c.want {
				t.Errorf("spendDedupHash = %s\nPython wrote      %s", got, c.want)
			}
		})
	}
}

// Guards against a plausible "simplification": formatting the floats with
// Go's default shortest form. Both vectors above would break; this states why
// in one place.
func TestSpendDedupHashWouldBreakWithGoFloatFormatting(t *testing.T) {
	if pyFloat(5400.0) == "5400" {
		t.Fatal("pyFloat regressed to Go formatting — the spend dedup hash is now wrong")
	}
	if pyFloat(1.0) == "1" {
		t.Fatal("pyFloat regressed to Go formatting — the spend dedup hash is now wrong")
	}
}

func TestPidHintFromExtra(t *testing.T) {
	cases := []struct {
		name  string
		extra map[string]any
		want  string
	}{
		{"absent", map[string]any{}, ""},
		{"nil map", nil, ""},
		{"explicit nil", map[string]any{"pid": nil}, ""},
		// encoding/json decodes every number as float64; an integral one has
		// to render as Python's int, not as "4240.0".
		{"json number", map[string]any{"pid": float64(4240)}, "4240"},
		{"go int", map[string]any{"pid": 4240}, "4240"},
		{"go int64", map[string]any{"pid": int64(4240)}, "4240"},
		{"string", map[string]any{"pid": "4240"}, "4240"},
		{"other keys ignored", map[string]any{"duration_s": 5400.0}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := pidHintFromExtra(c.extra); got != c.want {
				t.Errorf("pidHintFromExtra(%v) = %q, want %q", c.extra, got, c.want)
			}
		})
	}
}
