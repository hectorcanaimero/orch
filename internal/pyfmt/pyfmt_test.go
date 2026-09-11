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

// Same provenance as the Quote table: `python3 -c "print(f'{1234:,}')"`.
func TestCommasMatchesPythonThousandsSeparator(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0"},
		{1, "1"},
		{12, "12"},
		{123, "123"},
		// The group boundary from every side: three digits take no comma,
		// four take one, and an exact multiple of 1000 must not lose zeros.
		{999, "999"},
		{1000, "1,000"},
		{1234, "1,234"},
		{12345, "12,345"},
		{123456, "123,456"},
		{1234567, "1,234,567"},
		{1000000, "1,000,000"},
		{73300, "73,300"},
		// Negatives keep the sign outside the grouping.
		{-1, "-1"},
		{-1234, "-1,234"},
		{-1234567, "-1,234,567"},
		// The extremes, because the leading-group arithmetic is the part most
		// likely to slice out of range.
		{9223372036854775807, "9,223,372,036,854,775,807"},
		{-9223372036854775808, "-9,223,372,036,854,775,808"},
	}
	for _, c := range cases {
		if got := Commas(c.in); got != c.want {
			t.Errorf("Commas(%d) = %q, python prints %q", c.in, got, c.want)
		}
	}
}
