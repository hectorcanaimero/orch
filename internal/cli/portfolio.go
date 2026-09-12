package cli

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/hectorcanaimero/orch/internal/config"
	"github.com/hectorcanaimero/orch/internal/dashboard"
)

// openPortfolio resolves a glob into opened projects, plus the directories it
// could not open.
//
// Never fails on a single project: a glob that matches a scratch directory, a
// project mid-scaffold, or one whose database is locked must not stop the
// other nine from being served. Each failure becomes an `unavailable` row with
// its reason, which is what the operator needs in order to fix it — a
// portfolio that silently listed nine of ten would hide exactly the project
// that needs attention.
//
// Returns a close func for the backends that did open, so a caller that fails
// afterwards does not leak file handles.
func openPortfolio(ctx context.Context, pattern string, static http.Handler) (
	[]dashboard.PortfolioProject, []dashboard.UnavailableProject, func(), error,
) {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		// The only error Glob returns is a malformed pattern, which is a typo
		// in the operator's command rather than anything about their projects
		// — so this one does stop the process.
		return nil, nil, nil, fmt.Errorf("--portfolio %q: %w", pattern, err)
	}
	if len(matches) == 0 {
		return nil, nil, nil, fmt.Errorf(
			"--portfolio %q matched no directories (the shell expands globs too — "+
				"quote it if you meant orch to)", pattern)
	}
	// Sorted so the page's order is stable across restarts. Glob already
	// sorts, but that is documented of its result, not of what survives the
	// filtering below.
	sort.Strings(matches)

	var (
		projects    []dashboard.PortfolioProject
		unavailable []dashboard.UnavailableProject
		closers     []func() error
	)
	closeAll := func() {
		for _, c := range closers {
			_ = c()
		}
	}

	seen := map[string]string{}
	for _, root := range matches {
		info, statErr := os.Stat(root)
		if statErr != nil || !info.IsDir() {
			// A glob matching files is ordinary — `projects/*` catches a
			// README — and not worth reporting as a broken project.
			continue
		}

		proj, closer, reason := openPortfolioProject(ctx, root, static)
		if reason != "" {
			unavailable = append(unavailable, dashboard.UnavailableProject{Root: root, Reason: reason})
			continue
		}
		// Two directories resolving to one project id — `a/billing` and
		// `b/billing` — would collide under /p/, which NewPortfolio refuses.
		// Caught here instead so the message names both paths, which is what
		// the operator has to change.
		if first, dup := seen[proj.ID]; dup {
			unavailable = append(unavailable, dashboard.UnavailableProject{
				Root: root,
				Reason: fmt.Sprintf("project id %q is already taken by %s — rename one "+
					"directory, or set a different id in that project", proj.ID, first),
			})
			_ = closer()
			continue
		}
		seen[proj.ID] = root
		projects = append(projects, proj)
		closers = append(closers, closer)
	}

	if len(projects) == 0 && len(unavailable) == 0 {
		return nil, nil, nil, fmt.Errorf(
			"--portfolio %q matched only files, no project directories", pattern)
	}
	return projects, unavailable, closeAll, nil
}

// openPortfolioProject opens one project, or returns the reason it could not.
//
// The reason is operator-facing — it ends up in the payload and on the page —
// so it names what to fix rather than what failed internally.
//
// Each project gets its OWN dashboard.Server, with its own config, its own
// profile and its own token. That is what makes `/p/<id>/…` apply that
// project's access model rather than the portfolio's: a project configured as
// `stakeholder` still demands its own token, resolved against its own row.
func openPortfolioProject(ctx context.Context, root string, static http.Handler) (
	dashboard.PortfolioProject, func() error, string,
) {
	// An explicit root, always: ResolvePaths selects the state layout from
	// whether the root was given, and every project here is being pointed at
	// from elsewhere. Passing "" would resolve to the portfolio process's own
	// working directory for all of them.
	paths, err := config.ResolvePaths(root, "", "")
	if err != nil {
		return dashboard.PortfolioProject{}, nil, err.Error()
	}
	if _, statErr := os.Stat(paths.TasksJSON()); statErr != nil {
		return dashboard.PortfolioProject{}, nil, "no tasks.json — not an orch project"
	}

	loaded, err := config.Load(paths.ConfigYAML, paths.Root)
	if err != nil {
		return dashboard.PortfolioProject{}, nil, fmt.Sprintf("config load failed: %v", err)
	}
	cfg := loaded.Config

	dashCfg, err := dashboard.FromConfig(cfg)
	if err != nil {
		return dashboard.PortfolioProject{}, nil, err.Error()
	}

	backend, closeDB, err := openBackend(ctx, paths, cfg)
	if err != nil {
		return dashboard.PortfolioProject{}, nil, fmt.Sprintf("state: %v", err)
	}

	// The same resolution `orch dashboard` does, per project: the database
	// wins whenever this project has rotated a token (G8.2/F3.3).
	if err := resolveTokenHash(ctx, backend, &dashCfg); err != nil {
		_ = closeDB()
		return dashboard.PortfolioProject{}, nil, fmt.Sprintf("stakeholder token: %v", err)
	}
	// A stakeholder project with no token is refused by Validate, and that
	// refusal has to reach the operator as a row rather than as a dead
	// process: one misconfigured project in a glob of ten is exactly the case
	// the unavailable list is for.
	if err := dashCfg.Validate(); err != nil {
		_ = closeDB()
		return dashboard.PortfolioProject{}, nil, err.Error()
	}

	server, err := dashboard.New(dashCfg, dashboard.Options{
		Paths:  paths,
		Static: static,
		State:  backend,
	})
	if err != nil {
		_ = closeDB()
		return dashboard.PortfolioProject{}, nil, err.Error()
	}
	return dashboard.PortfolioProject{ID: paths.ID, Server: server}, closeDB, ""
}

// portfolioFlags are the dashboard flags the portfolio form honours.
//
// Not `--project-root`/`--project-id`: the glob names the projects, and a
// single root would contradict it. Not `--tunnel` either — see runPortfolio.
type portfolioFlags struct {
	host       string
	port       int
	profile    string
	token      string
	withTunnel bool
}

// runPortfolio serves N projects from one process (G8.5 / F3.6).
func runPortfolio(cmd *cobra.Command, pattern string, flags portfolioFlags) error {
	ctx := cmd.Context()

	if flags.profile != "" && flags.profile != string(dashboard.ProfileOperator) {
		return fmt.Errorf("%w: --profile %s", dashboard.ErrPortfolioProfile, flags.profile)
	}
	// A shared token across projects is the one thing the portfolio must not
	// invent: each project's token is its own, resolved against its own row,
	// and accepting a flag here would suggest otherwise.
	if flags.token != "" {
		return fmt.Errorf("--token has no meaning with --portfolio: each project " +
			"keeps its own token, and /p/<id>/ validates against that project's row")
	}
	// The tunnel is per project — its config, its state file, its URL — and
	// there is no project here to take it from. Refused rather than ignored.
	if flags.withTunnel {
		return fmt.Errorf("--tunnel has no meaning with --portfolio: a tunnel is " +
			"configured per project, and this process serves several")
	}

	spa, err := dashboard.SPA()
	if err != nil {
		return fmt.Errorf("reading the embedded SPA: %w", err)
	}
	static := dashboard.SPAHandler(spa)

	projects, unavailable, closeAll, err := openPortfolio(ctx, pattern, static)
	if err != nil {
		return err
	}
	defer closeAll()

	cfg := dashboard.Config{
		Profile: dashboard.ProfileOperator,
		Host:    dashboard.DefaultHost,
		Port:    dashboard.DefaultPort,
	}
	if cmd.Flags().Changed("host") {
		cfg.Host = flags.host
	}
	if cmd.Flags().Changed("port") {
		cfg.Port = flags.port
	}

	p, err := dashboard.NewPortfolio(dashboard.PortfolioOptions{
		Config:      cfg,
		Projects:    projects,
		Unavailable: unavailable,
		Static:      static,
	})
	if err != nil {
		return err
	}

	// Printed before the listener opens only for the problems: an operator
	// whose glob quietly dropped three projects needs to know at startup, not
	// by counting cards.
	for _, u := range unavailable {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "[warn] %s: %s\n", u.Root, u.Reason)
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-p.Ready()
		out := cmd.OutOrStdout()
		_, _ = fmt.Fprintf(out, "Orch portfolio dashboard running on http://%s\n", p.BoundAddr())
		_, _ = fmt.Fprintf(out, "  %d project(s): %s\n",
			len(projects), strings.Join(p.Projects(), ", "))
		if len(unavailable) > 0 {
			_, _ = fmt.Fprintf(out, "  %d unavailable (listed above, and on the page)\n",
				len(unavailable))
		}
		_, _ = fmt.Fprintln(out, "  Operator profile: nothing on /api/portfolio is "+
			"token-gated. Each project's own routes under /p/<id>/ keep theirs.")
	}()
	return p.Serve(ctx)
}
