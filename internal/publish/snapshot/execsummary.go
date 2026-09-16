package snapshot

import (
	"fmt"
	"strings"
	"time"

	"github.com/hectorcanaimero/orch/internal/graph"
)

// summaryPhrases is one language's wording of the executive summary. Each
// entry is a fmt format; the verbs and their order are the same in every
// language, so one function fills them.
type summaryPhrases struct {
	complete   string // pct, done, total
	inProgress string // n
	blocked    string // n
	blockers   string // heading of the blocker list
	finish     string // date, confidence
	remaining  string // hours
	spend      string // usd
	confidence map[string]string
	months     [12]string // "" = English "Jan 2" layout
}

// summaryPhrasesByLang: es and en are byte-for-byte the wording the Python
// port shipped; pt is Brazilian Portuguese.
var summaryPhrasesByLang = map[string]summaryPhrases{
	"en": {
		complete:   "Project %d%% complete — %d of %d tasks delivered.",
		inProgress: "%d in progress.",
		blocked:    "%d blocked — need attention.",
		blockers:   "Blocked:",
		finish:     "Estimated finish: %s (%s confidence).",
		remaining:  "~%.1fh remaining at current pace.",
		spend:      "AI spend: $%.2f.",
	},
	"es": {
		complete:   "Proyecto %d%% completo — %d de %d tareas entregadas.",
		inProgress: "%d en progreso.",
		blocked:    "%d bloqueada(s) — requieren atención.",
		blockers:   "Bloqueos:",
		finish:     "Fecha estimada: %s (confianza %s).",
		remaining:  "Restan ~%.1fh al ritmo actual.",
		spend:      "Gastado en AI: $%.2f.",
		confidence: map[string]string{"high": "alta", "low": "baja"},
		months:     [12]string{"ene", "feb", "mar", "abr", "may", "jun", "jul", "ago", "sept", "oct", "nov", "dic"},
	},
	"pt": {
		complete:   "Projeto %d%% concluído — %d de %d tarefas entregues.",
		inProgress: "%d em andamento.",
		blocked:    "%d bloqueada(s) — precisam de atenção.",
		blockers:   "Bloqueios:",
		finish:     "Previsão de conclusão: %s (confiança %s).",
		remaining:  "Restam ~%.1fh no ritmo atual.",
		spend:      "Gasto com IA: $%.2f.",
		confidence: map[string]string{"high": "alta", "low": "baixa"},
		months:     [12]string{"jan", "fev", "mar", "abr", "mai", "jun", "jul", "ago", "set", "out", "nov", "dez"},
	},
}

// executiveSummary ports `executive_summary` (orchestrator/dashboard/
// metrics.py), trimmed to the branch `_stakeholder_payload` actually calls:
// it always passes `eta_hours`, never `eta_date`, so the `eta_date` half of
// Python's function — and its parameter — has nothing here to port. Adding
// it back would be supporting a call shape nothing in this codebase uses.
//
// blockers feeds the blocked tail, capped at 3 like Python's `reasons[:3]`,
// each rendered as "• title: reason" — reason already translated
// (translate.go), never the raw text Python's version shipped.
func executiveSummary(lang string, s graph.Summary, etaHours *float64, projection *graph.Projection, totalSpendUSD *float64, blockers []Blocker) string {
	p, ok := summaryPhrasesByLang[lang]
	if !ok {
		p = summaryPhrasesByLang["es"]
	}
	pct := 0
	if s.Total > 0 {
		pct = roundInt(float64(s.Done) / float64(s.Total) * 100)
	}

	head := []string{fmt.Sprintf(p.complete, pct, s.Done, s.Total)}
	if s.InProgress > 0 {
		head = append(head, fmt.Sprintf(p.inProgress, s.InProgress))
	}
	if s.Blocked > 0 {
		head = append(head, fmt.Sprintf(p.blocked, s.Blocked))
	}
	switch {
	case projection != nil:
		confidence := projection.Confidence
		if p.confidence != nil {
			// Python's Spanish branch: anything that is not "low" reads high.
			confidence = p.confidence["high"]
			if projection.Confidence == "low" {
				confidence = p.confidence["low"]
			}
		}
		head = append(head, fmt.Sprintf(p.finish, shortDate(projection.Date, p), confidence))
	case etaHours != nil:
		head = append(head, fmt.Sprintf(p.remaining, *etaHours))
	}
	if totalSpendUSD != nil {
		head = append(head, fmt.Sprintf(p.spend, *totalSpendUSD))
	}

	text := strings.Join(head, " ")
	if len(blockers) > 0 {
		text += "\n\n" + p.blockers + "\n" + blockerLines(blockers)
	}
	return text
}

// shortDate renders a YYYY-MM-DD day as "Sep 16", "16 sept" or "16 set". A
// date that does not parse is returned as given rather than dropped.
func shortDate(day string, p summaryPhrases) string {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return day
	}
	if p.months[0] == "" {
		return t.Format("Jan 2")
	}
	return fmt.Sprintf("%d %s", t.Day(), p.months[t.Month()-1])
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
