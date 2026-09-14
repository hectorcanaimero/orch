package snapshot

import (
	"fmt"
	"strings"
	"time"

	"github.com/hectorcanaimero/orch/internal/graph"
)

// executiveSummary ports `executive_summary` (orchestrator/dashboard/
// metrics.py), trimmed to the branch `_stakeholder_payload` actually calls:
// it always passes `eta_hours`, never `eta_date`, so the `eta_date` half of
// Python's function — and its parameter — has nothing here to port. Adding
// it back would be supporting a call shape nothing in this codebase uses.
//
// blockers feeds the "Bloqueos"/"Blocked" tail, capped at 3 like Python's
// `reasons[:3]`, each rendered as "• title: reason" — reason already
// translated (translate.go), never the raw text Python's version shipped.
func executiveSummary(lang string, s graph.Summary, etaHours *float64, projection *graph.Projection, totalSpendUSD *float64, blockers []Blocker) string {
	pct := 0
	if s.Total > 0 {
		pct = roundInt(float64(s.Done) / float64(s.Total) * 100)
	}

	var head []string
	var tail []string

	switch lang {
	case "en":
		head = append(head, fmt.Sprintf("Project %d%% complete — %d of %d tasks delivered.", pct, s.Done, s.Total))
		if s.InProgress > 0 {
			head = append(head, fmt.Sprintf("%d in progress.", s.InProgress))
		}
		if s.Blocked > 0 {
			head = append(head, fmt.Sprintf("%d blocked — need attention.", s.Blocked))
		}
		if len(blockers) > 0 {
			tail = append(tail, "Blocked:\n"+blockerLines(blockers))
		}
		switch {
		case projection != nil:
			head = append(head, fmt.Sprintf("Estimated finish: %s (%s confidence).",
				shortDate(projection.Date, "en"), projection.Confidence))
		case etaHours != nil:
			head = append(head, fmt.Sprintf("~%.1fh remaining at current pace.", *etaHours))
		}
		if totalSpendUSD != nil {
			head = append(head, fmt.Sprintf("AI spend: $%.2f.", *totalSpendUSD))
		}
	default: // "es"
		head = append(head, fmt.Sprintf("Proyecto %d%% completo — %d de %d tareas entregadas.", pct, s.Done, s.Total))
		if s.InProgress > 0 {
			head = append(head, fmt.Sprintf("%d en progreso.", s.InProgress))
		}
		if s.Blocked > 0 {
			head = append(head, fmt.Sprintf("%d bloqueada(s) — requieren atención.", s.Blocked))
		}
		if len(blockers) > 0 {
			tail = append(tail, "Bloqueos:\n"+blockerLines(blockers))
		}
		switch {
		case projection != nil:
			confidence := "alta"
			if projection.Confidence == "low" {
				confidence = "baja"
			}
			head = append(head, fmt.Sprintf("Fecha estimada: %s (confianza %s).",
				shortDate(projection.Date, "es"), confidence))
		case etaHours != nil:
			head = append(head, fmt.Sprintf("Restan ~%.1fh al ritmo actual.", *etaHours))
		}
		if totalSpendUSD != nil {
			head = append(head, fmt.Sprintf("Gastado en AI: $%.2f.", *totalSpendUSD))
		}
	}

	text := strings.Join(head, " ")
	if len(tail) > 0 {
		text += "\n\n" + strings.Join(tail, "\n")
	}
	return text
}

// shortDate renders a YYYY-MM-DD day as "16 sept" or "Sep 16". A date that
// does not parse is returned as given rather than dropped.
func shortDate(day, lang string) string {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return day
	}
	if lang == "en" {
		return t.Format("Jan 2")
	}
	months := [...]string{"ene", "feb", "mar", "abr", "may", "jun", "jul", "ago", "sept", "oct", "nov", "dic"}
	return fmt.Sprintf("%d %s", t.Day(), months[t.Month()-1])
}

// blockerLines renders up to the first 3 blockers as "• title: reason",
// matching Python's `reasons[:3]` cap — a stakeholder summary is a headline,
// not the full blocked list (that's the `blockers` array itself).
func blockerLines(blockers []Blocker) string {
	n := len(blockers)
	if n > 3 {
		n = 3
	}
	lines := make([]string, 0, n)
	for _, b := range blockers[:n] {
		lines = append(lines, fmt.Sprintf("• %s: %s", b.Title, b.Reason))
	}
	return strings.Join(lines, "\n")
}
