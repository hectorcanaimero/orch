import { AlertTriangle } from "lucide-react"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Skeleton } from "@/components/ui/skeleton"
import { useTunnelCapabilities } from "@/hooks/useTunnelCapabilities"
import { TunnelPanel } from "@/pages/TunnelPanel"
import type { TunnelCapabilityReason } from "@/lib/types"

const REASON_COPY: Record<TunnelCapabilityReason, string> = {
  ok: "Tunnel controls are available.",
  config_disabled:
    "The tunnel feature is disabled in `dashboard.yaml` (`tunnel.enabled: false`). Enable it and restart the dashboard to control the tunnel.",
  profile_gate:
    "Your dashboard profile is not `operator`. Tunnel control is operator-only; re-launch the dashboard with `--profile operator` (or a matching token) to see this panel.",
  host_gate:
    "This dashboard is not reachable at a loopback host (127.0.0.1 / localhost / [::1]). Tunnel control refuses non-loopback requests by design.",
  autossh_missing:
    "The `autossh` binary is not on PATH. Install it (e.g. `brew install autossh`) and reload this page.",
}

/**
 * Sprint E-5 — Tunnel route wrapper.
 *
 * This page is the ONLY mount point for `TunnelPanel`. The capability probe
 * runs first and gates the render entirely: when `can_control === false` the
 * panel is not in the React tree at all (design D#9, non-negotiable per
 * proposal). The empty state explains WHY control is unavailable so the
 * operator has a remediation path.
 */
export function TunnelPage() {
  const { data: caps, isLoading, isError, error } = useTunnelCapabilities()

  return (
    <div className="space-y-6">
      <header>
        <h1 className="text-2xl font-semibold tracking-tight">Tunnel</h1>
        <p className="text-sm text-muted-foreground">
          Ephemeral public URL supervised by the dashboard.
        </p>
      </header>

      {isLoading ? (
        <Skeleton className="h-64 w-full" />
      ) : null}

      {isError ? (
        <Alert variant="destructive">
          <AlertTriangle className="h-4 w-4" />
          <AlertTitle>Failed to reach `/api/tunnel/capabilities`</AlertTitle>
          <AlertDescription>
            <p>{error?.message ?? "Unknown error"}</p>
            <p className="mt-1 text-xs opacity-80">
              This endpoint is auth-free — any failure here means the dashboard
              itself is unreachable.
            </p>
          </AlertDescription>
        </Alert>
      ) : null}

      {caps && caps.can_control ? (
        <TunnelPanel capabilities={caps} />
      ) : caps ? (
        <Alert>
          <AlertTriangle className="h-4 w-4" />
          <AlertTitle>Tunnel control unavailable</AlertTitle>
          <AlertDescription>
            {REASON_COPY[caps.reason] ?? "Tunnel controls are not available."}
          </AlertDescription>
        </Alert>
      ) : null}
    </div>
  )
}
