// Package pyfmt reproduces the Python string formatting that orch's
// user-visible messages were built on.
//
// These are not cosmetic helpers. `scripts/parity.sh` diffs the two binaries'
// output line for line, so a message that says `"claude"` where Python said
// `'claude'` is a failure, and a cap printed `80000` where Python printed
// `80,000` is another. Go's own verbs produce neither form: `%q` writes double
// quotes with Go's escaping rules, and `%d` has no thousands separator.
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
// Python's `repr(float)` is here too, in float.go. It started in
// `internal/state`, which needs it for the spend dedup preimage; it moved when
// `internal/providers` turned out to need the same thing for claude's
// `--max-budget-usd`, whose value dispatcher.py builds as `str(budget)`. Its
// golden vector — values CPython actually printed, given as raw IEEE-754 bits
// so the test file cannot round the case it is testing — came with it.
package pyfmt

import (
	"strconv"
	"strings"
)

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

// Commas renders an integer the way Python's `{n:,}` does: groups of three
// digits separated by commas, with the sign kept in front.
//
// Token counts and caps are printed this way in the budget gate's block
// reason, which is the line an operator reads when a run stops dispatching.
func Commas(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}

	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	// The first group is whatever is left over above a multiple of three.
	lead := len(s) % 3
	if lead == 0 {
		lead = 3
	}
	b.WriteString(s[:lead])
	for i := lead; i < len(s); i += 3 {
		b.WriteByte(',')
		b.WriteString(s[i : i+3])
	}
	return b.String()
}
