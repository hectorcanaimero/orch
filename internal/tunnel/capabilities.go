package tunnel

import "strings"

// Capability gate reason tokens, matching
// orchestrator/dashboard/tunnel/deps.py's REASON_* constants exactly —
// these cross the wire verbatim in /api/tunnel/capabilities's JSON body.
const (
	ReasonOK             = "ok"
	ReasonConfigDisabled = "config_disabled"
	ReasonProfileGate    = "profile_gate"
	ReasonHostGate       = "host_gate"
	// ReasonBinaryMissing: every gate passed and cloudflared is not on PATH.
	ReasonBinaryMissing = "binary_missing"
)

// loopbackHosts mirrors deps.py's _LOOPBACK_HOSTS frozenset exactly,
// brackets included for the IPv6 literal — ExtractHost preserves them.
var loopbackHosts = map[string]bool{
	"127.0.0.1": true,
	"localhost": true,
	"::1":       true,
	"[::1]":     true,
}

// ExtractHost returns the case-insensitive host from a Host header value,
// port stripped. Ported from deps.py's `_extract_host` — HTTP-request
// access (reading the header) stays with internal/dashboard (G5.2); this
// takes the raw header value so it's testable without an http.Request.
//
// IPv6 literals arrive bracket-wrapped ("[::1]:7420"); the brackets are
// kept because that's the canonical form compared against. A bare IPv6
// literal (multiple colons, no brackets) is left alone — only a
// host:port shape has its trailing port stripped.
func ExtractHost(hostHeader string) string {
	raw := strings.ToLower(strings.TrimSpace(hostHeader))
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "[") {
		if end := strings.Index(raw, "]"); end >= 0 {
			return raw[:end+1]
		}
		return raw
	}
	if strings.Count(raw, ":") > 1 {
		return raw
	}
	if i := strings.Index(raw, ":"); i >= 0 {
		return raw[:i]
	}
	return raw
}

// IsLoopbackHost reports whether host (already run through ExtractHost)
// names one of the loopback forms the tunnel gate accepts.
func IsLoopbackHost(host string) bool {
	return loopbackHosts[host]
}

// Gates is the already-resolved state of TUN-3's three ordered checks —
// config enabled, operator profile, loopback host — extracted by
// internal/dashboard's HTTP layer from its own config and the request.
// Keeping this a plain struct of booleans (rather than taking a
// *config.Config or an *http.Request) is what makes EvaluateCapabilities
// unit-testable with no HTTP or config-loading fixture at all — same
// reason Python's TunnelManagerConfig is its own dataclass instead of a
// reference into the full DashboardConfig.
type Gates struct {
	Enabled         bool
	OperatorProfile bool
	LoopbackHost    bool
}

// EvaluateCapabilities is the non-raising mirror of deps.py's
// `evaluate_capabilities`, plus TUN-4's binary-presence layer folded in
// (Python's server.py does that consultation inline in the route handler;
// here it's one function so a caller can't reorder the gates by mistake).
//
// binaryOnPath is only consulted when every other gate already passed —
// same "gate order" guarantee TUN-4 documents: config → profile → host →
// binary, never checked out of order, and PATH is never consulted before
// the first three all pass.
func EvaluateCapabilities(gates Gates, binaryOnPath bool) (canControl bool, reason string) {
	if !gates.Enabled {
		return false, ReasonConfigDisabled
	}
	if !gates.OperatorProfile {
		return false, ReasonProfileGate
	}
	if !gates.LoopbackHost {
		return false, ReasonHostGate
	}
	if !binaryOnPath {
		return false, ReasonBinaryMissing
	}
	return true, ReasonOK
}
