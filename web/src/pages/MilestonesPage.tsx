import { describeLoadError } from "@/lib/errors"
import { AlertTriangle, CheckCircle2, Circle, Clock, CircleDot } from "lucide-react"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Progress } from "@/components/ui/progress"
import { Skeleton } from "@/components/ui/skeleton"
import { useMilestones, type Milestone } from "@/hooks/useMilestones"

const STATUS: Record<Milestone["status"], { label: string; variant: "success" | "info" | "outline"; icon: typeof Circle }> = {
  done: { label: "Done", variant: "success", icon: CheckCircle2 },
  active: { label: "In progress", variant: "info", icon: CircleDot },
  pending: { label: "Not started", variant: "outline", icon: Circle },
}

function Intro() {
  return (
    <div>
      <h1 className="text-2xl font-semibold tracking-tight">Milestones</h1>
      <p className="text-sm text-muted-foreground">
        Each milestone is a phase of tasks.json — the same progress your client sees in the portal.
      </p>
    </div>
  )
}

export function MilestonesPage() {
  const { data: milestones, isLoading, isError, error } = useMilestones()

  if (isLoading) {
    return (
      <div className="space-y-4">
        <Intro />
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
        <AlertDescription>{describeLoadError(error)}</AlertDescription>
      </Alert>
    )
  }

  if (!milestones || milestones.length === 0) {
    return (
      <div className="space-y-4">
        <Intro />
        <Card>
          <CardHeader>
            <CardTitle>No tasks yet</CardTitle>
          </CardHeader>
          <CardContent className="text-sm text-muted-foreground">
            Milestones appear once tasks.json has tasks. Write a spec and run{" "}
            <code className="font-mono">orch atomize --apply</code>.
          </CardContent>
        </Card>
      </div>
    )
  }

  return (
    <div className="space-y-6">
      <Intro />
      <div className="grid gap-4 md:grid-cols-2">
        {milestones.map((m) => {
          const status = STATUS[m.status]
          const Icon = status.icon
          return (
            <Card key={m.phase} className="flex flex-col">
              <CardHeader className="flex flex-row items-start justify-between gap-2 pb-2">
                <CardTitle className="text-base">{m.name}</CardTitle>
                <Badge variant={status.variant} className="shrink-0">
                  <Icon className="mr-1 h-3 w-3" />
                  {status.label}
                </Badge>
              </CardHeader>
              <CardContent className="flex flex-col gap-3">
                <div className="space-y-1">
                  <div className="flex items-center justify-between text-sm">
                    <span className="text-muted-foreground">
                      {m.progress.done} / {m.progress.total} tasks
                      {m.blocked > 0 && ` · ${m.blocked} blocked`}
                    </span>
                    <span className="font-medium">{m.progress.pct}%</span>
                  </div>
                  <Progress value={m.progress.pct} className="h-2" />
                </div>
                {m.status !== "done" && (
                  <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
                    <Clock className="h-3.5 w-3.5" />
                    <span>
                      {m.eta
                        ? `ETA ${m.eta.eta_date} (${m.eta.confidence} confidence) at the project's pace`
                        : "No ETA: nothing unblocked is left, or no task finished in the last 7 days"}
                    </span>
                  </div>
                )}
              </CardContent>
            </Card>
          )
        })}
      </div>
    </div>
  )
}
