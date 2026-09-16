// Package tunnel supervises the dashboard's optional public URL: a
// Cloudflare quick tunnel (`cloudflared tunnel --url …`), which needs no
// Cloudflare account, login or DNS — only the binary on PATH.
//
// It is the only provider. autossh (Pinggy) and bore were removed: bore
// served plain http://, Pinggy needed SSH, and a single provider is what
// lets `tunnel.enabled: true` be the whole configuration.
//
// HTTP wiring (routes, the token gate for tunnelled requests) is
// internal/dashboard's. This package exposes a Manager, the install guide
// (install.go) and the pure capability gate (capabilities.go).
package tunnel

import (
	"fmt"
	"regexp"
	"strconv"
)

// Provider is the one provider's name, reported in status payloads.
const Provider = "cloudflared"

// Command is the binary the manager spawns.
const Command = "cloudflared"

// urlPattern matches the hostname cloudflared prints once the quick tunnel
// is up ("https://seasonal-deck-organisms-sf.trycloudflare.com"). At least
// one hyphen is required: cloudflared also logs its control endpoint,
// https://api.trycloudflare.com, when a request fails, and that is not a
// URL anybody should be handed.
var urlPattern = regexp.MustCompile(`https://[a-z0-9]+(?:-[a-z0-9]+)+\.trycloudflare\.com`)

// Args is cloudflared's argv (without the binary) for a quick tunnel to
// the dashboard on port. 127.0.0.1 rather than localhost: the dashboard
// binds IPv4 loopback by default and localhost may resolve to ::1 first.
func Args(port int) []string {
	return []string{
		"tunnel", "--no-autoupdate",
		"--url", "http://127.0.0.1:" + strconv.Itoa(port),
	}
}

// commandLine assembles the full argv, refusing a port that cannot be one
// rather than spawning a tunnel to nowhere.
func commandLine(cfg ManagerConfig) ([]string, error) {
	if cfg.Port <= 0 || cfg.Port > 65535 {
		return nil, fmt.Errorf("tunnel: port %d is not a port", cfg.Port)
	}
	command := cfg.Command
	if command == "" {
		command = Command
	}
	return append([]string{command}, Args(cfg.Port)...), nil
}
