package model

import (
	"encoding/json"
	"fmt"
)

// Task is a single unit of work from tasks.json. Field names match the
// frozen dataclass in orchestrator/models.py; JSON wire keys are handled
// by TaskFromJSON/MarshalJSON below rather than plain struct tags, because
// SaveTasksFile must reproduce Python's exact from_json defaulting AND
// must not invent a key (e.g. "comments") that the source file never had
// — see the `present` field.
type Task struct {
	ID            string
	Phase         int
	Title         string
	Description   string
	Model         string
	Reason        string
	Status        Status
	Dependencies  []string
	EstimateHours float64
	Files         []string
	SpecRef       string
	// Comments holds each entry's raw JSON value verbatim —
	// orchestrator/models.py types this `list[dict]` with no fixed shape,
	// so Go doesn't need to understand it to preserve it losslessly.
	Comments []json.RawMessage

	// Extra holds JSON object keys this struct doesn't know about, so a
	// tasks.json field added later (or by a newer/older Python version)
	// survives a Go Load→Save round trip untouched.
	Extra map[string]json.RawMessage

	// present records which OPTIONAL wire keys existed in the source this
	// Task was decoded from, so MarshalJSON reproduces the same key set
	// instead of adding one the source never had (every shipped
	// tasks.json omits "comments" entirely rather than writing "[]").
	// nil means "not decoded from JSON" (built directly in Go code) —
	// every optional field is then always emitted.
	present map[string]bool
}

// optionalTaskKeys are the wire keys tasks.json may omit; from_json in
// orchestrator/models.py defaults each one when missing. id/phase/title/
// model have no Python default — from_json raises KeyError, so
// TaskFromJSON returns an error instead of defaulting them.
var optionalTaskKeys = []string{
	"description", "reason", "status", "dependencies", "estimateHours",
	"files", "specRef", "comments",
}

// TaskFromJSON builds a Task from one decoded tasks.json row, applying the
// exact defaults Task.from_json (orchestrator/models.py) applies.
func TaskFromJSON(raw map[string]json.RawMessage) (Task, error) {
	var t Task

	id, err := popRequiredString(raw, "id")
	if err != nil {
		return Task{}, err
	}
	phase, err := popRequiredInt(raw, "phase")
	if err != nil {
		return Task{}, err
	}
	title, err := popRequiredString(raw, "title")
	if err != nil {
		return Task{}, err
	}
	model, err := popRequiredString(raw, "model")
	if err != nil {
		return Task{}, err
	}
	t.ID, t.Phase, t.Title, t.Model = id, phase, title, model

	t.present = make(map[string]bool, len(optionalTaskKeys))
	for _, key := range optionalTaskKeys {
		if _, ok := raw[key]; ok {
			t.present[key] = true
		}
	}

	if t.Description, err = popOptionalString(raw, "description", ""); err != nil {
		return Task{}, err
	}
	if t.Reason, err = popOptionalString(raw, "reason", ""); err != nil {
		return Task{}, err
	}
	if t.Status, err = popStatus(raw, "status", StatusTodo); err != nil {
		return Task{}, err
	}
	if t.Dependencies, err = popStringSlice(raw, "dependencies"); err != nil {
		return Task{}, err
	}
	if t.EstimateHours, err = popFalsyDefaultFloat(raw, "estimateHours"); err != nil {
		return Task{}, err
	}
	if t.Files, err = popStringSlice(raw, "files"); err != nil {
		return Task{}, err
	}
	if t.SpecRef, err = popOptionalString(raw, "specRef", ""); err != nil {
		return Task{}, err
	}
	if t.Comments, err = popRawMessageSlice(raw, "comments"); err != nil {
		return Task{}, err
	}

	t.Extra = raw // whatever's left is unknown to this struct
	return t, nil
}

// MarshalJSON reproduces the wire key set the Task was decoded with
// (t.present == nil, i.e. built in Go rather than decoded, emits every
// optional field).
func (t Task) MarshalJSON() ([]byte, error) {
	out := map[string]json.RawMessage{}
	for k, v := range t.Extra {
		out[k] = v
	}
	must := func(key string, v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("model: marshal task field %q: %w", key, err)
		}
		out[key] = b
		return nil
	}
	include := func(key string) bool {
		return t.present == nil || t.present[key]
	}

	if err := must("id", t.ID); err != nil {
		return nil, err
	}
	if err := must("phase", t.Phase); err != nil {
		return nil, err
	}
	if err := must("title", t.Title); err != nil {
		return nil, err
	}
	if err := must("model", t.Model); err != nil {
		return nil, err
	}
	if include("description") {
		if err := must("description", t.Description); err != nil {
			return nil, err
		}
	}
	if include("reason") {
		if err := must("reason", t.Reason); err != nil {
			return nil, err
		}
	}
	if include("status") {
		if err := must("status", t.Status); err != nil {
			return nil, err
		}
	}
	if include("dependencies") {
		deps := t.Dependencies
		if deps == nil {
			deps = []string{}
		}
		if err := must("dependencies", deps); err != nil {
			return nil, err
		}
	}
	if include("estimateHours") {
		if err := must("estimateHours", t.EstimateHours); err != nil {
			return nil, err
		}
	}
	if include("files") {
		files := t.Files
		if files == nil {
			files = []string{}
		}
		if err := must("files", files); err != nil {
			return nil, err
		}
	}
	if include("specRef") {
		if err := must("specRef", t.SpecRef); err != nil {
			return nil, err
		}
	}
	if include("comments") {
		comments := t.Comments
		if comments == nil {
			comments = []json.RawMessage{}
		}
		if err := must("comments", comments); err != nil {
			return nil, err
		}
	}
	return json.Marshal(out)
}

// UnmarshalJSON lets Task decode directly (e.g. as a TasksFile.Tasks
// element), delegating to TaskFromJSON for the actual defaulting logic.
func (t *Task) UnmarshalJSON(data []byte) error {
	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	parsed, err := TaskFromJSON(raw)
	if err != nil {
		return err
	}
	*t = parsed
	return nil
}
