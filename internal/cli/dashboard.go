package cli

import (
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/hectorcanaimero/orch/internal/config"
	"github.com/hectorcanaimero/orch/internal/dashboard"
)

// newDashboardCmd ports `orch dashboard` (the `run` function in
// orchestrator/dashboard/server.py).
//
// The server itself is internal/dashboard; this is the wiring — flags, the
// banner, and a signal handler so Ctrl+C shuts the listener down instead of
// killing the process mid-response.
func newDashboardCmd(flags *projectFlags) *cobra.Command {
	var (
		host    string
		port    int
		profile string
		token   string
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
			if err := dashCfg.Validate(); err != nil {
				return err
			}

			spa, err := dashboard.SPA()
			if err != nil {
				return fmt.Errorf("reading the embedded SPA: %w", err)
			}

			backend, closeDB, err := openBackend(ctx, paths, cfg)
			if err != nil {
				return err
			}
			defer func() { _ = closeDB() }()

			server, err := dashboard.New(dashCfg, dashboard.Options{
				Paths:  paths,
				Static: dashboard.SPAHandler(spa),
				State:  backend,
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
	return cmd
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

	addr := server.BoundAddr()
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
		if cfg.Token != "" {
			say("Token auth: ENABLED")
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
