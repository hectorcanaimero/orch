package dashboard

import (
	"context"
	"net/http"
	"time"

	"github.com/hectorcanaimero/orch/internal/model"
)

// portfolioPayload is `/api/portfolio`'s body: one row per project the
// operator pointed the process at, plus the directories that turned out not
// to be projects at all.
//
// Two lists rather than one with a flag, because "this project is here but
// its database would not answer" and "this directory is not an orch project"
// are different facts and the page does different things with them. A row in
// `projects` can still say `available: false` — see portfolioProject.
type portfolioPayload struct {
	GeneratedAt string             `json:"generated_at"`
	Projects    []portfolioProject `json:"projects"`
	Unavailable []unavailableRow   `json:"unavailable"`
}

// unavailableRow is a directory that could not be opened as a project.
type unavailableRow struct {
	Root   string `json:"root"`
	Reason string `json:"reason"`
}

// portfolioProject is one project's headline.
//
// The field names are the single-project vocabulary, unchanged:
// `velocity_per_day`, `eta_days`, `eta_date`, `confidence` and `blockers[]`
// are `/api/sprint`'s own, and the counters are `graph.Summary`'s, so a
// component that renders one project's panel renders a row here with no
// adapter. Agreed with sonnet-2 before either side was written.
//
// There is no `todo` counter, and its absence is deliberate: `graph.Summary`
// folds backlog and todo into `Backlog` on purpose ("two adjacent numbers
// that always move together read as one number badly"), and inventing the
// split here would make the portfolio disagree with every other view.
//
// No `href` either. The drill-down is always `/p/<project_id>/`, which the
// page composes; a field would be a second source of truth for a one-line
// rule.
type portfolioProject struct {
	ProjectID string `json:"project_id"`
	// ProjectName is tasks.json's `meta.project` — the name a human gave the
	// project — falling back to the id when the file names none. It was the
	// id twice over in the first cut, which is two keys carrying one value
	// and an invitation for the page to render the wrong one.
	ProjectName string `json:"project_name"`
	Root        string `json:"root"`
	// Available is false when this project's state could not be read. The
	// row is still present, with Reason set and the figures at zero: one
	// unreadable project must not blank the page, and a card greyed out with
	// its reason is more use than a project that silently vanished.
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`

	Total       int     `json:"total"`
	Done        int     `json:"done"`
	InProgress  int     `json:"in_progress"`
	Blocked     int     `json:"blocked"`
	Backlog     int     `json:"backlog"`
	PercentDone float64 `json:"percent_done"`

	VelocityPerDay float64  `json:"velocity_per_day"`
	ETADays        *float64 `json:"eta_days"`
	ETADate        *string  `json:"eta_date"`
	Confidence     string   `json:"confidence"`

	// Blockers is capped at portfolioBlockerLimit. `Blocked` is the real
	// count: a row is a headline, and the full list is one click away at
	// /p/<project_id>/.
	Blockers []blockerRow `json:"blockers"`

	Spend     portfolioSpend `json:"spend"`
	LastEvent *lastEventRow  `json:"last_event"`
}

// portfolioSpend is an object rather than a bare number because there are two
// reasons for there to be no figure — nothing spent, and nothing readable —
// and a `null` collapses them into one.
//
// Always available to the operator. `show_spend_to_stakeholder` gates a page a
// client opens; this surface is the operator's own, and a per-project
// stakeholder flag would make a portfolio show spend for some projects and not
// others for a reason that has nothing to do with the portfolio. Decided with
// orch-98.
type portfolioSpend struct {
	Available    bool    `json:"available"`
	TotalCostUSD float64 `json:"total_cost_usd"`
}

// lastEventRow is the newest row in a project's log — enough to answer "is
// anything happening here", which is the portfolio's whole question.
//
// TaskID is empty for a run-level event (`sprint_done`), and that is not a
// missing value: the event belongs to the run.
type lastEventRow struct {
	EventType string `json:"event_type"`
	TaskID    string `json:"task_id"`
	TS        string `json:"ts"`
}

// portfolioBlockerLimit is the same `reasons[:3]` cap the executive summary
// applies, and for the same reason: a row is a headline, not the blocked list.
const portfolioBlockerLimit = 3

// portfolioRow computes one project's row from the same functions the
// single-project routes use.
//
// `sprintHealth` and `graph.Summarize` are not re-implemented here — that is
// the point. A portfolio that computed its own velocity would eventually
// disagree with the panel the operator sees after clicking through, and the
// two numbers would both look right.
//
// Never returns an error. Every read that fails produces a row with
// `Available: false` and the reason in it, because the alternative is one
// project's locked database turning the whole page into a 500.
func (s *Server) portfolioRow(ctx context.Context, now time.Time) portfolioProject {
	row := portfolioProject{
		ProjectID:   s.paths.ID,
		ProjectName: s.paths.ID,
		Root:        s.paths.Root,
		Blockers:    []blockerRow{},
	}
	if s.state == nil {
		row.Reason = errNoBackend.Error()
		return row
	}

	view, err := s.loadView(ctx)
	if err != nil {
		row.Reason = err.Error()
		return row
	}
	row.Available = true
	if view.ProjectName != "" {
		row.ProjectName = view.ProjectName
	}
	row.Total = view.Summary.Total
	row.Done = view.Summary.Done
	row.InProgress = view.Summary.InProgress
	row.Blocked = view.Summary.Blocked
	row.Backlog = view.Summary.Backlog
	row.PercentDone = round1(view.Summary.PercentDone)

	// From here on a failed read degrades one section rather than the row:
	// the counters above are already true, and dropping them because the
	// event log would not answer would be throwing away the answer to keep
	// the question tidy.
	// The velocity read and the blocker reasons are two sections, and
	// nesting the second inside the first's `else` made one failure take
	// both: the row went on saying "3 blocked" and listed none, so an
	// operator could not tell "no reasons recorded" from "a query failed".
	// Exactly the thing this function's own rule forbids, and it survived
	// because TestPartialReadKeepsTheCounters only checked that velocity was
	// zero. Caught by orch-opus reviewing G8.5.
	//
	// A failed count becomes a velocity of zero, which `sprintHealth` already
	// renders as no projection at all (`confidence: "none"`, null ETA) rather
	// than as a confident "nothing left to do".
	done7d, err := s.state.CountDoneLastNDays(ctx, velocityWindowDays)
	if err != nil {
		s.log.Error("portfolio: velocity unavailable", "project", s.paths.ID, "err", err)
		done7d = 0
	}

	blockedIDs := make([]string, 0)
	for _, t := range view.Tasks {
		if t.Status == model.StatusBlocked {
			blockedIDs = append(blockedIDs, t.ID)
		}
	}
	lastEvents, evErr := s.state.LastEventByTask(ctx, blockedIDs)
	if evErr != nil {
		s.log.Error("portfolio: blocker reasons unavailable",
			"project", s.paths.ID, "err", evErr)
		lastEvents = nil
	}

	health := sprintHealth(view.Tasks, done7d, lastEvents, now)
	row.VelocityPerDay = health.VelocityPerDay
	row.ETADays = health.ETADays
	row.ETADate = health.ETADate
	row.Confidence = health.Confidence
	row.Blockers = capBlockers(health.Blockers)

	if spends, err := s.state.AllSpend(ctx, time.Time{}); err != nil {
		s.log.Error("portfolio: spend unavailable", "project", s.paths.ID, "err", err)
	} else {
		row.Spend = portfolioSpend{
			Available:    true,
			TotalCostUSD: totalCost(spends, s.pricing()),
		}
	}

	if evs, err := s.state.AllEvents(ctx, 1); err != nil {
		s.log.Error("portfolio: last event unavailable", "project", s.paths.ID, "err", err)
	} else if len(evs) > 0 {
		e := evs[len(evs)-1]
		row.LastEvent = &lastEventRow{EventType: e.EventType, TaskID: e.TaskID, TS: e.TS}
	}
	return row
}

func capBlockers(rows []blockerRow) []blockerRow {
	if rows == nil {
		return []blockerRow{}
	}
	if len(rows) > portfolioBlockerLimit {
		return rows[:portfolioBlockerLimit]
	}
	return rows
}

// portfolioDisabledPayload is what a single-project dashboard answers on
// `/api/portfolio`.
//
// The **404 is the contract** — the page's "is this process a portfolio?"
// check reads the status, not the body. The body is for whoever curls the
// route and needs to know it is a mode they did not start rather than a
// version that lacks the feature. Wording agreed with orch-98.
type portfolioDisabledPayload struct {
	Error string `json:"error"`
	Hint  string `json:"hint"`
}

func handlePortfolioDisabled(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusNotFound, portfolioDisabledPayload{
		Error: "portfolio mode is off",
		Hint:  "start orch dashboard with --portfolio '<glob>' to serve several projects",
	})
}
