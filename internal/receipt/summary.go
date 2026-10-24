package receipt

import (
	"fmt"
	"strings"
)

// Summary is a run's receipt in plain words (#456): what was built, what
// stopped and why, and the next one to three things to do, each with the
// command that does it. Deterministic: built from the receipt alone, so
// `orch report summary`, the Now page and the post-run notification say the
// same thing.
type Summary struct {
	Lang    string   `json:"lang"`
	Lines   []string `json:"lines"`
	Actions []Action `json:"actions"`
}

// Action is one next step. Command, when set, is the shell command that does
// it; Text says what to do first.
type Action struct {
	Text    string `json:"text"`
	Command string `json:"command,omitempty"`
}

// maxActions is the issue's "the next 1-3 actions".
const maxActions = 3

// Summary words the receipt in lang (en, es or pt; anything else is en).
// autoMerge is github.auto_merge: a project that merges its own green PRs is
// not told to merge them.
func (r *Receipt) Summary(lang string, autoMerge bool) Summary {
	w, ok := words[lang]
	if !ok {
		lang, w = "en", words["en"]
	}
	s := Summary{Lang: lang, Lines: []string{}, Actions: []Action{}}
	head := w["finished"]
	if !r.Finished {
		head = w["unfinished"]
	}
	s.Lines = append(s.Lines, fmt.Sprintf(head, r.RunID, len(r.Done), len(r.Blocked)))
	if len(r.Done) > 0 {
		s.Lines = append(s.Lines, fmt.Sprintf(w["built"], names(r.Done)))
	}
	for _, b := range r.Blocked {
		why := w["cause."+b.Cause]
		if b.Cause == "failure" {
			why = w["class."+b.FailureClass]
			if why == "" {
				why = w["class.other"]
			}
		}
		if reason := shortReason(b.Reason); reason != "" {
			why += " — " + reason
		}
		line := fmt.Sprintf(w["stopped"], label(b), why)
		if strings.HasSuffix(why, "…") {
			// The cut already ends the sentence.
			line = strings.TrimSuffix(line, ".")
		}
		s.Lines = append(s.Lines, line)
		s.Actions = append(s.Actions, Action{Text: fmt.Sprintf(w[actionKey(b)], b.TaskID, prURL(r, b.TaskID)), Command: retry(b.TaskID)})
	}
	if !autoMerge {
		for _, p := range r.PRs {
			if p.CIStatus == "success" {
				s.Actions = append(s.Actions, Action{Text: fmt.Sprintf(w["merge"], p.URL[strings.LastIndex(p.URL, "/")+1:], p.TaskID, p.URL)})
			}
		}
	}
	if len(s.Actions) > maxActions {
		s.Actions = s.Actions[:maxActions]
	}
	return s
}

// Text is the summary as `orch report summary` prints it and the
// notification posts it.
func (s Summary) Text() string {
	w := words[s.Lang]
	if w == nil {
		w = words["en"]
	}
	var b strings.Builder
	for _, l := range s.Lines {
		b.WriteString(l + "\n")
	}
	b.WriteString("\n" + w["next"] + "\n")
	if len(s.Actions) == 0 {
		b.WriteString(w["nothing"] + "\n")
	}
	for i, a := range s.Actions {
		fmt.Fprintf(&b, "%d. %s\n", i+1, a.Text)
		if a.Command != "" {
			fmt.Fprintf(&b, "   $ %s\n", a.Command)
		}
	}
	return b.String()
}

func retry(id string) string { return "orch task set --id " + id + " --status todo && orch run" }

// actionKey picks what to fix before the retry. A verify that could not
// start the app is the config's to fix, not the agent's.
func actionKey(b TaskRef) string {
	switch b.Cause {
	case "verify":
		if strings.Contains(b.Reason, "app did not start") || strings.Contains(b.Reason, "started the task's app") {
			return "do.verify_app"
		}
		return "do.verify"
	case "failure":
		switch b.FailureClass {
		case "permission", "budget", "rate_limit", "version_drift", "timeout":
			return "do." + b.FailureClass
		}
		return "do.failure"
	}
	return "do." + b.Cause
}

func prURL(r *Receipt, id string) string {
	for _, p := range r.PRs {
		if p.TaskID == id {
			return p.URL
		}
	}
	return "PR"
}

func label(t TaskRef) string {
	if t.Title == "" {
		return t.TaskID
	}
	return t.TaskID + " (" + t.Title + ")"
}

// names lists done tasks, the first five by name.
func names(refs []TaskRef) string {
	var out []string
	for i, t := range refs {
		if i == 5 {
			out = append(out, fmt.Sprintf("+%d", len(refs)-5))
			break
		}
		out = append(out, label(t))
	}
	return strings.Join(out, ", ")
}

// shortReason is a reason's first line without its closing punctuation, cut
// to 160 characters.
func shortReason(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	s = strings.TrimRight(s, ":. ")
	if r := []rune(s); len(r) > 160 {
		return string(r[:159]) + "…"
	}
	return s
}

// words is every sentence the summary uses, per language. In an action,
// %[1]s is the task id and %[2]s its PR's URL.
var words = map[string]map[string]string{
	"en": {
		"finished":            "Run %s finished: %d done, %d blocked.",
		"unfinished":          "Run %s has not finished: %d done, %d blocked so far.",
		"built":               "Built: %s.",
		"stopped":             "%s stopped: %s.",
		"next":                "Next:",
		"nothing":             "Nothing is waiting on you.",
		"cause.verify":        "the local verify rejected its work",
		"cause.ci":            "its pull request kept failing CI",
		"cause.not_started":   "orch could not start it",
		"cause.agent":         "its agent blocked it",
		"class.timeout":       "it ran past its time limit",
		"class.rate_limit":    "the provider's rate limit was hit",
		"class.permission":    "the provider CLI was refused (login or permissions)",
		"class.budget":        "a budget cap was reached",
		"class.version_drift": "the provider rejected the configured model",
		"class.id_spoof":      "its agent reported another task's id",
		"class.parser":        "orch could not read the CLI's output",
		"class.transient":     "the provider failed",
		"class.other":         "its agent failed",
		"do.verify_app":       "Fix verify.app.start in .orchestrator/config.yaml, then retry %[1]s.",
		"do.verify":           "Fix what the verify found (`orch events %[1]s`), then retry %[1]s.",
		"do.ci":               "Fix the failing checks on %[2]s, then retry %[1]s.",
		"do.not_started":      "Fix what stopped it (`orch doctor`), then retry %[1]s.",
		"do.agent":            "Give the agent what it asked for (`orch logs %[1]s`), then retry %[1]s.",
		"do.timeout":          "Split %[1]s or raise its estimate (`orch task edit --id %[1]s --estimate HOURS`), then retry it.",
		"do.rate_limit":       "Wait for the provider's limit to reset, then retry %[1]s.",
		"do.permission":       "Check the provider CLI's login with `orch doctor`, then retry %[1]s.",
		"do.budget":           "Raise the cap in .orchestrator/budgets.yaml or wait for its window, then retry %[1]s.",
		"do.version_drift":    "Fix the model in .orchestrator/model_router.yaml (`orch doctor` names it), then retry %[1]s.",
		"do.failure":          "Read what went wrong (`orch logs %[1]s`), then retry %[1]s.",
		"merge":               "Merge PR #%s (%s): its CI passed — %s",
	},
	"es": {
		"finished":            "La ejecución %s terminó: %d hechas, %d bloqueadas.",
		"unfinished":          "La ejecución %s no ha terminado: %d hechas, %d bloqueadas por ahora.",
		"built":               "Construido: %s.",
		"stopped":             "%s se detuvo: %s.",
		"next":                "Siguiente:",
		"nothing":             "Nada espera por ti.",
		"cause.verify":        "la verificación local rechazó su trabajo",
		"cause.ci":            "su pull request siguió fallando el CI",
		"cause.not_started":   "orch no pudo iniciarla",
		"cause.agent":         "su agente la bloqueó",
		"class.timeout":       "superó su tiempo límite",
		"class.rate_limit":    "se alcanzó el límite de uso del proveedor",
		"class.permission":    "el CLI del proveedor fue rechazado (sesión o permisos)",
		"class.budget":        "se alcanzó un tope de presupuesto",
		"class.version_drift": "el proveedor rechazó el modelo configurado",
		"class.id_spoof":      "su agente reportó el id de otra tarea",
		"class.parser":        "orch no pudo leer la salida del CLI",
		"class.transient":     "el proveedor falló",
		"class.other":         "su agente falló",
		"do.verify_app":       "Corrige verify.app.start en .orchestrator/config.yaml y reintenta %[1]s.",
		"do.verify":           "Corrige lo que encontró la verificación (`orch events %[1]s`) y reintenta %[1]s.",
		"do.ci":               "Corrige los checks que fallan en %[2]s y reintenta %[1]s.",
		"do.not_started":      "Corrige lo que la detuvo (`orch doctor`) y reintenta %[1]s.",
		"do.agent":            "Dale al agente lo que pidió (`orch logs %[1]s`) y reintenta %[1]s.",
		"do.timeout":          "Divide %[1]s o sube su estimación (`orch task edit --id %[1]s --estimate HORAS`) y reinténtala.",
		"do.rate_limit":       "Espera a que el límite del proveedor se reinicie y reintenta %[1]s.",
		"do.permission":       "Revisa la sesión del CLI del proveedor con `orch doctor` y reintenta %[1]s.",
		"do.budget":           "Sube el tope en .orchestrator/budgets.yaml o espera su ventana y reintenta %[1]s.",
		"do.version_drift":    "Corrige el modelo en .orchestrator/model_router.yaml (`orch doctor` lo nombra) y reintenta %[1]s.",
		"do.failure":          "Lee qué salió mal (`orch logs %[1]s`) y reintenta %[1]s.",
		"merge":               "Fusiona el PR #%s (%s): su CI pasó — %s",
	},
	"pt": {
		"finished":            "A execução %s terminou: %d concluídas, %d bloqueadas.",
		"unfinished":          "A execução %s não terminou: %d concluídas, %d bloqueadas até agora.",
		"built":               "Construído: %s.",
		"stopped":             "%s parou: %s.",
		"next":                "Próximos passos:",
		"nothing":             "Nada está esperando por você.",
		"cause.verify":        "a verificação local rejeitou o trabalho",
		"cause.ci":            "o pull request continuou falhando no CI",
		"cause.not_started":   "o orch não conseguiu iniciá-la",
		"cause.agent":         "o agente a bloqueou",
		"class.timeout":       "passou do limite de tempo",
		"class.rate_limit":    "o limite de uso do provedor foi atingido",
		"class.permission":    "o CLI do provedor foi recusado (login ou permissões)",
		"class.budget":        "um limite de orçamento foi atingido",
		"class.version_drift": "o provedor recusou o modelo configurado",
		"class.id_spoof":      "o agente informou o id de outra tarefa",
		"class.parser":        "o orch não conseguiu ler a saída do CLI",
		"class.transient":     "o provedor falhou",
		"class.other":         "o agente falhou",
		"do.verify_app":       "Corrija verify.app.start em .orchestrator/config.yaml e tente %[1]s de novo.",
		"do.verify":           "Corrija o que a verificação encontrou (`orch events %[1]s`) e tente %[1]s de novo.",
		"do.ci":               "Corrija as verificações que falham em %[2]s e tente %[1]s de novo.",
		"do.not_started":      "Corrija o que a impediu (`orch doctor`) e tente %[1]s de novo.",
		"do.agent":            "Dê ao agente o que ele pediu (`orch logs %[1]s`) e tente %[1]s de novo.",
		"do.timeout":          "Divida %[1]s ou aumente a estimativa (`orch task edit --id %[1]s --estimate HORAS`) e tente de novo.",
		"do.rate_limit":       "Espere o limite do provedor reiniciar e tente %[1]s de novo.",
		"do.permission":       "Verifique o login do CLI do provedor com `orch doctor` e tente %[1]s de novo.",
		"do.budget":           "Aumente o limite em .orchestrator/budgets.yaml ou espere a janela e tente %[1]s de novo.",
		"do.version_drift":    "Corrija o modelo em .orchestrator/model_router.yaml (`orch doctor` o indica) e tente %[1]s de novo.",
		"do.failure":          "Leia o que deu errado (`orch logs %[1]s`) e tente %[1]s de novo.",
		"merge":               "Faça o merge do PR #%s (%s): o CI passou — %s",
	},
}
