import { useEffect, useMemo, useState } from "react"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import {
  AlertTriangle,
  ChevronDown,
  Circle,
  Copy,
  ExternalLink,
  Loader2,
  Play,
  RefreshCw,
  Square,
} from "lucide-react"
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
import { useTunnelLogs } from "@/hooks/useTunnelLogs"
import { useTunnelStatus } from "@/hooks/useTunnelStatus"
import {
  startTunnel,
  stopTunnel,
  TunnelConflictHttpError,
} from "@/lib/api"
import { cn } from "@/lib/utils"
import type {
  TunnelCapabilities,
  TunnelConflictError,
  TunnelState,
} from "@/lib/types"

type BadgeVariant = "success" | "warning" | "danger" | "muted" | "info"

const STATE_META: Record<
  TunnelState,
  { label: string; badge: BadgeVariant; dotClass: string }
> = {
  idle: { label: "Idle", badge: "muted", dotClass: "text-zinc-400" },
  starting: { label: "Starting", badge: "info", dotClass: "text-sky-500" },
  running: {
    label: "Running",
    badge: "success",
    dotClass: "text-emerald-500",
  },
  stopping: {
    label: "Stopping",
    badge: "warning",
    dotClass: "text-amber-500",
  },
  error: { label: "Error", badge: "danger", dotClass: "text-red-500" },
}

const TUNNEL_URL_KEY = "orch_tunnel_last_url"

interface TunnelPanelProps {
  capabilities: TunnelCapabilities
}

/**
 * Sprint E-5 — Tunnel manager panel (TUN-4/5/6/10).
 *
 * MUST be rendered only when `capabilities.can_control === true` (design
 * D#9); parent enforces this via `useTunnelCapabilities`. The panel itself
 * does not re-check that invariant — mounting it under a false capability
 * would still work but generate 403/404 noise from the status hook.
 */
export function TunnelPanel({ capabilities }: TunnelPanelProps) {
  const qc = useQueryClient()
  const {
    data: status,
    isLoading,
    isError,
    error,
  } = useTunnelStatus({ enabled: capabilities.can_control })

  const [logsOpen, setLogsOpen] = useState(false)
  const [copied, setCopied] = useState(false)
  const [conflict, setConflict] = useState<TunnelConflictError | null>(null)

  // Persist the last known URL across page refreshes. We save it when the
  // backend returns one and clear it only when the tunnel goes back to idle.
  const [cachedUrl, setCachedUrl] = useState<string | null>(
    () => window.localStorage.getItem(TUNNEL_URL_KEY),
  )

  useEffect(() => {
    if (status?.url) {
      window.localStorage.setItem(TUNNEL_URL_KEY, status.url)
      setCachedUrl(status.url)
    } else if (status?.state === "idle") {
      window.localStorage.removeItem(TUNNEL_URL_KEY)
      setCachedUrl(null)
    }
  }, [status?.url, status?.state])

  const startMutation = useMutation({
    mutationFn: startTunnel,
    onSuccess: () => {
      setConflict(null)
      void qc.invalidateQueries({ queryKey: ["tunnel-status"] })
    },
    onError: (err) => {
      if (err instanceof TunnelConflictHttpError) {
        setConflict(err.body)
      }
    },
  })

  const stopMutation = useMutation({
    mutationFn: stopTunnel,
    onSuccess: () => {
      setConflict(null)
      void qc.invalidateQueries({ queryKey: ["tunnel-status"] })
    },
    onError: (err) => {
      if (err instanceof TunnelConflictHttpError) {
        setConflict(err.body)
      }
    },
  })

  const state: TunnelState = status?.state ?? "idle"
  const meta = STATE_META[state] ?? STATE_META.idle
  const running = state === "running" || state === "starting"

  // Live URL takes priority; fall back to cached only while tunnel is not idle
  const displayUrl = status?.url ?? (state !== "idle" ? cachedUrl : null)
  const transitioning =
    state === "starting" ||
    state === "stopping" ||
    startMutation.isPending ||
    stopMutation.isPending

  const logs = useTunnelLogs({
    enabled: capabilities.can_control && logsOpen,
  })

  const conflictCopy = useMemo(() => conflictMessage(conflict), [conflict])

  const handleCopy = async () => {
    if (!displayUrl) return
    try {
      await navigator.clipboard.writeText(displayUrl)
      setCopied(true)
      window.setTimeout(() => setCopied(false), 1500)
    } catch {
      // Clipboard rejection (permission / non-secure context) is harmless
      // — the link is still visible for a manual copy.
    }
  }

  return (
    <Card>
      <CardHeader className="gap-2">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex items-center gap-3">
            <CardTitle className="flex items-center gap-2">
              Tunnel
              <Badge variant={meta.badge} className="gap-1.5">
                <Circle
                  className={cn("h-2 w-2 fill-current", meta.dotClass)}
                  aria-hidden
                />
                {meta.label}
              </Badge>
            </CardTitle>
            {capabilities.provider ? (
              <Badge variant="outline" className="font-mono text-[10px]">
                {capabilities.provider}
              </Badge>
            ) : null}
          </div>
          <div className="flex items-center gap-2">
            {running ? (
              <Button
                type="button"
                size="sm"
                variant="outline"
                onClick={() => stopMutation.mutate()}
                disabled={transitioning}
                aria-label="Stop tunnel"
                className="gap-1.5"
              >
                {stopMutation.isPending || (state as TunnelState) === "stopping" ? (
                  <Loader2 className="h-4 w-4 animate-spin" aria-hidden />
                ) : (
                  <Square className="h-4 w-4" aria-hidden />
                )}
                Stop
              </Button>
            ) : (
              <Button
                type="button"
                size="sm"
                onClick={() => startMutation.mutate()}
                disabled={transitioning}
                aria-label="Start tunnel"
                className="gap-1.5"
              >
                {startMutation.isPending || (state as TunnelState) === "starting" ? (
                  <Loader2 className="h-4 w-4 animate-spin" aria-hidden />
                ) : (
                  <Play className="h-4 w-4" aria-hidden />
                )}
                Start
              </Button>
            )}
          </div>
        </div>
        <CardDescription>
          Operator-only, loopback-only ephemeral public URL. See{" "}
          <code className="font-mono text-xs">
            /api/tunnel/*
          </code>{" "}
          for the underlying contract.
        </CardDescription>
      </CardHeader>

      <CardContent className="space-y-4">
        {isLoading ? (
          <div className="space-y-2">
            <Skeleton className="h-9 w-full" />
            <Skeleton className="h-16 w-full" />
          </div>
        ) : null}

        {isError && !isLoading ? (
          <Alert variant="destructive">
            <AlertTriangle className="h-4 w-4" />
            <AlertTitle>Failed to load tunnel status</AlertTitle>
            <AlertDescription>
              {error?.message ?? "Unknown error"}
            </AlertDescription>
          </Alert>
        ) : null}

        {status ? (
          <>
            <UrlRow url={displayUrl} onCopy={handleCopy} copied={copied} />

            <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
              <Stat label="Restarts" value={status.restart_count} />
              <Stat
                label="Autossh reconnects"
                value={status.autossh_reconnects}
              />
              <Stat label="PID" value={status.pid ?? "—"} mono />
              <Stat
                label="Started"
                value={formatStarted(status.started_at)}
                mono
              />
            </div>

            {status.last_error ? (
              <Alert variant="destructive">
                <AlertTriangle className="h-4 w-4" />
                <AlertTitle>Last error</AlertTitle>
                <AlertDescription className="font-mono text-xs">
                  {status.last_error}
                </AlertDescription>
              </Alert>
            ) : null}
          </>
        ) : null}

        {conflict ? (
          <Alert
            variant={conflict.error === "not_running" ? "default" : "destructive"}
          >
            <AlertTriangle className="h-4 w-4" />
            <AlertTitle>{conflictCopy.title}</AlertTitle>
            <AlertDescription className="flex items-center justify-between gap-3">
              <span>{conflictCopy.body}</span>
              <Button
                type="button"
                size="sm"
                variant="outline"
                onClick={() => setConflict(null)}
              >
                Dismiss
              </Button>
            </AlertDescription>
          </Alert>
        ) : null}

        <LogsToggle
          open={logsOpen}
          onToggle={() => setLogsOpen((p) => !p)}
          lines={logs.lines}
          status={logs.status}
        />
      </CardContent>
    </Card>
  )
}

function UrlRow({
  url,
  onCopy,
  copied,
}: {
  url: string | null
  onCopy: () => void
  copied: boolean
}) {
  if (!url) {
    return (
      <div className="rounded-md border border-dashed bg-muted/40 px-3 py-2 text-sm text-muted-foreground">
        No URL yet — start the tunnel to publish one.
      </div>
    )
  }
  return (
    <div className="flex flex-wrap items-center gap-2 rounded-md border bg-muted/30 px-3 py-2">
      <a
        href={url}
        target="_blank"
        rel="noopener noreferrer"
        className="flex items-center gap-1.5 truncate font-mono text-sm text-foreground hover:underline"
      >
        <ExternalLink className="h-3.5 w-3.5 shrink-0" aria-hidden />
        <span className="truncate">{url}</span>
      </a>
      <div className="ml-auto flex items-center gap-2">
        <Button
          type="button"
          size="sm"
          variant="outline"
          onClick={onCopy}
          className="gap-1.5"
          aria-label="Copy tunnel URL"
        >
          <Copy className="h-3.5 w-3.5" aria-hidden />
          {copied ? "Copied" : "Copy"}
        </Button>
      </div>
    </div>
  )
}

function Stat({
  label,
  value,
  mono,
}: {
  label: string
  value: string | number
  mono?: boolean
}) {
  return (
    <div className="rounded-md border bg-background px-3 py-2">
      <div className="text-[10px] uppercase tracking-wide text-muted-foreground">
        {label}
      </div>
      <div className={cn("text-sm text-foreground", mono && "font-mono")}>
        {value}
      </div>
    </div>
  )
}

function LogsToggle({
  open,
  onToggle,
  lines,
  status,
}: {
  open: boolean
  onToggle: () => void
  lines: string[]
  status: ReturnType<typeof useTunnelLogs>["status"]
}) {
  return (
    <div className="rounded-md border">
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={open}
        className="flex w-full items-center justify-between gap-2 px-3 py-2 text-sm hover:bg-muted/40 focus-visible:bg-muted/40 focus-visible:outline-none"
      >
        <span className="flex items-center gap-2">
          <ChevronDown
            className={cn("h-4 w-4 transition-transform", open && "rotate-180")}
            aria-hidden
          />
          Log stream
          <Badge variant="muted" className="text-[10px]">
            {status}
          </Badge>
        </span>
        {open && status === "connecting" ? (
          <RefreshCw className="h-3.5 w-3.5 animate-spin" aria-hidden />
        ) : null}
      </button>
      {open ? (
        <div className="max-h-64 overflow-auto border-t bg-zinc-950 px-3 py-2 font-mono text-[11px] leading-relaxed text-zinc-100">
          {lines.length === 0 ? (
            <div className="text-zinc-500">
              Waiting for output… the backend redacts token patterns before
              emitting.
            </div>
          ) : (
            lines.map((line, i) => (
              <div key={i} className="whitespace-pre-wrap break-all">
                {line}
              </div>
            ))
          )}
        </div>
      ) : null}
    </div>
  )
}

function conflictMessage(conflict: TunnelConflictError | null): {
  title: string
  body: string
} {
  if (!conflict) return { title: "", body: "" }
  switch (conflict.error) {
    case "already_running":
      return {
        title: "Already running",
        body: `Tunnel is in "${conflict.state}" — stop it first before starting again.`,
      }
    case "locked":
      return {
        title: "Lock held",
        body:
          "Another dashboard process appears to own the tunnel lock. Run `orch doctor` to inspect.",
      }
    case "not_running":
      return {
        title: "Nothing to stop",
        body: "Tunnel is idle.",
      }
  }
}

function formatStarted(iso: string | null): string {
  if (!iso) return "—"
  try {
    return new Date(iso).toLocaleTimeString()
  } catch {
    return iso
  }
}

