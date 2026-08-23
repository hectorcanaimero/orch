/**
 * Graph page — visual DAG of the task dependency graph.
 *
 * Uses Mermaid.js loaded via CDN (no npm install) to render a `flowchart TD`
 * diagram from the `/api/graph` endpoint.  This endpoint is operator-only;
 * stakeholders land on a 403 before they can navigate here.
 *
 * Node colour mapping (follows dashboard palette):
 *   done        → green
 *   in_progress → blue
 *   blocked     → amber
 *   todo        → slate
 *   backlog     → zinc (default)
 *
 * Critical-path nodes get a thick red border via Mermaid `style` directives.
 */
import { useEffect, useRef, useState } from "react"
import { AlertTriangle, Maximize2, Minimize2 } from "lucide-react"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { useFullscreen } from "@/hooks/useFullscreen"
import { useGraph } from "@/hooks/useGraph"
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

/** Escape a Mermaid node label — wrap in quotes, escaping inner quotes. */
function mermaidLabel(label: string): string {
  return `"${label.replace(/"/g, "'").replace(/\n/g, " ").slice(0, 80)}"`
}

/** Map task status → Mermaid node shape character pair. */
function shapeOf(status: string): [string, string] {
  switch (status) {
    case "done":
      return ["([", "])"] // stadium → green
    case "in_progress":
      return ["[/", "/]"] // parallelogram → blue
    case "blocked":
      return ["{{", "}}"] // hexagon → amber
    default:
      return ["[", "]"] // rectangle → zinc / slate
  }
}

/** Mermaid classDef fill/stroke palette. */
const CLASS_DEFS = `
  classDef done       fill:#16a34a,stroke:#14532d,color:#fff
  classDef in_progress fill:#2563eb,stroke:#1e3a8a,color:#fff
  classDef blocked    fill:#d97706,stroke:#92400e,color:#fff
  classDef todo       fill:#475569,stroke:#1e293b,color:#fff
  classDef backlog    fill:#3f3f46,stroke:#27272a,color:#fff
  classDef critical   stroke:#ef4444,stroke-width:3px
`.trim()

function buildMermaidSource(
  nodes: GraphNode[],
  edges: GraphEdge[],
): string {
  if (nodes.length === 0) return "flowchart TD\n  empty([No tasks])"

  const lines: string[] = ["flowchart TD"]

  // Node declarations — include phase label and shaped by status
  for (const n of nodes) {
    const [open, close] = shapeOf(n.status)
    const safeId = n.id.replace(/-/g, "_")
    const label = `Ph${n.phase}: ${n.label}`
    lines.push(`  ${safeId}${open}${mermaidLabel(label)}${close}`)
  }

  // Edges
  for (const e of edges) {
    const src = e.source.replace(/-/g, "_")
    const tgt = e.target.replace(/-/g, "_")
    lines.push(`  ${src} --> ${tgt}`)
  }

  // classDef declarations
  lines.push("")
  lines.push(CLASS_DEFS)

  // class assignments — status + critical path
  const byStatus: Record<string, string[]> = {}
  const criticalIds: string[] = []

  for (const n of nodes) {
    const cls =
      n.status === "in_progress"
        ? "in_progress"
        : n.status === "done"
          ? "done"
          : n.status === "blocked"
            ? "blocked"
            : n.status === "todo"
              ? "todo"
              : "backlog"

    if (!byStatus[cls]) byStatus[cls] = []
    byStatus[cls].push(n.id.replace(/-/g, "_"))

    if (n.on_critical_path) {
      criticalIds.push(n.id.replace(/-/g, "_"))
    }
  }

  for (const [cls, ids] of Object.entries(byStatus)) {
    lines.push(`  class ${ids.join(",")} ${cls}`)
  }
  if (criticalIds.length > 0) {
    // Critical path nodes get the border class ON TOP of the status class.
    // Mermaid 10+ supports multiple class assignments per node via comma list.
    lines.push(`  class ${criticalIds.join(",")} critical`)
  }

  return lines.join("\n")
}

// ---- Component -------------------------------------------------------------

export function GraphPage() {
  const { data, isLoading, isError, error } = useGraph()
  const containerRef = useRef<HTMLDivElement>(null)
  const graphRef = useRef<HTMLDivElement>(null)
  const [renderError, setRenderError] = useState<string | null>(null)
  const [mermaidReady, setMermaidReady] = useState(false)
  const [isFullscreen, toggleFullscreen] = useFullscreen(graphRef)

  // Load Mermaid from CDN once
  useEffect(() => {
    loadMermaid()
      .then(() => setMermaidReady(true))
      .catch((e: Error) => setRenderError(e.message))
  }, [])

  // Render the diagram whenever data or Mermaid readiness changes
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
        }
      })
      .catch((e: Error) => {
        setRenderError(`Diagram render failed: ${e.message}`)
      })
  }, [mermaidReady, data])

  const nodeCount = data?.nodes.length ?? 0
  const edgeCount = data?.edges.length ?? 0
  const criticalCount = data?.nodes.filter((n) => n.on_critical_path).length ?? 0

  return (
    <div className="flex flex-col gap-4 h-full">
      <header className="flex flex-shrink-0 flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">
            Dependency Graph
          </h1>
          <p className="text-sm text-muted-foreground">
            {isLoading
              ? "Loading graph…"
              : data
                ? `${nodeCount} tasks · ${edgeCount} edges · ${criticalCount} on critical path`
                : ""}
          </p>
        </div>
        <Button
          type="button"
          variant="outline"
          size="icon"
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
      </header>

      {isLoading ? (
        <Skeleton className="h-96 w-full rounded-lg" />
      ) : isError ? (
        <Alert variant="destructive">
          <AlertTriangle className="h-4 w-4" />
          <AlertTitle>Failed to load graph</AlertTitle>
          <AlertDescription>
            {error?.message ?? "Unknown error"}
          </AlertDescription>
        </Alert>
      ) : renderError ? (
        <Alert variant="destructive">
          <AlertTriangle className="h-4 w-4" />
          <AlertTitle>Diagram render error</AlertTitle>
          <AlertDescription>{renderError}</AlertDescription>
        </Alert>
      ) : nodeCount === 0 ? (
        <div className="flex-shrink-0 rounded-lg border border-dashed bg-white p-10 text-center">
          <h2 className="text-base font-medium">No tasks yet</h2>
          <p className="mt-1 text-sm text-muted-foreground">
            Run{" "}
            <code className="rounded bg-zinc-100 px-1 py-0.5 text-xs">
              orch atomize
            </code>{" "}
            or{" "}
            <code className="rounded bg-zinc-100 px-1 py-0.5 text-xs">
              orch init
            </code>{" "}
            to seed your project.
          </p>
        </div>
      ) : (
        <div
          ref={graphRef}
          className="flex-1 overflow-auto rounded-lg border border-zinc-800 bg-zinc-950 p-4"
        >
          {/* Legend */}
          <div className="mb-4 flex flex-wrap gap-3 text-xs">
            {[
              { label: "Done", cls: "bg-green-600" },
              { label: "In progress", cls: "bg-blue-600" },
              { label: "Blocked", cls: "bg-amber-600" },
              { label: "Todo / Backlog", cls: "bg-zinc-600" },
            ].map(({ label, cls }) => (
              <span key={label} className="flex items-center gap-1.5">
                <span
                  className={`inline-block h-3 w-3 rounded-sm ${cls}`}
                  aria-hidden
                />
                <span className="text-zinc-300">{label}</span>
              </span>
            ))}
            <span className="flex items-center gap-1.5">
              <span
                className="inline-block h-3 w-3 rounded-sm border-2 border-red-500 bg-transparent"
                aria-hidden
              />
              <span className="text-zinc-300">Critical path</span>
            </span>
          </div>

          {/* Mermaid SVG target */}
          <div
            ref={containerRef}
            className="mermaid-output [&_svg]:max-w-none [&_svg]:h-auto"
          />
        </div>
      )}
    </div>
  )
}
