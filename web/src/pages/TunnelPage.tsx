import { useEffect, useState, type ReactNode } from "react"
import { useQueryClient } from "@tanstack/react-query"
import { AlertTriangle, Circle, Copy, ExternalLink, RefreshCw } from "lucide-react"
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
import { startTunnel, stopTunnel } from "@/lib/api"
import { cn } from "@/lib/utils"
import type { TunnelCapabilities, TunnelState, TunnelStatus } from "@/lib/types"

type BadgeVariant = "success" | "warning" | "danger" | "muted" | "info"

const STATE_META: Record<TunnelState, { label: string; badge: BadgeVariant; dotClass: string }> = {
  idle: { label: "Off", badge: "muted", dotClass: "text-zinc-400" },
  starting: { label: "Starting", badge: "info", dotClass: "text-sky-500" },
  running: { label: "Running", badge: "success", dotClass: "text-emerald-500" },
  stopping: { label: "Stopping", badge: "warning", dotClass: "text-amber-500" },
  error: { label: "Stopped with an error", badge: "danger", dotClass: "text-red-500" },
}

/**
 * Dashboard tunnel: share this dashboard over a Cloudflare quick tunnel.
 *
 * Three jobs, in the order an operator meets them: say why sharing is not
 * available (disabled, wrong profile, not on this machine), walk through
 * installing cloudflared when it is missing, and start/stop the tunnel with
 * the link to send. The server is the authority on every gate
 * (internal/dashboard/tunnelroutes.go); this page only explains them.
 */
export function TunnelPage() {
  const caps = useTunnelCapabilities()
  const { data: status } = useTunnelStatus({ enabled: !!caps.data?.can_control })

  return (
    <div className="space-y-6">
      <header>
        <h1 className="text-2xl font-semibold tracking-tight">Tunnel</h1>
        <p className="max-w-2xl text-sm text-muted-foreground">
          Share this dashboard over a Cloudflare quick tunnel: a public
          https://…trycloudflare.com address, with no Cloudflare account
          needed. Anyone who opens it needs the token that comes in the link.
        </p>
      </header>

      {caps.isLoading ? <Skeleton className="h-40 w-full" /> : null}

      {caps.isError ? (
        <Alert variant="destructive">
          <AlertTriangle className="h-4 w-4" />
          <AlertTitle>Could not ask the dashboard about the tunnel</AlertTitle>
          <AlertDescription>{caps.error?.message ?? "Unknown error"}</AlertDescription>
        </Alert>
      ) : null}

      {caps.data ? (
        <TunnelBody
          caps={caps.data}
          status={status}
          checking={caps.isFetching}
          onCheck={() => void caps.refetch()}
        />
      ) : null}
    </div>
  )
}

function TunnelBody({
  caps,
  status,
  checking,
  onCheck,
}: {
  caps: TunnelCapabilities
  status: TunnelStatus | undefined
  checking: boolean
  onCheck: () => void
}) {
  switch (caps.reason) {
    case "config_disabled":
      return (
        <Card>
          <CardHeader>
            <CardTitle>The tunnel is off for this project</CardTitle>
            <CardDescription>
              Add this to <code className="font-mono text-xs">.orchestrator/config.yaml</code>,
              then restart <code className="font-mono text-xs">orch dashboard</code>:
            </CardDescription>
          </CardHeader>
          <CardContent>
            <CommandBlock command={"tunnel:\n  enabled: true"} />
          </CardContent>
        </Card>
      )
    case "profile_gate":
      return (
        <Notice title="Only the operator dashboard can share itself">
          This dashboard runs with a stakeholder profile. Start the tunnel from
          the operator dashboard (<code className="font-mono text-xs">orch dashboard</code>{" "}
          without <code className="font-mono text-xs">--profile</code>).
        </Notice>
      )
    case "host_gate":
      return (
        <Notice title="Open this page on the machine running orch">
          The tunnel can only be controlled from http://127.0.0.1 or
          http://localhost, so nobody reaching the dashboard another way can
          publish it.
        </Notice>
      )
    case "binary_missing":
      return <InstallGuide caps={caps} checking={checking} onCheck={onCheck} />
    default:
      return (
        <div className="space-y-6">
          {caps.config_blocker ? (
            <Alert>
              <AlertTriangle className="h-4 w-4" />
              <AlertTitle>A cloudflared config file will stop the tunnel</AlertTitle>
              <AlertDescription className="space-y-2">
                <p>
                  Cloudflare quick tunnels do not start while{" "}
                  <code className="font-mono text-xs">{caps.config_blocker}</code> exists.
                  Rename it while you share the dashboard, then check again:
                </p>
                <CommandBlock command={`mv ${caps.config_blocker} ${caps.config_blocker}.bak`} />
                <Button type="button" size="sm" variant="outline" onClick={onCheck} disabled={checking}>
                  <RefreshCw className={cn("mr-1.5 h-3.5 w-3.5", checking && "animate-spin")} aria-hidden />
                  Check again
                </Button>
              </AlertDescription>
            </Alert>
          ) : null}
          <TunnelControl caps={caps} status={status} />
          <Limits />
        </div>
      )
  }
}

function Notice({ title, children }: { title: string; children: ReactNode }) {
  return (
    <Alert>
      <AlertTriangle className="h-4 w-4" />
      <AlertTitle>{title}</AlertTitle>
      <AlertDescription>{children}</AlertDescription>
    </Alert>
  )
}

function InstallGuide({
  caps,
  checking,
  onCheck,
}: {
  caps: TunnelCapabilities
  checking: boolean
  onCheck: () => void
}) {
  const guides = caps.guides ?? []
  const recommended = caps.host?.recommended_guide ?? ""
  const [selected, setSelected] = useState(recommended || guides[0]?.id)
  const guide = guides.find((g) => g.id === selected) ?? guides[0]

  return (
    <Card>
      <CardHeader className="gap-1">
        <CardTitle>Install cloudflared to share this dashboard</CardTitle>
        <CardDescription className="max-w-2xl">
          The tunnel is Cloudflare&apos;s <code className="font-mono text-xs">cloudflared</code>{" "}
          program, and it is not on this machine&apos;s PATH yet. It takes a
          minute: no Cloudflare account, login or domain is needed. Run these
          steps in a terminal, then check again — no restart needed.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-5">
        <div className="flex flex-wrap gap-2" role="group" aria-label="Installation method">
          {guides.map((g) => (
            <Button
              key={g.id}
              type="button"
              size="sm"
              variant={g.id === guide?.id ? "default" : "outline"}
              aria-pressed={g.id === guide?.id}
              onClick={() => setSelected(g.id)}
            >
              {g.label}
              {g.id === recommended ? " · this machine" : ""}
            </Button>
          ))}
        </div>

        {guide ? (
          <ol className="space-y-4">
            {guide.steps.map((step, i) => (
              <li key={step.title} className="grid grid-cols-[1.75rem_minmax(0,1fr)] gap-2">
                <span className="flex h-6 w-6 items-center justify-center rounded-full border text-xs font-medium tabular-nums">
                  {i + 1}
                </span>
                <div className="min-w-0 space-y-1.5">
                  <p className="text-sm font-medium">{step.title}</p>
                  <CommandBlock command={step.command} />
                </div>
              </li>
            ))}
          </ol>
        ) : null}

        <div className="flex flex-wrap items-center gap-3 border-t pt-4">
          <Button type="button" onClick={onCheck} disabled={checking}>
            <RefreshCw className={cn("mr-1.5 h-4 w-4", checking && "animate-spin")} aria-hidden />
            {checking ? "Checking…" : "Check again"}
          </Button>
          {caps.docs_url ? (
            <a
              href={caps.docs_url}
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex items-center gap-1 text-sm text-muted-foreground underline-offset-4 hover:underline"
            >
              Other systems: Cloudflare&apos;s download page
              <ExternalLink className="h-3.5 w-3.5" aria-hidden />
            </a>
          ) : null}
        </div>
      </CardContent>
    </Card>
  )
}

function TunnelControl({ caps, status }: { caps: TunnelCapabilities; status: TunnelStatus | undefined }) {
  const queryClient = useQueryClient()
  const [busy, setBusy] = useState(false)
  const [actionError, setActionError] = useState<string | null>(null)

  const state: TunnelState = status?.state ?? "idle"
  const meta = STATE_META[state] ?? STATE_META.idle
  const up = state === "starting" || state === "running"

  const act = async (action: () => Promise<TunnelStatus>) => {
    setBusy(true)
    setActionError(null)
    try {
      queryClient.setQueryData(["tunnel-status"], await action())
    } catch (err) {
      setActionError(err instanceof Error ? err.message : String(err))
      void queryClient.invalidateQueries({ queryKey: ["tunnel-capabilities"] })
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card>
      <CardHeader className="gap-2">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <CardTitle className="flex items-center gap-2">
            Sharing
            <Badge variant={meta.badge} className="gap-1.5">
              <Circle className={cn("h-2 w-2 fill-current", meta.dotClass)} aria-hidden />
              {meta.label}
            </Badge>
            {state === "running" && status?.started_at ? <Uptime since={status.started_at} /> : null}
          </CardTitle>
          {up ? (
            <Button type="button" variant="outline" disabled={busy} onClick={() => void act(stopTunnel)}>
              {busy ? "Stopping…" : "Stop sharing"}
            </Button>
          ) : (
            <Button type="button" disabled={busy || state === "stopping"} onClick={() => void act(startTunnel)}>
              {busy ? "Starting…" : "Start the tunnel"}
            </Button>
          )}
        </div>
        {caps.binary?.version ? (
          <CardDescription className="font-mono text-xs">{caps.binary.version}</CardDescription>
        ) : null}
      </CardHeader>
      <CardContent className="space-y-4">
        {status?.share_url && status.portal_url ? (
          <div className="space-y-3">
            <LinkRow
              label="Client portal"
              hint="What to send a client: progress and deliveries, read-only."
              url={status.portal_url}
            />
            <LinkRow
              label="Full dashboard"
              hint="Everything you see here. Only for yourself or your team."
              url={status.share_url}
            />
          </div>
        ) : up && status?.url ? (
          <p className="text-sm text-muted-foreground">
            This tunnel ({status.url}) was started by an earlier dashboard
            process, so there is no link with a token for it. Stop it and start
            it again to get one.
          </p>
        ) : up ? (
          <p className="text-sm text-muted-foreground">
            Waiting for Cloudflare to assign an address — usually a few seconds.
          </p>
        ) : (
          <p className="text-sm text-muted-foreground">
            Starting the tunnel gives you two links: one for a client, one for
            the full dashboard.
          </p>
        )}

        {actionError ? (
          <Alert variant="destructive">
            <AlertTriangle className="h-4 w-4" />
            <AlertTitle>The tunnel did not {up ? "stop" : "start"}</AlertTitle>
            <AlertDescription className="font-mono text-xs">{actionError}</AlertDescription>
          </Alert>
        ) : null}
        {!actionError && status?.last_error ? (
          <Alert variant="destructive">
            <AlertTriangle className="h-4 w-4" />
            <AlertTitle>cloudflared reported a problem</AlertTitle>
            <AlertDescription className="font-mono text-xs">{status.last_error}</AlertDescription>
          </Alert>
        ) : null}
      </CardContent>
    </Card>
  )
}

function Limits() {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Good to know</CardTitle>
      </CardHeader>
      <CardContent>
        <ul className="list-disc space-y-1.5 pl-5 text-sm text-muted-foreground">
          <li>The address changes every time the tunnel starts, and so does the token: send the new link.</li>
          <li>Anyone with the link can open it until you stop sharing. Stopping revokes it.</li>
          <li>
            Through the tunnel the dashboard refreshes every 15 seconds instead
            of live: Cloudflare quick tunnels do not carry live streams.
          </li>
          <li>
            Quick tunnels are meant for sharing and testing — Cloudflare limits
            them to 200 requests at a time and gives no uptime guarantee.
          </li>
        </ul>
      </CardContent>
    </Card>
  )
}

function LinkRow({ label, hint, url }: { label: string; hint: string; url: string }) {
  return (
    <div className="rounded-md border px-3 py-2">
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-sm font-medium">{label}</span>
        <span className="text-xs text-muted-foreground">{hint}</span>
      </div>
      <div className="mt-1.5 flex items-center gap-2">
        <a
          href={url}
          target="_blank"
          rel="noopener noreferrer"
          className="flex min-w-0 items-center gap-1.5 font-mono text-xs text-foreground hover:underline"
        >
          <ExternalLink className="h-3.5 w-3.5 shrink-0" aria-hidden />
          <span className="truncate">{url}</span>
        </a>
        <CopyButton text={url} label={`Copy the ${label.toLowerCase()} link`} />
      </div>
    </div>
  )
}

function CommandBlock({ command }: { command: string }) {
  return (
    <div className="flex items-start gap-2 rounded-md border bg-muted/40 px-3 py-2">
      <pre className="min-w-0 flex-1 overflow-x-auto whitespace-pre font-mono text-xs leading-5">{command}</pre>
      <CopyButton text={command} label="Copy the command" />
    </div>
  )
}

function CopyButton({ text, label }: { text: string; label: string }) {
  const [copied, setCopied] = useState(false)
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text)
      setCopied(true)
      window.setTimeout(() => setCopied(false), 1500)
    } catch {
      // Clipboard refused (permission / non-secure context): the text is
      // still on screen for a manual copy.
    }
  }
  return (
    <Button type="button" size="sm" variant="outline" onClick={() => void copy()} aria-label={label} className="ml-auto shrink-0 gap-1.5">
      <Copy className="h-3.5 w-3.5" aria-hidden />
      {copied ? "Copied" : "Copy"}
    </Button>
  )
}

function Uptime({ since }: { since: string }) {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), 30_000)
    return () => window.clearInterval(id)
  }, [])
  return <span className="text-xs font-normal text-muted-foreground">up {formatUptime(Date.parse(since), now)}</span>
}

/** "< 1 min", "12 min", "1 h 5 min". */
function formatUptime(startMs: number, nowMs: number): string {
  const minutes = Math.floor(Math.max(0, nowMs - startMs) / 60_000)
  if (!Number.isFinite(minutes) || minutes < 1) return "< 1 min"
  const h = Math.floor(minutes / 60)
  const m = minutes % 60
  return h > 0 ? `${h} h ${m} min` : `${m} min`
}
