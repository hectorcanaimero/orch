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
	// Branding is the white-label block, already resolved: the logo is a
	// data URI by the time it arrives, because reading a file is the
	// caller's job and this package shapes what it is given.
	Branding Branding
	// Now is injected rather than read from the clock so a snapshot is
	// reproducible in a test.
	Now time.Time
	// DoneInVelocityWindow is how many tasks finished in the last
	// VelocityWindowDays (state.CountDoneLastNDays). Zero means no pace is
	// known, and the summary falls back to the hours estimate.
	DoneInVelocityWindow int
	// PhaseTitles and PackageTitles are project.SpecOutline's names, used
	// where tasks.json has none. FinishedAt maps a task id to when it first
	// reached done (tasks_runtime.finished_at). All three may be nil.
	PhaseTitles   map[int]string
	PackageTitles map[string]string
	FinishedAt    map[string]string
	// Documents are the project files the portal shows (portal.documents),
	// already read by the caller.
	Documents []Document
	// Gates are the kinds of check every pull request goes through
	// (internal/ci.Gates), nil when tasks do not go through PRs. CI is each
	// task's pull-request CI result, by id. Together they make Quality.
	Gates []string
	CI    map[string]TaskCI
}

// VelocityWindowDays is the window DoneInVelocityWindow is counted over —
// the same seven days the dashboard's Sprint page measures velocity on.
const VelocityWindowDays = 7

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
	// Branding is omitted entirely when nothing is configured, which is what
	// keeps a snapshot without it byte-identical to one built before the
	// field existed. See docs/SNAPSHOT-SCHEMA.md for why this is additive
	// rather than a schema bump.
	Branding *Branding `json:"branding,omitempty"`
	// Deliveries is what was finished in the last DeliveriesWindowDays,
	// newest first — the portal's "since your last visit". Omitted when
	// nothing was.
	Deliveries []Delivery `json:"deliveries,omitempty"`
	// Documents are the files the operator chose to share, by title and
	// never by path. Omitted when there are none.
	Documents []Document `json:"documents,omitempty"`
	// Quality is how deliveries are checked, omitted when no check runs on
	// pull requests.
	Quality *Quality `json:"quality,omitempty"`
}

// Document is one shared project file: a stable id for linking, its title,
// when it last changed, and its markdown (frontmatter stripped).
type Document struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	UpdatedAt string `json:"updated_at"`
	Markdown  string `json:"markdown"`
}

// DeliveriesWindowDays is how far back Deliveries reaches.
const DeliveriesWindowDays = 30

// deliveriesLimit caps Deliveries: it is a headline, not the history.
const deliveriesLimit = 30

// Delivery is one finished deliverable, by title — never by id.
type Delivery struct {
	Title      string `json:"title"`
	Phase      string `json:"phase"`
	FinishedAt string `json:"finished_at"`
}

// Package is one group of deliverables inside a phase: an atomizer package
// (F6.1) named from its spec, or the unnamed group of tasks outside that
// scheme.
type Package struct {
	Name         string        `json:"name"`
	Total        int           `json:"total"`
	Done         int           `json:"done"`
	Deliverables []Deliverable `json:"deliverables"`
}

// Deliverable is one task as a client reads it: a title and a state.
type Deliverable struct {
	Title string `json:"title"`
	// Status is done | in_progress | blocked | pending (backlog and todo are
	// one state to a client: not started).
	Status     string `json:"status"`
	FinishedAt string `json:"finished_at,omitempty"`
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
	// ETADate is the projected finish day (YYYY-MM-DD) at the measured pace,
	// graph.ProjectCompletion — the date the Sprint page shows. Absent when
	// no pace is known. ETAConfidence is "high" or "low" alongside it.
	ETADate       *string `json:"eta_date,omitempty"`
	ETAConfidence string  `json:"eta_confidence,omitempty"`
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
	// Packages is the phase's deliverables grouped by package, in id order.
	Packages []Package `json:"packages,omitempty"`
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
	projection := graph.ProjectCompletion(remainingTasks(in.Tasks), in.DoneInVelocityWindow, VelocityWindowDays, in.Now)

	milestones := buildMilestones(in.Tasks, in.Phases, in.PhaseTitles)
	addPackages(milestones, in.Tasks, in.PackageTitles, in.FinishedAt)
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
			ETADate:            projectionDate(projection),
			ETAConfidence:      projectionConfidence(projection),
		},
		Milestones: milestones,
		Blockers:   blockers,
		Budget: Budget{
			Enabled:    in.ShowSpend,
			SpendUSD:   spendUSD,
			SpendByDay: spendByDay,
		},
		ExecutiveSummary: ExecutiveSummary{
			Text:     executiveSummary(lang, summary, eta, projection, totalSpendForSummary, blockers),
			Language: lang,
		},
		Branding:   brandingOrNil(in.Branding),
		Deliveries: buildDeliveries(in.Tasks, milestones, in.FinishedAt, in.Now),
		Documents:  in.Documents,
		Quality:    buildQuality(in.Tasks, in.Gates, in.CI),
	}
}

// remainingTasks is what the velocity projection counts down: tasks neither
// done nor blocked, as the Sprint page counts them.
func remainingTasks(tasks []model.Task) int {
	n := 0
	for _, t := range tasks {
		if t.Status != model.StatusDone && t.Status != model.StatusBlocked {
			n++
		}
	}
	return n
}

func projectionDate(p *graph.Projection) *string {
	if p == nil {
		return nil
	}
	return &p.Date
}

func projectionConfidence(p *graph.Projection) string {
	if p == nil {
		return ""
	}
	return p.Confidence
}

// addPackages groups each phase's tasks into packages, in task-id order.
func addPackages(milestones []Milestone, tasks []model.Task, titles map[string]string, finishedAt map[string]string) {
	byPhase := map[int][]model.Task{}
	for _, t := range tasks {
		byPhase[t.Phase] = append(byPhase[t.Phase], t)
	}
	for i := range milestones {
		phaseTasks := byPhase[milestones[i].Phase]
		sort.SliceStable(phaseTasks, func(a, b int) bool { return phaseTasks[a].ID < phaseTasks[b].ID })
		index := map[string]int{}
		var packages []Package
		for _, t := range phaseTasks {
			key := project.PackageKey(t.ID)
			name := ""
			if key != "" {
				name = titles[key]
				if name == "" {
					name = "F" + key
				}
			}
			pi, ok := index[name]
			if !ok {
				pi = len(packages)
				index[name] = pi
				packages = append(packages, Package{Name: name})
			}
			d := Deliverable{Title: t.Title, Status: clientStatus(t.Status)}
			if t.Status == model.StatusDone {
				d.FinishedAt = finishedAt[t.ID]
				packages[pi].Done++
			}
			packages[pi].Total++
			packages[pi].Deliverables = append(packages[pi].Deliverables, d)
		}
		// Unnamed groups last: named packages are the plan, the rest was added.
		sort.SliceStable(packages, func(a, b int) bool { return packages[a].Name != "" && packages[b].Name == "" })
		milestones[i].Packages = packages
	}
}

// clientStatus is a task status in the four states a client distinguishes.
func clientStatus(s model.Status) string {
	switch s {
	case model.StatusDone:
		return "done"
	case model.StatusInProgress:
		return "in_progress"
	case model.StatusBlocked:
		return "blocked"
	default:
		return "pending"
	}
}

// buildDeliveries lists what was finished in the last DeliveriesWindowDays,
// newest first. Timestamps are parsed, not compared as strings: one orch.db
// holds both the +00:00 and the Z spelling.
func buildDeliveries(tasks []model.Task, milestones []Milestone, finishedAt map[string]string, now time.Time) []Delivery {
	phaseName := map[int]string{}
	for _, m := range milestones {
		phaseName[m.Phase] = m.Name
	}
	cutoff := now.Add(-DeliveriesWindowDays * 24 * time.Hour)
	type dated struct {
		d  Delivery
		at time.Time
	}
	var rows []dated
	for _, t := range tasks {
		if t.Status != model.StatusDone {
			continue
		}
		at, err := time.Parse(time.RFC3339, finishedAt[t.ID])
		if err != nil || at.Before(cutoff) || at.After(now) {
			continue
		}
		rows = append(rows, dated{Delivery{Title: t.Title, Phase: phaseName[t.Phase], FinishedAt: at.UTC().Format(time.RFC3339)}, at})
	}
	sort.SliceStable(rows, func(a, b int) bool { return rows[a].at.After(rows[b].at) })
	if len(rows) > deliveriesLimit {
		rows = rows[:deliveriesLimit]
	}
	out := make([]Delivery, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.d)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// brandingOrNil keeps the field absent rather than present-and-empty.
//
// `omitempty` on a struct does not omit it — only a nil pointer does — so an
// unbranded project would otherwise grow `"branding":{}` and stop being
// byte-identical to what it produced yesterday.
func brandingOrNil(b Branding) *Branding {
	if b.Empty() {
		return nil
	}
	return &b
}

// buildMilestones reshapes graph.PhaseCounts (aggregate, id-free already)
// into the stakeholder's milestone shape, adding the phase name from
// tasks.json's own `phases[]` list — the same lookup Python's
// `_stakeholder_payload` does, falling back to "Phase N" when a project has
// no name for it (or none at all: plenty of fixtures predate `phases[]`).
func buildMilestones(tasks []model.Task, phases []model.Phase, specTitles map[int]string) []Milestone {
	names := make(map[int]string, len(phases))
	for n, title := range specTitles {
		names[n] = title
	}
	for _, p := range phases {
		if p.Name != "" {
			names[p.ID] = p.Name
		}
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
