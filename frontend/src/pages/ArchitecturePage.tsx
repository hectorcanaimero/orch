import { useEffect, useMemo, useRef, useState } from "react"
import {
  AlertTriangle,
  Loader2,
  Maximize2,
  Minimize2,
  Network,
  RefreshCcw,
} from "lucide-react"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { useArchitectureHistory } from "@/hooks/useArchitectureHistory"
import { useArchitectureStatus } from "@/hooks/useArchitectureStatus"
import { useFullscreen } from "@/hooks/useFullscreen"
import { useRegenerateArchitecture } from "@/hooks/useRegenerateArchitecture"
import {
  ArchitectureAlreadyGeneratingError,
  buildArchitectureIframeUrl,
} from "@/lib/api"
import { cn } from "@/lib/utils"
import type { ArchitectureSnapshot } from "@/lib/types"

const CURRENT_KEY = "__current__"

const PHASE_LABELS: Record<string, string> = {
  dispatching: "starting up",
  claude_working: "claude is generating the diagram",
  finalizing: "archiving the result",
}

function formatElapsed(seconds: number): string {
  if (seconds < 60) return `${seconds}s`
  const mins = Math.floor(seconds / 60)
  const secs = seconds % 60
  return `${mins}m ${secs.toString().padStart(2, "0")}s`
}

const usdFormatter = new Intl.NumberFormat("en-US", {
  style: "currency",
  currency: "USD",
  minimumFractionDigits: 2,
  maximumFractionDigits: 2,
})

const relativeFormatter = new Intl.RelativeTimeFormat("en", { numeric: "auto" })

const snapshotFormatter = new Intl.DateTimeFormat("en-US", {
  month: "short",
  day: "numeric",
  hour: "2-digit",
  minute: "2-digit",
  hour12: false,
})

/**
 * Coarse relative-time helper — enough for a "generated 2h ago" caption. We
 * pick the largest bucket that fits so the label stays readable without
 * pulling in a full date library.
 */
function formatRelative(iso: string): string {
  const then = new Date(iso).getTime()
  if (Number.isNaN(then)) return iso
  const diffMs = then - Date.now()
  const absSec = Math.abs(diffMs) / 1000
  if (absSec < 60) return relativeFormatter.format(Math.round(diffMs / 1000), "second")
  if (absSec < 3600) return relativeFormatter.format(Math.round(diffMs / 60_000), "minute")
  if (absSec < 86_400) return relativeFormatter.format(Math.round(diffMs / 3_600_000), "hour")
  return relativeFormatter.format(Math.round(diffMs / 86_400_000), "day")
}

function shortHash(hash: string | null): string {
  if (!hash) return "—"
  return hash.slice(0, 7)
}

function snapshotLabel(snap: ArchitectureSnapshot): string {
  const ts = new Date(snap.timestamp)
  const when = Number.isNaN(ts.getTime())
    ? snap.timestamp
    : snapshotFormatter.format(ts)
  const tasks = snap.source_artifacts?.task_count ?? 0
  return `${when} — ${tasks} tasks · ${usdFormatter.format(snap.cost_usd ?? 0)}`
}

export function ArchitecturePage() {
  const status = useArchitectureStatus()
  const history = useArchitectureHistory()
  const regen = useRegenerateArchitecture()

  const [selected, setSelected] = useState<string>(CURRENT_KEY)
  const viewerRef = useRef<HTMLDivElement>(null)
  const [isFullscreen, toggleFullscreen] = useFullscreen(viewerRef)

  const iframeSrc = useMemo(() => {
    if (selected === CURRENT_KEY) return buildArchitectureIframeUrl("current")
    return buildArchitectureIframeUrl({ snapshot: selected })
  }, [selected])

  // Re-mount the iframe when a fresh generation finishes so the browser drops
  // any cached response for `/api/architecture/current`. `generated_at` is
  // the cheapest cache-buster we already track.
  const iframeKey = useMemo(() => {
    if (selected !== CURRENT_KEY) return `snap-${selected}`
    return `current-${status.data?.generated_at ?? "none"}`
  }, [selected, status.data?.generated_at])

  const regenerating = status.data?.regenerate_in_progress ?? false
  const alreadyGenerating =
    regen.error instanceof ArchitectureAlreadyGeneratingError

  // Bridge the gap between the 202 response and the first status poll that
  // reports `regenerate_in_progress: true`. Subprocess startup + our poll
  // interval leaves a small window where the UI would otherwise look idle.
  // Once the poll confirms the run, we reset the mutation so `busy` collapses
  // back to `regenerating` alone.
  useEffect(() => {
    if (regenerating && regen.isSuccess) regen.reset()
  }, [regenerating, regen])

  // Safety net: if the subprocess crashes fast (or finishes so quickly the
  // poll never catches `regenerate_in_progress: true`), the bridge above
  // never fires and the button stays "busy" forever. Force a reset 15s
  // after mutation success if we still haven't observed a live run.
  useEffect(() => {
    if (!regen.isSuccess || regenerating) return
    const t = setTimeout(() => regen.reset(), 15_000)
    return () => clearTimeout(t)
  }, [regen.isSuccess, regenerating, regen])

  const busy = regenerating || regen.isSuccess
  const disableRegenerate = busy || regen.isPending

  // Local 1s ticker so the elapsed counter feels alive between polls. Off
  // when nothing's running to avoid pointless renders.
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (!busy) return
    const t = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(t)
  }, [busy])

  const startedMs = status.data?.started_at
    ? new Date(status.data.started_at).getTime()
    : null
  const elapsedS =
    startedMs !== null && !Number.isNaN(startedMs)
      ? Math.max(0, Math.floor((now - startedMs) / 1000))
      : null
  const phaseLabel = status.data?.phase
    ? (PHASE_LABELS[status.data.phase] ?? status.data.phase)
    : null

  const handleRegenerate = () => {
    if (disableRegenerate) return
    regen.mutate()
  }

  if (status.isLoading) {
    return (
      <div className="space-y-6">
        <Skeleton className="h-8 w-72" />
        <Skeleton className="h-12 w-full" />
        <Skeleton className="h-[60vh] w-full" />
      </div>
    )
  }

  if (status.isError) {
    return (
      <Alert variant="destructive">
        <AlertTriangle className="h-4 w-4" />
        <AlertTitle>Failed to load architecture status</AlertTitle>
        <AlertDescription>
          {status.error?.message ?? "Unknown error"}
        </AlertDescription>
      </Alert>
    )
  }

  const data = status.data
  if (!data) return null

  // Empty state — no diagram has been generated yet.
  if (!data.exists) {
    return (
      <div className="space-y-6">
        <header className="flex items-center gap-3">
          <Network className="h-5 w-5 text-muted-foreground" />
          <h1 className="text-2xl font-semibold tracking-tight">Architecture</h1>
        </header>

        <Card>
          <CardHeader>
            <CardTitle>No architecture generated yet</CardTitle>
            <CardDescription>
              The first generation reads your PRDs, specs and tasks and
              dispatches Claude via archify to produce an explorable HTML
              diagram. It may take a couple of minutes.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            {regen.isError && !alreadyGenerating ? (
              <Alert variant="destructive">
                <AlertTriangle className="h-4 w-4" />
                <AlertTitle>Regeneration failed</AlertTitle>
                <AlertDescription>
                  {regen.error?.message ?? "Unknown error"}
                </AlertDescription>
              </Alert>
            ) : null}
            <div>
              <Button
                type="button"
                onClick={handleRegenerate}
                disabled={disableRegenerate}
              >
                {regen.isPending || busy ? (
                  <>
                    <Loader2 className="h-4 w-4 animate-spin" />
                    Generating…
                  </>
                ) : (
                  <>
                    <RefreshCcw className="h-4 w-4" />
                    Generate architecture
                  </>
                )}
              </Button>
              {busy && !regenerating ? (
                <p className="text-xs text-muted-foreground">
                  Regeneration requested — waiting for it to start (subprocess
                  is booting).
                </p>
              ) : null}
            </div>
          </CardContent>
        </Card>
      </div>
    )
  }

  const snapshots = history.data?.snapshots ?? []
  const generatedRelative = data.generated_at
    ? formatRelative(data.generated_at)
    : "—"

  return (
    // Full-viewport column: header stays intrinsic, iframe takes the rest.
    // 4rem accounts for AppLayout's py-8 (2rem top + 2rem bottom).
    <div className="flex h-[calc(100vh-4rem)] flex-col gap-4">
      <header className="flex flex-shrink-0 flex-col gap-3">
        <div className="flex items-center gap-3">
          <Network className="h-5 w-5 text-muted-foreground" />
          <h1 className="text-2xl font-semibold tracking-tight">Architecture</h1>
          {status.isFetching ? (
            <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
          ) : null}
        </div>

        <div className="flex flex-wrap items-center gap-3">
          <div className="min-w-[280px] flex-1 sm:flex-none sm:w-[360px]">
            <Select value={selected} onValueChange={setSelected} disabled={history.isLoading}>
              <SelectTrigger aria-label="Architecture snapshot"><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem value={CURRENT_KEY}>Current</SelectItem>
                {snapshots.map((snap) => (
                  <SelectItem key={snap.timestamp} value={snap.timestamp}>
                    {snapshotLabel(snap)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <Button
            type="button"
            onClick={handleRegenerate}
            disabled={disableRegenerate}
            variant="outline"
          >
            {regen.isPending || busy ? (
              <>
                <Loader2 className="h-4 w-4 animate-spin" />
                Regenerating…
              </>
            ) : (
              <>
                <RefreshCcw className="h-4 w-4" />
                Regenerate
              </>
            )}
          </Button>
          <Button
            type="button"
            onClick={toggleFullscreen}
            variant="outline"
            title={isFullscreen ? "Exit fullscreen" : "Enter fullscreen"}
            aria-label={isFullscreen ? "Exit fullscreen" : "Enter fullscreen"}
          >
            {isFullscreen ? (
              <Minimize2 className="h-4 w-4" />
            ) : (
              <Maximize2 className="h-4 w-4" />
            )}
          </Button>
          <div className="text-xs text-muted-foreground">
            Last: {generatedRelative} · Source hash{" "}
            <span className="font-mono">{shortHash(data.source_hash)}</span> ·{" "}
            {snapshots.length} snapshot{snapshots.length === 1 ? "" : "s"}
          </div>
        </div>

        {busy ? (
          <div
            className={cn(
              "flex items-center gap-2 rounded-md border border-amber-500/40 bg-amber-500/10",
              "px-3 py-2 text-xs text-amber-800 dark:text-amber-200",
            )}
            role="status"
          >
            <Loader2 className="h-3.5 w-3.5 animate-spin" />
            {regenerating ? (
              <span>
                Regenerating architecture
                {phaseLabel ? (
                  <>
                    {" · "}
                    <span className="font-medium">{phaseLabel}</span>
                  </>
                ) : null}
                {elapsedS !== null ? (
                  <>
                    {" · "}
                    <span className="font-mono">{formatElapsed(elapsedS)}</span>
                    {" elapsed"}
                  </>
                ) : null}
                . The current diagram stays visible until the new one is ready.
              </span>
            ) : (
              "Regeneration requested — waiting for the subprocess to start."
            )}
          </div>
        ) : null}

        {alreadyGenerating ? (
          <div
            className="rounded-md border bg-muted px-3 py-2 text-xs text-muted-foreground"
            role="status"
          >
            A regeneration is already in progress — the view will refresh when
            it finishes.
          </div>
        ) : regen.isError ? (
          <Alert variant="destructive">
            <AlertTriangle className="h-4 w-4" />
            <AlertTitle>Regeneration failed</AlertTitle>
            <AlertDescription>
              {regen.error?.message ?? "Unknown error"}
            </AlertDescription>
          </Alert>
        ) : null}
      </header>

      {/* Iframe body — min-h-0 lets the flex child shrink so the iframe
          actually fills the remaining viewport height instead of overflowing.
          `viewerRef` is the fullscreen target — putting it here (not on the
          outer flex column) keeps the header out of the fullscreen surface. */}
      <div
        ref={viewerRef}
        className="relative flex min-h-0 flex-1 overflow-hidden rounded-md border bg-background"
      >
        <iframe
          key={iframeKey}
          src={iframeSrc}
          title="Architecture diagram"
          className="h-full w-full border-0"
          // The diagram is our own trusted output — no need for a sandbox
          // that would break inline styles/scripts inside the archify HTML.
        />
      </div>
    </div>
  )
}
