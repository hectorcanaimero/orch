package model

import (
	"encoding/json"
	"fmt"
	"os"
)

// Meta is tasks.json's top-level `meta` object. `Template` is only present
// when the project was scaffolded from a named template (see
// orchestrator/templates/projects/*/tasks.json.tmpl vs the bare
// orchestrator/templates/tasks.json.tmpl skeleton, which omits it).
type Meta struct {
	Project     string
	GeneratedAt string
	Template    string
	Note        string
	Extra       map[string]json.RawMessage
	present     map[string]bool
}

var optionalMetaKeys = []string{"project", "generatedAt", "template", "note"}

func metaFromJSON(raw map[string]json.RawMessage) (Meta, error) {
	var m Meta
	m.present = make(map[string]bool, len(optionalMetaKeys))
	for _, key := range optionalMetaKeys {
		if _, ok := raw[key]; ok {
			m.present[key] = true
		}
	}

	var err error
	if m.Project, err = popOptionalString(raw, "project", ""); err != nil {
		return Meta{}, err
	}
	if m.GeneratedAt, err = popOptionalString(raw, "generatedAt", ""); err != nil {
		return Meta{}, err
	}
	if m.Template, err = popOptionalString(raw, "template", ""); err != nil {
		return Meta{}, err
	}
	if m.Note, err = popOptionalString(raw, "note", ""); err != nil {
		return Meta{}, err
	}
	m.Extra = raw
	return m, nil
}

// MarshalJSON preserves both Python's field order (project, generatedAt,
// template, note) and, for a decoded Meta, its own present-keys set — see
// Task.MarshalJSON's doc comment for why order is part of the contract
// here, not cosmetic.
func (m Meta) MarshalJSON() ([]byte, error) {
	out := newOrderedJSON()
	for _, k := range sortedExtraKeys(m.Extra) {
		if err := out.setRaw(k, m.Extra[k]); err != nil {
			return nil, err
		}
	}
	include := func(key string) bool { return m.present == nil || m.present[key] }
	if include("project") {
		if err := out.set("project", m.Project); err != nil {
			return nil, err
		}
	}
	if include("generatedAt") {
		if err := out.set("generatedAt", m.GeneratedAt); err != nil {
			return nil, err
		}
	}
	if include("template") {
		if err := out.set("template", m.Template); err != nil {
			return nil, err
		}
	}
	if include("note") {
		if err := out.set("note", m.Note); err != nil {
			return nil, err
		}
	}
	return json.Marshal(out)
}

func (m *Meta) UnmarshalJSON(data []byte) error {
	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	parsed, err := metaFromJSON(raw)
	if err != nil {
		return err
	}
	*m = parsed
	return nil
}

// Phase is one entry in tasks.json's top-level `phases` array.
type Phase struct {
	ID    int
	Name  string
	Extra map[string]json.RawMessage
}

// MarshalJSON preserves Python's field order (id, name) — see
// Task.MarshalJSON's doc comment. Invisible today ("id" < "name"
// alphabetically too), but not guaranteed to stay that way, and the other
// two structs in this file prove alphabetical-by-accident isn't a property
// worth relying on.
func (p Phase) MarshalJSON() ([]byte, error) {
	out := newOrderedJSON()
	for _, k := range sortedExtraKeys(p.Extra) {
		if err := out.setRaw(k, p.Extra[k]); err != nil {
			return nil, err
		}
	}
	if err := out.set("id", p.ID); err != nil {
		return nil, fmt.Errorf("model: marshal phase field \"id\": %w", err)
	}
	if err := out.set("name", p.Name); err != nil {
		return nil, fmt.Errorf("model: marshal phase field \"name\": %w", err)
	}
	return json.Marshal(out)
}

func (p *Phase) UnmarshalJSON(data []byte) error {
	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	id, err := popRequiredInt(raw, "id")
	if err != nil {
		return err
	}
	name, err := popRequiredString(raw, "name")
	if err != nil {
		return err
	}
	p.ID, p.Name, p.Extra = id, name, raw
	return nil
}

// TasksFile is the full contents of a project's tasks.json.
type TasksFile struct {
	Meta   Meta
	Phases []Phase
	Tasks  []Task
}

func (f TasksFile) MarshalJSON() ([]byte, error) {
	type wire struct {
		Meta   Meta    `json:"meta"`
		Phases []Phase `json:"phases"`
		Tasks  []Task  `json:"tasks"`
	}
	phases := f.Phases
	if phases == nil {
		phases = []Phase{}
	}
	tasks := f.Tasks
	if tasks == nil {
		tasks = []Task{}
	}
	return json.Marshal(wire{Meta: f.Meta, Phases: phases, Tasks: tasks})
}

func (f *TasksFile) UnmarshalJSON(data []byte) error {
	type wire struct {
		Meta   Meta    `json:"meta"`
		Phases []Phase `json:"phases"`
		Tasks  []Task  `json:"tasks"`
	}
	var w wire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	f.Meta, f.Phases, f.Tasks = w.Meta, w.Phases, w.Tasks
	return nil
}

// LoadTasksFile reads and parses a tasks.json file.
func LoadTasksFile(path string) (TasksFile, error) {
	// #nosec G304 -- path is a caller-supplied project file path, the
	// normal shape of a CLI tool's file I/O, not untrusted input.
	data, err := os.ReadFile(path)
	if err != nil {
		return TasksFile{}, fmt.Errorf("model: read %s: %w", path, err)
	}
	var tf TasksFile
	if err := json.Unmarshal(data, &tf); err != nil {
		return TasksFile{}, fmt.Errorf("model: parse %s: %w", path, err)
	}
	return tf, nil
}

// SaveTasksFile writes f to path as 2-space indented JSON (matching the
// shipped templates), creating the file if needed.
func SaveTasksFile(path string, f TasksFile) error {
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("model: marshal tasks file: %w", err)
	}
	data = append(data, '\n')
	// #nosec G306 -- tasks.json is a plain, git-tracked file meant to be
	// read and edited directly (see the "note" every template writes into
	// it), not a secret that wants 0600.
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("model: write %s: %w", path, err)
	}
	return nil
}
