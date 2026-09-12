package snapshot

import (
	"math"
	"strconv"
)

// round1/round4 reproduce Python's `round(x, n)` — correctly rounded,
// half-to-even on the exact decimal value, which the arithmetic form
// (`math.Round(x*10^n)/10^n`) is not. Every other Go-ported package in this
// tree that needs Python's round has its own copy of this same pattern
// (internal/dashboard's round1, internal/project's round3) rather than a
// shared export — small enough that a shared home would cost more in import
// indirection than it saves.
func round1(v float64) float64 { return roundN(v, 1) }
func round4(v float64) float64 { return roundN(v, 4) }

func roundN(v float64, n int) float64 {
	f, err := strconv.ParseFloat(strconv.FormatFloat(v, 'f', n, 64), 64)
	if err != nil {
		return v
	}
	return f
}

// roundInt is Python's `round(x)` with no ndigits — half-to-even to the
// nearest integer. Used only for the executive summary's inline percentage,
// which Python computes separately from `ProjectSummary`'s own percent_done
// (rounded to 1 decimal, not 0) rather than reusing it.
func roundInt(v float64) int {
	n, err := strconv.Atoi(strconv.FormatFloat(v, 'f', 0, 64))
	if err != nil {
		return int(v)
	}
	return n
}

// roundUpToStep ports `round_up_to_step`: round UP to the nearest multiple
// of step (default $0.50) so a displayed spend can't under-report to a
// stakeholder. Non-positive values collapse to 0 rather than a negative
// multiple — spend is never negative in practice, and Python's own
// docstring only promises the non-negative case.
func roundUpToStep(value, step float64) float64 {
	if step <= 0 {
		return value
	}
	if value <= 0 {
		return 0.0
	}
	return math.Ceil(value/step) * step
}

// RoundUpToStep is `round_up_to_step` for a caller outside this package.
//
// Exported as a thin wrapper rather than by moving the function: the rounding
// rule belongs to the stakeholder payload, and the dashboard's own
// `/stakeholder/summary` needs the same figure the executive summary quotes.
// A second `math.Ceil(x/0.5)*0.5` somewhere else would be a second definition
// of "what a client is told it cost", which is the kind of duplication that
// drifts by a rounding step and is never noticed.
func RoundUpToStep(value, step float64) float64 { return roundUpToStep(value, step) }

// SpendStep is the step that rounding uses ($0.50), exposed with it so a
// caller cannot pass a different one by accident and produce a figure the
// executive summary disagrees with.
const SpendStep = 0.50
