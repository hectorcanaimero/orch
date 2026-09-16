package snapshot

import (
	"time"

	"github.com/hectorcanaimero/orch/internal/providers"
	"github.com/hectorcanaimero/orch/internal/state"
)

// Languages are the languages a snapshot is written in. `pt` is Brazilian
// Portuguese, the variant docs/MANUAL.pt.md is written in.
var Languages = []string{"en", "es", "pt"}

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
// sentence per class per language. The free-text `reason` an event's `extra`
// map carries never reaches a client.
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
	"pt": {
		providers.FailureIDSpoof:      "O agente informou um resultado inconsistente, que foi descartado por segurança.",
		providers.FailureTimeout:      "A tarefa ultrapassou o tempo estimado.",
		providers.FailureRateLimit:    "Foi atingido um limite de uso do provedor de IA.",
		providers.FailurePermission:   "Um problema de credenciais está impedindo o avanço.",
		providers.FailureBudget:       "Foi atingido um limite de orçamento configurado.",
		providers.FailureVersionDrift: "O provedor recusou a versão de modelo configurada.",
		providers.FailureTransient:    "Um erro temporário do provedor interrompeu a tarefa; ela será tentada de novo.",
		providers.FailureParser:       "Não foi possível interpretar a resposta do agente.",
		providers.FailureOther:        "Ocorreu um problema técnico que precisa de revisão da equipe.",
	},
}

// ciBlockedReason: the task's pull request failed its checks more times than
// the CI retries allow (engine event `ci_blocked`).
var ciBlockedReason = map[string]string{
	"es": "Las verificaciones automáticas del cambio fallaron; el equipo lo está revisando.",
	"en": "The change's automated checks failed; the team is looking into it.",
	"pt": "As verificações automáticas da mudança falharam; a equipe está analisando.",
}

// notStartedReason: the engine could not start the task at all (engine
// event `block`, written when a dispatch is refused before any agent runs).
var notStartedReason = map[string]string{
	"es": "La tarea no se pudo iniciar; el equipo está revisando la configuración.",
	"en": "The task could not be started; the team is checking the setup.",
	"pt": "Não foi possível iniciar a tarefa; a equipe está verificando a configuração.",
}

// genericBlockedReason: nothing in the event log says why — a person or an
// agent moved the task to blocked by hand.
var genericBlockedReason = map[string]string{
	"es": "En espera de que el equipo resuelva un bloqueo.",
	"en": "Waiting on the team to resolve a blocker.",
	"pt": "Aguardando a equipe resolver um bloqueio.",
}

// blockSignal is the most recent thing the event log says about why a task
// stopped: which kind of event, and for a failure its class.
type blockSignal struct {
	kind  string // "fail", "ci_blocked" or "block"
	class providers.Failure
	at    time.Time
}

// latestBlockSignals keeps, per task, the newest fail/timeout, `ci_blocked`
// or `block` event. Timestamps are parsed, not compared as strings: one
// orch.db holds both `Z` and `+00:00` spellings.
func latestBlockSignals(events []state.Event) map[string]blockSignal {
	out := map[string]blockSignal{}
	for _, e := range events {
		sig := blockSignal{kind: e.EventType}
		switch e.EventType {
		case "fail", "timeout":
			sig.kind = "fail"
			class, _ := e.Extra["failure_class"].(string)
			sig.class = providers.Failure(class)
		case "ci_blocked", "block":
		default:
			continue
		}
		at, ok := state.ParseTS(e.TS)
		if !ok {
			continue
		}
		sig.at = at
		if prev, seen := out[e.TaskID]; seen && at.Before(prev.at) {
			continue
		}
		out[e.TaskID] = sig
	}
	return out
}

// translateReason turns a task's latest block signal into lang's sentence.
// A failure whose class this table was never taught still reads as a
// technical problem (FailureOther), because it was one; only a task with no
// signal at all gets the generic sentence.
func translateReason(lang string, sig blockSignal, hasSignal bool) string {
	if !hasSignal {
		return genericBlockedReason[lang]
	}
	switch sig.kind {
	case "ci_blocked":
		return ciBlockedReason[lang]
	case "block":
		return notStartedReason[lang]
	}
	if phrase, ok := reasonPhrases[lang][sig.class]; ok {
		return phrase
	}
	return reasonPhrases[lang][providers.FailureOther]
}
