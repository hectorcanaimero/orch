package dashboard

import (
	"net/http"
	"time"

	"github.com/hectorcanaimero/orch/internal/graph"
	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/publish/snapshot"
)

// The last two read endpoints of G5.2: where the project is going, and when
// each milestone lands.
//
// Both hang off the same velocity figure, which is the one number in the
// dashboard that is a MEASUREMENT rather than a reading of state. It counts
// completions — see state.CountDoneLastNDays, which diverges from Python here
// on purpose, because Python counts row mtimes.

func (s *Server) sprintRoutes() []route {
	return []route{
		{pattern: "GET /api/sprint", name: "api_sprint", handler: s.handleSprint},
		{pattern: "GET /api/milestones", name: "api_milestones", handler: s.handleMilestones},
	}
}

func (s *Server) handleSprint(w http.ResponseWriter, r *http.Request) {
	if s.state == nil {
		s.failRead(w, "sprint health", errNoBackend)
		return
	}
	view, err := s.loadView(r.Context())
	if err != nil {
		s.failRead(w, "sprint health", err)
		return
	}
	done7d, err := s.state.CountDoneLastNDays(r.Context(), velocityWindowDays)
	if err != nil {
		s.failRead(w, "sprint health", err)
		return
	}

	// Only the blocked tasks need their last event, and asking for exactly
	// those is the difference between one small query and reading the whole
	// event log to render a panel that is usually empty.
	blockedIDs := make([]string, 0)
	for _, t := range view.Tasks {
		if t.Status == model.StatusBlocked {
			blockedIDs = append(blockedIDs, t.ID)
		}
	}
	lastEvents, err := s.state.LastEventByTask(r.Context(), blockedIDs)
	if err != nil {
		s.failRead(w, "sprint health", err)
		return
	}

	writeJSON(w, http.StatusOK, sprintHealth(view.Tasks, done7d, lastEvents, time.Now()))
}

// milestonesPayload is `/api/milestones`'s body.
type milestonesPayload struct {
	Milestones []milestoneRow `json:"milestones"`
}

// milestoneRow is one phase with its progress and its projection.
//
// A milestone is a phase. The `milestones` table migration 004 added never had
// a write path, so a page reading it was empty on every real project and told
// the operator to run a flag that did not exist. Phases are what the portal,
// `orch publish` and `orch report pdf` already call milestones
// (snapshot.PhaseMilestones), and now this page says the same thing they do.
type milestoneRow struct {
	Phase int    `json:"phase"`
	Name  string `json:"name"`
	// Status is "done" when every task is done, "active" once any task has
	// started, and "pending" before that.
	Status     string            `json:"status"`
	Progress   milestoneProgress `json:"progress"`
	InProgress int               `json:"in_progress"`
	Blocked    int               `json:"blocked"`
	// ETA projects the phase's unblocked remaining tasks at the project's
	// velocity with graph.ProjectCompletion, the projection /api/sprint and
	// the portal show, so a phase can never finish after the project does.
	// Null when nothing unblocked remains or there is no velocity to project
	// with; the UI renders "—".
	ETA *phaseETA `json:"eta"`
}

type phaseETA struct {
	ETADate    string  `json:"eta_date"`
	ETADays    float64 `json:"eta_days"`
	Confidence string  `json:"confidence"`
}

type milestoneProgress struct {
	Total int `json:"total"`
	Done  int `json:"done"`
	Pct   int `json:"pct"`
}

func (s *Server) handleMilestones(w http.ResponseWriter, r *http.Request) {
	if s.state == nil {
		s.failRead(w, "milestones", errNoBackend)
		return
	}
	view, err := s.loadView(r.Context())
	if err != nil {
		s.failRead(w, "milestones", err)
		return
	}
	phases := snapshot.PhaseMilestones(view.Tasks, view.Phases, nil)

	// The velocity every phase's ETA is projected from is the PROJECT's, not
	// each phase's own: the team that finishes one phase is the team that
	// finishes the next, and a per-phase velocity would be measured over
	// whatever handful of tasks it holds.
	done7d := 0
	if len(phases) > 0 {
		n, cErr := s.state.CountDoneLastNDays(r.Context(), velocityWindowDays)
		if cErr != nil {
			s.failRead(w, "milestones", cErr)
			return
		}
		done7d = n
	}
	now := time.Now()

	rows := make([]milestoneRow, 0, len(phases))
	for _, m := range phases {
		var eta *phaseETA
		// Blocked tasks are not remaining work, as in sprintHealth: a date for
		// work that cannot start is the number least worth promising.
		if p := graph.ProjectCompletion(m.Total-m.Done-m.Blocked, done7d, velocityWindowDays, now); p != nil {
			eta = &phaseETA{ETADate: p.Date, ETADays: p.Days, Confidence: p.Confidence}
		}
		rows = append(rows, milestoneRow{
			Phase:      m.Phase,
			Name:       m.Name,
			Status:     phaseStatus(m),
			Progress:   milestoneProgress{Total: m.Total, Done: m.Done, Pct: int(m.PercentDone)},
			InProgress: m.InProgress,
			Blocked:    m.Blocked,
			ETA:        eta,
		})
	}
	writeJSON(w, http.StatusOK, milestonesPayload{Milestones: rows})
}

func phaseStatus(m snapshot.Milestone) string {
	switch {
	case m.Complete:
		return "done"
	case m.Done > 0 || m.InProgress > 0 || m.Blocked > 0:
		return "active"
	default:
		return "pending"
	}
}
