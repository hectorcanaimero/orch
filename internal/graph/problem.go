// Package graph answers the static questions about a tasks.json DAG: is it
// valid, what order do the tasks go in, and what does it look like.
//
// Everything here is a pure function of the tasks and, where routing matters,
// of a set of route keys. No I/O, no database, no clock — which is what lets
// `orch validate` run on a project that has never been dispatched.
//
// Two of the three have a Python counterpart and are held to it:
//
//   - Validate ports `preflight.validate_graph`, message for message. The
//     text is user-visible and `scripts/parity.sh` diffs it, so even the
//     quoting style is copied.
//   - FindCycles ports `preflight.find_cycles`, including how it canonicalises
//     a cycle so the same loop is not reported twice from different entry
//     points.
//
// DOT has no counterpart: Python emits a self-contained HTML page with inline
// SVG and no DOT anywhere. Go replaces that (see docs/brainstorm/
// go-migration-notes.md), so its tests are its own.
package graph

// Severity mirrors the Python field. Every static validator currently emits
// "error"; the field exists because `validate_files_writable` and
// `validate_preset_sanity` emit warnings, and they will land here later.
type Severity string

const (
	SeverityError Severity = "error"
	SeverityWarn  Severity = "warn"
)

// Problem is one finding, mirroring `preflight.ValidationError`.
//
// `Kind` is the machine-stable tag callers filter on; `Message` is the human
// string. Both cross the wire in `orch validate --json`, so both are part of
// the compatibility contract rather than internal detail.
type Problem struct {
	// TaskID is empty when the problem is not about a specific task —
	// Python writes null there.
	TaskID      string   `json:"task_id"`
	Field       string   `json:"field"`
	Kind        string   `json:"kind"`
	Message     string   `json:"message"`
	Remediation string   `json:"remediation,omitempty"`
	Severity    Severity `json:"severity"`
}

// The kinds Validate can emit. Callers filter on these, so they are constants
// rather than literals scattered through the file.
const (
	KindSchemaTasks     = "schema.tasks"
	KindDepMissing      = "dep.missing"
	KindDepCycle        = "dep.cycle"
	KindRouteUnresolved = "route.unresolved"
)
