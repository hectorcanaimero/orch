package report

import (
	"testing"
	"time"
)

// The page speaks the snapshot's language: a Spanish or Portuguese client
// gets every label, date and freshness line in their language, not English
// wrapped around a translated summary.
func TestTheReportSpeaksTheSnapshotsLanguage(t *testing.T) {
	at := time.Date(2026, 9, 12, 11, 30, 0, 0, time.UTC)
	date := "2026-09-16"
	for lang, want := range map[string][]string{
		"es": {"Informe de avance", "Completadas", "Fin estimado", "16 sept 2026", "Hitos",
			"Qué está bloqueado", "Gasto", "$12.50 a la fecha",
			"Instantánea tomada el 12 de septiembre de 2026, 08:30 UTC.", "Antigüedad: 3 h."},
		"pt": {"Relatório de progresso", "Concluídas", "Previsão de término", "16 set 2026", "Marcos",
			"O que está bloqueado", "Gastos", "$12.50 até agora",
			"Instantâneo gerado em 12 de setembro de 2026, 08:30 UTC.", "Idade: 3 h."},
	} {
		t.Run(lang, func(t *testing.T) {
			s := demoSnapshot(3)
			s.ExecutiveSummary.Language = lang
			s.Summary.ETADate = &date
			b := render(t, s, at)
			for _, w := range want {
				if !containsText(b, w) {
					t.Errorf("the %s report does not say %q", lang, w)
				}
			}
			for _, english := range []string{"Progress report", "Milestones", "What is blocked", "Snapshot taken", "to date"} {
				if containsText(b, english) {
					t.Errorf("the %s report still says %q", lang, english)
				}
			}
		})
	}
}
