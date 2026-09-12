package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

// orderedJSON builds a JSON object with keys emitted in the exact order
// they were added.
//
// `encoding/json` always sorts a plain `map[string]json.RawMessage`'s keys
// alphabetically when marshaling it — Task.MarshalJSON and Meta.MarshalJSON
// used to build exactly that kind of map, so tasks.json came out of Go
// sorted (`comments`, `dependencies`, `description`, ... `title`) instead
// of in the field order both Python's dataclass and this struct's own
// declaration use (`id`, `phase`, `title`, `model`, ...). Found by actually
// diffing a Go- and Python-written tasks.json byte for byte (G3.5's parity
// harness) rather than checking individual fields, which is why it went
// unnoticed this long: every existing test asserted a field's VALUE, never
// the file's own key order, and Phase's two fields ("id" < "name") happen
// to sort the same as they're declared.
type orderedJSON struct {
	keys   []string
	values map[string]json.RawMessage
}

func newOrderedJSON() *orderedJSON {
	return &orderedJSON{values: map[string]json.RawMessage{}}
}

// set marshals v and records it under key, in insertion order. Setting the
// same key twice keeps its original position (matching a Python dict).
func (o *orderedJSON) set(key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("model: marshal field %q: %w", key, err)
	}
	return o.setRaw(key, b)
}

func (o *orderedJSON) setRaw(key string, raw json.RawMessage) error {
	if _, exists := o.values[key]; !exists {
		o.keys = append(o.keys, key)
	}
	o.values[key] = raw
	return nil
}

// sortedExtraKeys returns extra's keys sorted, so an Extra map's own
// contribution to a marshaled object has a deterministic order — Go map
// iteration is randomized, and without this a round-tripped file with
// genuinely unknown keys would reorder them between runs for no reason.
func sortedExtraKeys(extra map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (o *orderedJSON) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, fmt.Errorf("model: marshal key %q: %w", k, err)
		}
		b.Write(kb)
		b.WriteByte(':')
		b.Write(o.values[k])
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}
