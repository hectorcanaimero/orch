/**
 * Graph page — visual DAG of the task dependency graph.
 *
 * Mermaid is bundled and loaded on demand (its own chunk), so the graph works
 * offline and costs nothing until this page opens. Node colors are read from
 * the theme tokens at render time, so the diagram belongs to the page in both
 * themes. Supports zoom (buttons + mouse wheel) and pan (drag). Operator-only.
 */
import { useCallback, useEffect, useRef, useState } from "react"
import { AlertTriangle, Maximize2, Minimize2, RotateCcw, ZoomIn, ZoomOut } from "lucide-react"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { EmptyTasks } from "@/components/EmptyTasks"
import { Explain } from "@/components/Explain"
import { useFullscreen } from "@/hooks/useFullscreen"
import { useGraph } from "@/hooks/useGraph"
import { t } from "@/i18n"
import { criticalPathMatters, STATUS, statusKey, type StatusKey } from "@/lib/status"
import { useIsDark } from "@/lib/theme"
import type { GraphEdge, GraphNode } from "@/lib/types"

// ---- Mermaid DSL builder ---------------------------------------------------

function mermaidLabel(label: string): string {
  return `"${label.replace(/"/g, "'").replace(/\n/g, " ").slice(0, 80)}"`
}

// Shape repeats the status for readers who can't separate the colors.
function shapeOf(key: StatusKey): [string, string] {
  switch (key) {
    case "done":
      return ["([", "])"]
    case "in_progress":
      return ["[/", "/]"]
    case "blocked":
      return ["{{", "}}"]
    default:
      return ["[", "]"]
  }
}

function safeId(id: string): string {
  return id.replace(/[^A-Za-z0-9_]/g, "_")
}

/** Mix two #rrggbb colors; `amount` of `b` over `a`. Mermaid only takes literal colors. */
function mix(a: string, b: string, amount: number): string {
  const parse = (hex: string) => [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16))
  const [ca, cb] = [parse(a), parse(b)]
  if (ca.some(Number.isNaN) || cb.some(Number.isNaN)) return a
  return `#${ca.map((v, i) => Math.round(v + (cb[i] - v) * amount).toString(16).padStart(2, "0")).join("")}`
}

function token(name: string): string {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim()
}

function buildMermaidSource(nodes: GraphNode[], edges: GraphEdge[]): string {
  if (nodes.length === 0) return "flowchart TD\n  empty([No tasks])"

  const card = token("--card")
  const foreground = token("--foreground")
  const lines: string[] = ["flowchart TD"]
  const keyById = new Map<string, StatusKey>()

  for (const n of nodes) {
    const key = statusKey(n.status)
    keyById.set(n.id, key)
    const [open, close] = shapeOf(key)
    lines.push(`  ${safeId(n.id)}${open}${mermaidLabel(n.label)}${close}`)
  }
  for (const e of edges) lines.push(`  ${safeId(e.source)} --> ${safeId(e.target)}`)

  lines.push("")
  const cssVar: Record<StatusKey, string> = {
    backlog: "--status-queued",
    todo: "--status-queued",
    in_progress: "--status-running",
    blocked: "--status-blocked",
    failed: "--status-failed",
    done: "--status-done",
  }
  for (const key of Object.keys(cssVar) as StatusKey[]) {
    const color = token(cssVar[key])
    lines.push(`  classDef ${key} fill:${mix(card, color, 0.16)},stroke:${color},stroke-width:1.5px,color:${foreground}`)
  }

  const byStatus = new Map<StatusKey, string[]>()
  for (const n of nodes) {
    const key = keyById.get(n.id) ?? "backlog"
    byStatus.set(key, [...(byStatus.get(key) ?? []), safeId(n.id)])
  }
  for (const [key, ids] of byStatus) lines.push(`  class ${ids.join(",")} ${key}`)

  // The critical path is a trace along its edges, drawn only when the project
  // has branches; in a single chain it would mark every edge.
  if (criticalPathMatters(nodes)) {
    const critical = new Set(nodes.filter((n) => n.on_critical_path).map((n) => n.id))
    edges.forEach((e, i) => {
      if (critical.has(e.source) && critical.has(e.target)) {
        lines.push(`  linkStyle ${i} stroke:${foreground},stroke-width:2.5px`)
      }
    })
  }

  return lines.join("\n")
}

// ---- Zoom / pan hook -------------------------------------------------------

const ZOOM_MIN = 0.25
const ZOOM_MAX = 3
const ZOOM_STEP = 0.2

function useZoomPan() {
  const [zoom, setZoom] = useState(1)
  const [pan, setPan] = useState({ x: 0, y: 0 })
  const dragging = useRef(false)
  const lastPos = useRef({ x: 0, y: 0 })

  const clampZoom = (z: number) => Math.min(ZOOM_MAX, Math.max(ZOOM_MIN, z))

  const zoomIn = () => setZoom((z) => clampZoom(+(z + ZOOM_STEP).toFixed(2)))
  const zoomOut = () => setZoom((z) => clampZoom(+(z - ZOOM_STEP).toFixed(2)))
  const reset = useCallback(() => {
    setZoom(1)
    setPan({ x: 0, y: 0 })
  }, [])

  const onWheel = useCallback((e: WheelEvent) => {
    e.preventDefault()
    const delta = e.deltaY > 0 ? -ZOOM_STEP : ZOOM_STEP
    setZoom((z) => clampZoom(+(z + delta).toFixed(2)))
  }, [])

  const onMouseDown = useCallback((e: MouseEvent) => {
    if (e.button !== 0) return
    dragging.current = true
    lastPos.current = { x: e.clientX, y: e.clientY }
  }, [])

  const onMouseMove = useCallback((e: MouseEvent) => {
    if (!dragging.current) return
    const dx = e.clientX - lastPos.current.x
    const dy = e.clientY - lastPos.current.y
    lastPos.current = { x: e.clientX, y: e.clientY }
    setPan((p) => ({ x: p.x + dx, y: p.y + dy }))
  }, [])

  const onMouseUp = useCallback(() => {
    dragging.current = false
  }, [])

  return { zoom, pan, zoomIn, zoomOut, reset, onWheel, onMouseDown, onMouseMove, onMouseUp }
}

// ---- Component -------------------------------------------------------------

const LEGEND: StatusKey[] = ["done", "in_progress", "blocked", "todo"]

export function GraphPage() {
  const { data, isLoading, isError, error } = useGraph()
  const containerRef = useRef<HTMLDivElement>(null)
  const viewportRef = useRef<HTMLDivElement>(null)
  const graphRef = useRef<HTMLDivElement>(null)
  const [renderError, setRenderError] = useState<string | null>(null)
  const [isFullscreen, toggleFullscreen] = useFullscreen(graphRef)
  const isDark = useIsDark()

  const { zoom, pan, zoomIn, zoomOut, reset, onWheel, onMouseDown, onMouseMove, onMouseUp } = useZoomPan()

  // Attach wheel + drag to the viewport
  useEffect(() => {
    const el = viewportRef.current
    if (!el) return
    el.addEventListener("wheel", onWheel, { passive: false })
    el.addEventListener("mousedown", onMouseDown)
    window.addEventListener("mousemove", onMouseMove)
    window.addEventListener("mouseup", onMouseUp)
    return () => {
      el.removeEventListener("wheel", onWheel)
      el.removeEventListener("mousedown", onMouseDown)
      window.removeEventListener("mousemove", onMouseMove)
      window.removeEventListener("mouseup", onMouseUp)
    }
  }, [onWheel, onMouseDown, onMouseMove, onMouseUp, data])

  // Render whenever the data or the theme changes.
  useEffect(() => {
    if (!data || data.nodes.length === 0) return
    let cancelled = false
    void import("mermaid")
      .then(async ({ default: mermaid }) => {
        mermaid.initialize({
          startOnLoad: false,
          theme: "base",
          themeVariables: {
            fontFamily: "Geist Variable, ui-sans-serif, system-ui, sans-serif",
            lineColor: token("--muted-foreground"),
            background: token("--card"),
          },
          flowchart: { curve: "basis", useMaxWidth: false },
        })
        const { svg } = await mermaid.render(`orch-graph-${Date.now()}`, buildMermaidSource(data.nodes, data.edges))
        if (cancelled || !containerRef.current) return
        containerRef.current.innerHTML = svg
        setRenderError(null)
        reset()
      })
      .catch((e: Error) => {
        if (!cancelled) setRenderError(t("graph.render_failed", { message: e.message }))
      })
    return () => {
      cancelled = true
    }
  }, [data, isDark, reset])

  const nodeCount = data?.nodes.length ?? 0
  const edgeCount = data?.edges.length ?? 0
  const showCritical = data ? criticalPathMatters(data.nodes) : false

  return (
    <div className="flex h-full flex-col gap-4">
      <header className="flex flex-shrink-0 flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold tracking-[-0.02em]">{t("graph.title")}</h1>
          <p className="mt-1 text-sm text-muted-foreground tabular-nums">
            {isLoading ? t("graph.loading") : data ? t("graph.counts", { tasks: nodeCount, edges: edgeCount }) : ""}
          </p>
        </div>
        <div className="flex items-center gap-2">
          <div className="flex items-center gap-0.5 rounded-lg border bg-card p-0.5">
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={zoomOut}
              aria-label={t("graph.zoom_out")}
              className="h-8 w-8 p-0"
              disabled={!data || isLoading}
            >
              <ZoomOut className="h-4 w-4" />
            </Button>
            <button
              type="button"
              onClick={reset}
              className="h-8 min-w-[48px] rounded-md px-2 font-mono text-xs tabular-nums text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
              aria-label={t("graph.reset_zoom")}
              title={t("graph.reset_zoom")}
            >
              {Math.round(zoom * 100)}%
            </button>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={zoomIn}
              aria-label={t("graph.zoom_in")}
              className="h-8 w-8 p-0"
              disabled={!data || isLoading}
            >
              <ZoomIn className="h-4 w-4" />
            </Button>
          </div>
          <Button
            type="button"
            variant="outline"
            size="icon"
            onClick={reset}
            title={t("graph.reset_view")}
            aria-label={t("graph.reset_view")}
            disabled={!data || isLoading}
          >
            <RotateCcw className="h-4 w-4" />
          </Button>
          <Button
            type="button"
            variant="outline"
            size="icon"
            onClick={toggleFullscreen}
            title={isFullscreen ? t("common.exit_fullscreen") : t("common.fullscreen")}
            aria-label={isFullscreen ? t("common.exit_fullscreen") : t("common.fullscreen")}
          >
            {isFullscreen ? <Minimize2 className="h-4 w-4" /> : <Maximize2 className="h-4 w-4" />}
          </Button>
        </div>
      </header>

      {isLoading ? (
        <Skeleton className="h-96 w-full rounded-xl" />
      ) : isError ? (
        <Alert variant="destructive">
          <AlertTriangle className="h-4 w-4" />
          <AlertTitle>{t("graph.load_failed")}</AlertTitle>
          <AlertDescription>{error?.message ?? t("common.unknown_error")}</AlertDescription>
        </Alert>
      ) : renderError ? (
        <Alert variant="destructive">
          <AlertTriangle className="h-4 w-4" />
          <AlertTitle>{t("graph.render_error")}</AlertTitle>
          <AlertDescription>{renderError}</AlertDescription>
        </Alert>
      ) : nodeCount === 0 ? (
        <EmptyTasks />
      ) : (
        <div
          ref={graphRef}
          className="relative flex min-h-[420px] flex-1 flex-col overflow-hidden rounded-xl border bg-card"
        >
          {/* Legend sits above the drawing, never on top of a node. */}
          <div className="flex flex-wrap items-center justify-between gap-x-6 gap-y-2 border-b px-4 py-2.5 text-xs">
            <div className="flex flex-wrap items-center gap-x-4 gap-y-1.5">
              {LEGEND.map((key) => {
                const meta = STATUS[key]
                return (
                  <span key={key} className="inline-flex items-center gap-1.5 text-muted-foreground">
                    <meta.icon className={`h-3.5 w-3.5 ${meta.text}`} aria-hidden />
                    {t(meta.label)}
                  </span>
                )
              })}
              {showCritical ? (
                <span className="inline-flex items-center gap-1.5 text-muted-foreground">
                  <span className="inline-block h-[2.5px] w-5 rounded-full bg-foreground" aria-hidden />
                  <Explain term="critical_path" />
                </span>
              ) : null}
            </div>
            <span className="text-muted-foreground">{t("graph.hint")}</span>
          </div>

          <div
            ref={viewportRef}
            className="min-h-0 w-full flex-1 cursor-grab select-none overflow-hidden p-4 active:cursor-grabbing"
          >
            <div
              ref={containerRef}
              className="origin-top-left [&_svg]:h-auto [&_svg]:max-w-none"
              style={{
                transform: `translate(${pan.x}px, ${pan.y}px) scale(${zoom})`,
                transformOrigin: "top left",
                transition: "transform 0.05s ease-out",
              }}
            />
          </div>
        </div>
      )}
    </div>
  )
}
