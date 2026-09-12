package model

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// These two bugs were found by diffing a Go- and Python-written tasks.json
// byte for byte (G3.5's parity harness), not by checking a field's value —
// every existing test does the latter, which is exactly why both survived
// this long. See Task.MarshalJSON's doc comment.

// TestTaskMarshalJSONFieldOrderMatchesPython pins the full key order
// against orchestrator/models.py's Task dataclass declaration: id, phase,
// title, description, model, reason, status, dependencies, estimateHours,
// files, specRef, comments. Building the object via a plain
// map[string]json.RawMessage (the bug) would alphabetize it instead
// (comments, dependencies, description, estimateHours, files, id, model,
// phase, reason, specRef, status, title) — this test fails loudly on that
// regression rather than needing a human to notice a diff looks odd.
func TestTaskMarshalJSONFieldOrderMatchesPython(t *testing.T) {
	task := Task{
		ID: "F0.T1", Phase: 0, Title: "Task", Description: "desc",
		Model: "claude-sonnet-4-6", Reason: "why", Status: StatusTodo,
		Dependencies: []string{"F0.T0"}, EstimateHours: 1, Files: []string{"x.go"},
		SpecRef: "spec.md#F0.T1", Comments: []json.RawMessage{},
	}
	b, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"id", "phase", "title", "description", "model", "reason",
		"status", "dependencies", "estimateHours", "files", "specRef", "comments"}
	got := keyOrder(t, b)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("key order = %v, want %v\nfull output: %s", got, want, b)
	}
}

// TestTaskMarshalJSONEstimateHoursKeepsADecimalPoint pins the same
// int-vs-float rendering pyfmt.Float exists for everywhere else: a whole
// number still carries ".0", matching Python's plain `json.dump` of a
// float. encoding/json's default float formatting would render `1` here,
// not `1.0` — silently, only visible in a byte-for-byte diff against a
// real Python-written tasks.json.
func TestTaskMarshalJSONEstimateHoursKeepsADecimalPoint(t *testing.T) {
	cases := []struct {
		hours float64
		want  string
	}{
		{1, `"estimateHours":1.0`},
		{0.3, `"estimateHours":0.3`},
		{0, `"estimateHours":0.0`},
	}
	for _, tc := range cases {
		task := Task{ID: "x", Phase: 0, Title: "t", Model: "m", EstimateHours: tc.hours}
		b, err := json.Marshal(task)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), tc.want) {
			t.Errorf("EstimateHours=%v: output %s does not contain %q", tc.hours, b, tc.want)
		}
	}
}

// TestMetaMarshalJSONFieldOrderMatchesPython pins Meta's order: project,
// generatedAt, template, note.
func TestMetaMarshalJSONFieldOrderMatchesPython(t *testing.T) {
	meta := Meta{Project: "p", GeneratedAt: "2026-01-01", Template: "python-api", Note: "n"}
	b, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	got := keyOrder(t, b)
	want := []string{"project", "generatedAt", "template", "note"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("key order = %v, want %v\nfull output: %s", got, want, b)
	}
}

// keyOrder returns the top-level object keys of b in the order they
// appear in the byte stream — encoding/json's Decoder, used token by
// token, is the standard way to observe that order (Unmarshal into a map
// loses it).
func keyOrder(t *testing.T, b []byte) []string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(b))
	tok, err := dec.Token() // consume '{'
	if err != nil {
		t.Fatal(err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		t.Fatalf("expected an object, got %v", tok)
	}
	var keys []string
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, keyTok.(string))
		var discard json.RawMessage
		if err := dec.Decode(&discard); err != nil {
			t.Fatal(err)
		}
	}
	return keys
}
