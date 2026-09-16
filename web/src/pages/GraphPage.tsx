/**
 * Graph page — visual DAG of the task dependency graph.
 *
 * Laid out in-house (lib/graphLayout.ts) and drawn as SVG by React, so the
 * graph works offline, adds nothing heavy to the binary, and takes its colors
 * straight from the theme tokens (a theme switch needs no re-render). Supports
 * zoom (buttons + mouse wheel) and pan (drag). Operator-only.
 */
import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { AlertTriangle, Maximize2, Minimize2, RotateCcw, ZoomIn, ZoomOut } from "lucide-react"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { EmptyTasks } from "@/components/EmptyTasks"
import { Explain } from "@/components/Explain"
import { useFullscreen } from "@/hooks/useFullscreen"
import { useGraph } from "@/hooks/useGraph"
import { t } from "@/i18n"
import { layoutGraph, type LayoutNode } from "@/lib/graphLayout"
import { criticalPathMatters, STATUS, statusKey, type StatusKey } from "@/lib/status"

// ---- Drawing ---------------------------------------------------------------

const STATUS_VAR: Record<StatusKey, string> = {
  backlog: "var(--status-queued)",
  todo: "var(--status-queued)",
  in_progress: "var(--status-running)",
  blocked: "var(--status-blocked)",
  failed: "var(--status-failed)",
  done: "var(--status-done)",
}

const LINE_HEIGHT = 16

/** The outline repeats the status for readers who can't separate the colors. */
function NodeShape({ node, status }: { node: LayoutNode; status: StatusKey }) {
  const { x, y, width: w, height: h } = node
  const color = STATUS_VAR[status]
  const style = { fill: `color-mix(in oklab, ${color} 16%, var(--card))`, stroke: color, strokeWidth: 1.5 }
  const k = 12
  switch (status) {
    case "done":
      return <rect x={x} y={y} width={w} height={h} rx={h / 2} style={style} />
    case "in_progress":
      return <polygon points={`${x + k},${y} ${x + w},${y} ${x + w - k},${y + h} ${x},${y + h}`} style={style} />
    case "blocked":
      return (
        <polygon
          points={`${x + k},${y} ${x + w - k},${y} ${x + w},${y + h / 2} ${x + w - k},${y + h} ${x + k},${y + h} ${x},${y + h / 2}`}
          style={style}
        />
      )
    default:
      return <rect x={x} y={y} width={w} height={h} rx={6} style={style} />
  }
}

// ---- Zoom / pan hook -------------------------------------------------------

const ZOOM_MIN = 0.25
const ZOOM_MAX = 3
const ZOOM_STEP = 0.2

const clampZoom = (z: number) => Math.min(ZOOM_MAX, Math.max(ZOOM_MIN, z))

/**
 * `fit` is the zoom that shows the whole width of the drawing. Until the reader
 * zooms, the view follows it (a resized viewport refits); reset returns to it.
 */
function useZoomPan(fit: number) {
  const [userZoom, setUserZoom] = useState<number | null>(null)
  const zoom = userZoom ?? clampZoom(fit)
  const [pan, setPan] = useState({ x: 0, y: 0 })
  const dragging = useRef(false)
  const lastPos = useRef({ x: 0, y: 0 })

  const step = useCallback(
    (delta: number) => setUserZoom((z) => clampZoom(+((z ?? clampZoom(fit)) + delta).toFixed(2))),
    [fit],
  )
  const zoomIn = () => step(ZOOM_STEP)
  const zoomOut = () => step(-ZOOM_STEP)
  const reset = useCallback(() => {
    setUserZoom(null)
    setPan({ x: 0, y: 0 })
  }, [])

  const onWheel = useCallback(
    (e: WheelEvent) => {
      e.preventDefault()
      step(e.deltaY > 0 ? -ZOOM_STEP : ZOOM_STEP)
    },
    [step],
  )

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
  const viewportRef = useRef<HTMLDivElement>(null)
  const graphRef = useRef<HTMLDivElement>(null)
  const [isFullscreen, toggleFullscreen] = useFullscreen(graphRef)

  const layout = useMemo(() => (data ? layoutGraph(data.nodes, data.edges) : null), [data])
  const [viewportWidth, setViewportWidth] = useState(0)
  useEffect(() => {
    const el = viewportRef.current
    if (!el) return
    const observer = new ResizeObserver(([entry]) => setViewportWidth(Math.round(entry.contentRect.width)))
    observer.observe(el)
    return () => observer.disconnect()
  }, [layout])
  const fit = layout && viewportWidth ? Math.min(1, viewportWidth / (layout.width + 8)) : 1

  const { zoom, pan, zoomIn, zoomOut, reset, onWheel, onMouseDown, onMouseMove, onMouseUp } = useZoomPan(fit)

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

  const nodeCount = data?.nodes.length ?? 0
  const edgeCount = data?.edges.length ?? 0
  const showCritical = data ? criticalPathMatters(data.nodes) : false
  const nodeById = useMemo(() => new Map((data?.nodes ?? []).map((n) => [n.id, n])), [data])

  // The critical path is a trace along its edges, drawn only when the project
  // has branches; in a single chain it would mark every edge.
  const onCritical = (source: string, target: string) =>
    showCritical && Boolean(nodeById.get(source)?.on_critical_path && nodeById.get(target)?.on_critical_path)

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
      ) : nodeCount === 0 || !layout ? (
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
              style={{
                transform: `translate(${pan.x}px, ${pan.y}px) scale(${zoom})`,
                transformOrigin: "top left",
                transition: "transform 0.05s ease-out",
              }}
            >
              <svg
                width={layout.width + 8}
                height={layout.height + 8}
                viewBox={`-4 -4 ${layout.width + 8} ${layout.height + 8}`}
                role="img"
                aria-label={t("graph.title")}
                className="max-w-none font-sans"
              >
                <defs>
                  {(["muted-foreground", "foreground"] as const).map((color) => (
                    <marker
                      key={color}
                      id={`orch-graph-arrow-${color}`}
                      viewBox="0 0 10 10"
                      refX="9"
                      refY="5"
                      markerUnits="userSpaceOnUse"
                      markerWidth="8"
                      markerHeight="8"
                      orient="auto"
                    >
                      <path d="M0,0 L10,5 L0,10 z" style={{ fill: `var(--${color})` }} />
                    </marker>
                  ))}
                </defs>
                {layout.edges.map((e) => {
                  const critical = onCritical(e.source, e.target)
                  return (
                    <path
                      key={`${e.source}->${e.target}`}
                      d={e.d}
                      markerEnd={`url(#orch-graph-arrow-${critical ? "foreground" : "muted-foreground"})`}
                      style={{
                        fill: "none",
                        stroke: critical ? "var(--foreground)" : "var(--muted-foreground)",
                        strokeWidth: critical ? 2.5 : 1.25,
                        opacity: critical ? 1 : 0.7,
                      }}
                    />
                  )
                })}
                {layout.nodes.map((node) => {
                  const source = nodeById.get(node.id)
                  const status = statusKey(source?.status ?? "")
                  const top = node.y + node.height / 2 - ((node.lines.length - 1) * LINE_HEIGHT) / 2
                  return (
                    <g key={node.id}>
                      <title>{`${node.id} · ${source?.label ?? ""} · ${t(STATUS[status].label)}`}</title>
                      <NodeShape node={node} status={status} />
                      <text
                        x={node.x + node.width / 2}
                        textAnchor="middle"
                        dominantBaseline="central"
                        style={{ fill: "var(--foreground)", fontSize: 13 }}
                      >
                        {node.lines.map((line, i) => (
                          <tspan key={i} x={node.x + node.width / 2} y={top + i * LINE_HEIGHT}>
                            {line}
                          </tspan>
                        ))}
                      </text>
                    </g>
                  )
                })}
              </svg>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
