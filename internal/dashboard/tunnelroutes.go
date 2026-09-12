package dashboard

import (
	"net/http"
	"os/exec"

	"github.com/hectorcanaimero/orch/internal/tunnel"
)

// The two tunnel routes the trimmed SPA consumes, ported from
// `orchestrator/dashboard/server.py` and `dashboard/tunnel/deps.py`.
//
// `/start`, `/stop` and `/logs` are NOT here. G5.5 made the operator SPA
// read-only, so they have no consumer — and this server takes a read-only
// `StateReader` by construction (CHECKLIST rule 13). Serving a lever nothing
// pulls is how a surface grows an attacker's way.
//
// # The three gates, in a fixed order
//
// config enabled → operator profile → loopback host, and PATH is consulted
// only after all three pass. The order is TUN-3/TUN-4's and it is not
// cosmetic: each gate's failure reveals less than the next one's. Answering
// "autossh is missing" to a stakeholder on a shared URL would tell them what
// the box runs before establishing they may ask.

// tunnelDeps is what the routes need from the process: whether the tunnel is
// configured at all, and the manager when it is.
//
// A nil Manager with Enabled true is a wiring bug, not a config state, and
// `/status` says so with a 500 rather than pretending the tunnel is idle.
type tunnelDeps struct {
	Enabled  bool
	Provider string
	// Command is the binary the provider spawns, looked up on PATH for the
	// capabilities answer.
	Command string
	Manager *tunnel.Manager
}

func (s *Server) tunnelRoutes() []route {
	return []route{
		{pattern: "GET /api/tunnel/capabilities", name: "api_tunnel_capabilities",
			handler: s.handleTunnelCapabilities},
		{pattern: "GET /api/tunnel/status", name: "api_tunnel_status",
			handler: s.handleTunnelStatus},
	}
}

// capabilitiesPayload is `/api/tunnel/capabilities`'s body.
//
// `reason` is the FIRST failing gate, never a list — TUN-4 is explicit about
// that. `reasons` is the short-form list the SPA renders as chips, and it
// holds at most two entries: the first failing gate's short name, plus
// `autossh_missing` when every gate passed and only the binary was absent.
type capabilitiesPayload struct {
	Enabled    bool     `json:"enabled"`
	Provider   *string  `json:"provider"`
	CanControl bool     `json:"can_control"`
	Reason     string   `json:"reason"`
	Reasons    []string `json:"reasons"`
}

// shortReason maps a gate's reason to the chip the SPA draws. Three of the
// four have one; `autossh_missing` is already short and is appended verbatim.
var shortReason = map[string]string{
	tunnel.ReasonConfigDisabled: "disabled",
	tunnel.ReasonProfileGate:    "not_operator",
	tunnel.ReasonHostGate:       "not_loopback",
}

// handleTunnelCapabilities is deliberately auth-free and always 200.
//
// It is the one route on the stakeholder allow-list that also skips the token
// check (see noTokenRoutes), and the reason is in the SPA's boot order: it
// decides whether to draw the tunnel panel BEFORE it has asked anybody for a
// token. A 401 here would mean the panel could never appear, even for someone
// holding a valid one.
//
// What it discloses is bounded by design: three booleans about the server's
// own configuration and which gate stopped you. Never the token, never the
// URL, never the state.
func (s *Server) handleTunnelCapabilities(w http.ResponseWriter, r *http.Request) {
	deps := s.tunnel
	gates := tunnel.Gates{
		Enabled:         deps.Enabled,
		OperatorProfile: s.cfg.Profile == ProfileOperator,
		LoopbackHost:    tunnel.IsLoopbackHost(tunnel.ExtractHost(r.Host)),
	}

	// PATH is only consulted once every other gate has passed — the gate-order
	// guarantee. Checking it first would be cheaper and would leak what the
	// box has installed to a caller who is not allowed to ask.
	binaryOnPath := false
	if gates.Enabled && gates.OperatorProfile && gates.LoopbackHost {
		binaryOnPath = commandOnPath(deps.Command)
	}
	canControl, reason := tunnel.EvaluateCapabilities(gates, binaryOnPath)

	reasons := []string{}
	if !canControl {
		if short, ok := shortReason[reason]; ok {
			reasons = append(reasons, short)
		} else {
			// autossh_missing has no short form; Python appends it as-is.
			reasons = append(reasons, reason)
		}
	}

	var provider *string
	if deps.Enabled && deps.Provider != "" {
		p := deps.Provider
		provider = &p
	}

	writeJSON(w, http.StatusOK, capabilitiesPayload{
		Enabled:    deps.Enabled,
		Provider:   provider,
		CanControl: canControl,
		Reason:     reason,
		Reasons:    reasons,
	})
}

// handleTunnelStatus answers the manager's state, behind all three gates.
//
// The status codes are Python's and each says something different: 404 when
// the tunnel is not configured (the route may as well not exist), 403 for a
// profile or host that may not ask. `can_control`, not `enabled`, is what the
// SPA gates its panel on — a contract inherited from Python (D#9).
func (s *Server) handleTunnelStatus(w http.ResponseWriter, r *http.Request) {
	deps := s.tunnel
	if !deps.Enabled {
		writeJSON(w, http.StatusNotFound, errorPayload{Detail: "not found"})
		return
	}
	if s.cfg.Profile != ProfileOperator {
		writeJSON(w, http.StatusForbidden, errorPayload{Detail: "operator only"})
		return
	}
	if !tunnel.IsLoopbackHost(tunnel.ExtractHost(r.Host)) {
		writeJSON(w, http.StatusForbidden, errorPayload{Detail: "loopback only"})
		return
	}
	if deps.Manager == nil {
		// Enabled with no manager is a wiring bug, not a config state. Saying
		// "idle" would be a lie the operator cannot act on.
		s.failRead(w, "tunnel status", errNoTunnelManager)
		return
	}
	writeJSON(w, http.StatusOK, deps.Manager.Status())
}

// commandOnPath is `shutil.which`. An empty command is not on PATH — a
// provider with no binary configured cannot be spawned, and reporting it as
// available would move the failure to the moment somebody presses start.
func commandOnPath(name string) bool {
	if name == "" {
		return false
	}
	_, err := exec.LookPath(name)
	return err == nil
}
