package dashboard

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Portfolio serves N projects from one process (G8.5 / F3.6).
//
// # What it is, and what it is not
//
// It is a front door, not a second dashboard. Each project keeps its own
// *Server — its own Paths, its own state backend, its own profile and its own
// rotated token — and `/p/<project_id>/…` hands the request to that Server's
// handler with the prefix stripped. So a project configured as `stakeholder`
// still demands its own token on its own routes, resolved against its own row
// (G8.2/F3.3), and this type contains no second copy of the access model to
// drift from the first.
//
// The only route it serves itself is `/api/portfolio`: the one question that
// cannot be answered by any single project.
//
// # Operator only, and what that does and does not mean
//
// NewPortfolio refuses a non-operator profile. The portfolio row for a project
// is NOT gated by that project's token — it is the operator's own view of
// their own machine, and the boundary is the same one a single-project
// operator dashboard has today: the listener, bound to 127.0.0.1 by default.
// "Operator only" here means "there is no stakeholder portfolio", not
// "authenticated". Worth stating because the asymmetry is real: a project
// whose per-project routes require a token still contributes counters to a row
// that does not.
type Portfolio struct {
	cfg      Config
	projects []PortfolioProject
	// unavailable are directories that could not be opened at startup.
	unavailable []unavailableRow

	mux  *http.ServeMux
	http *http.Server
	log  *slog.Logger

	ready     chan struct{}
	mu        sync.Mutex
	boundAddr string

	// now is the clock sprintHealth projects from, injectable so a test does
	// not have to guess today's date.
	now func() time.Time
}

// PortfolioProject is one opened project: its identity and the server that
// owns its routes.
type PortfolioProject struct {
	// ID is the project id, and the path segment under /p/. Must be unique
	// across the set — NewPortfolio refuses a duplicate rather than letting
	// the second registration silently shadow the first.
	ID string
	// Server serves this project's routes. Never Serve()d: only its
	// Handler() is used, so it is a bundle of routes plus a gate rather than
	// a listener.
	Server *Server
}

// UnavailableProject is a directory the caller could not open.
//
// Reported rather than dropped: an operator whose glob matched a directory
// that is not an orch project needs to see which one and why, not a portfolio
// that is quietly one project short.
type UnavailableProject struct {
	Root   string
	Reason string
}

// PortfolioOptions is everything NewPortfolio needs.
//
// The caller opens the projects. This package does not glob directories or
// open databases for the same reason `New` takes a StateReader instead of a
// path: the filesystem and the driver belong to internal/cli, and a test here
// should not need either.
type PortfolioOptions struct {
	Config      Config
	Projects    []PortfolioProject
	Unavailable []UnavailableProject
	// Static is the SPA, mounted at "/" and at every /p/<id>/ prefix's root.
	Static http.Handler
	Logger *slog.Logger
	// Now defaults to time.Now.
	Now func() time.Time
}

// ErrPortfolioProfile is what a non-operator profile gets.
var ErrPortfolioProfile = errors.New(
	"dashboard: --portfolio is operator-only; a stakeholder profile has no " +
		"portfolio view, and the per-project routes keep their own tokens")

// NewPortfolio wires the front door over already-opened projects.
func NewPortfolio(opts PortfolioOptions) (*Portfolio, error) {
	if opts.Config.Profile != ProfileOperator {
		return nil, fmt.Errorf("%w (profile is %q)", ErrPortfolioProfile, opts.Config.Profile)
	}
	if len(opts.Projects) == 0 && len(opts.Unavailable) == 0 {
		return nil, errors.New("dashboard: portfolio has no projects")
	}
	if opts.Static == nil {
		return nil, errors.New("dashboard: no SPA handler — see internal/dashboard/spa.go")
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}

	p := &Portfolio{
		cfg:   opts.Config,
		mux:   http.NewServeMux(),
		log:   log,
		ready: make(chan struct{}),
		now:   now,
	}
	for _, u := range opts.Unavailable {
		// A conversion rather than a field-by-field literal, and on purpose:
		// the two types are identical today, and the day the wire row grows
		// a field this stops compiling until somebody decides what the
		// caller's type should say about it. A literal would leave the new
		// field zero for every unavailable project, quietly.
		p.unavailable = append(p.unavailable, unavailableRow(u))
	}

	seen := map[string]bool{}
	for _, proj := range opts.Projects {
		if proj.ID == "" {
			return nil, errors.New("dashboard: a portfolio project has no id")
		}
		if proj.Server == nil {
			return nil, fmt.Errorf("dashboard: project %q has no server", proj.ID)
		}
		// A duplicate id would register the same pattern twice, which
		// http.ServeMux panics on — and even if it did not, the second
		// project would be unreachable while still appearing in the list.
		// Refusing at startup names both, which a panic at request time does
		// not.
		if seen[proj.ID] {
			return nil, fmt.Errorf("dashboard: two portfolio projects share the id %q", proj.ID)
		}
		seen[proj.ID] = true
		p.projects = append(p.projects, proj)

		prefix := "/p/" + proj.ID
		p.mux.Handle(prefix+"/", http.StripPrefix(prefix, proj.Server.Handler()))
		// Without this, /p/<id> (no trailing slash) falls through to the SPA
		// at "/" and renders the portfolio page under a project URL.
		p.mux.Handle(prefix, http.RedirectHandler(prefix+"/", http.StatusMovedPermanently))
	}

	p.mux.HandleFunc("GET /api/portfolio", p.handlePortfolio)
	// An id nobody opened is a 404, not the SPA. Registered after the exact
	// prefixes above, which win on specificity regardless of order.
	p.mux.HandleFunc("/p/{project_id}/", p.handleUnknownProject)
	p.mux.Handle("/", opts.Static)

	// The same four timeouts Server sets, and they have to be the same four:
	// a project's routes run under THIS http.Server once they are delegated,
	// so a portfolio with different limits would give the same endpoint
	// different behaviour depending on which door it came through. The 30s
	// write timeout included — `/p/<id>/api/events/stream` clears its own
	// deadline with an http.ResponseController, which reaches the underlying
	// connection regardless of which server owns it and of the StripPrefix
	// in between (StripPrefix wraps the handler, not the ResponseWriter).
	p.http = &http.Server{
		Addr:              opts.Config.Addr(),
		Handler:           p.mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	return p, nil
}

// Handler exposes the mux, for a test that wants the routes without a
// listener.
func (p *Portfolio) Handler() http.Handler { return p.mux }

// Ready closes once the listener is open (or has failed to open).
func (p *Portfolio) Ready() <-chan struct{} { return p.ready }

// BoundAddr is the address the listener actually got.
func (p *Portfolio) BoundAddr() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.boundAddr
}

// Projects is the ids being served, in the order they were given.
func (p *Portfolio) Projects() []string {
	out := make([]string, 0, len(p.projects))
	for _, proj := range p.projects {
		out = append(out, proj.ID)
	}
	return out
}

func (p *Portfolio) handleUnknownProject(w http.ResponseWriter, r *http.Request) {
	// The id is echoed because this is the operator's own surface and the
	// likely cause is a typo or a glob that did not match — the reticence
	// writePlain shows on the gated routes is about not confirming which
	// routes exist to an unauthenticated caller, which does not apply here.
	writePlain(w, http.StatusNotFound,
		fmt.Sprintf("no project %q in this portfolio", r.PathValue("project_id")))
}

func (p *Portfolio) handlePortfolio(w http.ResponseWriter, r *http.Request) {
	now := p.now()
	payload := portfolioPayload{
		GeneratedAt: now.UTC().Format(time.RFC3339),
		Projects:    make([]portfolioProject, 0, len(p.projects)),
		Unavailable: p.unavailable,
	}
	if payload.Unavailable == nil {
		payload.Unavailable = []unavailableRow{}
	}
	// Sequentially, not in parallel: the expensive part is SQLite reads
	// against N local files, the page is polled rather than hot, and N
	// goroutines racing N different databases is a concurrency surface added
	// for a saving nobody has measured.
	for _, proj := range p.projects {
		payload.Projects = append(payload.Projects, proj.Server.portfolioRow(r.Context(), now))
	}
	writeJSON(w, http.StatusOK, payload)
}

// Serve listens and blocks until the context is cancelled, then shuts down
// gracefully. Same shape as Server.Serve, including the fixed grace period —
// see that method for why it is not the caller's context.
func (p *Portfolio) Serve(ctx context.Context) error {
	ln, err := net.Listen("tcp", p.cfg.Addr())
	if err != nil {
		close(p.ready)
		return fmt.Errorf("listen on %s: %w", p.cfg.Addr(), err)
	}
	p.mu.Lock()
	p.boundAddr = ln.Addr().String()
	p.mu.Unlock()
	close(p.ready)
	p.log.Info("portfolio dashboard listening",
		"addr", ln.Addr().String(), "projects", strings.Join(p.Projects(), ","))

	errCh := make(chan error, 1)
	go func() {
		if err := p.http.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
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

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.http.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("portfolio shutdown: %w", err)
	}
	return <-errCh
}
