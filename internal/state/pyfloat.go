package state

import (
	"math"
	"strconv"
	"strings"
)

// pyFloat renders v exactly as Python's repr(float) does.
//
// This is not a cosmetic choice. `cost_usd` and `duration_s` are interpolated
// into the `spend` dedup preimage through a Python f-string, so the hash a
// Python orch writes depends on Python's float formatting. Go's own shortest
// formatting disagrees in two places:
//
//	value    Python      strconv.FormatFloat(v, 'g', -1, 64)
//	1.0      "1.0"       "1"
//	5400.0   "5400.0"    "5400"
//	-0.0     "-0.0"      "-0"
//	1e16     "1e+16"     "1e+16"
//	1e21     "1e+21"     "1e+21"
//
// Get it wrong and two orch binaries sharing one orch.db compute different
// hashes for the same spend row, so `INSERT OR IGNORE` stops ignoring and the
// budget gate double-counts every dispatch. The failure is silent and
// permanent, which is why it has its own golden table in
// testdata/README.md.
//
// CPython's rule (`float_repr` → `PyOS_double_to_string(v, 'r', 0,
// Py_DTSF_ADD_DOT_0)`):
//
//   - the shortest decimal digit string that round-trips,
//   - exponential form when decpt <= -4 or decpt > 16, written as `e±dd`
//     with at least two exponent digits,
//   - otherwise positional, with ".0" appended when it would otherwise look
//     like an integer.
//
// `decpt` is the position of the decimal point relative to the digit string:
// 5400.0 has digits "54" and decpt 4.
func pyFloat(v float64) string {
	switch {
	case math.IsNaN(v):
		return "nan"
	case math.IsInf(v, 1):
		return "inf"
	case math.IsInf(v, -1):
		return "-inf"
	}

	neg := math.Signbit(v)
	if v == 0 {
		// Signbit distinguishes -0.0, which Python prints as "-0.0".
		if neg {
			return "-0.0"
		}
		return "0.0"
	}

	// 'e' with precision -1 gives the shortest round-tripping digits, which
	// is the same digit string CPython's dtoa produces. Parsing it back is
	// cheaper and far less error-prone than reimplementing dtoa.
	digits, exp10 := shortestDigits(math.Abs(v))
	decpt := exp10 + 1

	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}

	if decpt <= -4 || decpt > 16 {
		// Exponential. Python writes one digit, then a fractional part only
		// when there is one: repr(1e21) == "1e+21", repr(1.5e21) == "1.5e+21".
		b.WriteByte(digits[0])
		if len(digits) > 1 {
			b.WriteByte('.')
			b.WriteString(digits[1:])
		}
		b.WriteByte('e')
		e := decpt - 1
		if e < 0 {
			b.WriteByte('-')
			e = -e
		} else {
			b.WriteByte('+')
		}
		es := strconv.Itoa(e)
		if len(es) < 2 {
			b.WriteByte('0')
		}
		b.WriteString(es)
		return b.String()
	}

	switch {
	case decpt <= 0:
		// 0.00042 — leading zeros between the point and the first digit.
		b.WriteString("0.")
		b.WriteString(strings.Repeat("0", -decpt))
		b.WriteString(digits)
	case decpt >= len(digits):
		// 5400 — trailing zeros, then the ".0" Python always adds.
		b.WriteString(digits)
		b.WriteString(strings.Repeat("0", decpt-len(digits)))
		b.WriteString(".0")
	default:
		b.WriteString(digits[:decpt])
		b.WriteByte('.')
		b.WriteString(digits[decpt:])
	}
	return b.String()
}

// shortestDigits returns the shortest round-tripping decimal digits of a
// positive, finite v, plus the base-10 exponent such that
// v == 0.<digits> * 10^(exp+1), i.e. the exponent of the leading digit.
func shortestDigits(v float64) (digits string, exp int) {
	s := strconv.FormatFloat(v, 'e', -1, 64) // e.g. "5.4e+03", "1e+00"
	mant, es, _ := strings.Cut(s, "e")
	exp, _ = strconv.Atoi(es) // FormatFloat always emits a signed exponent
	return strings.Replace(mant, ".", "", 1), exp
}

// pyInt renders an integer the way Python interpolates one into an f-string.
// Split out so call sites read symmetrically with pyFloat; Go and Python
// agree here, and the function exists to make that agreement explicit rather
// than incidental.
func pyInt(v int64) string { return strconv.FormatInt(v, 10) }
