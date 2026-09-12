// Package tunnel ports orchestrator/dashboard/tunnel (Sprint E-5): a
// long-lived subprocess supervisor for the dashboard's optional public-URL
// tunnel, plus the pure three-gate capability check
// `/api/tunnel/capabilities` reports. Two providers only — autossh
// (fronting a Pinggy SSH tunnel) and bore — matching
// orchestrator/dashboard/tunnel/providers.py's PROVIDERS registry exactly;
// no cloudflared, despite that name appearing in an earlier (wrong) plan
// text and one Go comment — see
// docs/brainstorm/go-migration-notes/sonnet-2.md.
//
// HTTP wiring (routes, auth, config loading from config.yaml) is
// internal/dashboard's — G5.2, a different lane. This package exposes pure
// data and a Manager whose methods take a ManagerConfig value, mirroring
// why Python's TunnelManagerConfig is its own dataclass rather than a
// reference into the full DashboardConfig: tests build one without
// dragging in everything else.
package tunnel

import (
	"fmt"
	"regexp"
	"strings"
)

// Provider names, pinned so config can't swap in an arbitrary binary.
const (
	ProviderAutossh = "autossh"
	ProviderBore    = "bore"
)

// ProviderSpec is a frozen provider entry: name, pinned binary, default
// argv, and the regexes the manager's stdout reader matches against.
type ProviderSpec struct {
	Name        string
	Command     string
	DefaultArgs []string

	// URLPattern is matched against each line of the child's stdout.
	URLPattern string
	// URLTemplate assembles a partial URL some providers emit (bore:
	// "bore.pub:12345", captured as a named group) into a full one, using
	// "{name}" placeholders resolved from URLPattern's named groups —
	// mirrors Python's `url_template.format_map(match.groupdict())`.
	// Empty means "the provider emits a full URL verbatim" (autossh); the
	// manager then uses the whole match instead.
	URLTemplate string

	// ReconnectPattern, when non-empty, matches a line signalling the
	// provider's OWN supervisor silently reconnected the tunnel — counted
	// separately from operator-driven stop/start cycles. Matched
	// case-insensitively, same as Python's `re.IGNORECASE`. bore has none:
	// it exits on drop rather than reconnecting itself.
	ReconnectPattern string
}

// Pinggy's free tier assigns a random subdomain under a.pinggy.link;
// autossh writes the assigned URL to stdout once the ssh handshake
// succeeds.
const autosshURLPattern = `https://[a-z0-9-]+\.a\.pinggy\.link`

// autossh's own monitoring loop logs when it decides to restart the child
// ssh — matched so a supervisor-internal reconnect is counted distinctly
// from a manual stop/start. Wording is stable across autossh 1.4c.
const autosshReconnectPattern = `(?:starting ssh|ssh exited|ssh reconnecting|connection lost|port forwarding restarted)`

var autosshDefaultArgs = []string{
	"-M", "0",
	"-o", "StrictHostKeyChecking=no",
	"-o", "ServerAliveInterval=30",
	"-o", "ExitOnForwardFailure=yes",
	"-p", "443",
	"-R", "0:localhost:7420",
	"a.pinggy.io",
}

// bore emits a line shaped like:
//
//	2024-01-15T12:00:00.000000Z  INFO bore_cli::client: listening at bore.pub:12345
//
// We capture the assigned public port and assemble the URL via
// URLTemplate. bore.pub is TCP-only (no HTTPS termination on the free
// public server), so the result is http://, not https://. bore has no
// autossh-style supervisor reconnect loop — if it drops, the process
// exits and the manager transitions to error; the operator restarts it.
const boreURLPattern = `listening at bore\.pub:(?P<port>\d+)`
const boreURLTemplate = "http://bore.pub:{port}"

var boreDefaultArgs = []string{
	"local", "7420",
	"--to", "bore.pub",
}

// Providers is the provider allowlist, keyed by name. Frozen at package
// init — nothing in this package mutates it after that.
var Providers = map[string]ProviderSpec{
	ProviderAutossh: {
		Name:             ProviderAutossh,
		Command:          "autossh",
		DefaultArgs:      autosshDefaultArgs,
		URLPattern:       autosshURLPattern,
		ReconnectPattern: autosshReconnectPattern,
	},
	ProviderBore: {
		Name:        ProviderBore,
		Command:     "bore",
		DefaultArgs: boreDefaultArgs,
		URLPattern:  boreURLPattern,
		URLTemplate: boreURLTemplate,
	},
}

// Resolve looks up a provider by name. ok is false on a miss — the config
// gate is expected to have already validated the name against
// KnownProviders before this is ever called.
func Resolve(name string) (spec ProviderSpec, ok bool) {
	spec, ok = Providers[name]
	return spec, ok
}

// KnownProviders returns every registered provider name.
func KnownProviders() []string {
	names := make([]string, 0, len(Providers))
	for name := range Providers {
		names = append(names, name)
	}
	return names
}

// compileURLRegex compiles pattern (spec.URLPattern, or cfg.URLRegex when
// an operator overrides it) once per Start call.
func compileURLRegex(pattern string) (*regexp.Regexp, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("tunnel: compile URL regex %q: %w", pattern, err)
	}
	return re, nil
}

// compileReconnectRegex compiles spec.ReconnectPattern case-insensitively.
// An empty pattern (bore) compiles to a regexp that matches nothing —
// `(?i)` on an empty string is a valid, always-false-on-search pattern in
// Go's RE2, so callers don't need a nil check.
func compileReconnectRegex(spec ProviderSpec) (*regexp.Regexp, error) {
	if spec.ReconnectPattern == "" {
		// An empty pattern must mean "never matches" (bore has no
		// supervisor-reconnect concept) — "(?i)" alone is a valid regex
		// that matches the empty string at every position, which is the
		// opposite of what an absent pattern should mean.
		return regexp.MustCompile(`$^`), nil
	}
	re, err := regexp.Compile("(?i)" + spec.ReconnectPattern)
	if err != nil {
		return nil, fmt.Errorf("tunnel: compile reconnect regex %q: %w", spec.ReconnectPattern, err)
	}
	return re, nil
}

// formatURL renders template ("http://bore.pub:{port}") by substituting
// each "{name}" placeholder with the named capture group of the same name
// from re's match against line. template empty means "use the whole
// match" (autossh-style, no assembly needed).
func formatURL(template string, re *regexp.Regexp, match []string) string {
	if template == "" {
		return match[0]
	}
	out := template
	for i, name := range re.SubexpNames() {
		if name == "" || i >= len(match) {
			continue
		}
		out = strings.ReplaceAll(out, "{"+name+"}", match[i])
	}
	return out
}
