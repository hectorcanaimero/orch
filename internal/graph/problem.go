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
	// TaskID and Remediation are pointers so that "not applicable" crosses the
	// wire as `null` rather than as `""` or as an absent key.
	//
	// Python's `ValidationError.as_json` writes every key every time, with
	// `None` where there is nothing to say: a task with no id has
	// `"task_id": null`, and a problem with no fix to suggest has
	// `"remediation": null`. `orch validate --json` output is diffed between
	// the two binaries by scripts/parity.sh, so an omitted key and an empty
	// string are both failures — and they are also different to a consumer,
	// which can tell "no task" from "a task whose id is the empty string".
	TaskID      *string  `json:"task_id"`
	Field       string   `json:"field"`
	Kind        string   `json:"kind"`
	Message     string   `json:"message"`
	Remediation *string  `json:"remediation"`
	Severity    Severity `json:"severity"`
}

// ref is the address of a string, for the fields Python can write as null.
//
// Reads better at the call sites than a local per problem, and keeps the
// distinction visible: a Problem built without `ref(...)` is one that says
// null, deliberately.
func ref(s string) *string { return &s }

// The kinds Validate can emit. Callers filter on these, so they are constants
// rather than literals scattered through the file.
const (
	KindSchemaTasks     = "schema.tasks"
	KindDepMissing      = "dep.missing"
	KindDepCycle        = "dep.cycle"
	KindRouteUnresolved = "route.unresolved"
)
