package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/hectorcanaimero/orch/internal/config"
	"github.com/hectorcanaimero/orch/internal/dashboard"
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
		host       string
		port       int
		profile    string
		token      string
		withTunnel bool
	)

	cmd := &cobra.Command{
		Use:   "dashboard",
		Short: "Serve the operator dashboard",
		Long: "Serve the operator dashboard on a local HTTP port.\n\n" +
			"Reads the project's state and shows it; it never writes. The\n" +
			"stakeholder profile gates every data route behind a token and an\n" +
			"allow-list — see `profile` and `token` under `dashboard:` in\n" +
			"config.yaml, which the flags below override.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()

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
			if cmd.Flags().Changed("token") {
				dashCfg.Token = token
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
			if err := dashCfg.Validate(); err != nil {
				return err
			}

			spa, err := dashboard.SPA()
			if err != nil {
				return fmt.Errorf("reading the embedded SPA: %w", err)
			}

			tunnelOpts, stopTunnel, err := setUpTunnel(cmd, cfg, paths, dashCfg, withTunnel)
			if err != nil {
				return err
			}
			defer stopTunnel()

			server, err := dashboard.New(dashCfg, dashboard.Options{
				Paths:  paths,
				Static: dashboard.SPAHandler(spa),
				State:  backend,
				Tunnel: tunnelOpts,
			})
			if err != nil {
				return err
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

			go func() {
				<-server.Ready()
				printDashboardBanner(cmd, server, dashCfg, paths)
			}()
			return server.Serve(ctx)
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
		"Shared token a stakeholder session must present (default: config.yaml)")
	cmd.Flags().BoolVar(&withTunnel, "tunnel", false,
		"Also start the configured tunnel, and stop it on exit")
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

// setUpTunnel wires the tunnel half of the server, and starts it when asked.
//
// Returns the options the server needs and a stop func the caller defers. The
// stop func is always non-nil, so the defer at the call site needs no guard —
// a nil check there is the kind of thing that gets deleted in a refactor and
// panics in a shutdown path nobody tests.
//
// # Why --tunnel starts it and the dashboard does not
//
// Python's dashboard exposes start/stop as POST routes and the SPA drives
// them. G5.5 made the operator SPA read-only, so those routes have no
// consumer and are not ported (see internal/dashboard/tunnelroutes.go). That
// leaves the operator with no way to raise a tunnel at all — hence the flag.
// It is also the safer shape: the lever is on the command line, where the
// person holding it is the person who started the process.
func setUpTunnel(cmd *cobra.Command, cfg config.Config, paths config.Paths,
	dashCfg dashboard.Config, start bool) (dashboard.TunnelOptions, func(), error) {
	noop := func() {}

	if !cfg.Tunnel.Enabled {
		if start {
			return dashboard.TunnelOptions{}, noop, errors.New(
				"--tunnel needs `tunnel.enabled: true` in config.yaml")
		}
		return dashboard.TunnelOptions{}, noop, nil
	}

	mgr := tunnel.NewManager(paths.StateDir(), nil, tunnelLogLines)
	mcfg := tunnel.ManagerConfig{
		Provider:         cfg.Tunnel.Provider,
		Command:          cfg.Tunnel.Command,
		Args:             cfg.Tunnel.Args,
		URLRegex:         cfg.Tunnel.URLRegex,
		URLParseTimeoutS: cfg.Tunnel.URLParseTimeoutS,
		StopTimeout:      time.Duration(cfg.Tunnel.StopTimeoutS * float64(time.Second)),
	}
	// A lock left by a process that died without releasing it would refuse
	// every start until somebody deleted a file by hand. Swept once at boot,
	// which is the only moment it is safe to assume nothing of ours holds it.
	mgr.SweepStaleLock(&mcfg)

	opts := dashboard.TunnelOptions{
		Enabled:  true,
		Provider: cfg.Tunnel.Provider,
		Command:  cfg.Tunnel.Command,
		Manager:  mgr,
	}
	if !start {
		return opts, noop, nil
	}

	// The tunnel publishes a URL to the internet. Raising one from a
	// stakeholder-profile dashboard would publish a surface whose own gate
	// says the operator is not present, so the flag refuses rather than
	// asking the operator to notice.
	if dashCfg.Profile != dashboard.ProfileOperator {
		return dashboard.TunnelOptions{}, noop, fmt.Errorf(
			"--tunnel needs the %s profile; this dashboard is running as %s",
			dashboard.ProfileOperator, dashCfg.Profile)
	}

	state, err := mgr.Start(mcfg)
	if err != nil {
		return dashboard.TunnelOptions{}, noop, fmt.Errorf("starting the tunnel: %w", err)
	}
	if state.URL != nil {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Tunnel (%s): %s\n", cfg.Tunnel.Provider, *state.URL)
	} else {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(),
			"Tunnel (%s): started, no URL yet — /api/tunnel/status has it when it arrives\n",
			cfg.Tunnel.Provider)
	}

	return opts, func() {
		if _, err := mgr.Stop(); err != nil {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "[warn] stopping the tunnel: %v\n", err)
		}
	}, nil
}

// tunnelLogLines is how much of the provider's output the manager keeps. The
// buffer exists so `/api/tunnel/logs` can replay a tail; that route is not
// ported, so this only bounds memory for a long-lived process.
const tunnelLogLines = 200
