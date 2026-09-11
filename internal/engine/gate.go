package engine

import (
	"fmt"
	"strings"

	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/pyfmt"
)

// Decision is what the operator answered at the semi-mode gate.
type Decision string

const (
	DecisionDispatch Decision = "dispatch" // y
	DecisionDefer    Decision = "defer"    // Enter or n — re-offered at the end of the run
	DecisionSkip     Decision = "skip"     // s — blocked permanently
	DecisionQuit     Decision = "quit"     // q — drain what is in flight and stop
)

// Gate asks the operator whether to dispatch one task.
//
// An interface with a single method, rather than Python's SemiModeGate class
// that reads stdin directly, so the scheduler can be tested without a
// terminal. TerminalGate is the real one; tests supply their own.
//
// `reason` is the already-rendered explanation of why the gate fired, so an
// implementation does not need the router to print something useful.
type Gate interface {
	Ask(task model.Task, reason string) Decision
}

// HighEffortHours is the estimate at which a task is gated on size alone
// (FR-C-1d).
const HighEffortHours = 10.0

// packagesCriticalSuffixes are the library trees inside a package that gate a
// task. The leading `/lib/` is part of each, so a substring match after the
// `packages/` prefix is enough — which is what Python does, and what keeps a
// test able to point at the exact fragment that matched.
var packagesCriticalSuffixes = []string{
	"/lib/auth/",
	"/lib/billing/",
	"/lib/security/",
}

// IsPremium reports whether a task's route is the premium tier.
//
// A task whose model has no route is not premium rather than an error: the
// run has already failed startup validation (FR-D-6) if that is true of a
// real task, so reaching here means an out-of-band Task, and refusing to gate
// it is the conservative answer.
func IsPremium(task model.Task, routes map[string]model.RouteEntry) bool {
	route, ok := routes[task.Model]
	return ok && route.IsPremium
}

// TouchesMigrations reports whether a declared file lives under
// `supabase/migrations/`.
func TouchesMigrations(task model.Task) bool {
	for _, f := range task.Files {
		if strings.Contains(f, "supabase/migrations/") {
			return true
		}
	}
	return false
}

// TouchesEdgeFunctions reports whether a declared file is a
// `supabase/functions/*/index.ts`. Only the entry point counts — the other
// files in the same directory are helpers.
func TouchesEdgeFunctions(task model.Task) bool {
	for _, f := range task.Files {
		if strings.Contains(f, "supabase/functions/") && strings.HasSuffix(f, "/index.ts") {
			return true
		}
	}
	return false
}

// TouchesPackagesCritical reports whether a declared file lives under
// `packages/*/lib/{auth,billing,security}/`.
func TouchesPackagesCritical(task model.Task) bool {
	for _, f := range task.Files {
		if !strings.HasPrefix(f, "packages/") {
			continue
		}
		for _, suffix := range packagesCriticalSuffixes {
			if strings.Contains(f, suffix) {
				return true
			}
		}
	}
	return false
}

// TouchesCriticalFiles is the union of the three file predicates (FR-C-1b).
func TouchesCriticalFiles(task model.Task) bool {
	return TouchesMigrations(task) ||
		TouchesEdgeFunctions(task) ||
		TouchesPackagesCritical(task)
}

// IsHighEffort reports whether the estimate alone gates the task (FR-C-1d).
func IsHighEffort(task model.Task) bool { return task.EstimateHours >= HighEffortHours }

// IsCritical composes the four FR-C-1 rules. In semi mode, a task that
// answers true here is offered to the operator before it is dispatched.
//
// Rule (c) — `phase == 10 && premium` — is subsumed by (a) and is kept only
// because the spec lists it separately; Python keeps it for the same reason.
func IsCritical(task model.Task, routes map[string]model.RouteEntry) bool {
	return IsPremium(task, routes) ||
		TouchesCriticalFiles(task) ||
		(task.Phase == 10 && IsPremium(task, routes)) ||
		IsHighEffort(task)
}

// GateReason renders why the gate fired, for the operator's prompt. Only the
// first matching rule is reported — enough to know why — and the order is the
// spec's (FR-C-1).
func GateReason(task model.Task, routes map[string]model.RouteEntry) string {
	switch {
	case IsPremium(task, routes):
		return fmt.Sprintf("premium tier (%s)", routes[task.Model].Tier)
	case TouchesMigrations(task):
		return "touches supabase/migrations/**"
	case TouchesEdgeFunctions(task):
		return "touches supabase/functions/**/index.ts"
	case TouchesPackagesCritical(task):
		return "touches packages/*/lib/{auth,billing,security}/**"
	case IsHighEffort(task):
		// Python interpolates the float with an f-string, so an estimate of
		// 12.0 reads "estimate_hours=12.0", not "12".
		return fmt.Sprintf("estimate_hours=%s >= %s",
			pyfmt.Float(task.EstimateHours), pyfmt.Float(HighEffortHours))
	default:
		return "critical"
	}
}
