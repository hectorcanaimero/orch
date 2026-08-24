/**
 * Graph page — visual DAG of the task dependency graph.
 *
 * Uses Mermaid.js loaded via CDN to render a `flowchart TD` diagram.
 * Supports zoom (buttons + mouse wheel) and pan (drag). Operator-only.
 */
import { useCallback, useEffect, useRef, useState } from "react"
import { AlertTriangle, Maximize2, Minimize2, ZoomIn, ZoomOut, RotateCcw } from "lucide-react"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { useFullscreen } from "@/hooks/useFullscreen"
import { useGraph } from "@/hooks/useGraph"
import { cn } from "@/lib/utils"
import type { GraphEdge, GraphNode } from "@/lib/types"

// ---- Mermaid CDN loader ----------------------------------------------------

const MERMAID_CDN =
  "https://cdn.jsdelivr.net/npm/mermaid@11/dist/mermaid.min.js"

function loadMermaid(): Promise<void> {
  return new Promise((resolve, reject) => {
    if (
      typeof window !== "undefined" &&
      (window as unknown as Record<string, unknown>)["mermaid"]
    ) {
      resolve()
      return
    }
    const existing = document.querySelector(
      `script[src="${MERMAID_CDN}"]`,
    ) as HTMLScriptElement | null
    if (existing) {
      existing.addEventListener("load", () => resolve())
      existing.addEventListener("error", () =>
        reject(new Error("Mermaid CDN failed to load")),
      )
      return
    }
    const script = document.createElement("script")
    script.src = MERMAID_CDN
    script.async = true
    script.onload = () => resolve()
    script.onerror = () => reject(new Error("Mermaid CDN failed to load"))
    document.head.appendChild(script)
  })
}

// ---- Mermaid DSL builder ---------------------------------------------------

function mermaidLabel(label: string): string {
  return `"${label.replace(/"/g, "'").replace(/\n/g, " ").slice(0, 80)}"`
}

function shapeOf(status: string): [string, string] {
  switch (status) {
    case "done":        return ["([", "])"]
    case "in-progress": return ["[/", "/]"]
    case "blocked":     return ["{{", "}}"]
    default:            return ["[", "]"]
  }
}

const CLASS_DEFS = `
  classDef done        fill:#16a34a,stroke:#14532d,color:#fff
  classDef in_progress fill:#2563eb,stroke:#1e3a8a,color:#fff
  classDef blocked     fill:#d97706,stroke:#92400e,color:#fff
  classDef todo        fill:#475569,stroke:#1e293b,color:#fff
  classDef backlog     fill:#3f3f46,stroke:#27272a,color:#fff
  classDef critical    stroke:#ef4444,stroke-width:3px
`.trim()

function buildMermaidSource(nodes: GraphNode[], edges: GraphEdge[]): string {
  if (nodes.length === 0) return "flowchart TD\n  empty([No tasks])"

  const lines: string[] = ["flowchart TD"]

  for (const n of nodes) {
    const [open, close] = shapeOf(n.status)
    const safeId = n.id.replace(/-/g, "_")
    const label = `Ph${n.phase}: ${n.label}`
    lines.push(`  ${safeId}${open}${mermaidLabel(label)}${close}`)
  }

  for (const e of edges) {
    const src = e.source.replace(/-/g, "_")
    const tgt = e.target.replace(/-/g, "_")
    lines.push(`  ${src} --> ${tgt}`)
  }

  lines.push("")
  lines.push(CLASS_DEFS)

  const byStatus: Record<string, string[]> = {}
  const criticalIds: string[] = []

  for (const n of nodes) {
    const cls =
      n.status === "in-progress" ? "in_progress"
      : n.status === "done"     ? "done"
      : n.status === "blocked"  ? "blocked"
      : n.status === "todo"     ? "todo"
      : "backlog"

    if (!byStatus[cls]) byStatus[cls] = []
    byStatus[cls].push(n.id.replace(/-/g, "_"))
    if (n.on_critical_path) criticalIds.push(n.id.replace(/-/g, "_"))
  }

  for (const [cls, ids] of Object.entries(byStatus)) {
    lines.push(`  class ${ids.join(",")} ${cls}`)
  }
  if (criticalIds.length > 0) {
    lines.push(`  class ${criticalIds.join(",")} critical`)
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

  const clampZoom = (z: number) =>
    Math.min(ZOOM_MAX, Math.max(ZOOM_MIN, z))

  const zoomIn  = () => setZoom((z) => clampZoom(+(z + ZOOM_STEP).toFixed(2)))
  const zoomOut = () => setZoom((z) => clampZoom(+(z - ZOOM_STEP).toFixed(2)))
  const reset   = () => { setZoom(1); setPan({ x: 0, y: 0 }) }

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

  const onMouseUp = useCallback(() => { dragging.current = false }, [])

  return { zoom, pan, zoomIn, zoomOut, reset, onWheel, onMouseDown, onMouseMove, onMouseUp }
}

// ---- Component -------------------------------------------------------------

export function GraphPage() {
  const { data, isLoading, isError, error } = useGraph()
  const containerRef = useRef<HTMLDivElement>(null)
  const viewportRef  = useRef<HTMLDivElement>(null)
  const graphRef     = useRef<HTMLDivElement>(null)
  const [renderError, setRenderError] = useState<string | null>(null)
  const [mermaidReady, setMermaidReady] = useState(false)
  const [isFullscreen, toggleFullscreen] = useFullscreen(graphRef)

  const { zoom, pan, zoomIn, zoomOut, reset, onWheel, onMouseDown, onMouseMove, onMouseUp } =
    useZoomPan()

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
  }, [onWheel, onMouseDown, onMouseMove, onMouseUp])

  // Load Mermaid from CDN once
  useEffect(() => {
    loadMermaid()
      .then(() => setMermaidReady(true))
      .catch((e: Error) => setRenderError(e.message))
  }, [])

  // Render diagram whenever data or Mermaid readiness changes
  useEffect(() => {
    if (!mermaidReady || !data || !containerRef.current) return

    const mermaid = (window as unknown as Record<string, unknown>)[
      "mermaid"
    ] as {
      initialize: (cfg: Record<string, unknown>) => void
      render: (id: string, def: string) => Promise<{ svg: string }>
    }

    mermaid.initialize({
      startOnLoad: false,
      theme: "dark",
      securityLevel: "loose",
      flowchart: { curve: "basis", useMaxWidth: false },
    })

    const def = buildMermaidSource(data.nodes, data.edges)
    const uid = `orch-graph-${Date.now()}`

    mermaid
      .render(uid, def)
      .then(({ svg }) => {
        if (containerRef.current) {
          containerRef.current.innerHTML = svg
          setRenderError(null)
          // Auto-fit: reset pan and set zoom so SVG fits the viewport
          reset()
        }
      })
      .catch((e: Error) => {
        setRenderError(`Diagram render failed: ${e.message}`)
      })
  }, [mermaidReady, data])

  const nodeCount    = data?.nodes.length ?? 0
  const edgeCount    = data?.edges.length ?? 0
  const criticalCount = data?.nodes.filter((n) => n.on_critical_path).length ?? 0

  return (
    <div className="flex flex-col gap-4 h-full">
      <header className="flex flex-shrink-0 flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Dependency Graph</h1>
          <p className="text-sm text-muted-foreground">
            {isLoading
              ? "Loading graph…"
              : data
                ? `${nodeCount} tasks · ${edgeCount} edges · ${criticalCount} on critical path`
                : ""}
          </p>
        </div>
        <div className="flex items-center gap-2">
          {/* Zoom controls */}
          <div className="flex items-center rounded-md border border-zinc-200 bg-zinc-50 p-0.5 gap-0.5">
            <Button
              type="button" variant="ghost" size="sm"
              onClick={zoomOut}
              aria-label="Zoom out"
              className="h-8 w-8 p-0"
              disabled={!data || isLoading}
            >
              <ZoomOut className="h-4 w-4" />
            </Button>
            <button
              type="button"
              onClick={reset}
              className="h-8 min-w-[44px] px-2 text-xs font-mono text-zinc-600 hover:text-zinc-900 hover:bg-zinc-100 rounded transition-colors"
              aria-label="Reset zoom"
              title="Reset zoom and pan"
            >
              {Math.round(zoom * 100)}%
            </button>
            <Button
              type="button" variant="ghost" size="sm"
              onClick={zoomIn}
              aria-label="Zoom in"
              className="h-8 w-8 p-0"
              disabled={!data || isLoading}
            >
              <ZoomIn className="h-4 w-4" />
            </Button>
          </div>
          <Button
            type="button" variant="outline" size="icon"
            onClick={reset}
            title="Reset view"
            aria-label="Reset view"
            disabled={!data || isLoading}
          >
            <RotateCcw className="h-4 w-4" />
          </Button>
          <Button
            type="button" variant="outline" size="icon"
            onClick={toggleFullscreen}
            title={isFullscreen ? "Exit fullscreen" : "Enter fullscreen"}
            aria-label={isFullscreen ? "Exit fullscreen" : "Enter fullscreen"}
          >
            {isFullscreen ? (
              <Minimize2 className="h-4 w-4" />
            ) : (
              <Maximize2 className="h-4 w-4" />
            )}
          </Button>
        </div>
      </header>

      {isLoading ? (
        <Skeleton className="h-96 w-full rounded-lg" />
      ) : isError ? (
        <Alert variant="destructive">
          <AlertTriangle className="h-4 w-4" />
          <AlertTitle>Failed to load graph</AlertTitle>
          <AlertDescription>{error?.message ?? "Unknown error"}</AlertDescription>
        </Alert>
      ) : renderError ? (
        <Alert variant="destructive">
          <AlertTriangle className="h-4 w-4" />
          <AlertTitle>Diagram render error</AlertTitle>
          <AlertDescription>{renderError}</AlertDescription>
        </Alert>
      ) : nodeCount === 0 ? (
        <div className="flex-shrink-0 rounded-lg border border-dashed bg-zinc-50 p-10 text-center">
          <h2 className="text-base font-medium">No tasks yet</h2>
          <p className="mt-1 text-sm text-muted-foreground">
            Run{" "}
            <code className="rounded bg-zinc-100 px-1 py-0.5 text-xs">orch atomize</code>
            {" "}or{" "}
            <code className="rounded bg-zinc-100 px-1 py-0.5 text-xs">orch init</code>
            {" "}to seed your project.
          </p>
        </div>
      ) : (
        <div
          ref={graphRef}
          className="flex-1 min-h-0 rounded-lg border border-zinc-800 bg-zinc-950 overflow-hidden relative"
        >
          {/* Legend */}
          <div className="absolute top-3 left-3 z-10 flex flex-wrap gap-2.5 text-xs bg-zinc-900/80 backdrop-blur-sm px-3 py-2 rounded-md border border-zinc-800">
            {[
              { label: "Done",       cls: "bg-green-600" },
              { label: "In progress", cls: "bg-blue-600" },
              { label: "Blocked",    cls: "bg-amber-600" },
              { label: "Todo/Backlog", cls: "bg-zinc-500" },
            ].map(({ label, cls }) => (
              <span key={label} className="flex items-center gap-1.5">
                <span className={cn("inline-block h-2.5 w-2.5 rounded-sm", cls)} aria-hidden />
                <span className="text-zinc-300">{label}</span>
              </span>
            ))}
            <span className="flex items-center gap-1.5">
              <span className="inline-block h-2.5 w-2.5 rounded-sm border-2 border-red-500" aria-hidden />
              <span className="text-zinc-300">Critical</span>
            </span>
          </div>

          {/* Hint */}
          <div className="absolute bottom-3 right-3 z-10 text-xs text-zinc-500">
            Scroll to zoom · Drag to pan
          </div>

          {/* Zoomable / pannable viewport */}
          <div
            ref={viewportRef}
            className="w-full h-full overflow-hidden cursor-grab active:cursor-grabbing select-none"
          >
            <div
              ref={containerRef}
              className="mermaid-output origin-top-left [&_svg]:max-w-none [&_svg]:h-auto"
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
