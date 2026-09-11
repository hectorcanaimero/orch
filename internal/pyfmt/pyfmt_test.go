package pyfmt

import "testing"

// Every expectation below is what CPython printed for the same input:
//
//	python3 -c "print(repr(repr(\"it's\")))"
//
// rather than what the implementation happens to do. The two are only the same
// while the port is correct, which is the whole point of writing them down.
func TestQuoteMatchesPythonRepr(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"claude", `'claude'`},
		{"", `''`},
		// A single quote inside flips the outer quotes to double, and the
		// inner quote is then NOT escaped — Python does not escape what it
		// does not have to.
		{"it's", `"it's"`},
		// A double quote alone does not flip anything.
		{`say "hi"`, `'say "hi"'`},
		// Both kinds present: single quotes win and the inner one is
		// escaped. This is the case router's copy of this function gets
		// wrong.
		{`both ' and "`, `'both \' and "'`},
		{`back\slash`, `'back\\slash'`},
		{"tab\ttab", `'tab\ttab'`},
		{"nl\nnl", `'nl\nnl'`},
		{"cr\rcr", `'cr\rcr'`},
		// Printable non-ASCII survives: Python 3's repr does not escape it,
		// so neither may a byte-wise copy.
		{"ñ", `'ñ'`},
	}
	for _, c := range cases {
		if got := Quote(c.in); got != c.want {
			t.Errorf("Quote(%q) = %q, python's repr() gives %q", c.in, got, c.want)
		}
	}
}

// The double-quoted branch has to escape backslashes too. Python:
//
//	>>> repr("it's a back\\slash")
//	'"it\'s a back\\\\slash"'   # i.e. the text: "it's a back\\slash"
func TestQuoteEscapesBackslashInsideDoubleQuotes(t *testing.T) {
	got := Quote(`it's a back\slash`)
	want := `"it's a back\\slash"`
	if got != want {
		t.Errorf("Quote() = %q, want %q", got, want)
	}
}
