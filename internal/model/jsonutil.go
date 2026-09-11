package model

import (
	"encoding/json"
	"fmt"
	"strings"
)

// popRaw removes and returns raw[key], reporting whether it was present.
// Used throughout this package to consume "known" wire keys one at a time
// so whatever remains in the map is exactly the set of unknown fields to
// preserve (see Task.Extra / Meta.Extra / Phase.Extra).
func popRaw(raw map[string]json.RawMessage, key string) (json.RawMessage, bool) {
	v, ok := raw[key]
	if ok {
		delete(raw, key)
	}
	return v, ok
}

func isJSONNull(v json.RawMessage) bool {
	return strings.TrimSpace(string(v)) == "null"
}

func popRequiredString(raw map[string]json.RawMessage, key string) (string, error) {
	v, ok := popRaw(raw, key)
	if !ok {
		return "", fmt.Errorf("model: missing required field %q", key)
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return "", fmt.Errorf("model: field %q: %w", key, err)
	}
	return s, nil
}

func popRequiredInt(raw map[string]json.RawMessage, key string) (int, error) {
	v, ok := popRaw(raw, key)
	if !ok {
		return 0, fmt.Errorf("model: missing required field %q", key)
	}
	var n int
	if err := json.Unmarshal(v, &n); err != nil {
		return 0, fmt.Errorf("model: field %q: %w", key, err)
	}
	return n, nil
}

// popOptionalString mirrors Python's `raw.get(key, default)`: absent or
// explicitly null both fall back to def. (Python's literal `.get` returns
// None rather than def for an explicit null, storing None in a
// str-typed field — a type-hint violation Python doesn't enforce at
// runtime. No shipped tasks.json does this; Go treats null as "not set"
// instead of reproducing that corner case.)
func popOptionalString(raw map[string]json.RawMessage, key, def string) (string, error) {
	v, ok := popRaw(raw, key)
	if !ok || isJSONNull(v) {
		return def, nil
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return "", fmt.Errorf("model: field %q: %w", key, err)
	}
	return s, nil
}

// popStatus is popOptionalString's counterpart for the `status` field,
// which defaults to a non-empty Status rather than "".
func popStatus(raw map[string]json.RawMessage, key string, def Status) (Status, error) {
	v, ok := popRaw(raw, key)
	if !ok || isJSONNull(v) {
		return def, nil
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return "", fmt.Errorf("model: field %q: %w", key, err)
	}
	return Status(s), nil
}

// popFalsyDefaultFloat mirrors Python's `float(raw.get(key, 0) or 0)`:
// missing, null, or exactly 0 all end up as 0.0.
func popFalsyDefaultFloat(raw map[string]json.RawMessage, key string) (float64, error) {
	v, ok := popRaw(raw, key)
	if !ok {
		return 0, nil
	}
	var f float64
	if err := json.Unmarshal(v, &f); err != nil {
		return 0, fmt.Errorf("model: field %q: %w", key, err)
	}
	return f, nil
}

// popStringSlice mirrors Python's `list(raw.get(key, []) or [])`: missing
// or null both become an empty (non-nil) slice; a present array is kept
// verbatim, including when it's empty.
func popStringSlice(raw map[string]json.RawMessage, key string) ([]string, error) {
	v, ok := popRaw(raw, key)
	if !ok {
		return []string{}, nil
	}
	var s []string
	if err := json.Unmarshal(v, &s); err != nil {
		return nil, fmt.Errorf("model: field %q: %w", key, err)
	}
	if s == nil {
		s = []string{}
	}
	return s, nil
}

// popRawMessageSlice is popStringSlice's counterpart for fields whose
// entries are arbitrary JSON values (Task.Comments).
func popRawMessageSlice(raw map[string]json.RawMessage, key string) ([]json.RawMessage, error) {
	v, ok := popRaw(raw, key)
	if !ok {
		return []json.RawMessage{}, nil
	}
	var s []json.RawMessage
	if err := json.Unmarshal(v, &s); err != nil {
		return nil, fmt.Errorf("model: field %q: %w", key, err)
	}
	if s == nil {
		s = []json.RawMessage{}
	}
	return s, nil
}
