import { AlertTriangle, Calendar, CheckCircle2, Circle } from "lucide-react"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Progress } from "@/components/ui/progress"
import { Skeleton } from "@/components/ui/skeleton"
import { useMilestones } from "@/hooks/useMilestones"
import { useProjectConfig } from "@/hooks/useProjectConfig"
import { labelForStatus } from "@/lib/status"

export function MilestonesPage() {
  const { data: milestones, isLoading, isError, error } = useMilestones()
  const { data: config } = useProjectConfig()
  const statusLabels = config?.presentation?.status_labels as
    | Record<string, string>
    | undefined

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
            </CardContent>
          </Card>
        ))}
      </div>
    </div>
  )
}
