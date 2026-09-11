// Package doctor ports the environment-diagnostic subset of
// orchestrator/preflight.py and orchestrator/doctor.py into pure checks: no
// stdout, no argparse — each function returns []Check for a caller (a
// `orch doctor` subcommand, not yet wired — see internal/cli) to print as
// text or marshal as JSON.
//
// This is a narrower cut than build_doctor_report's full list. The G4.5
// brief named eight checks; the rest of preflight.py's surface (schema/
// cycle/dependency validation, config-file shape, tunnel binaries, the
// installed-skill check, SQLite orphan-row/divergent-DB detection) either
// already has a Go home (internal/graph's Validate ports
// validate_schema/validate_dependencies/validate_cycles) or is out of scope
// for this package; see docs/brainstorm/go-migration-notes.md for the full
// accounting.
package doctor

import "sort"

// Status is one check's outcome, matching Python's CheckStatus literal.
type Status string

const (
	StatusOK    Status = "ok"
	StatusWarn  Status = "warn"
	StatusError Status = "error"
	StatusSkip  Status = "skip"
)

// Check is the outcome of one diagnostic. Ports CheckResult
// (orchestrator/preflight.py); field names and JSON tags match its
// as_json() so a caller can marshal this directly into the same wire shape.
type Check struct {
	Name        string `json:"name"`
	Status      Status `json:"status"`
	Detail      string `json:"detail"`
	Remediation string `json:"remediation,omitempty"`
}

// Summary counts checks by status. Ports summarize_checks.
func Summary(checks []Check) map[Status]int {
	out := map[Status]int{StatusOK: 0, StatusWarn: 0, StatusError: 0, StatusSkip: 0}
	for _, c := range checks {
		out[c.Status]++
	}
	return out
}

// ExitCode is 2 when any check errored, 1 when the worst is a warning, 0
// otherwise. Ports exit_code_for_checks.
func ExitCode(checks []Check) int {
	worst := 0
	for _, c := range checks {
		switch c.Status {
		case StatusError:
			return 2
		case StatusWarn:
			worst = 1
		}
	}
	return worst
}

// SortByName orders checks alphabetically, matching check_backends' own
// `sorted(results, key=lambda r: r.name)` — the only place Python sorts
// explicitly; doctor.py's build_doctor_report otherwise appends in a fixed
// run order, which callers here get for free since each Check* function
// returns its own checks in a fixed order too.
func SortByName(checks []Check) {
	sort.Slice(checks, func(i, j int) bool { return checks[i].Name < checks[j].Name })
}
