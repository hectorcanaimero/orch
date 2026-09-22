package export

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hectorcanaimero/orch/internal/model"
)

// The text a person reads in ClickUp: task descriptions, List descriptions
// and the comment left on every status change. Written as prose in sections,
// never as a dump of orch's JSON, in the project's summary language.

type clickUpWords struct {
	what, doneWhen, covers, dependsOn, nothingDepends, who, files, spec, pkg,
	mirrorNote, model, estimate, timeout, coversRef, notInTasks,
	started, finished, blocked, ready, backlog, moved, lastNote, spend, attempts,
	phaseIntro, phaseCount, decimal string
}

var clickUpText = map[string]clickUpWords{
	"en": {
		what: "What to do", doneWhen: "How we know it is done", covers: "Requirements it covers",
		dependsOn: "Depends on", nothingDepends: "Nothing: it can start as soon as it is promoted to to do.",
		who: "Who does it and why", files: "Files the agent may change", spec: "Spec", pkg: "Package",
		mirrorNote: "Mirrored from orch. orch is the source of truth: changes made here do not go back to orch.",
		model: "Assigned model", estimate: "Estimate", timeout: "an attempt that runs past %s is stopped and retried",
		coversRef: "details in the PRD", notInTasks: "not in tasks.json",
		started: "🚀 orch started this task.", finished: "✅ orch finished this task.",
		blocked: "⛔ orch blocked this task.", ready: "📋 This task is ready to run.",
		backlog: "🗂️ This task went back to the backlog.", moved: "Status in ClickUp: %s → %s.",
		lastNote: "Latest orch note (%s, %s): %s", spend: "Spend so far: US$ %s over %d %s, %s output tokens, %s of work.",
		attempts: "attempt(s)",
		phaseIntro: "Phase F%d of orch project `%s`, mirrored by `orch export clickup`.",
		phaseCount: "%d task(s). Each task's description has what to do, how we know it is done, its dependencies and the files it may change.",
		decimal:    ".",
	},
	"es": {
		what: "Qué hay que hacer", doneWhen: "Cómo sabemos que está terminada", covers: "Qué requisitos cubre",
		dependsOn: "Depende de", nothingDepends: "Nada: puede empezar apenas se promueva a to do.",
		who: "Quién la hace y por qué", files: "Archivos que el agente puede tocar", spec: "Spec", pkg: "Paquete",
		mirrorNote: "Sincronizado desde orch. orch es la fuente de verdad: los cambios hechos acá no vuelven a orch.",
		model: "Modelo asignado", estimate: "Estimación", timeout: "si un intento pasa de %s, orch lo corta y reintenta",
		coversRef: "detalle en el PRD", notInTasks: "no está en tasks.json",
		started: "🚀 orch empezó esta tarea.", finished: "✅ orch terminó esta tarea.",
		blocked: "⛔ orch bloqueó esta tarea.", ready: "📋 La tarea está lista para ejecutarse.",
		backlog: "🗂️ La tarea volvió al backlog.", moved: "Estado en ClickUp: %s → %s.",
		lastNote: "Último registro de orch (%s, %s): %s", spend: "Consumo acumulado: US$ %s en %d %s, %s tokens de salida, %s de trabajo.",
		attempts: "intento(s)",
		phaseIntro: "Fase F%d del proyecto orch `%s`, sincronizada con `orch export clickup`.",
		phaseCount: "%d tarea(s). La descripción de cada una dice qué hay que hacer, cómo sabemos que está terminada, de qué depende y qué archivos puede tocar.",
		decimal:    ",",
	},
	"pt": {
		what: "O que fazer", doneWhen: "Como sabemos que está pronta", covers: "Requisitos que cobre",
		dependsOn: "Depende de", nothingDepends: "Nada: pode começar assim que for promovida a to do.",
		who: "Quem faz e por quê", files: "Arquivos que o agente pode alterar", spec: "Spec", pkg: "Pacote",
		mirrorNote: "Sincronizado a partir do orch. O orch é a fonte da verdade: mudanças feitas aqui não voltam para o orch.",
		model: "Modelo atribuído", estimate: "Estimativa", timeout: "uma tentativa que passar de %s é interrompida e repetida",
		coversRef: "detalhes no PRD", notInTasks: "não está no tasks.json",
		started: "🚀 O orch começou esta tarefa.", finished: "✅ O orch terminou esta tarefa.",
		blocked: "⛔ O orch bloqueou esta tarefa.", ready: "📋 A tarefa está pronta para rodar.",
		backlog: "🗂️ A tarefa voltou para o backlog.", moved: "Status no ClickUp: %s → %s.",
		lastNote: "Último registro do orch (%s, %s): %s", spend: "Consumo até agora: US$ %s em %d %s, %s tokens de saída, %s de trabalho.",
		attempts: "tentativa(s)",
		phaseIntro: "Fase F%d do projeto orch `%s`, sincronizada pelo `orch export clickup`.",
		phaseCount: "%d tarefa(s). A descrição de cada uma diz o que fazer, como sabemos que está pronta, suas dependências e os arquivos que pode alterar.",
		decimal:    ",",
	},
}

func (p ClickUpPlan) words() clickUpWords {
	if w, ok := clickUpText[strings.ToLower(strings.TrimSpace(p.Language))]; ok {
		return w
	}
	return clickUpText["en"]
}

// doneWhenLine splits an orch-spec description at its "Done when:" line.
var doneWhenLine = regexp.MustCompile(`(?mi)^\s*(?:done when|terminado cuando|pronto quando)\s*:\s*`)

var requirementID = regexp.MustCompile(`\bN?FR-\d+\b`)

// TaskDescription is a task's ClickUp description, in markdown.
func (p ClickUpPlan) TaskDescription(t model.Task) string {
	w := p.words()
	var b strings.Builder

	what, done := strings.TrimSpace(t.Description), ""
	if loc := doneWhenLine.FindStringIndex(what); loc != nil {
		what, done = strings.TrimSpace(what[:loc[0]]), strings.TrimSpace(what[loc[1]:])
	}
	if what == "" {
		what = t.Title
	}
	fmt.Fprintf(&b, "## %s\n\n%s\n\n", w.what, what)
	if done != "" {
		fmt.Fprintf(&b, "## %s\n\n%s\n\n", w.doneWhen, done)
	}

	if ids := uniqueRequirements(t.Description); len(ids) > 0 {
		fmt.Fprintf(&b, "## %s\n\n%s — %s.\n\n", w.covers, strings.Join(ids, ", "), w.coversRef)
	}

	fmt.Fprintf(&b, "## %s\n\n", w.dependsOn)
	if len(t.Dependencies) == 0 {
		b.WriteString(w.nothingDepends + "\n\n")
	} else {
		for _, dep := range t.Dependencies {
			if d, ok := p.All[dep]; ok {
				fmt.Fprintf(&b, "- **%s** — %s\n", dep, d.Title)
			} else {
				fmt.Fprintf(&b, "- **%s** — %s\n", dep, w.notInTasks)
			}
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "## %s\n\n", w.who)
	if t.Model != "" {
		fmt.Fprintf(&b, "%s: **%s**.", w.model, t.Model)
		if r := strings.TrimSpace(t.Reason); r != "" {
			b.WriteString(" " + r)
		}
		b.WriteString("\n")
	}
	if t.EstimateHours > 0 {
		fmt.Fprintf(&b, "%s: **%s**", w.estimate, p.hours(t.EstimateHours))
		if p.TimeoutMultiplier > 0 {
			fmt.Fprintf(&b, " (%s)", fmt.Sprintf(w.timeout, p.hours(t.EstimateHours*p.TimeoutMultiplier)))
		}
		b.WriteString(".\n")
	}
	b.WriteString("\n")

	if len(t.Files) > 0 {
		fmt.Fprintf(&b, "## %s\n\n", w.files)
		for _, f := range t.Files {
			fmt.Fprintf(&b, "- `%s`\n", f)
		}
		b.WriteString("\n")
	}

	b.WriteString("---\n")
	var foot []string
	if t.SpecRef != "" {
		foot = append(foot, fmt.Sprintf("📄 %s: `%s`", w.spec, specPath(p.SpecRoot, t.SpecRef)))
	}
	if pkg := p.PackageLabel(t.ID); pkg != "" {
		foot = append(foot, fmt.Sprintf("%s **%s**", w.pkg, pkg))
	}
	if len(foot) > 0 {
		b.WriteString(strings.Join(foot, " · ") + "\n\n")
	}
	fmt.Fprintf(&b, "_%s_\n\n", w.mirrorNote)
	b.WriteString(clickUpTaskMarker(p.ProjectID, t.ID))
	b.WriteString("\n")
	return b.String()
}

// PhaseContent is a phase List's description.
func (p ClickUpPlan) PhaseContent(ph ClickUpPhase) string {
	w := p.words()
	return fmt.Sprintf(w.phaseIntro, ph.Number, p.ProjectID) + " " + fmt.Sprintf(w.phaseCount, len(ph.Tasks))
}

// StatusComment is the comment left when the mirror moves a task: what
// happened, the latest note orch recorded for it, and what it has cost.
// ClickUp comments are plain text, so no markdown here.
func (p ClickUpPlan) StatusComment(t model.Task, from, to string) string {
	w := p.words()
	var lines []string
	switch normStatus(t.Status) {
	case model.StatusInProgress:
		lines = append(lines, w.started)
	case model.StatusDone:
		lines = append(lines, w.finished)
	case model.StatusBlocked:
		lines = append(lines, w.blocked)
	case model.StatusTodo:
		lines = append(lines, w.ready)
	case model.StatusBacklog:
		lines = append(lines, w.backlog)
	}
	lines = append(lines, fmt.Sprintf(w.moved, from, to))
	if author, body, at := lastComment(t.Comments); body != "" {
		lines = append(lines, fmt.Sprintf(w.lastNote, author, p.when(at), body))
	}
	if s, ok := p.Spend[t.ID]; ok && (s.CostUSD > 0 || s.TokensOut > 0) {
		lines = append(lines, fmt.Sprintf(w.spend, p.number(s.CostUSD, 2), s.Attempts, w.attempts,
			p.thousands(s.TokensOut), p.duration(s.DurationS)))
	}
	return strings.Join(lines, "\n")
}

// lastComment reads the newest orch note. Notes are stored as JSON objects
// with author/body/at (internal/state appendComment); anything else is
// skipped rather than shown raw.
func lastComment(comments []json.RawMessage) (author, body, at string) {
	for i := len(comments) - 1; i >= 0; i-- {
		var c struct {
			Author string `json:"author"`
			Body   string `json:"body"`
			At     string `json:"at"`
		}
		if json.Unmarshal(comments[i], &c) == nil && strings.TrimSpace(c.Body) != "" {
			author = c.Author
			if author == "" {
				author = "orch"
			}
			return author, strings.TrimSpace(c.Body), c.At
		}
	}
	return "", "", ""
}

func uniqueRequirements(s string) []string {
	seen := map[string]bool{}
	var out []string
	for _, id := range requirementID.FindAllString(s, -1) {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func (p ClickUpPlan) when(at string) string {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999-07:00", "2006-01-02 15:04:05"} {
		if ts, err := time.Parse(layout, at); err == nil {
			return ts.UTC().Format("2006-01-02 15:04 UTC")
		}
	}
	return at
}

func (p ClickUpPlan) hours(h float64) string {
	if h < 1 {
		return fmt.Sprintf("%d min", int(math.Round(h*60)))
	}
	return p.number(h, 1) + " h"
}

func (p ClickUpPlan) duration(seconds float64) string {
	m := int(math.Round(seconds / 60))
	if m < 60 {
		return fmt.Sprintf("%d min", m)
	}
	if m%60 == 0 {
		return fmt.Sprintf("%d h", m/60)
	}
	return fmt.Sprintf("%d h %d min", m/60, m%60)
}

// number formats f with at most prec decimals, trailing zeros dropped for
// prec 1, in the language's decimal separator.
func (p ClickUpPlan) number(f float64, prec int) string {
	s := strconv.FormatFloat(f, 'f', prec, 64)
	if prec == 1 {
		s = strings.TrimSuffix(s, ".0")
	}
	return strings.Replace(s, ".", p.words().decimal, 1)
}

func (p ClickUpPlan) thousands(n int) string {
	sep := ","
	if p.words().decimal == "," {
		sep = "."
	}
	s := strconv.Itoa(n)
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteString(sep)
		}
		b.WriteRune(r)
	}
	return b.String()
}
