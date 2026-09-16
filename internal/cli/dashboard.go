package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/hectorcanaimero/orch/internal/config"
	"github.com/hectorcanaimero/orch/internal/dashboard"
	"github.com/hectorcanaimero/orch/internal/publish"
	"github.com/hectorcanaimero/orch/internal/publish/snapshot"
	"github.com/hectorcanaimero/orch/internal/state"
	"github.com/hectorcanaimero/orch/internal/tunnel"
)

// newDashboardCmd ports `orch dashboard` (the `run` function in
// orchestrator/dashboard/server.py).
//
// The server itself is internal/dashboard; this is the wiring — flags, the
// banner, and a signal handler so Ctrl+C shuts the listener down instead of
// killing the process mid-response.
func newDashboardCmd(flags *projectFlags) *cobra.Command {
	var (
		host        string
		port        int
		profile     string
		token       string
		withTunnel  bool
		portfolio   string
		allowRemote bool
		withDemo    bool
	)

	cmd := &cobra.Command{
		Use:   "dashboard",
		Short: "Serve the operator dashboard",
		Long: "Serve the operator dashboard on a local HTTP port.\n\n" +
			"Reads the project's state and shows it; it never writes. The\n" +
			"stakeholder profile gates every data route behind a token and an\n" +
			"allow-list — see `profile` and `token` under `dashboard:` in\n" +
			"config.yaml, which the flags below override.\n\n" +
			"The stakeholder token is the exception: a token stored by\n" +
			"`orch dashboard token rotate` wins over --token, which wins over\n" +
			"dashboard.token. A token given while the database has one is\n" +
			"ignored, with a warning at startup.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()

			if withDemo {
				cleanup, err := prepareDemo(cmd, flags, withTunnel, portfolio)
				if err != nil {
					return err
				}
				defer cleanup()
			}

			// The portfolio is a different process shape — N projects, no
			// single `paths` — so it forks before any of the single-project
			// resolution below. It is not a mode of this command's wiring
			// with an `if` through every step; it is its own function.
			if portfolio != "" {
				return runPortfolio(cmd, portfolio, portfolioFlags{
					host: host, port: port, profile: profile,
					withTunnel: withTunnel, token: token,
					allowRemote: allowRemote,
				})
			}
			// Registered on this command but meaningful only with
			// --portfolio, so passing it alone is refused rather than
			// silently doing nothing. A single-project dashboard has its own
			// answer to remote binding — its profile and its token — and
			// pretending this flag participates in that would be a third
			// story about the same question.
			if allowRemote {
				return fmt.Errorf("--allow-remote only applies with --portfolio; " +
					"a single project's exposure is decided by its profile and token")
			}

			paths, cfg, err := loadProjectConfig(flags)
			if err != nil {
				return err
			}

			dashCfg, err := dashboard.FromConfig(cfg)
			if err != nil {
				return err
			}
			// Flags win over config.yaml, and only when passed — an unset
			// flag must not overwrite a configured value with its zero.
			if cmd.Flags().Changed("profile") {
				dashCfg.Profile = dashboard.Profile(profile)
			}
			// Where a token came from, for the warning below when the
			// database overrides it.
			tokenOrigin := "dashboard.token in config.yaml"
			if cmd.Flags().Changed("token") {
				dashCfg.Token = token
				tokenOrigin = "--token"
			}
			if cmd.Flags().Changed("host") {
				dashCfg.Host = host
			}
			if cmd.Flags().Changed("port") {
				dashCfg.Port = port
			}

			backend, closeDB, err := openBackend(ctx, paths, cfg)
			if err != nil {
				return err
			}
			// Discarded like every other command's: the process is exiting,
			// the database is read-only here, and a close error at that point
			// changes nothing a caller could act on.
			defer func() { _ = closeDB() }()

			// Resolved BEFORE Validate() so a project whose token lives only
			// in the database (never in config.yaml/--token) validates
			// correctly — the database wins whenever this project has a row
			// (G8.2/F3.3), regardless of --token; see resolveTokenHash's own
			// comment for why. This is a one-time snapshot for the startup
			// check and the banner below; the running server re-resolves it
			// live on every gated request instead (internal/dashboard's
			// expectedTokenHash), so `orch dashboard token rotate` against an
			// already-running dashboard needs no restart to take effect.
			if err := resolveTokenHash(ctx, backend, &dashCfg); err != nil {
				return fmt.Errorf("resolving the stakeholder token: %w", err)
			}
			// The database winning is deliberate, but silent it reads as a
			// broken flag (#275): the banner says `source: database` and the
			// given token 401s. Printed before Validate so a refused start
			// still says it. Never the token itself. Not under the operator
			// profile, which uses no token at all.
			if dashCfg.TokenSource == "database" && dashCfg.Token != "" &&
				dashCfg.Profile != dashboard.ProfileOperator {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
					"[warn] %s is ignored: this project has a stakeholder token "+
						"rotated into its database, which wins over --token and "+
						"dashboard.token. Use the token `orch dashboard token rotate` "+
						"printed (`orch dashboard token show` says when that was), or run "+
						"`orch dashboard token rotate` again for a new one.\n",
					tokenOrigin)
			}
			if err := dashCfg.Validate(); err != nil {
				return err
			}

			spa, err := dashboard.SPA()
			if err != nil {
				return fmt.Errorf("reading the embedded SPA: %w", err)
			}

			tunnelOpts, err := setUpTunnel(cfg, paths, dashCfg, withTunnel)
			if err != nil {
				return err
			}

			portal, err := publish.Bundle()
			if err != nil {
				return fmt.Errorf("reading the embedded client portal: %w", err)
			}

			server, err := dashboard.New(dashCfg, dashboard.Options{
				Paths:  paths,
				Static: dashboard.SPAHandler(spa),
				State:  backend,
				Tunnel: tunnelOpts,
				// The client portal at /stakeholder/, reading the snapshot
				// `orch publish` would write, built fresh per request.
				Portal: portal,
				Snapshot: func(ctx context.Context) (snapshot.Snapshot, error) {
					return buildPublishSnapshot(ctx, paths, cfg, backend, portalRefresh, time.Now())
				},
			})
			if err != nil {
				return err
			}
			// Whether --tunnel or the Tunnel page started it, a tunnel does not
			// outlive the dashboard it forwards to.
			if tunnelOpts.Manager != nil {
				defer func() {
					if _, err := server.StopTunnel(); err != nil && !errors.Is(err, tunnel.ErrNotRunning) {
						_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "[warn] stopping the tunnel: %v\n", err)
					}
				}()
			}

			// A project with no tasks.json still serves — the SPA's setup
			// wizard is the whole point of `/api/config/status` — but saying
			// so up front beats an empty board the operator has to interpret.
			if _, statErr := os.Stat(paths.TasksJSON()); statErr != nil {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
					"[warn] %s does not exist — the dashboard will show 0 tasks.\n",
					paths.TasksJSON())
			}

			// Ctrl+C cancels the context Serve is watching, which shuts the
			// listener down with its grace period rather than dropping
			// whatever was in flight.
			ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
			defer stop()

			// The tunnel starts once the listener has its port — `--port 0`
			// has none before — and a failure to start it ends the command,
			// which was asked for a tunnel.
			tunnelErr := make(chan error, 1)
			go func() {
				<-server.Ready()
				printDashboardBanner(cmd, server, dashCfg, paths)
				if withTunnel && server.BoundAddr() != "" {
					if err := startTunnelFromCLI(ctx, cmd, server, cfg.Tunnel.URLParseTimeoutS); err != nil {
						tunnelErr <- err
						stop()
					}
				}
			}()
			if err := server.Serve(ctx); err != nil {
				return err
			}
			select {
			case err := <-tunnelErr:
				return err
			default:
				return nil
			}
		},
	}

	cmd.Flags().StringVar(&host, "host", dashboard.DefaultHost,
		"Address to bind; 0.0.0.0 exposes it beyond localhost")
	cmd.Flags().IntVar(&port, "port", dashboard.DefaultPort,
		"Port to listen on; 0 picks any free one")
	cmd.Flags().StringVar(&profile, "profile", "",
		fmt.Sprintf("Access profile: %s, %s or %s (default: config.yaml)",
			dashboard.ProfileOperator, dashboard.ProfileStakeholder, dashboard.ProfileBoth))
	cmd.Flags().StringVar(&token, "token", "",
		"Shared token a stakeholder session must present (default: config.yaml; "+
			"ignored once orch dashboard token rotate has stored one)")
	cmd.Flags().BoolVar(&withTunnel, "tunnel", false,
		"Also start the Cloudflare quick tunnel (needs tunnel.enabled and cloudflared), and stop it on exit")
	cmd.Flags().StringVar(&portfolio, "portfolio", "",
		"Serve every orch project matching this glob from one process "+
			"(operator only). Quote it, or the shell expands it first.")
	cmd.Flags().BoolVar(&allowRemote, "allow-remote", false,
		"With --portfolio: allow a non-loopback --host, exposing every "+
			"project's counters on an unauthenticated /api/portfolio")
	cmd.Flags().BoolVar(&withDemo, "demo", false,
		"Serve a synthetic project with realistic history instead of this one; "+
			"nothing is dispatched")
	cmd.AddCommand(newDashboardTokenCmd(flags))
	return cmd
}

// resolveTokenHash sets dashCfg.TokenHash/TokenSource from whichever source
// currently has one for this project.
//
// The database wins unconditionally whenever a row exists — even over an
// explicit --token, which is the one place this port's usual "the flag wins"
// rule (see the flag-override block above) does not apply. `orch dashboard
// token rotate` exists so an operator can invalidate a token without editing
// YAML; a stale --token left in a shell alias or a saved command silently
// resurrecting the old one after a rotation would defeat that.
func resolveTokenHash(ctx context.Context, backend state.Backend, dashCfg *dashboard.Config) error {
	hash, _, ok, err := backend.StakeholderToken(ctx)
	if err != nil {
		return err
	}
	if ok {
		dashCfg.TokenHash = hash
		dashCfg.TokenSource = "database"
		return nil
	}
	if dashCfg.Token != "" {
		dashCfg.TokenHash = dashboard.HashToken(dashCfg.Token)
		dashCfg.TokenSource = "config.yaml"
	}
	return nil
}

// printDashboardBanner is what the operator reads before clicking.
//
// Printed AFTER the listener is open — `Ready()` is what makes that possible —
// so the URL in it already answers. Python prints before uvicorn binds, which
// produces a URL that 404s for the first moment somebody is fast enough.
func printDashboardBanner(cmd *cobra.Command, server *dashboard.Server, cfg dashboard.Config, paths config.Paths) {
	out := cmd.OutOrStdout()
	say := func(format string, args ...any) {
		_, _ = fmt.Fprintf(out, format+"\n", args...)
	}

	// Ready closes on a FAILED listen too — otherwise a waiter would block
	// forever on a server that is never coming up — so arriving here is not
	// the same as being up. The bound address is what tells the two apart:
	// it is set only after the listener exists. Without this the operator
	// gets "Orch dashboard running on http://" with nothing after the
	// slashes, and then the real error underneath it.
	//
	// Nothing is printed instead. Serve is already returning the reason to
	// the caller, which prints it and exits non-zero; a second line here
	// would be the same failure said twice.
	addr := server.BoundAddr()
	if addr == "" {
		return
	}
	say("Orch dashboard running on http://%s", addr)
	// Bound to every interface: the address above is not one anybody can
	// click, so spell out the two that are.
	if cfg.Host == "0.0.0.0" || cfg.Host == "::" {
		if _, port, err := net.SplitHostPort(addr); err == nil {
			say("  localhost: http://127.0.0.1:%s", port)
			if ip := firstNonLoopbackIP(); ip != "" {
				say("  LAN/VPN:   http://%s:%s", ip, port)
			}
		}
	}
	say("Project: %s (%s)", paths.ID, paths.Root)
	say("State dir: %s", paths.StateDir())
	say("Profile: %s", string(cfg.Profile))
	if cfg.Profile != dashboard.ProfileOperator {
		if cfg.TokenHash != "" {
			say("Token auth: ENABLED (source: %s)", cfg.TokenSource)
		} else {
			// Unreachable via this command — Validate refuses a stakeholder
			// profile with no token — but `both` reaches it, where the
			// stakeholder prefix is the part that 401s.
			say("Token auth: MISCONFIGURED (no token set — /stakeholder 401s)")
		}
	}
	say("Ctrl+C to stop")
}

// firstNonLoopbackIP is a best-effort address for the banner's LAN line.
//
// Best-effort is the whole contract: it reads the interfaces, takes the first
// IPv4 that is up and not loopback, and returns "" rather than guessing when
// there is nothing obvious. Python resolves the hostname instead, which on a
// machine whose hostname maps to 127.0.1.1 — every Debian — prints a loopback
// address labelled LAN.
func firstNonLoopbackIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		ipNet, ok := a.(*net.IPNet)
		if !ok || ipNet.IP.IsLoopback() || ipNet.IP.To4() == nil {
			continue
		}
		if ipNet.IP.IsLinkLocalUnicast() {
			continue
		}
		return ipNet.IP.String()
	}
	return ""
}

// setUpTunnel wires the tunnel half of the server. With --tunnel it also
// refuses, before anything listens, what would only fail once the dashboard
// is up: a profile that must not publish itself, a missing cloudflared (with
// the steps to install it), or a cloudflared config that blocks quick tunnels.
func setUpTunnel(cfg config.Config, paths config.Paths, dashCfg dashboard.Config, start bool) (dashboard.TunnelOptions, error) {
	if !cfg.Tunnel.Enabled {
		if start {
			return dashboard.TunnelOptions{}, errors.New(
				"--tunnel needs `tunnel.enabled: true` in config.yaml")
		}
		return dashboard.TunnelOptions{}, nil
	}

	mgr := tunnel.NewManager(paths.StateDir(), nil, tunnelLogLines)
	// A lock left by a process that died without releasing it would refuse
	// every start until somebody deleted a file by hand. Swept once at boot,
	// which is the only moment it is safe to assume nothing of ours holds it.
	mgr.SweepStaleLock(&tunnel.ManagerConfig{})
	opts := dashboard.TunnelOptions{
		Enabled: true, Manager: mgr, URLParseTimeoutS: cfg.Tunnel.URLParseTimeoutS,
	}
	if !start {
		return opts, nil
	}

	// The tunnel publishes this dashboard. Raising one from a stakeholder
	// dashboard would publish a surface whose own gate says the operator is
	// not present, so the flag refuses rather than asking anybody to notice.
	if dashCfg.Profile != dashboard.ProfileOperator {
		return dashboard.TunnelOptions{}, fmt.Errorf(
			"--tunnel needs the %s profile; this dashboard is running as %s",
			dashboard.ProfileOperator, dashCfg.Profile)
	}
	if !tunnel.LookupBinary("").Found {
		return dashboard.TunnelOptions{}, errors.New(strings.TrimRight(tunnel.MissingBinaryMessage(), "\n"))
	}
	if path := tunnel.ConfigBlocker(); path != "" {
		return dashboard.TunnelOptions{}, errors.New(tunnel.BlockerMessage(path))
	}
	return opts, nil
}

// startTunnelFromCLI starts the tunnel and prints its links once cloudflared
// reports the URL, which takes a few seconds.
func startTunnelFromCLI(ctx context.Context, cmd *cobra.Command, server *dashboard.Server, timeoutS int) error {
	if _, err := server.StartTunnel(); err != nil {
		return fmt.Errorf("starting the tunnel: %w", err)
	}
	if timeoutS <= 0 {
		timeoutS = defaultTunnelURLTimeoutS
	}
	out := cmd.OutOrStdout()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(time.Duration(timeoutS) * time.Second)
	for {
		if operator, portal := server.TunnelLinks(); operator != "" {
			_, _ = fmt.Fprintf(out, "Tunnel (Cloudflare quick tunnel) is up. The token in these links is "+
				"required through the tunnel and changes on every start:\n"+
				"  Dashboard:     %s\n  Client portal: %s\n", operator, portal)
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-deadline:
			_, _ = fmt.Fprintln(out, "Tunnel started, no URL yet — the Tunnel page shows the link when cloudflared reports it")
			return nil
		case <-ticker.C:
		}
	}
}

// defaultTunnelURLTimeoutS is how long --tunnel waits to print the links when
// tunnel.url_parse_timeout_s is unset.
const defaultTunnelURLTimeoutS = 30

// tunnelLogLines is how much of cloudflared's output the manager keeps in
// memory; stdout.log under state/tunnel/ has all of it.
const tunnelLogLines = 200

// portalRefresh is how often a live portal re-reads its snapshot.
const portalRefresh = 30 * time.Second
