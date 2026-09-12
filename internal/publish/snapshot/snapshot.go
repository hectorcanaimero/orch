// Package snapshot builds the versioned, stakeholder-safe project snapshot
// (G6.1): the one JSON document a static `orch publish` export, a watch
// server, or a dashboard's stakeholder route can all hand to a viewer with
// no login and no access to the operator's project tree.
//
// Contract, agreed with orch-98: `schema: 1`; no task ids, no backend/provider
// names, no file paths, no raw log or error text — a fixed translation table
// (translate.go) turns the technical failure taxonomy `internal/providers`
// already has into a short business-language sentence instead. Every field
// this package can emit is enumerated by SnapshotJSONFields in the sibling
// test file, and TestNoOperatorFields fails the build the day a new field is
// added here without also being added to that list — the check is an
// explicit allow-list, never a golden-file diff, per the contract.
//
// Deliberately pure: Build takes plain data (already-hydrated tasks, the
// event log, spend rows, phase names, a language and a "now") and returns a
// value. It does not open a database or read tasks.json itself — the caller
// (a CLI command, the dashboard, or `orch publish`'s exporter) already has
// those, via internal/project and internal/state, and importing state.Backend
// here would make every future consumer's test carry a database it doesn't
// need.
package snapshot

import (
	"sort"
	"strconv"
	"time"

	"github.com/hectorcanaimero/orch/internal/graph"
	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/project"
	"github.com/hectorcanaimero/orch/internal/providers"
	"github.com/hectorcanaimero/orch/internal/state"
)

// Schema is the current snapshot format version. A future incompatible
// change bumps this rather than growing an optional field forever — a
// stakeholder.html built against schema 1 should refuse a schema 2 document
// loudly rather than render a half-understood one.
const Schema = 1

// Input is everything Build needs, already gathered by the caller.
type Input struct {
	// Tasks must already be hydrated with live status (project.Hydrate) —
	// Build does not know what "live" means, only what a task's current
	// status is said to be.
	Tasks  []model.Task
	Phases []model.Phase // tasks.json's meta.phases, for milestone names
	Events []state.Event
	Spends []state.Spend

	ProjectName      string
	RefreshIntervalS int
	// Language selects the translation table and the executive summary's
	// wording — "es" or "en". Empty is treated as "es", matching
	// config.Dashboard.SummaryLanguage's own default.
	Language string
	// ShowSpend gates the Budget section on config.yaml's
	// show_spend_to_stakeholder (default off — "spend is sensitive", per
	// that key's own Python comment). Python's HTTP endpoint computes and
	// returns spend regardless of this flag; only the SPA's own rendering
	// respects it, so a direct fetch of /stakeholder/summary leaks spend
	// even with the flag off. Closing that gap here, at the point the
	// document is built, is not a new feature — it is the documented
	// intent of a flag that already exists — see
	// docs/brainstorm/go-migration-notes/sonnet.md.
	ShowSpend bool
	// Now is injected rather than read from the clock so a snapshot is
	// reproducible in a test.
	Now time.Time
}

// Snapshot is the document itself.
type Snapshot struct {
	Schema           int              `json:"schema"`
	GeneratedAt      string           `json:"generated_at"`
	ProjectName      string           `json:"project_name"`
	RefreshIntervalS int              `json:"refresh_interval_s"`
	Summary          Summary          `json:"summary"`
	Milestones       []Milestone      `json:"milestones"`
	Blockers         []Blocker        `json:"blockers"`
	Budget           Budget           `json:"budget"`
	ExecutiveSummary ExecutiveSummary `json:"executive_summary"`
}

// Summary is the header bar — the same seven figures
// `graph.Summarize`/`ProjectSummary.as_dict` already produce, plus the one
// figure that is genuinely new here: an ETA.
type Summary struct {
	Total              int     `json:"total"`
	Done               int     `json:"done"`
	InProgress         int     `json:"in_progress"`
	Blocked            int     `json:"blocked"`
	Backlog            int     `json:"backlog"`
	PercentDone        float64 `json:"percent_done"`
	EstimateHoursTotal float64 `json:"estimate_hours_total"`
	// ETAHours is nil when there is no signal yet (nothing done, or nothing
	// left) — rendering "—" is the caller's job; a 0 here would be read as
	// "no time left", which is a different fact.
	ETAHours *float64 `json:"eta_hours"`
}

// Milestone is one phase's progress. Phase numbers are not task ids — they
// are the same small integers tasks.json's own `phases[]` list and every
// task's `phase` field already carry, and Python's own phase timeline
// exposes them the same way.
type Milestone struct {
	Phase       int     `json:"phase"`
	Name        string  `json:"name"`
	Total       int     `json:"total"`
	Done        int     `json:"done"`
	InProgress  int     `json:"in_progress"`
	Blocked     int     `json:"blocked"`
	Backlog     int     `json:"backlog"`
	PercentDone float64 `json:"percent_done"`
	Complete    bool    `json:"complete"`
}

// Blocker is one blocked task, named by title (never by id) with a
// business-language reason (never the raw comment or exit code).
type Blocker struct {
	Phase  int    `json:"phase"`
	Title  string `json:"title"`
	Reason string `json:"reason"`
}

// Budget is accumulated AI spend. No per-provider breakdown, no limit —
// Python's own stakeholder payload has neither (limits are operator-only,
// via /api/config).
type Budget struct {
	Enabled    bool         `json:"enabled"`
	SpendUSD   *float64     `json:"spend_usd,omitempty"`
	SpendByDay []DailySpend `json:"spend_by_day,omitempty"`
}

// DailySpend is one day's total, over the trailing 14 days.
type DailySpend struct {
	Date    string  `json:"date"`
	CostUSD float64 `json:"cost_usd"`
}

// ExecutiveSummary is the deterministic, no-LLM business-language sentence —
// ported from `executive_summary` in orchestrator/dashboard/metrics.py,
// trimmed to the one call shape the stakeholder view actually uses (ETA in
// hours, never an ETA date — nothing here computes one).
type ExecutiveSummary struct {
	Text     string `json:"text"`
	Language string `json:"language"`
}

// Build assembles the snapshot from already-gathered project data.
func Build(in Input) Snapshot {
	lang := in.Language
	if lang != "es" && lang != "en" {
		lang = "es"
	}

	summary := graph.Summarize(in.Tasks)
	humanHours := project.HumanHoursByTask(in.Events)
	eta := etaHoursRemaining(in.Tasks, humanHours)

	milestones := buildMilestones(in.Tasks, in.Phases)
	blockers := buildBlockers(in.Tasks, in.Events, lang)

	var spendUSD *float64
	var spendByDay []DailySpend
	var totalSpendForSummary *float64
	if in.ShowSpend {
		total := round4(totalSpend(in.Spends))
		spendUSD = &total
		spendByDay = spendByDayLast14(in.Spends, in.Now)
		rounded := roundUpToStep(total, 0.5)
		totalSpendForSummary = &rounded
	}

	return Snapshot{
		Schema:           Schema,
		GeneratedAt:      in.Now.UTC().Format(time.RFC3339),
		ProjectName:      in.ProjectName,
		RefreshIntervalS: in.RefreshIntervalS,
		Summary: Summary{
			Total:              summary.Total,
			Done:               summary.Done,
			InProgress:         summary.InProgress,
			Blocked:            summary.Blocked,
			Backlog:            summary.Backlog,
			PercentDone:        round1(summary.PercentDone),
			EstimateHoursTotal: round1(summary.EstimateHoursTotal),
			ETAHours:           eta,
		},
		Milestones: milestones,
		Blockers:   blockers,
		Budget: Budget{
			Enabled:    in.ShowSpend,
			SpendUSD:   spendUSD,
			SpendByDay: spendByDay,
		},
		ExecutiveSummary: ExecutiveSummary{
			Text:     executiveSummary(lang, summary, eta, totalSpendForSummary, blockers),
			Language: lang,
		},
	}
}

// buildMilestones reshapes graph.PhaseCounts (aggregate, id-free already)
// into the stakeholder's milestone shape, adding the phase name from
// tasks.json's own `phases[]` list — the same lookup Python's
// `_stakeholder_payload` does, falling back to "Phase N" when a project has
// no name for it (or none at all: plenty of fixtures predate `phases[]`).
func buildMilestones(tasks []model.Task, phases []model.Phase) []Milestone {
	names := make(map[int]string, len(phases))
	for _, p := range phases {
		names[p.ID] = p.Name
	}
	rows := graph.PhaseCounts(tasks)
	out := make([]Milestone, 0, len(rows))
	for _, r := range rows {
		name := names[r.Phase]
		if name == "" {
			name = "Phase " + strconv.Itoa(r.Phase)
		}
		pct := 0.0
		if r.Total > 0 {
			pct = round1(float64(r.Done) / float64(r.Total) * 100)
		}
		out = append(out, Milestone{
			Phase:       r.Phase,
			Name:        name,
			Total:       r.Total,
			Done:        r.Done,
			InProgress:  r.InProgress,
			Blocked:     r.Blocked,
			Backlog:     r.Total - r.Done - r.InProgress - r.Blocked,
			PercentDone: pct,
			Complete:    r.Total > 0 && r.Done == r.Total,
		})
	}
	return out
}

// buildBlockers lists every blocked task by title and phase, with a
// business-language reason drawn from the translation table — never the raw
// comment or exit code a blocked task's events may carry. Sorted by
// (phase, title) for a stable document.
func buildBlockers(tasks []model.Task, events []state.Event, lang string) []Blocker {
	latestFailure := latestFailureClassByTask(events)

	out := make([]Blocker, 0)
	for _, t := range tasks {
		if t.Status != model.StatusBlocked {
			continue
		}
		class, ok := latestFailure[t.ID]
		out = append(out, Blocker{
			Phase:  t.Phase,
			Title:  t.Title,
			Reason: translateReason(lang, class, ok),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Phase != out[j].Phase {
			return out[i].Phase < out[j].Phase
		}
		return out[i].Title < out[j].Title
	})
	return out
}

// latestFailureClassByTask finds each task's most recent fail/timeout
// event's `failure_class`, the one structured, closed-vocabulary field the
// event log carries for why a task stopped — as opposed to `reason`, which
// is free text a human or a CLI wrote and is never surfaced here.
func latestFailureClassByTask(events []state.Event) map[string]providers.Failure {
	out := map[string]providers.Failure{}
	latestTS := map[string]string{}
	for _, e := range events {
		if e.EventType != "fail" && e.EventType != "timeout" {
			continue
		}
		raw, ok := e.Extra["failure_class"]
		if !ok {
			continue
		}
		class, ok := raw.(string)
		if !ok || class == "" {
			continue
		}
		if e.TS < latestTS[e.TaskID] {
			continue
		}
		latestTS[e.TaskID] = e.TS
		out[e.TaskID] = providers.Failure(class)
	}
	return out
}
