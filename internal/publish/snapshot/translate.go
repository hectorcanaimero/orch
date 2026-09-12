package snapshot

import "github.com/hectorcanaimero/orch/internal/providers"

// translateReason is the technical→business translation table the G6.1
// contract calls for.
//
// It has no Python equivalent to port: `_stakeholder_payload`'s own
// `blocked_reasons` truncates a task's first raw comment to 120 characters
// and ships it as-is (orchestrator/dashboard/server.py:1682-1687) — exactly
// the kind of leak (a stack trace, an exit code, a provider's own error
// text, all of it operator vocabulary) the Go contract explicitly forbids.
// This table replaces that with a closed, reviewed vocabulary: it maps
// `internal/providers.Failure` — the classifier the dispatch loop already
// runs every failure through (internal/providers/classify.go) — to one fixed
// sentence per class per language. A blocked task whose events carry no
// classified failure (for example: manually deferred, or blocked only by an
// unmet dependency) gets the generic sentence, never the free-text `reason`
// an event's `extra` map may also carry.
var reasonPhrases = map[string]map[providers.Failure]string{
	"es": {
		providers.FailureIDSpoof:      "El agente reportó un resultado inconsistente y se descartó por seguridad.",
		providers.FailureTimeout:      "La tarea superó el tiempo estimado.",
		providers.FailureRateLimit:    "Se alcanzó un límite de uso del proveedor de IA.",
		providers.FailurePermission:   "Un problema de credenciales impide continuar.",
		providers.FailureBudget:       "Se alcanzó un límite de presupuesto configurado.",
		providers.FailureVersionDrift: "El proveedor rechazó la versión de modelo configurada.",
		providers.FailureTransient:    "Un error temporal del proveedor interrumpió la tarea; se reintentará.",
		providers.FailureParser:       "No se pudo interpretar la respuesta del agente.",
		providers.FailureOther:        "Ocurrió un problema técnico que requiere revisión del equipo.",
	},
	"en": {
		providers.FailureIDSpoof:      "The agent reported an inconsistent result and was discarded for safety.",
		providers.FailureTimeout:      "The task exceeded its estimated time.",
		providers.FailureRateLimit:    "An AI provider usage limit was reached.",
		providers.FailurePermission:   "A credentials problem is blocking progress.",
		providers.FailureBudget:       "A configured budget limit was reached.",
		providers.FailureVersionDrift: "The provider rejected the configured model version.",
		providers.FailureTransient:    "A temporary provider error interrupted the task; it will be retried.",
		providers.FailureParser:       "The agent's response could not be understood.",
		providers.FailureOther:        "A technical issue occurred that needs the team's review.",
	},
}

var genericBlockedReason = map[string]string{
	"es": "En espera de que el equipo resuelva un bloqueo.",
	"en": "Waiting on the team to resolve a blocker.",
}

// translateReason looks up class's phrase in lang's table. hasClass is false
// when the task's events carry no classified failure at all (a dependency
// block, a manual defer) — that is not "unknown class", it is "no failure
// happened here", so it gets the same generic sentence as a class this table
// hasn't been taught about yet, rather than a third, more alarming message.
func translateReason(lang string, class providers.Failure, hasClass bool) string {
	if hasClass {
		if phrase, ok := reasonPhrases[lang][class]; ok {
			return phrase
		}
	}
	return genericBlockedReason[lang]
}
