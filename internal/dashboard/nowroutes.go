package dashboard

import (
	"errors"
	"net/http"
	"time"

	"github.com/hectorcanaimero/orch/internal/budget"
	"github.com/hectorcanaimero/orch/internal/config"
	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/state"
)

// /api/now is the operator's first screen: what is running this minute, what
// needs a person, and how the project is pacing. Every figure on it is one the
// dashboard already serves somewhere else — the pace is /api/sprint's, the
// budget waits are /api/budget/summary's — gathered into one read so the page
// that answers "is anything wrong right now" does not need five.
//
// Read-only like the rest of the package (CHECKLIST rule 13): the attention
// list names what to do and the page shows the command, it does not do it.

func (s *Server) nowRoutes() []route {
	return []route{
		{pattern: "GET /api/now", name: "api_now", handler: s.handleNow},
	}
}

type nowPayload struct {
	// Project is tasks.json's meta.project.
	Project string `json:"project"`
	// Run is the most recent run, live or finished; null when the project has
	// never run. A run is "live" as recorded — the dashboard does not probe
	// its process, so a run that crashed reads live until the next one starts.
	Run     *nowRun        `json:"run"`
	Working []nowDispatch  `json:"working"`
	Slots   nowSlots       `json:"slots"`
	Summary summaryPayload `json:"summary"`
	Pace    sprintPayload  `json:"pace"`
	// Attention is the blocked tasks with their reason, /api/sprint's blockers.
	Attention        []blockerRow     `json:"attention"`
	WaitingForBudget []budget.Waiting `json:"waiting_for_budget"`
}

type nowRun struct {
	RunID     string `json:"run_id"`
	StartedAt string `json:"started_at"`
	UpdatedAt string `json:"updated_at"`
	Mode      string `json:"mode"`
	Status    string `json:"status"`
}

// nowDispatch is one agent at work: the dispatch row joined with its task.
type nowDispatch struct {
	TaskID   string `json:"task_id"`
	Title    string `json:"title"`
	Phase    int    `json:"phase"`
	Provider string `json:"provider"`
	// Model is the task's route in tasks.json; the dispatch row keeps only the
	// backend.
	Model   string `json:"model"`
	Attempt int    `json:"attempt"`
	// MaxAttempts is config.yaml's retry.max_attempts; 0 when unset.
	MaxAttempts int    `json:"max_attempts"`
	StartedAt   string `json:"started_at"`
}

// nowSlots is in-flight dispatches against concurrency.global_max as `orch
// run` resolves it (defaults included); Max is 0 only when config.yaml does
// not load.
type nowSlots struct {
	Used int `json:"used"`
	Max  int `json:"max"`
}

func (s *Server) handleNow(w http.ResponseWriter, r *http.Request) {
	if s.state == nil {
		s.failRead(w, "now", errNoBackend)
		return
	}
	ctx := r.Context()
	view, err := s.loadView(ctx)
	if err != nil {
		s.failRead(w, "now", err)
		return
	}

	out := nowPayload{
		Project:          view.ProjectName,
		Working:          []nowDispatch{},
		Summary:          toSummaryPayload(view.Summary),
		WaitingForBudget: []budget.Waiting{},
	}

	run, err := s.state.LatestRun(ctx)
	switch {
	case errors.Is(err, state.ErrNoRuns):
	case err != nil:
		s.failRead(w, "now", err)
		return
	default:
		out.Run = &nowRun{RunID: run.RunID, StartedAt: run.StartedAt, UpdatedAt: run.UpdatedAt, Mode: run.Mode, Status: run.Status}
	}

	dispatches, err := s.state.InFlightDispatches(ctx)
	if err != nil {
		s.failRead(w, "now", err)
		return
	}
	// A config.yaml that does not load leaves the caps at 0 ("not set"): they
	// are denominators on a status page, and orch run names the broken file.
	var limits config.Config
	if loaded, lErr := config.Load(s.paths.ConfigYAML, s.paths.Root); lErr == nil {
		limits = loaded.Config
	}
	byID := make(map[string]model.Task, len(view.Tasks))
	for _, t := range view.Tasks {
		byID[t.ID] = t
	}
	for _, d := range dispatches {
		t, ok := byID[d.TaskID]
		if !ok {
			t = model.Task{ID: d.TaskID}
		}
		out.Working = append(out.Working, nowDispatch{
			TaskID: d.TaskID, Title: titleOr(t), Phase: t.Phase,
			Provider: d.Backend, Model: t.Model,
			Attempt: d.Attempt, MaxAttempts: limits.Retry.MaxAttempts,
			StartedAt: d.StartedAt,
		})
	}
	out.Slots = nowSlots{Used: len(dispatches), Max: limits.Concurrency.GlobalMax}

	blockedIDs := make([]string, 0)
	for _, t := range view.Tasks {
		if t.Status == model.StatusBlocked {
			blockedIDs = append(blockedIDs, t.ID)
		}
	}
	done7d, err := s.state.CountDoneLastNDays(ctx, velocityWindowDays)
	if err != nil {
		s.failRead(w, "now", err)
		return
	}
	lastEvents, err := s.state.LastEventByTask(ctx, blockedIDs)
	if err != nil {
		s.failRead(w, "now", err)
		return
	}
	out.Pace = sprintHealth(view.Tasks, done7d, lastEvents, time.Now())
	out.Attention = out.Pace.Blockers

	if out.WaitingForBudget, err = s.waitingForBudget(r); err != nil {
		s.failRead(w, "now", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
