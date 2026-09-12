package dashboard

import (
	"net/http"
	"time"

	"github.com/hectorcanaimero/orch/internal/model"
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

// milestoneRow is one milestone with its progress and its projection.
type milestoneRow struct {
	ID          string            `json:"id"`
	Title       string            `json:"title"`
	Description string            `json:"description"`
	TargetDate  string            `json:"target_date"`
	Status      string            `json:"status"`
	CreatedAt   string            `json:"created_at"`
	Progress    milestoneProgress `json:"progress"`
	// ETA is null when there is nothing left or no velocity to project with.
	// The UI renders "—", which is honest where a date would not be.
	ETA *etaPayload `json:"eta"`
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
	milestones, err := s.state.Milestones(r.Context())
	if err != nil {
		s.failRead(w, "milestones", err)
		return
	}

	// The velocity every milestone's ETA is projected from is the PROJECT's,
	// not each milestone's own. Python does the same, and it is the honest
	// choice with the data available: the team that finishes a milestone is
	// the team that finishes the next one, and a per-milestone velocity would
	// be measured over whatever handful of tasks it holds.
	velocity := 0.0
	if len(milestones) > 0 {
		done7d, cErr := s.state.CountDoneLastNDays(r.Context(), velocityWindowDays)
		if cErr != nil {
			s.failRead(w, "milestones", cErr)
			return
		}
		velocity = round2(float64(done7d) / float64(velocityWindowDays))
	}
	today := time.Now().UTC().Format("2006-01-02")

	rows := make([]milestoneRow, 0, len(milestones))
	for _, m := range milestones {
		rows = append(rows, milestoneRow{
			ID: m.ID, Title: m.Title, Description: m.Description,
			TargetDate: m.TargetDate, Status: m.Status, CreatedAt: m.CreatedAt,
			Progress: milestoneProgress{Total: m.Total, Done: m.Done, Pct: m.PercentDone},
			ETA:      milestoneETA(m.Total-m.Done, velocity, today, m.TargetDate),
		})
	}
	writeJSON(w, http.StatusOK, milestonesPayload{Milestones: rows})
}
