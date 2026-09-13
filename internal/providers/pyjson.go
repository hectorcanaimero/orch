package providers

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// This file is the small layer of Python semantics the ported parsers need.
//
// dispatcher.py reads CLI output with `json.loads` into Python dicts and then
// leans on Python's own rules: truthiness decides whether `is_error` is set,
// `str()` decides what an error message looks like, `float()`/`int()` coerce
// whatever the CLI put in a field. Go's json package gives none of that, so
// the rules are spelled out here once and tested once, rather than
// approximated at each of a dozen call sites.
//
// Every decode below uses json.Decoder.UseNumber, which keeps a number as the
// literal text the CLI wrote. That matters for pyStr: Python's `str(404)` is
// "404" and `str(404.0)` is "404.0", and only the original literal can tell
// those apart after the fact.

// decodeObject parses b as a single JSON value and returns it when that value
// is an object, mirroring `obj = json.loads(s); isinstance(obj, dict)`.
//
// Trailing non-whitespace content is rejected, because json.loads raises
// "Extra data" on it. Go's Unmarshal would silently accept some of those.
func decodeObject(b []byte) (map[string]any, bool) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	// Anything after the value but whitespace is Python's "Extra data".
	// Checked against the input offset rather than by decoding again: a
	// second Decode reports an error both for trailing garbage and for a
	// clean end of input, which cannot tell those two apart.
	if strings.TrimSpace(string(b[dec.InputOffset():])) != "" {
		return nil, false
	}
	obj, ok := v.(map[string]any)
	return obj, ok
}

// event is one decoded JSONL line plus the bytes it came from. The raw line is
// kept because codex's last-resort error message is Python's `str(ev)` — a
// dict repr. Echoing the line the CLI actually wrote is both closer to useful
// and far more testable than reimplementing CPython's dict formatting.
type event struct {
	obj map[string]any
	raw string
}

// eventType returns the event's "type" field when it is a string.
func (e event) eventType() string { return asString(e.obj["type"]) }

// jsonlEvents yields every line of b that parses as a JSON object.
//
// Lines that do not parse are skipped silently, exactly as
// `_iter_jsonl_events` does. Two real captures depend on that: a SIGKILLed
// codex leaves a half-written final line, and an unauthenticated codex
// interleaves plain-text `ERROR codex_api::…` lines from stderr among the
// events (testdata/codex/0.154.0/auth-error.jsonl).
//
// Split by hand rather than with bufio.Scanner: a single JSONL event carrying
// a large tool result can exceed Scanner's 64 KiB default and would be
// dropped, which is the same silent data loss this function exists to avoid.
func jsonlEvents(b []byte) []event {
	lines := strings.Split(string(b), "\n")
	events := make([]event, 0, len(lines))
	for _, line := range lines {
		// Python strips the full whitespace set; \r matters for CRLF logs.
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		obj, ok := decodeObject([]byte(line))
		if !ok {
			continue
		}
		events = append(events, event{obj: obj, raw: line})
	}
	return events
}

// pyTruthy implements Python's `bool(x)` for a value decoded from JSON, which
// is how dispatcher.py reads `is_error` and how `a or b` chains pick a branch.
func pyTruthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case json.Number:
		f, err := x.Float64()
		return err == nil && f != 0
	case string:
		return x != ""
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	default:
		return true
	}
}

// pyStr implements Python's `str(x)` for a JSON-decoded scalar.
//
// Only the scalar cases arise in the ported code: every call site either
// handles objects itself first or is guarded by pyTruthy, so a list or a dict
// never reaches here. Should one ever start to, the raw JSON is a more honest
// answer than a half-right imitation of Python's repr.
func pyStr(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case bool:
		if x {
			return "True"
		}
		return "False"
	case json.Number:
		// The literal the CLI wrote. For an integer literal this is exactly
		// Python's str(int); for a float literal it is the literal rather
		// than repr(float), which differ only for exotic spellings like
		// "1e3" (Python would say "1000.0"). No CLI writes those.
		return x.String()
	case string:
		return x
	default:
		if b, err := json.Marshal(x); err == nil {
			return string(b)
		}
		return ""
	}
}

// asString returns v when it is a string, else "". Mirrors the many
// `ev.get("type") == "..."` comparisons, which in Python are simply false for
// a non-string value rather than an error.
func asString(v any) string {
	s, _ := v.(string)
	return s
}

// asObject returns v when it is a JSON object, mirroring `isinstance(x, dict)`.
func asObject(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok
}

// toFloat coerces a JSON value the way `float(x or 0.0)` does, returning 0 for
// anything Python would refuse.
//
// Python raises ValueError on an uncoercible value and dispatcher.py does not
// catch it in claude's extract_cost, so malformed CLI output can crash the
// Python reap loop there. Returning 0 instead is a deliberate divergence:
// well-formed output behaves identically, and malformed output costs a wrong
// cost number rather than a dead orchestrator. Noted in
// docs/brainstorm/go-migration-notes.md.
func toFloat(v any) float64 {
	switch x := v.(type) {
	case json.Number:
		f, err := x.Float64()
		if err != nil {
			return 0
		}
		return f
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		if err != nil {
			return 0
		}
		return f
	case bool:
		if x {
			return 1
		}
		return 0
	default:
		return 0
	}
}

// toInt coerces a JSON value the way `int(x or 0)` does. Floats truncate
// toward zero, as Python's int() does; see toFloat on the uncoercible case.
func toInt(v any) int {
	switch x := v.(type) {
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return int(i)
		}
		f, err := x.Float64()
		if err != nil {
			return 0
		}
		return int(math.Trunc(f))
	case string:
		i, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		if err != nil {
			return 0
		}
		return int(i)
	case bool:
		if x {
			return 1
		}
		return 0
	default:
		return 0
	}
}
