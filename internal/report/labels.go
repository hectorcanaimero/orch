package report

import (
	"fmt"
	"time"

	"github.com/hectorcanaimero/orch/internal/publish/snapshot"
)

// labels is the page's own wording in one language. The executive summary
// and the blocker reasons arrive already translated in the snapshot; these
// are the words around them.
type labels struct {
	generated, unnamed                            string
	done, progress, inProgress, blocked           string
	estFinish, estRemaining                       string
	milestones, phase, more, whatIsBlocked, spend string
	toDate, taken                                 string
	minutes, hours, days, future                  string
	months                                        [12]string // full names; nil = English layout
	short                                         [12]string // abbreviations for the finish date
}

var labelsByLang = map[string]labels{
	"en": {
		generated: "Progress report · generated ", unnamed: "(unnamed project)",
		done: "Done", progress: "Progress", inProgress: "In progress", blocked: "Blocked",
		estFinish: "Est. finish", estRemaining: "Est. remaining",
		milestones: "Milestones", phase: "Phase %d", more: "+%d more", whatIsBlocked: "What is blocked", spend: "Spend",
		toDate: "$%.2f to date", taken: "Snapshot taken %s.",
		minutes: "%d minutes old.", hours: "%d hours old.", days: "%d days old.",
		future: "Its timestamp is in the future — check the clock on the machine that made it.",
	},
	"es": {
		generated: "Informe de avance · generado el ", unnamed: "(proyecto sin nombre)",
		done: "Completadas", progress: "Avance", inProgress: "En curso", blocked: "Bloqueadas",
		estFinish: "Fin estimado", estRemaining: "Restante estimado",
		milestones: "Hitos", phase: "Fase %d", more: "+%d más", whatIsBlocked: "Qué está bloqueado", spend: "Gasto",
		toDate: "$%.2f a la fecha", taken: "Instantánea tomada el %s.",
		minutes: "Antigüedad: %d min.", hours: "Antigüedad: %d h.", days: "Antigüedad: %d días.",
		future: "Su marca de tiempo está en el futuro: revise el reloj de la máquina que la generó.",
		months: [12]string{"enero", "febrero", "marzo", "abril", "mayo", "junio", "julio", "agosto",
			"septiembre", "octubre", "noviembre", "diciembre"},
		short: [12]string{"ene", "feb", "mar", "abr", "may", "jun", "jul", "ago", "sept", "oct", "nov", "dic"},
	},
	"pt": {
		generated: "Relatório de progresso · gerado em ", unnamed: "(projeto sem nome)",
		done: "Concluídas", progress: "Progresso", inProgress: "Em andamento", blocked: "Bloqueadas",
		estFinish: "Previsão de término", estRemaining: "Restante estimado",
		milestones: "Marcos", phase: "Fase %d", more: "+%d mais", whatIsBlocked: "O que está bloqueado", spend: "Gastos",
		toDate: "$%.2f até agora", taken: "Instantâneo gerado em %s.",
		minutes: "Idade: %d min.", hours: "Idade: %d h.", days: "Idade: %d dias.",
		future: "O horário dele está no futuro: verifique o relógio da máquina que o gerou.",
		months: [12]string{"janeiro", "fevereiro", "março", "abril", "maio", "junho", "julho", "agosto",
			"setembro", "outubro", "novembro", "dezembro"},
		short: [12]string{"jan", "fev", "mar", "abr", "mai", "jun", "jul", "ago", "set", "out", "nov", "dez"},
	},
}

// labelsFor picks the snapshot's language. A snapshot with no language (or
// one this package does not know) gets English, which is what every report
// printed before the page was translated.
func labelsFor(s snapshot.Snapshot) labels {
	if l, ok := labelsByLang[s.ExecutiveSummary.Language]; ok {
		return l
	}
	return labelsByLang["en"]
}

// longDate is "12 September 2026, 08:30 UTC" or "12 de septiembre de 2026, 08:30 UTC".
func (l labels) longDate(t time.Time) string {
	if l.months[0] == "" {
		return t.Format("2 January 2006, 15:04 UTC")
	}
	return fmt.Sprintf("%d de %s de %d, %s UTC", t.Day(), l.months[t.Month()-1], t.Year(), t.Format("15:04"))
}

// shortDate is "16 Sep 2026" or "16 sept 2026".
func (l labels) shortDate(t time.Time) string {
	if l.short[0] == "" {
		return t.Format("2 Jan 2006")
	}
	return fmt.Sprintf("%d %s %d", t.Day(), l.short[t.Month()-1], t.Year())
}
