import { useRef } from "react"
import {
  AlertTriangle,
  Calendar,
  CheckCircle2,
  Circle,
  Download,
  Clock,
} from "lucide-react"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Progress } from "@/components/ui/progress"
import { Skeleton } from "@/components/ui/skeleton"
import { GanttChart } from "@/components/charts/GanttChart"
import { useMilestones } from "@/hooks/useMilestones"
import { useProjectConfig } from "@/hooks/useProjectConfig"
import { labelForStatus } from "@/lib/status"

/** Serialize the rendered <svg> and trigger a download. No library. */
function downloadSvg(svg: SVGSVGElement | null): void {
  if (!svg) return
  const blob = new Blob([new XMLSerializer().serializeToString(svg)], {
    type: "image/svg+xml",
  })
  const url = URL.createObjectURL(blob)
  const a = document.createElement("a")
  a.href = url
  a.download = "milestones-timeline.svg"
  a.click()
  URL.revokeObjectURL(url)
}

export function MilestonesPage() {
  const { data: milestones, isLoading, isError, error } = useMilestones()
  const { data: config } = useProjectConfig()
  const statusLabels = config?.presentation?.status_labels
  const svgRef = useRef<SVGSVGElement>(null)
  // The ONE place a real clock is read — flows into the deterministic chart.
  const today = new Date().toISOString().slice(0, 10)

  if (isLoading) {
    return (
      <div className="space-y-4">
        <h1 className="text-2xl font-semibold tracking-tight">Milestones</h1>
        {[1, 2, 3].map((i) => (
          <Skeleton key={i} className="h-32 w-full" />
        ))}
      </div>
    )
  }

  if (isError) {
    return (
      <Alert variant="destructive">
        <AlertTriangle className="h-4 w-4" />
        <AlertTitle>Failed to load milestones</AlertTitle>
        <AlertDescription>{(error as Error)?.message}</AlertDescription>
      </Alert>
    )
  }

  if (!milestones || milestones.length === 0) {
    return (
      <div className="space-y-4">
        <h1 className="text-2xl font-semibold tracking-tight">Milestones</h1>
        <Card>
          <CardHeader>
            <CardTitle>No milestones yet</CardTitle>
          </CardHeader>
          <CardContent className="text-sm text-muted-foreground">
            Create milestones with{" "}
            <code className="font-mono">orch task set --milestone M1</code> and
            assign tasks to them.
          </CardContent>
        </Card>
      </div>
    )
  }

  return (
    <div className="space-y-6">
      <h1 className="text-2xl font-semibold tracking-tight">Milestones</h1>

      <details open className="rounded-lg border bg-card">
        <summary className="cursor-pointer select-none px-4 py-3 text-sm font-medium">
          Timeline
        </summary>
        <div className="border-t p-4">
          <div className="mb-2 flex justify-end">
            <Button
              variant="outline"
              size="sm"
              onClick={() => downloadSvg(svgRef.current)}
            >
              <Download className="mr-1.5 h-3.5 w-3.5" />
              Download SVG
            </Button>
          </div>
          <GanttChart ref={svgRef} milestones={milestones} today={today} />
        </div>
      </details>

      <div className="grid gap-4 md:grid-cols-2">
        {milestones.map((m) => (
          <Card key={m.id} className="flex flex-col">
            <CardHeader className="flex flex-row items-start justify-between gap-2 pb-2">
              <div>
                <CardTitle className="text-base">{m.title}</CardTitle>
                {m.description && (
                  <p className="mt-1 text-sm text-muted-foreground">
                    {m.description}
                  </p>
                )}
              </div>
              <Badge
                variant={m.status === "completed" ? "success" : "outline"}
                className="shrink-0"
              >
                {m.status === "completed" ? (
                  <CheckCircle2 className="mr-1 h-3 w-3" />
                ) : (
                  <Circle className="mr-1 h-3 w-3" />
                )}
                {labelForStatus(m.status, statusLabels)}
              </Badge>
            </CardHeader>
            <CardContent className="flex flex-col gap-3">
              <div className="space-y-1">
                <div className="flex items-center justify-between text-sm">
                  <span className="text-muted-foreground">
                    {m.progress.done} / {m.progress.total} tasks
                  </span>
                  <span className="font-medium">{m.progress.pct}%</span>
                </div>
                <Progress value={m.progress.pct} className="h-2" />
              </div>
              {m.target_date && (
                <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
                  <Calendar className="h-3.5 w-3.5" />
                  <span>Target: {m.target_date}</span>
                </div>
              )}
              {m.eta && (
                <div
                  className={
                    "flex items-center gap-1.5 text-xs " +
                    (m.eta.confidence === "high"
                      ? "text-emerald-600 dark:text-emerald-400"
                      : "text-amber-600 dark:text-amber-400")
                  }
                >
                  <Clock className="h-3.5 w-3.5" />
                  <span>ETA: {m.eta.eta_date}</span>
                </div>
              )}
            </CardContent>
          </Card>
        ))}
      </div>
    </div>
  )
}
