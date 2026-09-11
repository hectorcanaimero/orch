// Package pyfmt reproduces the Python string formatting that orch's
// user-visible messages were built on.
//
// These are not cosmetic helpers. `scripts/parity.sh` diffs the two binaries'
// output line for line, so a message that says `"claude"` where Python said
// `'claude'` is a failure. Go's own verbs do not produce Python's form: `%q`
// writes double quotes with Go's escaping rules.
//
// The package exists because the same function was about to be written a
// third time. `internal/graph` and `internal/router` each carried a private
// `pyQuote`, and the two copies did not agree: router's skipped the backslash
// escape in its double-quoted branch and the control characters in both, so
// the same input rendered two ways depending on which package formatted it.
// `repr("it's a back\slash")` is `"it's a back\\slash"` in Python and
// graph, and `"it's a back\slash"` in router. Neither package's tests covered
// it. Quote below is graph's — the correct one — and both packages now call
// it.
//
// Python's `repr(float)` is emulated separately, in `internal/state`, because
// it is 150 lines with its own vector of values CPython actually printed.
// When a second package needs it, it belongs here.
package pyfmt

import "strings"

// Quote renders a string the way Python's `{x!r}` does for the values that
// reach orch's messages — task ids, model names, provider and preset names.
//
// Python's repr prefers single quotes and switches to double only when the
// value contains a single quote and no double quote. Those names are
// `[A-Za-z0-9._/-]` in practice, so the simple case is the only one that
// happens — but the rule is implemented rather than assumed, because every
// one of those values comes out of a file a user edits.
func Quote(s string) string {
	hasSingle := strings.Contains(s, "'")
	hasDouble := strings.Contains(s, `"`)

	if hasSingle && !hasDouble {
		return `"` + escapeInner(s, '"') + `"`
	}
	return "'" + escapeInner(s, '\'') + "'"
}

// escapeInner escapes backslashes, the quote character in use, and the
// control characters Python's repr escapes.
func escapeInner(s string, quote byte) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\\':
			b.WriteString(`\\`)
		case quote:
			b.WriteByte('\\')
			b.WriteByte(c)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}
