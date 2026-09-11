package providers

import (
	"encoding/json"
	"testing"
)

func TestDecodeObject(t *testing.T) {
	tests := []struct {
		name  string
		input string
		ok    bool
	}{
		{name: "an object", input: `{"a":1}`, ok: true},
		{name: "an empty object", input: `{}`, ok: true},
		{name: "an object with whitespace around it", input: "  {\"a\":1}\n", ok: true},
		{name: "an array", input: `[1,2]`},
		{name: "a number", input: `42`},
		{name: "a string", input: `"hi"`},
		{name: "null", input: `null`},
		{name: "empty", input: ``},
		{name: "not json", input: `nope`},
		{name: "half an object", input: `{"a":`},
		{name: "trailing data is Python's Extra data error", input: `{"a":1} {"b":2}`},
		{name: "trailing junk after an object", input: `{"a":1} nope`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ok := decodeObject([]byte(tt.input))
			if ok != tt.ok {
				t.Errorf("ok = %v, want %v", ok, tt.ok)
			}
		})
	}
}

func TestDecodeObjectKeepsNumberLiterals(t *testing.T) {
	obj, ok := decodeObject([]byte(`{"int":404,"float":404.0,"big":12345678901234567890}`))
	if !ok {
		t.Fatal("want an object")
	}
	// json.Number preserves what the CLI wrote, which is the only way to tell
	// Python's str(404) from str(404.0) after the fact.
	for key, want := range map[string]string{
		"int":   "404",
		"float": "404.0",
		"big":   "12345678901234567890",
	} {
		n, ok := obj[key].(json.Number)
		if !ok {
			t.Errorf("%s is %T, want json.Number", key, obj[key])
			continue
		}
		if n.String() != want {
			t.Errorf("%s = %q, want %q", key, n.String(), want)
		}
	}
}

func TestJSONLEvents(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string // the "type" of each surviving event
	}{
		{name: "empty", input: ""},
		{name: "blank lines only", input: "\n\n  \n"},
		{
			name:  "two events",
			input: "{\"type\":\"a\"}\n{\"type\":\"b\"}\n",
			want:  []string{"a", "b"},
		},
		{
			name:  "blank lines between events are skipped",
			input: "{\"type\":\"a\"}\n\n\n{\"type\":\"b\"}",
			want:  []string{"a", "b"},
		},
		{
			name:  "CRLF line endings",
			input: "{\"type\":\"a\"}\r\n{\"type\":\"b\"}\r\n",
			want:  []string{"a", "b"},
		},
		{
			name:  "a truncated final line costs only itself",
			input: "{\"type\":\"a\"}\n{\"type\":\"trunc",
			want:  []string{"a"},
		},
		{
			name:  "an unparseable line in the middle is skipped",
			input: "{\"type\":\"a\"}\nnot json\n{\"type\":\"b\"}",
			want:  []string{"a", "b"},
		},
		{
			name:  "non-object lines are skipped",
			input: "[1,2]\n42\n\"str\"\n{\"type\":\"a\"}",
			want:  []string{"a"},
		},
		{
			name:  "an event with no type still counts",
			input: `{"no":"type"}`,
			want:  []string{""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events := jsonlEvents([]byte(tt.input))
			if len(events) != len(tt.want) {
				t.Fatalf("got %d events, want %d", len(events), len(tt.want))
			}
			for i, want := range tt.want {
				if got := events[i].eventType(); got != want {
					t.Errorf("event[%d].type = %q, want %q", i, got, want)
				}
			}
		})
	}
}

// TestJSONLEventsHandlesLongLines guards the reason this function does not use
// bufio.Scanner: a single event carrying a large tool result exceeds Scanner's
// 64 KiB default, and dropping it would be exactly the silent data loss
// jsonlEvents exists to avoid.
func TestJSONLEventsHandlesLongLines(t *testing.T) {
	big := make([]byte, 0, 300*1024)
	big = append(big, `{"type":"item.completed","item":{"type":"agent_message","text":"`...)
	for i := 0; i < 256*1024; i++ {
		big = append(big, 'x')
	}
	big = append(big, `"}}`...)
	big = append(big, '\n')
	big = append(big, `{"type":"turn.completed","usage":{"input_tokens":7,"output_tokens":8}}`...)

	events := jsonlEvents(big)
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2 — a long line was dropped", len(events))
	}
	_, in, out := sumCodexUsage(events)
	if in != 7 || out != 8 {
		t.Errorf("usage = (%d,%d), want (7,8)", in, out)
	}
}

func TestJSONLEventsKeepsRawLine(t *testing.T) {
	events := jsonlEvents([]byte("  {\"type\":\"error\"}  \n"))
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	// Trimmed, but otherwise the bytes the CLI wrote — codexErrorMessage
	// falls back to this.
	if events[0].raw != `{"type":"error"}` {
		t.Errorf("raw = %q", events[0].raw)
	}
}

func TestPyTruthy(t *testing.T) {
	tests := []struct {
		name string
		v    any
		want bool
	}{
		{name: "nil", v: nil},
		{name: "false", v: false},
		{name: "true", v: true, want: true},
		{name: "zero", v: json.Number("0")},
		{name: "zero as a float literal", v: json.Number("0.0")},
		{name: "a number", v: json.Number("404"), want: true},
		{name: "a negative number", v: json.Number("-1"), want: true},
		{name: "an unparseable number", v: json.Number("nope")},
		{name: "an empty string", v: ""},
		{name: "a string", v: "x", want: true},
		{name: "an empty list", v: []any{}},
		{name: "a list", v: []any{1}, want: true},
		{name: "an empty object", v: map[string]any{}},
		{name: "an object", v: map[string]any{"a": 1}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pyTruthy(tt.v); got != tt.want {
				t.Errorf("pyTruthy(%#v) = %v, want %v", tt.v, got, tt.want)
			}
		})
	}
}

func TestPyStr(t *testing.T) {
	tests := []struct {
		name string
		v    any
		want string
	}{
		{name: "nil", v: nil, want: "None"},
		{name: "true", v: true, want: "True"},
		{name: "false", v: false, want: "False"},
		{name: "an integer literal", v: json.Number("404"), want: "404"},
		{name: "a float literal keeps its written form", v: json.Number("404.0"), want: "404.0"},
		{name: "a string is itself, unquoted", v: "boom", want: "boom"},
		{name: "an empty string", v: "", want: ""},
		{name: "an object falls back to JSON", v: map[string]any{"code": "E"}, want: `{"code":"E"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pyStr(tt.v); got != tt.want {
				t.Errorf("pyStr(%#v) = %q, want %q", tt.v, got, tt.want)
			}
		})
	}
}

func TestToFloat(t *testing.T) {
	tests := []struct {
		name string
		v    any
		want float64
	}{
		{name: "nil is zero", v: nil},
		{name: "a number", v: json.Number("0.0285802"), want: 0.0285802},
		{name: "an integer", v: json.Number("5"), want: 5},
		{name: "a numeric string", v: "0.25", want: 0.25},
		{name: "a numeric string with spaces", v: " 0.25 ", want: 0.25},
		{name: "a non-numeric string is zero, where Python would raise", v: "nope"},
		{name: "true is one", v: true, want: 1},
		{name: "false is zero", v: false},
		{name: "an object is zero", v: map[string]any{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := toFloat(tt.v); got != tt.want {
				t.Errorf("toFloat(%#v) = %v, want %v", tt.v, got, tt.want)
			}
		})
	}
}

func TestToInt(t *testing.T) {
	tests := []struct {
		name string
		v    any
		want int
	}{
		{name: "nil is zero", v: nil},
		{name: "an integer", v: json.Number("1234"), want: 1234},
		{name: "a float truncates toward zero, as int() does", v: json.Number("12.9"), want: 12},
		{name: "a negative float truncates toward zero too", v: json.Number("-12.9"), want: -12},
		{name: "a numeric string", v: "7", want: 7},
		{name: "a float-shaped string is zero, where Python would raise", v: "7.5"},
		{name: "a non-numeric string is zero", v: "nope"},
		{name: "true is one", v: true, want: 1},
		{name: "an object is zero", v: map[string]any{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := toInt(tt.v); got != tt.want {
				t.Errorf("toInt(%#v) = %d, want %d", tt.v, got, tt.want)
			}
		})
	}
}

// TestPyFloat is the same golden table internal/state's pyFloat carries, kept
// here because claude's --max-budget-usd value is built with Python's
// str(float) and the two binaries must write the same flag.
func TestPyFloat(t *testing.T) {
	tests := []struct {
		v    float64
		want string
	}{
		{0, "0.0"},
		{1, "1.0"},
		{5, "5.0"},
		{12, "12.0"},
		{0.5, "0.5"},
		{2.5, "2.5"},
		{0.0285802, "0.0285802"},
		{5400, "5400.0"},
		{-1, "-1.0"},
		{-0.25, "-0.25"},
		{0.1, "0.1"},
		{1e16, "1e+16"},
		{1e21, "1e+21"},
		{1.5e21, "1.5e+21"},
		{0.0001, "0.0001"},
		{0.00001, "1e-05"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := pyFloat(tt.v); got != tt.want {
				t.Errorf("pyFloat(%v) = %q, want %q", tt.v, got, tt.want)
			}
		})
	}
}

func TestPyFloatNonFinite(t *testing.T) {
	if got := pyFloat(negZero()); got != "-0.0" {
		t.Errorf("pyFloat(-0.0) = %q, want %q", got, "-0.0")
	}
}

func negZero() float64 {
	z := 0.0
	return -z
}

func TestAsStringAndAsObject(t *testing.T) {
	if got := asString("x"); got != "x" {
		t.Errorf("asString(string) = %q", got)
	}
	if got := asString(json.Number("1")); got != "" {
		t.Errorf("asString(non-string) = %q, want empty", got)
	}
	if got := asString(nil); got != "" {
		t.Errorf("asString(nil) = %q, want empty", got)
	}
	if _, ok := asObject(map[string]any{}); !ok {
		t.Error("asObject(map) must succeed")
	}
	if _, ok := asObject("nope"); ok {
		t.Error("asObject(string) must fail")
	}
	if _, ok := asObject(nil); ok {
		t.Error("asObject(nil) must fail")
	}
}
