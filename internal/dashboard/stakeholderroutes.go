package dashboard

import (
	"net/http"
	"sort"
	"time"

	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/publish/snapshot"
)

// `/stakeholder/summary` — the one route the stakeholder profile exists for.
//
// `stakeholder_summary_json` has been in DefaultStakeholderRoutes since the
// allow-list was ported (#165), naming a route nothing served: the profile
// advertised a page and the server answered it with the SPA's own HTML, which
// crashed the page (`Cannot read properties of undefined (reading 'done')`).
// #206 made that absence honest with a 404; this fills it.
//
// # The payload is Python's, field for field
//
// One compiled `web/` bundle serves both dashboards, so one JSON shape is the
// architecture and not a detail. `publish/snapshot.Build` computes every
// figure here — this maps its output onto the keys the SPA already reads
// rather than recomputing anything, with one exception noted at
// phasesTimeline. Agreed with sonnet-2, who owns the page, before it was
// written.
func (s *Server) stakeholderRoutes() []route {
	return []route{
		{pattern: "GET /stakeholder/summary", name: "stakeholder_summary_json",
			handler: s.handleStakeholderSummary},
	}
}

// stakeholderSummaryPayload is `/stakeholder/summary`'s body, in Python's key
// order (`json.dumps` preserves insertion order, and the SPA reads these
// names).
type stakeholderSummaryPayload struct {
	ProjectID  string                 `json:"project_id"`
	Summary    stakeholderStats       `json:"summary"`
	Milestones []stakeholderMilestone `json:"milestones"`
	// SpendRoundedUSD is null — not zero — when the spend flag is off. Zero
	// would read as "nothing was spent", which is a different claim.
	SpendRoundedUSD *float64 `json:"spend_rounded_usd"`
	ETAHours        *float64 `json:"eta_hours"`
	// ETADate and ETAConfidence are the finish date the Sprint page shows,
	// null and "" when no pace is known. Go only: Python sent hours alone.
	ETADate          *string            `json:"eta_date"`
	ETAConfidence    string             `json:"eta_confidence"`
	RefreshIntervalS int                `json:"refresh_interval_s"`
	PhasesTimeline   []stakeholderPhase `json:"phases_timeline"`
	SpendByDay       []stakeholderDaily `json:"spend_by_day"`
	ExecSummary      string             `json:"exec_summary"`
}

// stakeholderStats is `ProjectSummary.as_dict()` — the same seven figures
// `graph.Summarize` produces, under the same names.
type stakeholderStats struct {
	Total              int     `json:"total"`
	Done               int     `json:"done"`
	InProgress         int     `json:"in_progress"`
	Blocked            int     `json:"blocked"`
	Backlog            int     `json:"backlog"`
	PercentDone        float64 `json:"percent_done"`
	EstimateHoursTotal float64 `json:"estimate_hours_total"`
}

// stakeholderMilestone is `milestones_from_phases`: a phase as a checklist
// row. `done` is "every task in this phase is done", which is the snapshot's
// `complete` under the name Python gave it.
type stakeholderMilestone struct {
	Phase      int  `json:"phase"`
	TotalCount int  `json:"total_count"`
	DoneCount  int  `json:"done_count"`
	Done       bool `json:"done"`
}

// stakeholderPhase is one bar of the phase timeline (Sprint E-7).
type stakeholderPhase struct {
	Phase         int     `json:"phase"`
	Name          string  `json:"name"`
	Total         int     `json:"total"`
	Done          int     `json:"done"`
	InProgress    int     `json:"in_progress"`
	Blocked       int     `json:"blocked"`
	Backlog       int     `json:"backlog"`
	EstimateHours float64 `json:"estimate_hours"`
	PctDone       int     `json:"pct_done"`
}

type stakeholderDaily struct {
	Date string  `json:"date"`
	Cost float64 `json:"cost"`
}

// stakeholderRefreshIntervalS is what the page polls at.
//
// Python reads `config.kanban.refresh_interval_s or 30`; Go dropped the kanban
// config block entirely (a config carrying it still loads, the keys are
// ignored), so the fallback is the only value there has ever been in practice
// and it is spelled once here.
const stakeholderRefreshIntervalS = 30

func (s *Server) handleStakeholderSummary(w http.ResponseWriter, r *http.Request) {
	if s.state == nil {
		s.failRead(w, "stakeholder summary", errNoBackend)
		return
	}
	view, err := s.loadView(r.Context())
	if err != nil {
		s.failRead(w, "stakeholder summary", err)
		return
	}
	events, err := s.state.AllEvents(r.Context(), 0)
	if err != nil {
		s.failRead(w, "stakeholder summary", err)
		return
	}
	// All of history: the figure is a lifetime total, and the daily series
	// does its own trailing-14-day cut inside Build.
	spends, err := s.state.AllSpend(r.Context(), time.Time{})
	if err != nil {
		s.failRead(w, "stakeholder summary", err)
		return
	}
	doneInWindow, err := s.state.CountDoneLastNDays(r.Context(), velocityWindowDays)
	if err != nil {
		s.failRead(w, "stakeholder summary", err)
		return
	}

	snap := snapshot.Build(snapshot.Input{
		Tasks:            view.Tasks,
		Phases:           view.Phases,
		Events:           events,
		Spends:           spends,
		ProjectName:      view.ProjectName,
		RefreshIntervalS: stakeholderRefreshIntervalS,
		Language:         s.cfg.SummaryLanguage,
		ShowSpend:        s.cfg.ShowSpendToStakeholder,
		Now:              time.Now(),

		DoneInVelocityWindow: doneInWindow,
	})

	writeJSON(w, http.StatusOK, stakeholderSummaryPayload{
		ProjectID:        s.paths.ID,
		Summary:          stakeholderStatsFrom(snap.Summary),
		Milestones:       stakeholderMilestonesFrom(snap.Milestones),
		SpendRoundedUSD:  stakeholderSpendTotal(snap.Budget),
		ETAHours:         snap.Summary.ETAHours,
		ETADate:          snap.Summary.ETADate,
		ETAConfidence:    snap.Summary.ETAConfidence,
		RefreshIntervalS: snap.RefreshIntervalS,
		PhasesTimeline:   phasesTimeline(snap.Milestones, view.Tasks),
		SpendByDay:       stakeholderSpendByDay(snap.Budget),
		ExecSummary:      snap.ExecutiveSummary.Text,
	})
}

func stakeholderStatsFrom(s snapshot.Summary) stakeholderStats {
	return stakeholderStats{
		Total: s.Total, Done: s.Done, InProgress: s.InProgress,
		Blocked: s.Blocked, Backlog: s.Backlog,
		PercentDone: s.PercentDone, EstimateHoursTotal: s.EstimateHoursTotal,
	}
}

func stakeholderMilestonesFrom(ms []snapshot.Milestone) []stakeholderMilestone {
	out := make([]stakeholderMilestone, 0, len(ms))
	for _, m := range ms {
		out = append(out, stakeholderMilestone{
			Phase: m.Phase, TotalCount: m.Total, DoneCount: m.Done, Done: m.Complete,
		})
	}
	return out
}

// stakeholderSpendTotal is the rounded-UP total, or nil when the flag is off.
//
// Rounded up on purpose — `snapshot.RoundUpToStep`, the same call the
// executive summary's own spend sentence makes, so the number in the sentence
// and the number in the card cannot differ by a rounding step. Python's
// docstring gives the reason: a displayed spend must never under-report to a
// stakeholder.
func stakeholderSpendTotal(b snapshot.Budget) *float64 {
	if !b.Enabled || b.SpendUSD == nil {
		return nil
	}
	rounded := snapshot.RoundUpToStep(*b.SpendUSD, snapshot.SpendStep)
	return &rounded
}

// stakeholderSpendByDay renames `cost_usd` to the `cost` the SPA reads, and is
// an empty list rather than null when the flag is off — the page ranges over
// it.
func stakeholderSpendByDay(b snapshot.Budget) []stakeholderDaily {
	out := make([]stakeholderDaily, 0, len(b.SpendByDay))
	if !b.Enabled {
		return out
	}
	for _, d := range b.SpendByDay {
		out = append(out, stakeholderDaily{Date: d.Date, Cost: d.CostUSD})
	}
	return out
}

// phasesTimeline maps the snapshot's phase rows onto the SPA's bar shape.
//
// The one figure that is computed here rather than mapped is
// `estimate_hours`: the snapshot totals estimate hours for the project but
// not per phase, so this sums them over the tasks the view already loaded.
// It is a sum, not a second definition of anything — every other number comes
// from the snapshot.
func phasesTimeline(ms []snapshot.Milestone, tasks []model.Task) []stakeholderPhase {
	hours := map[int]float64{}
	for _, t := range tasks {
		hours[t.Phase] = round2(hours[t.Phase] + t.EstimateHours)
	}

	out := make([]stakeholderPhase, 0, len(ms))
	for _, m := range ms {
		// Python's `round(done / total * 100)`. `roundDecimals(_, 0)`
		// formats with `strconv.FormatFloat(..., 'f', 0, _)`, which rounds
		// half to even exactly as CPython's `round` does — `int()` on the
		// raw quotient would truncate, turning 66.7% into 66.
		pct := 0
		if m.Total > 0 {
			pct = int(roundDecimals(float64(m.Done)/float64(m.Total)*100, 0))
		}
		out = append(out, stakeholderPhase{
			Phase: m.Phase, Name: m.Name, Total: m.Total, Done: m.Done,
			InProgress: m.InProgress, Blocked: m.Blocked, Backlog: m.Backlog,
			EstimateHours: hours[m.Phase], PctDone: pct,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Phase < out[j].Phase })
	return out
}
