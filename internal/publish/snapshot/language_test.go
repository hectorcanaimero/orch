package snapshot

import (
	"strings"
	"testing"
	"time"

	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/providers"
	"github.com/hectorcanaimero/orch/internal/state"
)

// Portuguese is a first-class language, not a fallback to Spanish: a client
// reading pt gets every sentence of the summary, and every blocker reason, in
// Portuguese.
func TestPortugueseExecutiveSummary(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	in := fixtureInput(now)
	in.Language = "pt"
	in.Tasks = append(in.Tasks, model.Task{ID: "B", Status: model.StatusTodo, EstimateHours: 3})
	in.DoneInVelocityWindow = 7

	snap := Build(in)
	if snap.ExecutiveSummary.Language != "pt" {
		t.Fatalf("language = %q, want pt", snap.ExecutiveSummary.Language)
	}
	text := snap.ExecutiveSummary.Text
	for _, want := range []string{
		"Projeto 25% concluído — 1 de 4 tarefas entregues.",
		"1 em andamento.",
		"1 bloqueada(s) — precisam de atenção.",
		"Previsão de conclusão: 16 set (confiança alta).",
		"Gasto com IA: $1.50.",
		"Bloqueios:\n• Wire the router: " + reasonPhrases["pt"][providers.FailureRateLimit],
	} {
		if !strings.Contains(text, want) {
			t.Errorf("pt summary = %q\nwant it to contain %q", text, want)
		}
	}
}

// A language added to one table and forgotten in another would print an
// empty reason to a client; every table carries every language.
func TestEveryLanguageHasEveryPhrase(t *testing.T) {
	for _, lang := range Languages {
		if genericBlockedReason[lang] == "" {
			t.Errorf("%s: no generic blocked reason", lang)
		}
		if ciBlockedReason[lang] == "" || notStartedReason[lang] == "" {
			t.Errorf("%s: missing the CI or not-started reason", lang)
		}
		for class := range reasonPhrases["en"] {
			if reasonPhrases[lang][class] == "" {
				t.Errorf("%s: no phrase for failure class %q", lang, class)
			}
		}
		if _, ok := summaryPhrasesByLang[lang]; !ok {
			t.Errorf("%s: no executive summary phrases", lang)
		}
	}
}

// The reason names what actually blocked the task — the most recent signal
// the event log carries — instead of one generic sentence for every blocker.
// Still a closed vocabulary: the free-text `reason` never reaches a client.
func TestBlockerReasonComesFromWhatBlockedIt(t *testing.T) {
	blocked := []model.Task{{ID: "T", Title: "Payments", Status: model.StatusBlocked}}
	cases := []struct {
		name   string
		events []state.Event
		want   string
	}{
		{"CI failed on its pull request", []state.Event{
			{TaskID: "T", EventType: "fail", TS: "2026-09-05T00:00:00Z",
				Extra: map[string]any{"failure_class": "timeout"}},
			{TaskID: "T", EventType: "ci_blocked", TS: "2026-09-06T00:00:00+00:00",
				Extra: map[string]any{"reason": "CI failed: lint job exit 1"}},
		}, ciBlockedReason["en"]},
		{"could not be started", []state.Event{
			{TaskID: "T", EventType: "block", TS: "2026-09-06T00:00:00Z",
				Extra: map[string]any{"reason": "no route for backend foo"}},
		}, notStartedReason["en"]},
		{"retries ran out on a class this table does not know", []state.Event{
			{TaskID: "T", EventType: "fail", TS: "2026-09-06T00:00:00Z",
				Extra: map[string]any{"failure_class": "tests", "reason": "test_x FAILED"}},
		}, reasonPhrases["en"][providers.FailureOther]},
		{"the latest signal wins, whatever the timestamp spelling", []state.Event{
			{TaskID: "T", EventType: "ci_blocked", TS: "2026-09-05T23:00:00+00:00"},
			{TaskID: "T", EventType: "fail", TS: "2026-09-06T00:00:00Z",
				Extra: map[string]any{"failure_class": "permission"}},
		}, reasonPhrases["en"][providers.FailurePermission]},
		{"a person blocked it: nothing in the log says why", nil, genericBlockedReason["en"]},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := buildBlockers(blocked, c.events, "en")
			if len(got) != 1 || got[0].Reason != c.want {
				t.Errorf("blockers = %+v, want reason %q", got, c.want)
			}
			if strings.Contains(got[0].Reason, "FAILED") || strings.Contains(got[0].Reason, "exit") {
				t.Errorf("raw text leaked: %q", got[0].Reason)
			}
		})
	}
}
