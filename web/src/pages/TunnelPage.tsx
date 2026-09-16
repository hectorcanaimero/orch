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
import { Explain } from "@/components/Explain"
import { t, type MessageKey } from "@/i18n"
import type { TunnelCapabilities, TunnelState, TunnelStatus } from "@/lib/types"

type BadgeVariant = "success" | "warning" | "danger" | "muted" | "info"

// The dot takes the badge's own status color, so the pair can never disagree.
const STATE_META: Record<TunnelState, { label: MessageKey; badge: BadgeVariant }> = {
  idle: { label: "tunnel.state.idle", badge: "muted" },
  starting: { label: "tunnel.state.starting", badge: "info" },
  running: { label: "tunnel.state.running", badge: "success" },
  stopping: { label: "tunnel.state.stopping", badge: "warning" },
  error: { label: "tunnel.state.error", badge: "danger" },
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
      <header className="space-y-1">
        <h1 className="text-2xl font-semibold tracking-[-0.02em]">{t("tunnel.title")}</h1>
        <p className="max-w-3xl text-sm text-muted-foreground">
          {t("tunnel.intro_before")} <Explain term="quick_tunnel">{t("tunnel.intro_term")}</Explain>
          {t("tunnel.intro_after")}
        </p>
      </header>

      {caps.isLoading ? <Skeleton className="h-40 w-full" /> : null}

      {caps.isError ? (
        <Alert variant="destructive">
          <AlertTriangle className="h-4 w-4" />
          <AlertTitle>{t("tunnel.caps_failed")}</AlertTitle>
          <AlertDescription>{caps.error?.message ?? t("common.unknown_error")}</AlertDescription>
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
            <CardTitle>{t("tunnel.disabled_title")}</CardTitle>
            <CardDescription>
              {t("tunnel.disabled_before")} <code className="font-mono text-xs">.orchestrator/config.yaml</code>
              {t("tunnel.disabled_middle")} <code className="font-mono text-xs">orch dashboard</code>:
            </CardDescription>
          </CardHeader>
          <CardContent>
            <CommandBlock command={"tunnel:\n  enabled: true"} />
          </CardContent>
        </Card>
      )
    case "profile_gate":
      return (
        <Notice title={t("tunnel.profile_title")}>
          {t("tunnel.profile_before")} (<code className="font-mono text-xs">orch dashboard</code>{" "}
          {t("tunnel.profile_without")} <code className="font-mono text-xs">--profile</code>).
        </Notice>
      )
    case "host_gate":
      return (
        <Notice title={t("tunnel.host_title")}>{t("tunnel.host_detail")}</Notice>
      )
    case "binary_missing":
      return <InstallGuide caps={caps} checking={checking} onCheck={onCheck} />
    default:
      return (
        <div className="space-y-6">
          {caps.config_blocker ? (
            <Alert className="border-status-blocked/40 bg-status-blocked/10 [&>svg]:text-status-blocked">
              <AlertTriangle className="h-4 w-4" />
              <AlertTitle>{t("tunnel.blocker_title")}</AlertTitle>
              <AlertDescription className="space-y-2">
                <p>
                  {t("tunnel.blocker_before")} <code className="font-mono text-xs">{caps.config_blocker}</code>{" "}
                  {t("tunnel.blocker_after")}
                </p>
                <CommandBlock command={`mv ${caps.config_blocker} ${caps.config_blocker}.bak`} />
                <Button type="button" size="sm" variant="outline" onClick={onCheck} disabled={checking}>
                  <RefreshCw className={cn("mr-1.5 h-3.5 w-3.5", checking && "animate-spin")} aria-hidden />
                  {t("tunnel.check_again")}
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
        <CardTitle>{t("tunnel.install_title")}</CardTitle>
        <CardDescription className="max-w-2xl">
          {t("tunnel.install_before")} <code className="font-mono text-xs">cloudflared</code>{" "}
          {t("tunnel.install_after")}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-5">
        <div className="flex flex-wrap gap-2" role="group" aria-label={t("tunnel.install_method")}>
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
              {g.id === recommended ? ` · ${t("tunnel.this_machine")}` : ""}
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
            {checking ? t("tunnel.checking") : t("tunnel.check_again")}
          </Button>
          {caps.docs_url ? (
            <a
              href={caps.docs_url}
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex items-center gap-1 text-sm text-muted-foreground underline-offset-4 hover:underline"
            >
              {t("tunnel.other_systems")}
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
            {t("tunnel.sharing")}
            <Badge variant={meta.badge} className="gap-1.5">
              <Circle className="h-2 w-2 fill-current" aria-hidden />
              {t(meta.label)}
            </Badge>
            {state === "running" && status?.started_at ? <Uptime since={status.started_at} /> : null}
          </CardTitle>
          {up ? (
            <Button type="button" variant="outline" disabled={busy} onClick={() => void act(stopTunnel)}>
              {busy ? t("tunnel.stopping") : t("tunnel.stop")}
            </Button>
          ) : (
            <Button type="button" disabled={busy || state === "stopping"} onClick={() => void act(startTunnel)}>
              {busy ? t("tunnel.starting") : t("tunnel.start")}
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
              label={t("tunnel.link.portal")}
              hint={t("tunnel.link.portal_hint")}
              copyLabel={t("tunnel.link.portal_copy")}
              url={status.portal_url}
            />
            <LinkRow
              label={t("tunnel.link.dashboard")}
              hint={t("tunnel.link.dashboard_hint")}
              copyLabel={t("tunnel.link.dashboard_copy")}
              url={status.share_url}
            />
          </div>
        ) : up && status?.url ? (
          <p className="text-sm text-muted-foreground">{t("tunnel.orphan", { url: status.url })}</p>
        ) : up ? (
          <p className="text-sm text-muted-foreground">{t("tunnel.waiting_address")}</p>
        ) : (
          <p className="text-sm text-muted-foreground">{t("tunnel.two_links")}</p>
        )}

        {actionError ? (
          <Alert variant="destructive">
            <AlertTriangle className="h-4 w-4" />
            <AlertTitle>{t(up ? "tunnel.did_not_stop" : "tunnel.did_not_start")}</AlertTitle>
            <AlertDescription className="font-mono text-xs">{actionError}</AlertDescription>
          </Alert>
        ) : null}
        {!actionError && status?.last_error ? (
          <Alert variant="destructive">
            <AlertTriangle className="h-4 w-4" />
            <AlertTitle>{t("tunnel.cloudflared_problem")}</AlertTitle>
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
        <CardTitle className="text-base">{t("tunnel.limits_title")}</CardTitle>
      </CardHeader>
      <CardContent>
        <ul className="list-disc space-y-1.5 pl-5 text-sm text-muted-foreground">
          {(["tunnel.limit.address", "tunnel.limit.revoke", "tunnel.limit.polling", "tunnel.limit.testing"] as const).map((key) => (
            <li key={key}>{t(key)}</li>
          ))}
        </ul>
      </CardContent>
    </Card>
  )
}

function LinkRow({ label, hint, copyLabel, url }: { label: string; hint: string; copyLabel: string; url: string }) {
  return (
    <div className="rounded-lg border px-3 py-2">
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
        <CopyButton text={url} label={copyLabel} />
      </div>
    </div>
  )
}

function CommandBlock({ command }: { command: string }) {
  return (
    <div className="flex items-start gap-2 rounded-lg border bg-muted/40 px-3 py-2">
      <pre className="min-w-0 flex-1 overflow-x-auto whitespace-pre font-mono text-xs leading-5">{command}</pre>
      <CopyButton text={command} label={t("tunnel.copy_command")} />
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
      {copied ? t("common.copied") : t("common.copy")}
    </Button>
  )
}

function Uptime({ since }: { since: string }) {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), 30_000)
    return () => window.clearInterval(id)
  }, [])
  return (
    <span className="text-xs font-normal text-muted-foreground tabular-nums">
      {t("tunnel.uptime", { duration: formatUptime(Date.parse(since), now) })}
    </span>
  )
}

/** "< 1 min", "12 min", "1 h 5 min". */
function formatUptime(startMs: number, nowMs: number): string {
  const minutes = Math.floor(Math.max(0, nowMs - startMs) / 60_000)
  if (!Number.isFinite(minutes) || minutes < 1) return t("tunnel.under_minute")
  const h = Math.floor(minutes / 60)
  const m = minutes % 60
  return h > 0 ? t("tunnel.hours_minutes", { h, m }) : t("tunnel.minutes", { m })
}
