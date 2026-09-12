import { useState } from "react"
import { AlertTriangle, Circle, Copy, ExternalLink } from "lucide-react"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Skeleton } from "@/components/ui/skeleton"
import { useTunnelCapabilities } from "@/hooks/useTunnelCapabilities"
import { useTunnelStatus } from "@/hooks/useTunnelStatus"
import { cn } from "@/lib/utils"
import type { TunnelCapabilityReason, TunnelState } from "@/lib/types"

const REASON_COPY: Record<TunnelCapabilityReason, string> = {
  ok: "Tunnel status is available.",
  config_disabled:
    "The tunnel feature is disabled in `dashboard.yaml` (`tunnel.enabled: false`).",
  profile_gate:
    "Your dashboard profile is not `operator`. Tunnel status is operator-only.",
  host_gate:
    "This dashboard is not reachable at a loopback host (127.0.0.1 / localhost / [::1]). Tunnel status refuses non-loopback requests by design.",
  autossh_missing: "The `autossh` binary is not on PATH.",
}

type BadgeVariant = "success" | "warning" | "danger" | "muted" | "info"

const STATE_META: Record<
  TunnelState,
  { label: string; badge: BadgeVariant; dotClass: string }
> = {
  idle: { label: "Idle", badge: "muted", dotClass: "text-zinc-400" },
  starting: { label: "Starting", badge: "info", dotClass: "text-sky-500" },
  running: { label: "Running", badge: "success", dotClass: "text-emerald-500" },
  stopping: { label: "Stopping", badge: "warning", dotClass: "text-amber-500" },
  error: { label: "Error", badge: "danger", dotClass: "text-red-500" },
}

/**
 * Tunnel status panel (G5.5 — trimmed from the Sprint E-5 manager UI).
 *
 * Read-only: reports whatever the dashboard's own tunnel supervisor is
 * doing, but no longer starts, stops, or streams its logs from the SPA —
 * that control surface (and `/api/tunnel/start`, `/api/tunnel/stop`,
 * `/api/tunnel/logs`) lost its only consumer here; see
 * docs/brainstorm/go-migration-notes/sonnet-2.md. Still reads
 * `/api/tunnel/capabilities` and `/api/tunnel/status`, both unchanged.
 */
export function TunnelPage() {
  const { data: caps, isLoading, isError, error } = useTunnelCapabilities()
  // `can_control` (not `enabled`) is the gate — same D#9 constraint the
  // pre-trim panel enforced: `/api/tunnel/status` must never be polled when
  // it's false. Losing Start/Stop doesn't relax that.
  const { data: status } = useTunnelStatus({ enabled: !!caps?.can_control })
  const [copied, setCopied] = useState(false)

  const state: TunnelState = status?.state ?? "idle"
  const meta = STATE_META[state] ?? STATE_META.idle

  const handleCopy = async () => {
    if (!status?.url) return
    try {
      await navigator.clipboard.writeText(status.url)
      setCopied(true)
      window.setTimeout(() => setCopied(false), 1500)
    } catch {
      // Clipboard rejection (permission / non-secure context) is harmless
      // — the link is still visible for a manual copy.
    }
  }

  return (
    <div className="space-y-6">
      <header>
        <h1 className="text-2xl font-semibold tracking-tight">Tunnel</h1>
        <p className="text-sm text-muted-foreground">
          Ephemeral public URL supervised by the dashboard. Start/stop moved
          to the dashboard's own controls — this is a read-only status view.
        </p>
      </header>

      {isLoading ? <Skeleton className="h-40 w-full" /> : null}

      {isError ? (
        <Alert variant="destructive">
          <AlertTriangle className="h-4 w-4" />
          <AlertTitle>Failed to reach `/api/tunnel/capabilities`</AlertTitle>
          <AlertDescription>{error?.message ?? "Unknown error"}</AlertDescription>
        </Alert>
      ) : null}

      {caps && !caps.can_control ? (
        <Alert>
          <AlertTriangle className="h-4 w-4" />
          <AlertTitle>Tunnel status unavailable</AlertTitle>
          <AlertDescription>
            {REASON_COPY[caps.reason] ?? "Tunnel status is not available."}
          </AlertDescription>
        </Alert>
      ) : null}

      {caps?.can_control ? (
        <Card>
          <CardHeader className="gap-2">
            <div className="flex flex-wrap items-center gap-3">
              <CardTitle className="flex items-center gap-2">
                Status
                <Badge variant={meta.badge} className="gap-1.5">
                  <Circle
                    className={cn("h-2 w-2 fill-current", meta.dotClass)}
                    aria-hidden
                  />
                  {meta.label}
                </Badge>
              </CardTitle>
              {caps.provider ? (
                <Badge variant="outline" className="font-mono text-[10px]">
                  {caps.provider}
                </Badge>
              ) : null}
            </div>
            <CardDescription>
              See <code className="font-mono text-xs">/api/tunnel/*</code> for
              the underlying contract.
            </CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            {status?.url ? (
              <div className="flex flex-wrap items-center gap-2 rounded-md border bg-muted/30 px-3 py-2">
                <a
                  href={status.url}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="flex items-center gap-1.5 truncate font-mono text-sm text-foreground hover:underline"
                >
                  <ExternalLink className="h-3.5 w-3.5 shrink-0" aria-hidden />
                  <span className="truncate">{status.url}</span>
                </a>
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  onClick={handleCopy}
                  className="ml-auto gap-1.5"
                  aria-label="Copy tunnel URL"
                >
                  <Copy className="h-3.5 w-3.5" aria-hidden />
                  {copied ? "Copied" : "Copy"}
                </Button>
              </div>
            ) : (
              <div className="rounded-md border border-dashed bg-muted/40 px-3 py-2 text-sm text-muted-foreground">
                No URL — tunnel is idle.
              </div>
            )}

            {status?.last_error ? (
              <Alert variant="destructive">
                <AlertTriangle className="h-4 w-4" />
                <AlertTitle>Last error</AlertTitle>
                <AlertDescription className="font-mono text-xs">
                  {status.last_error}
                </AlertDescription>
              </Alert>
            ) : null}
          </CardContent>
        </Card>
      ) : null}
    </div>
  )
}
