package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/hectorcanaimero/orch/internal/config"
	"github.com/hectorcanaimero/orch/internal/state"
)

// Server is the dashboard's HTTP surface.
//
// Ported from `create_app` in `orchestrator/dashboard/server.py`, minus
// FastAPI: the routes are plain handlers on a `http.ServeMux` and the access
// model is a decorator rather than middleware (see access.go for why).
//
// What this file owns is the plumbing — listening, routing, shutting down.
// Which routes exist is routes.go; who may reach them is access.go; serving
// the SPA is spa.go, which this never wraps.
type Server struct {
	cfg   Config
	paths config.Paths
	mux   *http.ServeMux
	http  *http.Server
	log   *slog.Logger
	state StateReader
	// ready is closed once Serve has a listener (or has failed to get one),
	// and boundAddr is what it bound. Both exist because `port: 0` is legal
	// and means "any free one": without them a caller that asked for an
	// ephemeral port has no way to learn which one it got, so the one config
	// a test or a tunnel wants most is the one it cannot use.
	ready     chan struct{}
	mu        sync.Mutex
	boundAddr string
	// static is the SPA handler, mounted at "/" and deliberately NOT gated.
	static http.Handler
}

// StateReader is the slice of the state backend the dashboard reads.
//
// Narrow and read-only by construction: a dashboard that could write would be
// a second writer to a database with one writer, and "read-only by design" is
// the property the profile guard rests on. CHECKLIST rule 13 says the same
// thing about the package; this says it in the type.
type StateReader interface {
	// Tasks is the live status of every task, which every task-shaped view
	// overlays on tasks.json. Filtered queries exist on the backend; the
	// dashboard filters in memory because it needs the unfiltered totals in
	// the same response.
	Tasks(ctx context.Context, filter state.TaskFilter) ([]state.TaskRuntime, error)
	// AllEvents is the event log across every task, oldest first. n <= 0
	// means all of it. The log view reads the tail; the per-task figures
	// (hours spent, last updated) are derived from the whole thing.
	AllEvents(ctx context.Context, n int) ([]state.Event, error)
	// AllSpend is every spend row newer than `since`, across every backend.
	// The metrics page asks for all of history; the budget summary asks for
	// today.
	AllSpend(ctx context.Context, since time.Time) ([]state.Spend, error)
	// SpendSince is one provider's rolling window, and it is here because
	// `budget.Gate` takes exactly this method: the dashboard reports the same
	// numbers the dispatch guardrail enforces by asking the same gate, rather
	// than by recomputing a window that could drift from it.
	SpendSince(ctx context.Context, backend string, since time.Time) ([]state.Spend, error)

	// Milestones is every milestone with its progress counts.
	Milestones(ctx context.Context) ([]state.Milestone, error)
	// CountDoneLastNDays is the numerator of velocity: tasks that finished
	// inside the window.
	CountDoneLastNDays(ctx context.Context, days int) (int, error)
	// LastEventByTask is the newest event per task id, which is how a blocked
	// task explains itself. The sprint panel asks only about the blocked ones.
	LastEventByTask(ctx context.Context, taskIDs []string) (map[string]state.Event, error)
}

// Options are what New needs beyond the config.
type Options struct {
	// Paths locates the project this dashboard reports on.
	Paths config.Paths
	// Static serves the built SPA. Required: a dashboard with no UI is a
	// JSON API nobody asked for, and a nil handler here would 404 the shell
	// while every API route worked — a failure that looks like a broken
	// build rather than a missing argument.
	Static http.Handler
	// State is the backend the read endpoints query. May be nil until (b).
	State StateReader
	// Logger defaults to slog.Default().
	Logger *slog.Logger
}

// New builds a server. It does not listen; Serve does.
func New(cfg Config, opts Options) (*Server, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if opts.Static == nil {
		return nil, errors.New("dashboard: no SPA handler — see internal/dashboard/spa.go")
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}

	s := &Server{
		cfg:    cfg,
		paths:  opts.Paths,
		mux:    http.NewServeMux(),
		log:    log,
		state:  opts.State,
		static: opts.Static,
		ready:  make(chan struct{}),
	}
	s.registerRoutes()
	s.http = &http.Server{
		Addr:    cfg.Addr(),
		Handler: s.mux,
		// A dashboard reads a SQLite file and answers; nothing here should
		// take ten seconds. The write timeout is the one that matters when
		// the read endpoints land: a slow query holding a connection is the
		// shape that starves the single writer.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	return s, nil
}

// Ready is closed once Serve has finished with the listener — bound, or failed
// to bind. A waiter is released either way: a channel that only closes on
// success is a deadlock the first time a port is taken.
//
// After it closes, BoundAddr is the address, or "" if the listen failed.
func (s *Server) Ready() <-chan struct{} { return s.ready }

// BoundAddr is the address the listener actually got, which is not
// cfg.Addr() when the configured port is 0.
func (s *Server) BoundAddr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.boundAddr
}

// Handler exposes the mux, for a test that wants to drive the routes without
// a listener.
func (s *Server) Handler() http.Handler { return s.mux }

// Serve listens and blocks until the context is cancelled, then shuts down
// gracefully.
//
// The listener is opened before this returns control, so a caller that prints
// the URL prints one that already answers. Python's own runner prints it
// before uvicorn binds, which produces a URL that 404s for the first moment
// somebody is fast enough to click it.
func (s *Server) Serve(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Addr())
	if err != nil {
		close(s.ready)
		return fmt.Errorf("listen on %s: %w", s.cfg.Addr(), err)
	}
	s.mu.Lock()
	s.boundAddr = ln.Addr().String()
	s.mu.Unlock()
	close(s.ready)
	s.log.Info("dashboard listening",
		"addr", ln.Addr().String(), "profile", string(s.cfg.Profile))

	errCh := make(chan error, 1)
	go func() {
		if err := s.http.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	// A fixed grace period rather than the caller's context: the context is
	// already cancelled, so passing it would make Shutdown return instantly
	// and drop every request in flight — which is the bug this pattern is
	// usually written to avoid and usually still contains.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.http.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("dashboard shutdown: %w", err)
	}
	return <-errCh
}

// ---- route registration ----------------------------------------------------

// route is one registered endpoint.
//
// `name` is Python's route name and is what the stakeholder allow-list matches
// on, so it is not decoration: changing one silently changes who can reach the
// route. They are the same strings `DEFAULT_STAKEHOLDER_ROUTES` uses.
type route struct {
	pattern string
	name    string
	handler http.HandlerFunc
}

// gated wraps a handler with the access model.
//
// Every data route goes through this and the SPA handler does not. That is the
// whole design: public is the default for what the static handler serves, so
// there is no path classification to get wrong, and a route registered without
// `gated` is a visible omission here rather than an invisible hole in a prefix
// list in another file.
func (s *Server) gated(r route) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch s.cfg.decide(req.URL.Path, r.name, tokenFrom(req)) {
		case Unauthorized:
			writePlain(w, http.StatusUnauthorized, "unauthorized")
		case Forbidden:
			writePlain(w, http.StatusForbidden, "forbidden")
		default:
			r.handler(w, req)
		}
	})
}

func (s *Server) registerRoutes() {
	for _, r := range s.routes() {
		s.mux.Handle(r.pattern, s.gated(r))
	}
	// Last, and ungated. `http.ServeMux` resolves by pattern specificity, not
	// registration order, so "/api/whoami" wins over "/" wherever this line
	// sits — but keeping it last is how a reader sees that everything above
	// is gated and this one thing is not.
	s.mux.Handle("/", s.static)
}

// writePlain sends a bare status line.
//
// No path, no route name, no detail. A 401 that echoed what was asked for
// would confirm to an unauthenticated caller which routes exist, and
// `no-store` keeps a proxy from serving somebody else the rejection.
func writePlain(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = fmt.Fprintln(w, msg)
}

// writeJSON sends one object.
//
// Compact, like Python's `JSONResponse`, and with HTML escaping off — the SPA
// parses this, nothing interpolates it into a page, and the escaping turns
// `<` in a task title into `<` for no reader's benefit.
func writeJSON(w http.ResponseWriter, status int, payload any) {
	// Encoded into a buffer first, not straight onto the ResponseWriter. The
	// streaming form writes a 200 and part of a body before it can discover
	// the value does not encode, and there is no taking a status code back.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(payload); err != nil {
		writePlain(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}
