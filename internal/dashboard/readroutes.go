package dashboard

import (
	"net/http"
	"strconv"
	"strings"
)

// The read endpoints of G5.2(b): everything the trimmed operator SPA fetches
// that is not the tunnel and not the SSE stream.
//
// All four start from one `projectView`, which is two queries. A handler that
// wanted a third would be a handler that has drifted from the others about
// what "the project" currently is.

// readRoutes are appended to the (a) routes in routes().
func (s *Server) readRoutes() []route {
	return []route{
		{pattern: "GET /api/tasks", name: "api_tasks", handler: s.handleTasks},
		{pattern: "GET /api/task/{task_id}", name: "api_task_detail", handler: s.handleTaskDetail},
		{pattern: "GET /api/graph", name: "api_graph", handler: s.handleGraph},
		{pattern: "GET /api/events", name: "api_events", handler: s.handleEvents},
	}
}

// tasksPayload is `/api/tasks`'s body.
//
// `count` is what the filters left; `total` is the project. Both, because a
// page that shows "12 tasks" after a filter and has no way to say "of 143"
// makes an empty filter look like an empty project.
type tasksPayload struct {
	ProjectID   string         `json:"project_id"`
	ProjectRoot string         `json:"project_root"`
	Summary     summaryPayload `json:"summary"`
	Tasks       []taskPayload  `json:"tasks"`
	Count       int            `json:"count"`
	Total       int            `json:"total"`
}

func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	view, err := s.loadView(r.Context())
	if err != nil {
		s.failRead(w, "tasks", err)
		return
	}

	q := r.URL.Query()
	filters := taskFilters{
		Status:         q.Get("status"),
		Model:          q.Get("model"),
		Q:              q.Get("q"),
		Parallelizable: q.Get("parallelizable") == "1",
	}
	// An unparseable `phase` is no filter rather than a 400, matching
	// FastAPI only in effect: there a bad int is a 422 the SPA never sends.
	// Treating it as absent keeps a hand-typed URL useful instead of terse.
	if raw := q.Get("phase"); raw != "" {
		if n, convErr := strconv.Atoi(raw); convErr == nil {
			filters.Phase = &n
		}
	}

	filtered := view.filter(filters)
	rows := make([]taskPayload, 0, len(filtered))
	for _, t := range filtered {
		rows = append(rows, view.task(t))
	}

	writeJSON(w, http.StatusOK, tasksPayload{
		ProjectID:   s.paths.ID,
		ProjectRoot: s.paths.Root,
		Summary:     toSummaryPayload(view.Summary),
		Tasks:       rows,
		Count:       len(rows),
		Total:       len(view.Tasks),
	})
}

func (s *Server) handleTaskDetail(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("task_id")
	view, err := s.loadView(r.Context())
	if err != nil {
		s.failRead(w, "task", err)
		return
	}
	for _, t := range view.Tasks {
		if t.ID != taskID {
			continue
		}
		payload := view.task(t)
		// Only this endpoint carries it, because only this one is answering
		// about a single task. On a list it would be a column repeating what
		// the `parallelizable=1` filter already says.
		para := view.Parallelizable[t.ID]
		payload.Parallelizable = &para
		writeJSON(w, http.StatusOK, payload)
		return
	}
	writeJSON(w, http.StatusNotFound, errorPayload{Detail: "task not found: " + taskID})
}

func (s *Server) handleGraph(w http.ResponseWriter, r *http.Request) {
	view, err := s.loadView(r.Context())
	if err != nil {
		s.failRead(w, "graph", err)
		return
	}
	writeJSON(w, http.StatusOK, view.graph())
}

// eventsPayload is `/api/events`'s body.
type eventsPayload struct {
	Events []eventPayload `json:"events"`
	Count  int            `json:"count"`
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if s.state == nil {
		s.failRead(w, "events", errNoBackend)
		return
	}
	limit := clampLimit(r.URL.Query().Get("limit"))

	// The tail is taken BEFORE the task filter, like Python. It means
	// `?task_id=X&limit=200` can return fewer than 200 rows for X — the
	// window is "the last 200 events", not "the last 200 events of X". Worth
	// knowing, and worth not silently changing: the log view's scrollback and
	// the SSE stream that appends to it have to agree on which window they
	// are in.
	events, err := s.state.AllEvents(r.Context(), limit)
	if err != nil {
		s.failRead(w, "events", err)
		return
	}

	taskID := r.URL.Query().Get("task_id")
	rows := make([]eventPayload, 0, len(events))
	for _, e := range events {
		if taskID != "" && e.TaskID != taskID {
			continue
		}
		rows = append(rows, formatEvent(e))
	}
	writeJSON(w, http.StatusOK, eventsPayload{Events: rows, Count: len(rows)})
}

// clampLimit reproduces FastAPI's `Query(200, ge=1, le=1000)` without its 422:
// out of range clamps rather than refuses. A hand-typed `?limit=0` asking for
// nothing is a mistake worth correcting; `?limit=99999` is a browser about to
// be handed a hundred thousand rows.
func clampLimit(raw string) int {
	const (
		def = 200
		lo  = 1
		hi  = 1000
	)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return def
	}
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}

// errorPayload is FastAPI's `HTTPException` body, which the SPA reads as
// `detail`.
type errorPayload struct {
	Detail string `json:"detail"`
}

// failRead answers a read that could not be served.
//
// 500 with the reason, and the reason is logged in full. Python returns the
// exception text to the client; here the client gets a sentence naming what
// failed and the operator gets the wrapped error in the log, because the
// wrapped error carries absolute paths.
func (s *Server) failRead(w http.ResponseWriter, what string, err error) {
	s.log.Error("dashboard read failed", "endpoint", what, "err", err)
	writeJSON(w, http.StatusInternalServerError,
		errorPayload{Detail: "could not read the project's " + what})
}
