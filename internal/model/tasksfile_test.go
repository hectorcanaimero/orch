package model

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// templateFixtures are verbatim copies of the five tasks.json templates
// orch ships (see testdata/README.md).
var templateFixtures = []string{
	"base-tasks.json.tmpl",
	"python-api-tasks.json.tmpl",
	"chatbot-whatsapp-tasks.json.tmpl",
	"data-pipeline-tasks.json.tmpl",
	"nextjs-saas-tasks.json.tmpl",
}

// canonicalJSON decodes to a generic value and re-encodes it — encoding/
// json sorts map keys and drops insignificant whitespace, so two JSON
// documents that differ only in formatting or key order compare equal.
// This is the "tras normalizar" step the G1.3 brief's round-trip
// requirement calls for.
func canonicalJSON(t *testing.T, data []byte) string {
	t.Helper()
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("canonicalJSON: %v", err)
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("canonicalJSON: %v", err)
	}
	return string(b)
}

func TestLoadSaveRoundTripsTemplateFixturesLosslessly(t *testing.T) {
	for _, name := range templateFixtures {
		t.Run(name, func(t *testing.T) {
			src := filepath.Join("testdata", name)
			// #nosec G304 -- name comes from the fixed templateFixtures
			// list above, not caller input.
			original, err := os.ReadFile(src)
			if err != nil {
				t.Fatal(err)
			}

			tf, err := LoadTasksFile(src)
			if err != nil {
				t.Fatalf("LoadTasksFile: %v", err)
			}

			out := filepath.Join(t.TempDir(), "tasks.json")
			if err := SaveTasksFile(out, tf); err != nil {
				t.Fatalf("SaveTasksFile: %v", err)
			}
			// #nosec G304 -- out is t.TempDir()-based, not caller input.
			roundTripped, err := os.ReadFile(out)
			if err != nil {
				t.Fatal(err)
			}

			want := canonicalJSON(t, original)
			got := canonicalJSON(t, roundTripped)
			if want != got {
				t.Errorf("round trip changed content:\n--- original ---\n%s\n--- round-tripped ---\n%s", want, got)
			}
		})
	}
}

func TestTaskFromJSONAppliesPythonDefaults(t *testing.T) {
	raw := map[string]json.RawMessage{
		"id":    json.RawMessage(`"F0.T1"`),
		"phase": json.RawMessage(`0`),
		"title": json.RawMessage(`"Do the thing"`),
		"model": json.RawMessage(`"claude/claude-sonnet-4-6"`),
	}
	task, err := TaskFromJSON(raw)
	if err != nil {
		t.Fatalf("TaskFromJSON: %v", err)
	}

	checks := []struct {
		name string
		got  any
		want any
	}{
		{"Description", task.Description, ""},
		{"Reason", task.Reason, ""},
		{"Status", task.Status, StatusTodo},
		{"len(Dependencies)", len(task.Dependencies), 0},
		{"EstimateHours", task.EstimateHours, 0.0},
		{"len(Files)", len(task.Files), 0},
		{"SpecRef", task.SpecRef, ""},
		{"len(Comments)", len(task.Comments), 0},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	if task.Dependencies == nil {
		t.Error("Dependencies is nil, want non-nil empty slice (matches Python's `list(... or [])`)")
	}
	if task.Files == nil {
		t.Error("Files is nil, want non-nil empty slice")
	}
	if task.Comments == nil {
		t.Error("Comments is nil, want non-nil empty slice")
	}
}

func TestTaskFromJSONRequiresIDPhaseTitleModel(t *testing.T) {
	base := map[string]json.RawMessage{
		"id":    json.RawMessage(`"F0.T1"`),
		"phase": json.RawMessage(`0`),
		"title": json.RawMessage(`"Do the thing"`),
		"model": json.RawMessage(`"claude/claude-sonnet-4-6"`),
	}
	for _, key := range []string{"id", "phase", "title", "model"} {
		t.Run(key, func(t *testing.T) {
			raw := make(map[string]json.RawMessage, len(base))
			for k, v := range base {
				raw[k] = v
			}
			delete(raw, key)
			if _, err := TaskFromJSON(raw); err == nil {
				t.Errorf("TaskFromJSON with %q missing: want error, got nil", key)
			}
		})
	}
}

// TestTaskRoundTripPreservesUnknownFieldsAndOmitsAbsentComments locks in
// the two halves of the "campos desconocidos preservados" requirement: a
// key this struct doesn't model survives a Marshal, and a key the source
// never had (comments, absent from every shipped template) doesn't get
// invented on save.
func TestTaskRoundTripPreservesUnknownFieldsAndOmitsAbsentComments(t *testing.T) {
	raw := map[string]json.RawMessage{
		"id":          json.RawMessage(`"F0.T1"`),
		"phase":       json.RawMessage(`0`),
		"title":       json.RawMessage(`"Do the thing"`),
		"model":       json.RawMessage(`"claude/claude-sonnet-4-6"`),
		"futureField": json.RawMessage(`{"nested":true}`),
	}
	task, err := TaskFromJSON(raw)
	if err != nil {
		t.Fatalf("TaskFromJSON: %v", err)
	}
	out, err := json.Marshal(task)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var back map[string]json.RawMessage
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	if string(back["futureField"]) != `{"nested":true}` {
		t.Errorf("futureField = %s, want preserved verbatim", back["futureField"])
	}
	if v, ok := back["comments"]; ok {
		t.Errorf(`"comments" key present (%s) but source never had it`, v)
	}
}

// TestTaskRoundTripKeepsExplicitEmptyCommentsArray is the mirror case: a
// source that DOES write "comments": [] explicitly keeps that key on save.
func TestTaskRoundTripKeepsExplicitEmptyCommentsArray(t *testing.T) {
	raw := map[string]json.RawMessage{
		"id":       json.RawMessage(`"F0.T1"`),
		"phase":    json.RawMessage(`0`),
		"title":    json.RawMessage(`"Do the thing"`),
		"model":    json.RawMessage(`"claude/claude-sonnet-4-6"`),
		"comments": json.RawMessage(`[]`),
	}
	task, err := TaskFromJSON(raw)
	if err != nil {
		t.Fatalf("TaskFromJSON: %v", err)
	}
	out, err := json.Marshal(task)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var back map[string]json.RawMessage
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	if _, ok := back["comments"]; !ok {
		t.Error(`"comments" key absent, want kept because the source had it explicitly`)
	}
}
