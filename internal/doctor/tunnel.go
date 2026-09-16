package doctor

import "github.com/hectorcanaimero/orch/internal/tunnel"

// CheckTunnel reports whether the dashboard tunnel can start: cloudflared on
// PATH and no cloudflared config file blocking quick tunnels. A project with
// the tunnel disabled skips it — nobody needs cloudflared for a tunnel they
// do not use. bin and blocker come from tunnel.LookupBinary and
// tunnel.ConfigBlocker; taking them as arguments keeps this testable without
// a PATH or a home directory.
func CheckTunnel(enabled bool, bin tunnel.Binary, blocker string) Check {
	const name = "tunnel.cloudflared"
	switch {
	case !enabled:
		return Check{Name: name, Status: StatusSkip, Detail: "tunnel.enabled is false"}
	case !bin.Found:
		return Check{
			Name: name, Status: StatusWarn,
			Detail: "tunnel.enabled is true but cloudflared is not on PATH — the tunnel cannot start; " +
				"`orch dashboard --tunnel` and the dashboard's Tunnel page show the install steps",
			Remediation: tunnel.MissingBinaryMessage(),
		}
	case blocker != "":
		return Check{
			Name: name, Status: StatusWarn,
			Detail:      "cloudflared found, but " + blocker + " blocks quick tunnels — rename it while you use the tunnel",
			Remediation: tunnel.BlockerMessage(blocker),
		}
	}
	return Check{Name: name, Status: StatusOK, Detail: bin.Path + " (" + bin.Version + ")"}
}
